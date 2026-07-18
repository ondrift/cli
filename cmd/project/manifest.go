package project

// manifest.go is the Driftfile parser + validator.
//
// Implements the Driftfile manifest format (v2 — project-level). The
// Driftfile *is* the project: the resource shape (name, retention,
// atomic/backbone/canvas/domains) sits at the top level, with two optional
// siblings — `environments` (per-environment config overrides) and `hooks`
// (local pre/post-deploy commands).
//
// The parser does three things in one pass:
//
//   1. Decodes the YAML into the canonical shape, expanding the short-form
//      sugars on the way (atomic-as-bare-list, canvas-as-bare-string, and
//      environments-as-bare-list) — at the top level AND inside each
//      environment override block.
//   2. Resolves `$ENVREF` shorthands in secrets to their literal
//      values from the deployer's environment.
//   3. Validates every field against the spec's binding validation
//      table, collecting all errors into one ParseErrors return so
//      the user sees the whole picture in a single block.
//
// Environment selection + merge (SelectEnvironment) happens AFTER parse,
// driven by the deploy command, so a single parse serves every environment.
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
	"bytes"
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

// Manifest is the parsed Driftfile, after shorthand expansion. The project's
// resource shape is inlined at the top level (the file *is* the project);
// `environments` and `hooks` are optional siblings. Downstream code reads
// m.Slice exactly as before — the only change from v1 is where those keys
// live in the file, not the struct shape they decode into.
type Manifest struct {
	// Slice is the base resource shape, inlined at the Driftfile root: name,
	// retention knobs, and the atomic/backbone/canvas/domains sections. Each
	// environment instantiates a slice from this shape plus its overrides.
	Slice Slice `yaml:",inline"`

	// Environments maps an environment name to a partial slice whose *set*
	// fields override the base when that environment is selected. The bare-list
	// form (`environments: [prod, staging]`) expands to a map of empty bodies.
	// Empty/absent = a single-environment project deployed under its bare name.
	Environments map[string]Slice `yaml:"environments,omitempty"`

	// Hooks are local shell commands run around a deploy (see Hooks).
	Hooks Hooks `yaml:"hooks,omitempty"`

	// Tests are local shell commands `drift project test` runs against a
	// `project run`-started local instance (see Tests).
	Tests Tests `yaml:"tests,omitempty"`

	// baseDir is set after parsing; relative paths in the manifest
	// resolve against it.
	baseDir string `yaml:"-"`
}

// Hooks are shell commands the CLI runs locally around a deploy: pre_deploy
// before anything ships (typically a build/lint), post_deploy after the slice
// is live (typically a smoke test). Commands run in declaration order via the
// shell, from the project root; a non-zero exit aborts. Deliberately NOT a
// pipeline engine — no test stages, env matrices, parallelism, caching, or
// remote execution. Cross-environment orchestration is the user's CI calling
// `drift project deploy` more than once, never a Driftfile concern.
type Hooks struct {
	PreDeploy  []string `yaml:"pre_deploy,omitempty"`
	PostDeploy []string `yaml:"post_deploy,omitempty"`
}

// Tests are shell commands `drift project test` runs once the project is up
// locally (the same instance `drift project run` starts) — e2e/integration
// checks against a real running instance, before anything ships to Drift.
// Commands run in declaration order via the shell, from the project root; a
// non-zero exit fails the run. The instance's local URL rides in as
// DRIFT_TEST_URL so a test command knows where to point (the port is picked
// at runtime, never fixed). Deliberately NOT a pipeline engine — same posture
// as Hooks: no stages, matrices, parallelism, or remote execution.
type Tests struct {
	E2E []string `yaml:"e2e,omitempty"`
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
	Host     string `yaml:"host"`
	Verify   string `yaml:"verify"`             // "dns-txt" (default for v1)
	Wildcard bool   `yaml:"wildcard,omitempty"` // route every subdomain of Host to this slice
}

