package slice

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

type sliceEntry struct {
	Name string `json:"name"`
	Tier string `json:"tier"`
}

func getListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all your slices",
		Example: "  drift slice list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/slice/list",
				nil,
			)
			if err != nil {
				return common.TransportError("list slices", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list slices")
			if err != nil {
				return err
			}

			var slices []sliceEntry
			if err := json.Unmarshal(body, &slices); err != nil {
				return fmt.Errorf("Couldn't list slices: the API response didn't look right (%w)", err)
			}

			active := common.GetActiveSlice()
			for _, s := range slices {
				marker := "  "
				if s.Name == active {
					marker = "* "
				}
				label := "configured"
				if s.Tier == "hacker" {
					label = "free"
				}
				fmt.Printf("%s%-20s %s\n", marker, s.Name, label)
			}
			return nil
		},
	}
}
