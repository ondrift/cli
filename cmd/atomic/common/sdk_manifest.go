// sdk_manifest.go — pre-deploy guard for a common footgun: a function that
// uses the Drift SDK but ships no dependency manifest to declare it. Without
// the manifest the build installs nothing, the artifact has no SDK, and the
// function fails at runtime with a cryptic "No module named 'drift'" (or the
// per-language equivalent). This turns that into a clear message at deploy
// time. Go and Rust auto-provision the SDK at build, so they aren't checked.
package atomic_common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type sdkManifestSpec struct {
	manifest    string   // the file that must declare the SDK
	ext         string   // source-file extension to scan
	needles     []string // any of these in the source ⇒ the function uses the SDK
	declaration string   // what the user should put in the manifest
}

var sdkManifestSpecs = map[string]sdkManifestSpec{
	"python": {
		manifest: "requirements.txt", ext: ".py",
		needles:     []string{"import drift"},
		declaration: "drift-sdk @ git+https://github.com/ondrift/sdk.git#subdirectory=python",
	},
	"node": {
		manifest: "package.json", ext: ".js",
		needles:     []string{"@ondrift/sdk"},
		declaration: `{ "dependencies": { "@ondrift/sdk": "github:ondrift/sdk#semver:*" } }`,
	},
	"ruby": {
		manifest: "Gemfile", ext: ".rb",
		needles:     []string{"require 'drift'", "require \"drift\""},
		declaration: "gem \"drift-sdk\", git: \"https://github.com/ondrift/sdk\", branch: \"master\", glob: \"ruby/*.gemspec\"",
	},
	"php": {
		manifest: "composer.json", ext: ".php",
		needles:     []string{`Drift\`},
		declaration: `{ "repositories": [{ "type": "vcs", "url": "https://github.com/ondrift/sdk" }], "require": { "ondrift/sdk": "*" } }`,
	},
}

// VerifySDKManifest returns an actionable error when a function's source uses
// the Drift SDK but the language's dependency manifest is missing. Languages
// that auto-provision the SDK (Go, Rust) return nil.
func VerifySDKManifest(dir, language string) error {
	spec, ok := sdkManifestSpecs[language]
	if !ok {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, spec.manifest)); err == nil {
		return nil // manifest present
	}
	if !sourceUsesSDK(dir, spec) {
		return nil // function doesn't touch the SDK — a manifest isn't required
	}
	return fmt.Errorf(
		"this function uses the Drift SDK but has no %s to declare it.\n"+
			"Create %s with:\n\n  %s\n\n"+
			"then deploy again (or run `drift atomic fetch` to resolve it locally first).",
		spec.manifest, spec.manifest, spec.declaration)
}

func sourceUsesSDK(dir string, spec sdkManifestSpec) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), spec.ext) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- controlled function dir (CLI-validated path)
		if err != nil {
			continue
		}
		content := string(data)
		for _, n := range spec.needles {
			if strings.Contains(content, n) {
				return true
			}
		}
	}
	return false
}
