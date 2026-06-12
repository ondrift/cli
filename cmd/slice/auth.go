// auth.go — `drift slice auth {set, list, disable}`.
//
// A SOFT Basic-auth gate in front of your slice's site — "keep the public /
// crawlers out of my not-public-yet site," not a hard access boundary. Enforced
// by the platform's router on Canvas/site paths (never your /api or /trigger
// routes, so it won't collide with a function's own auth). For real per-request
// authorization, use `drift atomic auth`. Thin wrappers around the API gateway's
// /ops/slice/auth endpoints.
package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

type siteAuthUser struct {
	Name string `json:"name"`
}

type siteAuthResponse struct {
	Enabled bool           `json:"enabled"`
	Realm   string         `json:"realm,omitempty"`
	Users   []siteAuthUser `json:"users,omitempty"`
}

func getAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Protect your site with a username/password login (soft gate for not-public-yet sites)",
		Long: "Put an HTTP Basic-auth login in front of your slice's site — handy for keeping\n" +
			"the public and crawlers out of a staging or pre-launch site.\n\n" +
			"It's a SOFT gate over TLS, not a hard access boundary, and it only covers your\n" +
			"site pages — your /api and /trigger routes are left alone so a function's own\n" +
			"auth keeps working. For real per-request authorization, use `drift atomic auth`.",
		Example: "  drift slice auth set alice\n" +
			"  drift slice auth set alice bob --realm \"Staging\"\n" +
			"  drift slice auth list\n" +
			"  drift slice auth disable",
	}
	cmd.AddCommand(getAuthSetCmd(), getAuthListCmd(), getAuthDisableCmd())
	return cmd
}

func getAuthSetCmd() *cobra.Command {
	var realm string
	var passwordStdin bool

	cmd := &cobra.Command{
		Use:   "set <username> [username...]",
		Short: "Enable (or replace) the login gate with one or more users",
		Long: "Enables the gate with the given user(s), prompting for each password (hidden).\n\n" +
			"This REPLACES the whole user list — the platform stores only bcrypt hashes and\n" +
			"can't return existing passwords, so to add a user you re-run `set` with every\n" +
			"username you want. Use `--password-stdin` (single user) for scripts.",
		Example: "  drift slice auth set alice\n" +
			"  drift slice auth set alice bob carol --realm \"Staging\"\n" +
			"  printf 'hunter2' | drift slice auth set alice --password-stdin",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}

			// Validate every username up front so we never prompt for three
			// passwords and then reject the batch on the first bad name.
			seen := make(map[string]bool, len(args))
			for _, name := range args {
				if err := validateSiteAuthUsername(name); err != nil {
					return err
				}
				if seen[name] {
					return fmt.Errorf("duplicate username: %s", name)
				}
				seen[name] = true
			}

			type userInput struct {
				Name     string `json:"name"`
				Password string `json:"password"`
			}
			var users []userInput

			if passwordStdin {
				if len(args) != 1 {
					return fmt.Errorf("--password-stdin can only be used with a single username")
				}
				pw := common.ReadPasswordFromStdin()
				if pw == "" {
					return fmt.Errorf("empty password on stdin")
				}
				users = append(users, userInput{Name: args[0], Password: pw})
			} else {
				for _, name := range args {
					pw := common.PromptForInputHidden(fmt.Sprintf("Password for %s", name))
					if pw == "" {
						return fmt.Errorf("password for %s cannot be empty", name)
					}
					users = append(users, userInput{Name: name, Password: pw})
				}
			}

			body, _ := json.Marshal(map[string]any{"realm": realm, "users": users})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/slice/auth", bytes.NewReader(body))
			if err != nil {
				return common.TransportError("set site auth", err)
			}
			defer resp.Body.Close()
			if _, err := common.CheckResponse(resp, "set site auth"); err != nil {
				fmt.Println(err)
				return nil
			}

			fmt.Printf("✔ Login gate enabled — %d user(s). Your site now prompts for a username and password.\n", len(users))
			fmt.Println("  Soft gate for staging / not-public-yet sites; your /api and /trigger routes are unaffected.")
			return nil
		},
	}

	cmd.Flags().StringVar(&realm, "realm", "", "Realm shown in the browser's login prompt (default \"Restricted\")")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin (single user; keeps it out of shell history)")
	return cmd
}

func getAuthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the login gate configured for the active slice (usernames only, never passwords)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				return common.TransportError("list site auth", err)
			}
			defer resp.Body.Close()
			respBody, err := common.CheckResponse(resp, "list site auth")
			if err != nil {
				fmt.Println(err)
				return nil
			}
			var sa siteAuthResponse
			_ = json.Unmarshal(respBody, &sa)
			if !sa.Enabled || len(sa.Users) == 0 {
				fmt.Println("No login gate on this slice — your site is public.")
				fmt.Println("Set one with `drift slice auth set <username>`.")
				return nil
			}
			realm := sa.Realm
			if realm == "" {
				realm = "Restricted"
			}
			fmt.Printf("Login gate ENABLED (realm: %s)\n\n", realm)
			fmt.Println("USERS")
			for _, u := range sa.Users {
				fmt.Printf("  %s\n", u.Name)
			}
			return nil
		},
	}
}

func getAuthDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove the login gate (your site becomes public again)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := common.RequireActiveSlice(); err != nil {
				return err
			}
			resp, err := common.DoRequest(http.MethodDelete, common.APIBaseURL+"/ops/slice/auth", nil)
			if err != nil {
				return common.TransportError("disable site auth", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 {
				_, _ = common.CheckResponse(resp, "disable site auth")
				return nil
			}
			fmt.Println("✔ Login gate disabled. Your site is public again.")
			return nil
		},
	}
}

// validateSiteAuthUsername mirrors the api's username rules so the CLI fails
// fast with a clear message before prompting for any password.
func validateSiteAuthUsername(name string) error {
	if name == "" {
		return fmt.Errorf("username cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("username %q is too long (max 64)", name)
	}
	if strings.ContainsRune(name, ':') {
		return fmt.Errorf("username %q may not contain ':'", name)
	}
	return nil
}
