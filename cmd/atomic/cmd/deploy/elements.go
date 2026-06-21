// elements.go — Element discovery for `drift project deploy`.
//
// An Element is a single-language backend: one language, one dependency
// manifest, and N `@atomic` functions across flat files in one directory.
// It is Drift's "service" unit (what you'd put in one container elsewhere).
//
// Discovery is layout-driven and subsumes the legacy folder-per-function
// layout for free:
//
//   - The DEFAULT element is the flat source files directly under atomic/
//     (present only if any carry an @atomic annotation). When a project adds
//     a second element the flat files are promoted into atomic/default/, which
//     is then just an ordinary named element.
//   - A NAMED element is any subdirectory of atomic/ that contains @atomic
//     functions. The legacy `atomic/<fn>/` layout is exactly this — each
//     folder is a one-function element — so old projects keep working.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
	"github.com/ondrift/cli/common"
)

// DefaultElementName is the name of the implicit flat element (atomic/*.<lang>).
const DefaultElementName = "default"

// ElementFunc is one @atomic function within an Element.
type ElementFunc struct {
	Trigger      string // "http" | "queue" | "cron"
	Method       string // http verb | queue name | cron expr
	Path         string // http route path (http triggers only)
	SentinelName string // the source-level function name the build binds to
	SourceFile   string // basename of the file declaring it (interpreted wrappers import from it)
	Auth         string
	Stream       string // "" | "sse" | "ws"
	Secrets      []string
}

// Element is a single-language backend — one language, one dependency
// manifest, N functions across the flat files in Dir.
type Element struct {
	Name  string // "default" for the flat element, else the subdir name
	Dir   string // absolute path to the element's package directory
	Lang  string // "go" | "python" | "node" | "ruby" | "php" | "rust"
	Funcs []ElementFunc
}

// DiscoverElements walks atomicRoot and returns its Elements (the flat
// Default element, if any, plus one per @atomic-bearing subdirectory),
// sorted by name. A missing atomicRoot yields no elements, not an error.
func DiscoverElements(atomicRoot string) ([]Element, error) {
	entries, err := os.ReadDir(atomicRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no atomic/ dir → no elements (e.g. a canvas-only app)
		}
		return nil, fmt.Errorf("read atomic dir: %w", err)
	}

	var elements []Element

	if def, err := elementFromDir(atomicRoot, DefaultElementName); err != nil {
		return nil, err
	} else if def != nil {
		elements = append(elements, *def)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		el, err := elementFromDir(filepath.Join(atomicRoot, e.Name()), e.Name())
		if err != nil {
			return nil, err
		}
		if el != nil {
			elements = append(elements, *el)
		}
	}

	sort.Slice(elements, func(i, j int) bool { return elements[i].Name < elements[j].Name })
	return elements, nil
}

// elementFromDir builds an Element from the @atomic functions in dir's
// top-level source files. Returns (nil, nil) when the dir has no @atomic
// functions. Enforces one language per element.
func elementFromDir(dir, name string) (*Element, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("element %q: read dir: %w", name, err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// Iterate files (not ParseAllAtomicMetadataFromDir) so we can record which
	// FILE each function lives in — interpreted wrappers import the handler from
	// its module.
	var el *Element
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if atomic_common.LanguageFromExt(filepath.Ext(fname)) == "" {
			continue
		}
		metas, perr := atomic_common.ParseAllAtomicMetadata(filepath.Join(dir, fname))
		if perr != nil {
			return nil, fmt.Errorf("element %q: %w", name, perr)
		}
		for _, m := range metas {
			if el == nil {
				el = &Element{Name: name, Dir: abs}
			}
			if el.Lang == "" {
				el.Lang = m.Language
			} else if m.Language != el.Lang {
				return nil, fmt.Errorf(
					"element %q mixes languages (%s and %s) — an Element is one language; "+
						"move the %s functions into their own Element (a separate folder under atomic/)",
					name, el.Lang, m.Language, m.Language)
			}
			el.Funcs = append(el.Funcs, ElementFunc{
				Trigger:      m.Trigger,
				Method:       m.Method,
				Path:         m.Path,
				SentinelName: m.SentinelName,
				SourceFile:   fname,
				Auth:         m.Auth,
				Stream:       m.Stream,
				Secrets:      m.Secrets,
			})
		}
	}
	if el == nil {
		return nil, nil
	}
	sort.Slice(el.Funcs, func(i, j int) bool {
		return el.Funcs[i].SentinelName < el.Funcs[j].SentinelName
	})
	return el, nil
}

// DeployKey is the platform's function identity for an @atomic function:
// the route path for http triggers, else the sentinel name. Org-only
// routing means two functions sharing a key collide regardless of element,
// so this is also the cross-element collision key.
func (f ElementFunc) DeployKey() string {
	switch f.Trigger {
	case "http":
		return f.Path
	case "queue":
		return f.Method // a queue handler's identity is its (lowercase) queue name
	default:
		return f.SentinelName
	}
}

