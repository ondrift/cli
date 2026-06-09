package common

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath_Tilde(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	got, err := expandPath("~/test/file")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	want := filepath.Join(home, "test/file")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandPath_Absolute(t *testing.T) {
	got, err := expandPath("/etc/hosts")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if got != "/etc/hosts" {
		t.Fatalf("got %q, want /etc/hosts", got)
	}
}

func TestExpandPath_Relative(t *testing.T) {
	got, err := expandPath("relative/path")
	if err != nil {
		t.Fatalf("expandPath: %v", err)
	}
	if got != "relative/path" {
		t.Fatalf("got %q", got)
	}
}

// TestExpandPath_BareTilde regression-tests an index-out-of-range
// panic that the previous implementation hit on the literal string
// "~" — it sliced [:2] after only checking len(path) > 0. A
// public-source CLI shouldn't panic on any input shape.
func TestExpandPath_BareTilde(t *testing.T) {
	got, err := expandPath("~")
	if err != nil {
		t.Fatalf("expandPath(~): unexpected error: %v", err)
	}
	if got != "~" {
		t.Fatalf("expandPath(~) = %q, want %q", got, "~")
	}
}

// TestExpandPath_Empty exercises the same length-check edge.
func TestExpandPath_Empty(t *testing.T) {
	got, err := expandPath("")
	if err != nil {
		t.Fatalf("expandPath(\"\"): unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expandPath(\"\") = %q, want empty", got)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	// Override HOME to use a temp directory.
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveSession("tok123", "ref456"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	tok, ref, err := GetTokenFromSession()
	if err != nil {
		t.Fatalf("GetTokenFromSession: %v", err)
	}
	if tok != "tok123" {
		t.Fatalf("token: got %q", tok)
	}
	if ref != "ref456" {
		t.Fatalf("refresh: got %q", ref)
	}
}

func TestSessionPreservesActiveSlice(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok1", "ref1")
	SaveActiveSlice("myapp")

	// Re-saving session should preserve active_slice.
	SaveSession("tok2", "ref2")

	got := GetActiveSlice()
	if got != "myapp" {
		t.Fatalf("active slice lost after re-save: got %q", got)
	}
}

func TestActiveSliceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")
	SaveActiveSlice("staging")

	got := GetActiveSlice()
	if got != "staging" {
		t.Fatalf("active slice: got %q, want staging", got)
	}
}

func TestGetActiveSlice_NoSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	got := GetActiveSlice()
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestRequireActiveSlice_Set(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")
	SaveActiveSlice("prod")

	s, err := RequireActiveSlice()
	if err != nil {
		t.Fatalf("RequireActiveSlice: %v", err)
	}
	if s != "prod" {
		t.Fatalf("got %q", s)
	}
}

func TestRequireActiveSlice_NotSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")

	_, err := RequireActiveSlice()
	if err == nil {
		t.Fatal("expected error when no active slice")
	}
}

func TestSessionFileFormat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	SaveSession("tok", "ref")
	SaveActiveSlice("myslice")

	path := filepath.Join(dir, ".drift", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["token"] != "tok" {
		t.Fatalf("token: %q", m["token"])
	}
	if m["refresh_token"] != "ref" {
		t.Fatalf("refresh: %q", m["refresh_token"])
	}
	if m["active_slice"] != "myslice" {
		t.Fatalf("active_slice: %q", m["active_slice"])
	}
}
