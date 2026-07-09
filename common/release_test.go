package common

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.11.0", "v1.11.0", 0},
		{"1.11.0", "v1.11.0", 0}, // optional leading v
		{"v1.10.0", "v1.11.0", -1},
		{"v1.11.0", "v1.10.0", 1},
		{"v1.9.0", "v1.10.0", -1},     // numeric, not lexical (9 < 10)
		{"v2.0.0", "v1.99.99", 1},     // major dominates
		{"v1.8.1", "v1.8", 1},         // missing patch counts as 0
		{"v1.11.0-rc1", "v1.11.0", 0}, // pre-release suffix ignored
		{"v1.12.0", "v1.11.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		// Antisymmetry: swapping arguments negates the result.
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d (antisymmetry)", c.b, c.a, got, -c.want)
		}
	}
}
