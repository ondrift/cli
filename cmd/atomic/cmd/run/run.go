// run.go — `drift atomic run <dir>`. Local development server with hot
// reload. Mirrors the deploy.go shape — six per-language paths
// (Go/Rust/Python/Node/Ruby/PHP) sharing one fsnotify-based file
// watcher + one debounce + one in-process subprocess supervisor.
//
// Per-language `generate*` methods on the `devRunner` struct write
// the wrapper into the user's source directory; per-language
// `buildAndRun*` methods (or the shared `runInterpreted`) start the
// subprocess. On every save (400ms-debounced), the runner kills the
// child, regenerates the wrapper, and rebuilds.
//
// As with deploy.go, a future refactor could split the per-language
// methods into one file per language for readability.
package atomic_cmd

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

// ==========================================================
// Embedded templates: Go server + Python/Node wrappers + SDKs
// ==========================================================

//go:embed default/server_post.txt
var defaultGolangServerPost string

//go:embed default/server_get.txt
var defaultGolangServerGet string

//go:embed default/wrapper_post_python.txt
var wrapperPostPython string

//go:embed default/wrapper_get_python.txt
var wrapperGetPython string

//go:embed default/wrapper_post_node.txt
var wrapperPostNode string

//go:embed default/wrapper_get_node.txt
var wrapperGetNode string

//go:embed default/wrapper_post_ruby.txt
var wrapperPostRuby string

//go:embed default/wrapper_get_ruby.txt
var wrapperGetRuby string

//go:embed default/wrapper_post_php.txt
var wrapperPostPHP string

//go:embed default/wrapper_get_php.txt
var wrapperGetPHP string

//go:embed default/wrapper_post_rust.txt
var wrapperPostRust string

//go:embed default/wrapper_get_rust.txt
var wrapperGetRust string

//go:embed default/cargo_template.toml
var cargoTemplate string

// ==========================================================
// Public command factory
// ==========================================================

func Run() *cobra.Command {
	var portOverride int
	var quiet bool
	cmd := &cobra.Command{
		Use:     "run [function folder]",
		Short:   "Run an Atomic function locally with hot reload",
		Example: "  drift atomic run ./send-email\n  drift atomic run ./create-invoice",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			srcDir := args[0]
			absSrc, err := filepath.Abs(srcDir)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}

			// Parse metadata (method, name, auth, env)
			meta, err := atomic_common.ParseAtomicMetadataFromDir(absSrc)
			if err != nil {
				return fmt.Errorf("parse Atomic metadata: %w", err)
			}
			// Resolve method/name based on trigger type. For queue and
			// cron triggers we synthesize a POST handler at the
			// directory's basename so the function runs as an HTTP
			// server you can POST to by hand to test it. The user's handler
			// signature is unchanged — only the wrapper boundary differs.
			var method, name string
			switch meta.Trigger {
			case "http":
				method, name = meta.Method, meta.Path
			case "queue", "cron":
				method = "post"
				name = filepath.Base(absSrc)
			default:
				return fmt.Errorf("@atomic %s= triggers can't be run locally yet", meta.Trigger)
			}

			// Detect language from the annotated source file.
			language, sourceFile, err := atomic_common.DetectLanguage(absSrc)
			if err != nil {
				return fmt.Errorf("detect language: %w", err)
			}

			// Port resolution order:
			//   1. --port flag (explicit)
			//   2. `port=` token on the @atomic annotation
			//   3. fallback 3000
			port := portOverride
			if port == 0 {
				port = detectPortFromAnnotation(absSrc)
			}
			if port == 0 {
				port = 3000
			}

			if !quiet {
				fmt.Printf("▶️  Running %s function '%s /%s' on http://localhost:%d\n", language, strings.ToUpper(method), name, port)
			}

			// Create a persistent temp workspace for this run session
			workDir, err := os.MkdirTemp("", "drift_atomic_run_*")
			if err != nil {
				return fmt.Errorf("create temp dir: %w", err)
			}
			// Clean up on exit
			defer os.RemoveAll(workDir)

			// Initial sync & start
			runner := &devRunner{
				srcDir:     absSrc,
				workDir:    workDir,
				method:     strings.ToLower(method),
				name:       name,
				port:       port,
				language:   language,
				sourceFile: sourceFile,
				procLock:   &sync.Mutex{},
			}

			if err := runner.syncWorkspace(); err != nil {
				return err
			}
			if err := runner.generateMain(); err != nil {
				return err
			}
			if err := runner.installDeps(); err != nil {
				log.Printf("dependency install warning: %v", err)
			}
			if err := runner.buildAndRun(); err != nil {
				log.Printf("initial build failed, staying in watch mode: %v", err)
			}

			// Start watching for changes (recursive)
			return runner.watchAndReload()
		},
	}

	cmd.Flags().IntVar(&portOverride, "port", 0, "Bind to this port (overrides any port= on the @atomic annotation; default 3000)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress the startup banner")

	return cmd
}

