package upgrade

import "testing"

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"":          "latest",
		"1.8.1":     "v1.8.1", // bare semver gets the v
		"v1.8.1":    "v1.8.1", // already prefixed
		"latest":    "latest",
		"main":      "main",    // a branch ref passes through
		"abc1234":   "abc1234", // a commit-ish passes through
		"2.0.0-rc1": "v2.0.0-rc1",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
