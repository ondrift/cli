package deed

import "github.com/spf13/cobra"

// GetCmd returns the "deed" command group — Drift's fourth pillar, identity,
// a peer of Backbone/Atomic/Canvas rather than a Backbone primitive. Deed
// runs on its own listener/port in the slice; today the CLI only exposes
// read-only status (KeyAuth/JWT/Vault/Link/Pocket are used through the SDK,
// not the CLI).
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "deed",
		Short:   "Interact with your slice's Deed (identity) pillar",
		Example: "  drift deed status",
		GroupID: "services",
	}

	cmd.AddCommand(statusCmd())
	return cmd
}