// ==========================================================
// devRunner orchestrates sync → generate → (install deps) → build → run → reload
// ==========================================================

type devRunner struct {
	srcDir     string
	workDir    string
	method     string // get/post/...
	name       string // route/resource name
	port       int
	language   string // "native", "python", "node", "ruby", "php", "rust"
	sourceFile string // filename with extension (e.g., "checkout.py")

	proc     *exec.Cmd
	procLock *sync.Mutex
}

// ---------- generateMain ----------

func (r *devRunner) generateMain() error {
	switch r.language {
	case "python":
		return r.generatePython()
	case "node":
		return r.generateNode()
	case "ruby":
		return r.generateRuby()
	case "php":
		return r.generatePHP()
	case "rust":
		return r.generateRust()
	default:
		return r.generateGo()
	}
}

// installDeps installs the user's declared dependencies (the Drift SDK
// among them) for the interpreted languages into the run workspace, when
// a manifest is present. The CLI is SDK-agnostic — it installs whatever
// the manifest declares. Go and Rust resolve their deps in buildAndRun.
func (r *devRunner) installDeps() error {
	switch r.language {
	case "python":
		reqPath := filepath.Join(r.srcDir, "requirements.txt")
		if _, err := os.Stat(reqPath); err != nil {
			return nil
		}
		fmt.Println("📦 Installing Python dependencies...")
		return runCmd(r.workDir, "pip3", "install", "-t", filepath.Join(r.workDir, "vendor"), "-r", reqPath, "--quiet")

	case "node":
		pkgPath := filepath.Join(r.srcDir, "package.json")
		if _, err := os.Stat(pkgPath); err != nil {
			return nil
		}
		if data, err := os.ReadFile(pkgPath); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "package.json"), data, 0o644) // #nosec G306 -- build-time artefact
		}
		if lockData, err := os.ReadFile(filepath.Join(r.srcDir, "package-lock.json")); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "package-lock.json"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		fmt.Println("📦 Installing Node dependencies...")
		return runCmd(r.workDir, "npm", "install", "--silent")

	case "ruby":
		gemfilePath := filepath.Join(r.srcDir, "Gemfile")
		if _, err := os.Stat(gemfilePath); err != nil {
			return nil
		}
		if data, err := os.ReadFile(gemfilePath); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "Gemfile"), data, 0o644) // #nosec G306 -- build-time artefact
		}
		if lockData, err := os.ReadFile(filepath.Join(r.srcDir, "Gemfile.lock")); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "Gemfile.lock"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		rb, err := atomic_common.FindRuby()
		if err != nil {
			return err
		}
		fmt.Printf("📦 Installing Ruby dependencies (ruby %s)...\n", rb.Version)
		return runCmdEnv(r.workDir, []string{
			"PATH=" + rb.BinDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"BUNDLE_PATH=vendor/bundle",
			"BUNDLE_WITHOUT=development:test",
		}, rb.Bundle, "install", "--standalone", "--quiet")

	case "php":
		composerPath := filepath.Join(r.srcDir, "composer.json")
		if _, err := os.Stat(composerPath); err != nil {
			return nil
		}
		if data, err := os.ReadFile(composerPath); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "composer.json"), data, 0o644) // #nosec G306 -- build-time artefact
		}
		if lockData, err := os.ReadFile(filepath.Join(r.srcDir, "composer.lock")); err == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(r.workDir, "composer.lock"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		fmt.Println("📦 Installing PHP dependencies...")
		return runCmd(r.workDir, "composer", "install", "--no-dev", "--quiet", "--no-interaction")

	default:
		return nil // Go and Rust deps handled in buildAndRun
	}
}

