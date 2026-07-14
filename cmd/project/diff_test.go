package project

import "testing"

// TestDiff_OmittedEnvelopeKnobsDoNotShrink is the regression test for the
// free-tier billing bug: a Driftfile that doesn't repeat every envelope
// value (function_memory, rate_limit, etc.) must never be classified as a
// shrink of a live slice that already has those values populated — that
// previously walked a Hacker-tier user straight into VerdictAbort's
// suggested `--allow-destructive` resize, which silently upgrades the
// slice to a paid tier. See docs/alpha-feedback/free-tier-same-footprint-deploy-billing-bug.md.
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
			NoSQL: BackboneNoSQLLimits{MaxCollections: 2, MaxStorageBytes: 10 * 1024 * 1024},
		},
	}

	// A Driftfile that declares the same 3 functions but doesn't repeat any
	// envelope knob — translate.go leaves them at their Go zero value.
	wanted := SliceConfig{
		Atomic: AtomicLimits{
			MaxNumberOfFunctions: 3,
			// MaxFunctionMemoryBytes, MaxFunctionRuntimeInSeconds,
			// MaxNumberOfRequestsPerMinute, MaxNumberOfHoursForLogRetention: omitted (0)
		},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{MaxCollections: 2 /* MaxStorageBytes omitted (0) */},
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
