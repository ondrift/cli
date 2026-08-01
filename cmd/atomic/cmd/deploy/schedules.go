// schedules.go — the Driftfile's `atomic.functions[].cron` field, carried into
// the deploy artifact.
//
// # Why this goes on the artifact and not through the trigger API
//
// There are two ways to register a schedule with a slice, and only one of them
// is metered. `drift atomic trigger register schedule` posts to the api's
// `/ops/trigger/register`, which proxies straight through to the slice — the
// operator never sees it, so `enforceScheduleLimit` never runs. The deploy
// artifact's `Triggers` field goes through the operator, which counts the new
// schedules, checks them against the slice's envelope, and only then registers.
//
// Scheduled jobs are priced (tier.CentsPerScheduledJob). A schedule the
// envelope never sees is a schedule nobody is billed for, so the metered path
// is the only correct one — which is why the Driftfile field is wired here
// rather than as a post-deploy reconcile pass.
//
// # Why a package-level registry
//
// The element deploy functions are called from `drift project deploy` (which
// has read the Driftfile) and from `drift atomic deploy` (which has not). Only
// the first can supply schedules, and threading a manifest through three deploy
// signatures to carry one optional map would put a Driftfile concept into the
// single-function path that has no Driftfile. Same shape as the existing
// `atomicForce` flag, and for the same reason.

package atomic_cmd

import "sync"

var (
	scheduleMu        sync.RWMutex
	declaredSchedules map[string]string
)

// SetDeclaredSchedules records the `atomic.functions[].cron` expressions from a
// Driftfile, keyed by function name. Called once by `drift project deploy`
// before any function ships; a nil or empty map clears it.
//
// The key is the FUNCTION name, which for an HTTP function is also its route —
// the deploy paths ship `name = f.Path` (see DeployGoElement). That is what
// lets the operator resolve the trigger target from the name alone.
func SetDeclaredSchedules(byFunction map[string]string) {
	scheduleMu.Lock()
	defer scheduleMu.Unlock()
	if len(byFunction) == 0 {
		declaredSchedules = nil
		return
	}
	declaredSchedules = make(map[string]string, len(byFunction))
	for k, v := range byFunction {
		if v != "" {
			declaredSchedules[k] = v
		}
	}
}

// DeclaredScheduleFor returns the cron expression declared for a function, or
// "" if the Driftfile declared none.
func DeclaredScheduleFor(functionName string) string {
	scheduleMu.RLock()
	defer scheduleMu.RUnlock()
	return declaredSchedules[functionName]
}

// scheduleTriggerFor returns the TriggerSpec for a function's declared cron, or
// nil when it has none.
//
// method matters: the slice's trigger registry looks a function up by
// (method, path), so a schedule registered under the wrong verb resolves to
// nothing and fires into a 404 forever — silently, because a trigger that
// cannot find its function still exists and still ticks. It is the function's
// OWN method because the Driftfile's `cron:` is documented as additive: same
// code, two triggers.
func scheduleTriggerFor(functionName, method string) []TriggerSpec {
	expr := DeclaredScheduleFor(functionName)
	if expr == "" {
		return nil
	}
	if method == "" {
		method = "POST"
	}
	return []TriggerSpec{{
		Type:     "schedule",
		Schedule: expr,
		Method:   method,
	}}
}
