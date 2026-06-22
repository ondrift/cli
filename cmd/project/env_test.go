package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte(
		"# a comment\n\nexport FOO=bar\nQUOTED=\"hello world\"\nSINGLE='x=y'\nEMPTY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars, order, err := loadEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"FOO": "bar", "QUOTED": "hello world", "SINGLE": "x=y", "EMPTY": ""}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	if len(order) != 4 {
		t.Errorf("order = %v, want 4 keys", order)
	}
	// Missing file → empty, no error.
	if vs, _, err := loadEnvFile(filepath.Join(dir, "nope")); err != nil || len(vs) != 0 {
		t.Errorf("missing file: got %v, %v", vs, err)
	}
	// Malformed line → error.
	bad := filepath.Join(dir, "bad.env")
	_ = os.WriteFile(bad, []byte("NOEQUALS\n"), 0o600)
	if _, _, err := loadEnvFile(bad); err == nil {
		t.Error("expected an error for a line with no '='")
	}
}

// Precedence: terminal env (2) > --secret override (3) > .env (4).
func TestVariableHierarchy(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("TERMWINS=fromfile\nFLAGWINS=fromfile\nFILEONLY=fromfile\n"), 0o600)

	t.Setenv("TERMWINS", "fromterminal") // tier 2 — must win over flag-absent + .env
	t.Cleanup(func() { os.Unsetenv("FLAGWINS"); os.Unsetenv("FILEONLY") })

	if _, err := applyVariableSources(dir, []string{"FLAGWINS=fromflag"}, true, ""); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TERMWINS": "fromterminal", // terminal beats override-absent + .env
		"FLAGWINS": "fromflag",     // --secret beats .env
		"FILEONLY": "fromfile",     // only the .env has it
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	// --no-env-file (useEnvFile=false) ignores the file entirely.
	t.Cleanup(func() { os.Unsetenv("FILEONLY2") })
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("FILEONLY2=x\n"), 0o600)
	if _, err := applyVariableSources(dir, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, set := os.LookupEnv("FILEONLY2"); set {
		t.Error("FILEONLY2 should not be set when the .env file is skipped")
	}
}

// .env.<env> out-ranks the base .env (env-specific wins), and a key only in
// the base .env still fills.
func TestEnvSpecificFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("SHARED=base\nBASEONLY=base\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".env.staging"),
		[]byte("SHARED=staging\nENVONLY=staging\n"), 0o600)
	t.Cleanup(func() {
		os.Unsetenv("SHARED")
		os.Unsetenv("BASEONLY")
		os.Unsetenv("ENVONLY")
	})

	if _, err := applyVariableSources(dir, nil, true, "staging"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"SHARED":   "staging", // .env.staging beats base .env
		"ENVONLY":  "staging", // only in .env.staging
		"BASEONLY": "base",    // only in base .env
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// Tier 1 (hardcoded literal) vs a $ENVREF resolved from the lowest tier (.env),
// end-to-end through ParseDriftfile.
func TestHardcodedAndRefSecrets(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_PASSWORD=fromfile\n"), 0o600)
	t.Cleanup(func() { os.Unsetenv("DB_PASSWORD") })

	df := filepath.Join(dir, "Driftfile")
	_ = os.WriteFile(df, []byte(
		"name: app\nbackbone:\n  secrets:\n    LITERAL: hardcoded123\n    DB_PASS: $DB_PASSWORD\n"), 0o600)

	if _, err := applyVariableSources(dir, nil, true, ""); err != nil {
		t.Fatal(err)
	}
	m, err := ParseDriftfile(df)
	if err != nil {
		t.Fatal(err)
	}
	secs := m.Slice.Backbone.Secrets
	if secs["LITERAL"] != "hardcoded123" {
		t.Errorf("LITERAL = %q, want hardcoded123 (hardcoded, tier 1)", secs["LITERAL"])
	}
	if secs["DB_PASS"] != "fromfile" {
		t.Errorf("DB_PASS = %q, want fromfile (resolved from .env, tier 4)", secs["DB_PASS"])
	}
}
