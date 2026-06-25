package azure

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ondrift/cli/common"
)

// sourceProvider supplies the heavy artifacts snapshot can't get from plain
// `az` JSON: a Function App's source tree, and a Cosmos account's documents.
// The real implementation (azProvider) hits Kudu + Cosmos; tests inject a fake
// returning fixtures, so the whole pipeline runs offline.
type sourceProvider interface {
	// functionSource returns a Function App's wwwroot as path→bytes
	// (e.g. "OrdersGet/function.json", "OrdersGet/__init__.py").
	functionSource(app string) (map[string][]byte, error)
	// cosmosCollections returns a Cosmos (Mongo API) account's data as
	// collectionName→documents.
	cosmosCollections(account string) (map[string][]json.RawMessage, error)
	// storageBlobs returns a storage account's user-container blobs as
	// container→(blobKey→bytes). System/runtime containers are excluded.
	storageBlobs(account string) (map[string]map[string][]byte, error)
	// storageQueues returns a storage account's queues as name→peeked messages
	// (peeked, never received — the live queue is untouched).
	storageQueues(account string) (map[string][]json.RawMessage, error)
	// storageStaticSites returns a storage account's static website ($web) as
	// siteName→(path→bytes), empty when there is no static website.
	storageStaticSites(account string) (map[string]map[string][]byte, error)
}

// azProvider is the live sourceProvider. These adapters are the parts the
// design (§9) flags as needing a real tenant to calibrate — they're wired with
// clean refusal fallbacks so a blocked tenant degrades honestly, never lies.
type azProvider struct {
	c  azClient
	rg string
}

// functionSource retrieves a Function App's code live, trying the supported
// mechanisms in order and returning the first that succeeds:
//
//  1. run-from-package URL — WEBSITE_RUN_FROM_PACKAGE is a SAS URL to a package
//     blob; just `az` to read the setting + a plain GET. No Kudu, no VFS.
//  2. Kudu VFS data/SitePackages — WEBSITE_RUN_FROM_PACKAGE is "1" (mounted from
//     storage, no URL to GET): the Linux Consumption default. The platform keeps
//     the deployed zip under Kudu's virtual filesystem, which survives there even
//     though the /api/zip endpoint below does not. This is the path that unblocks
//     the common case the other two miss.
//  3. Kudu /api/zip wwwroot — the full live wwwroot as a zip; works on
//     Windows/Dedicated plans that ship a complete SCM site, 404s on Linux
//     Consumption (stripped Kudu).
//
// If all fail the caller refuses cleanly and points at --source (your own code).
func (p azProvider) functionSource(app string) (map[string][]byte, error) {
	attempts := []struct {
		name string
		fn   func(string) (map[string][]byte, error)
	}{
		{"run-from-package URL", p.sourceFromRunPackage},
		{"Kudu VFS data/SitePackages", p.sourceFromSitePackages},
		{"storage deployment package (scm-releases)", p.sourceFromStoragePackage},
		{"Kudu /api/zip wwwroot", p.sourceFromKudu},
	}
	var errs []string
	for _, a := range attempts {
		if tree, err := a.fn(app); err == nil {
			return tree, nil
		} else {
			errs = append(errs, a.name+": "+err.Error())
		}
	}
	return nil, fmt.Errorf("could not retrieve source (%s)", strings.Join(errs, "; "))
}

