// new.go — `drift atomic new [name]`. Scaffolds a new Atomic function in
// any of the six supported languages, for an HTTP or a queue trigger, with
// the current handler signature the deploy/run wrappers expect:
//
//	GET   handler(req)        -> (status, message, payload)
//	POST… handler(body, req)  -> (status, message, payload)   (also queue)
//	Go is the exception: it returns a 4th `headers` value and, for the
//	body shape, defines a RequestBody struct the wrapper unmarshals into.
//
// Flags make it fully non-interactive (`-l python -m post`); anything not
// supplied is asked for with a survey prompt. The function's manifest
// declares the SDK unversioned (the CLI stays SDK-agnostic); `drift atomic
// fetch`/`run`/`deploy` resolve it.
package atomic_cmd_new

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
)

//go:embed languages/golang_get.txt
var tplGoGet string

//go:embed languages/golang_post.txt
var tplGoPost string

//go:embed languages/python_get.txt
var tplPyGet string

//go:embed languages/python_post.txt
var tplPyPost string

//go:embed languages/node_get.txt
var tplNodeGet string

//go:embed languages/node_post.txt
var tplNodePost string

//go:embed languages/ruby_get.txt
var tplRubyGet string

//go:embed languages/ruby_post.txt
var tplRubyPost string

//go:embed languages/php_get.txt
var tplPhpGet string

//go:embed languages/php_post.txt
var tplPhpPost string

//go:embed languages/rust_get.txt
var tplRustGet string

//go:embed languages/rust_post.txt
var tplRustPost string

// name/route/queue validation — mirrors the operator + runner regex so a
// scaffolded function is always deployable.
var nameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

var langExt = map[string]string{
	"go": "go", "python": "py", "node": "js", "ruby": "rb", "php": "php", "rust": "rs",
}

// New is the `drift atomic new` command.
func New() *cobra.Command {
	var lang, method, queue, auth, element string
	c := &cobra.Command{
		Use:     "new [name]",
		Short:   "Add an Atomic function to an element",
		GroupID: "development",
		Args:    cobra.MaximumNArgs(1),
		Example: "  drift atomic new\n" +
			"  drift atomic new send-email -l python -m post\n" +
			"  drift atomic new process-jobs -l go -q jobs\n" +
			"  drift atomic new train -l python -m post --element ml",
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return runNew(name, lang, method, queue, auth, element)
		},
	}
	c.Flags().StringVarP(&lang, "lang", "l", "", "language: go|python|node|ruby|php|rust")
	c.Flags().StringVarP(&method, "method", "m", "", "HTTP method: get|post|put|delete|patch")
	c.Flags().StringVarP(&queue, "queue", "q", "", "queue name (creates a queue-triggered function)")
	c.Flags().StringVarP(&auth, "auth", "a", "", "auth for HTTP: none|apikey|jwt (default none)")
	c.Flags().StringVarP(&element, "element", "e", "", "element to add it to (default: the flat top-level element)")
	return c
}

