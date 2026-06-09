package account

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// usageResource is one row of the GET /ops/plan/usage response.
type usageResource struct {
	Used  int `json:"used"`
	Limit int `json:"limit"` // -1 means unlimited
}

// usageResponse mirrors the server-side struct.
type usageResponse struct {
	Slice            string                   `json:"slice"`
	Tier             string                   `json:"tier"`
	MonthlyCostCents int                      `json:"monthly_cost_cents"`
	Resources        map[string]usageResource `json:"resources"`
	Limits           map[string]int           `json:"limits"`
}

// planManifest is a minimal parse of a drift.yaml file — just enough to count resources.
type planManifest struct {
	Name   string `yaml:"name"`
	Canvas []struct {
		Dir string `yaml:"dir"`
	} `yaml:"canvas"`
	Atomic []struct {
		Dir string `yaml:"dir"`
	} `yaml:"atomic"`
	Backbone struct {
		Secrets []struct {
			Key string `yaml:"key"`
		} `yaml:"secrets"`
		NoSQL []struct {
			Collection string `yaml:"collection"`
		} `yaml:"nosql"`
		Queues []struct {
			Name string `yaml:"name"`
		} `yaml:"queues"`
	} `yaml:"backbone"`
}

// FetchUsage calls GET /ops/plan/usage and returns the parsed response.
func FetchUsage() (usageResponse, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/plan/usage", nil)
	if err != nil {
		return usageResponse{}, common.TransportError("fetch your plan usage", err)
	}
	defer resp.Body.Close()
	b, err := common.CheckResponse(resp, "fetch your plan usage")
	if err != nil {
		return usageResponse{}, err
	}
	var u usageResponse
	if err := json.Unmarshal(b, &u); err != nil {
		return usageResponse{}, fmt.Errorf("Couldn't fetch your plan usage: the API response didn't look right (%w)", err)
	}
	return u, nil
}

func limitStr(limit int) string {
	if limit < 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", limit)
}

