package project

import (
	"os"
	"path/filepath"
	"testing"
)

// projectWith writes a throwaway project tree and returns a Manifest rooted at
// it. files is path→contents, relative to the project root.
func projectWith(t *testing.T, fns []AtomicEntry, files map[string]string) *Manifest {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	m := &Manifest{Slice: Slice{Name: "test", Atomic: AtomicSection{Functions: fns}}}
	m.baseDir = dir
	return m
}

// THE bug. A Driftfile declaring `cron:` sized its envelope at ZERO scheduled
// jobs, because the count read only `@atomic cron=` — the annotation that
// cannot deploy. Zero is not a small number here: enforceScheduleLimit treats
// `max <= 0` as "no quota to enforce", so the slice was billed for none AND
// gated at none.
func TestCountScheduledFunctions_CountsTheDriftfileField(t *testing.T) {
	m := projectWith(t, []AtomicEntry{
		{Name: "get-menu"},
		{Name: "nightly-rollup", Cron: "0 2 * * *"},
		{Name: "hourly-cleanup", Cron: "0 * * * *"},
	}, map[string]string{
		"atomic/get-menu/main.go":       "// @atomic http=get:/get-menu\nfunc GetMenu() {}\n",
		"atomic/nightly-rollup/main.go": "// @atomic http=post:/nightly-rollup\nfunc Rollup() {}\n",
		"atomic/hourly-cleanup/main.go": "// @atomic http=post:/hourly-cleanup\nfunc Cleanup() {}\n",
	})

	got, err := CountScheduledFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Errorf("two declared schedules must size the envelope at 2 — a zero envelope is billed for none and enforced at none, got %d", got)
	}
}

// The count must not depend on reading the source tree. A manifest preflight
// runs before any deploy, and returning zero there would size a slice for no
// scheduled jobs and then let it register them unmetered.
func TestCountScheduledFunctions_SurvivesAnUnreadableTree(t *testing.T) {
	m := &Manifest{Slice: Slice{Name: "test", Atomic: AtomicSection{
		Functions: []AtomicEntry{
			{Name: "nightly-rollup", Cron: "0 2 * * *"},
			{Name: "get-menu"},
		},
	}}}
	m.baseDir = filepath.Join(t.TempDir(), "does-not-exist")

	got, _ := CountScheduledFunctions(m)
	if got != 1 {
		t.Errorf("the Driftfile half needs no source access and must still be counted, got %d", got)
	}
}

// A function carrying BOTH declarations is one scheduled job, not two.
func TestCountScheduledFunctions_DoesNotDoubleCount(t *testing.T) {
	m := projectWith(t, []AtomicEntry{
		{Name: "nightly-rollup", Cron: "0 2 * * *"},
	}, map[string]string{
		"atomic/nightly-rollup/main.go": "// @atomic cron=\"0 2 * * *\"\nfunc Rollup() {}\n",
	})

	got, err := CountScheduledFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("one function is one scheduled job however many ways it says so, got %d", got)
	}
}

// A project with no schedules sizes at zero — which is correct here, and means
// "nothing to enforce" rather than "enforced at nothing".
func TestCountScheduledFunctions_NoSchedulesIsZero(t *testing.T) {
	m := projectWith(t, []AtomicEntry{{Name: "get-menu"}}, map[string]string{
		"atomic/get-menu/main.go": "// @atomic http=get:/get-menu\nfunc GetMenu() {}\n",
	})

	got, err := CountScheduledFunctions(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("want 0, got %d", got)
	}
}

// declaredSchedules is what reaches the deploy artifact. It keys on the
// function NAME because that is what the operator resolves the trigger target
// from — for an HTTP function the deployed name IS its route.
func TestDeclaredSchedules_KeyedByFunctionName(t *testing.T) {
	m := &Manifest{Slice: Slice{Atomic: AtomicSection{Functions: []AtomicEntry{
		{Name: "get-menu"},
		{Name: "nightly-rollup", Cron: "0 2 * * *"},
		{Name: "", Cron: "0 5 * * *"}, // nameless: unroutable, must be dropped
	}}}}

	got := declaredSchedules(m)
	if len(got) != 1 {
		t.Fatalf("only the named, cron-bearing entry may be published, got %v", got)
	}
	if got["nightly-rollup"] != "0 2 * * *" {
		t.Errorf("want the cron keyed by function name, got %v", got)
	}
}

// The envelope the slice is actually sized with.
func TestManifestToSliceConfig_SizesTheScheduledJobEnvelope(t *testing.T) {
	m := projectWith(t, []AtomicEntry{
		{Name: "a"},
		{Name: "nightly", Cron: "0 2 * * *"},
	}, map[string]string{
		"atomic/a/main.go":       "// @atomic http=get:/a\nfunc A() {}\n",
		"atomic/nightly/main.go": "// @atomic http=post:/nightly\nfunc N() {}\n",
	})

	cfg, err := ManifestToSliceConfig(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Atomic.MaxNumberOfScheduledJobs != 1 {
		t.Errorf("the declared schedule must reach the envelope — at 0 the operator enforces nothing, got %d", cfg.Atomic.MaxNumberOfScheduledJobs)
	}
}
