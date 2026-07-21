package portal

// configForm is the full slice configurator, in the terminal: every field the
// browser configurator shows — name, functions, scheduled jobs, function
// memory (32/64/128/256), NoSQL collections, SQL databases, queues, realtime
// connections, blobs, secrets, billing period — with the same live pricing
// (POST /ops/slice/price on every change) and the same create payload. Opened
// from the sidebar's "+ new slice". Matches free-tier exactly → tier "hacker"
// (everything included, no billing); anything else → tier "custom".

import (
	"fmt"
	"strings"

	"github.com/ondrift/cli/v2/common"
)

var memOptions = []int{32, 64, 128, 256} // MiB — the configurator's memory seats

// freeDefaults are the free (Hacker) ceiling for every tunable, in the same
// units the form steppers use (storage in MB, secret size in KB). Mirrors
// plan.HackerPreset. A config is free iff every value is ≤ its entry here
// (memory ≤ 32 MiB, handled separately). The free tier is a ceiling, so
// dialling anything *down* stays free; exceeding any one tips it to custom.
var freeDefaults = map[string]int{
	// Atomic
	"functions": 5, "scheduled": 1, "runtime": 10, "rpm": 60, "logret": 24, "history": 1,
	// Backbone
	"nosql": 2, "nosqlstore": 50, "sql": 1, "sqlstore": 50,
	"queues": 1, "queuedepth": 500, "blobs": 10, "blobsize": 5, "blobstore": 50,
	"secrets": 5, "secretsize": 1, "realtime": 50, "locks": 100000,
	// Canvas / slice
	"canvassize": 50, "backup": 3,
}

const (
	rowName = iota
	rowNum
	rowMem
	rowBilling
	rowCreate
	rowSection // a foldable section header (ATOMIC / BACKBONE / CANVAS / SLICE)
)

type formRow struct {
	kind           int
	key            string // for rowNum
	label          string
	section        string // section header this row sits under
	unit           string // display suffix (s · h · MB · KB · days · mo)
	min, max, step int
	priced         bool   // contributes to the monthly total
	help           string // one-line description shown under the form when focused
}

type configForm struct {
	name     string
	vals     map[string]int  // numeric field values by key
	mem      int             // index into memOptions
	billing  int             // months
	cursor   int             // focused row, indexing navRows()
	folded   map[string]bool // collapsed sections (by name)
	price    *priceResult
	priceErr string
	resize   bool // true = "configure" an existing slice (name fixed; submit resizes)
}

func newConfigForm() *configForm {
	vals := make(map[string]int, len(freeDefaults))
	for k, v := range freeDefaults {
		vals[k] = v
	}
	return &configForm{vals: vals, mem: 0, billing: 1, cursor: 1, folded: map[string]bool{}}
}

// newResizeForm builds the configurator for an existing slice, pre-populated
// from its current config (the inverse of gatherConfig) — the "configure /
// upgrade" flow. The name is fixed; submitting resizes rather than creates.
func newResizeForm(name string, c sliceCfg, billing int) *configForm {
	const mb, kb = 1024 * 1024, 1024
	vals := map[string]int{
		"functions": c.Atomic.MaxNumberOfFunctions,
		"runtime":   c.Atomic.MaxFunctionRuntimeInSeconds,
		"history":   c.Atomic.MaxNumberOfDeploymentsInHistory,
		"logret":    c.Atomic.MaxNumberOfHoursForLogRetention,
		"rpm":       c.Atomic.MaxNumberOfRequestsPerMinute,
		"scheduled": c.Atomic.MaxNumberOfScheduledJobs,

		"secrets":    c.Backbone.Secrets.MaxCount,
		"secretsize": c.Backbone.Secrets.MaxSizeInBytesEach / kb,
		"blobs":      c.Backbone.Blobs.MaxCount,
		"blobsize":   c.Backbone.Blobs.MaxSizeInBytesEach / mb,
		"blobstore":  c.Backbone.Blobs.MaxStorageBytes / mb,
		"nosql":      c.Backbone.NoSQL.MaxCollections,
		"nosqlstore": c.Backbone.NoSQL.MaxStorageBytes / mb,
		"sql":        c.Backbone.SQL.MaxDatabases,
		"sqlstore":   c.Backbone.SQL.MaxStorageBytes / mb,
		"queues":     c.Backbone.Queues.MaxQueues,
		"queuedepth": c.Backbone.Queues.MaxDepthEach,
		"realtime":   c.Backbone.Realtime.MaxConcurrentConnections,
		"locks":      c.Backbone.Locks.MaxConcurrent,

		"canvassize": int(c.Canvas.TotalMaxSizeInBytes / mb),
		"backup":     c.Backbone.BackupRetentionDays,
	}
	mem := 0
	for i, o := range memOptions {
		if o == int(c.Atomic.MaxFunctionMemoryBytes/mb) {
			mem = i
			break
		}
	}
	if billing <= 0 {
		billing = 1
	}
	return &configForm{name: name, vals: vals, mem: mem, billing: billing, cursor: 1, folded: map[string]bool{}, resize: true}
}

