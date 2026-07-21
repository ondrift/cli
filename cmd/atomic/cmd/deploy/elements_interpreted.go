// elements_interpreted.go — multi-function element deploys for the interpreted
// languages (Python/Node/Ruby/PHP). They all share one shape: stage the
// element's source + install its declared dependencies ONCE, then for each
// @atomic function generate that language's wrapper (which imports the handler
// from its module) and tar the staged dir into one artifact per function.
//
// This mirrors DeployGoElement (one dependency resolution per element, one
// artifact per function); the per-function step here is cheap (write wrapper +
// tar, no compile), so it runs sequentially.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
	"github.com/ondrift/cli/v2/common"
)

// interpretedLang carries the per-language bits the generic deployer needs.
// The import module is derived generically (file basename minus extension).
type interpretedLang struct {
	label   string                                                      // operator language label
	exts    map[string]bool                                             // source extensions
	install func(absFolder, stageDir string) error                      // copy manifest + install deps into stageDir
	wrapper func(stageDir, sourceModule, funcName, method string) error // write the per-function wrapper
}

var interpretedLangs = map[string]interpretedLang{
	"python": {label: "python", exts: map[string]bool{".py": true}, install: installPythonDeps, wrapper: generatePythonWrapper},
	"node":   {label: "node", exts: map[string]bool{".js": true, ".mjs": true, ".cjs": true}, install: installNodeDeps, wrapper: generateNodeWrapper},
	"ruby":   {label: "ruby", exts: map[string]bool{".rb": true}, install: installRubyDeps, wrapper: generateRubyWrapper},
	"php":    {label: "php", exts: map[string]bool{".php": true}, install: installPHPDeps, wrapper: generatePHPWrapper},
}

// DeployInterpretedElement builds and deploys every @atomic function in a
// non-Go (interpreted) element. Dependencies are installed once; each function
// then gets its own wrapper + archive.
func DeployInterpretedElement(el Element, digest string, quiet bool) error {
	lg, ok := interpretedLangs[el.Lang]
	if !ok {
		return fmt.Errorf("multi-function %s elements aren't built yet (element %q); "+
			"keep one function per folder for %s until it lands", el.Lang, el.Name, el.Lang)
	}
	for _, f := range el.Funcs {
		if f.Trigger != "http" && f.Trigger != "queue" {
			return fmt.Errorf("@atomic %s= triggers aren't wired in the deploy path yet "+
				"(function %s in element %q)", f.Trigger, f.SentinelName, el.Name)
		}
	}

	stageDir, err := os.MkdirTemp("", "drift-"+lg.label+"-element-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir) // #nosec G104

	// Stage the element's source files (one language, flat).
	entries, err := os.ReadDir(el.Dir)
	if err != nil {
		return fmt.Errorf("read element dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !lg.exts[filepath.Ext(e.Name())] {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(el.Dir, e.Name())) // #nosec G304
		if rerr != nil {
			return fmt.Errorf("read %s: %w", e.Name(), rerr)
		}
		if werr := os.WriteFile(filepath.Join(stageDir, e.Name()), data, 0o644); werr != nil { // #nosec G306
			return fmt.Errorf("write %s: %w", e.Name(), werr)
		}
	}

	// Install declared dependencies ONCE for the whole element.
	if ierr := lg.install(el.Dir, stageDir); ierr != nil {
		return ierr
	}

	userSrc, usErr := createUserSourceArchive(el.Dir, el.Name)
	if usErr == nil {
		defer os.Remove(userSrc) // #nosec G104
	} else {
		userSrc = ""
	}

	// Per function: write the wrapper bound to it, archive, and ship. Sequential
	// — the dependency install (the cost) is already done; tarring is cheap.
	var firstErr error
	for _, f := range el.Funcs {
		method, name := f.Method, f.Path
		if f.Trigger == "queue" {
			method, name = "queue", f.Method
		}
		sourceModule := strings.TrimSuffix(f.SourceFile, filepath.Ext(f.SourceFile))
		if werr := lg.wrapper(stageDir, sourceModule, f.SentinelName, method); werr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: generate wrapper: %w", f.MethodPath(), werr)
			}
			continue
		}
		archive, aerr := os.CreateTemp("", fmt.Sprintf("drift-%s-%s-*.tar.gz", lg.label, safeTmpName(name)))
		if aerr != nil {
			return fmt.Errorf("create archive temp: %w", aerr)
		}
		archivePath := archive.Name()
		archive.Close() // #nosec G104
		if terr := createTarGz(stageDir, archivePath); terr != nil {
			os.Remove(archivePath) // #nosec G104
			return fmt.Errorf("%s: archive: %w", f.MethodPath(), terr)
		}

		var triggers []TriggerSpec
		if f.Trigger == "queue" {
			triggers = []TriggerSpec{{Type: "queue", Source: f.Method, Method: "queue", PollMS: 500, MaxRetry: 3}}
		}
		serr := sendSourceToOperator(name, method, lg.label, f.Auth, el.Name,
			f.Stream, f.Secrets, archivePath, userSrc, triggers, digest)
		os.Remove(archivePath) // #nosec G104
		if serr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.MethodPath(), serr)
			}
			if !quiet {
				fmt.Printf("    %s %s\n", common.Cross(), f.MethodPath())
			}
			continue
		}
		if !quiet {
			fmt.Printf("    %s %s\n", common.Check(), f.MethodPath())
		}
	}
	return firstErr
}