// ---------- buildAndRun ----------

func (r *devRunner) buildAndRun() error {
	switch r.language {
	case "python":
		return r.runInterpreted("python3", "app.py")
	case "node":
		return r.runInterpreted("node", "app.js")
	case "ruby":
		rb, err := atomic_common.FindRuby()
		if err != nil {
			return err
		}
		return r.runInterpreted(rb.Ruby, "app.rb")
	case "php":
		return r.runInterpreted("php", "app.php")
	case "rust":
		return r.buildAndRunRust()
	default:
		return r.buildAndRunGo()
	}
}

func (r *devRunner) runInterpreted(interpreter, entryPoint string) error {
	r.stopProcess()

	envs, _ := readDotEnv(filepath.Join(r.srcDir, ".env"))
	runEnv := os.Environ()
	runEnv = append(runEnv, envs...)
	runEnv = append(runEnv, fmt.Sprintf("PORT=%d", r.port))

	cmd := exec.Command(interpreter, entryPoint) // #nosec G204 — controlled interpreter + entry point
	cmd.Dir = r.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = runEnv

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", interpreter, err)
	}

	r.procLock.Lock()
	r.proc = cmd
	r.procLock.Unlock()

	go func() {
		_ = cmd.Wait()
	}()

	fmt.Printf("✅ Server started (PID %d)\n", cmd.Process.Pid)
	return nil
}

// ---------- stop / sync / rebuild / watch ----------

func (r *devRunner) stopProcess() {
	r.procLock.Lock()
	defer r.procLock.Unlock()
	if r.proc != nil && r.proc.Process != nil {
		// Try graceful stop on POSIX
		if runtime.GOOS != "windows" {
			_ = r.proc.Process.Signal(os.Interrupt)
			done := make(chan struct{})
			go func() { _ = r.proc.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(800 * time.Millisecond):
				_ = r.proc.Process.Kill()
			}
		} else {
			_ = r.proc.Process.Kill()
		}
		r.proc = nil
	}
}

func (r *devRunner) syncWorkspace() error {
	// Copy entire srcDir → workDir, filtering out junk. Then we overwrite main/app later.
	filters := []string{`.git`, `.idea`, `.vscode`, `node_modules`, `vendor`, `app`, `bin`, `dist`}
	if err := copyDir(r.srcDir, r.workDir, filters); err != nil {
		return fmt.Errorf("sync workspace: %w", err)
	}
	return nil
}

func (r *devRunner) rebuild() error {
	// Re-sync to pick up any new files/deletions
	if err := r.syncWorkspace(); err != nil {
		return err
	}
	if err := r.generateMain(); err != nil {
		return err
	}
	if err := r.buildAndRun(); err != nil {
		log.Printf("build/run failed: %v", err)
		return err
	}
	return nil
}

func (r *devRunner) watchAndReload() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Recursively watch the srcDir
	if err := addRecursive(watcher, r.srcDir); err != nil {
		return err
	}

	// Debounce changes to avoid burst rebuilds
	var (
		debounceMu sync.Mutex
		debounce   *time.Timer
		trigger    = func() {
			debounceMu.Lock()
			defer debounceMu.Unlock()
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(400*time.Millisecond, func() {
				fmt.Println("\n🔄 Change detected — rebuilding…")
				if err := r.rebuild(); err != nil {
					// keep watching even if rebuild fails
				}
			})
		}
	)

	// Handle Ctrl+C to clean up
	go func() {
		sig := make(chan os.Signal, 1)
		// signal.Notify is platform-dependent; keep it simple
		// (caller process handles SIGINT; we just ensure child is stopped on exit)
		<-sig
		r.stopProcess()
		os.Exit(0)
	}()

	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return errors.New("watcher closed")
			}
			// Ignore temporary/editor files
			if isIgnorablePath(ev.Name) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				trigger()
				// If a new directory was created, start watching it
				if ev.Op&fsnotify.Create != 0 {
					if stat, err := os.Stat(ev.Name); err == nil && stat.IsDir() {
						_ = addRecursive(watcher, ev.Name)
					}
				}
			}
		case err := <-watcher.Errors:
			if err != nil {
				log.Printf("watcher error: %v", err)
			}
		}
	}
}

