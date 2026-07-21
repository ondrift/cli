package cmd

import (
	slate_cmd "github.com/ondrift/cli/v2/cmd/canvas/cmd"

	"github.com/spf13/cobra"
)

func GetCmd() *cobra.Command {
	slateCmd := &cobra.Command{
		Use:     "canvas",
		Short:   "Manage Canvas hosting",
		Example: "  drift canvas deploy ./my-site\n  drift canvas deploy ./dist",
		GroupID: "services",
	}

	slateCmd.AddCommand(slate_cmd.Deploy())
	slateCmd.GroupID = "services"

	return slateCmd
}
