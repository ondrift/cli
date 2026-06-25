// build_go.go — Go-language build path for `drift atomic deploy`.
// Compiles the user's Go source and produces a static linux binary at
// `<dir>/app`. The Drift SDK is pulled at its latest tag via `go get`
// (the root module is named to dodge the legacy nested-module pseudo-
// versions — see atomic_common.DriftGoModule); no version is ever pinned.
// Other deps come from the user's go.mod. The build runs in a private
// tempdir copy so the user's working tree is never modified.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

// buildGo is the legacy single-function path: stage the folder, then build one
// binary bound to the conventionally-derived handler name. The Element path
// (buildGoElementStage + buildGoEntrypoint) is the multi-function generalization.
func buildGo(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "native")
	buildDir, err := buildGoElementStage(absFolder, name)
	if err != nil {
		return "", err
	}
	// Deliberately no RemoveAll(buildDir) on success: the returned binary
	// lives inside it and the caller's `defer os.Remove` cleans the binary
	// after upload; the empty parent leaks until the CLI process exits.
	bin, err := buildGoEntrypoint(buildDir, funcName, method, "app")
	if err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", err
	}
	return bin, nil
}

// buildGoElementStage copies a Go Element's package into a fresh host-side
// tempdir and resolves its dependencies ONCE (go mod init/get/tidy). The
// caller then compiles one binary per function into it via buildGoEntrypoint
// and is responsible for RemoveAll(buildDir). Each call gets its own tempdir,
// so parallel deploys never race; the user's source is never modified.
func buildGoElementStage(srcDir, name string) (string, error) {
	buildDir, err := os.MkdirTemp("", "drift-go-build-")
	if err != nil {
		return "", fmt.Errorf("create build tempdir: %w", err)
	}
	if err := copyGoSourceFiles(srcDir, buildDir); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("copy build context: %w", err)
	}

	// Ensure a module exists (user functions may ship without a go.mod).
	if _, statErr := os.Stat(filepath.Join(buildDir, "go.mod")); statErr != nil {
		if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"mod", "init", "atomic/" + safeTmpName(name)}}); err != nil {
			os.RemoveAll(buildDir) // #nosec G104
			return "", fmt.Errorf("go mod init error: %w\n%s", err, string(out))
		}
	}

	// Pull the published SDK at its latest tag. The root module must be named
	// explicitly (see atomic_common.DriftGoModule) to dodge the legacy nested
	// `…/sdk/go` pseudo-version module. No version is pinned — @latest tracks
	// new tags, so a new SDK release never touches the CLI.
	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"get", atomic_common.DriftGoModule + "@latest"}}); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go get %s@latest error: %w\n%s", atomic_common.DriftGoModule, err, string(out))
	}

	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"mod", "tidy"}}); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go mod tidy error: %w\n%s", err, string(out))
	}

	return buildDir, nil
}

// buildGoEntrypoint generates a main bound to funcName in the already-staged
// buildDir and compiles one static linux binary named binBase, returning its
// path. The staged (tidied) package is reused, so per-function cost is just
// the compile + link — Go's build cache makes every call after the first cheap.
func buildGoEntrypoint(buildDir, funcName, method, binBase string) (string, error) {
	if err := generateMain(buildDir, funcName, method); err != nil {
		return "", fmt.Errorf("failed to generate main.go: %w", err)
	}
	if out, err := runToolchain(toolchainCmd{lang: "go", dir: buildDir, name: "go", args: []string{"build", "-o", binBase}, env: map[string]string{"GOOS": "linux", "CGO_ENABLED": "0"}}); err != nil {
		return "", fmt.Errorf("go build error (%s): %w\n%s", funcName, err, string(out))
	}
	return filepath.Join(buildDir, binBase), nil
}

// buildGoEntrypointIsolated compiles one function's entrypoint in its OWN fresh
// build dir (a copy of the staged package + a main bound to funcName), so it is
// safe to call CONCURRENTLY for an element's functions. They share the module
// cache (deps already resolved by buildGoElementStage) and Go's content-
// addressed build cache (the package compiles once; the rest only re-link), so
// parallelism turns the per-function link into the only marginal cost. Returns
// the binary path and the fn dir; the caller must RemoveAll(fnDir).
func buildGoEntrypointIsolated(stageDir, funcName, method, binBase string) (bin, fnDir string, err error) {
	fnDir, err = os.MkdirTemp("", "drift-go-fn-")
	if err != nil {
		return "", "", fmt.Errorf("create fn build dir: %w", err)
	}
	if err = copyGoSourceFiles(stageDir, fnDir); err != nil {
		os.RemoveAll(fnDir) // #nosec G104
		return "", "", fmt.Errorf("copy staged package: %w", err)
	}
	bin, err = buildGoEntrypoint(fnDir, funcName, method, binBase)
	if err != nil {
		os.RemoveAll(fnDir) // #nosec G104
		return "", "", err
	}
	return bin, fnDir, nil
}

// copyGoSourceFiles copies the top-level Go source files plus
// go.mod / go.sum from src to dst. Subdirectories are not copied —
// the build context for a single Atomic function is always flat
// (one Go package, one go.mod). Artefacts left behind by prior builds
// (app binary, main.go) live at the top level and are deliberately
// excluded so the tempdir starts clean.
func copyGoSourceFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read source dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Allowlist: only Go source + module files. Skips main.go,
		// app, and anything else a prior build may have left behind.
		if name == "main.go" || name == "app" {
			continue
		}
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" {
			continue
		}
		// #nosec G304 -- src is a CLI-validated absolute path; name comes from filesystem readdir, not user input
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
