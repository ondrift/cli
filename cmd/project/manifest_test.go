package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDriftfile_RestaurantTemplate parses the canonical restaurant
// Driftfile end-to-end and verifies every field lands where the spec
// says it should. This is the contract test for the v1.0 schema —
// when adding new spec features, extend this test before touching any
// other code.
func TestParseDriftfile_RestaurantTemplate(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "test-resend-key-xyz")
	t.Setenv("SENDER_EMAIL", "noreply@la-cucina.test")

	tmp := writeRestaurantFixture(t)

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}

	if m.Slice.Name != "la-cucina" {
		t.Errorf("slice.name = %q, want %q", m.Slice.Name, "la-cucina")
	}

	// atomic — bare-list shorthand expands to functions:[...]
	if len(m.Slice.Atomic.Functions) != 3 {
		t.Fatalf("atomic.functions count = %d, want 3", len(m.Slice.Atomic.Functions))
	}
	wantFns := []string{"get-menu", "submit-reservation", "confirm-reservation"}
	for i, fn := range m.Slice.Atomic.Functions {
		if fn.Name != wantFns[i] {
			t.Errorf("atomic.functions[%d].name = %q, want %q", i, fn.Name, wantFns[i])
		}
	}

	// backbone.nosql — flat-list of strings
	if len(m.Slice.Backbone.NoSQL) != 1 || m.Slice.Backbone.NoSQL[0].Name != "reservations" {
		t.Errorf("backbone.nosql = %+v, want one entry named reservations", m.Slice.Backbone.NoSQL)
	}

	// backbone.queues — flat-list of strings
	if len(m.Slice.Backbone.Queues) != 1 || m.Slice.Backbone.Queues[0] != "reservation-queue" {
		t.Errorf("backbone.queues = %+v, want [reservation-queue]", m.Slice.Backbone.Queues)
	}

	// backbone.cache — short-form `<key>: <path>` becomes File
	cache := m.Slice.Backbone.Cache
	if e, ok := cache["menu"]; !ok {
		t.Errorf("backbone.cache.menu missing")
	} else if e.File != "./backbone/menu.json" {
		t.Errorf("backbone.cache.menu.file = %q, want ./backbone/menu.json", e.File)
	}

	// backbone.secrets — $ENVREFs are resolved before validation.
	if got := m.Slice.Backbone.Secrets["RESTAURANT_NAME"]; got != "La Cucina" {
		t.Errorf("secret RESTAURANT_NAME = %q, want La Cucina", got)
	}
	if got := m.Slice.Backbone.Secrets["RESEND_API_KEY"]; got != "test-resend-key-xyz" {
		t.Errorf("secret RESEND_API_KEY = %q, want resolved env value", got)
	}
	if got := m.Slice.Backbone.Secrets["SENDER_EMAIL"]; got != "noreply@la-cucina.test" {
		t.Errorf("secret SENDER_EMAIL = %q, want resolved env value", got)
	}

	// canvas — bare-string shorthand becomes sites:[{dir: ./canvas}]
	if len(m.Slice.Canvas.Sites) != 1 || m.Slice.Canvas.Sites[0].Dir != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice.Canvas.Sites)
	}
}

// TestParseDriftfile_MissingEnvref ensures that an unset $ENVREF
// surfaces as a clear validation error, not silent injection of
// an empty string.
func TestParseDriftfile_MissingEnvref(t *testing.T) {
	tmp := writeRestaurantFixture(t)
	// Deliberately leave RESEND_API_KEY/SENDER_EMAIL unset.

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for missing envref, got nil")
	}
	msg := err.Error()
	if !contains(msg, "RESEND_API_KEY") || !contains(msg, "SENDER_EMAIL") {
		t.Errorf("error should mention both unset envrefs, got: %s", msg)
	}
}

// TestParseDriftfile_InvalidName rejects an out-of-shape slice name.
func TestParseDriftfile_InvalidName(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: BAD_Name_With_Underscores
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected validation error for invalid name, got nil")
	}
	if !contains(err.Error(), "name") {
		t.Errorf("error should mention the name field, got: %s", err)
	}
}

// TestParseDriftfile_CanvasShorthandString verifies the bare-string
// canvas shorthand expands correctly.
func TestParseDriftfile_CanvasShorthandString(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice.Canvas.Sites) != 1 || m.Slice.Canvas.Sites[0].Dir != "./canvas" {
		t.Errorf("canvas.sites = %+v, want one entry at ./canvas", m.Slice.Canvas.Sites)
	}
}

