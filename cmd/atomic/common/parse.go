package atomic_common

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AtomicMeta is everything a single `@atomic` annotation declares.
//
// One annotation per callable sentinel. The trigger is exactly one of
// `http`, `queue`, or `cron`; auth, stream, and secrets are optional
// inline keywords on the same line.
//
//	@atomic http=post:foo/bar auth=none secrets=KEY1,KEY2
//	@atomic queue=validate auth=none secrets=KEY1
//	@atomic cron="0 * * * *" auth=none
type AtomicMeta struct {
	// Trigger is "http", "queue", or "cron".
	Trigger string
	// Method carries the trigger-specific value:
	//   http  → HTTP verb ("get", "post", "put", "delete", "patch")
	//   queue → queue name
	//   cron  → cron expression (e.g. "0 * * * *")
	Method string
	// Path is the route path, populated only for http triggers.
	Path string
	// Auth is the platform-level auth gate ("none", "apikey", or "").
	Auth string
	// Stream is the response shape ("", "sse", or "ws").
	Stream string
	// Secrets is the allowlist of backbone secrets the runner injects
	// as DRIFT_SECRET_<NAME> env vars at invocation time.
	Secrets []string
	// SentinelName is the name of the source-level callable (function
	// or method name) the annotation sits directly above. Populated by
	// ParseAllAtomicMetadata; left empty by ParseAtomicMetadata.
	SentinelName string
	// Language is the source language detected for the file the
	// annotation came from ("go", "python", "node", "ruby", "php",
	// "rust"). Populated by ParseAllAtomicMetadata.
	Language string
}

// ParseAtomicMetadataFromDir scans every file in dir for an `@atomic`
// annotation and returns the first one found.
func ParseAtomicMetadataFromDir(dir string) (AtomicMeta, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return AtomicMeta{}, fmt.Errorf("failed to read dir: %w", err)
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		filename := filepath.Join(dir, file.Name())
		if meta, err := ParseAtomicMetadata(filename); err == nil {
			return meta, nil
		}
	}
	return AtomicMeta{}, fmt.Errorf("no valid atomic metadata found in directory %s", dir)
}

// atomicLineRe captures the body of an `@atomic` annotation. The line
// must begin with a comment marker (`//`, `#`, or `--`) followed by
// `@atomic` — anchoring this way means the word `@atomic` appearing
// inside prose ("the @atomic annotation above") doesn't match.
var atomicLineRe = regexp.MustCompile(`(?m)^[ \t]*(?://|#|--)\s*@atomic\s+([^\r\n]+)$`)

// ParseAtomicMetadata reads filename, finds the first `@atomic` line,
// and parses it into an AtomicMeta. Returns an error if the file
// doesn't contain a valid annotation.
func ParseAtomicMetadata(filename string) (AtomicMeta, error) {
	data, err := os.ReadFile(filename) // #nosec G304 — CLI tool reads user's own source files by design
	if err != nil {
		return AtomicMeta{}, err
	}

	match := atomicLineRe.FindSubmatch(data)
	if len(match) < 2 {
		return AtomicMeta{}, fmt.Errorf("atomic metadata not found")
	}
	return parseAtomicLine(string(match[1]))
}

// parseAtomicLine tokenises the body of an `@atomic` line into key=value
// pairs and validates the result. Quoted values (single or double) are
// supported so cron expressions with spaces work.
//
//	http=post:foo/bar           → trigger=http, method=post, path=foo/bar
//	queue=validate              → trigger=queue, method=validate
//	cron="0 * * * *"            → trigger=cron, method="0 * * * *"
//	secrets=KEY1,KEY2           → secrets=[KEY1, KEY2]
func parseAtomicLine(line string) (AtomicMeta, error) {
	tokens, err := tokeniseAtomicLine(line)
	if err != nil {
		return AtomicMeta{}, err
	}

	var meta AtomicMeta
	triggerCount := 0
	for _, tok := range tokens {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			return AtomicMeta{}, fmt.Errorf("@atomic token %q must be key=value", tok)
		}
		key := strings.TrimSpace(tok[:eq])
		val := strings.TrimSpace(tok[eq+1:])
		val = strings.Trim(val, `"'`)
		switch key {
		case "http":
			triggerCount++
			meta.Trigger = "http"
			parts := strings.SplitN(val, ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return AtomicMeta{}, fmt.Errorf("@atomic http=<verb>:<path> required, got %q", val)
			}
			meta.Method = parts[0]
			meta.Path = parts[1]
		case "queue":
			triggerCount++
			meta.Trigger = "queue"
			meta.Method = val
		case "cron":
			triggerCount++
			meta.Trigger = "cron"
			meta.Method = val
		case "auth":
			meta.Auth = val
		case "stream":
			meta.Stream = val
		case "secrets":
			for _, raw := range strings.Split(val, ",") {
				s := strings.TrimSpace(raw)
				if s != "" {
					meta.Secrets = append(meta.Secrets, s)
				}
			}
		default:
			return AtomicMeta{}, fmt.Errorf("@atomic: unknown key %q", key)
		}
	}
	if triggerCount == 0 {
		return AtomicMeta{}, fmt.Errorf("@atomic: missing trigger (one of http=, queue=, cron=)")
	}
	if triggerCount > 1 {
		return AtomicMeta{}, fmt.Errorf("@atomic: exactly one trigger allowed (http=, queue=, cron= are mutually exclusive)")
	}
	return meta, nil
}

