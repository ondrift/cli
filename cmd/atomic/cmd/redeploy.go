package atomic_cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func Redeploy() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:     "redeploy <function-name>",
		Short:   "Re-deploy the last known artifact for a function",
		Example: "  drift atomic redeploy send-email\n  drift atomic redeploy users --method get",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			payload, _ := json.Marshal(map[string]string{"name": function, "method": method})

			s := common.StartSpinner("", "Redeploying "+function+"...")

			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/atomic/redeploy",
				bytes.NewReader(payload),
			)
			if err != nil {
				s.Stop()
				return common.TransportError("redeploy function", err)
			}
			defer resp.Body.Close()

			_, err = common.CheckResponse(resp, "redeploy function")
			s.Stop()
			if err != nil {
				fmt.Println(err)
				return nil
			}

			fmt.Printf("Function %s redeployed from last known artifact.\n", common.Highlight(function))
			return nil
		},
	}
	cmd.Flags().StringVarP(&method, "method", "m", "", "HTTP method, to disambiguate get:x from post:x at the same path")
	return cmd
}
