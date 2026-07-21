// alert.go — `drift atomic alert {add, list, remove}`. Thin wrapper
// around the API gateway's /ops/atomic/alert endpoints.
package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type alertSpec struct {
	Name         string `json:"name"`
	Function     string `json:"function"`
	Trigger      string `json:"trigger"`
	Threshold    int    `json:"threshold"`
	WindowSec    int    `json:"window_seconds"`
	NotifyType   string `json:"notify_type"`
	NotifyTarget string `json:"notify_target"`
}

func Alert() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "alert",
		Short:   "Add, list, or remove function alerts",
		GroupID: "operations",
		Example: "  drift atomic alert add checkout-errors checkout --on errors --threshold 1 --window 5m --notify webhook=https://hooks.slack.com/...\n" +
			"  drift atomic alert list\n" +
			"  drift atomic alert remove checkout-errors",
	}
	cmd.AddCommand(getAlertAddCmd(), getAlertListCmd(), getAlertRemoveCmd())
	return cmd
}

func getAlertAddCmd() *cobra.Command {
	var (
		on        string
		threshold int
		window    string
		notify    string
	)
	c := &cobra.Command{
		Use:   "add <name> <function>",
		Short: "Register an alert (v1: errors trigger + webhook notify)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			windowSec, err := parseWindow(window)
			if err != nil {
				return err
			}
			ntype, ntarget, err := parseNotify(notify)
			if err != nil {
				return err
			}
			spec := alertSpec{
				Name:         args[0],
				Function:     args[1],
				Trigger:      on,
				Threshold:    threshold,
				WindowSec:    windowSec,
				NotifyType:   ntype,
				NotifyTarget: ntarget,
			}
			body, _ := json.Marshal(spec)
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/atomic/alert", strings.NewReader(string(body)))
			if err != nil {
				return common.TransportError("add alert", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, e := common.CheckResponse(resp, "add alert")
				return e
			}
			fmt.Printf("Alert %q added on function %q (%s, threshold %d / %s).\n",
				spec.Name, spec.Function, spec.Trigger, spec.Threshold, window)
			return nil
		},
	}
	c.Flags().StringVar(&on, "on", "errors", "Trigger condition (v1: errors)")
	c.Flags().IntVar(&threshold, "threshold", 1, "Number of events that fire the alert")
	c.Flags().StringVar(&window, "window", "5m", "Evaluation window (e.g. 60s, 5m, 1h; minimum 60s)")
	c.Flags().StringVar(&notify, "notify", "", "Notification channel (v1: webhook=https://...)")
	_ = c.MarkFlagRequired("notify")
	return c
}

func getAlertListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List alerts registered on the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/alert", nil)
			if err != nil {
				return common.TransportError("list alerts", err)
			}
			defer resp.Body.Close()
			body, err := common.CheckResponse(resp, "list alerts")
			if err != nil {
				return err
			}
			var specs []alertSpec
			_ = json.Unmarshal(body, &specs)
			if len(specs) == 0 {
				fmt.Println("No alerts registered.")
				return nil
			}
			fmt.Printf("%-24s  %-24s  %-8s  %-9s  %-7s  %s\n",
				"NAME", "FUNCTION", "TRIGGER", "THRESHOLD", "WINDOW", "NOTIFY")
			for _, s := range specs {
				fmt.Printf("%-24s  %-24s  %-8s  %-9d  %-7s  %s=%s\n",
					s.Name, s.Function, s.Trigger, s.Threshold,
					formatWindow(s.WindowSec), s.NotifyType, s.NotifyTarget)
			}
			return nil
		},
	}
}

func getAlertRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an alert by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u := common.APIBaseURL + "/ops/atomic/alert?name=" + url.QueryEscape(args[0])
			resp, err := common.DoRequest(http.MethodDelete, u, nil)
			if err != nil {
				return common.TransportError("remove alert", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, e := common.CheckResponse(resp, "remove alert")
				return e
			}
			fmt.Printf("Alert %q removed.\n", args[0])
			return nil
		},
	}
}

// parseWindow accepts duration strings ending in `s`, `m`, or `h`
// and returns total seconds. The slice rejects windows < 60s; the
// CLI mirrors that here with a friendlier message.
func parseWindow(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("--window is required (e.g. 5m)")
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
		return 0, fmt.Errorf("invalid window %q (use Ns, Nm, or Nh)", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid window %q (use Ns, Nm, or Nh)", s)
	}
	if total := n * mult; total >= 60 {
		return total, nil
	}
	return 0, fmt.Errorf("window must be >= 60 seconds (got %s)", s)
}

func formatWindow(sec int) string {
	switch {
	case sec%3600 == 0:
		return fmt.Sprintf("%dh", sec/3600)
	case sec%60 == 0:
		return fmt.Sprintf("%dm", sec/60)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

// parseNotify accepts `webhook=https://...`. Returns (type, target).
// `email=...` is reserved but rejected here (matches the slice's
// validation, surfacing the refusal client-side without a round-trip).
func parseNotify(s string) (string, string, error) {
	if s == "" {
		return "", "", fmt.Errorf("--notify is required (e.g. webhook=https://hooks.slack.com/...)")
	}
	idx := strings.Index(s, "=")
	if idx < 1 {
		return "", "", fmt.Errorf("invalid --notify %q (use type=value, e.g. webhook=https://...)", s)
	}
	t, v := s[:idx], s[idx+1:]
	switch t {
	case "webhook":
		if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
			return "", "", fmt.Errorf("webhook target must be an http(s) URL")
		}
	case "email":
		return "", "", fmt.Errorf("notify type %q is reserved but not yet implemented (v1: webhook only)", t)
	default:
		return "", "", fmt.Errorf("notify type %q not supported (v1: webhook only)", t)
	}
	return t, v, nil
}
