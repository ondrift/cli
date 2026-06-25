package azure

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RefuseCode is a stable, machine-readable reason a resource will not be
// migrated. Stable because REFUSED.md is a contract: Studio quotes from it, a
// customer diffs it across runs, and a sales deck must be able to trust it.
// Never renumber or repurpose a code; add new ones.
type RefuseCode string

const (
	RefuseRuntimeDotNet     RefuseCode = "RUNTIME_DOTNET"
	RefuseRuntimeJava       RefuseCode = "RUNTIME_JAVA"
	RefuseRuntimePowerShell RefuseCode = "RUNTIME_POWERSHELL"
	RefuseRuntimeUnknown    RefuseCode = "RUNTIME_UNVERIFIED"
	RefuseDurableFunctions  RefuseCode = "DURABLE_FUNCTIONS"
	RefuseServiceBusTopic   RefuseCode = "SERVICEBUS_TOPIC"
	RefuseEventGridHub      RefuseCode = "EVENTGRID_OR_EVENTHUB"
	RefuseCosmosServerLogic RefuseCode = "COSMOS_SQL_SERVER_LOGIC"
	RefuseAzureSQL          RefuseCode = "AZURE_SQL_DATABASE"
	RefuseAADIdentity       RefuseCode = "AUTH_AAD_EASYAUTH"
	RefuseSourceBlocked     RefuseCode = "SOURCE_RETRIEVAL_BLOCKED"
	RefuseUnknownBinding    RefuseCode = "BINDING_UNSUPPORTED"
	RefuseTimerTrigger      RefuseCode = "TIMER_TRIGGER"
)

// Refusal is one resource the tool will not move, on the record.
type Refusal struct {
	Code     RefuseCode `json:"code"`
	Resource string     `json:"resource"`
	Detail   string     `json:"detail"`   // what was found
	Guidance string     `json:"guidance"` // what to do about it
}

// refusalForRuntime maps a non-movable runtime to its stable refusal.
func refusalForRuntime(resource, runtime string) (Refusal, bool) {
	switch runtime {
	case "dotnet":
		return Refusal{RefuseRuntimeDotNet, resource, ".NET worker runtime", "No Drift SDK for .NET — keep on Azure, or rewrite the handlers (Studio)."}, true
	case "java":
		return Refusal{RefuseRuntimeJava, resource, "Java worker runtime", "No Drift SDK for Java — keep on Azure, or rewrite the handlers (Studio)."}, true
	case "powershell":
		return Refusal{RefuseRuntimePowerShell, resource, "PowerShell worker runtime", "No Drift SDK for PowerShell — keep on Azure."}, true
	case "python", "node":
		return Refusal{}, false
	default:
		return Refusal{RefuseRuntimeUnknown, resource, "runtime not determined from inventory", "Verify the worker runtime; re-run snapshot once known."}, true
	}
}

// writeRefusedMD renders the canonical REFUSED.md — the honesty artifact.
func writeRefusedMD(dir string, refs []Refusal) error {
	var b strings.Builder
	b.WriteString("# REFUSED — what stays on Azure\n\n")
	if len(refs) == 0 {
		b.WriteString("Nothing was refused: every discovered resource maps to a Drift primitive.\n")
		return os.WriteFile(filepath.Join(dir, "REFUSED.md"), []byte(b.String()), 0o644)
	}
	b.WriteString("These resources were **not** migrated. This is the tool keeping its promise —\n")
	b.WriteString("it moves what fits and refuses the rest, with a reason, rather than producing a\n")
	b.WriteString("slice that \"kind of works\".\n\n")
	b.WriteString("| Code | Resource | Found | What to do |\n")
	b.WriteString("|------|----------|-------|------------|\n")
	sorted := append([]Refusal(nil), refs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })
	for _, r := range sorted {
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", r.Code, r.Resource, r.Detail, r.Guidance))
	}
	return os.WriteFile(filepath.Join(dir, "REFUSED.md"), []byte(b.String()), 0o644)
}

// writeReportMD renders the human-review report: the TODOs a person must finish
// after the Tier-A scaffold (binding → SDK swaps the tool won't guess).
func writeReportMD(dir string, todos []string) error {
	var b strings.Builder
	b.WriteString("# REPORT — manual review needed before deploy\n\n")
	b.WriteString("The transform scaffolds each movable function: the `@atomic` annotation, the\n")
	b.WriteString("handler name, and the Drift `(status, message, payload)` return are correct, and\n")
	b.WriteString("the original body is preserved verbatim. What it does **not** do is guess how\n")
	b.WriteString("your Azure bindings map to Drift SDK calls — those are marked with `TODO(drift)`\n")
	b.WriteString("in the source and listed here.\n\n")
	if len(todos) == 0 {
		b.WriteString("No manual steps detected.\n")
	} else {
		for _, t := range todos {
			b.WriteString("- [ ] " + t + "\n")
		}
	}
	return os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte(b.String()), 0o644)
}

// writePlanMD renders what a subsequent `drift project deploy` would create.
func writePlanMD(dir string, m Manifest, driftfile string) error {
	var b strings.Builder
	b.WriteString("# PLAN — what `drift project deploy` will create\n\n")
	b.WriteString(fmt.Sprintf("Slice: **%s**\n\n", m.slug()))
	b.WriteString(fmt.Sprintf("- %d Atomic function(s)\n", len(m.Functions)))
	b.WriteString(fmt.Sprintf("- %d NoSQL collection(s)\n", len(m.Collections)))
	b.WriteString(fmt.Sprintf("- %d secret(s)\n", len(m.Secrets)))
	b.WriteString(fmt.Sprintf("- %d Canvas site(s)\n\n", len(m.Sites)))
	b.WriteString("Generated Driftfile:\n\n```yaml\n")
	b.WriteString(driftfile)
	b.WriteString("\n```\n\nFrom this folder:\n\n```bash\ndrift project deploy\n```\n")
	return os.WriteFile(filepath.Join(dir, "PLAN.md"), []byte(b.String()), 0o644)
}
