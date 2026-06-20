package atomic_cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSrc(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Flat files form the Default element; an @atomic subdir is a named element.
func TestDiscoverElements_DefaultAndNamed(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "auth.go"),
		"package main\n\n// @atomic http=post:auth/login auth=none\nfunc PostAuthLogin(body any, req any) {}\n")
	writeSrc(t, filepath.Join(root, "groups.go"),
		"package main\n\n// @atomic http=get:groups auth=none\nfunc GetGroups(req any) {}\n")
	writeSrc(t, filepath.Join(root, "lib.go"), // no @atomic — shared package code
		"package main\n\ntype RequestBody = map[string]any\n")
	writeSrc(t, filepath.Join(root, "health", "health.go"),
		"package main\n\n// @atomic http=get:health auth=none\nfunc GetHealth(req any) {}\n")

	els, err := DiscoverElements(root)
	if err != nil {
		t.Fatalf("DiscoverElements: %v", err)
	}
	if len(els) != 2 {
		t.Fatalf("got %d elements, want 2 (default + health): %+v", len(els), els)
	}
	// sorted by name → default, health
	def, health := els[0], els[1]
	if def.Name != "default" || def.Lang != "go" || len(def.Funcs) != 2 {
		t.Errorf("default element = %+v, want {default, go, 2 funcs}", def)
	}
	if health.Name != "health" || len(health.Funcs) != 1 {
		t.Errorf("health element = %+v, want {health, 1 func}", health)
	}
	// sentinel + path round-trip
	if def.Funcs[0].SentinelName != "GetGroups" || def.Funcs[0].Path != "groups" {
		t.Errorf("default func[0] = %+v, want GetGroups/groups", def.Funcs[0])
	}
}

// Legacy folder-per-function: only subdirs, no flat files → named
// one-function elements. Back-compat with the old layout, for free.
func TestDiscoverElements_LegacyFolders(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "auth-login", "auth-login.go"),
		"package main\n\n// @atomic http=post:auth/login auth=none\nfunc PostAuthLogin(body any, req any) {}\n")
	writeSrc(t, filepath.Join(root, "auth-me", "auth-me.go"),
		"package main\n\n// @atomic http=get:auth/me auth=none\nfunc GetAuthMe(req any) {}\n")

	els, err := DiscoverElements(root)
	if err != nil {
		t.Fatalf("DiscoverElements: %v", err)
	}
	if len(els) != 2 {
		t.Fatalf("got %d elements, want 2 named one-function elements", len(els))
	}
	for _, e := range els {
		if len(e.Funcs) != 1 {
			t.Errorf("element %q has %d funcs, want 1", e.Name, len(e.Funcs))
		}
	}
}

// Two languages in one element directory is incoherent → loud error.
func TestDiscoverElements_MixedLanguageRejected(t *testing.T) {
	root := t.TempDir()
	writeSrc(t, filepath.Join(root, "a.go"),
		"package main\n\n// @atomic http=get:a auth=none\nfunc GetA(req any) {}\n")
	writeSrc(t, filepath.Join(root, "b.py"),
		"# @atomic http=get:b auth=none\ndef get_b(req): pass\n")

	_, err := DiscoverElements(root)
	if err == nil {
		t.Fatal("expected a mixed-language error, got nil")
	}
}
