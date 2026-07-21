// domain.go — `drift slice domain {add, verify, list, remove}`.
// Thin wrappers around the API gateway's /ops/slice/domain* endpoints.
package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type domainResponse struct {
	Host         string   `json:"host"`
	Status       string   `json:"status"`
	StatusDetail string   `json:"status_detail,omitempty"`
	TXTToken     string   `json:"txt_token,omitempty"`
	Verify       string   `json:"verify,omitempty"`
	Instructions []string `json:"instructions,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	Wildcard     bool     `json:"wildcard,omitempty"`
}

func getDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Add, verify, list, or remove custom hostnames for your slice",
		Example: "  drift slice domain add forms.gemeente.example\n" +
			"  drift slice domain add imprente.com --wildcard\n" +
			"  drift slice domain verify forms.gemeente.example\n" +
			"  drift slice domain list\n" +
			"  drift slice domain remove forms.gemeente.example",
	}
	cmd.AddCommand(
		getDomainAddCmd(),
		getDomainVerifyCmd(),
		getDomainListCmd(),
		getDomainRemoveCmd(),
	)
	return cmd
}

func getDomainAddCmd() *cobra.Command {
	var wildcard bool
	cmd := &cobra.Command{
		Use:   "add <host>",
		Short: "Register a custom hostname for the active slice",
		Long: `Register a custom hostname for the active slice.

--wildcard opts the domain into routing every one of its subdomains
(isrand.imprente.com, alice.imprente.com, ...) to this SAME slice — your
app is then responsible for its own per-subdomain multitenancy (reading
its own Host header). This is a routing feature, not a wildcard TLS
certificate: each subdomain still gets its own certificate, issued on
first request, exactly like every other custom domain.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			host := strings.ToLower(strings.TrimSpace(args[0]))
			body, _ := json.Marshal(map[string]any{"host": host, "verify": "dns-txt", "wildcard": wildcard})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/slice/domain", strings.NewReader(string(body)))
			if err != nil {
				return common.TransportError("add domain", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "add domain")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var d domainResponse
			_ = json.Unmarshal(respBody, &d)
			fmt.Printf("Domain %s added (status: %s).\n\n", d.Host, d.Status)
			for _, line := range d.Instructions {
				fmt.Println(line)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&wildcard, "wildcard", false, "Route every subdomain of this domain to the same slice")
	return cmd
}

func getDomainVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <host>",
		Short: "Re-check the TXT record and (on success) issue the TLS certificate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			host := strings.ToLower(strings.TrimSpace(args[0]))
			u := common.APIBaseURL + "/ops/slice/domain/verify?host=" + url.QueryEscape(host)
			resp, err := common.DoJSONRequest(http.MethodPost, u, strings.NewReader("{}"))
			if err != nil {
				return common.TransportError("verify domain", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "verify domain")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var d domainResponse
			_ = json.Unmarshal(respBody, &d)
			switch d.Status {
			case "live":
				fmt.Printf("✔ %s is live.\n", d.Host)
			case "verified":
				fmt.Printf("✔ %s verified — issuing the TLS certificate now. Re-run `drift slice domain list` to check progress.\n", d.Host)
				if d.Detail != "" {
					fmt.Printf("  detail: %s\n", d.Detail)
				}
			default:
				fmt.Printf("Status: %s\n", d.Status)
				if d.StatusDetail != "" {
					fmt.Printf("Detail: %s\n", d.StatusDetail)
				}
			}
			return nil
		},
	}
}

func getDomainListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List custom hostnames for the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/domain", nil)
			if err != nil {
				return common.TransportError("list domains", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "list domains")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var domains []domainResponse
			_ = json.Unmarshal(respBody, &domains)
			if len(domains) == 0 {
				fmt.Println("No custom domains registered for this slice.")
				return nil
			}
			fmt.Printf("%-40s  %-10s  %-9s  %s\n", "HOST", "STATUS", "WILDCARD", "DETAIL")
			for _, d := range domains {
				detail := d.StatusDetail
				if d.TXTToken != "" && d.Status == "pending" {
					detail = "TXT token: " + d.TXTToken
				}
				wildcard := ""
				if d.Wildcard {
					wildcard = "yes"
				}
				fmt.Printf("%-40s  %-10s  %-9s  %s\n", d.Host, d.Status, wildcard, detail)
			}
			return nil
		},
	}
}

func getDomainRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <host>",
		Short: "Remove a custom hostname (also removes its TLS certificate and routing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			host := strings.ToLower(strings.TrimSpace(args[0]))
			u := common.APIBaseURL + "/ops/slice/domain?host=" + url.QueryEscape(host)
			resp, err := common.DoRequest(http.MethodDelete, u, nil)
			if err != nil {
				return common.TransportError("remove domain", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, _ = common.CheckResponse(resp, "remove domain")
				return nil
			}
			fmt.Printf("Domain %s removed.\n", host)
			return nil
		},
	}
}
