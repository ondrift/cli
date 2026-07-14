// create.go — `drift account create`. Two-step signup:
//
//  1. Initiate — POST /signup/initiate with {username, password,
//     email}. Password travels in plaintext over TLS; the auth
//     service bcrypts on receipt before persisting. The CLI does NOT bcrypt — that
//     keeps signup symmetric with login (which has always sent
//     plaintext) and lets the server enforce its own password
//     rules on the actual plaintext rather than on a hash.
//  2. Verify — user enters the 8-digit OTP from email, CLI POSTs
//     /signup/verify, server materialises the account and
//     returns the JWT pair.
//
// Password length and username shape are validated client-side as
// a UX nicety (so a typo doesn't round-trip the email-OTP step
// before failing). The server validates the same rules on receipt.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

var usernameRe = regexp.MustCompile(`^[a-z0-9]{2,32}$`)

func GetCreateCmd() *cobra.Command {
	var username, password, email string
	var passwordStdin bool
	var inviteCode string

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new account",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if username == "" {
				username = common.PromptForInput("Username")
			}
			if email == "" {
				email = common.PromptForInput("Email")
			}

			// Password source — same precedence as `drift account
			// login`: --password-stdin > --password > interactive.
			// Repeat-prompt is skipped on the non-interactive paths
			// (the caller is asserting the value is what they meant).
			interactivePassword := false
			switch {
			case passwordStdin:
				password = common.ReadPasswordFromStdin()
			case password != "":
				fmt.Fprintln(os.Stderr,
					"warning: --password leaks the password to shell history and `ps`. "+
						"Prefer `--password-stdin` (echo $PASS | drift account create --password-stdin).")
			default:
				password = common.PromptForInputHidden("Password")
				interactivePassword = true
			}

			if interactivePassword {
				repeatPassword := common.PromptForInputHidden("Repeat password")
				if password != repeatPassword {
					fmt.Println("Those passwords don't match. Try again.")
					return
				}
			}

			// Client-side validation. The server validates the same
			// rules on receipt; doing it here is a UX nicety so a
			// typo doesn't round-trip the email-OTP step before
			// failing.
			if len(password) < 8 {
				fmt.Println("Password must be at least 8 characters.")
				return
			}

			if !usernameRe.MatchString(username) {
				fmt.Println("Username must be 2-32 lowercase letters and numbers (no hyphens or special characters).")
				return
			}

			// Step 1: initiate signup — sends OTP to the user's email.
			// Password travels in plaintext over TLS; the auth service
			// bcrypts on receipt before persisting.
			fmt.Println("\nSending verification code...")

			initiatePayload, _ := json.Marshal(map[string]string{
				"username":    username,
				"password":    password,
				"email":       email,
				"invite_code": inviteCode,
			})

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Post(common.APIBaseURL+"/signup/initiate", "application/json", bytes.NewBuffer(initiatePayload))
			if err != nil {
				fmt.Println(common.TransportError("sign up", err))
				return
			}
			body, err := common.CheckResponse(resp, "sign up")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				fmt.Println(err)
				return
			}

			// Step 2: prompt for OTP and verify.
			// The code is always "000000" and the CLI skips the prompt
			// entirely either in local dev (DRIFT_ENV=local on the server)
			// or during a closed, invite-only alpha (the invite code already
			// gates access, so a second OTP round-trip is redundant) — the
			// server tells us which so the message shown is accurate.
			var initiateResp struct {
				OTPBypassed bool `json:"otp_bypassed"`
				InviteOnly  bool `json:"invite_only"`
			}
			_ = json.Unmarshal(body, &initiateResp)

			var code string
			if initiateResp.OTPBypassed {
				code = "000000"
				if initiateResp.InviteOnly {
					fmt.Println("Invite-only alpha — skipping email verification (your invite code is the gate).")
				} else {
					fmt.Println("Dev mode — skipping email verification.")
				}
			} else {
				fmt.Println("Check your email for an 8-digit verification code.")
				code = common.PromptForInput("Verification code")
			}

			verifyPayload, _ := json.Marshal(map[string]string{
				"username":  username,
				"code":      code,
				"device_id": common.GetOrCreateDeviceID(),
			})

			resp, err = client.Post(common.APIBaseURL+"/signup/verify", "application/json", bytes.NewBuffer(verifyPayload))
			if err != nil {
				fmt.Println(common.TransportError("verify your signup", err))
				return
			}
			body, err = common.CheckResponse(resp, "verify your signup")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				fmt.Println(err)
				return
			}

			// Parse tokens and save session.
			var tokenResp struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
				fmt.Println("Couldn't finish signing up: the API didn't return valid tokens. That's on us; please try again.")
				return
			}
			if err := common.SaveSession(tokenResp.AccessToken, tokenResp.RefreshToken); err != nil {
				fmt.Println("Signed up, but couldn't save your session to disk:", err)
				return
			}

			fmt.Printf("\n\033[48;2;241;160;6m"+" "+"\033[0m"+" Welcome to Drift, %s!\n", username)
			fmt.Println("\n\033[48;2;61;213;166m" + " " + "\033[0m" + " Next steps:")
			fmt.Println("  1. Create your first slice (project)            :: 'drift slice create <name>'")
			fmt.Println("  2. Deploy your app                              :: 'drift deploy drift.yaml'")
			fmt.Println("\nManage slices                                    :: 'drift slice list'")
			fmt.Println("Switch active slice                               :: 'drift slice use <name>'")
			fmt.Println("Check your usage anytime                          :: 'drift account usage'")
			fmt.Println("Happy building!")
		},
		Example: `  drift account create
  drift account create --username alice --email alice@example.com
  drift account create -u alice -e alice@example.com -p s3cret`,
	}

	createCmd.Flags().StringVarP(&username, "username", "u", "", "Username (skips interactive prompt)")
	createCmd.Flags().StringVarP(&email, "email", "e", "", "Email address (skips interactive prompt)")
	createCmd.Flags().StringVarP(&password, "password", "p", "", "Password (DEPRECATED: leaks to ps + shell history; use --password-stdin)")
	createCmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin (recommended for CI)")
	createCmd.Flags().StringVar(&inviteCode, "invite-code", "", "Invite code (required while the platform is in closed-alpha / invite-only mode)")
	createCmd.MarkFlagsMutuallyExclusive("password", "password-stdin")

	return createCmd
}