type AtomicSection struct {
	FunctionMemory  string        `yaml:"function_memory"`
	FunctionTimeout string        `yaml:"function_timeout"`
	RateLimit       string        `yaml:"rate_limit"`
	DeployHistory   int           `yaml:"deploy_history"` // past deploys kept per function (rollback)
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
	NoSQLStorage  string `yaml:"nosql_storage"`
	SQLStorage    string `yaml:"sql_storage"`  // storage per SQL database (e.g. "100MB")
	BlobStorage   string `yaml:"blob_storage"` // total blob storage (the billing driver, e.g. "1GB")
	BlobMaxSize   string `yaml:"blob_max_size"`
	BlobMaxCount  int    `yaml:"blob_max_count"` // free safety quota (not a price driver)
	QueueMaxDepth int    `yaml:"queue_max_depth"`
	SecretMaxSize string `yaml:"secret_max_size"` // max size of one secret value (e.g. "4KB")
	Locks         int    `yaml:"locks"`           // max concurrent Backbone locks
	// RealtimeConnections caps simultaneous live realtime WebSocket
	// connections across the slice (the live pub/sub primitive). Billed in
	// 50-connection blocks; 0 (omitted) means realtime is off for this slice.
	RealtimeConnections int                   `yaml:"realtime_connections"`
	NoSQL               []NoSQLEntry          `yaml:"nosql"`
	Queues              []string              `yaml:"queues"`
	Cache               map[string]CacheEntry `yaml:"cache"`
	Secrets             map[string]string     `yaml:"secrets"`

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
	// TTL: how long a document lives after its LAST write before the
	// platform deletes it — resets on every update. Duration string
	// (durationRe: <int>[smhd]); empty = kept forever. Per-collection,
	// not per-document.
	TTL string `yaml:"ttl"`
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

	// raw.Decode above is lenient about unrecognized keys — a typo'd field
	// name (e.g. "mane:" instead of "name:") is silently dropped, not
	// rejected, because yaml.Node.Decode has no strict/KnownFields option
	// (only the streaming Decoder type does). Re-decode the same
	// (already shorthand-expanded) content through that streaming decoder,
	// purely to catch this — its result is discarded; `m` above remains
	// the one true parse.
	normalized, err := yaml.Marshal(&raw)
	if err != nil {
		return nil, fmt.Errorf("Driftfile: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(normalized))
	dec.KnownFields(true)
	var strict Manifest
	if err := dec.Decode(&strict); err != nil {
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

// ─── Hooks (cheap pre-build parse) ──────────────────────────────────

// ParseHooks decodes ONLY the `hooks:` block, with no validation and no
// file-existence checks. The deploy command calls it BEFORE the full
// ParseDriftfile so a `pre_deploy` build can produce artifacts (e.g. a
// canvas/ dist directory) that the full parse then validates. Hook command
// strings are left verbatim — `${VAR}` in a command is expanded by the shell
// at run time (the deploy environment is already exported), not at the YAML
// layer. A missing/garbled `hooks:` block yields empty hooks, never an error,
// so a half-built project can still run its build step.
func ParseHooks(path string) (Hooks, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return Hooks{}, fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Hooks Hooks `yaml:"hooks"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return Hooks{}, fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	return wrapper.Hooks, nil
}

// ParseTests decodes ONLY the `tests:` block, with no validation and no
// file-existence checks — same cheap-parse posture as ParseHooks, so `drift
// project test` can check "are any tests even declared" before paying for a
// full build.
func ParseTests(path string) (Tests, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return Tests{}, fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Tests Tests `yaml:"tests"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return Tests{}, fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	return wrapper.Tests, nil
}

// ParseProjectName cheaply decodes ONLY the top-level `name` — no validation,
// no `${VAR}`/`$ENVREF` resolution — so commands that just need the project's
// identity (e.g. `drift project stop`/`logs` finding the container) work without
// the project's secrets being set in the environment.
func ParseProjectName(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 — CLI reads the user's manifest by design
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var wrapper struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return "", fmt.Errorf("Driftfile: invalid YAML: %w", err)
	}
	if strings.TrimSpace(wrapper.Name) == "" {
		return "", fmt.Errorf("Driftfile has no name")
	}
	return wrapper.Name, nil
}

// ─── Environment selection + merge ──────────────────────────────────

// SelectEnvironment resolves the deploy target for the chosen environment:
// it validates `env` against the declared environments, deep-merges that
// environment's overrides onto the base slice, and derives the slice name.
// It mutates the manifest in place (m.Slice becomes the effective slice) and
// returns the resolved environment name (empty for a single-environment
// project). `explicit` is true when the user named an environment (positional
// arg or --env), which makes "no environments declared" an error rather than
// a silent fall-through.
//
// Resolution:
//   - No environments declared: a bare-name single slice. An explicit env is
//     an error (nothing to select).
//   - env == "": default to `prod`/`production` if present, else error asking
//     the user to pick one.
//   - env names a declared environment: merge + derive name.
//
// Naming: `prod`/`production` (and the no-environments case) deploy under the
// bare project name; every other environment deploys under `<name>-<env>`.
func (m *Manifest) SelectEnvironment(env string, explicit bool) (string, error) {
	if len(m.Environments) == 0 {
		if explicit && env != "" {
			return "", fmt.Errorf("this project declares no environments, so it can't deploy %q — add an `environments:` block, or drop the argument to deploy the single slice", env)
		}
		return "", nil
	}

	names := sortedKeys(m.Environments)
	if env == "" {
		switch {
		case hasKey(m.Environments, "prod"):
			env = "prod"
		case hasKey(m.Environments, "production"):
			env = "production"
		default:
			return "", fmt.Errorf("this project declares environments (%s) but no default — pick one: drift project deploy <env>", strings.Join(names, ", "))
		}
	}

	overlay, ok := m.Environments[env]
	if !ok {
		return "", fmt.Errorf("unknown environment %q — declared: %s", env, strings.Join(names, ", "))
	}

	base := m.Slice.Name
	m.Slice = mergeSlice(m.Slice, overlay)
	m.Slice.Name = deriveSliceName(base, env)
	if !nameRe.MatchString(m.Slice.Name) {
		return "", fmt.Errorf("derived slice name %q (project %q + environment %q) must be 1–32 lowercase letters, numbers, or hyphens — shorten the project or environment name", m.Slice.Name, base, env)
	}
	return env, nil
}

// deriveSliceName maps (project name, environment) to a slice name. The
// production environment — and a single-environment project — own the bare
// project name; every other environment gets a `-<env>` suffix so its slice
// is a distinct, separately-billed instance.
func deriveSliceName(base, env string) string {
	if env == "" || env == "prod" || env == "production" {
		return base
	}
	return base + "-" + env
}

// mergeSlice deep-merges an environment overlay onto the base slice. Scalar
// config knobs replace the base when set; resource lists/maps replace the base
// only when the overlay provides a non-empty one (the spec's list-override =
// REPLACE rule). The name is never merged — SelectEnvironment derives it.
func mergeSlice(base, overlay Slice) Slice {
	out := base
	if overlay.LogRetention != "" {
		out.LogRetention = overlay.LogRetention
	}
	if overlay.BackupRetention != "" {
		out.BackupRetention = overlay.BackupRetention
	}
	out.Atomic = mergeAtomic(base.Atomic, overlay.Atomic)
	out.Backbone = mergeBackbone(base.Backbone, overlay.Backbone)
	out.Canvas = mergeCanvas(base.Canvas, overlay.Canvas)
	if len(overlay.Domains) > 0 {
		out.Domains = overlay.Domains
	}
	return out
}

func mergeAtomic(base, overlay AtomicSection) AtomicSection {
	out := base
	if overlay.FunctionMemory != "" {
		out.FunctionMemory = overlay.FunctionMemory
	}
	if overlay.FunctionTimeout != "" {
		out.FunctionTimeout = overlay.FunctionTimeout
	}
	if overlay.RateLimit != "" {
		out.RateLimit = overlay.RateLimit
	}
	if overlay.DeployHistory != 0 {
		out.DeployHistory = overlay.DeployHistory
	}
	if len(overlay.Functions) > 0 {
		out.Functions = overlay.Functions
	}
	if overlay.Egress != nil {
		out.Egress = overlay.Egress
	}
	return out
}

func mergeBackbone(base, overlay BackboneSection) BackboneSection {
	out := base
	if overlay.NoSQLStorage != "" {
		out.NoSQLStorage = overlay.NoSQLStorage
	}
	if overlay.SQLStorage != "" {
		out.SQLStorage = overlay.SQLStorage
	}
	if overlay.BlobMaxSize != "" {
		out.BlobMaxSize = overlay.BlobMaxSize
	}
	if overlay.SecretMaxSize != "" {
		out.SecretMaxSize = overlay.SecretMaxSize
	}
	if overlay.BlobMaxCount != 0 {
		out.BlobMaxCount = overlay.BlobMaxCount
	}
	if overlay.QueueMaxDepth != 0 {
		out.QueueMaxDepth = overlay.QueueMaxDepth
	}
	if overlay.Locks != 0 {
		out.Locks = overlay.Locks
	}
	if overlay.RealtimeConnections != 0 {
		out.RealtimeConnections = overlay.RealtimeConnections
	}
	if len(overlay.NoSQL) > 0 {
		out.NoSQL = overlay.NoSQL
	}
	if len(overlay.SQL) > 0 {
		out.SQL = overlay.SQL
	}
	if len(overlay.Queues) > 0 {
		out.Queues = overlay.Queues
	}
	if len(overlay.Cache) > 0 {
		out.Cache = overlay.Cache
	}
	if len(overlay.Secrets) > 0 {
		out.Secrets = overlay.Secrets
	}
	return out
}

func mergeCanvas(base, overlay CanvasSection) CanvasSection {
	out := base
	if overlay.CanvasSize != "" {
		out.CanvasSize = overlay.CanvasSize
	}
	if len(overlay.Sites) > 0 {
		out.Sites = overlay.Sites
	}
	return out
}

// sortedKeys returns the map's keys in deterministic order, for stable
// messages.
func sortedKeys(m map[string]Slice) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func hasKey(m map[string]Slice, k string) bool {
	_, ok := m[k]
	return ok
}

// expandShorthands rewrites every short form documented in the spec into its
// canonical map. With the project-level layout the sections live at the
// Driftfile root (no `slice:` wrapper), and the same section sugars apply
// inside every `environments.<env>` override block:
//
//	atomic: [a, b, c]    -> atomic: { functions: [a, b, c] }
//	canvas: ./path       -> canvas: { sites: [./path] }
//	canvas: [./a, ./b]   -> canvas: { sites: [./a, ./b] }
//
// Plus the environments bare-list sugar:
//
//	environments: [prod, staging] -> environments: { prod: {}, staging: {} }
//
// The per-list short forms inside atomic.functions, canvas.sites, and
// backbone.nosql are handled natively by the element UnmarshalYAML methods.
func expandShorthands(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}

	// Top-level section sugars (atomic-as-list, canvas-as-string/list).
	expandSectionShorthands(doc)

	// environments: expand the bare-list form to a map, then run the same
	// section sugars inside every (non-empty) environment override block.
	if envNode := findChild(doc, "environments"); envNode != nil {
		if envNode.Kind == yaml.SequenceNode {
			expandEnvListToMap(envNode)
		}
		if envNode.Kind == yaml.MappingNode {
			for i := 1; i < len(envNode.Content); i += 2 {
				if body := envNode.Content[i]; body.Kind == yaml.MappingNode {
					expandSectionShorthands(body)
				}
			}
		}
	}

	return nil
}

// expandSectionShorthands rewrites the atomic/canvas section sugars within one
// mapping node (the Driftfile root, or an environment override block).
func expandSectionShorthands(m *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return
	}

	// atomic short form: a sequence becomes { functions: <seq> }.
	if atomicNode := findChild(m, "atomic"); atomicNode != nil && atomicNode.Kind == yaml.SequenceNode {
		wrap := *atomicNode
		atomicNode.Kind = yaml.MappingNode
		atomicNode.Tag = ""
		atomicNode.Style = 0
		atomicNode.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "functions", Tag: "!!str"},
			&wrap,
		}
	}

	// canvas short forms:
	//   string   -> { sites: [string] }
	//   sequence -> { sites: <seq> }
	if canvasNode := findChild(m, "canvas"); canvasNode != nil {
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
}

// expandEnvListToMap rewrites `environments: [prod, staging]` into the
// canonical `environments: { prod: {}, staging: {} }` — each named environment
// inheriting the base shape unchanged.
func expandEnvListToMap(node *yaml.Node) {
	content := make([]*yaml.Node, 0, len(node.Content)*2)
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			continue // be lenient; a malformed entry surfaces at validation
		}
		content = append(content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: item.Value, Tag: "!!str"},
			&yaml.Node{Kind: yaml.MappingNode}, // empty override body
		)
	}
	node.Kind = yaml.MappingNode
	node.Tag = ""
	node.Style = 0
	node.Content = content
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

// UnmarshalYAML accepts either a bare-string (database name → empty
// database) or a map (the long form with name/schema/seed). This
// mirrors nosql so `sql: [ledger]` and the long form both work.
func (s *SQLEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		s.Name = node.Value
		return nil
	}
	type raw SQLEntry
	var r raw
	if err := node.Decode(&r); err != nil {
		return err
	}
	*s = SQLEntry(r)
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
		return nil, fmt.Errorf("${VAR} placeholders reference unset variables: %s (provide them via the environment, a --secret KEY=value flag, or a .env file next to the Driftfile)", strings.Join(names, ", "))
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
		errs = append(errs, "name must be a non-empty string")
	} else if !nameRe.MatchString(m.Slice.Name) {
		errs = append(errs, fmt.Sprintf("name %q must be 1–32 lowercase letters, numbers, or hyphens (no leading/trailing hyphen)", m.Slice.Name))
	}

	// slice-level operational durations
	if v := m.Slice.LogRetention; v != "" && !durationRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("log_retention %q must be an integer ending in s, m, h, or d", v))
	}
	if v := m.Slice.BackupRetention; v != "" && !durationRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backup_retention %q must be an integer ending in s, m, h, or d", v))
	}

	// atomic envelope
	a := m.Slice.Atomic
	if v := a.FunctionMemory; v != "" && !memoryRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("atomic.function_memory %q must be an integer ending in MB or GB", v))
	}
	if v := a.FunctionTimeout; v != "" && !timeoutRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("atomic.function_timeout %q must be an integer ending in s, m, or h", v))
	}
	if v := a.RateLimit; v != "" && !rateRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("atomic.rate_limit %q must be an integer per s, min, or h (e.g. 1000/min)", v))
	}

	// atomic functions
	for i, fn := range a.Functions {
		if !nameRe.MatchString(fn.Name) {
			errs = append(errs, fmt.Sprintf("atomic.functions[%d]: name %q is invalid", i, fn.Name))
			continue
		}
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = resolveBaseDir(m, dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("atomic.functions[%d]: function %q not found at %s", i, fn.Name, dir))
		}
		if v := fn.Cron; v != "" && !cronFiveFieldRe.MatchString(v) {
			errs = append(errs, fmt.Sprintf("atomic.functions[%d]: function %q cron %q is not a valid 5-field cron expression", i, fn.Name, v))
		}
	}

	// backbone envelope
	b := m.Slice.Backbone
	if v := b.NoSQLStorage; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backbone.nosql_storage %q must be an integer ending in KB, MB, or GB", v))
	}
	if v := b.BlobMaxSize; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backbone.blob_max_size %q must be an integer ending in KB, MB, or GB", v))
	}
	if v := b.BlobStorage; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backbone.blob_storage %q must be an integer ending in KB, MB, or GB", v))
	}
	if v := b.SQLStorage; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backbone.sql_storage %q must be an integer ending in KB, MB, or GB", v))
	}
	if v := b.SecretMaxSize; v != "" && !sizeRe.MatchString(v) {
		errs = append(errs, fmt.Sprintf("backbone.secret_max_size %q must be an integer ending in KB, MB, or GB", v))
	}
	if b.Locks < 0 {
		errs = append(errs, "backbone.locks must be >= 0")
	}
	if m.Slice.Atomic.DeployHistory < 0 {
		errs = append(errs, "atomic.deploy_history must be >= 0")
	}
	if b.QueueMaxDepth < 0 {
		errs = append(errs, fmt.Sprintf("backbone.queue_max_depth %d must be a positive integer", b.QueueMaxDepth))
	}
	if b.RealtimeConnections < 0 {
		errs = append(errs, fmt.Sprintf("backbone.realtime_connections %d must be a positive integer", b.RealtimeConnections))
	}

	// nosql collections
	for i, c := range b.NoSQL {
		if !nameRe.MatchString(c.Name) {
			errs = append(errs, fmt.Sprintf("backbone.nosql[%d]: collection name %q is invalid", i, c.Name))
		}
		if c.TTL != "" && !durationRe.MatchString(c.TTL) {
			errs = append(errs, fmt.Sprintf("backbone.nosql[%d]: %q ttl %q must be an integer ending in s, m, h, or d", i, c.Name, c.TTL))
		}
		if c.Seed != "" {
			seedPath := resolveBaseDir(m, c.Seed)
			if _, err := os.Stat(seedPath); err != nil {
				errs = append(errs, fmt.Sprintf("backbone.nosql[%d]: %q seed file not found at %s", i, c.Name, seedPath))
				continue
			}
			if seedErrs := validateJSONLSeed(c.Name, seedPath); len(seedErrs) > 0 {
				errs = append(errs, seedErrs...)
			}
		}
	}

	// queues
	for i, q := range b.Queues {
		if !nameRe.MatchString(q) {
			errs = append(errs, fmt.Sprintf("backbone.queues[%d]: name %q is invalid", i, q))
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
		errs = append(errs, fmt.Sprintf("canvas.canvas_size %q must be an integer ending in KB, MB, or GB", v))
	}
	for i, s := range m.Slice.Canvas.Sites {
		dir := resolveBaseDir(m, s.Dir)
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			errs = append(errs, fmt.Sprintf("canvas.sites[%d]: directory not found at %s", i, dir))
		}
	}

	// environments — per-env config overrides
	for _, name := range sortedKeys(m.Environments) {
		errs = append(errs, validateEnvOverride(name, m.Environments[name])...)
	}

	// hooks — local lifecycle commands
	errs = append(errs, validateHooks(m.Hooks)...)
	errs = append(errs, validateTests(m.Tests)...)

	return errs
}

// validateEnvOverride checks one environment override block. Environment names
// follow the identifier shape; the block may not set `name` (the slice name is
// derived); and every scalar knob it sets must satisfy the same format its base
// section requires. Resource-list overrides (functions/collections/sites) are
// NOT re-checked for on-disk existence here — that surfaces when the merged
// slice deploys, the same as the base resources.
func validateEnvOverride(name string, ov Slice) []string {
	var errs []string
	if !nameRe.MatchString(name) {
		errs = append(errs, fmt.Sprintf("environment name %q must be 1–32 lowercase letters, numbers, or hyphens (no leading/trailing hyphen)", name))
	}
	if strings.TrimSpace(ov.Name) != "" {
		errs = append(errs, fmt.Sprintf("environments.%s must not set name — the slice name is derived from the project name and environment", name))
	}
	check := func(field, val string, re *regexp.Regexp, suffix string) {
		if val != "" && !re.MatchString(val) {
			errs = append(errs, fmt.Sprintf("environments.%s.%s %q must be %s", name, field, val, suffix))
		}
	}
	check("log_retention", ov.LogRetention, durationRe, "an integer ending in s, m, h, or d")
	check("backup_retention", ov.BackupRetention, durationRe, "an integer ending in s, m, h, or d")
	check("atomic.function_memory", ov.Atomic.FunctionMemory, memoryRe, "an integer ending in MB or GB")
	check("atomic.function_timeout", ov.Atomic.FunctionTimeout, timeoutRe, "an integer ending in s, m, or h")
	check("atomic.rate_limit", ov.Atomic.RateLimit, rateRe, "an integer per s, min, or h (e.g. 1000/min)")
	check("backbone.nosql_storage", ov.Backbone.NoSQLStorage, sizeRe, "an integer ending in KB, MB, or GB")
	check("backbone.sql_storage", ov.Backbone.SQLStorage, sizeRe, "an integer ending in KB, MB, or GB")
	check("backbone.blob_max_size", ov.Backbone.BlobMaxSize, sizeRe, "an integer ending in KB, MB, or GB")
	check("backbone.secret_max_size", ov.Backbone.SecretMaxSize, sizeRe, "an integer ending in KB, MB, or GB")
	check("canvas.canvas_size", ov.Canvas.CanvasSize, sizeRe, "an integer ending in KB, MB, or GB")
	return errs
}

// validateHooks rejects empty/whitespace-only hook commands. The commands
// themselves are arbitrary shell — their correctness is the user's, surfaced
// when they run.
func validateHooks(h Hooks) []string {
	var errs []string
	for i, c := range h.PreDeploy {
		if strings.TrimSpace(c) == "" {
			errs = append(errs, fmt.Sprintf("hooks.pre_deploy[%d] is empty", i))
		}
	}
	for i, c := range h.PostDeploy {
		if strings.TrimSpace(c) == "" {
			errs = append(errs, fmt.Sprintf("hooks.post_deploy[%d] is empty", i))
		}
	}
	return errs
}

// validateTests rejects empty/whitespace-only test commands — same posture
// as validateHooks.
func validateTests(t Tests) []string {
	var errs []string
	for i, c := range t.E2E {
		if strings.TrimSpace(c) == "" {
			errs = append(errs, fmt.Sprintf("tests.e2e[%d] is empty", i))
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
