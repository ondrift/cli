package azure

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fakeProvider returns in-memory fixtures for the heavy artifacts, so the whole
// snapshot pipeline runs offline. orders-api is the rich Python case (an HTTP
// function + a timer function); notify-worker is a Node queue function.
type fakeProvider struct{}

func (fakeProvider) functionSource(app string) (map[string][]byte, error) {
	switch app {
	case "orders-api":
		return map[string][]byte{
			"requirements.txt": []byte("azure-functions\n"),
			"GetOrder/function.json": []byte(`{"bindings":[
				{"type":"httpTrigger","direction":"in","name":"req","methods":["get"],"route":"orders/{id}","authLevel":"anonymous"},
				{"type":"http","direction":"out","name":"$return"}]}`),
			"GetOrder/__init__.py": []byte("import azure.functions as func\n\ndef main(req: func.HttpRequest) -> func.HttpResponse:\n    return func.HttpResponse(\"ok\")\n"),
			"NightlyReport/function.json": []byte(`{"bindings":[
				{"type":"timerTrigger","direction":"in","name":"timer","schedule":"0 0 2 * * *"}]}`),
			"NightlyReport/__init__.py": []byte("def main(timer):\n    pass\n"),
		}, nil
	case "notify-worker":
		return map[string][]byte{
			"OnMessage/function.json": []byte(`{"bindings":[
				{"type":"queueTrigger","direction":"in","name":"msg","queueName":"notify"}]}`),
			"OnMessage/index.js": []byte("module.exports = async function (ctx, msg) { ctx.log(msg); };\n"),
		}, nil
	}
	return nil, os.ErrNotExist
}

func (fakeProvider) cosmosCollections(account string) (map[string][]json.RawMessage, error) {
	return map[string][]json.RawMessage{
		"orders": {
			json.RawMessage(`{"id":"o1","total":42}`),
			json.RawMessage(`{"id":"o2","total":7}`),
		},
	}, nil
}

func (fakeProvider) storageBlobs(account string) (map[string]map[string][]byte, error) {
	return map[string]map[string][]byte{
		"assets": {"logo.svg": []byte("<svg/>")},
	}, nil
}

func (fakeProvider) storageQueues(account string) (map[string][]json.RawMessage, error) {
	// Pretty-printed (multi-line) on purpose: `az -o json` returns indented JSON,
	// so this exercises the JSONL compaction. A regression back to multi-line
	// output is caught by the validity check in TestRunSnapshot_Golden.
	return map[string][]json.RawMessage{
		"notify": {json.RawMessage("{\n  \"content\": \"aGVsbG8=\",\n  \"id\": \"m1\"\n}")},
	}, nil
}

func (fakeProvider) storageStaticSites(account string) (map[string]map[string][]byte, error) {
	return nil, nil
}

func TestRunSnapshot_Golden(t *testing.T) {
	// A dir that does NOT pre-exist — runSnapshot must create it, even if every
	// export refuses (the bug that shipped: manifest write into a missing dir).
	dir := filepath.Join(t.TempDir(), "azure_export")
	m, err := runSnapshot(fakeAz{t}, fakeProvider{}, nil, "Contoso-Prod", "demo-rg", dir, "orders", true)
	if err != nil {
		t.Fatalf("runSnapshot: %v", err)
	}

	eq(t, "functions", len(m.Functions), 3) // GetOrder, NightlyReport, OnMessage
	eq(t, "collections", len(m.Collections), 1)
	eq(t, "secrets", len(m.Secrets), 2)   // STRIPE_KEY + DB_CONN (Azure plumbing filtered)
	eq(t, "refusals", len(m.Refusals), 1) // legacy-billing (.NET)

	if m.Refusals[0].Code != RefuseRuntimeDotNet {
		t.Errorf("refusal = %s, want %s", m.Refusals[0].Code, RefuseRuntimeDotNet)
	}

	byName := map[string]ManifestFunction{}
	for _, f := range m.Functions {
		byName[f.Name] = f
	}
	if got := byName["GetOrder"].Trigger; got.Type != "http" || got.Method != "get" || got.Route != "orders/:id" {
		t.Errorf("GetOrder trigger = %+v, want http get orders/:id", got)
	}
	if got := byName["NightlyReport"].Trigger; got.Type != "cron" || got.Schedule != "0 2 * * *" {
		t.Errorf("NightlyReport trigger = %+v, want cron 0 2 * * *", got)
	}
	if got := byName["OnMessage"].Trigger; got.Type != "queue" || got.Queue != "notify" {
		t.Errorf("OnMessage trigger = %+v, want queue notify", got)
	}

	// The export files exist on disk with the right shape.
	for _, rel := range []string{
		"manifest.json", "REFUSED.md",
		"atomic/get-order/__init__.py",
		"atomic/nightly-report/__init__.py",
		"atomic/on-message/index.js",
		"backbone/nosql/orders.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected export %s: %v", rel, err)
		}
	}

	// Queue messages must be valid JSONL — one compact JSON object per line — so
	// apply can re-push them line by line. (The fixture is multi-line; this guards
	// the compaction.)
	queueFiles, _ := filepath.Glob(filepath.Join(dir, "queues", "storage", "*", "*.jsonl"))
	if len(queueFiles) == 0 {
		t.Error("expected a queue export (the fixtures include a storage account)")
	}
	for _, qf := range queueFiles {
		data, _ := os.ReadFile(qf) // #nosec G304 -- test temp dir
		for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
			if len(line) > 0 && !json.Valid(line) {
				t.Errorf("%s: queue message is not compact one-line JSON (compaction regressed): %q", qf, line)
			}
		}
	}

	// The manifest round-trips and hashes the source verbatim.
	reloaded, err := readManifest(dir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	eq(t, "reloaded functions", len(reloaded.Functions), 3)
	src, _ := os.ReadFile(filepath.Join(dir, "atomic", "get-order", "__init__.py"))
	if byName["GetOrder"].SHA256 != sha256hex(src) {
		t.Errorf("GetOrder source hash mismatch — export must be verbatim")
	}
}

