package portal

import "testing"

// TestValidSliceName locks the naming rule to the platform's own
// (drift-common/slice.NameRegex): lowercase alphanumeric with INTERIOR
// hyphens, 1–30 chars, no leading/trailing hyphen. The interior-hyphen cases
// are the #CLITUI4 regression — the TUI used to reject any hyphen even though
// a Driftfile accepts them.
func TestValidSliceName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"myapp", true},
		{"sam-rivera", true},    // #CLITUI4: interior hyphen
		{"myapp-staging", true}, // per-env slice name
		{"a-b-c", true},
		{"ab--cd", true}, // consecutive interior hyphens are allowed by the server regex
		{"a", true},
		{"1app", true},
		{"-sam", false},   // leading hyphen
		{"sam-", false},   // trailing hyphen
		{"-", false},      // lone hyphen
		{"", false},       // empty
		{"MyApp", false},  // uppercase
		{"my_app", false}, // underscore
		{"my app", false}, // space
		{"this-name-is-way-too-long-to-be-valid", false}, // > 30 chars
	}
	for _, c := range cases {
		if got := validSliceName(c.name); got != c.want {
			t.Errorf("validSliceName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
