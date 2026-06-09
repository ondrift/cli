package slice

import (
	account "github.com/ondrift/cli/cmd/account"

	"github.com/spf13/cobra"
)

func getPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "plan",
		Short:   "Show the active slice's plan and resource usage",
		Example: "  drift slice plan",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, err := account.FetchUsage()
			if err != nil {
				return err
			}
			account.PrintUsage(u)
			return nil
		},
	}
}