// TestRunSnapshot_LocalSource exercises --source: snapshot reads a Function
// App's code from a local directory (the robust path) instead of Kudu (which
// 404s on real Linux Consumption + run-from-package).
func TestRunSnapshot_LocalSource(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "orders-api")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(appDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("host.json", "{}")
	write("Submit/function.json", `{"bindings":[{"type":"httpTrigger","direction":"in","name":"req","methods":["post"],"route":"submit"}]}`)
	write("Submit/__init__.py", "def main(req):\n    return 1\n")

	out := filepath.Join(t.TempDir(), "export")
	m, err := runSnapshot(fakeAz{t}, fakeProvider{}, map[string]string{"orders-api": appDir}, "sub", "demo-rg", out, "x", false)
	if err != nil {
		t.Fatalf("runSnapshot: %v", err)
	}

	var got *ManifestFunction
	for i := range m.Functions {
		if m.Functions[i].Name == "Submit" {
			got = &m.Functions[i]
		}
	}
	if got == nil {
		t.Fatal("expected the locally-sourced Submit function in the manifest")
	}
	if got.Trigger.Type != "http" || got.Trigger.Method != "post" || got.Trigger.Route != "submit" {
		t.Errorf("Submit trigger = %+v, want http post submit", got.Trigger)
	}
	if _, err := os.Stat(filepath.Join(out, "atomic", "submit", "__init__.py")); err != nil {
		t.Errorf("expected local source written to the export: %v", err)
	}
}

// TestSourceFromSitePackages exercises the Linux-Consumption retrieval path: the
// app runs from a mounted package, so the code lives under Kudu VFS at
// data/SitePackages/<name> (name in packagename.txt). This is the path that
// /api/zip can't serve — the gap that made source download impossible there.
func TestSourceFromSitePackages(t *testing.T) {
	// A deployment package zip: wwwroot-shaped (function dir + handler), in memory.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	want := map[string]string{
		"Submit/function.json": `{"bindings":[{"type":"httpTrigger","direction":"in","name":"req","methods":["post"],"route":"submit"}]}`,
		"Submit/__init__.py":   "def main(req):\n    return 1\n",
	}
	for name, body := range want {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	pkgZip := buf.Bytes()

	const pkgName = "20260624000000.zip"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer faketoken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/vfs/data/SitePackages/packagename.txt":
			io.WriteString(w, pkgName+"\r\n") // trailing CRLF, like the real endpoint
		case "/api/vfs/data/SitePackages/" + pkgName:
			w.Write(pkgZip) // #nosec G104 -- test server
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	prev := scmHost
	scmHost = func(string) string { return srv.URL }
	defer func() { scmHost = prev }()

	tree, err := azProvider{c: fakeAz{t}, rg: "demo-rg"}.sourceFromSitePackages("any-app")
	if err != nil {
		t.Fatalf("sourceFromSitePackages: %v", err)
	}
	if gotAuth != "Bearer faketoken" {
		t.Errorf("Kudu request auth = %q, want the minted bearer", gotAuth)
	}
	for name, body := range want {
		if string(tree[name]) != body {
			t.Errorf("tree[%q] = %q, want %q", name, string(tree[name]), body)
		}
	}
}

// TestExtractPackage covers the deployment-package format dispatch: a real zip is
// read in-process; a non-package payload is a clean error (the SquashFS branch is
// verified live, against a real Linux Consumption app).
func TestExtractPackage(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("GetOrder/__init__.py")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "def main(req):\n    return 1\n") // #nosec G104 -- test buffer
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := extractPackage(buf.Bytes())
	if err != nil {
		t.Fatalf("extractPackage(zip): %v", err)
	}
	if string(tree["GetOrder/__init__.py"]) != "def main(req):\n    return 1\n" {
		t.Errorf("zip handler not extracted verbatim, got %q", tree["GetOrder/__init__.py"])
	}
	if _, err := extractPackage([]byte("not a deployment package")); err == nil {
		t.Error("expected an error for a non-package payload")
	}
}
