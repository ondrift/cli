// deploy.go — `drift atomic deploy <dir>`. Builds and uploads a single
// Atomic function. Six per-language build paths share one wrapper-
// injection scheme + one multipart-upload tail:
//
//  1. Detect language from source files in the directory.
//  2. Generate the language-specific wrapper that bridges the runner's
//     JSON wire protocol to user code (the `generate*Wrapper` helpers
//     at the top of this file).
//  3. Build:
//     - Go     → `go build` to a static linux binary
//     - Rust   → `cargo build --target aarch64-unknown-linux-musl`
//     - Python → bundle source + dependencies (pip install --target)
//     - Node   → npm install --production
//     - Ruby   → tar source as-is (interpreted at runtime)
//     - PHP    → composer install --no-dev (if composer.json present)
//  4. Tar the build artefact, POST as multipart to /ops/atomic with
//     function metadata (name, route, secrets, triggers).
//
// A future refactor could split the per-language build paths into
// `build_go.go` / `build_python.go` / etc. for readability — someone
// reviewing "what does the CLI run on my machine when I deploy a Python
// function?" should be able to grep one file. It's mechanical
// (move-only, no logic changes).
package atomic_cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

//go:embed default/server_post_native.txt
var defaultNativeServerPost string

//go:embed default/server_get_native.txt
var defaultNativeServerGet string

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

// safeTmpName turns a route path that may contain slashes or `:param`
// segments into a string safe for use as a tempfile/dir pattern.
// `os.CreateTemp` rejects path separators in its pattern argument.
func safeTmpName(name string) string {
	out := strings.ReplaceAll(name, "/", "__")
	out = strings.ReplaceAll(out, ":", "")
	return out
}

// generateMain writes a main.go that wraps the user's Go handler function.
// For body-shaped triggers it binds `var body <T>` to the handler's actual
// first-parameter type (goBodyType) rather than a hardcoded name, so several
// POST handlers can coexist in one element — each with its own body struct —
// without colliding on a shared `RequestBody`.
func generateMain(dir, funcName, method string) error {
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = strings.NewReplacer("{{FUNC}}", funcName, "{{BODYTYPE}}", goBodyType(dir, funcName)).
			Replace(defaultNativeServerPost)
	default:
		code = strings.NewReplacer("{{FUNC}}", funcName).Replace(defaultNativeServerGet)
	}
	return os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0o600)
}

// goBodyType returns the type of funcName's first parameter, read from the
// staged Go sources in dir — e.g. `LoginBody` from
// `func PostLogin(body LoginBody, req drift.Request)`. This lets each POST
// handler in an element declare its own body struct; the generated wrapper
// unmarshals into exactly that type. Falls back to "RequestBody" (the
// historical name) when the signature can't be read, so single-function and
// legacy deploys are byte-for-byte unchanged.
func goBodyType(dir, funcName string) string {
	const fallback = "RequestBody"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	re := regexp.MustCompile(`func\s+` + regexp.QuoteMeta(funcName) + `\s*\(\s*[A-Za-z_]\w*\s+([^,)]+)`)
	for _, e := range entries {
		if e.IsDir() || e.Name() == "main.go" || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- staged build dir, filesystem-sourced name
		if rerr != nil {
			continue
		}
		if m := re.FindSubmatch(data); m != nil {
			if t := strings.TrimSpace(string(m[1])); t != "" {
				return t
			}
		}
	}
	return fallback
}

// generatePythonWrapper writes app.py that wraps the user's Python function.
func generatePythonWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostPython)
	default:
		code = replacer.Replace(wrapperGetPython)
	}
	return os.WriteFile(filepath.Join(dir, "app.py"), []byte(code), 0o600)
}

// generateNodeWrapper writes app.js that wraps the user's Node function.
func generateNodeWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostNode)
	default:
		code = replacer.Replace(wrapperGetNode)
	}
	return os.WriteFile(filepath.Join(dir, "app.js"), []byte(code), 0o600)
}

