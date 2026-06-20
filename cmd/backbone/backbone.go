package backbone

import "github.com/spf13/cobra"

// GetCmd returns the "backbone" command group, which exposes the Drift
// backbone primitives (secrets, queues, blobs) directly from the CLI.
func GetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backbone",
		Short:   "Interact with your slice's Backbone primitives",
		Example: "  drift backbone secret list\n  drift backbone queue push jobs '{\"task\":\"build\"}'\n  drift backbone blob put assets logo.png ./logo.png\n  drift backbone cache set session-token abc123 --ttl 3600",
		GroupID: "services",
	}

	cmd.AddCommand(secretCmd(), blobCmd(), queueCmd(), lockCmd(), nosqlCmd(), sqlCmd(), cacheCmd(), statusCmd())
	return cmd
}
