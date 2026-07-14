package account

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// GetAccountCmd returns the "drift account" command group.
// Subcommands: create, login, reset-password, usage.
func GetAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage your Drift account",
		Example: `  drift account create
  drift account login
  drift account reset-password
  drift account usage`,
		GroupID: "account",
	}
	cmd.AddCommand(
		GetCreateCmd(),
		GetLoginCmd(),
		GetResetPasswordCmd(),
		GetUsageCmd(),
	)
	return cmd
}

// usageResource mirrors core/api/routes/plan.go's usageResource wire shape.
type usageResource struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

// usageResponse mirrors core/api/routes/plan.go's usageResponse — the
// GET /ops/plan/usage envelope. That endpoint has existed since before this
// command did; this is the first CLI consumer of it.
type usageResponse struct {
	Slice            string                   `json:"slice"`
	Tier             string                   `json:"tier"`
	MonthlyCostCents int                      `json:"monthly_cost_cents"`
	Resources        map[string]usageResource `json:"resources"`
	Limits           map[string]int           `json:"limits"`
}

// resourceLabels gives a fixed, human-readable print order for the
// server's resources map — Go map iteration order is random, and the
// server's key set is a small, known vocabulary (see plan.go's
// HandlePlanUsage), so a hardcoded order beats sorting alphabetically.
var resourceLabels = []struct{ key, label string }{
	{"atomic_functions", "Atomic functions"},
	{"backbone_secrets", "Secrets"},
	{"backbone_blobs", "Blobs"},
	{"backbone_nosql_collections", "NoSQL collections"},
	{"backbone_queues", "Queues"},
}

func GetUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show resource usage and limits for your active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/plan/usage", nil)
			if err != nil {
				return common.TransportError("check usage", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "check usage")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var u usageResponse
			if err := json.Unmarshal(body, &u); err != nil {
				return fmt.Errorf("Couldn't check usage: bad response (%w)", err)
			}

			cost := "free"
			if u.MonthlyCostCents > 0 {
				cost = fmt.Sprintf("€%s/month", formatEuros(u.MonthlyCostCents))
			}
			fmt.Printf("Slice %q — %s tier, %s\n\n", u.Slice, u.Tier, cost)

			for _, r := range resourceLabels {
				res, ok := u.Resources[r.key]
				if !ok {
					continue
				}
				fmt.Printf("  %-20s %d / %d\n", r.label, res.Used, res.Limit)
			}

			if len(u.Limits) > 0 {
				fmt.Println()
				keys := make([]string, 0, len(u.Limits))
				for k := range u.Limits {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("  %-30s %d\n", k, u.Limits[k])
				}
			}

			return nil
		},
	}
}

func formatEuros(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("%d", cents/100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
