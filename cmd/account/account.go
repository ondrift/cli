package account

import (
	"github.com/spf13/cobra"
)

// GetAccountCmd returns the "drift account" command group.
// Subcommands: create, login, reset-password, delete.
func GetAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage your Drift account",
		Example: `  drift account create
  drift account login
  drift account reset-password
  drift account delete`,
		GroupID: "account",
	}
	cmd.AddCommand(
		GetCreateCmd(),
		GetLoginCmd(),
		GetResetPasswordCmd(),
		GetDeleteCmd(),
	)
	return cmd
}
