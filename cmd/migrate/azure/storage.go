package azure

// storage.go — blob + queue export from a storage account, all via `az storage`
// (the read-only allowlist permits list / download / peek, so no tool beyond az
// is needed). Blobs are downloaded from the user's own containers — the Functions
// runtime/deployment containers are skipped. Queue messages are PEEKED, never
// received, so the live queue is left completely intact (they're re-pushed at
// apply, never seeded). The connection string carries the account key and is
// redacted from all command logging (see az.go).

import (
	"encoding/json"
	"fmt"
	"os"
)

// systemContainers are Azure-managed (Functions runtime/deploy, diagnostics)
// containers — not the user's data, so the snapshot skips them. $web is the
// static website, captured as a Canvas site rather than a blob bucket.
var systemContainers = map[string]bool{
	"scm-releases": true, "function-releases": true,
	"azure-webjobs-hosts": true, "azure-webjobs-secrets": true,
	"azure-webjobs-eventhub": true,
	"$logs":                  true, "$root": true, "$web": true, "$blobchangefeed": true,
}

// storageAccountConn builds a connection string from the account's primary key
// (key access is a read-only `list`).
func (p azProvider) storageAccountConn(account string) (string, error) {
	var keys []struct {
		Value string `json:"value"`
	}
	if err := p.c.runJSON([]string{"storage", "account", "keys", "list", "-g", p.rg, "-n", account}, &keys); err != nil {
		return "", fmt.Errorf("reading storage keys for %s: %w", account, err)
	}
	if len(keys) == 0 || keys[0].Value == "" {
		return "", fmt.Errorf("no access key on storage account %s (key access disabled? pass the data another way)", account)
	}
	return fmt.Sprintf("DefaultEndpointsProtocol=https;AccountName=%s;AccountKey=%s;EndpointSuffix=core.windows.net", account, keys[0].Value), nil
}

// storageBlobs downloads every blob in the account's user containers, returning
// container → (blob key → bytes). System containers are skipped.
func (p azProvider) storageBlobs(account string) (map[string]map[string][]byte, error) {
	conn, err := p.storageAccountConn(account)
	if err != nil {
		return nil, err
	}
	var containers []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"storage", "container", "list", "--connection-string", conn}, &containers); err != nil {
		return nil, fmt.Errorf("listing containers on %s: %w", account, err)
	}
	out := map[string]map[string][]byte{}
	for _, c := range containers {
		if systemContainers[c.Name] {
			continue
		}
		var blobs []struct {
			Name string `json:"name"`
		}
		if err := p.c.runJSON([]string{"storage", "blob", "list", "-c", c.Name, "--connection-string", conn}, &blobs); err != nil {
			return nil, fmt.Errorf("listing blobs in %s/%s: %w", account, c.Name, err)
		}
		if len(blobs) == 0 {
			continue
		}
		bucket := map[string][]byte{}
		for _, b := range blobs {
			data, derr := p.downloadBlob(conn, c.Name, b.Name)
			if derr != nil {
				return nil, derr
			}
			bucket[b.Name] = data
		}
		out[c.Name] = bucket
	}
	return out, nil
}

// downloadBlob fetches one blob to a temp file (az writes binary; stdout can't
// carry it) and returns its bytes.
func (p azProvider) downloadBlob(conn, container, name string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "drift-az-blob-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()              // #nosec G104 -- az writes the file; we only need the path
	defer os.Remove(tmpPath) // #nosec G104
	if _, err := p.c.runRaw([]string{
		"storage", "blob", "download", "-c", container, "-n", name,
		"-f", tmpPath, "--connection-string", conn, "--no-progress", "--overwrite",
	}); err != nil {
		return nil, fmt.Errorf("downloading %s/%s: %w", container, name, err)
	}
	return os.ReadFile(tmpPath) // #nosec G304 -- our own temp file
}

// storageStaticSites captures a Storage static website: the special `$web`
// container holds the served content, which IS downloadable (unlike a Static Web
// App's build-pipeline content). Returns siteName→(path→bytes), empty when the
// account has no $web container. The site maps to a Canvas site, not a blob bucket.
func (p azProvider) storageStaticSites(account string) (map[string]map[string][]byte, error) {
	conn, err := p.storageAccountConn(account)
	if err != nil {
		return nil, err
	}
	var containers []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"storage", "container", "list", "--connection-string", conn}, &containers); err != nil {
		return nil, fmt.Errorf("listing containers on %s: %w", account, err)
	}
	hasWeb := false
	for _, c := range containers {
		if c.Name == "$web" {
			hasWeb = true
		}
	}
	if !hasWeb {
		return nil, nil
	}
	var blobs []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"storage", "blob", "list", "-c", "$web", "--connection-string", conn}, &blobs); err != nil {
		return nil, fmt.Errorf("listing $web on %s: %w", account, err)
	}
	if len(blobs) == 0 {
		return nil, nil
	}
	site := map[string][]byte{}
	for _, b := range blobs {
		data, derr := p.downloadBlob(conn, "$web", b.Name)
		if derr != nil {
			return nil, derr
		}
		site[b.Name] = data
	}
	return map[string]map[string][]byte{account: site}, nil
}

// storageQueues peeks every queue (never receives — the live queue is untouched),
// returning queue name → peeked messages (up to 32, the peek maximum).
func (p azProvider) storageQueues(account string) (map[string][]json.RawMessage, error) {
	conn, err := p.storageAccountConn(account)
	if err != nil {
		return nil, err
	}
	var queues []struct {
		Name string `json:"name"`
	}
	if err := p.c.runJSON([]string{"storage", "queue", "list", "--connection-string", conn}, &queues); err != nil {
		return nil, fmt.Errorf("listing queues on %s: %w", account, err)
	}
	out := map[string][]json.RawMessage{}
	for _, q := range queues {
		var msgs []json.RawMessage
		if err := p.c.runJSON([]string{
			"storage", "message", "peek", "--queue-name", q.Name,
			"--num-messages", "32", "--connection-string", conn,
		}, &msgs); err != nil {
			return nil, fmt.Errorf("peeking queue %s: %w", q.Name, err)
		}
		out[q.Name] = msgs
	}
	return out, nil
}
