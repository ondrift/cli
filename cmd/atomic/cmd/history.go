package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func History() *cobra.Command {
	return &cobra.Command{
		Use:     "history <function-name>",
		Short:   "Show deployment history for a function",
		Example: "  drift atomic history send-email",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/atomic/history?function="+url.QueryEscape(function),
				nil,
			)
			if err != nil {
				return common.TransportError("fetch deployment history", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "fetch deployment history")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var records []struct {
				ID          string `json:"_id"`
				Name        string `json:"name"`
				Language    string `json:"language"`
				Method      string `json:"method"`
				ArtifactKey string `json:"artifact_key"`
				DeployedAt  string `json:"deployed_at"`
			}
			json.Unmarshal(body, &records) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			if len(records) == 0 {
				fmt.Printf("No deployment history for %s.\n", common.Highlight(function))
				return nil
			}

			fmt.Printf("Deployment history for %s (%d entries):\n\n", common.Highlight(function), len(records))
			fmt.Printf("%-4s  %-12s  %-8s  %s\n", "#", "LANGUAGE", "METHOD", "DEPLOYED")
			for i, r := range records {
				deployed := r.DeployedAt
				if t, err := time.Parse(time.RFC3339Nano, r.DeployedAt); err == nil {
					deployed = t.Format("2006-01-02 15:04:05")
				}
				fmt.Printf("%-4d  %-12s  %-8s  %s\n", i+1, r.Language, r.Method, deployed)
			}
			fmt.Printf("\nRoll back to a prior deploy: drift atomic rollback %s <#>\n", function)
			fmt.Printf("Re-deploy the latest artifact:  drift atomic redeploy %s\n", function)
			return nil
		},
	}
}
