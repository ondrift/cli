package common

import (
	"os"
)

// ANSI escape sequences. The colors below match the website mockup at
// website/html/index.html (the "drift deploy" CLI demo card).
//
// 24-bit truecolor is used so the CLI matches the marketing screenshot
// exactly. NO_COLOR=1 (https://no-color.org) disables all styling, and
// styling is also dropped automatically when stdout is not a terminal so
// that piped output stays clean.
const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	atomic = "\x1b[1;38;2;241;160;6m"   // #f1a006 — bold orange
	bone   = "\x1b[1;38;2;130;105;235m" // #8269eb — bold purple
	canvas = "\x1b[1;38;2;16;185;129m"  // #10b981 — bold emerald
	check  = "\x1b[38;2;16;185;129m"    // #10b981 — emerald (no bold)
	hint   = "\x1b[38;2;106;153;85m"    // #6A9955 — VS Code "comment" green
	blueHi = "\x1b[38;2;86;156;214m"    // #569CD6 — VS Code "keyword" blue
)

// IsStyleEnabled reports whether ANSI styling should be emitted. Styling is
// suppressed when NO_COLOR is set, when output is being piped, or when the
// terminal advertises itself as dumb.
func IsStyleEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// style wraps s in the given ANSI escape sequence when styling is enabled,
// or returns s unchanged otherwise.
func style(escape, s string) string {
	if !IsStyleEnabled() {
		return s
	}
	return escape + s + reset
}

// AtomicHeader styles the "Atomic" section header.
func AtomicHeader() string { return style(atomic, "Atomic") }

// BackboneHeader styles the "Backbone" section header.
func BackboneHeader() string { return style(bone, "Backbone") }

// CanvasHeader styles the "Canvas" section header.
func CanvasHeader() string { return style(canvas, "Canvas") }

// Check returns a styled checkmark for successful items.
func Check() string { return style(check, "✓") }

// Hint styles a parenthetical hint string (e.g. source file annotation).
func Hint(s string) string { return style(hint, s) }

// Highlight styles a value that should stand out, such as the template name.
func Highlight(s string) string { return style(blueHi, s) }

// BoldText returns the input wrapped in ANSI bold (no color).
func BoldText(s string) string { return style(bold, s) }
