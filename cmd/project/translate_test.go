package project

import (
	"testing"
)

// TestManifestToSliceConfig_Counts verifies that resource lists are
// counted correctly and land in the right Max… fields.
func TestManifestToSliceConfig_Counts(t *testing.T) {
	m := &Manifest{
		Slice: Slice{
			Name: "test",
			Atomic: AtomicSection{
				Functions: []AtomicEntry{
					{Name: "a"},
					{Name: "b"},
					{Name: "c", Cron: "0 * * * *"}, // scheduled
				},
			},
			Backbone: BackboneSection{
				NoSQL:               []NoSQLEntry{{Name: "x", Size: "10MB"}, {Name: "y", Size: "20MB"}},
				Queues:              []string{"q1", "q2", "q3"},
				Secrets:             map[string]string{"A": "1", "B": "2"},
				RealtimeConnections: 200,
			},
		},
	}
	cfg, err := ManifestToSliceConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Atomic.MaxNumberOfFunctions != 3 {
		t.Errorf("functions: got %d, want 3", cfg.Atomic.MaxNumberOfFunctions)
	}
	if cfg.Atomic.MaxNumberOfScheduledJobs != 1 {
		t.Errorf("scheduled: got %d, want 1", cfg.Atomic.MaxNumberOfScheduledJobs)
	}
	if cfg.Backbone.NoSQL.MaxCollections != 2 {
		t.Errorf("collections: got %d, want 2", cfg.Backbone.NoSQL.MaxCollections)
	}
	if cfg.Backbone.Queues.MaxQueues != 3 {
		t.Errorf("queues: got %d, want 3", cfg.Backbone.Queues.MaxQueues)
	}
	if cfg.Backbone.Secrets.MaxCount != 2 {
		t.Errorf("secrets: got %d, want 2", cfg.Backbone.Secrets.MaxCount)
	}
	if cfg.Backbone.Realtime.MaxConcurrentConnections != 200 {
		t.Errorf("realtime: got %d, want 200", cfg.Backbone.Realtime.MaxConcurrentConnections)
	}
}

// TestManifestToSliceConfig_EnvelopeKnobs verifies that envelope
// strings parse into the right integer values.
func TestManifestToSliceConfig_EnvelopeKnobs(t *testing.T) {
	m := &Manifest{
		Slice: Slice{
			Name: "test",
			Atomic: AtomicSection{
				FunctionMemory:  "256MB",
				FunctionTimeout: "60s",
				RateLimit:       "1000/min",
			},
			Backbone: BackboneSection{
				NoSQL:         []NoSQLEntry{{Name: "events", Size: "500MB"}},
				BlobMaxSize:   "5MB",
				QueueMaxDepth: 1000,
			},
			Canvas:          CanvasSection{CanvasSize: "100MB"},
			LogRetention:    "7d",
			BackupRetention: "14d",
		},
	}
	cfg, err := ManifestToSliceConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Atomic.MaxFunctionMemoryBytes; got != 256*1024*1024 {
		t.Errorf("function_memory: got %d, want %d", got, 256*1024*1024)
	}
	if cfg.Atomic.MaxFunctionRuntimeInSeconds != 60 {
		t.Errorf("function_timeout: got %d, want 60", cfg.Atomic.MaxFunctionRuntimeInSeconds)
	}
	if cfg.Atomic.MaxNumberOfRequestsPerMinute != 1000 {
		t.Errorf("rate_limit: got %d, want 1000", cfg.Atomic.MaxNumberOfRequestsPerMinute)
	}
	if cfg.Backbone.NoSQL.Collections["events"] != 500*1024*1024 {
		t.Errorf("nosql[events].size: got %d, want %d", cfg.Backbone.NoSQL.Collections["events"], 500*1024*1024)
	}
	if cfg.Backbone.Blobs.MaxSizeInBytesEach != 5*1024*1024 {
		t.Errorf("blob_max_size: got %d, want %d", cfg.Backbone.Blobs.MaxSizeInBytesEach, 5*1024*1024)
	}
	if cfg.Canvas.TotalMaxSizeInBytes != 100*1024*1024 {
		t.Errorf("canvas_size: got %d, want %d", cfg.Canvas.TotalMaxSizeInBytes, 100*1024*1024)
	}
	if cfg.Atomic.MaxNumberOfHoursForLogRetention != 24*7 {
		t.Errorf("log_retention: got %d, want %d", cfg.Atomic.MaxNumberOfHoursForLogRetention, 24*7)
	}
	if cfg.Backbone.BackupRetentionDays != 14 {
		t.Errorf("backup_retention: got %d, want 14", cfg.Backbone.BackupRetentionDays)
	}
}

// TestManifestToSliceConfig_RatePerS verifies the /s and /h rate
// shortcuts normalise to per-minute correctly.
func TestManifestToSliceConfig_RatePerS(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"100/s", 6000},
		{"1000/min", 1000},
		{"60000/h", 1000},
	}
	for _, c := range cases {
		m := &Manifest{Slice: Slice{Name: "t", Atomic: AtomicSection{RateLimit: c.in}}}
		cfg, err := ManifestToSliceConfig(m)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got := cfg.Atomic.MaxNumberOfRequestsPerMinute; got != c.want {
			t.Errorf("%s: got %d rpm, want %d", c.in, got, c.want)
		}
	}
}