// MethodPath renders a function as "<method>:<path>" for diagnostics.
func (f ElementFunc) MethodPath() string {
	if f.Trigger == "http" {
		return f.Method + ":" + f.Path
	}
	return f.Trigger + "=" + f.Method
}

// DeployGoElement builds and deploys every @atomic function in a Go element.
// The element's package is staged and dependency-resolved ONCE; each function
// is then compiled to its own binary (reusing the warm build cache) and sent
// to the operator. digest is the element's content fingerprint, recorded
// against every function so an unchanged element is skippable next deploy.
func DeployGoElement(el Element, digest string, quiet bool) error {
	if len(el.Funcs) == 0 {
		return nil
	}
	// Fail fast on triggers the deploy path doesn't wire yet.
	for _, f := range el.Funcs {
		if f.Trigger != "http" && f.Trigger != "queue" {
			return fmt.Errorf("@atomic %s= triggers aren't wired in the deploy path yet "+
				"(function %s in element %q)", f.Trigger, f.SentinelName, el.Name)
		}
	}

	stageDir, err := buildGoElementStage(el.Dir, el.Name)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir) // #nosec G104

	// One user-source archive for the element's package, shared by its
	// functions (they all compile from the same source).
	userSrc, usErr := createUserSourceArchive(el.Dir, el.Name)
	if usErr == nil {
		defer os.Remove(userSrc) // #nosec G104
	} else {
		userSrc = ""
	}

	// Warm Go's build cache by compiling the shared package once (a throwaway
	// entrypoint). The parallel builds below then only re-link, and a
	// package-wide compile error surfaces here once rather than N times across
	// the workers.
	warmMethod := el.Funcs[0].Method
	if el.Funcs[0].Trigger == "queue" {
		warmMethod = "queue"
	}
	if _, warmDir, werr := buildGoEntrypointIsolated(stageDir, el.Funcs[0].SentinelName, warmMethod, "warm"); werr != nil {
		return fmt.Errorf("element %q failed to compile: %w", el.Name, werr)
	} else {
		os.RemoveAll(warmDir) // #nosec G104
	}

	// Build + ship every function CONCURRENTLY — each in its own dir, sharing
	// the warmed module + build caches, so this is N parallel links + uploads.
	deployOne := func(f ElementFunc) error {
		method, name := f.Method, f.Path
		if f.Trigger == "queue" {
			// function_name = the queue name (lowercase, valid); the operator
			// rejects the PascalCase sentinel. The trigger Source binds the
			// same queue to this handler.
			method, name = "queue", f.Method
		}
		bin, fnDir, berr := buildGoEntrypointIsolated(stageDir, f.SentinelName, method,
			safeTmpName(el.Name+"-"+f.SentinelName))
		if berr != nil {
			return berr
		}
		defer os.RemoveAll(fnDir) // #nosec G104
		var triggers []TriggerSpec
		if f.Trigger == "queue" {
			triggers = []TriggerSpec{{Type: "queue", Source: f.Method, Method: "queue", PollMS: 500, MaxRetry: 3}}
		}
		return sendSourceToOperator(name, method, "native", f.Auth, el.Name,
			f.Stream, f.Secrets, bin, userSrc, triggers, digest)
	}

	results := make([]error, len(el.Funcs))
	workers := min(runtime.NumCPU(), 8, len(el.Funcs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := range el.Funcs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = deployOne(el.Funcs[i])
		}(i)
	}
	wg.Wait()

	// Ordered report; return the first failure with its real error.
	var firstErr error
	for i, f := range el.Funcs {
		switch {
		case results[i] != nil:
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", f.MethodPath(), results[i])
			}
			if !quiet {
				fmt.Printf("    %s %s\n", common.Cross(), f.MethodPath())
			}
		case !quiet:
			fmt.Printf("    %s %s\n", common.Check(), f.MethodPath())
		}
	}
	return firstErr
}

// CheckElementCollisions rejects a project where two @atomic functions share a
// deploy identity (route path for http, sentinel for queue). With org-only
// routing the /api space is shared across elements, so a clash between
// functions in different elements is just as fatal as one within an element.
func CheckElementCollisions(elements []Element) error {
	type ref struct{ element, mp string }
	byKey := map[string][]ref{}
	for _, el := range elements {
		for _, f := range el.Funcs {
			k := f.DeployKey()
			byKey[k] = append(byKey[k], ref{el.Name, f.MethodPath()})
		}
	}

	var collisions []string
	for k, refs := range byKey {
		if len(refs) < 2 {
			continue
		}
		parts := make([]string, len(refs))
		for i, r := range refs {
			parts[i] = fmt.Sprintf("%s [element %s]", r.mp, r.element)
		}
		sort.Strings(parts)
		collisions = append(collisions, fmt.Sprintf("path %q: %s", k, strings.Join(parts, ", ")))
	}
	if len(collisions) == 0 {
		return nil
	}
	sort.Strings(collisions)
	return fmt.Errorf("route collision — these functions share a path and would shadow each "+
		"other on deploy (a path identifies a function regardless of method; give each a "+
		"distinct path):\n  - %s", strings.Join(collisions, "\n  - "))
}
