package project

// manifest.go is the Driftfile parser + validator.
//
// Implements the Driftfile manifest format (v1).
//
// The parser does three things in one pass:
//
//   1. Decodes the YAML into the canonical nested shape, expanding the
//      two short-form sugars on the way (atomic-as-bare-list and
//      canvas-as-bare-string).
//   2. Resolves `$ENVREF` shorthands in secrets to their literal
//      values from the deployer's environment.
//   3. Validates every field against the spec's binding validation
//      table, collecting all errors into one ParseErrors return so
//      the user sees the whole picture in a single block.
//
// What the parser does NOT do:
//
//   - Reach over the network. Live-slice diffing, cost calculation,
//     and reconcile-rule classification all happen later, in the
//     deploy command driver.
//   - Apply defaults for envelope knobs. Omitted knobs are passed
//     through as zero-valued strings; the API gateway resolves them
//     to the slice envelope's defaults.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ─── Shared validators ──────────────────────────────────────────────

// nameRe matches the canonical Drift identifier shape: 1–32 lowercase
// letters, numbers, or hyphens; cannot start or end with a hyphen.
// Used for slice names, function names, collection names, queue names.
var nameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// sizeRe matches `<integer>(KB|MB|GB)` — used by canvas_size,
// nosql_storage, blob_max_size, function_memory.
var sizeRe = regexp.MustCompile(`^[0-9]+(KB|MB|GB)$`)

// memoryRe is sizeRe minus KB — function memory caps don't go below
// MB granularity.
var memoryRe = regexp.MustCompile(`^[0-9]+(MB|GB)$`)

// durationRe matches `<integer>(s|m|h|d)` — used by log_retention,
// backup_retention.
var durationRe = regexp.MustCompile(`^[0-9]+[smhd]$`)

// timeoutRe is durationRe minus `d` — function timeouts don't run
// for days.
var timeoutRe = regexp.MustCompile(`^[0-9]+[smh]$`)

// rateRe matches `<integer>/(s|min|h)` — used by rate_limit.
var rateRe = regexp.MustCompile(`^[0-9]+/(s|min|h)$`)

// cronFiveFieldRe is a permissive 5-field cron check. The parser only
// validates that there are exactly five whitespace-separated tokens;
// the actual cron grammar (`*`, `*/n`, `1-5`, `1,3,5`) lives in the
// scheduler that consumes it. This catches obvious mistakes (six
// fields, empty string) without locking the spec to a specific cron
// dialect.
var cronFiveFieldRe = regexp.MustCompile(`^\S+\s+\S+\s+\S+\s+\S+\s+\S+$`)

// ─── The canonical (post-expansion) shape ────────────────────────────

// Manifest is the parsed Driftfile, after shorthand expansion. The
// shape mirrors the spec's nested model exactly; downstream code
// reads off this struct without needing to think about short forms.
type Manifest struct {
	Slice Slice `yaml:"slice"`

	// baseDir is set after parsing; relative paths in the manifest
	// resolve against it.
	baseDir string `yaml:"-"`
}

type Slice struct {
	Name            string `yaml:"name"`
	LogRetention    string `yaml:"log_retention"`
	BackupRetention string `yaml:"backup_retention"`

	Atomic   AtomicSection   `yaml:"atomic"`
	Backbone BackboneSection `yaml:"backbone"`
	Canvas   CanvasSection   `yaml:"canvas"`

	// Domains lists per-slice custom hostnames the slice should answer
	// on (e.g. forms.gemeente.example). Schema-only today; the
	// reconcile path is planned.
	Domains []DomainEntry `yaml:"domains"`
}

// DomainEntry declares one custom hostname for the slice. Verify is
// the ownership-proof method; "dns-txt" is the only mode for v1.
type DomainEntry struct {
	Host   string `yaml:"host"`
	Verify string `yaml:"verify"` // "dns-txt" (default for v1)
}

type AtomicSection struct {
	FunctionMemory  string        `yaml:"function_memory"`
	FunctionTimeout string        `yaml:"function_timeout"`
	RateLimit       string        `yaml:"rate_limit"`
	Functions       []AtomicEntry `yaml:"functions"`

	// Egress declares the slice's outbound network posture.
	// Schema-only today; richer enforcement modes are planned.
	Egress *EgressSection `yaml:"egress,omitempty"`
}

