package atomic_common

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAtomicMetadata_Go(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
package main
// @atomic http=post:checkout auth=apikey
func PostCheckout(req Request) Response { return Response{} }
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("ParseAtomicMetadata: %v", err)
	}
	if meta.Trigger != "http" || meta.Method != "post" || meta.Path != "checkout" || meta.Auth != "apikey" || meta.Stream != "" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadata_Python(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app.py"), []byte(`
# @atomic http=get:menu auth=none
def get_menu(req):
    return {"status": 200}
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "app.py"))
	if err != nil {
		t.Fatalf("ParseAtomicMetadata: %v", err)
	}
	if meta.Trigger != "http" || meta.Method != "get" || meta.Path != "menu" || meta.Auth != "none" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadata_WithStream(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
// @atomic http=get:events auth=none stream=sse
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if meta.Stream != "sse" {
		t.Fatalf("stream: %q, want sse", meta.Stream)
	}
}

func TestParseAtomicMetadata_WebSocket(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
// @atomic http=get:chat auth=none stream=ws
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if meta.Stream != "ws" {
		t.Fatalf("stream: %q, want ws", meta.Stream)
	}
}

func TestParseAtomicMetadata_PathParams(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
// @atomic http=get:users/:id/posts/:postId auth=apikey
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if meta.Method != "get" || meta.Path != "users/:id/posts/:postId" || meta.Auth != "apikey" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadata_InlineSecrets(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
// @atomic http=post:charge auth=none secrets=STRIPE_KEY,SENDGRID_KEY
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"STRIPE_KEY", "SENDGRID_KEY"}
	if !reflect.DeepEqual(meta.Secrets, want) {
		t.Fatalf("secrets = %v, want %v", meta.Secrets, want)
	}
}

func TestParseAtomicMetadata_QueueTrigger(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "worker.go"), []byte(`
// @atomic queue=validate auth=none secrets=STRIPE_KEY
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "worker.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if meta.Trigger != "queue" || meta.Method != "validate" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadata_CronTrigger(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "job.go"), []byte(`
// @atomic cron="0 * * * *" auth=none
`), 0o644)

	meta, err := ParseAtomicMetadata(filepath.Join(dir, "job.go"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if meta.Trigger != "cron" || meta.Method != "0 * * * *" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadata_RejectsMultipleTriggers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.go"), []byte(`
// @atomic http=get:foo queue=bar auth=none
`), 0o644)

	if _, err := ParseAtomicMetadata(filepath.Join(dir, "bad.go")); err == nil {
		t.Fatal("expected error for multiple triggers")
	}
}

func TestParseAtomicMetadata_RejectsMissingTrigger(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.go"), []byte(`
// @atomic auth=none
`), 0o644)

	if _, err := ParseAtomicMetadata(filepath.Join(dir, "bad.go")); err == nil {
		t.Fatal("expected error for missing trigger")
	}
}

func TestParseAtomicMetadata_NoAnnotation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}"), 0o644)

	if _, err := ParseAtomicMetadata(filepath.Join(dir, "main.go")); err == nil {
		t.Fatal("expected error for file without annotation")
	}
}

func TestParseAtomicMetadata_FileNotFound(t *testing.T) {
	if _, err := ParseAtomicMetadata("/nonexistent/file.go"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseAtomicMetadataFromDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "utils.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "handler.go"), []byte(`
// @atomic http=post:order auth=none
`), 0o644)

	meta, err := ParseAtomicMetadataFromDir(dir)
	if err != nil {
		t.Fatalf("ParseAtomicMetadataFromDir: %v", err)
	}
	if meta.Method != "post" || meta.Path != "order" || meta.Auth != "none" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestParseAtomicMetadataFromDir_NoAnnotation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)

	if _, err := ParseAtomicMetadataFromDir(dir); err == nil {
		t.Fatal("expected error when no file has annotation")
	}
}

func TestParseAtomicMetadataFromDir_EmptyDir(t *testing.T) {
	if _, err := ParseAtomicMetadataFromDir(t.TempDir()); err == nil {
		t.Fatal("expected error for empty directory")
	}
}

// ── Multi-handler parsing tests ─────────────────────────────────────

func TestParseAllAtomicMetadata_SingleHandler(t *testing.T) {
	dir := t.TempDir()
	src := `package main
import "fmt"

// @atomic http=get:hello auth=none
func GetHello(req drift.Request) {
    fmt.Println("hi")
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(src), 0o644)

	metas, err := ParseAllAtomicMetadata(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("got %d handlers, want 1", len(metas))
	}
	if metas[0].SentinelName != "GetHello" {
		t.Errorf("sentinel: %q", metas[0].SentinelName)
	}
	if metas[0].Path != "hello" {
		t.Errorf("path: %q", metas[0].Path)
	}
	if metas[0].Language != "go" {
		t.Errorf("language: %q", metas[0].Language)
	}
}