func runNew(name, lang, method, queue, auth, element string) error {
	// ---- name ----
	if name == "" {
		if err := survey.AskOne(&survey.Input{Message: "Function name (e.g. send-email):"}, &name,
			survey.WithValidator(survey.Required)); err != nil {
			return err
		}
	}
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid name %q — use lowercase letters, digits and hyphens (e.g. send-email)", name)
	}

	// ---- language ----
	if lang == "" {
		if err := survey.AskOne(&survey.Select{
			Message: "Language:",
			Options: []string{"go", "python", "node", "ruby", "php", "rust"},
			VimMode: true,
		}, &lang); err != nil {
			return err
		}
	}
	lang = strings.ToLower(lang)
	if _, ok := langExt[lang]; !ok {
		return fmt.Errorf("unsupported language %q (go|python|node|ruby|php|rust)", lang)
	}

	// ---- trigger ----
	if method != "" && queue != "" {
		return fmt.Errorf("choose either --method (HTTP) or --queue, not both")
	}
	isQueue := queue != ""
	if method == "" && queue == "" {
		trig := "HTTP"
		if err := survey.AskOne(&survey.Select{
			Message: "Trigger:", Options: []string{"HTTP", "Queue"}, Default: "HTTP", VimMode: true,
		}, &trig); err != nil {
			return err
		}
		if trig == "Queue" {
			isQueue = true
			q := name
			if err := survey.AskOne(&survey.Input{Message: "Queue name:", Default: name}, &q); err != nil {
				return err
			}
			queue = strings.TrimSpace(q)
			if queue == "" {
				queue = name
			}
		} else {
			if err := survey.AskOne(&survey.Select{
				Message: "HTTP method:",
				Options: []string{"POST", "GET", "PUT", "DELETE", "PATCH"},
				VimMode: true,
			}, &method); err != nil {
				return err
			}
			if auth == "" {
				if err := survey.AskOne(&survey.Select{
					Message: "Auth:", Options: []string{"none", "apikey", "jwt"}, VimMode: true,
				}, &auth); err != nil {
					return err
				}
			}
		}
	}

	// ---- resolve trigger details + the handler shape ----
	var annotationBody, funcMethod, shape string
	if isQueue {
		if !nameRe.MatchString(queue) {
			return fmt.Errorf("invalid queue name %q", queue)
		}
		annotationBody = fmt.Sprintf("queue=%s auth=none", queue)
		funcMethod = "queue"
		shape = "post" // queue messages carry a body: handler(body, req)
	} else {
		method = strings.ToLower(method)
		switch method {
		case "get", "post", "put", "delete", "patch":
		default:
			return fmt.Errorf("invalid method %q (get|post|put|delete|patch)", method)
		}
		if auth == "" {
			auth = "none"
		}
		switch auth {
		case "none", "apikey", "jwt":
		default:
			return fmt.Errorf("invalid auth %q (none|apikey|jwt)", auth)
		}
		annotationBody = fmt.Sprintf("http=%s:%s auth=%s", method, name, auth)
		funcMethod = method
		if method == "get" {
			shape = "get"
		} else {
			shape = "post"
		}
	}

	// ---- assemble + write ----
	funcName := atomic_common.FuncNameForLanguage(funcMethod, name, lang)
	annotation := commentPrefix(lang) + " @atomic " + annotationBody
	source := strings.NewReplacer("{{ANNOTATION}}", annotation, "{{FUNC}}", funcName).
		Replace(sourceTemplate(lang, shape))

	// ---- resolve the target element + drop a flat handler file into it ----
	// An element is a single-language backend: a folder of flat source files
	// sharing one manifest. The default element is atomic/ itself; --element
	// targets a named subfolder. Adding a function never creates a per-function
	// folder — it's just another file alongside its siblings.
	atomicDir := resolveAtomicDir()
	elementDir, elementID := atomicDir, defaultElement
	if element != "" {
		if !nameRe.MatchString(element) {
			return fmt.Errorf("invalid element name %q — use lowercase letters, digits and hyphens", element)
		}
		elementDir, elementID = filepath.Join(atomicDir, element), element
	}

	// One language per element — a different language is, by definition, a
	// different element.
	if existing := elementLang(elementDir); existing != "" && existing != lang {
		return fmt.Errorf("the %s element is %s — a different language is a different element; "+
			"pass --element <name> to start a new one", elementID, existing)
	}
	if err := os.MkdirAll(elementDir, 0o750); err != nil {
		return fmt.Errorf("create element dir: %w", err)
	}

	srcFile := filepath.Join(elementDir, sourceBase(lang, name)+"."+langExt[lang])
	if exists(srcFile) {
		return fmt.Errorf("%s already exists", srcFile)
	}
	if err := os.WriteFile(srcFile, []byte(source), 0o600); err != nil {
		return fmt.Errorf("write source: %w", err)
	}

	// Manifest / .env / .gitignore: one per element, written only when absent so
	// adding a function never clobbers an existing element's shared files.
	created := []string{srcFile}
	manifestName, manifestBody := manifest(lang, elementID)
	if mp := filepath.Join(elementDir, manifestName); !exists(mp) {
		if err := os.WriteFile(mp, []byte(manifestBody), 0o600); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}
		created = append(created, mp)
	}
	if ep := filepath.Join(elementDir, ".env"); !exists(ep) {
		_ = os.WriteFile(ep, []byte(dotEnv), 0o600) // #nosec G306 -- local-dev secrets file
	}
	if gp := filepath.Join(atomicDir, ".gitignore"); !exists(gp) {
		_ = os.WriteFile(gp, []byte(gitignore), 0o600) // #nosec G306
	}

	trigger := "HTTP " + strings.ToUpper(method)
	if isQueue {
		trigger = "queue=" + queue
	}
	fmt.Printf("✅ Added %s to the %s element  (%s, %s)\n", filepath.Base(srcFile), elementID, lang, trigger)
	for _, f := range created {
		fmt.Printf("   %s\n", f)
	}
	fmt.Printf("\nNext:\n")
	fmt.Printf("\tdrift project deploy   # discovers every @atomic function and ships the element\n")
	return nil
}

