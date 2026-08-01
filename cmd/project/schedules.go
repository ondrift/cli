// schedules.go — resolving `atomic.functions[].cron` into what the deploy
// artifact carries.
//
// The Driftfile field was documented with worked examples and never read by any
// deploy code; `translate.go` called it "vestigial" while the spec called it the
// feature. This is the half that makes the spec true.

package project

import (
	atomic_cmd "github.com/ondrift/cli/v2/cmd/atomic/cmd/deploy"
)

// declaredSchedules maps function name → cron expression for every Driftfile
// entry carrying a `cron:`.
//
// The key is the function name because that is what the artifact ships and what
// the operator resolves the trigger target from — for an HTTP function the
// deployed name IS its route (`buildTriggerDef` builds
// `http://atomic:8000/api/<name>`), so no path plumbing is needed here.
func declaredSchedules(m *Manifest) map[string]string {
	out := map[string]string{}
	for _, fn := range m.Slice.Atomic.Functions {
		if fn.Cron != "" && fn.Name != "" {
			out[fn.Name] = fn.Cron
		}
	}
	return out
}

// ScheduledFunctionNames lists the functions the Driftfile puts on a schedule,
// for the deploy summary.
func ScheduledFunctionNames(m *Manifest) []string {
	var names []string
	for _, fn := range m.Slice.Atomic.Functions {
		if fn.Cron != "" && fn.Name != "" {
			names = append(names, fn.Name)
		}
	}
	return names
}

// compile-time proof the deploy package still exposes the setter this file
// feeds; a rename there should break here rather than silently stop scheduling.
var _ = atomic_cmd.SetDeclaredSchedules