// navRows is the navigable list: a header for each section, followed by that
// section's fields unless the section is folded. The cursor indexes this.
func (f *configForm) navRows() []formRow {
	var nav []formRow
	last := ""
	for _, r := range f.rows() {
		if r.section != last {
			nav = append(nav, formRow{kind: rowSection, section: r.section, label: r.section,
				help: "Press enter (or ←/→) to fold / unfold this section."})
			last = r.section
		}
		if !f.folded[r.section] {
			nav = append(nav, r)
		}
	}
	return nav
}

// toggleFold collapses/expands a section, keeping the cursor in range.
func (f *configForm) toggleFold(section string) {
	f.folded[section] = !f.folded[section]
	if n := len(f.navRows()); f.cursor >= n {
		f.cursor = n - 1
	}
}

// createNavIndex returns the cursor position of the Create button, unfolding
// its section first so it's always reachable.
func (f *configForm) createNavIndex() int {
	for _, r := range f.rows() {
		if r.kind == rowCreate {
			f.folded[r.section] = false
		}
	}
	for i, r := range f.navRows() {
		if r.kind == rowCreate {
			return i
		}
	}
	return 0
}

func (f *configForm) rows() []formRow {
	const A, B, C, S = "ATOMIC", "BACKBONE", "CANVAS", "SLICE"
	return []formRow{
		// ── Atomic ──
		{kind: rowNum, key: "functions", label: "Functions", section: A, min: 1, max: 100, step: 1, priced: true,
			help: "How many Atomic serverless functions you can deploy to this slice."},
		{kind: rowNum, key: "scheduled", label: "Scheduled jobs", section: A, min: 0, max: 50, step: 1, priced: true,
			help: "How many of your functions may run automatically on a cron schedule."},
		{kind: rowMem, label: "Function memory", section: A, priced: true,
			help: "The most RAM any single function may use during one invocation."},
		{kind: rowNum, key: "runtime", label: "Function runtime", section: A, unit: "s", min: 1, max: 300, step: 5,
			help: "Max wall-clock seconds a single function invocation may run before timeout."},
		{kind: rowNum, key: "rpm", label: "Requests / min", section: A, min: 10, max: 100000, step: 10,
			help: "Max requests per minute served across the slice's functions (rate limit)."},
		{kind: rowNum, key: "logret", label: "Log retention", section: A, unit: "h", min: 1, max: 720, step: 6,
			help: "How many hours of function logs are kept before they're purged."},
		{kind: rowNum, key: "history", label: "Deploy history", section: A, min: 1, max: 50, step: 1,
			help: "How many past deployments are kept per function, for rollback."},
		// ── Backbone ──
		{kind: rowNum, key: "nosql", label: "NoSQL collections", section: B, min: 0, max: 100, step: 1, priced: true,
			help: "How many NoSQL collections you can create in Backbone (document storage)."},
		{kind: rowNum, key: "nosqlstore", label: "NoSQL storage", section: B, unit: "MB", min: 10, max: 100000, step: 10,
			help: "Total document storage available across all NoSQL collections."},
		{kind: rowNum, key: "sql", label: "SQL databases", section: B, min: 0, max: 50, step: 1, priced: true,
			help: "How many SQL databases you can create in Backbone (relational storage)."},
		{kind: rowNum, key: "sqlstore", label: "SQL storage", section: B, unit: "MB", min: 10, max: 100000, step: 10,
			help: "Storage available per SQL database."},
		{kind: rowNum, key: "queues", label: "Queues", section: B, min: 0, max: 100, step: 1, priced: true,
			help: "How many message queues you can create for background, async work."},
		{kind: rowNum, key: "queuedepth", label: "Queue depth", section: B, min: 50, max: 100000, step: 50,
			help: "Maximum number of messages held in a single queue at once."},
		{kind: rowNum, key: "blobstore", label: "Blob storage", section: B, unit: "MB", min: 0, max: 100000, step: 10,
			help: "Total blob storage you reserve. Pooled with NoSQL/SQL/Canvas and billed per GiB — this is what blobs cost."},
		{kind: rowNum, key: "blobs", label: "Blob count", section: B, min: 0, max: 10000, step: 10, priced: true,
			help: "How many blobs you can store — a free safety quota (you pay for the storage above, not the count)."},
		{kind: rowNum, key: "blobsize", label: "Blob size", section: B, unit: "MB", min: 1, max: 1000, step: 1,
			help: "Maximum size of a single blob — a free safety quota."},
		{kind: rowNum, key: "secrets", label: "Secrets", section: B, min: 0, max: 200, step: 1, priced: true,
			help: "How many encrypted secrets (API keys, tokens) you can store in Backbone."},
		{kind: rowNum, key: "secretsize", label: "Secret size", section: B, unit: "KB", min: 1, max: 1024, step: 1,
			help: "Maximum size of a single secret value."},
		{kind: rowNum, key: "realtime", label: "Realtime conns", section: B, min: 0, max: 10000, step: 10, priced: true,
			help: "Peak concurrent realtime (WebSocket) connections allowed across the slice."},
		{kind: rowNum, key: "locks", label: "Locks", section: B, min: 0, max: 1000000, step: 10000,
			help: "Concurrent Backbone locks (per-element edit locks). Cheap; cap is generous."},
		// ── Canvas ──
		{kind: rowNum, key: "canvassize", label: "Max site size", section: C, unit: "MB", min: 0, max: 10000, step: 10,
			help: "Total storage for the static site(s) Canvas serves for this slice."},
		// ── Slice meta ──
		{kind: rowName, label: "Name", section: S,
			help: "A short, lowercase name (a–z, 0–9). It becomes part of your slice's URL."},
		{kind: rowNum, key: "backup", label: "Backup retention", section: S, unit: "days", min: 0, max: 365, step: 1, priced: true,
			help: "How many days of backups / snapshots are retained for the slice."},
		{kind: rowBilling, label: "Billing period", section: S,
			help: "How many months to pay for up front. Free-tier slices are never billed."},
		{kind: rowCreate, label: "Create", section: S,
			help: "Provision the slice with these limits. The free tier needs no payment."},
	}
}

