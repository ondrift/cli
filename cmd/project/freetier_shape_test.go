package project

// freetier_shape_test.go pins the free-tier first-run path.
//
// The trap this file exists for (HQ CLI-STANDARDUSAGE-T17KKN): a free slice
// is provisioned at the FIXED Hacker preset — 5 functions, 1 scheduled job,
// 2 NoSQL collections, 1 queue, 5 secrets, 50MB canvas — which is larger
// than any honest first Driftfile. The manifest is therefore "smaller" than
// the live slice in several dimensions at once, the never-shrink gate fires,
// and a first-time user is sent to a command with --allow-destructive in its
// name to destroy capacity that has never held anything.
//
// Two distinct ways in, both reproduced below:
//
//  1. `drift slice create <name> --free` followed by `drift project deploy`.
//     Independent of price — the slice is already sitting at the preset.
//
//  2. `drift project deploy` twice, no `slice create` at all, when the
//     manifest prices at €0. reconcileSlice picks tier "hacker" for a
//     zero-cost manifest, CreateSlice then omits the config entirely for
//     that tier (api.go), and the operator's priceAndValidate discards any
//     config it is sent and writes plan.HackerConfig instead
//     (platform src/core/operator/routes/slice_create.go). So the slice
//     comes up at the full preset and deploy #2 of the same unchanged
//     project refuses.
//
// Case 2 needs a genuinely zero-cost manifest to reach the "hacker" branch.
// Verified against the platform's own tier.PriceConfig on 2026-07-27:
// a canvas-only project at 10MB prices at 0 cents; 2 functions + one 5MB
// collection prices at 10 cents (CentsPerFunction is 5 and there is NO free
// allowance in the pricing model), so THAT project is created as "custom",
// keeps its declared shape, and is not affected by case 2 at all.

import "testing"

// hackerPresetLive is the CLI-side mirror of what the server actually writes
// into a free slice's config on creation: platform src/common/tier/tiers.go
// HackerPreset, via tier.HackerConfig. Values are copied literally so this
// test fails loudly if the preset moves under it.
func hackerPresetLive() SliceConfig {
	return SliceConfig{
		Canvas: CanvasLimits{TotalMaxSizeInBytes: 50 * 1024 * 1024},
		Atomic: AtomicLimits{
			MaxNumberOfFunctions:            5,
			MaxFunctionRuntimeInSeconds:     10,
			MaxNumberOfDeploymentsInHistory: 1,
			MaxNumberOfHoursForLogRetention: 24,
			MaxNumberOfRequestsPerMinute:    60,
			MaxNumberOfScheduledJobs:        1,
			MaxFunctionMemoryBytes:          32 * 1024 * 1024,
		},
		Backbone: BackboneLimits{
			Secrets:             BackboneSecretsLimits{MaxCount: 5, MaxSizeInBytesEach: 1024},
			Blobs:               BackboneBlobsLimits{MaxCount: 10, MaxSizeInBytesEach: 5 * 1024 * 1024},
			NoSQL:               BackboneNoSQLLimits{MaxCollections: 2},
			SQL:                 BackboneSQLLimits{MaxDatabases: 1},
			Queues:              BackboneQueuesLimits{MaxQueues: 1, MaxDepthEach: 500},
			Realtime:            BackboneRealtimeLimits{MaxConcurrentConnections: 50},
			Locks:               BackboneLocksLimits{MaxConcurrent: 100000},
			BackupRetentionDays: 3,
		},
	}
}

// honestFirstProject is what someone writes on day one: two functions and
// one collection. Every envelope knob is omitted, which translate.go leaves
// at the Go zero value on purpose. Prices at 10 cents.
func honestFirstProject() SliceConfig {
	return SliceConfig{
		Atomic: AtomicLimits{MaxNumberOfFunctions: 2},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{
				MaxCollections: 1,
				Collections:    map[string]int{"notes": 5 * 1024 * 1024},
			},
		},
	}
}

// canvasOnlyProject is a static site and nothing else — the flagship free
// use case, and the one manifest shape that genuinely prices at €0 and so
// selects the "hacker" tier on create.
func canvasOnlyProject() SliceConfig {
	return SliceConfig{
		Canvas: CanvasLimits{TotalMaxSizeInBytes: 10 * 1024 * 1024},
	}
}

