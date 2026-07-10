// reset.go — `drift account reset-password`. Two-step, guided password
// reset, same shape as `drift account create`'s signup flow:
//
//  1. Initiate — POST /reset/initiate {username}. The server always
//     returns success (anti-enumeration), whether or not the account
//     exists — so the CLI can't distinguish "code sent" from "no such
//     user" here. That's intentional; if nothing arrives, try the
//     username again.
//  2. Verify — user enters the code emailed to them plus a new
//     password; CLI POSTs /reset/verify {username, code, new_password}.
//     A successful reset revokes every existing session for the account
//     server-side and issues no new one, so the CLI immediately logs
//     back in with the new password to leave you signed in.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func GetResetPasswordCmd() *cobra.Command {
	var username string

	resetCmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Reset a forgotten password via an emailed code",
		Example: `  drift account reset-password
  drift account reset-password --username alice`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if username == "" {
				username = common.PromptForInput("Username")
			}

			fmt.Println("\nSending reset code...")
			initiatePayload, _ := json.Marshal(map[string]string{"username": username})

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Post(common.APIBaseURL+"/reset/initiate", "application/json", bytes.NewBuffer(initiatePayload))
			if err != nil {
				fmt.Println(common.TransportError("request a password reset", err))
				return
			}
			_, err = common.CheckResponse(resp, "request a password reset")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				fmt.Println(err)
				return
			}

			fmt.Println("If that account exists, a reset code has been emailed to it.")
			code := common.PromptForInput("Reset code")

			newPassword := common.PromptForInputHidden("New password")
			repeatPassword := common.PromptForInputHidden("Repeat new password")
			if newPassword != repeatPassword {
				fmt.Println("Those passwords don't match. Try again.")
				return
			}
			if len(newPassword) < 8 {
				fmt.Println("Password must be at least 8 characters.")
				return
			}

			verifyPayload, _ := json.Marshal(map[string]string{
				"username":     username,
				"code":         code,
				"new_password": newPassword,
			})
			resp, err = client.Post(common.APIBaseURL+"/reset/verify", "application/json", bytes.NewBuffer(verifyPayload))
			if err != nil {
				fmt.Println(common.TransportError("verify the reset code", err))
				return
			}
			_, err = common.CheckResponse(resp, "verify the reset code")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if err != nil {
				fmt.Println(err)
				return
			}

			fmt.Println("Password reset. Every existing session for this account has been signed out.")

			// The reset revokes every refresh token server-side and issues no
			// new one — log back in immediately so this doesn't leave you
			// signed out of your own CLI session.
			DoLogin(username, newPassword)
		},
	}

	resetCmd.Flags().StringVarP(&username, "username", "u", "", "Username (skips interactive prompt)")
	return resetCmd
}
