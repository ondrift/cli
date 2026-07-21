// fetch.go — `drift atomic fetch [path]`. Walks the tree from a path
// (default: cwd), finds every Atomic function (a directory holding a file
// with an @atomic annotation), and runs that language's dependency
// resolution *in place* — a "tidy" for all functions, all languages, in
// one command. After it, an editor/LSP sees the deps and `drift atomic
// run`/`deploy` have nothing left to fetch.
//
// The CLI stays SDK-agnostic: for the interpreted languages it just runs
// the package manager against whatever the function's manifest declares.
// Go is the one exception — its go.mod is intentionally bare, so (exactly
// like the build path) we name the SDK's root module for `go get` to dodge
// the legacy nested-module pseudo-versions. See atomic_common.DriftGoModule.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"

	"github.com/spf13/cobra"
)

func Fetch() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch [path]",
		Short: "Resolve dependencies for every Atomic function under a path",
		Long: "Recursively finds every Atomic function under the given path " +
			"(default: current directory) and resolves its dependencies in " +
			"place using that language's package manager — a 'tidy' for all " +
			"functions across all six languages at once.",
		Example: "  drift atomic fetch\n  drift atomic fetch ./intake-review",
		GroupID: "development",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runFetch(root)
		},
	}
}

type fetchTarget struct {
	dir      string
	language string // "native"(go) | python | node | ruby | php | rust
}

// skipDirs are never descended into during discovery: VCS metadata and the
// per-language dependency directories fetch itself produces.
var fetchSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"target": true, ".bundle": true,
}

func runFetch(root string) error {
	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("path %q: %w", root, err)
	}

	targets, err := discoverFunctions(root)
	if err != nil {
		return fmt.Errorf("scanning for functions: %w", err)
	}
	if len(targets) == 0 {
		fmt.Printf("No Atomic functions found under %s\n", root)
		return nil
	}

	fmt.Printf("Found %d Atomic function(s). Resolving dependencies...\n\n", len(targets))
	var failed int
	for _, t := range targets {
		fmt.Printf("  %-32s %-6s … ", t.dir, fetchLangLabel(t.language))
		if ferr := fetchOne(t); ferr != nil {
			failed++
			fmt.Println("✗")
			for _, line := range strings.Split(strings.TrimRight(ferr.Error(), "\n"), "\n") {
				fmt.Printf("      %s\n", line)
			}
			continue
		}
		fmt.Println("✓")
	}

	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%d of %d function(s) failed to resolve", failed, len(targets))
	}
	fmt.Printf("✅ All %d function(s) resolved.\n", len(targets))
	return nil
}

// discoverFunctions walks root and returns every directory that holds an
// Atomic function, with its detected language.
func discoverFunctions(root string) ([]fetchTarget, error) {
	var targets []fetchTarget
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries; keep scanning
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && fetchSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		// DetectLanguage returns the language only when the dir contains a
		// file carrying an @atomic annotation; otherwise it errors and we
		// keep walking.
		if lang, _, derr := atomic_common.DetectLanguage(p); derr == nil {
			targets = append(targets, fetchTarget{dir: p, language: lang})
			return filepath.SkipDir // functions don't nest; don't descend
		}
		return nil
	})
	sort.Slice(targets, func(i, j int) bool { return targets[i].dir < targets[j].dir })
	return targets, err
}

func fetchOne(t fetchTarget) error {
	switch t.language {
	case "native":
		return fetchGo(t.dir)
	case "node":
		return fetchNode(t.dir)
	case "python":
		return fetchPython(t.dir)
	case "ruby":
		return fetchRuby(t.dir)
	case "php":
		return fetchPHP(t.dir)
	case "rust":
		return fetchRust(t.dir)
	default:
		return fmt.Errorf("unsupported language %q", t.language)
	}
}

// runInDir runs name+args with cwd=dir, optional extra env appended to the
// inherited environment, and returns combined output for error reporting.
func runInDir(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- name is a fixed toolchain binary; args are constants or discovered paths
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func fetchGo(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		if out, e := runInDir(dir, nil, "go", "mod", "init", "atomic/"+filepath.Base(dir)); e != nil {
			return fmt.Errorf("go mod init: %w\n%s", e, out)
		}
	}
	// Name the SDK's root module so `go get` resolves the latest real tag
	// rather than a stale legacy-nested-module pseudo-version. No version pin.
	if out, e := runInDir(dir, nil, "go", "get", atomic_common.DriftGoModule+"@latest"); e != nil {
		return fmt.Errorf("go get %s@latest: %w\n%s", atomic_common.DriftGoModule, e, out)
	}
	if out, e := runInDir(dir, nil, "go", "mod", "tidy"); e != nil {
		return fmt.Errorf("go mod tidy: %w\n%s", e, out)
	}
	return nil
}

func fetchNode(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return nil // nothing declared
	}
	if out, e := runInDir(dir, nil, "npm", "install", "--silent"); e != nil {
		return fmt.Errorf("npm install: %w\n%s", e, out)
	}
	return nil
}

func fetchPython(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err != nil {
		return nil
	}
	if out, e := runInDir(dir, nil, "pip3", "install", "-t", "vendor", "-r", "requirements.txt", "--quiet"); e != nil {
		return fmt.Errorf("pip install: %w\n%s", e, out)
	}
	return nil
}

func fetchRuby(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "Gemfile")); err != nil {
		return nil
	}
	rb, err := atomic_common.FindRuby()
	if err != nil {
		return err
	}
	env := []string{
		"PATH=" + rb.BinDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"BUNDLE_PATH=vendor/bundle",
		"BUNDLE_WITHOUT=development:test",
	}
	if out, e := runInDir(dir, env, rb.Bundle, "install", "--standalone", "--quiet"); e != nil {
		return fmt.Errorf("bundle install (ruby %s): %w\n%s", rb.Version, e, out)
	}
	return nil
}

func fetchPHP(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "composer.json")); err != nil {
		return nil
	}
	if out, e := runInDir(dir, nil, "composer", "install", "--no-dev", "--no-interaction", "--quiet"); e != nil {
		return fmt.Errorf("composer install: %w\n%s", e, out)
	}
	return nil
}

func fetchRust(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err != nil {
		return nil
	}
	if out, e := runInDir(dir, nil, "cargo", "fetch"); e != nil {
		return fmt.Errorf("cargo fetch: %w\n%s", e, out)
	}
	return nil
}

// fetchLangLabel renders the user-facing language name ("native" is Go).
func fetchLangLabel(language string) string {
	if language == "native" {
		return "go"
	}
	return language
}