// TestFreeTier_DeployAfterFreeCreateIsNotAShrink is case 1: the path the
// card filed. A free slice sitting at the Hacker preset plus an honest first
// Driftfile must not be classified as a destructive shrink — the preset's
// spare capacity is empty slots the user never asked for and cannot give
// back, and the resulting Match sends no resize at all.
func TestFreeTier_DeployAfterFreeCreateIsNotAShrink(t *testing.T) {
	live := hackerPresetLive()

	d := Diff("lab", honestFirstProject(), &live, TierHacker, 0, 0)

	if d.Verdict == VerdictAbort {
		t.Fatalf("verdict = abort — the first deploy after `slice create --free` is refused.\nshrinks: %+v", d.Shrinks)
	}
	// Grow, not Match: the free grant hands out 2 collection SLOTS but no
	// per-collection storage quota (HackerPreset sets MaxCollections and
	// leaves Collections nil), so a Driftfile declaring `notes: 5MB` really
	// does need that quota provisioned. The point of the fix is that this
	// deploy PROCEEDS — 5MB of storage still prices at €0, so the slice
	// stays free — rather than being refused as a destructive shrink.
	if d.Verdict != VerdictGrow {
		t.Fatalf("verdict = %s, want grow (the declared collection's quota)", d.Verdict)
	}
	// The five dimensions the preset over-granted must be recorded as spare
	// allowance rather than silently dropped.
	if len(d.UnusedFreeGrant) == 0 {
		t.Fatal("UnusedFreeGrant is empty — the spare allowance should be recorded, not silently dropped")
	}
	for _, u := range d.UnusedFreeGrant {
		if u.Delta() >= 0 {
			t.Fatalf("UnusedFreeGrant contains a non-shrink entry %+v", u)
		}
	}
}

// TestFreeTier_SecondDeployOfUnchangedCanvasProject is case 2, reached by
// the DOCUMENTED golden path with no `slice create` involved.
func TestFreeTier_SecondDeployOfUnchangedCanvasProject(t *testing.T) {
	manifest := canvasOnlyProject()

	// Deploy #1: no live slice → Create. Manifest prices at €0, so
	// reconcileSlice picks tier "hacker" and the config is discarded.
	first := Diff("site", manifest, nil, "", 0, 0)
	if first.Verdict != VerdictCreate {
		t.Fatalf("first deploy verdict = %s, want create", first.Verdict)
	}

	// What the server actually provisions, regardless of what was declared.
	live := hackerPresetLive()

	// Deploy #2: same project, nothing edited.
	second := Diff("site", manifest, &live, TierHacker, 0, 0)
	if second.Verdict == VerdictAbort {
		t.Fatalf("second deploy of an UNCHANGED project = abort.\nshrinks: %+v", second.Shrinks)
	}
}

// TestFreeTier_GrowingPastTheFreeCeilingStillGrows confirms the fix didn't
// overcorrect into "a free slice never changes shape". Declaring MORE than
// the free grant is still a Grow — that is the path onto a paid shape — and
// the unused dimensions elsewhere in the manifest must not suppress it.
func TestFreeTier_GrowingPastTheFreeCeilingStillGrows(t *testing.T) {
	live := hackerPresetLive()
	manifest := SliceConfig{
		// 8 > the free grant's 5, while secrets/queues/scheduled go unused.
		Atomic: AtomicLimits{MaxNumberOfFunctions: 8},
	}

	d := Diff("lab", manifest, &live, TierHacker, 0, 4000)
	if d.Verdict != VerdictGrow {
		t.Fatalf("verdict = %s, want grow — 8 functions exceeds the free grant of 5", d.Verdict)
	}
	found := false
	for _, g := range d.Grows {
		if g.Path == "atomic.functions" {
			found = true
		}
	}
	if !found {
		t.Fatalf("grows = %+v, want an entry for atomic.functions", d.Grows)
	}
}

// TestFreeTier_PaidSliceStillRefusesAShrink is the control. It passed before
// the fix and must keep passing: on a slice whose shape the user chose and
// pays for, a manifest that declares less is a real shrink and the gate
// must hold.
func TestFreeTier_PaidSliceStillRefusesAShrink(t *testing.T) {
	live := SliceConfig{
		Atomic: AtomicLimits{MaxNumberOfFunctions: 40},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{
				MaxCollections: 12,
				Collections:    map[string]int{"events": 2 * 1024 * 1024 * 1024},
			},
		},
	}
	wanted := SliceConfig{
		Atomic: AtomicLimits{MaxNumberOfFunctions: 3},
		Backbone: BackboneLimits{
			NoSQL: BackboneNoSQLLimits{
				MaxCollections: 1,
				Collections:    map[string]int{"events": 5 * 1024 * 1024},
			},
		},
	}

	d := Diff("prod", wanted, &live, "custom", 4200, 300)
	if d.Verdict != VerdictAbort {
		t.Fatalf("verdict = %s, want abort — a paid slice declaring less IS a real shrink", d.Verdict)
	}
	if len(d.UnusedFreeGrant) != 0 {
		t.Fatalf("UnusedFreeGrant = %+v, want empty on a paid slice", d.UnusedFreeGrant)
	}
}
