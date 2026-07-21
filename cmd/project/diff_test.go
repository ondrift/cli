package project

import "testing"

// TestDiff_OmittedEnvelopeKnobsDoNotShrink is the regression test for the
// free-tier billing bug: a Driftfile that doesn't repeat every envelope
// value (function_memory, rate_limit, etc.) must never be classified as a
// shrink of a live slice that already has those values populated — that
// previously walked a Hacker-tier user straight into VerdictAbort's
// suggested `--allow-destructive` resize, which silently upgrades the
// slice to a paid tier. See docs/alpha-feedback/free-tier-same-footprint-deploy-billing-bug.md.
//
// NoSQL/SQL/Blobs storage used to be exactly this kind of Omittable envelope
// knob (one blanket `nosql_storage` scalar); it isn't any more — size now
// lives ON each declared collection/database/bucket, so there's no separate
// per-primitive envelope value a redeploy could "forget" to repeat while
// still declaring the same resource list (see perItemDeltas in diff.go).
// This test only needs to cover the knobs that are still genuinely
// Omittable scalars.
func TestDiff_OmittedEnvelopeKnobsDoNotShrink(t *testing.T) {
	// A live Hacker-tier slice already has real, non-zero envelope values —
	// exactly like core/common/plan.HackerPreset populates on creation.
	live := SliceConfig{
		Atomic: AtomicLimits{
			MaxNumberOfFunctions:            3,
			MaxFunctionMemoryBytes:          32 * 1024 * 1024,
			MaxFunctionRuntimeInSeconds:     10,
			MaxNumberOfRequestsPerMinute:    60,
			MaxNumberOfHoursForLogRetention: 24,
		},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{MaxCollections: 2, Collections: map[string]int{"a": 5 * 1024 * 1024, "b": 5 * 1024 * 1024}},
		},
	}

	// A Driftfile that declares the same 3 functions and the same two
	// collections (same names, same sizes) but doesn't repeat any Omittable
	// scalar envelope knob — translate.go leaves those at their Go zero value.
	wanted := SliceConfig{
		Atomic: AtomicLimits{
			MaxNumberOfFunctions: 3,
			// MaxFunctionMemoryBytes, MaxFunctionRuntimeInSeconds,
			// MaxNumberOfRequestsPerMinute, MaxNumberOfHoursForLogRetention: omitted (0)
		},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{MaxCollections: 2, Collections: map[string]int{"a": 5 * 1024 * 1024, "b": 5 * 1024 * 1024}},
		},
	}

	d := Diff("myapp", wanted, &live, 0, 0)
	if d.Verdict != VerdictMatch {
		t.Fatalf("verdict = %s, want match — omitted envelope knobs must not be treated as a shrink (shrinks: %+v)", d.Verdict, d.Shrinks)
	}
	if len(d.Shrinks) != 0 {
		t.Fatalf("shrinks = %+v, want none", d.Shrinks)
	}
}

// TestDiff_RealCountZeroIsStillAShrink confirms the fix didn't overcorrect:
// a derived count (e.g. secrets) genuinely declared as zero is a real
// shrink from a live slice that has some, and must still be caught.
func TestDiff_RealCountZeroIsStillAShrink(t *testing.T) {
	live := SliceConfig{Backbone: BackboneLimits{Secrets: BackboneSecretsLimits{MaxCount: 2}}}
	wanted := SliceConfig{} // 0 secrets declared

	d := Diff("myapp", wanted, &live, 0, 0)
	if d.Verdict != VerdictAbort {
		t.Fatalf("verdict = %s, want abort — a real declared count of 0 is a genuine shrink", d.Verdict)
	}
}

// TestDiff_ExplicitSmallerEnvelopeKnobIsStillAShrink confirms an Omittable
// field is only exempted when Wanted is the Go zero value — an explicitly
// declared, smaller-but-nonzero value must still be caught as a shrink.
func TestDiff_ExplicitSmallerEnvelopeKnobIsStillAShrink(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxFunctionMemoryBytes: 32 * 1024 * 1024}}
	wanted := SliceConfig{Atomic: AtomicLimits{MaxFunctionMemoryBytes: 16 * 1024 * 1024}}

	d := Diff("myapp", wanted, &live, 0, 0)
	if d.Verdict != VerdictAbort {
		t.Fatalf("verdict = %s, want abort — an explicit smaller value is a real shrink, not an omission", d.Verdict)
	}
}

