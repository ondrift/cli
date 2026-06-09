package common

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser launches the user's default web browser at the given URL.
//
// The URL arrives over the network (a configurator handoff response).
// Even though that response comes back over HTTPS in production, an
// attacker with TLS-MITM access or a compromised configurator could
// influence the URL. The OS-level launchers (`open` on macOS,
// `xdg-open` on Linux, `cmd /c start` on Windows) all interpret a URL
// that begins with `-` as a flag — and macOS's `open --background`
// would silently load the URL in any zero-day-vulnerable browser
// without bringing it to the foreground. Two defences:
//
//  1. Validate the URL parses with a known scheme (https, or http to
//     localhost for dev) before passing it to any shell. Reject
//     anything else.
//  2. On macOS / Linux, insert `--` between the command and the URL
//     so the launcher stops interpreting flags.
//
// We avoid pulling in a third-party dependency for the launch itself
// — the per-OS command is a one-liner. If the launch fails (no
// display, SSH session, container, locked-down environment) we
// return an error so the caller can fall back to printing the URL
// for the user to open manually.
func OpenBrowser(rawURL string) error {
	if err := validateOpenURL(rawURL); err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "--", rawURL) // #nosec G204
	case "linux":
		cmd = exec.Command("xdg-open", "--", rawURL) // #nosec G204
	case "windows":
		// Windows `start` doesn't accept `--` as a separator; instead
		// pass an empty title argument first so the URL can never be
		// confused with a /flag.
		cmd = exec.Command("cmd", "/c", "start", "", rawURL) // #nosec G204
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// validateOpenURL refuses anything that isn't a plain http/https URL
// — and even http is only allowed against localhost (dev-mode
// handoff). This is a structural defence against the launcher
// flag-injection class of attacks: an attacker can't sneak in
// `--background` if the input doesn't parse as a URL with the right
// scheme.
func validateOpenURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("refusing to open empty URL")
	}
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("refusing to open URL beginning with '-': %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("refusing to open unparseable URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("refusing to open plaintext-http URL outside localhost: %q", raw)
	default:
		return fmt.Errorf("refusing to open URL with unsupported scheme %q", u.Scheme)
	}
}
