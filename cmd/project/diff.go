package project

// diff.go is the reconcile core. It compares a Driftfile-derived
// SliceConfig against the live slice (or none, for a fresh slice)
// and classifies the divergence into one of four buckets:
//
//   Create  — the slice doesn't exist yet; deploy will provision it.
//   Match   — declared shape == live shape; nothing to change.
//   Grow    — declared shape exceeds live in one or more fields;
//             deploy will resize via the platform's resize API.
//   Abort   — declared shape is *smaller* than live in one or more
//             fields. Deploy refuses; the user must explicitly run
//             `drift slice resize --from Driftfile --allow-destructive`.
//
// The load-bearing rule: deploy never shrinks a slice. The destructive
// path has its own named verb.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Verdict is the four-way classifier described above.
type Verdict int

const (
	VerdictCreate Verdict = iota
	VerdictMatch
	VerdictGrow
	VerdictAbort
)

func (v Verdict) String() string {
	switch v {
	case VerdictCreate:
		return "create"
	case VerdictMatch:
		return "match"
	case VerdictGrow:
		return "grow"
	case VerdictAbort:
		return "abort"
	}
	return "unknown"
}

// FieldDelta records one resource/envelope dimension that changed.
type FieldDelta struct {
	Path      string // human-readable path, e.g. "atomic.functions" or "backbone.nosql.<name>"
	Live      int    // current value on the live slice (0 if Create)
	Wanted    int    // value the manifest declares
	IsBytes   bool   // render as a size string instead of a bare integer
	IsTime    bool   // render as a duration (seconds) — applies to function_timeout
	IsHours   bool   // render as a duration (hours) — applies to log_retention
	IsDays    bool   // render as a duration (days) — applies to backup_retention
	Omittable bool   // Wanted==0 means "the Driftfile didn't declare this knob", not "shrink it to zero"
}

// Delta returns Wanted - Live; positive means grow, negative means shrink.
func (f FieldDelta) Delta() int { return f.Wanted - f.Live }

// DiffResult is the structured output of Diff(). Render it with
// RenderDiff to get the user-facing prompt block.
type DiffResult struct {
	Verdict         Verdict
	SliceName       string
	IsNewSlice      bool
	Grows           []FieldDelta // fields the manifest wants larger than live
	Shrinks         []FieldDelta // fields the manifest wants smaller than live (only set on Abort)
	LiveCostCents   int          // monthly cost of the live slice (0 if Create)
	WantedCostCents int          // monthly cost of the manifest's declared shape
	WantedItems     []LineItem   // itemised breakdown backing WantedCostCents (server-computed)
}

// Diff compares the manifest-derived SliceConfig against the live
// SliceConfig and returns the verdict + per-field deltas. liveCfg
// must be a pointer; nil means "the slice doesn't exist yet" → Create.
// Callers that have the server's itemised price breakdown (PriceConfig's
// second return value) should set the result's WantedItems field
// themselves afterward — Diff doesn't take it directly so existing callers
// (and tests) that don't care about display don't need to change.
func Diff(sliceName string, manifest SliceConfig, liveCfg *SliceConfig, liveCostCents, wantedCostCents int) DiffResult {
	if liveCfg == nil {
		// Create — every non-zero field in manifest is a grow.
		grows := compareFields(SliceConfig{}, manifest, true)
		return DiffResult{
			Verdict:         VerdictCreate,
			SliceName:       sliceName,
			IsNewSlice:      true,
			Grows:           grows,
			LiveCostCents:   0,
			WantedCostCents: wantedCostCents,
		}
	}

	deltas := compareFields(*liveCfg, manifest, false)
	var grows, shrinks []FieldDelta
	for _, d := range deltas {
		if d.Delta() > 0 {
			grows = append(grows, d)
		} else if d.Delta() < 0 {
			shrinks = append(shrinks, d)
		}
	}

	verdict := VerdictMatch
	switch {
	case len(shrinks) > 0:
		verdict = VerdictAbort
	case len(grows) > 0:
		verdict = VerdictGrow
	}

	return DiffResult{
		Verdict:         verdict,
		SliceName:       sliceName,
		Grows:           grows,
		Shrinks:         shrinks,
		LiveCostCents:   liveCostCents,
		WantedCostCents: wantedCostCents,
	}
}