// TestDiff_ExplicitLargerEnvelopeKnobIsStillAGrow confirms growing an
// envelope knob still works after the fix.
func TestDiff_ExplicitLargerEnvelopeKnobIsStillAGrow(t *testing.T) {
	live := SliceConfig{Atomic: AtomicLimits{MaxFunctionMemoryBytes: 32 * 1024 * 1024}}
	wanted := SliceConfig{Atomic: AtomicLimits{MaxFunctionMemoryBytes: 128 * 1024 * 1024}}

	d := Diff("myapp", wanted, &live, 0, 500)
	if d.Verdict != VerdictGrow {
		t.Fatalf("verdict = %s, want grow", d.Verdict)
	}
}

// TestDiff_RemovedCollectionIsAShrink confirms that dropping a previously
// declared collection from the Driftfile — even though it's a per-item map
// key disappearing rather than a scalar changing — is still caught as a
// real shrink (its quota effectively goes to 0), not silently ignored the
// way an Omittable scalar's absence would be.
func TestDiff_RemovedCollectionIsAShrink(t *testing.T) {
	live := SliceConfig{Backbone: BackboneLimits{
		NoSQL: BackboneNoSQLLimits{Collections: map[string]int{"posts": 50 * 1024 * 1024}},
	}}
	wanted := SliceConfig{Backbone: BackboneLimits{
		NoSQL: BackboneNoSQLLimits{Collections: map[string]int{}},
	}}

	d := Diff("myapp", wanted, &live, 0, 0)
	if d.Verdict != VerdictAbort {
		t.Fatalf("verdict = %s, want abort — removing a declared collection is a real shrink", d.Verdict)
	}
}

// TestDiff_PerItemShrinkCaughtEvenIfAggregateGrows is the core reason
// per-item deltas exist instead of one summed total: shrinking collection
// "a" while growing collection "b" by more must still trip the abort gate,
// even though the SUM of both moved up. A blanket-total comparison would
// hide the shrink entirely.
func TestDiff_PerItemShrinkCaughtEvenIfAggregateGrows(t *testing.T) {
	live := SliceConfig{Backbone: BackboneLimits{
		NoSQL: BackboneNoSQLLimits{Collections: map[string]int{"a": 100 * 1024 * 1024, "b": 10 * 1024 * 1024}},
	}}
	wanted := SliceConfig{Backbone: BackboneLimits{
		// "a" shrinks by 60MB, "b" grows by 90MB — net +30MB overall.
		NoSQL: BackboneNoSQLLimits{Collections: map[string]int{"a": 40 * 1024 * 1024, "b": 100 * 1024 * 1024}},
	}}

	d := Diff("myapp", wanted, &live, 0, 0)
	if d.Verdict != VerdictAbort {
		t.Fatalf("verdict = %s, want abort — collection \"a\" shrank even though the total grew", d.Verdict)
	}
	foundShrink := false
	for _, s := range d.Shrinks {
		if s.Path == "backbone.nosql.a" {
			foundShrink = true
		}
	}
	if !foundShrink {
		t.Fatalf("shrinks = %+v, want an entry for backbone.nosql.a", d.Shrinks)
	}
}

// TestRenderLineItems_FiltersZeroCostRows confirms informational-only line
// items (UnitCents==0, e.g. "included" resources like NoSQL collections)
// don't clutter the terminal cost-confirm prompt the way they do in the
// browser configurator.
func TestRenderLineItems_FiltersZeroCostRows(t *testing.T) {
	items := []LineItem{
		{Key: "base", Label: "Base", Quantity: 1, UnitCents: 100, SubtotalCent: 100},
		{Key: "atomic_functions", Label: "Functions", Quantity: 3, UnitCents: 5, SubtotalCent: 15},
		{Key: "bb_nosql", Label: "NoSQL collections", Quantity: 2, UnitCents: 0, SubtotalCent: 0},
	}
	out := renderLineItems(items)
	if !contains(out, "Base") || !contains(out, "Functions") {
		t.Fatalf("expected priced items in output, got: %q", out)
	}
	if contains(out, "NoSQL collections") {
		t.Fatalf("expected zero-cost item to be filtered out, got: %q", out)
	}
}
