package project

import "github.com/spf13/cobra"

// GetCmd returns the `drift project` command group: deploy the project
// described by ./Driftfile to a slice, or preview the diff first.
//
// Vocabulary the platform uses:
//   - Primitives — Atomic, Backbone, Canvas (the *what*).
//   - Slice     — the rented infrastructure that holds primitives
//     (the *where*).
//   - Project   — a Driftfile bundling primitives + slice (the
//     *what + where*, deployed as one unit).
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Short:   "Deploy a Drift project (Driftfile-driven)",
		Example: "  drift project deploy\n  drift project diff",
		GroupID: "project",
	}
	cmd.AddCommand(
		getDeployCmd(),
		getDiffCmd(),
		getRunCmd(),
		getStopCmd(),
		getLogsCmd(),
	)
	return cmd
}
