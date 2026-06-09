package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseDriftfile_RestaurantTemplate parses the canonical restaurant
// Driftfile end-to-end and verifies every field lands where the spec
// says it should. This is the contract test for the v1.0 schema —
// when adding new spec features, extend this test before touching any
// other code.
func TestParseDriftfile_RestaurantTemplate(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "test-resend-key-xyz")
	t.Setenv("SENDER_EMAIL", "noreply@la-cucina.test")

	tmp := writeRestaurantFixture(t)

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}

	if m.Slice.Name != "la-cucina" {
		t.Errorf("slice.name = %q, want %q", m.Slice.Name, "la-cucina")
	}

	// atomic — bare-list shorthand expands to functions:[...]
	if len(m.Slice.Atomic.Functions) != 3 {
		t.Fatalf("atomic.functions count = %d, want 3", len(m.Slice.Atomic.Functions))
	}
	wantFns := []string{"get-menu", "submit-reservation", "confirm-reservation"}
	for i, fn := range m.Slice.Atomic.Functions {
		if fn.Name != wantFns[i] {
			t.Errorf("atomic.functions[%d].name = %q, want %q", i, fn.Name, wantFns[i])
		}
	}

	// backbone.nosql — flat-list of strings
	if len(m.Slice.Backbone.NoSQL) != 1 || m.Slice.Backbone.NoSQL[0].Name != "reservations" {
		t.Errorf("backbone.nosql = %+v, want one entry named reservations", m.Slice.Backbone.NoSQL)
	}

	// backbone.queues — flat-list of strings
	if len(m.Slice.Backbone.Queues) != 1 || m.Slice.Backbone.Queues[0] != "reservation-queue" {
		t.Errorf("backbone.queues = %+v, want [reservation-queue]", m.Slice.Backbone.Queues)
	}

	// backbone.cache — short-form `<key>: <path>` becomes File
	cache := m.Slice.Backbone.Cache
	if e, ok := cache["menu"]; !ok {
		t.Errorf("backbone.cache.menu missing")
	} else if e.File != "./backbone/menu.json" {
		t.Errorf("backbone.cache.menu.file = %q, want ./backbone/menu.json", e.File)
	}

	// backbone.secrets — $ENVREFs are resolved before validation.
	if got := m.Slice.Backbone.Secrets["RESTAURANT_NAME"]; got != "La Cucina" {
		t.Errorf("secret RESTAURANT_NAME = %q, want La Cucina", got)
	}
	if got := m.Slice.Backbone.Secrets["RESEND_API_KEY"]; got != "test-resend-key-xyz" {
		t.Errorf("secret RESEND_API_KEY = %q, want resolved env value", got)
	}
	if got := m.Slice.Backbone.Secrets["SENDER_EMAIL"]; got != "noreply@la-cucina.test" {
		t.Errorf("secret SENDER_EMAIL = %q, want resolved env value", got)
	}

	// canvas — bare-string shorthand becomes sites:[{dir: ./canvas}]
	if len(m.Slice.Canvas.Sites) != 1 || m.Slice.Canvas.Sites[0].Dir != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice.Canvas.Sites)
	}
}

// TestParseDriftfile_MissingEnvref ensures that an unset $ENVREF
// surfaces as a clear validation error, not silent injection of
// an empty string.
func TestParseDriftfile_MissingEnvref(t *testing.T) {
	tmp := writeRestaurantFixture(t)
	// Deliberately leave RESEND_API_KEY/SENDER_EMAIL unset.

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for missing envref, got nil")
	}
	msg := err.Error()
	if !contains(msg, "RESEND_API_KEY") || !contains(msg, "SENDER_EMAIL") {
		t.Errorf("error should mention both unset envrefs, got: %s", msg)
	}
}

// TestParseDriftfile_InvalidName rejects an out-of-shape slice name.
func TestParseDriftfile_InvalidName(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: BAD_Name_With_Underscores
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected validation error for invalid name, got nil")
	}
	if !contains(err.Error(), "slice.name") {
		t.Errorf("error should mention slice.name, got: %s", err)
	}
}

// TestParseDriftfile_CanvasShorthandString verifies the bare-string
// canvas shorthand expands correctly.
func TestParseDriftfile_CanvasShorthandString(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: hello
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice.Canvas.Sites) != 1 || m.Slice.Canvas.Sites[0].Dir != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice.Canvas.Sites)
	}
}

// TestParseDriftfile_AtomicShorthandList verifies the bare-list
// atomic shorthand expands correctly.
func TestParseDriftfile_AtomicShorthandList(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: hello
  atomic:
    - foo
    - bar
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "foo"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "bar"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice.Atomic.Functions) != 2 {
		t.Fatalf("atomic.functions count = %d, want 2", len(m.Slice.Atomic.Functions))
	}
	if m.Slice.Atomic.Functions[0].Name != "foo" || m.Slice.Atomic.Functions[1].Name != "bar" {
		t.Errorf("atomic.functions = %+v, want [foo bar]", m.Slice.Atomic.Functions)
	}
}

// writeRestaurantFixture writes a minimal-but-complete restaurant
// project shape into a temp dir and returns the dir path. The
// Driftfile content matches the canonical template under
// templates/sites/hospitality/restaurant/.
func writeRestaurantFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: la-cucina
  atomic:
    - get-menu
    - submit-reservation
    - confirm-reservation
  backbone:
    nosql: [reservations]
    queues: [reservation-queue]
    cache:
      menu: ./backbone/menu.json
    secrets:
      RESTAURANT_NAME: "La Cucina"
      RESEND_API_KEY:  $RESEND_API_KEY
      SENDER_EMAIL:    $SENDER_EMAIL
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "get-menu"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "submit-reservation"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "confirm-reservation"))
	mustMkdir(t, filepath.Join(tmp, "backbone"))
	mustWrite(t, filepath.Join(tmp, "backbone", "menu.json"), `[]`)
	return tmp
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestParseDriftfile_BraceVarSubstitution covers the env-aware
// Driftfile feature: ${VAR} placeholders resolve from os.Environ
// before YAML parsing. Typical use: `slice.name: ${ENV}-myapp`
// resolved by `drift project deploy --env=prod` setting ENV=prod.
func TestParseDriftfile_BraceVarSubstitution(t *testing.T) {
	t.Setenv("ENV", "staging")
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: ${ENV}-hello
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if m.Slice.Name != "staging-hello" {
		t.Errorf("slice.name = %q, want staging-hello", m.Slice.Name)
	}
}

// TestParseDriftfile_BraceVarMissing surfaces every unset placeholder
// at once instead of one-at-a-time.
func TestParseDriftfile_BraceVarMissing(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
slice:
  name: ${ENV}-${REGION}-app
  canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unset ${VAR}, got nil")
	}
	msg := err.Error()
	if !contains(msg, "ENV") || !contains(msg, "REGION") {
		t.Errorf("error should mention both unset vars, got: %s", msg)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
