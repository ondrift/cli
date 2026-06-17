package atomic_cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write creates dir/name (and any parents) with the given content.
func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return full
}

func digest(t *testing.T, dir, element string) string {
	t.Helper()
	d, err := FunctionDigest(dir, element)
	if err != nil {
		t.Fatalf("FunctionDigest(%s): %v", dir, err)
	}
	if d == "" {
		t.Fatalf("FunctionDigest(%s) returned empty digest", dir)
	}
	return d
}

func TestFunctionDigest_Deterministic(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "handler.py", "def h(body, _req):\n    return {}\n")
	write(t, dir, "requirements.txt", "drift\n")

	if a, b := digest(t, dir, "billing"), digest(t, dir, "billing"); a != b {
		t.Fatalf("digest not deterministic: %s != %s", a, b)
	}
}

func TestFunctionDigest_MtimeIndependent(t *testing.T) {
	dir := t.TempDir()
	f := write(t, dir, "handler.py", "def h(body, _req):\n    return {}\n")
	before := digest(t, dir, "")

	// Bump the file's mtime well into the past; the digest must not change.
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(f, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if after := digest(t, dir, ""); after != before {
		t.Fatalf("digest changed after mtime bump: %s -> %s", before, after)
	}
}

func TestFunctionDigest_ContentChangeFlipsDigest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "handler.py", "def h(body, _req):\n    return {}\n")
	before := digest(t, dir, "")

	write(t, dir, "handler.py", "def h(body, _req):\n    return {'ok': True}\n")
	if after := digest(t, dir, ""); after == before {
		t.Fatalf("digest unchanged after content edit: %s", after)
	}
}

func TestFunctionDigest_NewFileFlipsDigest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "handler.py", "x=1\n")
	before := digest(t, dir, "")

	write(t, dir, "helper.py", "y=2\n")
	if after := digest(t, dir, ""); after == before {
		t.Fatalf("digest unchanged after adding a file: %s", after)
	}
}

func TestFunctionDigest_ElementMatters(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "handler.py", "x=1\n")

	if a, b := digest(t, dir, ""), digest(t, dir, "billing"); a == b {
		t.Fatalf("digest ignored element grouping: %s == %s", a, b)
	}
}

func TestFunctionDigest_IgnoresBuildAndHiddenArtefacts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "handler.js", "module.exports = () => ({})\n")
	write(t, dir, "package.json", `{"name":"fn"}`)
	before := digest(t, dir, "")

	// Files inside build/runtime artefact dirs and hidden files must not count.
	write(t, dir, "node_modules/left-pad/index.js", "// vendored\n")
	write(t, dir, "target/release/blob", "binary-junk\n")
	write(t, dir, "__pycache__/handler.cpython.pyc", "bytecode\n")
	write(t, dir, ".env", "SECRET=shhh\n")

	if after := digest(t, dir, ""); after != before {
		t.Fatalf("digest changed after adding ignored artefacts: %s -> %s", before, after)
	}
}

func TestFunctionDigest_NestedSourceCounts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app.go", "package main\n")
	before := digest(t, dir, "")

	// A genuine nested source file (not in a skip dir) must change the digest.
	write(t, dir, "internal/util.go", "package internal\n")
	if after := digest(t, dir, ""); after == before {
		t.Fatalf("digest ignored a nested source file: %s", after)
	}
}
