package azure

import (
	"sort"
	"strconv"
	"strings"
)

// azUsage is the trimmed shape of one `az consumption usage list` record.
type azUsage struct {
	PretaxCost   string `json:"pretaxCost"`
	Currency     string `json:"currency"`
	InstanceName string `json:"instanceName"`
	MeterDetails struct {
		MeterCategory string `json:"meterCategory"`
		MeterName     string `json:"meterName"`
	} `json:"meterDetails"`
}

// azureCostLine is one aggregated row of the Azure bill, after mapping.
type azureCostLine struct {
	Category string `json:"category"` // a Drift-facing bucket, or "Other Azure services"
	Cents    int    `json:"cents"`
	Mapped   bool   `json:"mapped"` // true → a Drift primitive covers it → counts toward the saving
}

// azureCost is the mapped Azure bill for the scope.
type azureCost struct {
	Lines        []azureCostLine `json:"lines"`
	TotalCents   int             `json:"total_cents"`   // everything on the bill
	MovableCents int             `json:"movable_cents"` // only mapped categories — the honest saving base
	OtherCents   int             `json:"other_cents"`   // unmapped — STAYS on Azure, never a saving
	Currency     string          `json:"currency"`
	Source       string          `json:"source"`
}

// mapMeterCategory maps an Azure meter category to a Drift-facing bucket.
// Anything we don't explicitly recognize returns ("Other Azure services",
// false) — it is reported, but NEVER folded into the saving. Lying here is
// how a migration estimate loses trust; the catch-all is the integrity valve.
func mapMeterCategory(meterCategory string) (bucket string, mapped bool) {
	switch strings.ToLower(strings.TrimSpace(meterCategory)) {
	case "azure functions", "functions":
		return "Compute (Functions → Atomic)", true
	case "azure app service", "app service":
		return "Compute (App Service → Atomic)", true
	case "azure cosmos db", "cosmos db":
		return "Data (Cosmos → Backbone NoSQL)", true
	case "storage":
		return "Storage (→ Backbone blobs/queues)", true
	case "bandwidth":
		return "Egress (→ included)", true
	default:
		return "Other Azure services", false
	}
}

// parseCents converts a pretaxCost string (e.g. "12.34") to integer cents.
func parseCents(s string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int(f*100 + 0.5)
}

// computeAzureCost pulls `az consumption usage list`, aggregates by mapped
// category, and splits movable (Drift-coverable) from "Other" (stays on Azure).
func computeAzureCost(c azClient) (azureCost, error) {
	var usage []azUsage
	if err := c.runJSON([]string{"consumption", "usage", "list"}, &usage); err != nil {
		// Reading the bill needs a Cost Management reader role that many dev
		// logins lack. Degrade to a Drift-only projection rather than failing the
		// whole estimate — the projected Drift cost is still useful on its own.
		return azureCost{Currency: "EUR", Source: "could not read your Azure bill (no Cost Management access)"}, nil
	}

	byBucket := map[string]int{}
	mappedOf := map[string]bool{}
	currency := ""
	for _, u := range usage {
		if currency == "" {
			currency = u.Currency
		}
		bucket, mapped := mapMeterCategory(u.MeterDetails.MeterCategory)
		cents := parseCents(u.PretaxCost)
		byBucket[bucket] += cents
		mappedOf[bucket] = mapped
	}

	out := azureCost{Currency: currency, Source: "az consumption usage list (actuals)"}
	for bucket, cents := range byBucket {
		out.Lines = append(out.Lines, azureCostLine{Category: bucket, Cents: cents, Mapped: mappedOf[bucket]})
		out.TotalCents += cents
		if mappedOf[bucket] {
			out.MovableCents += cents
		} else {
			out.OtherCents += cents
		}
	}
	// Stable order: mapped first (by spend desc), then Other last.
	sort.SliceStable(out.Lines, func(i, j int) bool {
		if out.Lines[i].Mapped != out.Lines[j].Mapped {
			return out.Lines[i].Mapped
		}
		return out.Lines[i].Cents > out.Lines[j].Cents
	})
	if currency == "" {
		out.Currency = "EUR"
	}
	// Usage records can exist yet sum to zero (a sandbox / no-spend scope). Say so
	// plainly instead of labelling an empty bill "actuals".
	if out.TotalCents == 0 {
		out.Lines = nil
		out.Source = "no billing data for this scope (sandbox / no spend, or no Cost Management access)"
	}
	return out, nil
}
