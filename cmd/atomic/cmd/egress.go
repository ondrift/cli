// egress.go — `drift atomic egress {list, refresh, test}`. Thin
// wrapper around the API gateway's /ops/atomic/egress endpoints.
//
//	list    — show the active mode + declared hostnames + the IPs
//	          the operator most-recently resolved them to.
//	refresh — force a fresh DNS resolution + chart re-render. Hit
//	          this when an allowlisted CDN's IPs change.
//	test    — local-only pattern match: does <host> match any
//	          entry on the active allowlist? Doesn't hit the
//	          cluster — purely a "did I spell it right" tool.
//
// There is intentionally no `add`/`remove`. The Driftfile is the
// single source of truth — users edit `slice.atomic.egress.hosts`
// and run `drift project deploy`. The deploy reconcile path
// invokes `refresh` automatically when the egress section
// changes (see cli/cmd/project/deploy.go).
package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

// egressView is the JSON shape both `list` and `refresh` return.
// `resolved_ips` is populated by `refresh`; `list` may have it empty
// (the operator can't show resolved IPs without doing the DNS work).
type egressView struct {
	Mode          string   `json:"mode"`
	DeclaredHosts []string `json:"declared_hosts"`
	ResolvedIPs   []string `json:"resolved_ips,omitempty"`
	ResolvedAt    string   `json:"resolved_at,omitempty"`
}

func Egress() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "egress",
		Short:   "Manage the slice's outbound egress allowlist",
		GroupID: "operations",
		Long: `Show or refresh the slice's outbound egress allowlist.

The Driftfile's slice.atomic.egress block is the source of truth:

  slice:
    atomic:
      egress:
        mode: allowlist          # open | allowlist
        hosts:
          - api.stripe.com
          - hooks.slack.com
          - smtp.sendgrid.net:587

To change the list, edit your Driftfile and run 'drift project deploy'.
This command exists for inspection, ad-hoc refresh, and local pattern
testing.`,
		Example: "  drift atomic egress list\n" +
			"  drift atomic egress refresh\n" +
			"  drift atomic egress test api.stripe.com",
	}
	cmd.AddCommand(getEgressListCmd(), getEgressRefreshCmd(), getEgressTestCmd())
	return cmd
}

func getEgressListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the active egress allowlist for the current slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/egress", nil)
			if err != nil {
				return common.TransportError("list egress", err)
			}
			defer resp.Body.Close()
			body, err := common.CheckResponse(resp, "list egress")
			if err != nil {
				return err
			}
			var v egressView
			_ = json.Unmarshal(body, &v)
			renderEgress(v)
			return nil
		},
	}
}

func getEgressRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Force a DNS re-resolution and re-apply the slice's egress allowlist",
		Long: `Re-resolves every declared hostname and pushes the new IP set into the
slice's egress allowlist via the operator. Use when an allowlisted CDN's
IPs have rotated, or after editing your Driftfile via a manual deploy.

This is the v1 escape hatch for IP-changing hosts. A future sidecar
may automate this on a cron.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodPost, common.APIBaseURL+"/ops/atomic/egress/refresh", nil)
			if err != nil {
				return common.TransportError("refresh egress", err)
			}
			defer resp.Body.Close()
			body, err := common.CheckResponse(resp, "refresh egress")
			if err != nil {
				return err
			}
			var v egressView
			_ = json.Unmarshal(body, &v)
			renderEgress(v)
			return nil
		},
	}
}

func getEgressTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <host>",
		Short: "Check whether a host is on the active allowlist (local pattern match, no DNS)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/egress", nil)
			if err != nil {
				return common.TransportError("fetch egress", err)
			}
			defer resp.Body.Close()
			body, err := common.CheckResponse(resp, "fetch egress")
			if err != nil {
				return err
			}
			var v egressView
			_ = json.Unmarshal(body, &v)

			if v.Mode != "allowlist" {
				fmt.Printf("Slice egress mode is %q — every public host is reachable.\n", orOpen(v.Mode))
				return nil
			}

			match := matchHostAgainstList(target, v.DeclaredHosts)
			if match == "" {
				fmt.Printf("✗  %s is NOT on the allowlist.\n", target)
				fmt.Printf("    Add it to your Driftfile under slice.atomic.egress.hosts and run 'drift project deploy'.\n")
				return fmt.Errorf("not on allowlist")
			}
			fmt.Printf("✔  %s matches %q on the allowlist.\n", target, match)
			return nil
		},
	}
}

// matchHostAgainstList does the wildcard-aware prefix match for
// `drift atomic egress test`. Wildcard rule: a leading `*.` matches
// any single subdomain label or longer. Returns the matching entry,
// or "" if no match.
//
// Pure logic — covered by egress_test.go.
func matchHostAgainstList(target string, list []string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	if i := strings.Index(target, ":"); i > 0 {
		target = target[:i] // strip port from probe; entries with :port still match the host portion
	}
	for _, entry := range list {
		host := strings.ToLower(strings.TrimSpace(entry))
		if i := strings.Index(host, ":"); i > 0 {
			host = host[:i]
		}
		if strings.HasPrefix(host, "*.") {
			suffix := host[1:] // ".amazonaws.com"
			if strings.HasSuffix(target, suffix) && len(target) > len(suffix) {
				return entry
			}
			continue
		}
		if target == host {
			return entry
		}
	}
	return ""
}

func renderEgress(v egressView) {
	mode := orOpen(v.Mode)
	fmt.Printf("Mode: %s\n", mode)
	if mode == "open" {
		fmt.Println("Every public host on any port is reachable. Private CIDRs are blocked unconditionally.")
		return
	}
	fmt.Println()
	if len(v.DeclaredHosts) == 0 {
		fmt.Println("(no hosts declared — every external request will be denied)")
	} else {
		fmt.Println("Declared hosts:")
		for _, h := range v.DeclaredHosts {
			fmt.Printf("  - %s\n", h)
		}
	}
	if len(v.ResolvedIPs) > 0 {
		fmt.Println()
		fmt.Printf("Resolved to %d IP/port entries (active in the egress allowlist):\n", len(v.ResolvedIPs))
		for _, ip := range v.ResolvedIPs {
			fmt.Printf("  - %s\n", ip)
		}
		if v.ResolvedAt != "" {
			fmt.Printf("\nLast resolved: %s\n", v.ResolvedAt)
		}
	}
}

func orOpen(s string) string {
	if s == "" {
		return "open"
	}
	return s
}