// TestParseDriftfile_AtomicShorthandList verifies the bare-list
// atomic shorthand expands correctly.
func TestParseDriftfile_AtomicShorthandList(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
atomic:
  - foo
  - bar
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "foo"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "bar"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice.Atomic.Functions) != 2 {
		t.Fatalf("atomic.functions count = %d, want 2", len(m.Slice.Atomic.Functions))
	}
	if m.Slice.Atomic.Functions[0].Name != "foo" || m.Slice.Atomic.Functions[1].Name != "bar" {
		t.Errorf("atomic.functions = %+v, want [foo bar]", m.Slice.Atomic.Functions)
	}
}

// TestParseDriftfile_SQLShorthandString verifies the bare-string SQL
// shorthand parses (regression: SQLEntry used to reject `sql: [ledger]`
// with "cannot unmarshal !!str into project.SQLEntry"). It mirrors the
// nosql short form — a bare string is a database with no schema/seed,
// created lazily on first use.
func TestParseDriftfile_SQLShorthandString(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
backbone:
  sql:
    - ledger
    - name: audit
      schema: ./sql/audit.sql
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if len(m.Slice.Backbone.SQL) != 2 {
		t.Fatalf("backbone.sql count = %d, want 2", len(m.Slice.Backbone.SQL))
	}
	if m.Slice.Backbone.SQL[0].Name != "ledger" || m.Slice.Backbone.SQL[0].Schema != "" {
		t.Errorf("sql[0] = %+v, want bare {Name: ledger}", m.Slice.Backbone.SQL[0])
	}
	if m.Slice.Backbone.SQL[1].Name != "audit" || m.Slice.Backbone.SQL[1].Schema != "./sql/audit.sql" {
		t.Errorf("sql[1] = %+v, want {Name: audit, Schema: ./sql/audit.sql}", m.Slice.Backbone.SQL[1])
	}
}

// TestParseDriftfile_UnknownTopLevelField catches a typo'd top-level key
// (e.g. "nmae" instead of "name") that the lenient first decode pass used
// to silently drop, leaving the slice unnamed with no error at all. The
// strict KnownFields(true) re-decode in ParseDriftfile must now reject it.
func TestParseDriftfile_UnknownTopLevelField(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
nmae: hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unknown field \"nmae\", got nil")
	}
	if !contains(err.Error(), "nmae") {
		t.Errorf("error should name the unrecognized field, got: %s", err)
	}
}

// TestParseDriftfile_UnknownNestedField catches a typo'd field nested
// under atomic/backbone/canvas (e.g. "schedule" misspelled), which is the
// more common real-world case than a top-level typo.
func TestParseDriftfile_UnknownNestedField(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
backbone:
  nosql: [widgets]
  qeues: [jobs]
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unknown field \"qeues\", got nil")
	}
	if !contains(err.Error(), "qeues") {
		t.Errorf("error should name the unrecognized field, got: %s", err)
	}
}

// writeRestaurantFixture writes a minimal-but-complete restaurant
// project shape into a temp dir and returns the dir path. The
// Driftfile content matches the canonical template under
// templates/sites/hospitality/restaurant/.
func writeRestaurantFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: la-cucina
atomic:
  - get-menu
  - submit-reservation
  - confirm-reservation
backbone:
  nosql: [reservations]
  queues: [reservation-queue]
  cache:
    menu: ./backbone/menu.json
  secrets:
    RESTAURANT_NAME: "La Cucina"
    RESEND_API_KEY:  $RESEND_API_KEY
    SENDER_EMAIL:    $SENDER_EMAIL
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "get-menu"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "submit-reservation"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "confirm-reservation"))
	mustMkdir(t, filepath.Join(tmp, "backbone"))
	mustWrite(t, filepath.Join(tmp, "backbone", "menu.json"), `[]`)
	return tmp
}

