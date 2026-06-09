// domains.go — Driftfile reconcile of `slice.domains[]`. Adds any
// host that's declared in the manifest but missing on the slice;
// removes any host that's live on the slice but absent from the
// manifest. After add, the user still has to flip DNS and run
// `drift slice domain verify <host>` — that step requires the
// CNAME / TXT to be live and is not something we can do
// automatically from the deploy path.
package project

import (
	"encoding/json"
	"fmt"
	"github.com/ondrift/cli/common"
	"net/http"
	"net/url"
	"strings"
)

type liveDomain struct {
	Host   string `json:"host"`
	Status string `json:"status"`
}

// applyDomains reconciles the slice's custom-domain set against the
// Driftfile. The function is best-effort: failures are surfaced but
// don't abort the deploy — the rest of the slice (atomic, backbone,
// canvas) is more important than the cosmetic step of attaching a
// hostname.
func applyDomains(m *Manifest) error {
	declared := map[string]Manifest{}
	for _, d := range m.Slice.Domains {
		host := strings.ToLower(strings.TrimSpace(d.Host))
		if host == "" {
			continue
		}
		declared[host] = Manifest{}
	}

	live, err := fetchLiveDomains()
	if err != nil {
		fmt.Printf("  %s domain reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}

	liveByHost := map[string]liveDomain{}
	for _, d := range live {
		liveByHost[strings.ToLower(d.Host)] = d
	}

	added := 0
	removed := 0

	for host := range declared {
		if _, ok := liveByHost[host]; ok {
			continue
		}
		if err := addDomain(host); err != nil {
			fmt.Printf("  %s add domain %s: %v\n", common.Hint("·"), host, err)
			continue
		}
		added++
		fmt.Printf("  %s domain added: %s (verify with `drift slice domain verify %s` once DNS is live)\n",
			common.Check(), host, host)
	}

	for host := range liveByHost {
		if _, ok := declared[host]; ok {
			continue
		}
		if err := removeDomain(host); err != nil {
			fmt.Printf("  %s remove domain %s: %v\n", common.Hint("·"), host, err)
			continue
		}
		removed++
		fmt.Printf("  %s domain removed: %s\n", common.Check(), host)
	}

	if added == 0 && removed == 0 && len(declared) > 0 {
		fmt.Printf("  %s domains in sync (%d declared)\n", common.Hint("·"), len(declared))
	}
	return nil
}

func fetchLiveDomains() ([]liveDomain, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/domain", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list domains")
	if err != nil {
		return nil, err
	}
	var out []liveDomain
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func addDomain(host string) error {
	body, _ := json.Marshal(map[string]string{"host": host, "verify": "dns-txt"})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/slice/domain", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "add domain")
		return e
	}
	return nil
}

func removeDomain(host string) error {
	resp, err := common.DoRequest(http.MethodDelete,
		common.APIBaseURL+"/ops/slice/domain?host="+url.QueryEscape(host), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "remove domain")
		return e
	}
	return nil
}