func (f *configForm) memMiB() int { return memOptions[f.mem] }

// isFree reports whether the config fits within the free tier — i.e. every
// dimension is at or below its free allowance (the free tier is a ceiling, not
// an exact spec, so dialing a value *down* keeps you free). Exceeding any free
// limit — including bumping memory above 32 MiB — tips the slice into custom
// (paid). A free-fitting slice is created as "hacker" and gets the standard
// free bundle.
func (f *configForm) isFree() bool {
	if f.mem != 0 { // memory above the 32 MiB free seat
		return false
	}
	for k, v := range freeDefaults {
		if f.vals[k] > v {
			return false
		}
	}
	return true
}

// adjust nudges the focused field by ±step (or cycles memory / billing).
func (f *configForm) adjust(r formRow, up bool) {
	d := 1
	if !up {
		d = -1
	}
	switch r.kind {
	case rowNum:
		v := f.vals[r.key] + d*r.step
		if v < r.min {
			v = r.min
		}
		if v > r.max {
			v = r.max
		}
		f.vals[r.key] = v
	case rowMem:
		f.mem += d
		if f.mem < 0 {
			f.mem = 0
		}
		if f.mem >= len(memOptions) {
			f.mem = len(memOptions) - 1
		}
	case rowBilling:
		b := f.billing + d
		if b < 1 {
			b = 1
		}
		if b > 36 {
			b = 36
		}
		f.billing = b
	}
}

