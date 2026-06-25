package azure

// source_package.go — the deployment-package retrieval path, the one that works
// on Linux Consumption (the most common Function App plan) where the others
// don't. On Consumption the code is neither a package URL, a mounted SitePackage,
// nor reachable over the (stripped) Kudu wwwroot — `func`/zip-deploy stages it as
// `scm-releases/scm-latest-<app>.zip` in the app's linked storage account
// (AzureWebJobsStorage). On Linux that "zip" is actually a SquashFS image, so
// extraction is format-aware: a real zip is read in-process; a SquashFS is
// expanded via `unsquashfs` (refused cleanly, pointing at --source, when the tool
// isn't installed). This is the universal ground truth for Consumption apps.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sourceFromStoragePackage retrieves a Function App's code from the deployment
// package staged in its linked storage account. Works when run-from-package,
// SitePackages and the Kudu wwwroot all miss (the Linux Consumption default).
func (p azProvider) sourceFromStoragePackage(app string) (map[string][]byte, error) {
	conn, err := p.storageConnString(app)
	if err != nil {
		return nil, err
	}
	pkg, err := p.findDeploymentPackage(conn, app)
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp("", "drift-az-pkg-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()              // #nosec G104 -- az writes the file; we only need the path
	defer os.Remove(tmpPath) // #nosec G104
	if _, err := p.c.runRaw([]string{
		"storage", "blob", "download", "-c", "scm-releases", "-n", pkg,
		"-f", tmpPath, "--connection-string", conn, "--no-progress", "--overwrite",
	}); err != nil {
		return nil, fmt.Errorf("downloading %s: %w", pkg, err)
	}
	raw, err := os.ReadFile(tmpPath) // #nosec G304 -- our own temp file
	if err != nil {
		return nil, err
	}
	return extractPackage(raw)
}

// storageConnString reads the app's AzureWebJobsStorage connection string (the
// account that holds the deployment package). A managed-identity storage binding
// (no key) can't be used this way — refused cleanly, pointing at --source.
func (p azProvider) storageConnString(app string) (string, error) {
	var settings []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := p.c.runJSON([]string{"functionapp", "config", "appsettings", "list", "-g", p.rg, "-n", app}, &settings); err != nil {
		return "", fmt.Errorf("reading app settings: %w", err)
	}
	for _, s := range settings {
		if !strings.EqualFold(s.Name, "AzureWebJobsStorage") {
			continue
		}
		v := strings.TrimSpace(s.Value)
		if strings.Contains(v, "AccountKey=") {
			return v, nil
		}
		return "", fmt.Errorf("AzureWebJobsStorage has no account key (managed-identity storage?) — pass --source %s=<dir>", app)
	}
	return "", fmt.Errorf("no AzureWebJobsStorage connection string on %s — pass --source %s=<dir>", app, app)
}

// findDeploymentPackage locates the active package in scm-releases: the
// conventional scm-latest-<app>.zip, else the newest blob (lexical compare on the
// RFC3339 lastModified is correct for ISO timestamps).
func (p azProvider) findDeploymentPackage(conn, app string) (string, error) {
	var blobs []struct {
		Name       string `json:"name"`
		Properties struct {
			LastModified string `json:"lastModified"`
		} `json:"properties"`
	}
	if err := p.c.runJSON([]string{"storage", "blob", "list", "-c", "scm-releases", "--connection-string", conn}, &blobs); err != nil {
		return "", fmt.Errorf("listing scm-releases (app not zip-deployed, or no deployment container): %w", err)
	}
	want := "scm-latest-" + app + ".zip"
	newest, newestMod := "", ""
	for _, b := range blobs {
		if b.Name == want {
			return b.Name, nil
		}
		if b.Properties.LastModified > newestMod {
			newestMod, newest = b.Properties.LastModified, b.Name
		}
	}
	if newest == "" {
		return "", fmt.Errorf("scm-releases is empty (app not zip-deployed?)")
	}
	return newest, nil
}

// extractPackage turns a deployment package into the path→bytes tree the rest of
// the snapshot consumes, dispatching on the file's magic bytes: a zip is read in
// process; a SquashFS image (Linux Consumption) is expanded via unsquashfs.
func extractPackage(raw []byte) (map[string][]byte, error) {
	switch {
	case bytes.HasPrefix(raw, []byte("PK\x03\x04")), bytes.HasPrefix(raw, []byte("PK\x05\x06")):
		return unzipToTree(raw)
	case bytes.HasPrefix(raw, []byte("hsqs")): // SquashFS, little-endian
		return unsquashfsToTree(raw)
	default:
		// Some packages are plain zips with an unexpected prefix; try anyway.
		if tree, err := unzipToTree(raw); err == nil {
			return tree, nil
		}
		return nil, fmt.Errorf("deployment package is neither a zip nor a SquashFS image")
	}
}

// unsquashfsToTree expands a SquashFS image (how Linux Consumption stages the
// deployment) into the path→bytes tree. Shells out to `unsquashfs`; when it isn't
// installed, refuses cleanly and points at --source (the user has their code).
func unsquashfsToTree(raw []byte) (map[string][]byte, error) {
	if _, err := exec.LookPath("unsquashfs"); err != nil {
		return nil, fmt.Errorf("the deployment package is a SquashFS image (Linux Consumption) and `unsquashfs` is not installed — install squashfs-tools (brew install squashfs / apt-get install squashfs-tools), or pass --source <local-dir>")
	}
	img, err := os.CreateTemp("", "drift-az-sqfs-*.img")
	if err != nil {
		return nil, err
	}
	imgPath := img.Name()
	defer os.Remove(imgPath) // #nosec G104
	if _, err := img.Write(raw); err != nil {
		img.Close() // #nosec G104
		return nil, err
	}
	img.Close() // #nosec G104

	parent, err := os.MkdirTemp("", "drift-az-unsq-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent) // #nosec G104
	dest := filepath.Join(parent, "root")
	// -d <dest> (must not pre-exist), -n (no xattrs), then the image.
	if out, err := exec.Command("unsquashfs", "-d", dest, "-n", imgPath).CombinedOutput(); err != nil { // #nosec G204 -- imgPath is our own temp file
		return nil, fmt.Errorf("unsquashfs failed: %w\n%s", err, string(out))
	}
	return localFunctionSource(dest)
}