// TestDiff_CreatePath verifies a missing live slice produces a Create
// verdict with all positive Wanted fields as grows.
func TestDiff_CreatePath(t *testing.T) {
	manifest := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5, MaxFunctionMemoryBytes: 64 * 1024 * 1024},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{MaxCollections: 2}},
	}
	d := Diff("hello", manifest, nil, 0, 0)
	if d.Verdict != VerdictCreate {
		t.Errorf("verdict: got %s, want create", d.Verdict)
	}
	if !d.IsNewSlice {
		t.Error("IsNewSlice should be true on Create")
	}
	if len(d.Grows) != 3 { // functions, memory, collections
		t.Errorf("grows: got %d, want 3 (got %+v)", len(d.Grows), d.Grows)
	}
}

// TestDiff_MatchPath verifies identical manifests produce a Match
// verdict with no grows or shrinks.
func TestDiff_MatchPath(t *testing.T) {
	cfg := SliceConfig{
		Atomic: AtomicLimits{MaxNumberOfFunctions: 5},
	}
	d := Diff("hello", cfg, &cfg, 1500, 1500)
	if d.Verdict != VerdictMatch {
		t.Errorf("verdict: got %s, want match", d.Verdict)
	}
	if len(d.Grows) != 0 || len(d.Shrinks) != 0 {
		t.Errorf("expected no deltas, got grows=%d shrinks=%d", len(d.Grows), len(d.Shrinks))
	}
}

// TestDiff_GrowPath verifies a Wanted > Live in some field produces
// a Grow verdict and the field shows up in d.Grows.
func TestDiff_GrowPath(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 3}}
	manifest := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 5}}
	d := Diff("hello", manifest, &live, 1500, 3000)
	if d.Verdict != VerdictGrow {
		t.Errorf("verdict: got %s, want grow", d.Verdict)
	}
	if len(d.Grows) != 1 || d.Grows[0].Path != "atomic.functions" {
		t.Errorf("grows: got %+v, want one entry for atomic.functions", d.Grows)
	}
	if d.Grows[0].Delta() != 2 {
		t.Errorf("delta: got %d, want 2", d.Grows[0].Delta())
	}
}

// TestDiff_AbortPath verifies a Wanted < Live in some field produces
// an Abort verdict — the load-bearing safety property.
func TestDiff_AbortPath(t *testing.T) {
	live := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{MaxCollections: 6}},
	}
	manifest := SliceConfig{
		Atomic:   AtomicLimits{MaxNumberOfFunctions: 5},
		Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{MaxCollections: 4}},
	}
	d := Diff("hello", manifest, &live, 3000, 1500)
	if d.Verdict != VerdictAbort {
		t.Errorf("verdict: got %s, want abort", d.Verdict)
	}
	if len(d.Shrinks) != 1 || d.Shrinks[0].Path != "backbone.nosql_collections" {
		t.Errorf("shrinks: got %+v, want one entry for nosql_collections", d.Shrinks)
	}
}

// TestRenderDiff_FreeWording verifies the binary "This slice is free."
// vs "€N/month" wording rule.
func TestRenderDiff_FreeWording(t *testing.T) {
	d := Diff("hello", SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 1}}, nil, 0, 0)
	out := RenderDiff(d)
	if !contains(out, "This slice is free.") {
		t.Errorf("free slice: got %q, want 'This slice is free.'", out)
	}

	d2 := Diff("hello", SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 50}}, nil, 0, 1500)
	out2 := RenderDiff(d2)
	if !contains(out2, "Cost: €15/month") {
		t.Errorf("paid slice: want 'Cost: €15/month' in output, got: %s", out2)
	}
}

// TestRenderDiff_FreeToPaidCrossing verifies the explicit
// "free → €N/mo" wording when a grow crosses the Hacker boundary.
func TestRenderDiff_FreeToPaidCrossing(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 3}}
	manifest := SliceConfig{Atomic: AtomicLimits{MaxNumberOfFunctions: 8}}
	d := Diff("hello", manifest, &live, 0, 1500)
	out := RenderDiff(d)
	if !contains(out, "free → €15/mo") {
		t.Errorf("expected 'free → €15/mo' in output, got: %s", out)
	}
}

// TestRenderDiff_AbortMessage verifies the abort UX names the offending
// fields and gives the operator the escape hatch (`drift slice resize`).
func TestRenderDiff_AbortMessage(t *testing.T) {
	live := SliceConfig{Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{MaxCollections: 6}}}
	manifest := SliceConfig{Backbone: BackboneLimits{NoSQL: BackboneNoSQLLimits{MaxCollections: 4}}}
	d := Diff("hello", manifest, &live, 1500, 1000)
	out := RenderDiff(d)

	for _, want := range []string{
		"Refusing to deploy",
		"backbone.nosql_collections",
		"6 (current) > 4 (declared)",
		"drift slice resize --from Driftfile --allow-destructive",
		"--no-slice-reconcile",
	} {
		if !contains(out, want) {
			t.Errorf("abort message missing %q\nfull output:\n%s", want, out)
		}
	}
}