// EgressSection — declares whether the slice's outbound traffic to
// the public internet is open (today's default) or restricted to a
// curated list of hostnames. Private-CIDR exclusion (RFC-1918,
// link-local incl. IMDS, CGNAT) is preserved unconditionally
// regardless of mode.
type EgressSection struct {
	Mode  string   `yaml:"mode"`            // "open" | "allowlist"
	Hosts []string `yaml:"hosts,omitempty"` // e.g. "api.stripe.com", "*.amazonaws.com", "smtp.sendgrid.net:587"
}

type AtomicEntry struct {
	Name    string `yaml:"name"`
	Dir     string `yaml:"dir"`
	Element string `yaml:"element"`
	Cron    string `yaml:"cron"`

	// Alerts is the per-function alerting list. v1: `errors`
	// trigger only; `webhook` notify only.
	Alerts []AlertEntry `yaml:"alerts,omitempty"`
}

// AlertEntry declares one alert on a function. `On` is the trigger
// (`errors` for v1). `Threshold` and `Window` together define when
// the alert fires (e.g. >=1 error over a 5-minute window). `Notify`
// is the destination — `webhook=https://hooks.slack.com/...` for v1.
type AlertEntry struct {
	On        string `yaml:"on"`        // "errors" (v1)
	Threshold int    `yaml:"threshold"` // count of errors in the window
	Window    string `yaml:"window"`    // duration string e.g. "5m"
	Notify    string `yaml:"notify"`    // "webhook=https://..." (v1)
}

type BackboneSection struct {
	NoSQLStorage  string                `yaml:"nosql_storage"`
	BlobMaxSize   string                `yaml:"blob_max_size"`
	BlobMaxCount  int                   `yaml:"blob_max_count"`
	QueueMaxDepth int                   `yaml:"queue_max_depth"`
	NoSQL         []NoSQLEntry          `yaml:"nosql"`
	Queues        []string              `yaml:"queues"`
	Cache         map[string]CacheEntry `yaml:"cache"`
	Secrets       map[string]string     `yaml:"secrets"`

	// SQL declares per-slice SQLite databases. Each entry becomes a
	// `.db` file.
	SQL []SQLEntry `yaml:"sql,omitempty"`
}

// SQLEntry declares one SQL database. `Schema` is a path to a SQL
// file with idempotent DDL (`CREATE TABLE IF NOT EXISTS`); it runs
// on every deploy. `Seed` is a path to a SQL file that runs only
// when the database has no user tables yet.
type SQLEntry struct {
	Name   string `yaml:"name"`
	Schema string `yaml:"schema,omitempty"`
	Seed   string `yaml:"seed,omitempty"`
}

type NoSQLEntry struct {
	Name string `yaml:"name"`
	Seed string `yaml:"seed"` // path to JSONL
}

// CacheEntry is the long-form expansion. Short-form `<key>: <path>`
// expands to {File: <path>}. Short-form `{value: ...}` expands to
// {Value: <inline-value>}.
type CacheEntry struct {
	File  string `yaml:"file"`
	Value string `yaml:"value"`
	TTL   int    `yaml:"ttl"`
}

type CanvasSection struct {
	CanvasSize string        `yaml:"canvas_size"`
	Sites      []CanvasEntry `yaml:"sites"`
}

type CanvasEntry struct {
	Dir   string `yaml:"dir"`
	Route string `yaml:"route"`
}

// ─── Parser ─────────────────────────────────────────────────────────

// ParseErrors aggregates every validation failure in one error so the
// user sees them all at once. Implements `error` so it can flow
// through the cobra RunE return.
type ParseErrors []string

func (p ParseErrors) Error() string {
	if len(p) == 1 {
		return p[0]
	}
	return fmt.Sprintf("%d validation errors:\n  - %s", len(p), strings.Join(p, "\n  - "))
}

