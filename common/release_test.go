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

func TestCLIInstallPath(t *testing.T) {
	const base = "github.com/ondrift/cli"
	cases := []struct {
		name         string
		target, from string
		want         string
	}{
		// v1 has no major suffix — Go only requires one from v2 on. This is also
		// the rollback path (`drift upgrade v1.8.1`), which a hardcoded "/v2"
		// would break: a /v2 module path cannot serve a v1 tag.
		{"v1 target", "v1.8.1", "v2.2.0", base + "/cmd/drift"},
		{"v1 both", "v1.23.2", "v1.22.0", base + "/cmd/drift"},

		// The bug this fixes: v2.2.0 must resolve through .../cli/v2/cmd/drift.
		{"v2 target", "v2.2.0", "v1.23.2", base + "/v2/cmd/drift"},
		{"v2 from v2", "v2.2.1", "v2.2.0", base + "/v2/cmd/drift"},

		// Future majors need no code change — that's the point of deriving it.
		{"v3 target", "v3.0.0", "v2.2.0", base + "/v3/cmd/drift"},
		{"v10 target", "v10.1.0", "v2.2.0", base + "/v10/cmd/drift"},

		// Non-version labels carry no major, so the running binary's major wins:
		// @latest under a fixed module path means "newest release of THAT major".
		{"latest falls back to current", "latest", "v2.2.0", base + "/v2/cmd/drift"},
		{"latest on v1", "latest", "v1.23.2", base + "/cmd/drift"},
		{"branch falls back", "main", "v2.0.0", base + "/v2/cmd/drift"},
		{"commit sha falls back", "3104b18", "v2.1.0", base + "/v2/cmd/drift"},

		// Unprefixed versions are accepted the same way normalizeVersion does.
		{"no v prefix", "2.2.0", "", base + "/v2/cmd/drift"},

		// Last resort with nothing to go on: the pre-v2 path.
		{"both empty", "", "", base + "/cmd/drift"},
	}
	for _, c := range cases {
		if got := CLIInstallPath(c.target, c.from); got != c.want {
			t.Errorf("%s: CLIInstallPath(%q, %q) = %q, want %q", c.name, c.target, c.from, got, c.want)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"v2.2.0", 2}, {"2.2.0", 2}, {"v1.0.0", 1}, {"v10.0.0", 10},
		{"v2.2.0-rc1", 2}, // pre-release suffix ignored
		{"latest", 0}, {"main", 0}, {"3104b18", 0}, {"", 0},
	}
	for _, c := range cases {
		if got := MajorVersion(c.in); got != c.want {
			t.Errorf("MajorVersion(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