// perItemDeltas builds one FieldDelta per name appearing in EITHER map — a
// collection/database/bucket declared with its own `size` now, not a single
// slice-wide envelope. Deliberately NOT Omittable: unlike the old blanket
// scalar (where Wanted==0 meant "the Driftfile didn't mention this knob"),
// a name missing from `wanted` here means the Driftfile stopped declaring
// that specific item — a real shrink (its quota goes to 0) that the
// abort-on-shrink gate must catch, not silently ignore. Sorted by name for
// deterministic output.
func perItemDeltas(prefix string, live, wanted map[string]int) []FieldDelta {
	seen := make(map[string]bool, len(live)+len(wanted))
	for name := range live {
		seen[name] = true
	}
	for name := range wanted {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]FieldDelta, 0, len(names))
	for _, name := range names {
		out = append(out, FieldDelta{
			Path:    fmt.Sprintf("%s.%s", prefix, name),
			Live:    live[name],
			Wanted:  wanted[name],
			IsBytes: true,
		})
	}
	return out
}

// compareFields enumerates every declared dimension and returns the
// non-zero / non-equal ones. When includeZeroLive is true (Create
// path), every field with a positive Wanted produces a delta
// regardless of Live; otherwise only fields where Live != Wanted
// produce a delta — EXCEPT Omittable fields, where Wanted==0 means
// "the Driftfile didn't mention this knob" (translate.go's own
// documented convention), not "shrink it to zero". An omitted knob
// must never diff against whatever the live slice already has,
// or a Driftfile that simply doesn't repeat every envelope value
// looks like a shrink of a slice that never actually changed.
func compareFields(live, wanted SliceConfig, includeZeroLive bool) []FieldDelta {
	pairs := []FieldDelta{
		// Real derived counts — 0 is a meaningful, deliberate value
		// (e.g. "this project genuinely declares zero secrets"), so these
		// are never Omittable.
		{Path: "atomic.functions", Live: live.Atomic.MaxNumberOfFunctions, Wanted: wanted.Atomic.MaxNumberOfFunctions},
		{Path: "atomic.scheduled_jobs", Live: live.Atomic.MaxNumberOfScheduledJobs, Wanted: wanted.Atomic.MaxNumberOfScheduledJobs},
		{Path: "backbone.nosql_collections", Live: live.Backbone.NoSQL.MaxCollections, Wanted: wanted.Backbone.NoSQL.MaxCollections},
		{Path: "backbone.queues", Live: live.Backbone.Queues.MaxQueues, Wanted: wanted.Backbone.Queues.MaxQueues},
		{Path: "backbone.secrets", Live: live.Backbone.Secrets.MaxCount, Wanted: wanted.Backbone.Secrets.MaxCount},

		// Envelope knobs — translate.go leaves these at 0 whenever the
		// Driftfile omits them, specifically so downstream code treats
		// zero as "inherit the platform default", not a declared value.
		{Path: "atomic.function_memory", Live: live.Atomic.MaxFunctionMemoryBytes, Wanted: wanted.Atomic.MaxFunctionMemoryBytes, IsBytes: true, Omittable: true},
		{Path: "atomic.function_timeout", Live: live.Atomic.MaxFunctionRuntimeInSeconds, Wanted: wanted.Atomic.MaxFunctionRuntimeInSeconds, IsTime: true, Omittable: true},
		{Path: "atomic.rate_limit_per_minute", Live: live.Atomic.MaxNumberOfRequestsPerMinute, Wanted: wanted.Atomic.MaxNumberOfRequestsPerMinute, Omittable: true},
		{Path: "atomic.log_retention", Live: live.Atomic.MaxNumberOfHoursForLogRetention, Wanted: wanted.Atomic.MaxNumberOfHoursForLogRetention, IsHours: true, Omittable: true},
		{Path: "backbone.queue_max_depth", Live: live.Backbone.Queues.MaxDepthEach, Wanted: wanted.Backbone.Queues.MaxDepthEach, Omittable: true},
		{Path: "backbone.blob_max_size", Live: live.Backbone.Blobs.MaxSizeInBytesEach, Wanted: wanted.Backbone.Blobs.MaxSizeInBytesEach, IsBytes: true, Omittable: true},
		{Path: "backbone.blob_max_count", Live: live.Backbone.Blobs.MaxCount, Wanted: wanted.Backbone.Blobs.MaxCount, Omittable: true},
		{Path: "backbone.backup_retention", Live: live.Backbone.BackupRetentionDays, Wanted: wanted.Backbone.BackupRetentionDays, IsDays: true, Omittable: true},
		{Path: "canvas.max_size", Live: live.Canvas.TotalMaxSizeInBytes, Wanted: wanted.Canvas.TotalMaxSizeInBytes, IsBytes: true, Omittable: true},
	}
	pairs = append(pairs, perItemDeltas("backbone.nosql", live.Backbone.NoSQL.Collections, wanted.Backbone.NoSQL.Collections)...)
	pairs = append(pairs, perItemDeltas("backbone.sql", live.Backbone.SQL.Databases, wanted.Backbone.SQL.Databases)...)
	pairs = append(pairs, perItemDeltas("backbone.blobs", live.Backbone.Blobs.Buckets, wanted.Backbone.Blobs.Buckets)...)
	out := []FieldDelta{}
	for _, p := range pairs {
		if includeZeroLive {
			if p.Wanted > 0 {
				out = append(out, p)
			}
			continue
		}
		if p.Omittable && p.Wanted == 0 {
			continue // Driftfile didn't declare this knob — not a shrink
		}
		if p.Live != p.Wanted {
			out = append(out, p)
		}
	}
	return out
}

