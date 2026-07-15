package portal

// The Slice tab is the active slice's overview — its tier + live memory, then
// its snapshots (portable backups): create (c), download (w), delete (d).
// Create is async on the server; refresh (r) to see status flip to complete.
// Slice switching/creation lives in the sidebar (TAB), not here.

import (
	"fmt"
	"strings"

	sliceCmd "github.com/ondrift/cli/cmd/slice"
	common "github.com/ondrift/cli/common"
)

// handleSlice applies Slice-tab keys (snapshot actions); returns true if it
// consumed k. Unhandled keys fall through to the global handler.
func (m *model) handleSlice(k key) bool {
	switch k {
	case keyUp:
		if m.snapSel > 0 {
			m.snapSel--
		}
	case keyDown:
		if m.snapSel < len(m.snaps)-1 {
			m.snapSel++
		}
	case keySet: // 's' — snapshot (create); 'c' is now "configure"
		m.input = &inputPrompt{
			label:      "snapshot name (enter = auto):",
			allowEmpty: true,
			run: func(s string) string {
				if err := createSnapshot(s); err != nil {
					return "✗ " + err.Error()
				}
				return "snapshot started — press r to refresh"
			},
		}
	case keyCreate: // 'c' — configure (resize / upgrade) the slice
		m.openConfigure()
	case keyDownload:
		if m.snapSel < len(m.snaps) {
			s := m.snaps[m.snapSel]
			fname := snapFilename(s)
			if n, err := downloadSnapshot(s.ID, fname); err != nil {
				m.status = "✗ " + err.Error()
			} else {
				m.status = fmt.Sprintf("saved %s (%s)", fname, fmtBytes(n))
			}
		}
	case keyDelete:
		if m.snapSel < len(m.snaps) {
			s := m.snaps[m.snapSel]
			label := snapLabel(s)
			m.conf = &confirmAction{
				prompt: fmt.Sprintf("delete snapshot %s?", label),
				run: func() string {
					if err := deleteSnapshot(s.ID); err != nil {
						return "✗ " + err.Error()
					}
					return "deleted snapshot " + label
				},
			}
		}
	case keyEnter:
		// no-op (avoid an accidental large download); use 'w' to download
	default:
		return false
	}
	return true
}

// loadSnaps fetches snapshots for the active slice (lazy — on entering the view).
func (m *model) loadSnaps() {
	snaps, err := fetchSnapshots()
	if err != nil {
		m.loadErr[tabSlice] = err.Error()
		return
	}
	m.loadErr[tabSlice] = ""
	m.snaps = snaps
	if m.snapSel >= len(m.snaps) {
		m.snapSel = max(0, len(m.snaps)-1)
	}
}

// openConfigure opens the configurator pre-filled with the active slice's
// current settings, in resize mode (Apply changes → /ops/slice/resize).
func (m *model) openConfigure() {
	if m.cfg == nil {
		m.status = "✗ settings not loaded yet — press r"
		return
	}
	m.form = newResizeForm(m.cfg.Name, m.cfg.Config, m.cfg.BillingPeriodMonths)
	m.focus = focusMain
	m.recomputePrice()
}

// sliceSummaryCards is the at-a-glance headline above the census: the slice's
// identity (tier / cost / snapshots), a one-glance memory figure, and its
// footprint composition. The annotated census below elaborates the memory story.
func (m *model) sliceSummaryCards() []statCard {
	tier := "—"
	for _, s := range m.slices {
		if s.Name == m.active {
			tier = sliceCmd.TierLabel(s.Tier)
			break
		}
	}
	cost := "—"
	if m.cfg != nil {
		if m.cfg.MonthlyCostCents > 0 {
			cost = fmt.Sprintf("$%.2f", float64(m.cfg.MonthlyCostCents)/100)
		} else {
			cost = "free"
		}
	}
	slice := statCard{title: "slice", rows: [][2]string{
		{"tier", tier},
		{"cost", cost},
		{"snaps", fmt.Sprintf("%d", len(m.snaps))},
	}}

	rt := m.rt
	used, limit := "—", "—"
	usage := "—"
	// "used" is footprintBytes: your code's actual non-reclaimable memory —
	// NOT raw cgroup usage, which is dominated by page cache (your own
	// NoSQL/blob/SQL data sitting in RAM for speed) on anything that's
	// touched real data. Page cache costs nothing to hold, vanishes
	// instantly under pressure, and isn't something your code controls or
	// should be budgeted against — same category as the platform's own
	// invisible runtime-startup headroom for compiled functions. Neither
	// belongs in "how much of MY commitment am I using".
	usedBytes := rt.footprintBytes()
	used = mib(usedBytes)
	// "limit" is the Driftfile-declared function_memory — what the user
	// actually configured and is billed for — NOT the pod's real cgroup
	// ceiling. The platform pads the real ceiling with its own runtime
	// headroom (e.g. compiled functions get an invisible allowance to boot),
	// which the user never declared and was never meant to see; showing that
	// raw number here would just be confusing ("I said 64MB, why does it say
	// 960?"). Usage is measured against the same declared limit.
	if rt.FunctionMemoryLimitBytes > 0 {
		limit = mib(rt.FunctionMemoryLimitBytes)
		usage = fmt.Sprintf("%.0f%%", float64(usedBytes)/float64(rt.FunctionMemoryLimitBytes)*100)
	}
	memory := statCard{title: "memory", rows: [][2]string{
		{"used", used},
		{"limit", limit},
		{"usage", usage},
	}}
	footprint := statCard{title: "breakdown", rows: [][2]string{
		{"anon", mib(rt.AnonymousBytes)},
		{"file", mib(rt.FileBackedBytes)},
		{"cache", mib(rt.CgroupFileBytes)},
	}}
	return []statCard{slice, memory, footprint}
}