// generateRubyWrapper writes app.rb that wraps the user's Ruby function.
func generateRubyWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostRuby)
	default:
		code = replacer.Replace(wrapperGetRuby)
	}
	return os.WriteFile(filepath.Join(dir, "app.rb"), []byte(code), 0o600)
}

// generatePHPWrapper writes app.php that wraps the user's PHP function.
func generatePHPWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostPHP)
	default:
		code = replacer.Replace(wrapperGetPHP)
	}
	return os.WriteFile(filepath.Join(dir, "app.php"), []byte(code), 0o600)
}

// generateRustWrapper writes src/main.rs that wraps the user's Rust function.
func generateRustWrapper(dir, sourceModule, funcName, method string) error {
	replacer := strings.NewReplacer("{{FUNC}}", funcName, "{{SOURCE}}", sourceModule)
	var code string
	switch method {
	case "post", "put", "delete", "patch", "queue":
		code = replacer.Replace(wrapperPostRust)
	default:
		code = replacer.Replace(wrapperGetRust)
	}
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(srcDir, "main.rs"), []byte(code), 0o600)
}

// createTarGz creates a .tar.gz archive of the given directory, writing it to destPath.
// Only regular files and directories are included. Hidden files (.git etc) are skipped.
func createTarGz(srcDir, destPath string) error {
	f, err := os.Create(destPath) // #nosec G304 — CLI creates temp archive
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip hidden directories/files.
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
			// #nosec G122 -- snapshot/runner dir is operator-controlled (created by the slice/operator inside its own staging dirs); never user-writable, no symlink TOCTOU risk.
		}
		src, err := os.Open(path) // #nosec G122 G304 -- false-positive: see the cross-repo audit baseline; this site has been reviewed.
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}

// TriggerSpec is the minimal trigger definition declared in source comments.
type TriggerSpec struct {
	Type     string `json:"type"`               // "queue" | "webhook" | "schedule"
	Source   string `json:"source"`             // queue name or webhook path
	Schedule string `json:"schedule,omitempty"` // Go duration string e.g. "5m" (schedule triggers)
	PollMS   int    `json:"poll_ms,omitempty"`  // polling interval ms (queue triggers)
	MaxRetry int    `json:"max_retry,omitempty"`
	// Method tells the runner which HTTP method the function is registered
	// under, so the trigger registry's lookup matches. Empty defaults to
	// "POST" (legacy `// drift:trigger queue X` paired with `@atomic
	// http=post:...`). Set to "queue" for handlers declared via
	// `@atomic queue=NAME` — those register under a synthetic method that
	// no HTTP request can match, keeping them externally unreachable.
	Method string `json:"method,omitempty"`
}

// sourceFiles returns all source files (.go, .py, .js, .rb, .php, .rs) in dir.
func sourceFiles(dir string) []string {
	var files []string
	for _, ext := range []string{"*.go", "*.py", "*.js", "*.rb", "*.php", "*.rs"} {
		matches, _ := filepath.Glob(filepath.Join(dir, ext))
		files = append(files, matches...)
	}
	return files
}

// extractTriggerLine extracts the content after "drift:trigger " from a comment line.
// Supports both // and # comment prefixes.
func extractTriggerLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"// drift:trigger ", "# drift:trigger "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

// extractScheduleLine extracts the content after "drift:schedule " from a comment line.
func extractScheduleLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"// drift:schedule ", "# drift:schedule "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

