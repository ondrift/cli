package project

// translate.go converts a parsed Driftfile (Manifest) into the
// SliceConfig wire shape the platform's pricing + resize APIs
// understand. This is the single source of truth for "what does this
// Driftfile mean in the platform's resource taxonomy?".
//
// Two kinds of translation happen here:
//
//   1. Resource counts: count the lists in the Manifest and write
//      them into the corresponding "Max…" fields. The user never
//      writes counts in the Driftfile — they're always derived.
//
//   2. Envelope knobs: parse the duration / size / rate strings into
//      the int fields the platform stores. Empty strings (omitted
//      knobs) leave the corresponding field at zero, signalling
//      "use the platform default" to downstream code.
//
// What this file does NOT do:
//
//   - Reach over the network (the Driftfile alone is enough).
//   - Apply Hacker-envelope defaults to omitted knobs. The platform's
//     pricing endpoint and configurator already do that; calling it
//     here would double up.
//
// SliceConfig types are CLI-local mirrors of the platform's
// drift-common/models.SliceConfig wire shape. The CLI is intentionally
// self-contained (no drift-common dependency) so it can be `go install`ed
// without pulling in core code.

import (
	"fmt"
	"strconv"
	"strings"
)

// SliceConfig mirrors the JSON wire shape of drift-common/models.SliceConfig.
// Field names match the platform's encoded shape exactly: parent fields
// live under lowercase JSON keys (canvas, atomic, backbone, secrets, blobs,
// nosql, queues, locks); leaf fields use the Go field names because the
// platform's models package omits json tags on those.
type SliceConfig struct {
	Canvas   CanvasLimits   `json:"canvas"`
	Atomic   AtomicLimits   `json:"atomic"`
	Backbone BackboneLimits `json:"backbone"`
}

type CanvasLimits struct {
	TotalMaxSizeInBytes int
}

type AtomicLimits struct {
	MaxNumberOfFunctions            int
	MaxFunctionRuntimeInSeconds     int
	MaxNumberOfDeploymentsInHistory int
	MaxNumberOfHoursForLogRetention int
	MaxNumberOfRequestsPerMinute    int
	MaxNumberOfScheduledJobs        int
	MaxFunctionMemoryBytes          int
}

type BackboneLimits struct {
	Secrets  BackboneSecretsLimits  `json:"secrets"`
	Blobs    BackboneBlobsLimits    `json:"blobs"`
	NoSQL    BackboneNoSQLLimits    `json:"nosql"`
	SQL      BackboneSQLLimits      `json:"sql"`
	Queues   BackboneQueuesLimits   `json:"queues"`
	Realtime BackboneRealtimeLimits `json:"realtime"`
	Locks    BackboneLocksLimits    `json:"locks"`
	// BackupRetentionDays needs its json tag — the platform model tags this
	// leaf "backup_retention_days" (unlike the other Go-named leaves), so
	// without it the value marshals as "BackupRetentionDays" and the server
	// silently reads 0.
	BackupRetentionDays int `json:"backup_retention_days"`
}

type BackboneSQLLimits struct {
	MaxDatabases    int
	MaxStorageBytes int
}

type BackboneSecretsLimits struct {
	MaxCount           int
	MaxSizeInBytesEach int
}

type BackboneBlobsLimits struct {
	MaxCount           int
	MaxSizeInBytesEach int
}

type BackboneNoSQLLimits struct {
	MaxCollections  int
	MaxStorageBytes int
}

type BackboneQueuesLimits struct {
	MaxQueues    int
	MaxDepthEach int
}

type BackboneLocksLimits struct {
	MaxConcurrent int
}

type BackboneRealtimeLimits struct {
	MaxConcurrentConnections int
}

