// Package migrate provides `drift migrate` — assisted, read-only migration
// tooling for bringing workloads onto Drift from another cloud. The first
// (and currently only) provider is Azure.
//
// The posture is hacker-first and trust-first: Drift never holds the
// customer's cloud credentials and never receives their data. Every command
// runs locally, shells out to the provider's own CLI (`az`) with the
// operator's own login, prints exactly what it does, and only ever reads.
package migrate

import (
	"github.com/spf13/cobra"

	"github.com/ondrift/cli/cmd/migrate/azure"
)

// GetCmd returns the `drift migrate` command group.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate workloads onto Drift from another cloud",
		Long: "Assisted, read-only migration tooling. Drift never holds your cloud\n" +
			"credentials or your data — every command runs locally, on your machine,\n" +
			"with your own provider login, and prints exactly what it does.",
		Example: "  drift migrate azure estimate -g my-resource-group",
		GroupID: "project",
	}
	cmd.AddCommand(azure.GetCmd())
	return cmd
}
