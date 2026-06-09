// egress.go — Driftfile reconcile of the per-slice outbound egress
// allowlist. Compares `slice.atomic.egress` from the Driftfile
// against the live mode + declared hosts on the slice, and triggers
// a refresh if anything changed. The operator does the DNS
// resolution and pushes the rendered IP/port list into the slice's
// NetworkPolicy via charter — the CLI's only job is to decide
// whether a refresh is needed.
//
// Reconcile semantics:
//
//   - Driftfile has no egress block (or `mode` is empty / "open"),
//     and live is also open → no-op.
//   - Driftfile is open, live is allowlist → POST refresh; operator
//     re-renders the chart with `mode: open` and the slice goes
//     back to "any public host."
//   - Driftfile is allowlist, live is open OR list differs → POST
//     refresh; operator re-resolves and pushes the new IP set.
//   - Driftfile is allowlist, live matches exactly → no-op.
package project

import (
	"encoding/json"
	"fmt"
	"github.com/ondrift/cli/common"
	"net/http"
	"sort"
	"strings"
)

// liveEgressView is the JSON shape returned by GET /ops/atomic/egress.
type liveEgressView struct {
	Mode          string   `json:"mode"`
	DeclaredHosts []string `json:"declared_hosts"`
}

func applyEgress(m *Manifest) error {
	declaredMode, declaredHosts := desiredEgress(m)

	live, err := fetchLiveEgress()
	if err != nil {
		// The endpoint may not be available against an older operator.
		// Surface the warning and skip — egress is additive; not
		// applying it shouldn't abort the rest of the deploy.
		fmt.Printf("  %s egress reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}

	if egressInSync(declaredMode, declaredHosts, live) {
		// Don't print anything when there's nothing to say — matches
		// the rest of the apply* helpers' quiet path.
		return nil
	}

	if err := refreshEgress(); err != nil {
		fmt.Printf("  %s egress refresh: %v\n", common.Hint("·"), err)
		return nil
	}

	if declaredMode == "allowlist" {
		fmt.Printf("  %s egress allowlist applied (%d host%s)\n",
			common.Check(), len(declaredHosts), pluralS(len(declaredHosts)))
	} else {
		fmt.Printf("  %s egress mode set to open\n", common.Check())
	}
	return nil
}

// desiredEgress folds the Driftfile's egress block into the
// (mode, hosts) tuple the rest of this file works in. Default
// (block absent or mode unset) is "open" with no hosts.
func desiredEgress(m *Manifest) (mode string, hosts []string) {
	if m.Slice.Atomic.Egress == nil {
		return "open", nil
	}
	mode = strings.ToLower(strings.TrimSpace(m.Slice.Atomic.Egress.Mode))
	if mode == "" {
		mode = "open"
	}
	hosts = append(hosts, m.Slice.Atomic.Egress.Hosts...)
	return mode, hosts
}

// egressInSync compares the desired mode + host list against the
// live view. Hosts are compared as sorted, lowercased, trimmed
// strings — the user can re-order their Driftfile entries without
// triggering a refresh. Mode "" is normalised to "open" on the
// live side.
func egressInSync(mode string, hosts []string, live liveEgressView) bool {
	liveMode := strings.ToLower(strings.TrimSpace(live.Mode))
	if liveMode == "" {
		liveMode = "open"
	}
	if mode != liveMode {
		return false
	}
	if mode == "open" {
		return true // hosts are irrelevant when mode is open
	}
	return normaliseHosts(hosts) == normaliseHosts(live.DeclaredHosts)
}

func normaliseHosts(in []string) string {
	cp := make([]string, 0, len(in))
	for _, h := range in {
		s := strings.ToLower(strings.TrimSpace(h))
		if s != "" {
			cp = append(cp, s)
		}
	}
	sort.Strings(cp)
	return strings.Join(cp, "\x00")
}

func fetchLiveEgress() (liveEgressView, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/egress", nil)
	if err != nil {
		return liveEgressView{}, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "fetch egress")
	if err != nil {
		return liveEgressView{}, err
	}
	var v liveEgressView
	if err := json.Unmarshal(body, &v); err != nil {
		return liveEgressView{}, err
	}
	return v, nil
}

func refreshEgress() error {
	resp, err := common.DoRequest(http.MethodPost, common.APIBaseURL+"/ops/atomic/egress/refresh", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "refresh egress")
		return e
	}
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