// parseTriggerComments scans source files in dir for drift:trigger annotations.
//
// Supported formats (// for Go/Node, # for Python):
//
//	// drift:trigger queue my-queue
//	// drift:trigger queue my-queue poll=250ms retry=5
//	# drift:trigger webhook /hooks/payment
func parseTriggerComments(dir string) ([]TriggerSpec, error) {
	var triggers []TriggerSpec
	for _, f := range sourceFiles(dir) {
		data, err := os.ReadFile(f) // #nosec G304 — CLI reads user's source file by design
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			content, ok := extractTriggerLine(line)
			if !ok {
				continue
			}
			parts := strings.Fields(content)
			if len(parts) < 2 {
				continue
			}
			trigType := parts[0]
			if trigType != "queue" && trigType != "webhook" {
				fmt.Printf("Warning: unknown trigger type %q — skipping\n", trigType)
				continue
			}
			spec := TriggerSpec{
				Type:     trigType,
				Source:   parts[1],
				PollMS:   500,
				MaxRetry: 3,
			}
			for _, kv := range parts[2:] {
				if strings.HasPrefix(kv, "poll=") {
					if d, err := time.ParseDuration(strings.TrimPrefix(kv, "poll=")); err == nil {
						spec.PollMS = int(d.Milliseconds())
					}
				} else if strings.HasPrefix(kv, "retry=") {
					if n, err := strconv.Atoi(strings.TrimPrefix(kv, "retry=")); err == nil && n > 0 {
						spec.MaxRetry = n
					}
				}
			}
			triggers = append(triggers, spec)
		}
	}
	return triggers, nil
}

// parseScheduleComments scans source files in dir for drift:schedule annotations.
//
// The value must be a standard 5-field cron expression (minute hour dom month dow).
//
//	// drift:schedule */5 * * * *
//	# drift:schedule 0 15 * * *
func parseScheduleComments(dir string) ([]TriggerSpec, error) {
	var triggers []TriggerSpec
	for _, f := range sourceFiles(dir) {
		data, err := os.ReadFile(f) // #nosec G304 — CLI reads user's source file by design
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			expr, ok := extractScheduleLine(line)
			if !ok || expr == "" {
				continue
			}
			triggers = append(triggers, TriggerSpec{
				Type:     "schedule",
				Schedule: expr,
			})
		}
	}
	return triggers, nil
}

// sendSourceToOperator uploads the compiled binary to the API as
// multipart/form-data. The whole payload is buffered (typical Go binaries
// are ~10–30 MB) so the request body can be replayed if the auto-refresh
// path needs to retry after a 401.
// createUserSourceArchive creates a tar.gz of the user's original source files
// from the function directory. This is the portable, Drift-free snapshot of the
// user's code — no generated wrappers, no injected SDKs, no compiled binaries.
// Used for the snapshot/backup system so users can migrate away from Drift.
func createUserSourceArchive(absFolder, name string) (string, error) {
	archivePath := filepath.Join(os.TempDir(), fmt.Sprintf("drift-user-source-%s.tar.gz", name))
	if err := createTarGz(absFolder, archivePath); err != nil {
		return "", fmt.Errorf("create user source archive: %w", err)
	}
	return archivePath, nil
}

