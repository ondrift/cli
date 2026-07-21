// login.go — `drift account login`. POSTs `{username, password,
// device_id}` to /login; on success, persists the JWT pair to
// `~/.drift/session.json` (mode 0600). The device_id is a
// stable per-workstation random ID (see common/session.go ::
// GetOrCreateDeviceID) — refresh tokens are bound to it, so a
// stolen session.json without the matching device_id can't refresh.
//
// Three ways to supply the password:
//
//   - Interactive (default) — `drift account login` prompts for it
//     with terminal echo disabled.
//   - --password-stdin       — `echo $PASS | drift account login -u alice
//     --password-stdin`. The password never appears as a process
//     argument; doesn't show up in `ps`, shell history, or
//     process-listing logs. The pattern gh / docker login / kubectl /
//     doctl / op all use. Recommended for CI.
//   - --password (-p)        — kept for backward compatibility but
//     prints a deprecation warning. The password ends up in shell
//     history and `ps` output. Migrate to --password-stdin.
package account

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

// DoLoginErr performs the login POST and persists the session, returning an
// error instead of printing one. Lets callers outside the CLI-command UX
// (the portal's pre-launch login window) react to a failed attempt
// themselves — DoLogin below is just this plus the command's print-and-return
// behavior.
func DoLoginErr(username, password string) error {
	jsonData, _ := json.Marshal(map[string]string{
		"username":  username,
		"password":  password,
		"device_id": common.GetOrCreateDeviceID(),
	})

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Post(common.APIBaseURL+"/login", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return common.TransportError("log in", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "log in")
	if err != nil {
		return err
	}

	var respData map[string]string
	if err := json.Unmarshal(body, &respData); err != nil {
		return fmt.Errorf("couldn't log in: the API response didn't look right — %w", err)
	}

	token := respData["access_token"]
	refreshToken := respData["refresh_token"]
	if token == "" || refreshToken == "" {
		return fmt.Errorf("couldn't log in: the API didn't return a full set of tokens. That's on us; please try again")
	}

	if err := common.SaveSession(token, refreshToken); err != nil {
		return fmt.Errorf("logged in, but couldn't save your session to disk: %w", err)
	}
	return nil
}

func DoLogin(username, password string) {
	if err := DoLoginErr(username, password); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Logged in as %s.\n", username)
}

func GetLoginCmd() *cobra.Command {
	var username, password string
	var passwordStdin bool

	loginCmd := &cobra.Command{
		Use:   "login",
		Short: "Login to Drift and get a JWT token",
		Example: `  drift account login
  drift account login --username alice
  echo $PASS | drift account login -u alice --password-stdin`,
		Run: func(cmd *cobra.Command, args []string) {
			if username == "" {
				username = common.PromptForInput("Username")
			}

			// Source resolution order: --password-stdin wins, then
			// --password, then interactive prompt. The two flags
			// are mutually exclusive at the cobra level below;
			// even so, this branch keeps the precedence clear.
			switch {
			case passwordStdin:
				password = common.ReadPasswordFromStdin()
			case password != "":
				fmt.Fprintln(os.Stderr,
					"warning: --password leaks the password to shell history and `ps`. "+
						"Prefer `--password-stdin` (echo $PASS | drift account login --password-stdin).")
			default:
				password = common.PromptForInputHidden("Password")
			}

			DoLogin(username, password)
		},
	}

	loginCmd.Flags().StringVarP(&username, "username", "u", "", "Username (skips interactive prompt)")
	loginCmd.Flags().StringVarP(&password, "password", "p", "", "Password (DEPRECATED: leaks to ps + shell history; use --password-stdin)")
	loginCmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from stdin (recommended for CI)")
	loginCmd.MarkFlagsMutuallyExclusive("password", "password-stdin")

	return loginCmd
}