// ParseDriftfile reads a Driftfile from disk, expands shorthands,
// resolves $ENVREF secrets, and validates everything against the
// spec. baseDir is the directory containing the Driftfile and is
// used as the resolution root for relative paths.
func ParseDriftfile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Expand ${VAR} placeholders against the process environment before
	// YAML parsing — the env-aware Driftfile feature. ${VAR} is the
	// staging/prod overlay primitive: typically `slice.name: ${ENV}-myapp`
	// resolved by `drift project deploy --env=prod` setting ENV=prod.
	// Distinct from `$VAR` (no braces) which is the secret-envref shape
	// in slice.backbone.secrets — that path runs later, on already-parsed
	// values, and is unaffected by this substitution.
	expanded, err := substituteBraceVars(data)
	if err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}
	data = expanded

	// We unmarshal twice: once into the typed Manifest for downstream
	// use, and once into a generic node tree so we can detect short
	// forms before the strict typed decode would reject them.
	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	// Walk the raw node tree and rewrite the two short forms into
	// their canonical maps before the typed decode.
	if err := expandShorthands(&raw); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}

	var m Manifest
	if err := raw.Decode(&m); err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}

	m.baseDir = filepath.Dir(path)

	if err := resolveSecretEnvRefs(&m); err != nil {
		return nil, err
	}

	if errs := validate(&m); len(errs) > 0 {
		return nil, errs
	}
	return &m, nil
}

// expandShorthands rewrites the two short forms documented in the
// spec into their canonical maps:
//
//	slice.atomic: [a, b, c]              -> slice.atomic: { functions: [a, b, c] }
//	slice.canvas: ./path                 -> slice.canvas: { sites: [./path] }
//	slice.canvas: [./a, ./b]             -> slice.canvas: { sites: [./a, ./b] }
//
// Plus the per-list short forms inside atomic.functions, canvas.sites,
// and backbone.nosql, which yaml.Unmarshal handles natively because
// the spec allows mixed string-or-map list elements.
func expandShorthands(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	sliceNode := findChild(doc, "slice")
	if sliceNode == nil || sliceNode.Kind != yaml.MappingNode {
		return nil
	}

	// slice.atomic short form: a sequence becomes { functions: <seq> }.
	if atomicNode := findChild(sliceNode, "atomic"); atomicNode != nil && atomicNode.Kind == yaml.SequenceNode {
		wrap := *atomicNode
		atomicNode.Kind = yaml.MappingNode
		atomicNode.Tag = ""
		atomicNode.Style = 0
		atomicNode.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "functions", Tag: "!!str"},
			&wrap,
		}
	}

	// slice.canvas short forms:
	//   string  -> { sites: [string] }
	//   sequence -> { sites: <seq> }
	if canvasNode := findChild(sliceNode, "canvas"); canvasNode != nil {
		switch canvasNode.Kind {
		case yaml.ScalarNode:
			path := canvasNode.Value
			canvasNode.Kind = yaml.MappingNode
			canvasNode.Tag = ""
			canvasNode.Style = 0
			canvasNode.Value = ""
			canvasNode.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "sites", Tag: "!!str"},
				{
					Kind: yaml.SequenceNode,
					Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Value: path, Tag: "!!str"},
					},
				},
			}
		case yaml.SequenceNode:
			wrap := *canvasNode
			canvasNode.Kind = yaml.MappingNode
			canvasNode.Tag = ""
			canvasNode.Style = 0
			canvasNode.Content = []*yaml.Node{
				{Kind: yaml.ScalarNode, Value: "sites", Tag: "!!str"},
				&wrap,
			}
		}
	}

	return nil
}

// findChild returns the value node for a given key in a mapping node,
// or nil if the key isn't present.
func findChild(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ─── Custom unmarshalers for mixed-string-or-map list elements ──────

// UnmarshalYAML accepts either a bare-string (function name) or a map
// (the long form with name/dir/element/cron).
func (a *AtomicEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		a.Name = node.Value
		return nil
	}
	type raw AtomicEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*a = AtomicEntry(r)
	return nil
}

// UnmarshalYAML accepts either a bare-string (collection name) or a
// map (the long form with name/seed).
func (n *NoSQLEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		n.Name = node.Value
		return nil
	}
	type raw NoSQLEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*n = NoSQLEntry(r)
	return nil
}

// UnmarshalYAML accepts either a bare-string (canvas directory) or a
// map (the long form with dir/route).
func (c *CanvasEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.Dir = node.Value
		return nil
	}
	type raw CanvasEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*c = CanvasEntry(r)
	return nil
}