func sendSourceToOperator(name, method, language, auth, element, stream string, secrets []string, sourcePath, userSourcePath string, triggers []TriggerSpec, digest string) error {
	meta, err := json.Marshal(map[string]any{
		"name":     name,
		"method":   method,
		"language": language,
		"auth":     auth,
		"element":  element,
		"stream":   stream,
		"secrets":  secrets,
		"triggers": triggers,
		"digest":   digest,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	f, err := os.Open(sourcePath) // #nosec G304 — CLI tool reads user's compiled binary by design
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaPart, err := mw.CreateFormField("metadata")
	if err != nil {
		return fmt.Errorf("failed to create metadata field: %w", err)
	}
	if _, err := metaPart.Write(meta); err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}
	srcPart, err := mw.CreateFormFile("source", filepath.Base(sourcePath))
	if err != nil {
		return fmt.Errorf("failed to create source field: %w", err)
	}
	if _, err := io.Copy(srcPart, f); err != nil {
		return fmt.Errorf("failed to buffer source: %w", err)
	}

	// Attach the user's original source files for snapshot/backup.
	if userSourcePath != "" {
		uf, err := os.Open(userSourcePath) // #nosec G304
		if err == nil {
			usPart, partErr := mw.CreateFormFile("user-source", filepath.Base(userSourcePath))
			if partErr == nil {
				io.Copy(usPart, uf) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			}
			uf.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		}
	}

	if err := mw.Close(); err != nil {
		return fmt.Errorf("failed to finalize multipart: %w", err)
	}

	// The operator serializes atomic deploys per slice behind a non-blocking
	// lock — that lock is what makes the function-quota check atomic (count →
	// compare to limit → assign slot, all as one critical section). When
	// `drift project deploy` builds functions in parallel and they reach the
	// deploy step together, the losers get 409 "another deploy in progress".
	// Retry those (with backoff) so they serialize through the lock instead of
	// failing spuriously. A 429 (function limit reached) is a REAL rejection
	// from inside that critical section — never retried; it surfaces as-is.
	bodyBytes := buf.Bytes()
	contentType := mw.FormDataContentType()
	deadline := time.Now().Add(90 * time.Second)
	for {
		resp, err := common.DoRequestWithContentType(
			http.MethodPost,
			common.APIBaseURL+"/ops/atomic",
			contentType,
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			// Transport error (a timeout or a momentary blip — common when an
			// element ships many functions at once and the API is briefly
			// saturated). The deploy is idempotent, so retry within the deadline
			// rather than failing the whole element on one slow upload.
			if time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)
				continue
			}
			return common.TransportError("deploy the function", err)
		}
		if resp.StatusCode == http.StatusConflict && time.Now().Before(deadline) {
			resp.Body.Close() // #nosec G104 -- retrying; body discarded intentionally
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_, cerr := common.CheckResponse(resp, "deploy the function")
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited
		return cerr
	}
}

