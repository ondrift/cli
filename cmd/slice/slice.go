package slice

import "github.com/spf13/cobra"

// GetCmd returns the "drift slice" command group for managing slices.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "slice",
		Short:   "Manage your Drift slices (projects)",
		Example: "  drift slice list\n  drift slice create my-slice\n  drift slice use my-slice\n  drift slice resize my-slice\n  drift slice plan\n  drift slice delete my-slice",
		GroupID: "account",
	}
	cmd.AddCommand(
		getCreateCmd(),
		getResizeCmd(),
		getListCmd(),
		getUseCmd(),
		getDeleteCmd(),
		getPlanCmd(),
		getRestartCmd(),
		getSnapshotCmd(),
		getDomainCmd(),
		getAuthCmd(),
	)
	return cmd
}