// renderSliceTab draws the active slice's overview + snapshots on the left and
// the current settings panel on the right. Slice switch/create is the sidebar's
// job; 's' snapshots, 'c' configures.
func (m *model) renderSliceTab(b *strings.Builder) {
	// At-a-glance summary cards, then the full memory census — the slice's real
	// OOM picture — at full width (moved here from Canvas: the Slice tab is its
	// home, and the census's long annotations don't fit the narrow left column
	// below). The cards headline; the census elaborates the memory story.
	if m.rt != nil {
		for _, ln := range statCards(m.contentW-2, 2, m.sliceSummaryCards()) {
			fmt.Fprintf(b, "  %s\r\n", ln)
		}
		b.WriteString("\r\n")
		m.renderMemoryCensus(b)
		b.WriteString("\r\n")
	}
	// Right column: the current settings. Left column: overview + snapshots.
	rightW := 24
	if rightW > m.contentW/2 {
		rightW = m.contentW / 2
	}
	leftW := m.contentW - rightW - 3
	if leftW < 20 {
		leftW = 20
	}

	var lb strings.Builder
	m.renderSliceOverview(&lb)
	left := strings.Split(strings.TrimRight(lb.String(), "\r\n"), "\r\n")
	right := m.settingsLines()

	n := max(len(left), len(right))
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		fmt.Fprintf(b, "%s   %s\r\n", pad(truncVis(l, leftW), leftW), r)
	}
}

// renderSliceOverview is the left column: name/tier header + snapshots (the full
// memory census renders full-width above this, in renderSliceTab).
func (m *model) renderSliceOverview(b *strings.Builder) {
	tier := ""
	for _, s := range m.slices {
		if s.Name == m.active {
			tier = sliceCmd.TierLabel(s.Tier)
			break
		}
	}
	fmt.Fprintf(b, "  \x1b[1m%s\x1b[0m", m.activeOrDash())
	if tier != "" {
		fmt.Fprintf(b, "    \x1b[2mtier\x1b[0m %s", tier)
	}
	b.WriteString("\r\n\r\n") // full memory census now sits above (renderSliceTab)

	fmt.Fprintf(b, "  \x1b[1mSnapshots\x1b[0m\r\n")
	m.renderSnapshots(b)
}