// RenderDiff produces the user-facing block for `drift deploy --plan`
// and the cost-confirm prompt. Wording matches the spec's reconcile-
// rule examples exactly.
func RenderDiff(d DiffResult) string {
	var sb strings.Builder

	switch d.Verdict {
	case VerdictCreate:
		fmt.Fprintf(&sb, "Slice %q does not exist. Will create:\n", d.SliceName)
		for _, g := range d.Grows {
			fmt.Fprintf(&sb, "    %s: %s\n", g.Path, formatValue(g, g.Wanted))
		}
		sb.WriteString("\n")
		sb.WriteString(renderLineItems(d.WantedItems))
		sb.WriteString("  ")
		sb.WriteString(formatCostLine(d.WantedCostCents, true))
		sb.WriteString("\n")

	case VerdictMatch:
		fmt.Fprintf(&sb, "Slice %q matches the manifest. No changes.\n", d.SliceName)

	case VerdictGrow:
		fmt.Fprintf(&sb, "Slice %q needs to grow:\n", d.SliceName)
		for _, g := range d.Grows {
			fmt.Fprintf(&sb, "    %s   %s → %s   %s\n",
				g.Path,
				formatValue(g, g.Live),
				formatValue(g, g.Wanted),
				formatPositive(g.Delta()))
		}
		sb.WriteString("\n")
		sb.WriteString(renderLineItems(d.WantedItems))
		sb.WriteString("  ")
		sb.WriteString(formatCostChange(d.LiveCostCents, d.WantedCostCents))
		sb.WriteString("\n")

	case VerdictAbort:
		fmt.Fprintf(&sb, "✘ Refusing to deploy.\n\n")
		fmt.Fprintf(&sb, "  Slice %q is larger than the manifest declares:\n", d.SliceName)
		for _, s := range d.Shrinks {
			fmt.Fprintf(&sb, "    %s   %s (current) > %s (declared)\n",
				s.Path,
				formatValue(s, s.Live),
				formatValue(s, s.Wanted))
		}
		sb.WriteString("\n  drift deploy will never shrink a slice. Shrinking deletes\n")
		sb.WriteString("  data that the manifest cannot know about.\n\n")
		sb.WriteString("  To apply the manifest's shape including shrinks:\n")
		sb.WriteString("    drift slice resize --from Driftfile --allow-destructive\n\n")
		sb.WriteString("  To leave the slice's shape alone and deploy code only:\n")
		sb.WriteString("    drift project deploy --no-slice-reconcile\n")
	}

	return sb.String()
}

