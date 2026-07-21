// login.go — the pre-launch session gate for the bare `drift` dashboard.
// Runs in cooked mode, before the alt-screen buffer opens: with no session,
// or a stored access token whose exp claim has already passed and can't be
// silently refreshed, this shows a login window and blocks until it
// succeeds. Nothing about the dashboard itself (model, raw mode, alt-screen)
// is touched until a usable session exists — see the call site in Run.
package portal

import (
	"fmt"
	"strings"

	account "github.com/ondrift/cli/v2/cmd/account"
	"github.com/ondrift/cli/v2/common"
)

// ensureLoggedIn gates dashboard launch on a usable session: nothing to do
// when the stored token is present and unexpired; a locally-expired token is
// refreshed silently first (the same thing a live API call would trigger on
// its own 401 anyway, so doing it upfront costs nothing extra); only a hard
// failure — no session, or refresh also failing — shows the login window.
func ensureLoggedIn() error {
	if common.GetUsername() == "" {
		return runLoginWindow("You're not logged in to Drift.")
	}
	if common.TokenExpired() {
		if err := common.RefreshAccessToken(); err == nil {
			return nil
		}
		return runLoginWindow("Your session has expired.")
	}
	return nil
}

// runLoginWindow prints a bordered login prompt and blocks until a login
// attempt succeeds (Ctrl-C exits like any other cooked-mode prompt). The
// password field supports paste: term.ReadPassword (via
// common.PromptForInputHidden) just reads bytes until Enter, and a
// terminal-pasted password arrives indistinguishable from fast typing.
func runLoginWindow(reason string) error {
	const w = 46
	rule := strings.Repeat("─", w-2)
	line := func(s string) string { return "│ " + pad(s, w-4) + " │" }

	fmt.Println()
	fmt.Println("┌" + rule + "┐")
	fmt.Println(line(bold("Drift login")))
	fmt.Println(line(""))
	fmt.Println(line(reason))
	fmt.Println(line("Log in to continue."))
	fmt.Println("└" + rule + "┘")
	fmt.Println()

	for {
		username := common.PromptForInput("Username")
		password := common.PromptForInputHidden("Password")
		if err := account.DoLoginErr(username, password); err != nil {
			fmt.Println(cRed+"✗"+cReset, err)
			fmt.Println()
			continue
		}
		fmt.Printf("Logged in as %s.\n\n", username)
		return nil
	}
}
