package atomic_common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard checked that the manifest FILE existed, not that it declared the
// SDK. A package.json carrying `"dependencies": {}` passed, and the failure
// moved from deploy time — one clear, actionable message — to the first
// request, as a 500 with a Node stack trace in it:
//
//	Error: Cannot find module '@ondrift/sdk'
//	Require stack: - /runner/POST__default__expense/app.js
//
// Hit for real on 2026-07-27 building a first project.

func writeFn(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestVerifySDKManifest_EmptyDependenciesIsRejected(t *testing.T) {
	dir := writeFn(t, map[string]string{
		"h.js":         "const drift = require('@ondrift/sdk');\nfunction h() {}\nmodule.exports={h};\n",
		"package.json": `{"name":"fn","private":true,"dependencies":{}}`,
	})
	err := VerifySDKManifest(dir, "node")
	if err == nil {
		t.Fatal("a package.json that does not declare the SDK must be rejected at DEPLOY time, not at the first request")
	}
	for _, want := range []string{"does not declare", "@ondrift/sdk"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must say what is wrong and what to add; %q missing from: %v", want, err)
		}
	}
}

func TestVerifySDKManifest_DeclaredIsAccepted(t *testing.T) {
	dir := writeFn(t, map[string]string{
		"h.js":         "const drift = require('@ondrift/sdk');\nfunction h() {}\nmodule.exports={h};\n",
		"package.json": `{"dependencies":{"@ondrift/sdk":"github:ondrift/sdk#semver:*"}}`,
	})
	if err := VerifySDKManifest(dir, "node"); err != nil {
		t.Errorf("a correctly declared manifest must pass: %v", err)
	}
}

// The original behaviour must survive: no manifest at all is still the error
// that names the file to create.
func TestVerifySDKManifest_MissingManifestStillReported(t *testing.T) {
	dir := writeFn(t, map[string]string{
		"h.js": "const drift = require('@ondrift/sdk');\nfunction h() {}\nmodule.exports={h};\n",
	})
	err := VerifySDKManifest(dir, "node")
	if err == nil {
		t.Fatal("a missing package.json must still be reported")
	}
	if !strings.Contains(err.Error(), "has no package.json") {
		t.Errorf("got: %v", err)
	}
}

// A function that never touches the SDK needs no manifest — the guard must not
// start demanding one.
func TestVerifySDKManifest_NoSDKUsageNeedsNothing(t *testing.T) {
	dir := writeFn(t, map[string]string{"h.js": "function h() { return [200,'OK',{}]; }\nmodule.exports={h};\n"})
	if err := VerifySDKManifest(dir, "node"); err != nil {
		t.Errorf("a function not using the SDK needs no manifest: %v", err)
	}
}

// Go and Rust auto-provision the SDK and have no manifest to check.
func TestVerifySDKManifest_AutoProvisionedLanguagesSkip(t *testing.T) {
	for _, lang := range []string{"go", "rust"} {
		if err := VerifySDKManifest(t.TempDir(), lang); err != nil {
			t.Errorf("%s must skip the check: %v", lang, err)
		}
	}
}

// What you write in SOURCE is not what you write in the MANIFEST for Ruby and
// PHP, so the needles differ per language and must not be conflated.
func TestVerifySDKManifest_PerLanguageManifestNeedles(t *testing.T) {
	cases := []struct {
		lang, src, srcName, manifest, manifestName string
	}{
		{"ruby", "require 'drift'\ndef h; end\n", "h.rb",
			`gem "drift-sdk", git: "https://github.com/ondrift/sdk"`, "Gemfile"},
		{"php", "<?php\nuse Drift\\Backbone;\n", "h.php",
			`{"require":{"ondrift/sdk":"*"}}`, "composer.json"},
		{"python", "import drift\n", "h.py",
			"drift-sdk @ git+https://github.com/ondrift/sdk.git#subdirectory=python", "requirements.txt"},
	}
	for _, c := range cases {
		dir := writeFn(t, map[string]string{c.srcName: c.src, c.manifestName: c.manifest})
		if err := VerifySDKManifest(dir, c.lang); err != nil {
			t.Errorf("%s: a correctly declared %s must pass: %v", c.lang, c.manifestName, err)
		}
		bare := writeFn(t, map[string]string{c.srcName: c.src, c.manifestName: "# nothing declared\n"})
		if err := VerifySDKManifest(bare, c.lang); err == nil {
			t.Errorf("%s: a %s that declares nothing must be rejected", c.lang, c.manifestName)
		}
	}
}
