package slice

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func getUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "use <name>",
		Short:   "Set the active slice for subsequent commands",
		Example: "  drift slice use my-slice",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			name := args[0]

			// Validate the slice exists before saving.
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/list", nil)
			if err == nil {
				defer resp.Body.Close()
				var slices []struct {
					Name string `json:"name"`
				}
				if json.NewDecoder(resp.Body).Decode(&slices) == nil {
					found := false
					for _, s := range slices {
						if s.Name == name {
							found = true
							break
						}
					}
					if !found {
						fmt.Printf("Warning: slice '%s' does not exist. Setting it anyway.\n", name)
					}
				}
			}

			if err := common.SaveActiveSlice(name); err != nil {
				fmt.Println("Failed to set active slice:", err)
				return
			}
			fmt.Printf("Active slice set to '%s'.\n", name)
		},
	}
}
