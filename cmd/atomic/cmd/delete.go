package atomic_cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func Delete() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a deployed atomic function by name",
		Example: "  drift atomic delete send-email",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			resp, err := common.DoRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/atomic/delete?name="+url.QueryEscape(name),
				nil,
			)
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
}