func TestParseAllAtomicMetadata_MultipleHandlersOneFile(t *testing.T) {
	dir := t.TempDir()
	src := `package main

// @atomic http=get:reviewer/queue auth=none secrets=KEY
func GetReviewerQueue(req drift.Request) {}

// helper, no decorator → unbilled, unrouted
func verifyJWT(token string) bool { return true }

// @atomic http=get:reviewer/submission/:id auth=none secrets=KEY
func GetReviewerSubmissionId(req drift.Request) {}

// @atomic http=get:reviewer/mailbox auth=none secrets=KEY
func GetReviewerMailbox(req drift.Request) {}
`
	path := filepath.Join(dir, "reviewer.go")
	os.WriteFile(path, []byte(src), 0o644)

	metas, err := ParseAllAtomicMetadata(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("got %d handlers, want 3", len(metas))
	}
	wantNames := []string{"GetReviewerQueue", "GetReviewerSubmissionId", "GetReviewerMailbox"}
	for i, w := range wantNames {
		if metas[i].SentinelName != w {
			t.Errorf("[%d] sentinel: got %q want %q", i, metas[i].SentinelName, w)
		}
	}
	wantPaths := []string{"reviewer/queue", "reviewer/submission/:id", "reviewer/mailbox"}
	for i, w := range wantPaths {
		if metas[i].Path != w {
			t.Errorf("[%d] path: got %q want %q", i, metas[i].Path, w)
		}
	}
}