// ==========================================================
// Utilities
// ==========================================================

// #nosec G204 -- the slice's entire purpose is to spawn user-supplied subprocesses. Sandboxed via the FROM scratch image (no shell), read-only rootfs, all capabilities dropped, non-root uid 1000, RLIMIT_AS memory cap, semaphore-bounded concurrency, per-invocation tmpdir isolation. This is the platform's design, not a bug.
func runCmd(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runCmdEnv is runCmd with extra environment variables appended to the
// inherited environment (later entries win on duplicate keys, so callers can
// override e.g. PATH). Used for toolchains that need a specific interpreter on
// PATH or config via env (e.g. host Ruby + BUNDLE_PATH).
func runCmdEnv(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...) // #nosec G204 -- name is a discovered toolchain binary, not user input
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func addRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isFilteredDir(d.Name()) {
				return filepath.SkipDir
			}
			return w.Add(p)
		}
		return nil
	})
}

func isFilteredDir(name string) bool {
	switch name {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", "bin", "dist":
		return true
	default:
		return false
	}
}

func isIgnorablePath(p string) bool {
	base := filepath.Base(p)
	// Common editor swap files and temp artifacts
	ignorable := []string{".DS_Store", "4913"}
	for _, s := range ignorable {
		if base == s {
			return true
		}
	}
	// Vim/Emacs/JetBrains temp files
	if strings.HasPrefix(base, ".#") || strings.HasSuffix(base, "~") || strings.HasSuffix(base, ".swp") {
		return true
	}
	return false
}

func copyDir(src, dst string, exclude []string) error {
	// Ensure dst exists
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}

	excluded := map[string]struct{}{}
	for _, e := range exclude {
		excluded[e] = struct{}{}
	}

	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		// Skip excluded directories anywhere in the path
		parts := strings.Split(rel, string(os.PathSeparator))
		for _, p := range parts {
			if _, ok := excluded[p]; ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		// Copy files
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 — CLI tool reads user's own project files by design
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}

	out, err := os.Create(dst) // #nosec G304 — CLI tool writes to user's own workspace by design
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// Simple .env reader (no external deps). Lines like KEY=VALUE, ignoring comments and empty lines.
func readDotEnv(path string) ([]string, error) {
	f, err := os.Open(path) // #nosec G304 — CLI tool reads user's .env file by design
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var envs []string
	scanner := bufio.NewScanner(f)
	re := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)\s*$`)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := re.FindStringSubmatch(line)
		if len(m) == 3 {
			key := m[1]
			val := m[2]
			// strip optional surrounding quotes
			val = strings.Trim(val, "\"'")
			envs = append(envs, fmt.Sprintf("%s=%s", key, val))
		}
	}
	return envs, nil
}

// Attempt to read `port=####` from the annotation line in any source file at src root.
func detectPortFromAnnotation(src string) int {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".go" && ext != ".py" && ext != ".js" && ext != ".rb" && ext != ".php" && ext != ".rs" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, e.Name())) // #nosec G304 — CLI tool reads user's own source files by design
		line := firstAnnotationLine(string(b))
		if line == "" {
			continue
		}
		// naive parse for `port=XXXX` tokens
		for _, tok := range strings.Fields(line) {
			if strings.HasPrefix(tok, "port=") {
				p := strings.TrimPrefix(tok, "port=")
				if n, err := strconv.Atoi(p); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstAnnotationLine(src string) string {
	// Expect a line like:
	//   // @atomic http=get:users auth=apikey port=8081   (Go)
	//   # @atomic http=get:users auth=apikey port=8081    (Python/Node)
	for _, ln := range strings.Split(src, "\n") {
		s := strings.TrimSpace(ln)
		if strings.HasPrefix(s, "// @atomic ") || strings.HasPrefix(s, "# @atomic ") {
			return s
		}
	}
	return ""
}