// TestCheckRouteCollisions flags two functions that share a route path
// (the deploy identity is method-agnostic, so get:items + post:items would
// shadow each other) and passes when the paths are distinct.
func TestCheckRouteCollisions(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: shop
atomic:
  - items-get
  - items-post
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))
	mustWrite(t, filepath.Join(tmp, "atomic", "items-get", "main.go"),
		"// @atomic http=get:items auth=none\npackage main\n")
	mustWrite(t, filepath.Join(tmp, "atomic", "items-post", "main.go"),
		"// @atomic http=post:items auth=none\npackage main\n")

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	// get:items and post:items are DISTINCT functions — method is part of the
	// identity — so they must NOT collide.
	if err := checkRouteCollisions(m); err != nil {
		t.Errorf("get:items + post:items are distinct functions, should not collide: %v", err)
	}

	// Two functions with the SAME method+path genuinely collide.
	mustWrite(t, filepath.Join(tmp, "atomic", "items-get", "main.go"),
		"// @atomic http=post:items auth=none\npackage main\n") // now also post:items
	m2, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	err = checkRouteCollisions(m2)
	if err == nil {
		t.Fatal("expected a collision error for two post:items, got nil")
	}
	if !strings.Contains(err.Error(), "items") || !strings.Contains(err.Error(), "collision") {
		t.Errorf("collision error should name the route: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestParseDriftfile_BraceVarSubstitution covers the env-aware
// Driftfile feature: ${VAR} placeholders resolve from os.Environ
// before YAML parsing. (With project-level environments the slice name is
// usually derived, not written; ${VAR} still works anywhere in the file.)
func TestParseDriftfile_BraceVarSubstitution(t *testing.T) {
	t.Setenv("ENV", "staging")
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: ${ENV}-hello
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile failed: %v", err)
	}
	if m.Slice.Name != "staging-hello" {
		t.Errorf("slice.name = %q, want staging-hello", m.Slice.Name)
	}
}

// TestParseDriftfile_BraceVarMissing surfaces every unset placeholder
// at once instead of one-at-a-time.
func TestParseDriftfile_BraceVarMissing(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: ${ENV}-${REGION}-app
canvas: ./canvas
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil {
		t.Fatal("expected error for unset ${VAR}, got nil")
	}
	msg := err.Error()
	if !contains(msg, "ENV") || !contains(msg, "REGION") {
		t.Errorf("error should mention both unset vars, got: %s", msg)
	}
}

// TestEnvironments covers the project-level environments feature: per-env
// config overrides deep-merge onto the base, the resource set is shared,
// un-overridden knobs inherit, and the slice name is derived.
func TestEnvironments(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: snip
log_retention: 30d
atomic:
  rate_limit: 5000/min
  function_memory: 128MB
  functions: [redirect]
backbone:
  nosql_storage: 500MB
  nosql: [links]
canvas: ./web
environments:
  prod: {}
  staging:
    log_retention: 3d
    atomic: { rate_limit: 200/min, function_memory: 64MB }
    backbone: { nosql_storage: 50MB }
  dev:
    atomic: { rate_limit: 20/min }
`)
	mustMkdir(t, filepath.Join(tmp, "web"))
	mustMkdir(t, filepath.Join(tmp, "atomic", "redirect"))

	parse := func() *Manifest {
		m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
		if err != nil {
			t.Fatalf("ParseDriftfile: %v", err)
		}
		return m
	}

	// prod → bare name; base values untouched.
	m := parse()
	if env, err := m.SelectEnvironment("prod", true); err != nil || env != "prod" {
		t.Fatalf("select prod: env=%q err=%v", env, err)
	}
	if m.Slice.Name != "snip" {
		t.Errorf("prod name = %q, want snip", m.Slice.Name)
	}
	if m.Slice.Atomic.RateLimit != "5000/min" || m.Slice.LogRetention != "30d" {
		t.Errorf("prod values changed: rate=%q retention=%q", m.Slice.Atomic.RateLimit, m.Slice.LogRetention)
	}

	// staging → suffixed name; scalar overrides applied; resource set shared.
	m = parse()
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	if m.Slice.Name != "snip-staging" {
		t.Errorf("staging name = %q, want snip-staging", m.Slice.Name)
	}
	if m.Slice.Atomic.RateLimit != "200/min" || m.Slice.Atomic.FunctionMemory != "64MB" {
		t.Errorf("staging atomic overrides not applied: %+v", m.Slice.Atomic)
	}
	if m.Slice.LogRetention != "3d" || m.Slice.Backbone.NoSQLStorage != "50MB" {
		t.Errorf("staging overrides not applied: retention=%q nosql_storage=%q", m.Slice.LogRetention, m.Slice.Backbone.NoSQLStorage)
	}
	if len(m.Slice.Atomic.Functions) != 1 || m.Slice.Atomic.Functions[0].Name != "redirect" {
		t.Errorf("staging functions = %+v, want shared [redirect]", m.Slice.Atomic.Functions)
	}
	if len(m.Slice.Backbone.NoSQL) != 1 || m.Slice.Backbone.NoSQL[0].Name != "links" {
		t.Errorf("staging nosql = %+v, want shared [links]", m.Slice.Backbone.NoSQL)
	}

	// dev → only rate overridden; everything else inherits the base.
	m = parse()
	if _, err := m.SelectEnvironment("dev", true); err != nil {
		t.Fatal(err)
	}
	if m.Slice.Name != "snip-dev" || m.Slice.Atomic.RateLimit != "20/min" {
		t.Errorf("dev name/rate = %q/%q", m.Slice.Name, m.Slice.Atomic.RateLimit)
	}
	if m.Slice.Atomic.FunctionMemory != "128MB" || m.Slice.LogRetention != "30d" {
		t.Errorf("dev should inherit base mem/retention: mem=%q retention=%q", m.Slice.Atomic.FunctionMemory, m.Slice.LogRetention)
	}

	// Default (no arg) resolves to prod when present.
	m = parse()
	if env, err := m.SelectEnvironment("", false); err != nil || env != "prod" {
		t.Errorf("default select: env=%q err=%v, want prod", env, err)
	}

	// Unknown environment errors.
	m = parse()
	if _, err := m.SelectEnvironment("nope", true); err == nil {
		t.Error("expected error for unknown environment")
	}
}

// TestEnvironmentsBareList covers the `environments: [a, b]` sugar: each named
// environment inherits the base shape unchanged; the name still derives.
func TestEnvironmentsBareList(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
environments: [prod, staging]
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseDriftfile: %v", err)
	}
	if len(m.Environments) != 2 {
		t.Fatalf("environments count = %d, want 2", len(m.Environments))
	}
	if _, err := m.SelectEnvironment("staging", true); err != nil {
		t.Fatal(err)
	}
	if m.Slice.Name != "hello-staging" {
		t.Errorf("name = %q, want hello-staging", m.Slice.Name)
	}
}

// TestSelectEnvironmentNoEnvironments: a single-slice project ignores an empty
// selection but rejects an explicit one.
func TestSelectEnvironmentNoEnvironments(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), "name: solo\ncanvas: ./canvas\n")
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	m, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatal(err)
	}
	if env, err := m.SelectEnvironment("", false); err != nil || env != "" {
		t.Errorf("no-env default: env=%q err=%v", env, err)
	}
	if m.Slice.Name != "solo" {
		t.Errorf("name = %q, want solo", m.Slice.Name)
	}
	if _, err := m.SelectEnvironment("staging", true); err == nil {
		t.Error("expected error: explicit env on a project that declares none")
	}
}

// TestEnvironmentRejectsName: an override block may not set name.
func TestEnvironmentRejectsName(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./canvas
environments:
  staging:
    name: other
`)
	mustMkdir(t, filepath.Join(tmp, "canvas"))

	_, err := ParseDriftfile(filepath.Join(tmp, "Driftfile"))
	if err == nil || !contains(err.Error(), "must not set name") {
		t.Errorf("expected a 'must not set name' error, got: %v", err)
	}
}

// TestParseHooks reads the hooks block without validating the rest of the
// project (so a pre_deploy build can run before the full parse).
func TestParseHooks(t *testing.T) {
	tmp := t.TempDir()
	mustWrite(t, filepath.Join(tmp, "Driftfile"), `
name: hello
canvas: ./dist
hooks:
  pre_deploy:
    - npm run build
    - npm run lint
  post_deploy: [./smoke.sh]
`)
	// Note: ./dist does NOT exist — ParseHooks must not care.
	h, err := ParseHooks(filepath.Join(tmp, "Driftfile"))
	if err != nil {
		t.Fatalf("ParseHooks: %v", err)
	}
	if len(h.PreDeploy) != 2 || h.PreDeploy[0] != "npm run build" {
		t.Errorf("pre_deploy = %+v", h.PreDeploy)
	}
	if len(h.PostDeploy) != 1 || h.PostDeploy[0] != "./smoke.sh" {
		t.Errorf("post_deploy = %+v", h.PostDeploy)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