func formatBytes(n int) string {
	if n < 0 {
		return "∞"
	}
	switch {
	case n >= 1024*1024*1024:
		return fmt.Sprintf("%d GB", n/(1024*1024*1024))
	case n >= 1024*1024:
		return fmt.Sprintf("%d MB", n/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%d KB", n/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// tierLabel returns a human-readable label for the slice type.
func tierLabel(tier string) string {
	if tier == "hacker" {
		return "Free"
	}
	return "Configured"
}

// formatEuros renders a price in cents as a euro string. Whole euros render
// without a decimal ("€5/mo"); non-whole values show the cents ("€4.50/mo").
func formatEuros(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("€%d", cents/100)
	}
	return fmt.Sprintf("€%d.%02d", cents/100, cents%100)
}

func planHeader(slice, tier string, monthlyCostCents int) string {
	label := tierLabel(tier)
	if monthlyCostCents == 0 {
		return fmt.Sprintf("%s  ·  %s plan  ·  free", slice, label)
	}
	return fmt.Sprintf("%s  ·  %s plan  ·  %s/mo", slice, label, formatEuros(monthlyCostCents))
}

// PrintUsage is shared with GetAccountCmd.
func PrintUsage(u usageResponse) {
	fmt.Printf("\n📊  %s\n\n", planHeader(u.Slice, u.Tier, u.MonthlyCostCents))

	type row struct {
		label string
		key   string
	}
	rows := []row{
		{"Atomic functions", "atomic_functions"},
		{"Canvas sites", "canvas_sites"},
		{"Secrets", "backbone_secrets"},
		{"NoSQL collections", "backbone_nosql_collections"},
		{"Queues", "backbone_queues"},
		{"Blobs", "backbone_blobs"},
	}

	const div = "    ──────────────────────────────────────────"
	fmt.Printf("    %-24s  %5s  %5s\n", "Resource", "Used", "Limit")
	fmt.Println(div)

	var atCapacity []string
	for _, r := range rows {
		res := u.Resources[r.key]
		lim := limitStr(res.Limit)
		var status string
		if res.Limit > 0 && res.Used >= res.Limit {
			status = "  ⚠️  at capacity"
			atCapacity = append(atCapacity, r.label)
		} else {
			status = "  ✅"
		}
		fmt.Printf("    %-24s  %5d  %5s%s\n", r.label, res.Used, lim, status)
	}

	fmt.Println(div)
	fmt.Println()
	if len(atCapacity) > 0 {
		fmt.Printf("    %s at capacity — run \"drift slice resize\" for more room.\n",
			strings.Join(atCapacity, " and "))
	}
	fmt.Printf("    Tip: run \"drift plan <drift.yaml>\" to check a project before deploying.\n\n")

	if len(u.Limits) > 0 {
		type capRow struct {
			label string
			key   string
			fmtFn func(int) string
		}
		dur := func(n int) string {
			if n < 0 {
				return "∞"
			}
			return fmt.Sprintf("%ds", n)
		}
		rpm := func(n int) string {
			if n < 0 {
				return "∞"
			}
			return fmt.Sprintf("%d/min", n)
		}
		hrs := func(n int) string {
			if n < 0 {
				return "∞"
			}
			return fmt.Sprintf("%dh", n)
		}
		caps := []capRow{
			{"Atomic max runtime", "atomic_runtime_seconds", dur},
			{"Atomic requests / min", "atomic_requests_per_minute", rpm},
			{"Atomic scheduled jobs", "atomic_scheduled_jobs", limitStr},
			{"Atomic log retention", "atomic_log_retention_hours", hrs},
			{"Canvas total size", "canvas_total_size_bytes", formatBytes},
			{"Secret max size", "backbone_secret_size_bytes", formatBytes},
			{"Blob max size", "backbone_blob_size_bytes", formatBytes},
			{"Queue max depth", "backbone_queue_depth", limitStr},
			{"NoSQL max storage", "backbone_nosql_storage_bytes", formatBytes},
		}
		const cdiv = "    ──────────────────────────────────────────"
		fmt.Printf("    %-30s  %10s\n", "Capability", "Limit")
		fmt.Println(cdiv)
		for _, c := range caps {
			if v, ok := u.Limits[c.key]; ok {
				fmt.Printf("    %-30s  %10s\n", c.label, c.fmtFn(v))
			}
		}
		fmt.Println(cdiv)
		fmt.Println()
	}
}

// GetPlanCmd returns the "drift plan drift.yaml" command, which runs a
// pre-flight check of a manifest against the user's current plan quota.
func GetPlanCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "plan <drift.yaml>",
		Short:   "Preview a project's resource requirements against your quota",
		GroupID: "services",
		Long: `Reads a drift.yaml manifest and shows a pre-flight check: how many
resources this project will consume, whether they fit within your current
plan limits, and what headroom remains after the deploy.

To see your current plan and usage, run: drift slice plan`,
		Example: `  drift plan drift.yaml
  drift plan ./my-app/drift.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			usage, err := FetchUsage()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(args[0]) // #nosec G304 — CLI reads user's manifest
			if err != nil {
				return fmt.Errorf("failed to read manifest: %w", err)
			}
			var m planManifest
			if err := yaml.Unmarshal(data, &m); err != nil {
				return fmt.Errorf("failed to parse manifest: %w", err)
			}
			printFlightPlan(m, usage, args[0])
			return nil
		},
	}
}

// printFlightPlan shows the pre-flight check table for a drift.yaml manifest.
func printFlightPlan(m planManifest, u usageResponse, manifestPath string) {
	name := m.Name
	if name == "" {
		name = "project"
	}

	fmt.Printf("\n📋  \"%s\"  —  deployment plan\n", name)
	fmt.Printf("    %s\n\n", planHeader(u.Slice, u.Tier, u.MonthlyCostCents))

	type check struct {
		label  string
		key    string
		adding int
	}
	all := []check{
		{"Atomic functions", "atomic_functions", len(m.Atomic)},
		{"Canvas sites", "canvas_sites", len(m.Canvas)},
		{"Secrets", "backbone_secrets", len(m.Backbone.Secrets)},
		{"NoSQL collections", "backbone_nosql_collections", len(m.Backbone.NoSQL)},
		{"Queues", "backbone_queues", len(m.Backbone.Queues)},
	}

	var active []check
	for _, c := range all {
		if c.adding > 0 {
			active = append(active, c)
		}
	}

	if len(active) == 0 {
		fmt.Println("    This manifest doesn't declare any quota-gated resources.")
		fmt.Println()
		return
	}

	const div = "    ─────────────────────────────────────────────────────────"
	fmt.Printf("    %-22s  %5s  %9s  %7s  %5s\n", "Resource", "Now", "+Project", "=Total", "Limit")
	fmt.Println(div)

	issues, warnings := 0, 0
	for _, c := range active {
		res := u.Resources[c.key]
		projected := res.Used + c.adding
		lim := limitStr(res.Limit)

		var status string
		switch {
		case res.Limit > 0 && projected > res.Limit:
			over := projected - res.Limit
			status = fmt.Sprintf("  ❌  over quota by %d", over)
			issues++
		case res.Limit > 0 && projected == res.Limit:
			status = "  ⚠️   at capacity after deploy"
			warnings++
		default:
			status = "  ✅"
		}

		fmt.Printf("    %-22s  %5d  %+9d  %7d  %5s%s\n",
			c.label, res.Used, c.adding, projected, lim, status)
	}

	fmt.Println(div)
	fmt.Println()

	switch {
	case issues > 0:
		noun := "resource"
		if issues > 1 {
			noun = "resources"
		}
		fmt.Printf("    ❌  %d %s would exceed your %s plan quota.\n", issues, noun, tierLabel(u.Tier))
		fmt.Printf("        Resize: drift slice resize --functions N ...\n\n")
	case warnings > 0:
		fmt.Printf("    ⚠️   Some resources will be at capacity after this deployment.\n")
		fmt.Printf("        Ready to go — run \"drift deploy %s\" when you're ready.\n\n", manifestPath)
	default:
		fmt.Printf("    ✅  Looks good — run \"drift deploy %s\" when you're ready.\n\n", manifestPath)
	}
}