// defaultElement is the implicit flat element (atomic/*.<lang>) — mirrors the
// deploy package's DefaultElementName without importing it.
const defaultElement = "default"

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// resolveAtomicDir picks where to drop the new function, relative to the working
// directory: "." when we're already inside an atomic/ folder, else "atomic"
// (created on first use — a fresh project just gets its first function).
func resolveAtomicDir() string {
	if cwd, err := os.Getwd(); err == nil && filepath.Base(cwd) == "atomic" {
		return "."
	}
	return "atomic"
}

// elementLang reports the single language of the source already in dir (an
// element is one language), or "" if it holds none yet.
func elementLang(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if l := atomic_common.LanguageFromExt(filepath.Ext(e.Name())); l != "" {
			return l
		}
	}
	return ""
}

// commentPrefix returns the line-comment marker for the language.
func commentPrefix(lang string) string {
	switch lang {
	case "python", "ruby":
		return "#"
	default:
		return "//"
	}
}

// sourceBase is the source file's base name (no extension). Python and Rust
// turn the file into a module/mod, which can't contain hyphens, so they get
// an underscore-safe name; the others use the function name verbatim.
func sourceBase(lang, name string) string {
	if lang == "python" || lang == "rust" {
		return strings.ReplaceAll(name, "-", "_")
	}
	return name
}

func sourceTemplate(lang, shape string) string {
	get := shape == "get"
	switch lang {
	case "go":
		if get {
			return tplGoGet
		}
		return tplGoPost
	case "python":
		if get {
			return tplPyGet
		}
		return tplPyPost
	case "node":
		if get {
			return tplNodeGet
		}
		return tplNodePost
	case "ruby":
		if get {
			return tplRubyGet
		}
		return tplRubyPost
	case "php":
		if get {
			return tplPhpGet
		}
		return tplPhpPost
	case "rust":
		if get {
			return tplRustGet
		}
		return tplRustPost
	}
	return ""
}

// manifest returns the (filename, contents) of the function's dependency
// manifest. The SDK is declared unversioned so builds always track latest;
// Go's go.mod stays bare (the build/fetch path runs `go get …@latest`).
func manifest(lang, name string) (string, string) {
	switch lang {
	case "go":
		return "go.mod", fmt.Sprintf("module atomic/%s\n\ngo 1.26.2\n", name)
	case "python":
		return "requirements.txt", "drift-sdk @ git+https://github.com/ondrift/sdk.git#subdirectory=python\n"
	case "node":
		return "package.json", fmt.Sprintf("{\n"+
			"  \"name\": \"atomic-%s\",\n"+
			"  \"version\": \"1.0.0\",\n"+
			"  \"private\": true,\n"+
			"  \"dependencies\": {\n"+
			"    \"@ondrift/sdk\": \"github:ondrift/sdk#semver:*\"\n"+
			"  }\n}\n", name)
	case "ruby":
		return "Gemfile", "source \"https://rubygems.org\"\n\n" +
			"gem \"drift-sdk\", git: \"https://github.com/ondrift/sdk\", branch: \"master\", glob: \"ruby/*.gemspec\"\n"
	case "php":
		return "composer.json", "{\n" +
			"  \"repositories\": [\n" +
			"    { \"type\": \"vcs\", \"url\": \"https://github.com/ondrift/sdk\" }\n" +
			"  ],\n" +
			"  \"require\": {\n" +
			"    \"ondrift/sdk\": \"*\"\n" +
			"  }\n}\n"
	case "rust":
		return "Cargo.toml", rustCargoToml
	}
	return "", ""
}

const rustCargoToml = `[package]
name = "atomic-function"
version = "0.1.0"
edition = "2021"

[[bin]]
name = "atomic-function"
path = "src/main.rs"

[dependencies]
drift-sdk = { git = "https://github.com/ondrift/sdk" }
serde_json = "1"

[profile.release]
opt-level = "z"
lto = true
strip = true
panic = "abort"
`

const dotEnv = "# Secrets for local development — loaded automatically by 'drift atomic run'.\n" +
	"# In production use 'drift backbone secret set KEY=VALUE' and read them via the SDK.\n" +
	"#\n# Example:\n# API_KEY=your-api-key-here\n"

const gitignore = ".env\nnode_modules/\nvendor/\ntarget/\n"
