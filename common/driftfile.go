// driftfile.go — keeping this machine's copy of the Driftfile format in step
// with the platform's.
//
// The platform owns the format and serves it from
// https://api.ondrift.eu/driftfile/schema. The CLI ships NO copy of it: a
// schema compiled into the binary is a second source of truth that goes stale
// the moment the platform moves, and the whole point of serving the format is
// that there is exactly one definition of it.
//
// So the schema arrives the first time this CLI talks to the platform for any
// reason, and is stored at ~/.drift/driftfile.schema.json. From then on:
//
//   - platform reachable   → fetch and overwrite the local copy, so the format
//     is never older than the last time this machine was online;
//   - platform unreachable → use the local copy, so being offline costs
//     freshness and nothing else.
//
// # The one consequence worth stating
//
// A CLI that has NEVER reached the platform has no schema and cannot validate
// a Driftfile. That is honest rather than a gap: a binary that has never met
// the platform has no basis for claiming what the format is, and inventing one
// is exactly how the two drifted apart before. One online command — any
// command — fixes it permanently.
//
// # Cost
//
// The refresh is a conditional request. The server derives its ETag from the
// format version, so a machine already holding the current schema pays one 304
// with an empty body rather than a re-download. That is what makes "refresh
// whenever we talk to the platform" affordable instead of chatty.
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// driftfileRefreshOnce bounds the refresh to a single attempt per invocation.
// "Whenever we call the API" is the intent; once per PROCESS is the honest
// implementation — a single `drift project deploy` makes many HTTP calls, and
// paying a schema round-trip on each would be latency nobody asked for.
var driftfileRefreshOnce sync.Once

// DriftfileSchemaPath is where this machine's copy lives — beside the session,
// under the user's own dot-dir.
func DriftfileSchemaPath() (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("HOME is not set, so there is nowhere to keep the Driftfile schema")
	}
	return filepath.Join(home, ".drift", "driftfile.schema.json"), nil
}

// RefreshDriftfileSchema brings the local copy up to date, best-effort. Called
// automatically on the first API contact of each invocation.
//
// Every failure is silent by design: this runs alongside whatever the user
// actually asked for, and a schema refresh must never be the reason a deploy
// or a login appears to fail.
func RefreshDriftfileSchema() {
	driftfileRefreshOnce.Do(refreshDriftfileSchema)
}

func refreshDriftfileSchema() {
	req, err := http.NewRequest(http.MethodGet, APIBaseURL+"/driftfile/schema", nil)
	if err != nil {
		return
	}
	// The server's ETag is derived from the format version, so the version we
	// already hold reconstructs it — no sidecar file to keep in step.
	if v := DriftfileSchemaVersion(); v != "" {
		req.Header.Set("If-None-Match", `"driftfile-`+v+`"`)
	}

	// Short. This is a side errand attached to someone else's command.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Already current — the common case, and it costs an empty body.
	if resp.StatusCode == http.StatusNotModified || resp.StatusCode != http.StatusOK {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return
	}
	// Only overwrite with something that is actually a versioned schema.
	// Replacing a good local copy with whatever a captive portal returned
	// would turn "offline" into "corrupted".
	if driftfileVersionOf(raw) == "" {
		return
	}
	storeDriftfileSchema(raw)
}

func storeDriftfileSchema(raw []byte) {
	p, err := DriftfileSchemaPath()
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	// Write-then-rename: a schema half-written by an interrupted command is
	// worse than no schema, because it fails at parse time rather than at a
	// point anyone can act on.
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// LocalDriftfileSchema returns this machine's schema, or an error naming what
// to do about it.
func LocalDriftfileSchema() ([]byte, error) {
	p, err := DriftfileSchemaPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p) // #nosec G304 — a path this process wrote, under the user's own home
	if err != nil {
		return nil, fmt.Errorf(
			"no Driftfile schema on this machine yet.\n" +
				"The format is served by the platform and fetched the first time the CLI reaches it — " +
				"run any command while online (`drift slice list` will do) and it will be here from then on")
	}
	return raw, nil
}

// DriftfileSchemaVersion reports the format version this machine holds, or ""
// if it has never been fetched.
func DriftfileSchemaVersion() string {
	raw, err := LocalDriftfileSchema()
	if err != nil {
		return ""
	}
	return driftfileVersionOf(raw)
}

// DriftfileSchemaVersionOrNone renders the version for humans, including the
// case where there is not one yet.
func DriftfileSchemaVersionOrNone() string {
	if v := DriftfileSchemaVersion(); v != "" {
		return v
	}
	return "not fetched yet — run any command while online"
}

func driftfileVersionOf(raw []byte) string {
	var doc struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return doc.Version
}

// DriftfileMajor returns the leading semver component, the only part that
// decides whether a client can read a file at all.
func DriftfileMajor(v string) int {
	n, err := strconv.Atoi(strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0])
	if err != nil {
		return 0
	}
	return n
}
