package common

// Release-update check. The drift CLI is distributed via `go install`, so a
// "new version" is just the latest GitHub release tag of the CLI repo. This is
// shared by `drift upgrade` (resolves "latest", compares) and the dashboard
// (shows an unobtrusive "update available" banner).

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

// LatestRelease is the salient subset of a GitHub "latest release".
type LatestRelease struct {
	Tag string // e.g. "v1.11.0"
	URL string // html_url — the release notes page
}

// FetchLatestCLIRelease asks GitHub for the CLI's latest published release.
// Failures are returned to the caller: the dashboard swallows them (the banner
// just stays hidden); `drift upgrade` surfaces them.
func FetchLatestCLIRelease() (LatestRelease, error) {
	url := "https://api.github.com/repos/" + CLIRepo + "/releases/latest"
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
	if resp.StatusCode == http.StatusNotFound {
		return LatestRelease{}, fmt.Errorf("no published releases yet")
	}
	if resp.StatusCode != http.StatusOK {
		return LatestRelease{}, fmt.Errorf("GitHub returned %d", resp.StatusCode)
	}

	var body struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return LatestRelease{}, err
	}
	if body.Tag == "" {
		return LatestRelease{}, fmt.Errorf("latest release has no tag")
	}
	return LatestRelease{Tag: body.Tag, URL: body.URL}, nil
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
