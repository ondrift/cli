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

func TestIsSemverTag(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"v1.11.0", true},
		{"1.11.0", true}, // optional leading v
		{"v1.0.0", true},
		{"v1.11", false},       // missing patch — not a clean release tag
		{"v1.11.0-rc1", false}, // pre-release suffix — selection wants a clean tag
		{"master", false},      // not version-shaped at all
		{"v1.x.0", false},      // non-numeric segment
		{"", false},
	}
	for _, c := range cases {
		if got := isSemverTag(c.name); got != c.want {
			t.Errorf("isSemverTag(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLatestSemverTag(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{
			name: "picks the highest, ignoring API order",
			tags: []string{"v1.9.0", "v1.11.0", "v1.10.0"},
			want: "v1.11.0",
		},
		{
			name: "ignores non-version tags",
			tags: []string{"v1.2.0", "demo-snapshot", "v1.3.0", "master"},
			want: "v1.3.0",
		},
		{
			name: "empty input",
			tags: nil,
			want: "",
		},
		{
			name: "no version-shaped tags",
			tags: []string{"demo", "staging"},
			want: "",
		},
	}
	for _, c := range cases {
		if got := latestSemverTag(c.tags); got != c.want {
			t.Errorf("%s: latestSemverTag(%v) = %q, want %q", c.name, c.tags, got, c.want)
		}
	}
}