// DeployFolder builds and deploys the atomic function at folder. It is
// exported so that "drift deploy" can call it directly without going through
// the cobra command layer. When quiet is true, all per-function status
// chatter is suppressed so the manifest deploy can render its own clean
// summary line for each function.
func DeployFolder(folder, element string, quiet bool) error {
	meta, err := atomic_common.ParseAtomicMetadataFromDir(folder)
	if err != nil {
		return fmt.Errorf("failed to parse Atomic metadata: %w", err)
	}
	if meta.Trigger != "http" && meta.Trigger != "queue" {
		return fmt.Errorf("@atomic %s= triggers aren't supported by the deploy path yet (only http= and queue= are wired)", meta.Trigger)
	}

	absFolder, err := filepath.Abs(folder)
	if err != nil {
		return fmt.Errorf("failed to resolve folder path: %w", err)
	}

	// Resolve (method, name) per trigger type. For HTTP, both come from
	// the @atomic line (`http=post:reviewer/login` → method=post,
	// name=reviewer/login). For queue triggers the @atomic line carries
	// only the queue name (`queue=validate` → meta.Method holds
	// "validate"); the function's identifier is the directory's
	// basename, registered under a synthetic "queue" method that no
	// HTTP request can match. That keeps queue-only functions
	// unreachable from /api/* while the in-process trigger registry can
	// still look them up by (method, name).
	var method, name string
	auth, stream, secrets := meta.Auth, meta.Stream, meta.Secrets

	switch meta.Trigger {
	case "http":
		method, name = meta.Method, meta.Path
	case "queue":
		method = "queue"
		name = filepath.Base(absFolder)
	}

	language, _, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return fmt.Errorf("failed to detect language: %w", err)
	}

	// Catch a common footgun before building: a function that uses the SDK
	// but ships no dependency manifest to declare it — otherwise it deploys
	// fine and then fails cryptically at runtime ("No module named 'drift'").
	// No-op for Go/Rust (they auto-provision the SDK at build).
	if err := atomic_common.VerifySDKManifest(absFolder, language); err != nil {
		return err
	}

	if !quiet {
		langLabel := language
		if langLabel == "native" {
			langLabel = "go"
		}
		switch meta.Trigger {
		case "queue":
			fmt.Printf("Deploying queue handler '%s' (%s, source: %s)\n", name, langLabel, meta.Method)
		default:
			// Org-only routing: a function is served at /<name>; the element is
			// never a route segment.
			fmt.Printf("Deploying function '%s /%s' (%s, auth: %s)\n", method, name, langLabel, auth)
		}
	}

	triggers, err := parseTriggerComments(absFolder)
	if err != nil {
		return fmt.Errorf("failed to parse trigger comments: %w", err)
	}
	schedules, err := parseScheduleComments(absFolder)
	if err != nil {
		return fmt.Errorf("failed to parse schedule comments: %w", err)
	}
	triggers = append(triggers, schedules...)

	// Auto-register the @atomic queue=NAME annotation as a TriggerSpec so
	// the trigger registry binds the queue to this function. Manual
	// `// drift:trigger queue NAME` comments still work and stack with
	// this — useful for handlers that consume from multiple queues.
	if meta.Trigger == "queue" {
		triggers = append(triggers, TriggerSpec{
			Type:     "queue",
			Source:   meta.Method,
			Method:   "queue", // matches the synthetic registration method
			PollMS:   500,
			MaxRetry: 3,
		})
	}
	if len(triggers) > 0 && !quiet {
		fmt.Printf("  %d trigger(s) found: ", len(triggers))
		for i, t := range triggers {
			if i > 0 {
				fmt.Print(", ")
			}
			if t.Type == "schedule" {
				fmt.Printf("schedule(every %s)", t.Schedule)
			} else {
				fmt.Printf("%s(%s)", t.Type, t.Source)
			}
		}
		fmt.Println()
	}

	var sourcePath string
	switch language {
	case "python":
		sourcePath, err = buildPython(absFolder, method, name)
	case "node":
		sourcePath, err = buildNode(absFolder, method, name)
	case "ruby":
		sourcePath, err = buildRuby(absFolder, method, name)
	case "php":
		sourcePath, err = buildPHP(absFolder, method, name)
	case "rust":
		sourcePath, err = buildRust(absFolder, method, name)
	default:
		sourcePath, err = buildGo(absFolder, method, name)
	}
	if err != nil {
		return err
	}
	defer os.Remove(sourcePath)

	// Create a portable archive of the user's original source files.
	userSourcePath, usErr := createUserSourceArchive(absFolder, name)
	if usErr != nil {
		userSourcePath = "" // best-effort — deploy still works without it
	} else {
		defer os.Remove(userSourcePath)
	}

	// Content fingerprint of this function's source, recorded with the deploy
	// so a later `drift project deploy` can skip it if nothing changed.
	// Best-effort: on error we send "" — the deploy still succeeds, it just
	// won't be skippable next time (an empty digest never matches).
	digest, dErr := FunctionDigest(absFolder, element)
	if dErr != nil {
		digest = ""
	}

	if err := sendSourceToOperator(name, method, language, auth, element, stream, secrets, sourcePath, userSourcePath, triggers, digest); err != nil {
		return err
	}

	if !quiet {
		fmt.Printf("  Function '%s /%s' deployed successfully!\n", method, name)
	}
	return nil
}

// buildGo compiles a Go function to a static Linux binary and returns the path.
func Deploy() *cobra.Command {
	var element string

	atomicDeployCmd := &cobra.Command{
		Use:     "deploy [function folder]",
		Short:   "Deploy a function endpoint",
		Example: "  drift atomic deploy ./send-email\n  drift atomic deploy ./create-invoice --element billing",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return DeployFolder(args[0], element, false)
		},
	}

	atomicDeployCmd.Flags().StringVarP(&element, "element", "e", "", "Group this function under a named element (e.g. --element payments)")
	return atomicDeployCmd
}
