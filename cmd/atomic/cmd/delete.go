package atomic_cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func Delete() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a deployed atomic function by name",
		Example: "  drift atomic delete send-email\n  drift atomic delete users --method get",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			target := common.APIBaseURL + "/ops/atomic/delete?name=" + url.QueryEscape(name)
			if method != "" {
				target += "&method=" + url.QueryEscape(method)
			}
			resp, err := common.DoRequest(http.MethodDelete, target, nil)
			if err != nil {
				fmt.Println(common.TransportError("delete atomic function", err))
				return
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete atomic function"); err != nil {
				fmt.Println(err)
				return
			}

			fmt.Printf("Function %q deleted.\n", name)
		},
	}
	cmd.Flags().StringVarP(&method, "method", "m", "", "HTTP method, to disambiguate get:x from post:x at the same path")
	return cmd
}
