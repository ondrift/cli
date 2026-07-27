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
	if !sourceUsesSDK(dir, spec) {
		return nil // function doesn't touch the SDK — a manifest isn't required
	}

	manifestPath := filepath.Join(dir, spec.manifest)
	data, err := os.ReadFile(manifestPath) // #nosec G304 -- controlled function dir (CLI-validated path)
	if err != nil {
		return fmt.Errorf(
			"this function uses the Drift SDK but has no %s to declare it.\n"+
				"Create %s with:\n\n  %s\n\n"+
				"then deploy again (or run `drift atomic fetch` to resolve it locally first).",
			spec.manifest, spec.manifest, spec.declaration)
	}

	// Present is not the same as DECLARING it. This checked only that the file
	// existed, so a package.json carrying `"dependencies": {}` sailed through
	// and the failure moved from deploy time — one clear message — to the first
	// request, as a 500 with a Node stack trace in it:
	//
	//   Error: Cannot find module '@ondrift/sdk'
	//
	// The string the manifest has to contain is the same needle already used to
	// detect SDK usage in the source, so there is nothing new to keep in step.
	for _, n := range spec.manifestNeedles() {
		if strings.Contains(string(data), n) {
			return nil
		}
	}
	return fmt.Errorf(
		"this function uses the Drift SDK, and %s exists but does not declare it — "+
			"so the dependency install resolves nothing and the function fails at its first invocation "+
			"with \"cannot find module\".\nAdd to %s:\n\n  %s\n",
		spec.manifest, spec.manifest, spec.declaration)
}

// manifestNeedles is what the MANIFEST must mention to count as declaring the
// SDK. Usually the same token looked for in source; Ruby and PHP differ because
// what you write in source ("require 'drift'", "Drift\") is not what you write
// in the Gemfile/composer.json.
func (s sdkManifestSpec) manifestNeedles() []string {
	switch s.manifest {
	case "Gemfile":
		return []string{"drift-sdk"}
	case "composer.json":
		return []string{"ondrift/sdk"}
	case "requirements.txt":
		return []string{"drift-sdk", "ondrift/sdk"}
	default:
		return s.needles
	}
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
