// link.go — `drift slice link {add, list, remove}`.
//
// Slice-to-slice linking: grant the ACTIVE slice permission to call another of
// your slices (e.g. an app slice → your own observability slice). Thin wrappers
// around the API gateway's /ops/slice/link endpoints; the operator records the
// link and unions the target's host into the active slice's egress allowlist.
//
// Same-user only (the target must be a slice you own). This widens to
// "same Drift Team" when Teams land — the CLI surface won't change.
package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type linkResponse struct {
	Target     string `json:"target"`
	TargetHost string `json:"target_host"`
	CreatedAt  string `json:"created_at,omitempty"`
}

func getLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link the active slice to another of your slices so it can call it",
		Long: "Grant the active slice permission to reach another slice you own " +
			"(e.g. an app → your own observability slice). The target becomes " +
			"reachable from your functions; everything else stays subject to your " +
			"egress posture.",
		Example: "  drift slice link add c12\n" +
			"  drift slice link list\n" +
			"  drift slice link remove c12",
	}
	cmd.AddCommand(getLinkAddCmd(), getLinkListCmd(), getLinkRemoveCmd())
	return cmd
}

func getLinkAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <target-slice>",
		Short: "Allow the active slice to call <target-slice> (a slice you own)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			target := strings.ToLower(strings.TrimSpace(args[0]))
			body, _ := json.Marshal(map[string]string{"target": target})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/slice/link", strings.NewReader(string(body)))
			if err != nil {
				return common.TransportError("add link", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "add link")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var l linkResponse
			_ = json.Unmarshal(respBody, &l)
			fmt.Printf("Linked → %s\n", l.Target)
			fmt.Printf("  Call it from your functions, in-cluster:\n")
			fmt.Printf("    drift.Slice(%q).Post(\"/api/...\", body)\n", l.Target)
			fmt.Printf("  (private path — no public hop; the callee sees your slice as X-Drift-Slice)\n")
			return nil
		},
	}
}

func getLinkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the slices the active slice is linked to",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/link", nil)
			if err != nil {
				return common.TransportError("list links", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "list links")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var links []linkResponse
			_ = json.Unmarshal(respBody, &links)
			if len(links) == 0 {
				fmt.Println("This slice isn't linked to any other slice.")
				return nil
			}
			fmt.Printf("%-30s  %s\n", "TARGET", "HOST")
			for _, l := range links {
				fmt.Printf("%-30s  %s\n", l.Target, l.TargetHost)
			}
			return nil
		},
	}
}

func getLinkRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <target-slice>",
		Short: "Remove a link (the active slice can no longer call <target-slice>)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			target := strings.ToLower(strings.TrimSpace(args[0]))
			u := common.APIBaseURL + "/ops/slice/link?target=" + url.QueryEscape(target)
			resp, err := common.DoRequest(http.MethodDelete, u, nil)
			if err != nil {
				return common.TransportError("remove link", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, _ = common.CheckResponse(resp, "remove link")
				return nil
			}
			fmt.Printf("Unlinked → %s\n", target)
			return nil
		},
	}
}
