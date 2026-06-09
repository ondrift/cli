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
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

func buildGo(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "native")

	// Each buildGo invocation works in its own host-side scratch
	// directory so parallel `drift project deploy` runs never race on
	// go.mod / go.sum mutation of a shared source folder. The user's
	// source dir is never modified.
	buildDir, err := os.MkdirTemp("", "drift-go-build-")
	if err != nil {
		return "", fmt.Errorf("create build tempdir: %w", err)
	}
	// Note: we deliberately do NOT defer-RemoveAll(buildDir). The
	// returned binary path lives inside buildDir, and the caller's own
	// `defer os.Remove(sourcePath)` (in deploy.go) removes the binary
	// after upload. The empty parent dir leaks until the CLI process
	// exits — acceptable for a short-lived deploy run.

	if err := copyGoSourceFiles(absFolder, buildDir); err != nil {
		os.RemoveAll(buildDir) // #nosec G104 -- best-effort tempdir cleanup on error path
		return "", fmt.Errorf("copy build context: %w", err)
	}

	if err := generateMain(buildDir, funcName, method); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("failed to generate main.go: %w", err)
	}

	// Ensure a module exists (user functions may ship without a go.mod;
	// templates always have one).
	if _, statErr := os.Stat(filepath.Join(buildDir, "go.mod")); statErr != nil {
		initCmd := exec.Command("go", "mod", "init", "atomic/"+safeTmpName(name)) // #nosec G204
		initCmd.Dir = buildDir
		if out, err := initCmd.CombinedOutput(); err != nil {
			os.RemoveAll(buildDir) // #nosec G104
			return "", fmt.Errorf("go mod init error: %w\n%s", err, string(out))
		}
	}

	// Pull the published SDK at its latest tag. The root module must be
	// named explicitly (see atomic_common.DriftGoModule): a bare `go mod
	// tidy` would otherwise resolve a stale pseudo-version of the repo's
	// legacy nested `…/sdk/go` module. No version is pinned — @latest tracks
	// new tags, so a new SDK release never touches the CLI.
	getCmd := exec.Command("go", "get", atomic_common.DriftGoModule+"@latest") // #nosec G204
	getCmd.Dir = buildDir
	if out, err := getCmd.CombinedOutput(); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go get %s@latest error: %w\n%s", atomic_common.DriftGoModule, err, string(out))
	}

	tidyCmd := exec.Command("go", "mod", "tidy") // #nosec G204
	tidyCmd.Dir = buildDir
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go mod tidy error: %w\n%s", err, string(out))
	}

	buildCmd := exec.Command("go", "build", "-o", "app") // #nosec G204
	buildCmd.Dir = buildDir
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		os.RemoveAll(buildDir) // #nosec G104
		return "", fmt.Errorf("go build error: %w\n%s", err, string(out))
	}

	return filepath.Join(buildDir, "app"), nil
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