// settingsLines renders the active slice's current quota settings (the SliceConfig
// the configurator edits) as a compact right-hand panel.
func (m *model) settingsLines() []string {
	if m.cfg == nil {
		return []string{dim("SETTINGS"), "", dim("loading…")}
	}
	c := m.cfg.Config
	const mb, kb = 1024 * 1024, 1024
	row := func(label, val string) string {
		return " " + pad(dim(label), 11) + val
	}
	pair := func(n, store int, unit string) string {
		return fmt.Sprintf("%d · %d %s", n, store, unit)
	}
	out := []string{dim("SETTINGS")}
	if m.cfg.MonthlyCostCents > 0 {
		out = append(out, row("monthly", fmt.Sprintf("$%.2f", float64(m.cfg.MonthlyCostCents)/100)))
	} else {
		out = append(out, row("monthly", cGreen+"free"+cReset))
	}
	out = append(out,
		"",
		row("functions", fmt.Sprintf("%d", c.Atomic.MaxNumberOfFunctions)),
		row("memory", fmt.Sprintf("%d MB", c.Atomic.MaxFunctionMemoryBytes/mb)),
		row("runtime", fmt.Sprintf("%d s", c.Atomic.MaxFunctionRuntimeInSeconds)),
		row("requests", fmt.Sprintf("%d /min", c.Atomic.MaxNumberOfRequestsPerMinute)),
		row("scheduled", fmt.Sprintf("%d", c.Atomic.MaxNumberOfScheduledJobs)),
		row("log ret.", fmt.Sprintf("%d h", c.Atomic.MaxNumberOfHoursForLogRetention)),
		row("history", fmt.Sprintf("%d", c.Atomic.MaxNumberOfDeploymentsInHistory)),
		"",
		row("nosql", pair(c.Backbone.NoSQL.MaxCollections, c.Backbone.NoSQL.MaxStorageBytes/mb, "MB")),
		row("sql", pair(c.Backbone.SQL.MaxDatabases, c.Backbone.SQL.MaxStorageBytes/mb, "MB")),
		row("queues", fmt.Sprintf("%d · %d", c.Backbone.Queues.MaxQueues, c.Backbone.Queues.MaxDepthEach)),
		row("blobs", pair(c.Backbone.Blobs.MaxCount, c.Backbone.Blobs.MaxSizeInBytesEach/mb, "MB")),
		row("secrets", pair(c.Backbone.Secrets.MaxCount, c.Backbone.Secrets.MaxSizeInBytesEach/kb, "KB")),
		row("vault", fmt.Sprintf("%d KB/item · %d entries/uid", c.Deed.Vault.MaxSizeInBytesEach/kb, c.Deed.Vault.MaxEntriesPerUID)),
		row("pocket", fmt.Sprintf("%d KB/item", c.Deed.Pocket.MaxSizeInBytesEach/kb)),
		row("realtime", fmt.Sprintf("%d", c.Backbone.Realtime.MaxConcurrentConnections)),
		row("locks", fmt.Sprintf("%d", c.Backbone.Locks.MaxConcurrent)),
		row("canvas", fmt.Sprintf("%d MB", c.Canvas.TotalMaxSizeInBytes/mb)),
		row("backups", fmt.Sprintf("%d days", c.Backbone.BackupRetentionDays)),
		"",
		dim(" [c] configure"),
	)
	return out
}

func (m *model) renderSnapshots(b *strings.Builder) {
	if len(m.snaps) == 0 {
		fmt.Fprintf(b, "  No snapshots of \x1b[36m%s\x1b[0m — press 's' to create one.\r\n", m.activeOrDash())
		return
	}
	fmt.Fprintf(b, "    \x1b[2m%-22s %-9s %10s  %s\x1b[0m\r\n", "NAME", "STATUS", "SIZE", "CREATED")
	win := m.viewport()
	start := 0
	if m.snapSel >= win {
		start = m.snapSel - win + 1
	}
	end := min(start+win, len(m.snaps))
	for i := start; i < end; i++ {
		s := m.snaps[i]
		row(b, i == m.snapSel, fmt.Sprintf("%-22s %-9s %10s  %s",
			trunc(snapLabel(s), 22), s.Status, fmtBytes(s.Size), shortTime(s.CreatedAt)))
	}
}

func (m *model) activeOrDash() string {
	if m.active == "" {
		return "—"
	}
	return m.active
}

// sliceURL is the active slice's public URL (see sliceURLFor).
func (m *model) sliceURL() string { return m.sliceURLFor(m.active) }

// sliceURLFor builds a slice's public URL (https://user-slice.ondrift.eu); the
// "default" slice lives at the bare https://user.ondrift.eu. Mirrors the deploy
// command's buildSiteURL. Empty when there's no user/slice.
func (m *model) sliceURLFor(slice string) string {
	if m.user == "" || slice == "" {
		return ""
	}
	scheme := "http://"
	if strings.HasPrefix(common.APIBaseURL, "https://") {
		scheme = "https://"
	}
	host := strings.TrimPrefix(common.APIBaseURL, scheme)
	host = strings.TrimPrefix(host, "api.")
	// Every slice — including "default" — is reached at <user>-<slice>.<root>;
	// there is no bare <user>.<root> shortcut.
	return scheme + m.user + "-" + slice + "." + host
}

func snapLabel(s snapshotRow) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

func snapFilename(s snapshotRow) string {
	base := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ' ' {
			return '-'
		}
		return r
	}, snapLabel(s))
	return base + ".tar.gz"
}

func shortTime(s string) string {
	if len(s) >= 16 {
		return strings.Replace(s[:16], "T", " ", 1)
	}
	return s
}