// installPythonDeps installs the element's requirements.txt (the Drift SDK
// among them) into stageDir/vendor — the wrapper prepends vendor/ to sys.path.
// No-op when there's no requirements.txt.
func installPythonDeps(absFolder, stageDir string) error {
	reqPath := filepath.Join(absFolder, "requirements.txt")
	if _, err := os.Stat(reqPath); err != nil {
		return nil
	}
	// Stage requirements.txt next to the source (as node/php/ruby do their
	// manifests) so the install runs entirely within stageDir with relative
	// paths only — which keeps it bind-mountable when the build runs in a
	// container (absolute host paths in args wouldn't resolve at the /w mount).
	data, rerr := os.ReadFile(reqPath) // #nosec G304 -- controlled base dir
	if rerr != nil {
		return fmt.Errorf("read requirements.txt: %w", rerr)
	}
	if werr := os.WriteFile(filepath.Join(stageDir, "requirements.txt"), data, 0o644); werr != nil { // #nosec G306
		return fmt.Errorf("write staged requirements.txt: %w", werr)
	}
	// Mirror build_python.go: install the manifest verbatim into vendor/. No
	// --platform/--only-binary — a git-source dep (the SDK) isn't a prebuilt wheel.
	if out, err := runToolchain(toolchainCmd{lang: "python", dir: stageDir, name: "pip3", args: []string{"install", "-t", "vendor", "-r", "requirements.txt", "--quiet"}}); err != nil {
		return fmt.Errorf("pip install error: %w\n%s", err, string(out))
	}
	return nil
}

// installNodeDeps mirrors build_node.go: copies package.json (+ lock) into the
// stage and runs npm with linux platform resolution. No-op without a manifest.
func installNodeDeps(absFolder, stageDir string) error {
	data, rerr := os.ReadFile(filepath.Join(absFolder, "package.json")) // #nosec G304
	if rerr != nil {
		return nil
	}
	if werr := os.WriteFile(filepath.Join(stageDir, "package.json"), data, 0o644); werr != nil { // #nosec G306
		return fmt.Errorf("write staged package.json: %w", werr)
	}
	if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "package-lock.json")); lerr == nil { // #nosec G304
		_ = os.WriteFile(filepath.Join(stageDir, "package-lock.json"), lockData, 0o644) // #nosec G306
	}
	npmCPU := "x64"
	if runtime.GOARCH == "arm64" {
		npmCPU = "arm64"
	}
	if out, err := runToolchain(toolchainCmd{lang: "node", dir: stageDir, name: "npm", args: []string{"install", "--production", "--silent", "--os=linux", "--cpu=" + npmCPU}}); err != nil {
		return fmt.Errorf("npm install error: %w\n%s", err, string(out))
	}
	return nil
}

// installRubyDeps mirrors build_ruby.go: copies the Gemfile (+ lock) and runs
// `bundle install --standalone` under a host Ruby >= 3.0. No-op without a Gemfile.
func installRubyDeps(absFolder, stageDir string) error {
	gemfilePath := filepath.Join(absFolder, "Gemfile")
	if _, err := os.Stat(gemfilePath); err != nil {
		return nil
	}
	data, rerr := os.ReadFile(gemfilePath) // #nosec G304
	if rerr != nil {
		return fmt.Errorf("read Gemfile: %w", rerr)
	}
	if werr := os.WriteFile(filepath.Join(stageDir, "Gemfile"), data, 0o644); werr != nil { // #nosec G306
		return fmt.Errorf("write staged Gemfile: %w", werr)
	}
	if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "Gemfile.lock")); lerr == nil { // #nosec G304
		_ = os.WriteFile(filepath.Join(stageDir, "Gemfile.lock"), lockData, 0o644) // #nosec G306
	}
	// --standalone emits vendor/bundle/bundler/setup.rb which the wrapper loads
	// without requiring bundler at runtime. BUNDLE_PATH/WITHOUT go through env so
	// this works across bundler 2.x–4.x. Host build resolves a Ruby >= 3.0 (Apple's
	// 2.6 is too old); the container build uses the image's bundle and needs none.
	tc := toolchainCmd{
		lang: "ruby", dir: stageDir, name: "bundle",
		args: []string{"install", "--standalone", "--quiet"},
		env:  map[string]string{"BUNDLE_PATH": "vendor/bundle", "BUNDLE_WITHOUT": "development:test"},
	}
	rbVer := "image"
	if !toolchainContainerMode {
		rb, ferr := atomic_common.FindRuby()
		if ferr != nil {
			return ferr
		}
		tc.hostName, tc.hostPath, rbVer = rb.Bundle, rb.BinDir, rb.Version
	}
	if out, err := runToolchain(tc); err != nil {
		return fmt.Errorf("bundle install error (ruby %s): %w\n%s", rbVer, err, string(out))
	}
	return nil
}

// installPHPDeps mirrors build_php.go: copies composer.json (+ lock) and runs
// `composer install --no-dev`. No-op without a composer.json.
func installPHPDeps(absFolder, stageDir string) error {
	composerPath := filepath.Join(absFolder, "composer.json")
	if _, serr := os.Stat(composerPath); serr != nil {
		return nil
	}
	data, rerr := os.ReadFile(composerPath) // #nosec G304
	if rerr != nil {
		return fmt.Errorf("read composer.json: %w", rerr)
	}
	if werr := os.WriteFile(filepath.Join(stageDir, "composer.json"), data, 0o644); werr != nil { // #nosec G306
		return fmt.Errorf("write staged composer.json: %w", werr)
	}
	if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "composer.lock")); lerr == nil { // #nosec G304
		_ = os.WriteFile(filepath.Join(stageDir, "composer.lock"), lockData, 0o644) // #nosec G306
	}
	if out, err := runToolchain(toolchainCmd{lang: "php", dir: stageDir, name: "composer", args: []string{"install", "--no-dev", "--ignore-platform-reqs", "--quiet", "--no-interaction"}}); err != nil {
		return fmt.Errorf("composer install error: %w\n%s", err, string(out))
	}
	return nil
}
