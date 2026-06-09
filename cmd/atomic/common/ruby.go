// ruby.go — locate a usable host Ruby.
//
// Drift's promise for Ruby is "write Ruby, have Ruby, go" — no Docker, no
// version pin. The only wrinkle is macOS: Apple ships an ancient system Ruby
// (2.6) whose bundler can't even install modern git gems. So instead of
// blindly trusting `ruby` on PATH, we find a Ruby >= 3.0 — checking PATH
// first, then Homebrew, then rbenv — and use that one for both `bundle
// install` (deploy) and running functions locally (`drift atomic run`).
package atomic_common

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RubyToolchain is a discovered host Ruby >= 3.0 and its matching bundler.
type RubyToolchain struct {
	Ruby    string // absolute path to the ruby binary
	Bundle  string // absolute path to the matching bundle binary
	BinDir  string // directory holding both (prepend to PATH for child procs)
	Version string // e.g. "4.0.2"
}

// FindRuby returns the best host Ruby >= 3.0. It prefers a Ruby already on
// PATH, then well-known Homebrew prefixes, then rbenv. Apple's system Ruby
// (2.6) is skipped by the version gate. No version is ever pinned.
func FindRuby() (RubyToolchain, error) {
	var seen []string

	pick := func(ruby string) *RubyToolchain {
		if ruby == "" {
			return nil
		}
		out, err := exec.Command(ruby, "-e", "print RUBY_VERSION").Output() // #nosec G204 -- ruby path is from PATH/Homebrew/rbenv discovery, not user input
		if err != nil {
			return nil
		}
		ver := strings.TrimSpace(string(out))
		seen = append(seen, fmt.Sprintf("%s=%s", ruby, ver))
		if !rubyAtLeast3(ver) {
			return nil
		}
		dir := filepath.Dir(ruby)
		bundle := filepath.Join(dir, "bundle")
		if _, statErr := os.Stat(bundle); statErr != nil {
			p, lerr := exec.LookPath("bundle")
			if lerr != nil {
				return nil
			}
			bundle = p
		}
		return &RubyToolchain{Ruby: ruby, Bundle: bundle, BinDir: dir, Version: ver}
	}

	candidates := []string{}
	if p, err := exec.LookPath("ruby"); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, homebrewRubies()...)
	candidates = append(candidates, rbenvRubies()...)

	for _, c := range candidates {
		if tc := pick(c); tc != nil {
			return *tc, nil
		}
	}

	hint := "Drift runs Ruby functions with your host Ruby — install Ruby 3.0+ (e.g. `brew install ruby`, rbenv, or your package manager)."
	if len(seen) > 0 {
		return RubyToolchain{}, fmt.Errorf("no Ruby >= 3.0 found (checked: %s). %s", strings.Join(seen, ", "), hint)
	}
	return RubyToolchain{}, fmt.Errorf("no Ruby found on this system. %s", hint)
}

func rubyAtLeast3(v string) bool {
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return err == nil && major >= 3
}

// homebrewRubies returns candidate ruby binaries from Homebrew installs,
// covering both Apple-silicon (/opt/homebrew) and Intel (/usr/local) prefixes
// and every keg (ruby, ruby@4, …), newest first.
func homebrewRubies() []string {
	var out []string
	if brew, err := exec.LookPath("brew"); err == nil {
		if p, e := exec.Command(brew, "--prefix", "ruby").Output(); e == nil { // #nosec G204
			out = append(out, filepath.Join(strings.TrimSpace(string(p)), "bin", "ruby"))
		}
	}
	for _, base := range []string{"/opt/homebrew/opt", "/usr/local/opt"} {
		matches, _ := filepath.Glob(filepath.Join(base, "ruby*", "bin", "ruby"))
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		out = append(out, matches...)
	}
	return out
}

// rbenvRubies returns installed rbenv ruby binaries, highest version first.
func rbenvRubies() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".rbenv", "versions", "*", "bin", "ruby"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	return matches
}
