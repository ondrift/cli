package azure

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// GetCmd returns the `drift migrate azure` command group.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "azure",
		Short:   "Estimate, snapshot and transform Azure workloads for Drift (read-only)",
		Example: "  drift migrate azure estimate -g my-resource-group",
	}
	cmd.AddCommand(
		getEstimateCmd(),
		getSnapshotCmd(),
		getTransformCmd(),
		getApplyCmd(),
	)
	return cmd
}

// preflight verifies `az` is present and logged in, then prints the trust
// banner: which subscription, as whom. It is the moment the tool tells the
// operator exactly what it's about to read — and reassures them it reads only.
func preflight(c azClient) (azAccount, error) {
	if !azInstalled() {
		return azAccount{}, fmt.Errorf("the Azure CLI (`az`) is not installed or not on PATH — see https://aka.ms/azure-cli")
	}
	acct, err := activeAccount(c)
	if err != nil {
		return azAccount{}, err
	}
	fmt.Fprintf(os.Stderr, "Azure subscription: %s (%s)\n", acct.Name, acct.ID)
	if acct.User.Name != "" {
		fmt.Fprintf(os.Stderr, "Signed in as:       %s\n", acct.User.Name)
	}
	fmt.Fprintln(os.Stderr, "Drift never receives your Azure credentials or data — everything below is read-only and printed.")
	fmt.Fprintln(os.Stderr)
	return acct, nil
}