// renderLineItems formats the server's itemised price breakdown, one line
// per priced item: "label   quantity x €unit = €subtotal". Items with
// UnitCents==0 are informational-only (things included at no charge, e.g.
// NoSQL collections) — the pricing engine returns them so the browser
// configurator can show "here's what you get", but a terminal cost-confirm
// prompt shouldn't dump a run of €0.00 lines alongside the ones that
// actually cost money, so they're filtered out here. Returns "" if there's
// nothing to show (no items, or all of them free).
func renderLineItems(items []LineItem) string {
	var sb strings.Builder
	for _, it := range items {
		if it.UnitCents == 0 {
			continue
		}
		// bb_storage is the one line item whose Quantity (MiB, for a
		// readable small number) doesn't match its own Label/UnitCents
		// (per GiB) — displaying it via the generic "quantity x unit"
		// format reads as "50 x €0.25 = €0.01", which looks like broken
		// math (and worse, like 50 GiB for a cent) unless you already know
		// Quantity is secretly MiB. Show the same GiB unit as the label
		// and rate here instead; SubtotalCent (already byte-accurate)
		// doesn't change.
		if it.Key == "bb_storage" {
			gib := float64(it.Quantity) / 1024
			fmt.Fprintf(&sb, "    %-24s %.4f x €%s = €%s\n", it.Label, gib, formatEuros(it.UnitCents), formatEuros(it.SubtotalCent))
			continue
		}
		fmt.Fprintf(&sb, "    %-24s %d x €%s = €%s\n", it.Label, it.Quantity, formatEuros(it.UnitCents), formatEuros(it.SubtotalCent))
	}
	return sb.String()
}

// formatValue renders a FieldDelta value according to its unit hints.
func formatValue(f FieldDelta, n int) string {
	if n == 0 {
		return "0"
	}
	switch {
	case f.IsBytes:
		return formatBytes(n)
	case f.IsTime:
		return formatSeconds(n)
	case f.IsHours:
		if n%24 == 0 {
			return fmt.Sprintf("%dd", n/24)
		}
		return fmt.Sprintf("%dh", n)
	case f.IsDays:
		return fmt.Sprintf("%dd", n)
	}
	return fmt.Sprintf("%d", n)
}

func formatBytes(n int) string {
	const (
		KB = 1024
		MB = 1024 * 1024
		GB = 1024 * 1024 * 1024
	)
	switch {
	case n%GB == 0:
		return fmt.Sprintf("%dGB", n/GB)
	case n%MB == 0:
		return fmt.Sprintf("%dMB", n/MB)
	case n%KB == 0:
		return fmt.Sprintf("%dKB", n/KB)
	}
	return fmt.Sprintf("%dB", n)
}

func formatSeconds(n int) string {
	switch {
	case n%3600 == 0:
		return fmt.Sprintf("%dh", n/3600)
	case n%60 == 0:
		return fmt.Sprintf("%dm", n/60)
	}
	return fmt.Sprintf("%ds", n)
}

func formatPositive(delta int) string {
	if delta > 0 {
		return fmt.Sprintf("(+%d)", delta)
	}
	if delta < 0 {
		return fmt.Sprintf("(%d)", delta)
	}
	return ""
}

// formatCostLine renders the cost on the create or grow prompt.
// "This slice is free." vs "Cost: €N/month".
func formatCostLine(monthlyCents int, leadingNewlineHandled bool) string {
	if monthlyCents == 0 {
		return "This slice is free."
	}
	return fmt.Sprintf("Cost: €%s/month", formatEuros(monthlyCents))
}

// formatCostChange renders "Cost change: <before> → <after>" for the
// grow prompt. Crossing the free→paid boundary is always rendered
// explicitly with "free → €N/month".
func formatCostChange(liveCents, wantedCents int) string {
	if liveCents == wantedCents {
		if liveCents == 0 {
			return "This slice is free."
		}
		return fmt.Sprintf("Cost: €%s/month (unchanged)", formatEuros(liveCents))
	}
	before := "free"
	if liveCents > 0 {
		before = "€" + formatEuros(liveCents) + "/mo"
	}
	after := "free"
	if wantedCents > 0 {
		after = "€" + formatEuros(wantedCents) + "/mo"
	}
	return fmt.Sprintf("Cost change:    %s → %s", before, after)
}

func formatEuros(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("%d", cents/100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// JSON helpers — small wrappers used by the api client. Kept here
// so the diff package is self-contained.

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode JSON: %v", err))
	}
	return b
}
