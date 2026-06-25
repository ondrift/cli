package azure

// prereqs.go — the provider-side tools the snapshot needs are prerequisites, like
// `az` itself. Rather than silently refusing a Cosmos account or a function app
// mid-run (and exiting 0 with the skip buried in REFUSED.md), the snapshot checks
// them UP FRONT against the discovered inventory and fails loudly if one is
// missing. The checks are conditional: mongoexport only matters when there's a
// Cosmos account; unsquashfs only when function source is fetched live (Linux
// Consumption packages it as SquashFS), so `--source` for every app sidesteps it.

import (
	"fmt"
	"os/exec"
	"strings"
)

// onPath reports whether an external tool is available on PATH. A package var so
// tests can stub tool availability deterministically.
var onPath = func(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// requireSnapshotTools fails loudly when a tool the snapshot will need (given the
// inventory + which apps are --source'd) is not installed. Returns nil when every
// needed prerequisite is present (or none is needed for this resource group).
func requireSnapshotTools(inv Inventory, sourceMap map[string]string) error {
	var missing []string

	if len(inv.Cosmos) > 0 && !onPath("mongoexport") {
		missing = append(missing, "mongoexport — exports the Cosmos account(s) "+
			joinNames(inv.Cosmos)+".\n      install: brew install mongodb-database-tools"+
			"  ·  https://www.mongodb.com/try/download/database-tools")
	}

	liveFetch := false
	for _, fa := range inv.FunctionApps {
		if fa.Class != Movable {
			continue
		}
		if _, sourced := sourceMap[fa.Name]; !sourced {
			liveFetch = true
			break
		}
	}
	if liveFetch && !onPath("unsquashfs") {
		missing = append(missing, "unsquashfs — unpacks Linux Consumption deployment packages.\n"+
			"      install: brew install squashfs  ·  apt-get install squashfs-tools\n"+
			"      or pass --source <app>=<dir> for each function app (you already have the code)")
	}

	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("missing prerequisite tool(s) for this resource group — these are required, like `az` itself:\n")
	for _, m := range missing {
		b.WriteString("  • " + m + "\n")
	}
	b.WriteString("\nInstall what's listed and re-run. (Drift never bundles them — every Azure call uses your own tools.)")
	return fmt.Errorf("%s", b.String())
}

// joinNames renders discovered resource names for an error message.
func joinNames(rs []NamedResource) string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}