// gatherConfig builds the SliceConfig payload — identical to the configurator's
// buildConfig (user-tunable fields + the same fixed values).
func (f *configForm) gatherConfig() map[string]any {
	v := f.vals
	const mb, kb = 1024 * 1024, 1024
	return map[string]any{
		"canvas": map[string]any{"TotalMaxSizeInBytes": v["canvassize"] * mb},
		"atomic": map[string]any{
			"MaxNumberOfFunctions":            v["functions"],
			"MaxFunctionRuntimeInSeconds":     v["runtime"],
			"MaxNumberOfDeploymentsInHistory": v["history"],
			"MaxNumberOfHoursForLogRetention": v["logret"],
			"MaxNumberOfRequestsPerMinute":    v["rpm"],
			"MaxNumberOfScheduledJobs":        v["scheduled"],
			"MaxFunctionMemoryBytes":          f.memMiB() * mb,
		},
		"backbone": map[string]any{
			"secrets":               map[string]any{"MaxCount": v["secrets"], "MaxSizeInBytesEach": v["secretsize"] * kb},
			"blobs":                 map[string]any{"MaxCount": v["blobs"], "MaxSizeInBytesEach": v["blobsize"] * mb, "MaxStorageBytes": v["blobstore"] * mb},
			"nosql":                 map[string]any{"MaxCollections": v["nosql"], "MaxStorageBytes": v["nosqlstore"] * mb},
			"sql":                   map[string]any{"MaxDatabases": v["sql"], "MaxStorageBytes": v["sqlstore"] * mb},
			"queues":                map[string]any{"MaxQueues": v["queues"], "MaxDepthEach": v["queuedepth"]},
			"realtime":              map[string]any{"MaxConcurrentConnections": v["realtime"]},
			"locks":                 map[string]any{"MaxConcurrent": v["locks"]},
			"backup_retention_days": v["backup"],
		},
	}
}

// ── model glue ───────────────────────────────────────────────────────────────

// handleForm routes keys while the configurator is open. Returns true to quit.
func (m *model) handleForm(k key) bool {
	f := m.form
	nav := f.navRows()
	if f.cursor >= len(nav) {
		f.cursor = len(nav) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}
	cur := nav[f.cursor]
	switch k {
	case keyQuit, keyBack:
		m.form = nil
		m.status = "cancelled"
	case keyTab:
		// Jump straight to Create (toggle back to the top if already there).
		if ci := f.createNavIndex(); f.cursor == ci {
			f.cursor = 0
		} else {
			f.cursor = ci
		}
	case keyUp:
		if f.cursor > 0 {
			f.cursor--
		}
	case keyDown:
		if f.cursor < len(nav)-1 {
			f.cursor++
		}
	case keyLeft:
		if cur.kind == rowSection {
			f.toggleFold(cur.section)
		} else {
			f.adjust(cur, false)
			m.recomputePrice()
		}
	case keyRight:
		if cur.kind == rowSection {
			f.toggleFold(cur.section)
		} else {
			f.adjust(cur, true)
			m.recomputePrice()
		}
	case keyEnter:
		switch cur.kind {
		case rowSection:
			f.toggleFold(cur.section)
		case rowName:
			if f.resize { // configuring an existing slice — the name is fixed
				m.status = "slice name can't be changed"
				break
			}
			m.input = &inputPrompt{
				label:      "slice name (a–z 0–9):",
				buf:        f.name,
				allowEmpty: true,
				run: func(s string) string {
					f.name = strings.TrimSpace(s)
					return ""
				},
			}
		case rowCreate:
			m.submitForm()
		}
	}
	return false
}

// recomputePrice re-prices the form. We price even free-tier configs so the
// breakdown still lists every resource — the panel just renders them as "free".
func (m *model) recomputePrice() {
	f := m.form
	pr, err := fetchPrice(f.gatherConfig(), f.billing)
	if err != nil {
		f.priceErr = err.Error()
		return
	}
	f.price, f.priceErr = pr, ""
}

