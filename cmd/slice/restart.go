package slice

import (
	"fmt"
	"github.com/ondrift/cli/common"
	"net/http"

	"github.com/spf13/cobra"
)

func getRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "restart",
		Short:   "Restart your slice",
		Example: "  drift slice restart",
		RunE: func(cmd *cobra.Command, args []string) error {
			slice, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			s := common.StartSpinner("", "Restarting slice...")

			resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/restart", nil)
			if err != nil {
				s.Stop()
				return common.TransportError("restart slice", err)
			}
			defer resp.Body.Close()

			_, err = common.CheckResponse(resp, "restart slice")
			s.Stop()
			if err != nil {
				fmt.Println(err)
				return nil
			}

			fmt.Printf("Slice %s is restarting. It may take a few seconds to become available again.\n", common.Highlight(slice))
			return nil
		},
	}
}
