package slice

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// SliceEntry is one slice in the caller's account, as returned by
// /ops/slice/list. Exported so the `drift portal` TUI can reuse the fetch.
type SliceEntry struct {
	Name string `json:"name"`
	Tier string `json:"tier"`
}

// FetchSlices returns the caller's slices. Data-only (no printing) so both the
// `slice list` command and the portal dashboard can share it.
func FetchSlices() ([]SliceEntry, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/list", nil)
	if err != nil {
		return nil, common.TransportError("list slices", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "list slices")
	if err != nil {
		return nil, err
	}

	var slices []SliceEntry
	if err := json.Unmarshal(body, &slices); err != nil {
		return nil, fmt.Errorf("Couldn't list slices: the API response didn't look right (%w)", err)
	}
	return slices, nil
}

// TierLabel renders a tier for display: hacker reads as "free", everything else
// as "configured". Shared by `slice list` and the portal.
func TierLabel(tier string) string {
	if tier == "hacker" {
		return "free"
	}
	return "configured"
}

func getListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all your slices",
		Example: "  drift slice list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			slices, err := FetchSlices()
			if err != nil {
				return err
			}
			active := common.GetActiveSlice()
			for _, s := range slices {
				marker := "  "
				if s.Name == active {
					marker = "* "
				}
				fmt.Printf("%s%-20s %s\n", marker, s.Name, TierLabel(s.Tier))
			}
			return nil
		},
	}
}
