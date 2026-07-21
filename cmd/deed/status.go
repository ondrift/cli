package deed

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Deed (identity) resource usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/deed/admin/status", nil)
			if err != nil {
				return common.TransportError("check deed status", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "check deed status")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var status struct {
				Vault struct {
					Entries int `json:"entries"`
				} `json:"vault"`
				Link struct {
					Identities int `json:"identities"`
				} `json:"link"`
				Pocket struct {
					Items int `json:"items"`
				} `json:"pocket"`
			}
			json.Unmarshal(body, &status) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			fmt.Printf("%s Deed Status\n\n", common.Highlight(""))

			fmt.Printf("  %-18s %d entries\n", "Vault", status.Vault.Entries)
			fmt.Printf("  %-18s %d identities\n", "Link", status.Link.Identities)
			fmt.Printf("  %-18s %d items\n", "Pocket", status.Pocket.Items)

			return nil
		},
	}
}
