// alerts.go — Driftfile reconcile of per-function alerts. The
// alert-spec name on the slice is `<function>-<index>` so multiple
// alerts on the same function get stable names; reconcile is
// idempotent (existing names are replaced wholesale; absent names
// are removed). v1: errors trigger only, webhook notify only —
// matches the slice's per-user-alerting primitive.
package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ondrift/cli/v2/common"
)

type liveAlert struct {
	Name string `json:"name"`
}

type alertSpec struct {
	Name         string `json:"name"`
	Function     string `json:"function"`
	Trigger      string `json:"trigger"`
	Threshold    int    `json:"threshold"`
	WindowSec    int    `json:"window_seconds"`
	NotifyType   string `json:"notify_type"`
	NotifyTarget string `json:"notify_target"`
}

// applyAlerts reconciles `slice.atomic.functions[].alerts[]` against
// the live alert registry. Best-effort: per-alert failures are
// surfaced but don't abort the deploy.
func applyAlerts(m *Manifest) error {
	declared := map[string]alertSpec{}
	for _, fn := range m.Slice.Atomic.Functions {
		for i, a := range fn.Alerts {
			spec, err := alertEntryToSpec(fn.Name, i, a)
			if err != nil {
				fmt.Printf("  %s alert on %s skipped: %v\n", common.Hint("·"), fn.Name, err)
				continue
			}
			declared[spec.Name] = spec
		}
	}

	live, err := fetchLiveAlerts()
	if err != nil {
		fmt.Printf("  %s alert reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}
	liveByName := map[string]liveAlert{}
	for _, a := range live {
		liveByName[a.Name] = a
	}

	added, removed := 0, 0

	// POST replaces an existing spec by name → safe to send
	// every declared alert unconditionally. Lets us update
	// thresholds / notify targets without an explicit "edit" verb.
	for _, spec := range declared {
		if err := putAlert(spec); err != nil {
			fmt.Printf("  %s alert %s: %v\n", common.Hint("·"), spec.Name, err)
			continue
		}
		if _, exists := liveByName[spec.Name]; !exists {
			added++
			fmt.Printf("  %s alert added: %s (function %s, %s threshold %d / %ds)\n",
				common.Check(), spec.Name, spec.Function, spec.Trigger,
				spec.Threshold, spec.WindowSec)
		}
	}

	for name := range liveByName {
		if _, ok := declared[name]; ok {
			continue
		}
		if err := deleteAlert(name); err != nil {
			fmt.Printf("  %s remove alert %s: %v\n", common.Hint("·"), name, err)
			continue
		}
		removed++
		fmt.Printf("  %s alert removed: %s\n", common.Check(), name)
	}

	if added == 0 && removed == 0 && len(declared) > 0 {
		fmt.Printf("  %s alerts in sync (%d declared)\n", common.Hint("·"), len(declared))
	}
	return nil
}

func alertEntryToSpec(function string, idx int, a AlertEntry) (alertSpec, error) {
	if a.On == "" {
		a.On = "errors"
	}
	if a.On != "errors" {
		return alertSpec{}, fmt.Errorf("trigger %q reserved but not yet implemented (v1: errors)", a.On)
	}
	windowSec, err := parseDriftfileWindow(a.Window)
	if err != nil {
		return alertSpec{}, err
	}
	ntype, ntarget, err := parseDriftfileNotify(a.Notify)
	if err != nil {
		return alertSpec{}, err
	}
	threshold := a.Threshold
	if threshold < 1 {
		threshold = 1
	}
	return alertSpec{
		Name:         fmt.Sprintf("%s-%d", function, idx),
		Function:     function,
		Trigger:      a.On,
		Threshold:    threshold,
		WindowSec:    windowSec,
		NotifyType:   ntype,
		NotifyTarget: ntarget,
	}, nil
}

func fetchLiveAlerts() ([]liveAlert, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/alert", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list alerts")
	if err != nil {
		return nil, err
	}
	var out []liveAlert
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func putAlert(spec alertSpec) error {
	body, _ := json.Marshal(spec)
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/atomic/alert", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "add alert")
		return e
	}
	return nil
}

func deleteAlert(name string) error {
	resp, err := common.DoRequest(http.MethodDelete,
		common.APIBaseURL+"/ops/atomic/alert?name="+url.QueryEscape(name), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "remove alert")
		return e
	}
	return nil
}

func parseDriftfileWindow(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("window required (e.g. 5m)")
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "s"):
		mult = 1
	case strings.HasSuffix(s, "m"):
		mult = 60
	case strings.HasSuffix(s, "h"):
		mult = 3600
	default:
		return 0, fmt.Errorf("invalid window %q (use Ns / Nm / Nh)", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid window %q", s)
	}
	if total := n * mult; total >= 60 {
		return total, nil
	}
	return 0, fmt.Errorf("window must be >= 60 seconds")
}

func parseDriftfileNotify(s string) (string, string, error) {
	if s == "" {
		return "", "", fmt.Errorf("notify required (e.g. webhook=https://...)")
	}
	idx := strings.Index(s, "=")
	if idx < 1 {
		return "", "", fmt.Errorf("invalid notify %q", s)
	}
	t, v := s[:idx], s[idx+1:]
	switch t {
	case "webhook":
		if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			return "", "", fmt.Errorf("webhook target must be an http(s) URL")
		}
		return t, v, nil
	default:
		return "", "", fmt.Errorf("notify type %q not supported (v1: webhook)", t)
	}
}
