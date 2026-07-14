package project

// api.go is the thin client for the platform's slice + price + resize
// endpoints. Every helper here calls one HTTP endpoint and returns
// the parsed response. The CLI is intentionally self-contained — no
// drift-common dependency — so wire shapes are mirrored locally.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/ondrift/cli/common"
)

// LiveSlice is a CLI-local mirror of the fields we actually use from
// the platform's models.Slice. The wire endpoint returns more fields
// (createdAt, billing, provisioning) but we only need name + config.
type LiveSlice struct {
	Name             string      `json:"name"`
	Tier             string      `json:"tier"`
	Config           SliceConfig `json:"config"`
	MonthlyCostCents int         `json:"monthly_cost_cents"`
}

// FetchLiveSlice GETs /ops/slice/get?name=<name>. Returns nil if the
// slice doesn't exist (404), error for any other failure.
func FetchLiveSlice(name string) (*LiveSlice, error) {
	u := common.APIBaseURL + "/ops/slice/get?name=" + url.QueryEscape(name)
	resp, err := common.DoJSONRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, common.TransportError("fetch slice", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := common.CheckResponse(resp, "fetch slice")
	if err != nil {
		return nil, err
	}
	var s LiveSlice
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("decode slice: %w", err)
	}
	return &s, nil
}

// LineItem mirrors the platform's core/common/plan.LineItem wire shape —
// one priced (or informational, UnitCents==0) row in a price breakdown.
type LineItem struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	Quantity     int    `json:"quantity"`
	UnitCents    int    `json:"unit_cents"`     // 0 means "included"
	SubtotalCent int    `json:"subtotal_cents"` // authoritative
}

// PriceConfig POSTs /ops/slice/price with a SliceConfig and returns the
// monthly cost in cents plus the itemised breakdown the server already
// computes (core/common/plan.PriceConfig) — the same data the browser
// configurator shows, just never threaded through the CLI's HTTP client
// until now. Used by both --plan and the cost-confirm prompt; the
// platform's pricing function is the single source of truth, never the CLI.
func PriceConfig(cfg SliceConfig) (int, []LineItem, error) {
	body := mustJSON(map[string]any{
		"config":                cfg,
		"billing_period_months": 1,
	})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/price", bytes.NewReader(body))
	if err != nil {
		return 0, nil, common.TransportError("price config", err)
	}
	defer resp.Body.Close()
	respBody, err := common.CheckResponse(resp, "price config")
	if err != nil {
		return 0, nil, err
	}
	var pr struct {
		MonthlyCents int        `json:"monthly_cents"`
		Items        []LineItem `json:"items"`
	}
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return 0, nil, fmt.Errorf("decode price response: %w", err)
	}
	return pr.MonthlyCents, pr.Items, nil
}

// ResizeSlice POSTs /ops/slice/resize with the new SliceConfig.
// The platform-side endpoint already enforces "won't shrink below
// current usage", so even with a destructive flag, populated
// resources can't disappear silently.
func ResizeSlice(name string, cfg SliceConfig, billingMonths int) error {
	if billingMonths <= 0 {
		billingMonths = 1
	}
	payload := map[string]any{
		"name":                  name,
		"config":                cfg,
		"billing_period_months": billingMonths,
	}
	if os.Getenv("DRIFT_ENV") == "production" {
		payload["payment_token"] = "" // Phase G: real token. Only production enforces it.
	}
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/resize", bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return common.TransportError("resize slice", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "resize slice")
	return err
}

// CreateSlice POSTs /ops/slice/create. tier is "hacker" for free
// slices (server overrides Config with HackerConfig) or "custom"
// for everything else.
func CreateSlice(name, tier string, cfg SliceConfig, billingMonths int) error {
	payload := map[string]any{
		"name": name,
		"tier": tier,
	}
	if tier != "hacker" {
		if billingMonths <= 0 {
			billingMonths = 1
		}
		payload["config"] = cfg
		payload["billing_period_months"] = billingMonths
		if os.Getenv("DRIFT_ENV") == "production" {
			payload["payment_token"] = "" // Phase G: real token. Only production enforces it.
		}
	}
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/create", bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "create slice")
	return err
}

// readResponseBody is a small helper used by callers that want raw bytes.
func readResponseBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