// sourceFromRunPackage downloads the package named by WEBSITE_RUN_FROM_PACKAGE.
// On Linux Consumption that setting is how code gets there in the first place,
// so this is the canonical, supported way to pull it back out.
func (p azProvider) sourceFromRunPackage(app string) (map[string][]byte, error) {
	var settings []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := p.c.runJSON([]string{"functionapp", "config", "appsettings", "list", "-g", p.rg, "-n", app}, &settings); err != nil {
		return nil, fmt.Errorf("reading app settings: %w", err)
	}
	var pkg string
	for _, s := range settings {
		if strings.EqualFold(s.Name, "WEBSITE_RUN_FROM_PACKAGE") {
			pkg = strings.TrimSpace(s.Value)
		}
	}
	if !strings.HasPrefix(strings.ToLower(pkg), "http") {
		// "1" means mounted-from-storage (no URL to GET); "" means not set.
		return nil, fmt.Errorf("WEBSITE_RUN_FROM_PACKAGE is %q, not a downloadable URL", pkg)
	}
	resp, err := http.Get(pkg) // a blob SAS URL — the token in the URL authorizes the read
	if err != nil {
		return nil, fmt.Errorf("downloading package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("package download returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, err
	}
	return unzipToTree(raw)
}

// scmHost is the Kudu/SCM base URL for a Function App. A package var so tests can
// point the Kudu adapters at an httptest server.
var scmHost = func(app string) string {
	return "https://" + app + ".scm.azurewebsites.net"
}

// kuduToken mints the short-lived ARM bearer the Kudu/SCM endpoints accept (the
// same token resource a `curl` against `*.scm.azurewebsites.net` uses).
func (p azProvider) kuduToken() (string, error) {
	var tok struct {
		AccessToken string `json:"accessToken"`
	}
	if err := p.c.runJSON([]string{"account", "get-access-token", "--resource", "https://management.azure.com"}, &tok); err != nil {
		return "", fmt.Errorf("could not mint a Kudu token: %w", err)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("az returned an empty access token")
	}
	return tok.AccessToken, nil
}

// kuduGET does an authenticated GET against a Kudu/SCM URL, returning the body
// (bounded by limit). A non-200 is an error carrying the status, so a caller can
// fall through to the next retrieval mechanism.
func kuduGET(url, token string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// sourceFromSitePackages retrieves the active deployment package on apps that run
// from a mounted package (WEBSITE_RUN_FROM_PACKAGE=1) — the Linux Consumption
// default, where the setting is "1" (no URL to GET) and /api/zip is stripped. The
// platform keeps the deployed zip under Kudu's virtual filesystem at
// data/SitePackages/<name>, the active <name> in data/SitePackages/packagename.txt.
// /api/vfs survives on Linux Consumption where /api/zip does not — this is what
// makes "we couldn't download the source" no longer true.
func (p azProvider) sourceFromSitePackages(app string) (map[string][]byte, error) {
	tok, err := p.kuduToken()
	if err != nil {
		return nil, err
	}
	base := scmHost(app) + "/api/vfs/data/SitePackages/"
	nameRaw, err := kuduGET(base+"packagename.txt", tok, 4<<10)
	if err != nil {
		return nil, fmt.Errorf("reading packagename.txt (no mounted package, or SCM disabled): %w", err)
	}
	pkg := strings.TrimSpace(string(nameRaw))
	if pkg == "" {
		return nil, fmt.Errorf("data/SitePackages/packagename.txt is empty (no active package)")
	}
	raw, err := kuduGET(base+pkg, tok, 256<<20)
	if err != nil {
		return nil, fmt.Errorf("downloading active package %q: %w", pkg, err)
	}
	return unzipToTree(raw)
}

// sourceFromKudu pulls site/wwwroot as a zip via the SCM/Kudu /api/zip endpoint.
func (p azProvider) sourceFromKudu(app string) (map[string][]byte, error) {
	tok, err := p.kuduToken()
	if err != nil {
		return nil, err
	}
	raw, err := kuduGET(scmHost(app)+"/api/zip/site/wwwroot/", tok, 64<<20)
	if err != nil {
		return nil, fmt.Errorf("could not reach Kudu /api/zip (Linux Consumption strips it; private endpoint / SCM disabled?): %w", err)
	}
	return unzipToTree(raw)
}

// unzipToTree turns a zip archive into the path→bytes tree the rest of the
// snapshot consumes. Build output Oryx adds (.python_packages/, node_modules/)
// rides along untouched — snapshotFunctions only reads folders containing a
// function.json, so the extra files are simply ignored downstream.
func unzipToTree(raw []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("payload was not a zip: %w", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out[f.Name] = b
	}
	return out, nil
}

// cosmosCollections lives in cosmos.go (live Mongo export via mongoexport).

func getSnapshotCmd() *cobra.Command {
	var rg, out, sliceName string
	var sources []string
	var derefSecrets, azLog bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Export an Azure resource group to a portable azure_export/ folder (read-only)",
		Long: "Reads a resource group and writes a vendor-neutral export: function source\n" +
			"(Python/Node, incl. Linux Consumption), Cosmos Mongo documents as JSONL, blob\n" +
			"containers, storage queues (peeked, never dequeued), $web static sites, and app\n" +
			"settings as secrets — plus a manifest with hashes and a REFUSED.md for anything\n" +
			"that won't move. Read-only: nothing on Azure is ever mutated.\n\n" +
			"Prerequisites, checked up front against your resource group: mongoexport (for a\n" +
			"Cosmos account) and squashfs-tools (for Linux Consumption source). A missing one\n" +
			"fails loudly with the install command — or pass --source <app>=<dir> to skip live\n" +
			"retrieval for an app you already have locally.",
		Example: "  drift migrate azure snapshot -g my-rg -o ./azure_export",
		RunE: func(_ *cobra.Command, _ []string) error {
			if strings.TrimSpace(rg) == "" {
				return fmt.Errorf("a resource group is required: -g <name>")
			}
			c := azRunner{logAll: azLog}
			acct, err := preflight(c)
			if err != nil {
				return err
			}
			srcMap, err := parseSourceMap(sources)
			if err != nil {
				return err
			}
			// Prerequisite tools are checked up front against what this RG actually
			// contains, so a missing tool fails loudly here rather than silently
			// refusing a resource mid-run.
			inv, err := discover(c, acct.Name, rg)
			if err != nil {
				return err
			}
			if err := requireSnapshotTools(inv, srcMap); err != nil {
				return err
			}
			m, err := runSnapshot(c, azProvider{c: c, rg: rg}, srcMap, acct.Name, rg, out, sliceName, derefSecrets)
			if err != nil {
				return err
			}
			fmt.Printf("\n%s  %d function(s) · %d collection(s) · %d blob container(s) · %d queue(s) · %d secret(s) · %d refused → %s\n",
				common.Check(), len(m.Functions), len(m.Collections), len(m.Blobs), len(m.Queues), len(m.Secrets), len(m.Refusals), common.Highlight(out))
			fmt.Printf("  next: %s\n", common.BoldText("drift migrate azure transform -i "+out+" -o ./drift_workspace"))
			return nil
		},
	}
	cmd.Flags().StringVarP(&rg, "resource-group", "g", "", "Azure resource group to snapshot (required)")
	cmd.Flags().StringVarP(&out, "out", "o", "./azure_export", "Output directory")
	cmd.Flags().StringVar(&sliceName, "slice", "", "Target Drift slice name (default: derived from the resource group)")
	cmd.Flags().StringArrayVar(&sources, "source", nil, "Use LOCAL source for a function app instead of pulling it live: --source <app-name>=<dir> (repeatable). Robust on any plan type — you already have your code.")
	cmd.Flags().BoolVar(&derefSecrets, "deref-secrets", false, "Capture secret VALUES (default: names only). Written 0600, plaintext on disk.")
	cmd.Flags().BoolVar(&azLog, "az-log", false, "Print every az command before it runs")
	return cmd
}

// runSnapshot is the testable export pipeline: discover → pull source/data →
// write azure_export/ + manifest + REFUSED.md. Pure given an azClient and a
// sourceProvider (real or fixture).
func runSnapshot(c azClient, sp sourceProvider, sourceMap map[string]string, sub, rg, outDir, sliceName string, derefSecrets bool) (Manifest, error) {
	// Always create the output dir up front. Otherwise a snapshot that exports
	// no files (everything refused) would never create it, and the manifest /
	// REFUSED.md writes — which is exactly where you'd SEE the refusals — fail.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Manifest{}, err
	}
	inv, err := discover(c, sub, rg)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{
		Version:       manifestVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Subscription:  sub,
		ResourceGroup: rg,
		SliceName:     sliceName,
	}

	for _, fa := range inv.FunctionApps {
		if fa.Class != Movable {
			if ref, ok := refusalForRuntime(fa.Name, fa.Runtime); ok {
				m.Refusals = append(m.Refusals, ref)
			}
			continue
		}
		var tree map[string][]byte
		var srcErr error
		if dir, ok := sourceMap[fa.Name]; ok {
			tree, srcErr = localFunctionSource(dir) // robust: read your own code
		} else {
			tree, srcErr = sp.functionSource(fa.Name) // live: run-from-package, then Kudu
		}
		if srcErr != nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, fa.Name, srcErr.Error(),
				"Pass --source " + fa.Name + "=<local-source-dir> (you have the code), or export it manually, then re-run."})
			continue
		}
		m.Functions = append(m.Functions, snapshotFunctions(outDir, fa, tree, &m)...)
		for _, s := range snapshotSecrets(c, rg, fa.Name, derefSecrets) {
			m.Secrets = appendSecret(m.Secrets, s)
		}
	}

	for _, cosmos := range inv.Cosmos {
		colls, err := sp.cosmosCollections(cosmos.Name)
		if err != nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, cosmos.Name, err.Error(), "Export the collections manually (mongoexport), then re-run."})
			continue
		}
		names := make([]string, 0, len(colls))
		for name := range colls {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			docs := colls[name]
			var buf bytes.Buffer
			for _, d := range docs {
				buf.Write(d)
				buf.WriteByte('\n')
			}
			rel := path.Join("backbone", "nosql", slugify(name)+".jsonl")
			if err := writeExport(outDir, rel, buf.Bytes()); err != nil {
				return m, err
			}
			m.Collections = append(m.Collections, ManifestCollection{
				Name: slugify(name), Account: cosmos.Name, DataPath: rel,
				DocCount: len(docs), SHA256: sha256hex(buf.Bytes()),
			})
		}
	}

	for _, st := range inv.Storage {
		// Blobs — every user container downloaded under blobs/<account>/<container>/.
		if byContainer, err := sp.storageBlobs(st.Name); err != nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, st.Name + " (blobs)", err.Error(),
				"Export the container(s) manually (az storage blob download-batch), then re-run."})
		} else {
			for _, container := range sortedKeys(byContainer) {
				blobs := byContainer[container]
				dataDir := path.Join("blobs", slugify(st.Name), slugify(container))
				var total int64
				for _, key := range sortedKeys(blobs) {
					if err := writeExport(outDir, path.Join(dataDir, key), blobs[key]); err != nil {
						return m, err
					}
					total += int64(len(blobs[key]))
				}
				m.Blobs = append(m.Blobs, ManifestBlob{
					Account: st.Name, Container: container, DataDir: dataDir,
					BlobCount: len(blobs), Bytes: total,
				})
			}
		}
		// Queues — peeked (never received), one JSONL per queue.
		if byQueue, err := sp.storageQueues(st.Name); err != nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, st.Name + " (queues)", err.Error(),
				"Peek the queue(s) manually, then re-run."})
		} else {
			for _, q := range sortedKeys(byQueue) {
				var buf bytes.Buffer
				for _, msg := range byQueue[q] {
					// `az -o json` pretty-prints, so each message spans many lines;
					// compact to one line so the file is valid JSONL (apply re-pushes
					// it line by line).
					var c bytes.Buffer
					if json.Compact(&c, msg) == nil {
						buf.Write(c.Bytes())
					} else {
						buf.Write(msg)
					}
					buf.WriteByte('\n')
				}
				rel := path.Join("queues", "storage", slugify(st.Name), slugify(q)+".jsonl")
				if err := writeExport(outDir, rel, buf.Bytes()); err != nil {
					return m, err
				}
				m.Queues = append(m.Queues, ManifestQueue{
					Account: st.Name, Name: q, DataPath: rel, MessageCount: len(byQueue[q]),
				})
			}
		}
		// Static website ($web) → a Canvas site (content is downloadable, unlike a
		// Static Web App's build-pipeline content).
		if sites, err := sp.storageStaticSites(st.Name); err != nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, st.Name + " ($web)", err.Error(),
				"Download the $web container manually, then add it under canvas/."})
		} else {
			for _, site := range sortedKeys(sites) {
				files := sites[site]
				dir := path.Join("canvas", slugify(site))
				for _, key := range sortedKeys(files) {
					if err := writeExport(outDir, path.Join(dir, key), files[key]); err != nil {
						return m, err
					}
				}
				m.Sites = append(m.Sites, ManifestSite{Name: slugify(site), SourceDir: dir})
			}
		}
	}

	for _, site := range inv.StaticSites {
		m.Sites = append(m.Sites, ManifestSite{Name: slugify(site.Name), SourceDir: ""})
	}

	if err := writeManifest(outDir, m); err != nil {
		return m, err
	}
	if err := writeRefusedMD(outDir, m.Refusals); err != nil {
		return m, err
	}
	return m, nil
}