// ManifestToSliceConfig builds a SliceConfig from a parsed Driftfile.
// Unset envelope knobs leave the corresponding field at zero, which
// the platform reads as "use the slice envelope's default for this
// field" (the same convention the configurator uses today).
//
// Returns a translation-error block if any envelope knob fails to
// parse despite passing the upstream Driftfile validation. That
// would be an internal bug, not user error, but we surface it
// rather than swallowing it.
func ManifestToSliceConfig(m *Manifest) (SliceConfig, error) {
	var (
		cfg  SliceConfig
		errs []string
	)

	// ── Resource counts ─────────────────────────────────────────────
	// An Atomic function is a callable in user source with an `@atomic`
	// annotation directly above it. Walk every directory listed under
	// atomic.functions in the Driftfile and count decorated callables.
	// Helpers (no annotation) don't count.
	if hc, hcErr := CountAtomicFunctions(m); hcErr == nil {
		cfg.Atomic.MaxNumberOfFunctions = hc
	} else {
		// Source not readable (e.g. manifest preflight before deploy);
		// fall back to one function per directory entry.
		cfg.Atomic.MaxNumberOfFunctions = len(m.Slice.Atomic.Functions)
	}

	// Scheduled-job count comes from the source `@atomic cron=` directives —
	// the authoritative place a schedule is declared — not the vestigial
	// Driftfile functions[].cron field. Falls back to that field only when
	// source isn't readable (manifest preflight before deploy), mirroring
	// the function-count fallback above.
	if sc, scErr := CountScheduledFunctions(m); scErr == nil {
		cfg.Atomic.MaxNumberOfScheduledJobs = sc
	} else {
		scheduled := 0
		for _, fn := range m.Slice.Atomic.Functions {
			if fn.Cron != "" {
				scheduled++
			}
		}
		cfg.Atomic.MaxNumberOfScheduledJobs = scheduled
	}
	if v := m.Slice.Atomic.DeployHistory; v > 0 {
		cfg.Atomic.MaxNumberOfDeploymentsInHistory = v
	}

	cfg.Backbone.NoSQL.MaxCollections = len(m.Slice.Backbone.NoSQL)
	cfg.Backbone.SQL.MaxDatabases = len(m.Slice.Backbone.SQL)
	cfg.Backbone.Queues.MaxQueues = len(m.Slice.Backbone.Queues)
	cfg.Backbone.Secrets.MaxCount = len(m.Slice.Backbone.Secrets)

	// Realtime is a scalar knob (a connection budget), not a list of named
	// resources — declared directly. Omitted → 0 → realtime disabled.
	cfg.Backbone.Realtime.MaxConcurrentConnections = m.Slice.Backbone.RealtimeConnections

	// ── Atomic envelope knobs ───────────────────────────────────────
	if v := m.Slice.Atomic.FunctionMemory; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.atomic.function_memory: %v", err))
		} else {
			cfg.Atomic.MaxFunctionMemoryBytes = bytes
		}
	}
	if v := m.Slice.Atomic.FunctionTimeout; v != "" {
		secs, err := parseDurationSeconds(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.atomic.function_timeout: %v", err))
		} else {
			cfg.Atomic.MaxFunctionRuntimeInSeconds = secs
		}
	}
	if v := m.Slice.Atomic.RateLimit; v != "" {
		rpm, err := parseRatePerMinute(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.atomic.rate_limit: %v", err))
		} else {
			cfg.Atomic.MaxNumberOfRequestsPerMinute = rpm
		}
	}

	// ── Backbone envelope knobs ────────────────────────────────────
	if v := m.Slice.Backbone.NoSQLStorage; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.backbone.nosql_storage: %v", err))
		} else {
			cfg.Backbone.NoSQL.MaxStorageBytes = bytes
		}
	}
	if v := m.Slice.Backbone.BlobMaxSize; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.backbone.blob_max_size: %v", err))
		} else {
			cfg.Backbone.Blobs.MaxSizeInBytesEach = bytes
		}
	}
	if v := m.Slice.Backbone.BlobMaxCount; v > 0 {
		cfg.Backbone.Blobs.MaxCount = v
	}
	if v := m.Slice.Backbone.QueueMaxDepth; v > 0 {
		cfg.Backbone.Queues.MaxDepthEach = v
	}
	if v := m.Slice.Backbone.SQLStorage; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.backbone.sql_storage: %v", err))
		} else {
			cfg.Backbone.SQL.MaxStorageBytes = bytes
		}
	}
	if v := m.Slice.Backbone.SecretMaxSize; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.backbone.secret_max_size: %v", err))
		} else {
			cfg.Backbone.Secrets.MaxSizeInBytesEach = bytes
		}
	}
	if v := m.Slice.Backbone.Locks; v > 0 {
		cfg.Backbone.Locks.MaxConcurrent = v
	}

	// ── Canvas envelope ─────────────────────────────────────────────
	if v := m.Slice.Canvas.CanvasSize; v != "" {
		bytes, err := parseSizeBytes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.canvas.canvas_size: %v", err))
		} else {
			cfg.Canvas.TotalMaxSizeInBytes = bytes
		}
	}

	// ── Operational ─────────────────────────────────────────────────
	if v := m.Slice.LogRetention; v != "" {
		hours, err := parseDurationHours(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.log_retention: %v", err))
		} else {
			cfg.Atomic.MaxNumberOfHoursForLogRetention = hours
		}
	}
	if v := m.Slice.BackupRetention; v != "" {
		days, err := parseDurationDays(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("slice.backup_retention: %v", err))
		} else {
			cfg.Backbone.BackupRetentionDays = days
		}
	}

	if len(errs) > 0 {
		return cfg, fmt.Errorf("translation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// parseSizeBytes turns "64MB", "1GB", "500KB" into a byte count.
// The Driftfile parser already validates the format; this is a
// belt-and-braces re-check that fails noisily if someone bypasses it.
func parseSizeBytes(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"KB", 1024},
		{"MB", 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with KB/MB/GB suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in KB, MB, or GB", s)
}

// parseDurationSeconds turns "30s", "5m", "1h" into a number of seconds.
// Days are not allowed here — function timeouts that long are nonsense.
func parseDurationSeconds(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"s", 1},
		{"m", 60},
		{"h", 60 * 60},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, or h", s)
}

// parseDurationHours turns a `<int><s|m|h|d>` string into hours,
// rounded down. Used by log retention which is stored as integer hours.
func parseDurationHours(s string) (int, error) {
	for _, suf := range []struct {
		ext   string
		scale int
	}{
		{"s", 0}, // sub-hour values truncate to 0
		{"m", 0}, // sub-hour values truncate to 0
		{"h", 1},
		{"d", 24},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h/d suffix", s)
			}
			return n * suf.scale, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, h, or d", s)
}

// parseDurationDays turns a `<int><s|m|h|d>` string into days,
// rounded down. Used by backup retention which is stored as integer days.
func parseDurationDays(s string) (int, error) {
	for _, suf := range []struct {
		ext  string
		secs int
	}{
		{"s", 1},
		{"m", 60},
		{"h", 60 * 60},
		{"d", 60 * 60 * 24},
	} {
		if strings.HasSuffix(s, suf.ext) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, suf.ext))
			if err != nil {
				return 0, fmt.Errorf("%q is not an integer with s/m/h/d suffix", s)
			}
			totalSecs := n * suf.secs
			days := totalSecs / (60 * 60 * 24)
			return days, nil
		}
	}
	return 0, fmt.Errorf("%q must end in s, m, h, or d", s)
}

// parseRatePerMinute turns "1000/min", "10/s", "60000/h" into requests
// per minute. The platform stores rate limits in per-minute granularity
// regardless of the Driftfile's expressed unit.
func parseRatePerMinute(s string) (int, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("%q must be of the form <integer>/<s|min|h>", s)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%q has a non-integer count", s)
	}
	switch parts[1] {
	case "s":
		return n * 60, nil
	case "min":
		return n, nil
	case "h":
		return n / 60, nil
	default:
		return 0, fmt.Errorf("%q must end in /s, /min, or /h", s)
	}
}