func (m *model) submitForm() {
	f := m.form

	if f.resize { // "configure" an existing slice — apply a resize/upgrade
		if err := resizeSlice(f.name, f.gatherConfig(), f.billing); err != nil {
			m.status = "✗ " + err.Error()
			return
		}
		m.form = nil
		m.invalidateAll()
		m.load(m.tab)
		m.status = "configured " + f.name
		return
	}

	if f.isFree() && m.hasFreeSlice() {
		m.status = "✗ you already have a free slice — bump a value to create a paid one"
		return
	}
	if !validSliceName(f.name) {
		m.status = "✗ name must be lowercase a–z / 0–9, 1–30 chars"
		return
	}
	tier := "custom"
	if f.isFree() {
		tier = "hacker"
	}
	if err := createSlice(f.name, tier, f.gatherConfig(), f.billing); err != nil {
		m.status = "✗ " + err.Error()
		return
	}
	name := f.name
	m.form = nil
	m.loadSlices()
	_ = common.SaveActiveSlice(name) // #nosec G104 -- best-effort; status reflects create success
	m.active = name
	m.load(m.tab)
	m.status = "created + activated " + name
}

// formLines renders the configurator as two columns inside the main pane: the
// form on the left, a live pricing panel pinned on the right (always visible,
// just like the browser configurator).
func (m *model) formLines() []string {
	f := m.form

	// Column widths: form ~46, pricing gets the rest (clamped so it survives a
	// narrow terminal — truncVis in mainLines() clips any overflow anyway).
	formW := 46
	if formW > m.contentW-20 {
		formW = m.contentW - 20
	}
	if formW < 24 {
		formW = 24
	}

	// Pricing panel: a compact fixed-width column so values right-align cleanly.
	panelW := m.contentW - formW - 2
	if panelW > 38 {
		panelW = 38
	}
	if panelW < 22 {
		panelW = 22
	}

	// Build the form (title + section headers + fields), noting the focused
	// field's line so the window can keep it on screen.
	// Iterate the SAME navRows() the cursor + handleForm index, so the
	// highlight, the edited field, and the help line never drift apart. (The
	// section headers are navRows entries, rendered by renderRow.)
	nav := f.navRows()
	if f.cursor >= len(nav) {
		f.cursor = len(nav) - 1
	}
	if f.cursor < 0 {
		f.cursor = 0
	}

	form := []string{bold(cGreen + "+ New slice" + cReset), ""}
	cursorLine := 0
	for i, r := range nav {
		if r.kind == rowCreate {
			if i == f.cursor {
				cursorLine = len(form) // Create lives in the price column; keep the bottom in view
			}
			continue
		}
		if i == f.cursor {
			cursorLine = len(form)
		}
		form = append(form, f.renderRow(i, r))
	}

	// Help footer for the focused row, pinned under the (windowed) form.
	help := []string{"", dim(strings.Repeat("─", formW-2))}
	for _, hl := range wrapText(nav[f.cursor].help, formW-2) {
		help = append(help, dim(hl))
	}

	availH := m.contentH + 2 // body rows available for the left column
	bodyH := availH - len(help)
	if bodyH < 4 {
		bodyH = 4
	}
	form = windowLines(form, cursorLine, bodyH)

	left := make([]string, 0, len(form)+len(help))
	left = append(left, form...)
	left = append(left, help...)

	right := m.priceColumn(panelW)

	n := max(len(left), len(right))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		var l, rr string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rr = right[i]
		}
		out = append(out, pad(truncVis(l, formW), formW)+dim("│ ")+rr)
	}
	return out
}

// windowLines returns at most h lines, scrolled so index `focus` stays visible.
func windowLines(lines []string, focus, h int) []string {
	if len(lines) <= h {
		return lines
	}
	start := focus - h/2
	if start < 0 {
		start = 0
	}
	if start > len(lines)-h {
		start = len(lines) - h
	}
	return lines[start : start+h]
}