// snapshotFunctions parses every function folder in a Function App's source
// tree, writes each handler's source into azure_export/, and returns the
// manifest entries. Refusals (unrecognized triggers) are appended to m.
func snapshotFunctions(outDir string, fa FunctionApp, tree map[string][]byte, m *Manifest) []ManifestFunction {
	var fns []ManifestFunction
	// group files by top-level folder (the Azure function name)
	folders := map[string]map[string][]byte{}
	for p, b := range tree {
		parts := strings.SplitN(p, "/", 2)
		if len(parts) != 2 {
			continue // root files (requirements.txt, host.json) — not a function
		}
		if folders[parts[0]] == nil {
			folders[parts[0]] = map[string][]byte{}
		}
		folders[parts[0]][parts[1]] = b
	}
	names := make([]string, 0, len(folders))
	for n := range folders {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, fnName := range names {
		files := folders[fnName]
		fjson, ok := files["function.json"]
		if !ok {
			continue // not an Azure function folder
		}
		trig, bindings, ok := deriveTrigger(fjson, fnName)
		if !ok {
			m.Refusals = append(m.Refusals, Refusal{RefuseUnknownBinding, fa.Name + "/" + fnName, "no supported trigger in function.json", "Trigger type not in {http, queue, timer} — keep on Azure or adapt (Studio)."})
			continue
		}
		code, codeName := pickEntry(files, fa.Runtime)
		if code == nil {
			m.Refusals = append(m.Refusals, Refusal{RefuseSourceBlocked, fa.Name + "/" + fnName, "no handler source file found", "Export the handler source manually."})
			continue
		}
		slug := slugify(fnName)
		rel := path.Join("atomic", slug, codeName)
		_ = writeExport(outDir, rel, code)
		fns = append(fns, ManifestFunction{
			Name: fnName, Handler: driftHandlerName(trig, fnName), Runtime: fa.Runtime,
			Trigger: trig, SourcePath: rel, SHA256: sha256hex(code), Bindings: bindings,
		})
	}

	// v2 in-code model: no function.json folders, just a function_app.py with
	// @app.* decorators. Parse it so its functions aren't silently invisible.
	if len(fns) == 0 && isV2Model(tree) {
		src := tree["function_app.py"]
		rel := path.Join("atomic", slugify(fa.Name)+"-v2", "function_app.py")
		_ = writeExport(outDir, rel, src) // #nosec G104 -- best-effort, same as v1
		v2fns, refs := parseV2Functions(fa.Name, src, rel)
		m.Refusals = append(m.Refusals, refs...)
		for i := range v2fns {
			v2fns[i].SHA256 = sha256hex(src)
		}
		fns = append(fns, v2fns...)
	}

	return fns
}

// pickEntry returns the handler source file + its name for a runtime.
func pickEntry(files map[string][]byte, runtime string) ([]byte, string) {
	order := []string{"__init__.py", "main.py"}
	if runtime == "node" {
		order = []string{"index.js", "index.mjs"}
	}
	for _, name := range order {
		if b, ok := files[name]; ok {
			return b, name
		}
	}
	return nil, ""
}

// snapshotSecrets lists a Function App's app settings and keeps the ones that
// look like user secrets (filtering Azure's own plumbing).
func snapshotSecrets(c azClient, rg, app string, deref bool) []ManifestSecret {
	var settings []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := c.runJSON([]string{"functionapp", "config", "appsettings", "list", "-g", rg, "-n", app}, &settings); err != nil {
		return nil
	}
	var out []ManifestSecret
	for _, s := range settings {
		if isAzurePlumbingSetting(s.Name) {
			continue
		}
		ms := ManifestSecret{Name: s.Name}
		if deref {
			ms.Value = s.Value
		}
		out = append(out, ms)
	}
	return out
}

// isAzurePlumbingSetting filters out Azure's built-in app settings, which are
// runtime plumbing, not user secrets.
func isAzurePlumbingSetting(name string) bool {
	up := strings.ToUpper(name)
	for _, p := range []string{"FUNCTIONS_", "AZUREWEBJOBS", "WEBSITE_", "APPINSIGHTS", "APPLICATIONINSIGHTS", "SCM_", "DOCKER_", "WEBSITES_"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

func appendSecret(list []ManifestSecret, s ManifestSecret) []ManifestSecret {
	for _, e := range list {
		if e.Name == s.Name {
			return list
		}
	}
	return append(list, s)
}

// parseSourceMap turns repeated --source <app>=<dir> flags into a lookup.
func parseSourceMap(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		name, dir, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(dir) == "" {
			return nil, fmt.Errorf("--source must be <app-name>=<dir>, got %q", p)
		}
		m[name] = dir
	}
	return m, nil
}

// localFunctionSource reads a Function App's source from a local directory into
// the same path→bytes tree the Kudu adapter would return. This is the robust
// path: a developer migrating their own app always has the code, and it works
// on every plan type (Linux Consumption's Kudu does not serve /api/zip).
// sortedKeys returns a map's string keys sorted, for deterministic export +
// manifest ordering regardless of Go's map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func localFunctionSource(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading local source %s: %w", dir, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files under %s", dir)
	}
	return out, nil
}

// writeExport writes content to outDir/rel, creating parents.
func writeExport(outDir, rel string, content []byte) error {
	full := filepath.Join(outDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}