// tokeniseAtomicLine splits the line into key=value tokens, respecting
// quoted values so cron expressions like `"0 * * * *"` stay intact.
func tokeniseAtomicLine(line string) ([]string, error) {
	var (
		tokens []string
		buf    strings.Builder
		quote  byte
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			buf.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			buf.WriteByte(c)
		case c == ' ' || c == '\t':
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("@atomic: unterminated quoted value")
	}
	if buf.Len() > 0 {
		tokens = append(tokens, buf.String())
	}
	return tokens, nil
}

// ── Multi-handler parsing ───────────────────────────────────────────
//
// ParseAllAtomicMetadata walks a source file looking for callable
// sentinels (function/method declarations) and returns one AtomicMeta
// per decorated callable.
//
// Strict rule: a callable has at most ONE `@atomic` annotation in the
// comment line(s) directly above it. Two or more `@atomic` lines stacked
// above the same sentinel is a hard error — the user must split into
// separate functions. Callables with no `@atomic` above them are
// helpers (skipped, free, unrouted).

// sentinelRe is the per-language regex for matching a callable
// sentinel declaration line. Only the function NAME is captured (group 1).
var sentinelRe = map[string]*regexp.Regexp{
	"go":     regexp.MustCompile(`^[ \t]*func[ \t]+([A-Z][A-Za-z0-9_]*)[ \t]*\(`),
	"python": regexp.MustCompile(`^[ \t]*def[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"node":   regexp.MustCompile(`^[ \t]*(?:async[ \t]+)?function[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"ruby":   regexp.MustCompile(`^[ \t]*def[ \t]+([A-Za-z_][A-Za-z0-9_?!]*)[ \t]*(?:\(|$)`),
	"php":    regexp.MustCompile(`^[ \t]*function[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
	"rust":   regexp.MustCompile(`^[ \t]*pub[ \t]+fn[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`),
}

// commentAtomicRe matches a comment line that carries an `@atomic`
// annotation. Captures the body (everything after `@atomic`).
var commentAtomicRe = regexp.MustCompile(`^[ \t]*(?://|#|--)\s*@atomic\s+([^\r\n]+)$`)

// commentLineRe matches any comment line (used to "look one more line up").
var commentLineRe = regexp.MustCompile(`^[ \t]*(?://|#|--)`)

// languageFromExt returns the parser language key for a file extension.
func languageFromExt(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "node"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".rs":
		return "rust"
	}
	return ""
}

// LanguageFromExt reports the Drift language key for a file extension (e.g.
// ".py" → "python"), or "" if the extension isn't a supported source file.
// Exported so element discovery can pick out source files per language.
func LanguageFromExt(ext string) string { return languageFromExt(ext) }

// ParseAllAtomicMetadata reads filename and returns one AtomicMeta
// per decorated callable in the file. Returns an empty slice if the
// file has no decorated callables. Returns an error if any callable
// has more than one `@atomic` line stacked above it.
func ParseAllAtomicMetadata(filename string) ([]AtomicMeta, error) {
	lang := languageFromExt(filepath.Ext(filename))
	if lang == "" {
		return nil, fmt.Errorf("unsupported source extension: %s", filepath.Ext(filename))
	}
	rx, ok := sentinelRe[lang]
	if !ok {
		return nil, fmt.Errorf("no sentinel pattern for language %q", lang)
	}

	data, err := os.ReadFile(filename) // #nosec G304 — CLI tool reads user's own source files by design
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var out []AtomicMeta
	for i, line := range lines {
		m := rx.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		sentinel := m[1]

		// Walk backward, skipping blank lines, to find the comment
		// directly above this callable.
		j := i - 1
		for j >= 0 && strings.TrimSpace(lines[j]) == "" {
			j--
		}
		if j < 0 {
			continue
		}
		annoMatch := commentAtomicRe.FindStringSubmatch(lines[j])
		if annoMatch == nil {
			// Comment present but no @atomic, or no comment at all → helper.
			continue
		}

		// Walk one more line up: if that's ALSO an @atomic, the user
		// stacked decorators. Hard error.
		k := j - 1
		for k >= 0 && strings.TrimSpace(lines[k]) == "" {
			k--
		}
		if k >= 0 && commentAtomicRe.MatchString(lines[k]) {
			return nil, fmt.Errorf("%s:%d: multiple @atomic decorators above %q — only one decorator per callable is allowed",
				filepath.Base(filename), i+1, sentinel)
		}

		meta, perr := parseAtomicLine(annoMatch[1])
		if perr != nil {
			return nil, fmt.Errorf("%s:%d: %w", filepath.Base(filename), j+1, perr)
		}
		meta.SentinelName = sentinel
		meta.Language = lang
		out = append(out, meta)
	}
	return out, nil
}

// ParseAllAtomicMetadataFromDir walks every source file in dir, runs
// ParseAllAtomicMetadata on each, and returns the concatenated list.
func ParseAllAtomicMetadataFromDir(dir string) ([]AtomicMeta, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var out []AtomicMeta
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		ext := filepath.Ext(f.Name())
		if languageFromExt(ext) == "" {
			continue
		}
		metas, err := ParseAllAtomicMetadata(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, metas...)
	}
	return out, nil
}