// priceColumn is the right-hand live pricing panel (width w), with the Create
// button pinned at the bottom. On free-tier every line reads "free" (green).
func (m *model) priceColumn(w int) []string {
	f := m.form
	free := f.isFree()
	out := []string{dim("PRICING"), ""}

	switch {
	case f.priceErr != "":
		out = append(out, "\x1b[31m✗ "+f.priceErr+"\x1b[0m")
	case f.price != nil:
		out = append(out, priceRow3(dim("Item"), dim("amt"), dim("price"), w))
		for _, it := range f.price.Items {
			price := cents(it.SubtotalCents)
			if free || it.SubtotalCents == 0 {
				price = cGreen + "free" + cReset
			}
			out = append(out, priceRow3(it.Label, fmt.Sprint(it.Quantity), price, w))
		}
		out = append(out, dim(strings.Repeat("─", w)))
		monthly := bold(cents(f.price.MonthlyCents))
		if free {
			monthly = cGreen + bold("free") + cReset
		}
		out = append(out, priceRow3(bold("Monthly"), "", monthly, w))
		if !free {
			out = append(out,
				priceRow3("Prepaid", "", cents(f.price.PrepaidCents), w),
				dim(fmt.Sprintf("over %d month(s)", f.billing)))
		}
	default:
		out = append(out, dim("computing…"))
	}

	// Pre-flight: a second free slice would be rejected (one per account), so
	// warn and disable Create rather than letting the server 409. (Never applies
	// when configuring an existing slice — that's a resize, not a create.)
	blocked := !f.resize && free && m.hasFreeSlice()
	out = append(out, "")
	if blocked {
		for _, l := range wrapText("You already have a free slice — bump a value to create a paid one.", w) {
			out = append(out, "\x1b[33m"+l+"\x1b[0m")
		}
		out = append(out, "")
	}
	out = append(out, "")
	label := "Create slice"
	if f.resize {
		label = "Apply changes"
	}
	out = append(out, button(label, cOrange, bgOrange, f.cursorOnCreate(), blocked)...)
	return out
}

// priceRow3 lays out item / amount / price across width w — the item
// left-justified, the amount and price right-aligned — counting visible width
// so ANSI colours don't skew the columns.
func priceRow3(item, amount, price string, w int) string {
	const amtW, priceW = 6, 8
	itemW := w - amtW - priceW - 2
	if itemW < 4 {
		itemW = 4
	}
	return pad(truncVis(item, itemW), itemW) + " " + rjust(amount, amtW) + " " + rjust(price, priceW)
}

// rjust right-aligns a (possibly coloured) string within w visible columns.
func rjust(s string, w int) string {
	if d := w - vlen(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// hasFreeSlice reports whether the account already holds a free (hacker) slice
// — the one-per-account rule means a second free slice would be rejected.
func (m *model) hasFreeSlice() bool {
	for _, s := range m.slices {
		if s.Tier == "hacker" {
			return true
		}
	}
	return false
}

// cursorOnCreate reports whether the cursor is on the (hidden-from-left) Create
// row, which is rendered as the button at the bottom of the pricing column.
func (f *configForm) cursorOnCreate() bool {
	nav := f.navRows()
	return f.cursor >= 0 && f.cursor < len(nav) && nav[f.cursor].kind == rowCreate
}

func (f *configForm) renderRow(i int, r formRow) string {
	if r.kind == rowSection {
		arrow := "▾"
		if f.folded[r.section] {
			arrow = "▸"
		}
		if f.cursor == i {
			return cyan("▶") + " " + bold(arrow+" "+r.section)
		}
		return "  " + dim(arrow+" "+r.section)
	}

	cur := "  "
	if f.cursor == i {
		cur = cyan("▶") + " "
	}
	switch r.kind {
	case rowName:
		v := f.name
		if v == "" {
			v = dim("(enter to type)")
		}
		return cur + fmt.Sprintf("%-18s %s", "Name", v)
	case rowMem:
		return cur + fmt.Sprintf("%-18s ‹ %d MiB ›", "Function memory", f.memMiB())
	case rowBilling:
		return cur + fmt.Sprintf("%-18s ‹ %d mo ›", "Billing period", f.billing)
	default: // rowNum
		unit := ""
		if r.unit != "" {
			unit = " " + r.unit
		}
		return cur + fmt.Sprintf("%-18s ‹ %d%s ›", r.label, f.vals[r.key], unit)
	}
}

// cents formats integer euro-cents as €X.YY.
func cents(c int) string {
	return fmt.Sprintf("€%d.%02d", c/100, c%100)
}

// wrapText word-wraps plain text to a visible width of w (no ANSI inside).
func wrapText(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
