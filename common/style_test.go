package common

import (
	"testing"
)

func TestStyleDisabledInTests(t *testing.T) {
	// In test execution, stdout is not a TTY, so style should be disabled.
	if IsStyleEnabled() {
		t.Skip("style is enabled (running in a real terminal)")
	}

	// When style is disabled, functions should return plain text.
	if got := AtomicHeader(); got != "Atomic" {
		t.Fatalf("AtomicHeader: got %q, want Atomic", got)
	}
	if got := BackboneHeader(); got != "Backbone" {
		t.Fatalf("BackboneHeader: got %q, want Backbone", got)
	}
	if got := CanvasHeader(); got != "Canvas" {
		t.Fatalf("CanvasHeader: got %q, want Canvas", got)
	}
	if got := Check(); got != "✓" {
		t.Fatalf("Check: got %q, want ✓", got)
	}
	if got := Hint("(hint)"); got != "(hint)" {
		t.Fatalf("Hint: got %q", got)
	}
	if got := Highlight("value"); got != "value" {
		t.Fatalf("Highlight: got %q", got)
	}
	if got := BoldText("bold"); got != "bold" {
		t.Fatalf("BoldText: got %q", got)
	}
}

func TestStyleFunction(t *testing.T) {
	// Direct test of the style() function behavior.
	// In test context (not a TTY), it should return the input unchanged.
	got := style(atomic, "test")
	if IsStyleEnabled() {
		// If somehow in a TTY, it should contain ANSI codes.
		if got == "test" {
			t.Fatal("style should add ANSI when enabled")
		}
	} else {
		if got != "test" {
			t.Fatalf("style should be passthrough when disabled: got %q", got)
		}
	}
}

func TestStyleEnabledRespectsNOCOLOR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if IsStyleEnabled() {
		t.Fatal("styleEnabled should return false when NO_COLOR is set")
	}
}

func TestStyleEnabledRespectsDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	if IsStyleEnabled() {
		t.Fatal("styleEnabled should return false for TERM=dumb")
	}
}
