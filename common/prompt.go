// prompt.go — minimal interactive prompts using only stdlib +
// golang.org/x/term (which we already pull in indirectly via
// cobra). Replaced manifoldco/promptui — that package was
// unmaintained since 2021, and our two call sites are simple
// enough that 30 lines of stdlib reads cleaner than the
// dependency.
package common

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// PromptForInput prints a label and reads a line from stdin. The
// returned string has its trailing newline stripped. If stdin is
// closed or unreadable, returns the empty string.
func PromptForInput(label string) string {
	fmt.Fprintf(os.Stdout, "%s: ", label)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return strings.TrimRight(line, "\r\n")
	}
	return strings.TrimRight(line, "\r\n")
}

// PromptForInputHidden prints a label and reads a line from stdin
// with terminal echo disabled — so the typed value (a password)
// doesn't appear on screen. Falls back to non-hidden read if stdin
// isn't a terminal (e.g. piped input in CI).
//
// term.ReadPassword restores the terminal to its prior state on
// return, so the user's shell doesn't end up in a no-echo state
// even on signal-driven termination.
func PromptForInputHidden(label string) string {
	fmt.Fprintf(os.Stdout, "%s: ", label)

	// If stdin isn't a terminal we can't toggle echo off — fall back
	// to a normal line read. The caller still gets the value; only
	// the on-screen masking is missing.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n")
	}

	bytes, err := term.ReadPassword(fd)
	// term.ReadPassword swallows the trailing newline; print one so
	// subsequent output isn't on the same line as the password
	// prompt.
	fmt.Fprintln(os.Stdout)
	if err != nil {
		return ""
	}
	return string(bytes)
}

// ReadPasswordFromStdin reads a single line of plaintext from
// stdin, strips the trailing newline, and returns it. Used by
// `--password-stdin` flags so the password never appears as a
// CLI argument (which would expose it to `ps`, shell history, and
// process-listing log surfaces). Standard pattern matched by gh,
// docker login, kubectl, doctl, op. Returns the empty string on
// read error.
func ReadPasswordFromStdin() string {
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
