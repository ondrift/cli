package azure

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ondrift/cli/v2/common"
)

// EstimateResult is the full output of `estimate`, also the --json shape.
type EstimateResult struct {
	Subscription   string         `json:"subscription"`
	ResourceGroup  string         `json:"resource_group"`
	Currency       string         `json:"currency"`
	Azure          azureCost      `json:"azure"`
	DriftResources driftResources `json:"drift_resources"`
	Drift          driftBreakdown `json:"drift"`
	Movable        []FunctionApp  `json:"movable_function_apps"`
	Refused        []FunctionApp  `json:"refused_or_unverified"`
	CosmosCount    int            `json:"cosmos_accounts"`
	StorageCount   int            `json:"storage_accounts"`
	Assumptions    []string       `json:"assumptions"`
	SavingCents    int            `json:"monthly_saving_cents"`
	FXNote         string         `json:"fx_note,omitempty"`
}

func getEstimateCmd() *cobra.Command {
	var rg string
	var asJSON, asCSV, dryRun, azLog bool

	cmd := &cobra.Command{
		Use:   "estimate",
		Short: "Estimate the monthly cost of moving an Azure resource group to Drift",
		Long: "Reads an Azure resource group and its bill (read-only), maps each workload to\n" +
			"a Drift primitive, refuses what doesn't fit (on the record), and prints what the\n" +
			"same workloads would cost on Drift. Nothing is mutated; every az command is read-only.",
		Example: "  drift migrate azure estimate -g my-resource-group\n" +
			"  drift migrate azure estimate -g my-rg --json",
		RunE: func(_ *cobra.Command, _ []string) error {
			if strings.TrimSpace(rg) == "" {
				return fmt.Errorf("a resource group is required: -g <name>")
			}
			var c azClient = azRunner{dryRun: dryRun, logAll: azLog || dryRun}

			sub := ""
			if dryRun {
				fmt.Fprintln(os.Stderr, "(dry run — printing the az commands that would run; no data fetched)")
				fmt.Fprintln(os.Stderr)
			} else {
				acct, err := preflight(c)
				if err != nil {
					return err
				}
				sub = acct.Name
			}

			res, err := buildEstimate(c, sub, rg)
			if err != nil {
				return err
			}
			switch {
			case asJSON:
				return renderJSON(res)
			case asCSV:
				renderCSV(res)
			default:
				renderTable(res)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&rg, "resource-group", "g", "", "Azure resource group to estimate (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the estimate as JSON")
	cmd.Flags().BoolVar(&asCSV, "csv", false, "Emit the estimate as CSV")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the az commands that would run; fetch nothing")
	cmd.Flags().BoolVar(&azLog, "az-log", false, "Print every az command before it runs")
	return cmd
}

// buildEstimate runs the read-only pipeline: discover → Azure cost → synth a
// Drift slice → price it → diff. Pure given an azClient (real or fixture).
func buildEstimate(c azClient, sub, rg string) (EstimateResult, error) {
	inv, err := discover(c, sub, rg)
	if err != nil {
		return EstimateResult{}, err
	}
	az, err := computeAzureCost(c)
	if err != nil {
		return EstimateResult{}, err
	}
	dr, assumptions := synthDrift(inv)
	bd := priceDrift(dr)

	saving := az.MovableCents - bd.MonthlyCents
	if saving < 0 {
		saving = 0
	}

	res := EstimateResult{
		Subscription: sub, ResourceGroup: rg, Currency: az.Currency,
		Azure: az, DriftResources: dr, Drift: bd,
		CosmosCount: len(inv.Cosmos), StorageCount: len(inv.Storage),
		Assumptions: assumptions, SavingCents: saving,
	}
	for _, fa := range inv.FunctionApps {
		if fa.Class == Movable {
			res.Movable = append(res.Movable, fa)
		} else {
			res.Refused = append(res.Refused, fa)
		}
	}
	if az.Currency != "" && !strings.EqualFold(az.Currency, "EUR") {
		res.FXNote = fmt.Sprintf("Azure costs are in %s; Drift is priced in EUR. The saving is not FX-converted.", az.Currency)
	}
	return res, nil
}

// synthDrift turns the inventory into a priced Drift slice, returning the
// stated assumptions. The Drift bill is small (slices are cheap), so the
// estimate's credibility rests on the Azure side, not on synth precision —
// but every assumption is printed so nothing is implied silently.
func synthDrift(inv Inventory) (driftResources, []string) {
	movable := 0
	for _, fa := range inv.FunctionApps {
		if fa.Class == Movable {
			movable++
		}
	}
	dr := driftResources{
		Functions: movable,
	}
	assume := []string{
		"1 Drift function per movable Function App — a Function App hosts many functions; `transform` produces the exact per-function count",
		"Cosmos containers map to NoSQL collections, which are FREE by count — you pay for the storage GiB they hold (sized at `transform`)",
		"function memory (RAM), storage volume, queues, scheduled jobs and realtime are sized at `snapshot`/`transform`, not here",
		"Azure SQL is not migrated by the current tool — refused, never priced as a saving",
	}
	return dr, assume
}

// --- rendering -------------------------------------------------------------

func money(cents int, cur string) string {
	sym := strings.ToUpper(cur) + " "
	switch strings.ToUpper(cur) {
	case "EUR", "":
		sym = "€"
	case "USD":
		sym = "$"
	case "GBP":
		sym = "£"
	}
	return fmt.Sprintf("%s%.2f", sym, float64(cents)/100)
}

func renderTable(r EstimateResult) {
	cur := r.Currency
	fmt.Printf("\n%s  —  resource group %s\n\n", common.BoldText("Azure → Drift estimate"), common.Highlight(r.ResourceGroup))

	// Azure bill, mapped first then the honest "Other" catch-all. With no billing
	// data we say so plainly rather than presenting a misleading €0.00 bill.
	hasBill := r.Azure.TotalCents > 0
	fmt.Println(common.BoldText("Your Azure bill"))
	if hasBill {
		for _, l := range r.Azure.Lines {
			tag := common.Check() + " maps to Drift"
			if !l.Mapped {
				tag = common.Hint("stays on Azure — not a saving")
			}
			fmt.Printf("  %-38s %12s   %s\n", l.Category, money(l.Cents, cur), tag)
		}
		fmt.Printf("  %-38s %12s\n", common.BoldText("Total (this scope)"), money(r.Azure.TotalCents, cur))
		fmt.Printf("  %-38s %12s\n", "Movable to Drift", money(r.Azure.MovableCents, cur))
	} else {
		fmt.Printf("  %s\n", common.Hint(r.Azure.Source))
		fmt.Printf("  %s\n", common.Hint("Run in a billed subscription, or pass --csv <cost-export>, to compare against your actual spend."))
	}
	fmt.Println()

	// What moves / what doesn't.
	fmt.Printf("%s  %d movable Function App(s) · %d Cosmos account(s) · %d storage account(s)\n",
		common.BoldText("What moves:"), len(r.Movable), r.CosmosCount, r.StorageCount)
	if len(r.Refused) > 0 {
		fmt.Println(common.BoldText("What stays on Azure:"))
		for _, fa := range r.Refused {
			fmt.Printf("  %-28s %-10s %s\n", fa.Name, "("+fa.Runtime+")", common.Hint(fa.Reason))
		}
	}
	fmt.Println()

	// Drift projection.
	fmt.Println(common.BoldText("On Drift (projected)"))
	for _, l := range r.Drift.Lines {
		if l.SubtotalCents == 0 && l.Quantity == 0 {
			continue
		}
		fmt.Printf("  %-38s %5d × %-7s = %8s\n", l.Label, l.Quantity, money(l.UnitCents, "EUR"), money(l.SubtotalCents, "EUR"))
	}
	fmt.Printf("  %-38s %22s / month\n", common.BoldText("Drift slice total"), money(r.Drift.MonthlyCents, "EUR"))
	fmt.Println()

	// Headline — a saving only makes sense against a real bill; otherwise lead
	// with the projected Drift cost (which is exact) and ask for the bill.
	if hasBill {
		fmt.Printf("%s  %s/mo on Azure (movable)  →  %s/mo on Drift\n",
			common.BoldText("Estimated saving:"), money(r.Azure.MovableCents, cur), money(r.Drift.MonthlyCents, "EUR"))
		fmt.Printf("  %s  %s / month   ·   ~%s / year\n",
			common.CanvasHeader(), common.BoldText(money(r.SavingCents, cur)), money(r.SavingCents*12, cur))
		if r.FXNote != "" {
			fmt.Printf("  %s\n", common.Hint(r.FXNote))
		}
	} else {
		fmt.Printf("%s  this workload runs for %s/mo on Drift. Provide your bill (above) to see the saving.\n",
			common.BoldText("Projected Drift cost:"), common.BoldText(money(r.Drift.MonthlyCents, "EUR")))
	}
	fmt.Println()

	// Integrity footer.
	fmt.Println(common.Hint("Assumptions:"))
	for _, a := range r.Assumptions {
		fmt.Printf("  %s %s\n", common.Hint("·"), common.Hint(a))
	}
	fmt.Printf("  %s\n", common.Hint("Source: "+r.Azure.Source+". Azure costs lag 24–72h and exclude unmapped meters from the saving."))
	fmt.Println()
}

func renderJSON(r EstimateResult) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func renderCSV(r EstimateResult) {
	fmt.Println("section,label,quantity,amount_cents,currency,mapped")
	for _, l := range r.Azure.Lines {
		fmt.Printf("azure,%q,,%d,%s,%t\n", l.Category, l.Cents, r.Currency, l.Mapped)
	}
	for _, l := range r.Drift.Lines {
		fmt.Printf("drift,%q,%d,%d,EUR,\n", l.Label, l.Quantity, l.SubtotalCents)
	}
	fmt.Printf("saving,%q,,%d,%s,\n", "monthly_saving", r.SavingCents, r.Currency)
}