// UnmarshalYAML for cache map values accepts either a bare-string
// (file path) or a map (the long form with value/ttl).
func (c *CacheEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		c.File = node.Value
		return nil
	}
	type raw CacheEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*c = CacheEntry(r)
	return nil
}

// ─── Secret $ENVREF resolution ──────────────────────────────────────

// braceVarRe matches ${NAME} placeholders. NAME is ASCII letters,
// digits, and underscores starting with a letter or underscore — same
// shape as POSIX shell variable names. Unmatched braces (e.g. `${`
// without closing) are left alone.
var braceVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substituteBraceVars replaces every `${VAR}` in data with
// `os.Getenv("VAR")`. Returns an error listing every variable that
// is referenced but not set, so the user sees every gap at once
// instead of fixing them one at a time.
func substituteBraceVars(data []byte) ([]byte, error) {
	missing := map[string]struct{}{}
	out := braceVarRe.ReplaceAllFunc(data, func(match []byte) []byte {
		name := string(braceVarRe.FindSubmatch(match)[1])
		val, ok := os.LookupEnv(name)
		if !ok {
			missing[name] = struct{}{}
			return match
		}
		return []byte(val)
	})
	if len(missing) > 0 {
		var names []string
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("${VAR} placeholders reference unset environment variables: %s (set them, or pass --env=<name> to drift project deploy/diff to set ENV)", strings.Join(names, ", "))
	}
	return out, nil
}

// secretsMap holds the resolved secrets after $ENVREF substitution.
// The Manifest's Secrets field still holds the *manifest* values
// (with the literal "$NAME" string for envrefs); resolveSecretEnvRefs
// converts them in place. This keeps the SDK's mental model simple:
// after ParseDriftfile, what's in m.Slice.Backbone.Secrets is what
// the platform should store.
func resolveSecretEnvRefs(m *Manifest) error {
	if m.Slice.Backbone.Secrets == nil {
		return nil
	}
	missing := []string{}
	for k, v := range m.Slice.Backbone.Secrets {
		if !strings.HasPrefix(v, "$") {
			continue
		}
		// Quoted "$dollars" is a literal — it would already have lost
		// the quotes by the time we see the string here, so we can't
		// distinguish from a real envref. The spec's escape hatch
		// ("To force a literal that starts with $, quote it") relies
		// on the fact that a literal value is meant to BE that string.
		// We treat all bare $-prefixed values as envrefs.
		envName := strings.TrimPrefix(v, "$")
		envVal, ok := os.LookupEnv(envName)
		if !ok {
			missing = append(missing, fmt.Sprintf("secret %q: environment variable %s is not set", k, envName))
			continue
		}
		m.Slice.Backbone.Secrets[k] = envVal
	}
	if len(missing) > 0 {
		return ParseErrors(missing)
	}
	return nil
}

// ─── Validation ─────────────────────────────────────────────────────