func TestParseAllAtomicMetadata_StackedDecoratorsRejected(t *testing.T) {
	dir := t.TempDir()
	src := `package main

// @atomic http=get:foo auth=none
// @atomic http=get:bar auth=none
func GetSomething(req drift.Request) {}
`
	path := filepath.Join(dir, "bad.go")
	os.WriteFile(path, []byte(src), 0o644)

	_, err := ParseAllAtomicMetadata(path)
	if err == nil {
		t.Fatal("expected error for stacked decorators")
	}
	if !strings.Contains(err.Error(), "multiple @atomic decorators") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseAllAtomicMetadata_HelpersIgnored(t *testing.T) {
	dir := t.TempDir()
	src := `package main

// regular comment, not an annotation
func helperOne() {}

func helperTwo() {} // inline comment, no decorator

// @atomic http=post:submit auth=none
func PostSubmit(req drift.Request) {}

func helperThree() {}
`
	path := filepath.Join(dir, "submit.go")
	os.WriteFile(path, []byte(src), 0o644)

	metas, err := ParseAllAtomicMetadata(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("got %d, want 1", len(metas))
	}
	if metas[0].SentinelName != "PostSubmit" {
		t.Errorf("sentinel: %q", metas[0].SentinelName)
	}
}

func TestParseAllAtomicMetadata_RubyMultiHandler(t *testing.T) {
	dir := t.TempDir()
	src := `require 'drift'

# @atomic http=get:reviewer/queue auth=none secrets=KEY
def get_reviewer_queue(req)
  # ...
end

# @atomic http=get:reviewer/mailbox auth=none secrets=KEY
def get_reviewer_mailbox(req)
  # ...
end

# helper
def verify_jwt(req)
  # ...
end
`
	path := filepath.Join(dir, "reviewer.rb")
	os.WriteFile(path, []byte(src), 0o644)

	metas, err := ParseAllAtomicMetadata(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d, want 2", len(metas))
	}
	if metas[0].SentinelName != "get_reviewer_queue" || metas[1].SentinelName != "get_reviewer_mailbox" {
		t.Errorf("got %q, %q", metas[0].SentinelName, metas[1].SentinelName)
	}
}

func TestParseAllAtomicMetadata_PythonMultiHandler(t *testing.T) {
	dir := t.TempDir()
	src := `import drift

# @atomic http=post:submit auth=none
def post_submit(body, req):
    return 200, "OK", {}

def _helper():
    pass

# @atomic http=get:status/:token auth=none
def get_status_token(req):
    return 200, "OK", {}
`
	path := filepath.Join(dir, "app.py")
	os.WriteFile(path, []byte(src), 0o644)

	metas, err := ParseAllAtomicMetadata(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("got %d, want 2", len(metas))
	}
}

func TestParseAllAtomicMetadataFromDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package main
// @atomic http=get:a auth=none
func GetA(r drift.Request) {}
`), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(`package main
// @atomic http=get:b auth=none
func GetB(r drift.Request) {}

// @atomic http=get:c auth=none
func GetC(r drift.Request) {}
`), 0o644)

	metas, err := ParseAllAtomicMetadataFromDir(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("got %d, want 3", len(metas))
	}
}

// ─── Orphaned @atomic annotations ──────────────────────────────────────────
//
// An annotation that matched no callable used to be dropped in silence, and the
// silence was expensive. The file parsed as ZERO functions, so `project deploy`
// went on to compare a slice holding N functions against a manifest declaring 0
// and refused with "atomic.functions 2 (current) > 0 (declared)" — a message
// about slice SIZE that says nothing about the real cause. Finding it meant
// reading the parser.
//
// The common way in is following a doc example that puts the annotation at the
// top of the file above an import rather than directly above a callable — which
// is exactly what the Node SDK's own documentation showed.

func TestParseAll_OrphanedAnnotationIsAnError(t *testing.T) {
	dir := t.TempDir()
	// Verbatim the shape the SDK docs used: annotation on top, arrow function
	// handed to drift.run, no named function anywhere.
	src := `// @atomic http=get:events auth=none
const drift = require("@ondrift/sdk");
drift.run(async (req) => ({ status: 200 }));
`
	if err := os.WriteFile(filepath.Join(dir, "events.js"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseAllAtomicMetadata(filepath.Join(dir, "events.js"))
	if err == nil {
		t.Fatal("an @atomic above no callable must be an error — the silence is the bug")
	}
	for _, want := range []string{"events.js:1", "not directly above a function", "function myFunction"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name the file:line and the shape expected; %q missing from:\n%v", want, err)
		}
	}
}

func TestParseAll_OrphanNamesTheRightLine(t *testing.T) {
	dir := t.TempDir()
	src := `package main

// @atomic http=post:good auth=none
func PostGood(req Request) Response { return Response{} }

// @atomic http=post:orphan auth=none
var notAFunction = 1
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseAllAtomicMetadata(filepath.Join(dir, "main.go"))
	if err == nil {
		t.Fatal("the second annotation is an orphan and must be reported")
	}
	if !strings.Contains(err.Error(), "main.go:6") {
		t.Errorf("must name line 6 (the orphan), got: %v", err)
	}
}

// A well-formed file must stay silent — an over-eager guard that fires on valid
// code would be worse than the silence it replaces.
func TestParseAll_ValidFilesStillParseCleanly(t *testing.T) {
	cases := map[string]string{
		"main.go":   "package main\n\n// @atomic http=get:a auth=none\nfunc GetA(r Request) Response { return Response{} }\n",
		"app.py":    "# @atomic http=get:b auth=none\ndef get_b(body, req):\n    return 200, 'OK', {}\n",
		"h.js":      "const drift = require('@ondrift/sdk');\n\n// @atomic http=get:c auth=none\nfunction getC(req) { return [200, 'OK', {}]; }\n\nmodule.exports = { getC };\n",
		"h.rb":      "# @atomic http=get:d auth=none\ndef get_d(body, req)\n  [200, 'OK', {}]\nend\n",
		"index.php": "<?php\n// @atomic http=get:e auth=none\nfunction getE($body, $req) { return [200, 'OK', []]; }\n",
	}
	for name, src := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		metas, err := ParseAllAtomicMetadata(path)
		if err != nil {
			t.Errorf("%s: valid source must parse cleanly, got %v", name, err)
			continue
		}
		if len(metas) != 1 {
			t.Errorf("%s: expected 1 function, got %d", name, len(metas))
		}
	}
}

// A comment that merely mentions @atomic in prose is not an annotation — the
// regex requires the annotation body, so a sentence about it must not trip the
// guard.
func TestParseAll_ProseMentionIsNotAnOrphan(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\n// Every handler needs an @atomic\n// decorator above it.\nfunc helper() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAllAtomicMetadata(filepath.Join(dir, "main.go")); err != nil {
		t.Errorf("prose mentioning @atomic must not be treated as an annotation: %v", err)
	}
}
