package atomic_cmd

import "testing"

// The Driftfile's `cron:` reaches the deploy artifact, which is the whole point
// — the field was documented with worked examples and read by nothing.
func TestScheduleTriggerFor_DeclaredCronBecomesAScheduleTrigger(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"nightly-rollup": "0 2 * * *"})

	got := scheduleTriggerFor("nightly-rollup", "get")
	if len(got) != 1 {
		t.Fatalf("a declared cron must produce exactly one trigger, got %d", len(got))
	}
	if got[0].Type != "schedule" {
		t.Errorf("type must be schedule, got %q", got[0].Type)
	}
	if got[0].Schedule != "0 2 * * *" {
		t.Errorf("the cron expression must be carried verbatim, got %q", got[0].Schedule)
	}
}

// THE field that decides whether the schedule ever fires. The slice's trigger
// registry looks a function up by (method, path); a schedule registered under
// the wrong verb resolves to nothing and ticks forever into a 404 — silently,
// because a trigger that cannot find its function still exists and still runs.
func TestScheduleTriggerFor_CarriesTheFunctionsOwnMethod(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"report": "0 * * * *"})

	for _, method := range []string{"get", "post", "put", "delete", "patch"} {
		got := scheduleTriggerFor("report", method)
		if len(got) != 1 {
			t.Fatalf("method %s: want one trigger, got %d", method, len(got))
		}
		if got[0].Method != method {
			t.Errorf("a schedule must fire against the function's OWN method — the registry keys on (method, path), so a mismatch ticks into a 404 forever. want %q, got %q", method, got[0].Method)
		}
	}
}

// A function with no declared cron gets no trigger. Otherwise every deploy
// would register a schedule nobody asked for and be billed for it.
func TestScheduleTriggerFor_UndeclaredFunctionGetsNothing(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"nightly-rollup": "0 2 * * *"})

	if got := scheduleTriggerFor("get-menu", "get"); got != nil {
		t.Errorf("an undeclared function must get no schedule, got %+v", got)
	}
}

// With nothing published at all — which is every `drift atomic deploy`, since
// that command never reads a Driftfile — no function is scheduled.
func TestScheduleTriggerFor_NoDriftfileMeansNoSchedules(t *testing.T) {
	SetDeclaredSchedules(nil)

	if got := scheduleTriggerFor("anything", "get"); got != nil {
		t.Errorf("a deploy with no Driftfile must schedule nothing, got %+v", got)
	}
}

// An empty expression is not a declaration. Sent through, it would register a
// schedule trigger the slice refuses ("schedule required for schedule
// triggers") and fail the deploy over a field the user left blank.
func TestSetDeclaredSchedules_EmptyExpressionsAreDropped(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"a": "", "b": "0 2 * * *"})

	if got := scheduleTriggerFor("a", "get"); got != nil {
		t.Errorf("a blank cron is not a declaration, got %+v", got)
	}
	if got := scheduleTriggerFor("b", "get"); len(got) != 1 {
		t.Errorf("a real cron beside it must survive, got %+v", got)
	}
}

// The registry is replaced, not merged. Two `drift project deploy` runs in one
// process (the test suite, or a future multi-slice deploy) must not leak the
// first Driftfile's schedules into the second.
func TestSetDeclaredSchedules_ReplacesRatherThanMerges(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })

	SetDeclaredSchedules(map[string]string{"first": "0 1 * * *"})
	SetDeclaredSchedules(map[string]string{"second": "0 2 * * *"})

	if got := scheduleTriggerFor("first", "get"); got != nil {
		t.Error("a second Driftfile must not inherit the first's schedules — that would register a job the project no longer declares")
	}
	if got := scheduleTriggerFor("second", "get"); len(got) != 1 {
		t.Error("the current Driftfile's schedule must be live")
	}
}

// A missing method defaults to POST rather than to an empty string, which the
// operator would otherwise have to guess at.
func TestScheduleTriggerFor_BlankMethodDefaults(t *testing.T) {
	t.Cleanup(func() { SetDeclaredSchedules(nil) })
	SetDeclaredSchedules(map[string]string{"x": "0 2 * * *"})

	got := scheduleTriggerFor("x", "")
	if len(got) != 1 || got[0].Method != "POST" {
		t.Errorf("want a POST default, got %+v", got)
	}
}