// validate checks every field in the manifest against the spec's
// binding rules. Returns a slice of error messages for one-shot
// reporting.
func validate(m *Manifest) ParseErrors {
	var errs ParseErrors

	// slice.name
	if strings.TrimSpace(m.Slice.Name) == "" {
		errs = append(errs, "slice.name must be a non-empty string")
	} else if !nameRe.MatchString(m.Slice.Name) {
		errs = append(errs, fmt.Sprintf("slice.name %q must be 1–32 lowercase letters, numbers, or hyphens (no leading/trailing hyphen)", m.Slice.Name))
	}

	// slice-level operational durations
	if v := m.Slice.LogRetention; v != "" && !durationRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.log_retention %q must be an integer ending in s, m, h, or d", v))
	}
	if v := m.Slice.BackupRetention; v != "" && !durationRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.backup_retention %q must be an integer ending in s, m, h, or d", v))
	}

	// atomic envelope
	a := m.Slice.Atomic
	if v := a.FunctionMemory; v != "" && !memoryRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.atomic.function_memory %q must be an integer ending in MB or GB", v))
	}
	if v := a.FunctionTimeout; v != "" && !timeoutRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.atomic.function_timeout %q must be an integer ending in s, m, or h", v))
	}
	if v := a.RateLimit; v != "" && !rateRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.atomic.rate_limit %q must be an integer per s, min, or h (e.g. 1000/min)", v))
	}

	// atomic functions
	for _, fn := range a.Functions {
		if !nameRe.MatchString(fn.Name) {
			errs = append(errs, fmt.Sprintf("atomic function name %q is invalid", fn.Name))
			continue
		}
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = resolveBaseDir(m, dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("atomic function %q not found at %s", fn.Name, dir))
		}
		if v := fn.Cron; v != "" && !cronFiveFieldRe.MatchString(v) {
			errs = append(errs, fmt.Sprintf("atomic function %q: cron %q is not a valid 5-field cron expression", fn.Name, v))
		}
	}

	// backbone envelope
	b := m.Slice.Backbone
	if v := b.NoSQLStorage; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.backbone.nosql_storage %q must be an integer ending in KB, MB, or GB", v))
	}
	if v := b.BlobMaxSize; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.backbone.blob_max_size %q must be an integer ending in KB, MB, or GB", v))
	}
	if b.QueueMaxDepth < 0 {
		errs = append(errs, fmt.Sprintf("slice.backbone.queue_max_depth %d must be a positive integer", b.QueueMaxDepth))
	}

	// nosql collections
	for _, c := range b.NoSQL {
		if !nameRe.MatchString(c.Name) {
			errs = append(errs, fmt.Sprintf("nosql collection name %q is invalid", c.Name))
		}
		if c.Seed != "" {
			seedPath := resolveBaseDir(m, c.Seed)
			if _, err := os.Stat(seedPath); err != nil {
				errs = append(errs, fmt.Sprintf("nosql %q seed file not found at %s", c.Name, seedPath))
				continue
			}
			if seedErrs := validateJSONLSeed(c.Name, seedPath); len(seedErrs) > 0 {
				errs = append(errs, seedErrs...)
			}
		}
	}

	// queues
	for _, q := range b.Queues {
		if !nameRe.MatchString(q) {
			errs = append(errs, fmt.Sprintf("queue name %q is invalid", q))
		}
	}

	// cache
	for k, e := range b.Cache {
		if e.File == "" && e.Value == "" {
			errs = append(errs, fmt.Sprintf("cache %q must have either a file path or an inline value", k))
			continue
		}
		if e.File != "" {
			fp := resolveBaseDir(m, e.File)
			if _, err := os.Stat(fp); err != nil {
				errs = append(errs, fmt.Sprintf("cache %q file not found at %s", k, fp))
			}
		}
	}

	// canvas envelope
	if v := m.Slice.Canvas.CanvasSize; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("slice.canvas.canvas_size %q must be an integer ending in KB, MB, or GB", v))
	}
	for _, s := range m.Slice.Canvas.Sites {
		dir := resolveBaseDir(m, s.Dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("canvas directory not found at %s", dir))
		}
	}

	return errs
}

// validateJSONLSeed checks every line of a JSONL seed file for
// JSON-validity and a non-empty `_id` field.
func validateJSONLSeed(collection, path string) []string {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return []string{fmt.Sprintf("nosql %q seed: read %s: %v", collection, path, err)}
	}

	var errs []string
	lines := strings.Split(string(data), "\n")
	for i, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: invalid JSON: %v", collection, path, i+1, err))
			continue
		}
		id, ok := doc["_id"]
		if !ok {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: missing _id", collection, path, i+1))
			continue
		}
		// Treat empty string and nil as "missing".
		if s, isStr := id.(string); isStr && s == "" {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: empty _id", collection, path, i+1))
		} else if id == nil {
			errs = append(errs, fmt.Sprintf("nosql %q seed: %s:%d: empty _id", collection, path, i+1))
		}
	}
	return errs
}

// resolveBaseDir resolves a possibly-relative path against the
// Driftfile's directory, leaving absolute paths untouched.
func resolveBaseDir(m *Manifest, rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	if m.baseDir == "" {
		return rel
	}
	return filepath.Join(m.baseDir, rel)
}

// ResolvePath is the exported sibling of resolveBaseDir, used by the
// run driver after parse to find files referenced by the manifest.
func (m *Manifest) ResolvePath(rel string) string { return resolveBaseDir(m, rel) }
