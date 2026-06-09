package atomic_cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func Rollback() *cobra.Command {
	return &cobra.Command{
		Use:     "rollback <function-name> <position>",
		Short:   "Roll back a function to a previous deployment (see drift atomic history)",
		Example: "  drift atomic rollback send-email 2",
		GroupID: "operations",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]
			position, err := strconv.Atoi(args[1])
			if err != nil || position < 1 {
				return fmt.Errorf("position must be a positive integer (1 = most recent prior deploy)")
			}

			payload, _ := json.Marshal(map[string]any{
				"name":     function,
				"position": position,
			})

			s := common.StartSpinner("", fmt.Sprintf("Rolling back %s to position %d...", function, position))

			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/atomic/rollback",
				bytes.NewReader(payload),
			)
			if err != nil {
				s.Stop()
				return common.TransportError("roll back function", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "roll back function")
			s.Stop()
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var result struct {
				Name       string `json:"name"`
				Position   int    `json:"position"`
				DeployedAt string `json:"deployed_at"`
				Language   string `json:"language"`
				Method     string `json:"method"`
			}
			_ = json.Unmarshal(body, &result)

			fmt.Printf("Function %s rolled back to %s deploy from %s.\n",
				common.Highlight(function),
				result.Language,
				result.DeployedAt,
			)
			return nil
		},
	}
}
