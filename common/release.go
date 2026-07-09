package common

// Release-update check. The drift CLI is distributed via `go install` and
// versioned with a plain `git tag` + `git push` — no GitHub "Release" object
// is ever created (that's a separate GitHub feature layered on top of tags,
// not something a tag push produces on its own). So a "new version" is
// resolved from the repo's pushed tags (GET /repos/{repo}/tags — a plain,
// unauthenticated GitHub REST call, no token needed), not from
// /releases/latest, which would only ever see a tag someone manually
// promoted to a Release through the GitHub UI/API. This is shared by
// `drift upgrade` (resolves "latest", compares) and the dashboard (shows an
// unobtrusive "update available" banner).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CLIRepo is the GitHub owner/repo the drift CLI is released from.
const CLIRepo = "ondrift/cli"

// CLIModulePath is the go-installable path of the drift binary.
const CLIModulePath = "github.com/ondrift/cli/cmd/drift"

// LatestRelease is the salient subset of the CLI's latest pushed version tag.
type LatestRelease struct {
	Tag string // e.g. "v1.11.0"
	URL string // best-effort link to see what's in it
}

// FetchLatestCLIRelease asks GitHub for the CLI's latest pushed version tag.
// Failures are returned to the caller: the dashboard swallows them (the banner
// just stays hidden); `drift upgrade` surfaces them.
func FetchLatestCLIRelease() (LatestRelease, error) {
	url := "https://api.github.com/repos/" + CLIRepo + "/tags?per_page=100"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return LatestRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return LatestRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return LatestRelease{}, fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}

	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return LatestRelease{}, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}

	best := latestSemverTag(names)
	if best == "" {
		return LatestRelease{}, fmt.Errorf("no version tags found")
	}
	return LatestRelease{
		Tag: best,
		URL: "https://github.com/" + CLIRepo + "/releases/tag/" + best,
	}, nil
}

// latestSemverTag returns the highest "vMAJOR.MINOR.PATCH" tag in names (any
// tag not shaped that way — a branch snapshot, a demo marker, etc. — is
// ignored), or "" if none match. Pulled out of FetchLatestCLIRelease so the
// selection logic is unit-testable without a network call.
func latestSemverTag(names []string) string {
	best := ""
	for _, name := range names {
		if !isSemverTag(name) {
			continue
		}
		if best == "" || CompareVersions(name, best) > 0 {
			best = name
		}
	}
	return best
}

// isSemverTag reports whether name is a plain "vMAJOR.MINOR.PATCH" version
// tag (the "v" is optional). Pre-release/build suffixes are deliberately NOT
// accepted here — unlike CompareVersions, which ignores them when comparing
// two already-known version strings, tag *selection* should only ever settle
// on a clean release tag.
func isSemverTag(name string) bool {
	v := strings.TrimPrefix(strings.TrimPrefix(name, "v"), "V")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// CompareVersions compares two "vMAJOR.MINOR.PATCH" strings. A leading "v" is
// optional, missing segments count as 0, and any pre-release/build suffix
// ("-rc1", "+meta") is ignored. Returns -1 if a<b, 0 if equal, +1 if a>b.
func CompareVersions(a, b string) int {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := range 3 {
		switch {
		case pa[i] < pb[i]:
			return -1
		case pa[i] > pb[i]:
			return 1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i] // drop pre-release/build metadata
	}
	var out [3]int
	for i, seg := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimSpace(seg))
		out[i] = n
	}
	return out
}
