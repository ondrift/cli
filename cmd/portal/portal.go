// Package portal is the bare `drift` dashboard — a full-screen, k9s-style TUI
// over the same /ops/* surface the other commands wrap: a navbar, a slices
// sidebar (TAB to focus), and a brand-coloured Slice/Atomic/Backbone/Canvas
// main pane for browsing functions, data, and sites without leaving the
// terminal. (Still reachable as the hidden `drift portal` alias.)
//
// Deliberately framework-free (no bubbletea): alt-screen + raw mode via
// golang.org/x/term + ANSI, matching the lean toolkit the rest of the CLI uses.
// One model, one render() (layout.go), one key loop. Every fetch is auto-scoped
// to the active slice (X-Slice header), so the panes follow the sidebar.
package portal

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	sliceCmd "github.com/ondrift/cli/cmd/slice"
	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// tabs across the top — mirrors the four portal pillars.
var tabs = []string{"Slice", "Atomic", "Backbone", "Canvas"}

const (
	tabSlice = iota
	tabAtomic
	tabBackbone
	tabCanvas
)

// GetCmd returns the `drift portal` command.
func GetCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "portal",
		Short:   "Interactive dashboard for your slices, functions & data (TUI)",
		Example: "  drift portal",
		GroupID: "account",
		Hidden:  true, // primary entrypoint is bare `drift`; kept as an alias
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("create") {
				name, _ := cmd.Flags().GetString("create")
				return Run(version, &name)
			}
			return Run(version, nil)
		},
	}
	// Internal-only: `drift slice create <name>`'s default path re-execs into
	// this instead of importing cmd/portal directly (cmd/portal already
	// imports cmd/slice for SliceEntry/FetchSlices/TierLabel, so the reverse
	// import would be a cycle). Not meant to be typed by a user directly.
	cmd.Flags().String("create", "", "internal: launch straight into create-slice mode, pre-filled with this name")
	_ = cmd.Flags().MarkHidden("create")
	return cmd
}

type confirmAction struct {
	prompt string
	run    func() string // performs the action, returns a status line
}

// inputPrompt is a single-line inline text capture (e.g. set a secret). While
// non-nil it intercepts raw keystrokes; Enter submits, Esc/Ctrl-C cancels.
type inputPrompt struct {
	label      string
	buf        string
	allowEmpty bool                // if true, an empty submit still runs (e.g. auto-named snapshot)
	run        func(string) string // called with the entered text; returns a status line
}

type model struct {
	tab    int
	user   string
	active string
	status string

	version string         // the running CLI version (for the update check)
	latest  string         // a newer release tag when one is available; "" otherwise
	conf    *confirmAction // pending destructive confirm (intercepts keys)
	input   *inputPrompt   // pending inline text input (intercepts keys)

	slices []sliceCmd.SliceEntry
	fns    []fnRow
	bb     *bbStatus
	rt     *rtStats

	// Atomic: a function rail (left) + the opened function's live details (right).
	// fnExp is the Enter-committed function (not the rail cursor); the ticker keeps
	// its logs live. -1 = nothing opened yet (the detail pane shows a prompt).
	fnMet     map[string]fnMetrics // per-function metrics (rail + details pane)
	fnExp     int                  // the opened (Enter-committed) function; -1 = none
	fnExpLogs []logEntry           // opened function's live log tail
	triggers  []triggerDef         // slice triggers (queue/schedule/webhook) for the detail card
	detailSel int                  // focusDetail: cursor over the function's settable fields (fnConfigKeys)
	hideCards bool                 // Atomic: [s] hides the service stat cards
	logsFull  bool                 // Atomic focusDetail: [f] expands logs to full-screen
	news      []announcement       // hosted "what's new" feed (strip above the main panel)

	detail   *detailView     // non-nil = scrollable drill-down (Backbone dump / Atomic metrics+logs)
	chooser  *newChooser     // non-nil = the "+ New slice" method chooser (modal)
	explorer *fileExplorer   // non-nil = the Driftfile directory explorer (modal)
	form     *configForm     // non-nil = the new-slice configurator (modal)
	deleting *deleteSlice    // non-nil = the "Deleting <name>" confirmation mode
	platform *platformStatus // non-nil = the Ctrl-S platform-status popup is open

	fd       int         // terminal fd (for suspend/resume around an external deploy)
	oldState *term.State // saved cooked-mode state, restored on suspend

	focus    int // focusMain / focusSidebar (TAB toggles)
	sideSel  int // sidebar cursor (0..len(slices); last = "+ new slice")
	sideRows int // sidebar content rows (computed each render; pins "+ new slice")
	contentW int // main-pane content width  (computed each render)
	contentH int // main-pane content height (computed each render)

	// Slice tab = active-slice overview + snapshots + current settings
	snaps    []snapshotRow
	snapSel  int
	cfg      *sliceDoc    // current slice's config/tier (settings panel + configure)
	price    *priceResult // itemized bill for cfg's current config (#CLITUI1)
	priceErr string

	// Backbone data explorer
	bbPrim  int      // selected primitive (NoSQL/Cache/Queues/Blobs/Locks/Secrets)
	bbItems []bbItem // rows for the active primitive
	bbSel   int      // cursor within bbItems

	rows, cols int // terminal size (captured at start)

	sel     [4]int // per-tab cursor (Backbone uses bbSel instead)
	loadErr [4]string

	loadedAt [4]time.Time // per-tab cache stamp; tab switches reuse fresh data
}

// Run launches the full-screen dashboard. It's the bare `drift` entrypoint
// (and the hidden `drift portal` alias). version is the running CLI version,
// used for the "update available" banner. createName is non-nil when the
// dashboard should open straight into create-slice mode instead of the
// normal sidebar/tabs view — *createName pre-fills the form's name field
// (possibly empty, letting the user type it in the form). This is how
// `drift slice create <name>`'s default path lands the user directly in the
// configurator-equivalent view, now that the old browser-based configurator
// service is retired.
func Run(version string, createName *string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("the drift dashboard needs an interactive terminal")
	}
	user := common.GetUsername()
	if user == "" {
		return fmt.Errorf("not logged in — run `drift account login` first")
	}

	m := &model{user: user, active: common.GetActiveSlice(), version: version, cols: 80, rows: 24, fnExp: -1}
	if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
		m.cols, m.rows = w, h
	}
	m.loadSlices() // sidebar is always populated, independent of the active tab
	m.load(m.tab)
	if createName != nil {
		m.form = newConfigForm()
		m.form.name = *createName
		m.recomputePrice()
	}

	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enter raw mode: %w", err)
	}
	m.fd, m.oldState = fd, old              // for suspend/resume around an external deploy
	defer term.Restore(fd, old)             // #nosec G104 -- best-effort terminal restore
	fmt.Print("\x1b[?1049h\x1b[?25l")       // enter alt-screen buffer + hide cursor (k9s/vim style)
	defer fmt.Print("\x1b[?25h\x1b[?1049l") // show cursor + leave alt-screen (restores scrollback) on quit

	r := bufio.NewReader(os.Stdin)

	// A render mutex serializes the main loop and the resize watcher: input is
	// read unlocked (so a resize can repaint while we wait on a key), but every
	// model mutation + render happens under the lock.
	var mu sync.Mutex
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			mu.Lock()
			if w, h, e := term.GetSize(m.fd); e == nil && w > 0 && h > 0 {
				m.cols, m.rows = w, h
			}
			m.render()
			mu.Unlock()
		}
	}()

	// Live tail: while a function's inline panel is open on the Atomic tab, poll
	// its logs + metrics and repaint, so the logs stream without keystrokes. The
	// fetch runs unlocked; only the model write + render take the lock.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			mu.Lock()
			fn := ""
			if m.tab == tabAtomic && m.fnExp >= 0 && m.fnExp < len(m.fns) &&
				m.detail == nil && m.form == nil && m.chooser == nil &&
				m.explorer == nil && m.input == nil && m.conf == nil && m.platform == nil {
				fn = fnKey(m.fns[m.fnExp]) // element/name — the slice's metrics key
			}
			mu.Unlock()
			if fn == "" {
				continue
			}
			met, errM := fetchMetrics(fn)
			logs, errL := fetchLogs(fn)
			mu.Lock()
			if m.tab == tabAtomic && m.fnExp >= 0 && m.fnExp < len(m.fns) &&
				fnKey(m.fns[m.fnExp]) == fn {
				if errM == nil && m.fnMet != nil {
					m.fnMet[fn] = met // the details pane reads per-function metrics from here
				}
				if errL == nil {
					m.fnExpLogs = logs
				}
				m.render()
			}
			mu.Unlock()
		}
	}()

	// Fetch the hosted "what's new" feed once, off the startup path, then repaint
	// so the strip appears without delaying launch. Failures are silent.
	go func() {
		news, err := fetchAnnouncements()
		if err != nil || len(news) == 0 {
			return
		}
		mu.Lock()
		m.news = news
		m.render()
		mu.Unlock()
	}()

	// Check GitHub for a newer CLI release, off the startup path. If one exists,
	// the status line shows an "update available" nudge. Silent on any failure
	// (offline, rate-limited, no releases yet) — and only when we know our own
	// version (a dev build with an empty version never nags).
	go func() {
		if m.version == "" {
			return
		}
		rel, err := common.FetchLatestCLIRelease()
		if err != nil || common.CompareVersions(m.version, rel.Tag) >= 0 {
			return
		}
		mu.Lock()
		m.latest = rel.Tag
		m.render()
		mu.Unlock()
	}()

	for {
		mu.Lock()
		m.render()
		inputActive := m.input != nil
		mu.Unlock()

		if inputActive {
			b, err := r.ReadByte()
			if err != nil {
				return nil // stdin closed
			}
			mu.Lock()
			m.applyInputByte(b)
			mu.Unlock()
			continue
		}

		k, err := readKey(r)
		if err != nil {
			return nil // stdin closed
		}
		mu.Lock()
		quit := m.handle(k)
		mu.Unlock()
		if quit {
			return nil
		}
	}
}

// ─── input ──────────────────────────────────────────────────────────────────

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keyTab
	keyRefresh
	keyDelete
	keyYes
	keyQuit
	keySet      // 's' — set/new (e.g. a secret); on Atomic, toggle the stat cards
	keyCreate   // 'c' — create (e.g. a snapshot)
	keyDownload // 'w' — write/download (e.g. a snapshot)
	keyFull     // 'f' — toggle full-screen logs (Atomic, function in focus)
	keyPrevPrim // '[' — previous sub-view / primitive
	keyNextPrim // ']' — next sub-view / primitive
	keyNew      // 'n' — new slice (sidebar)
	keyOpen     // 'o' / Ctrl-O — open the slice's URL in a browser
	keyStatus   // Ctrl-S — platform status popup (anywhere)
	keyBack     // backspace — leave a drill-down
	key1
	key2
	key3
	key4
)

func readKey(r *bufio.Reader) (key, error) {
	b, err := r.ReadByte()
	if err != nil {
		return keyNone, err
	}
	switch b {
	case 3: // Ctrl-C is the only quit
		return keyQuit, nil
	case '\r', '\n':
		return keyEnter, nil
	case '\t':
		return keyTab, nil
	case 'j', 'J':
		return keyDown, nil
	case 'k', 'K':
		return keyUp, nil
	case 'h', 'H':
		return keyLeft, nil
	case 'l', 'L':
		return keyRight, nil
	case 'r', 'R':
		return keyRefresh, nil
	case 'd', 'D':
		return keyDelete, nil
	case 'y', 'Y':
		return keyYes, nil
	case 's', 'S':
		return keySet, nil
	case 'c', 'C':
		return keyCreate, nil
	case 'w', 'W':
		return keyDownload, nil
	case 'f', 'F':
		return keyFull, nil
	case '[':
		return keyPrevPrim, nil
	case ']':
		return keyNextPrim, nil
	case 'n', 'N':
		return keyNew, nil
	case 'o', 'O':
		return keyOpen, nil
	case 0x0f: // Ctrl-O — open URL (also works from the main pane)
		return keyOpen, nil
	case 0x13: // Ctrl-S — platform status popup
		return keyStatus, nil
	case 0x7f, 8: // backspace / delete-back
		return keyBack, nil
	case '1':
		return key1, nil
	case '2':
		return key2, nil
	case '3':
		return key3, nil
	case '4':
		return key4, nil
	case 0x1b: // ESC: arrow sequence (ESC [ A/B/C/D) or a lone Esc = go back
		if r.Buffered() == 0 { // nothing follows → a real Esc keypress
			return keyBack, nil
		}
		if b1, _ := r.ReadByte(); b1 == '[' {
			switch b2, _ := r.ReadByte(); b2 {
			case 'A':
				return keyUp, nil
			case 'B':
				return keyDown, nil
			case 'C':
				return keyRight, nil
			case 'D':
				return keyLeft, nil
			}
		}
		return keyBack, nil // ESC + anything else → also treat as back
	}
	return keyNone, nil
}

// handle applies a keypress; returns true to quit.
func (m *model) handle(k key) bool {
	// Ctrl-C quits from anywhere — modals, confirms, drill-downs included.
	if k == keyQuit {
		return true
	}

	// Platform-status popup: while open any key dismisses it; Ctrl-S (from
	// anywhere) opens it.
	if m.platform != nil {
		m.platform = nil
		return false
	}
	if k == keyStatus {
		m.openStatus()
		return false
	}

	// A pending confirm intercepts everything: y/enter runs it, anything else cancels.
	if m.conf != nil {
		if k == keyYes || k == keyEnter {
			m.status = m.conf.run()
			m.conf = nil
			m.load(m.tab)
		} else {
			m.conf = nil
			m.status = "cancelled"
		}
		return false
	}

	m.status = ""

	// "+ New slice" modals capture all keys until they finish or cancel.
	if m.chooser != nil {
		return m.handleChooser(k)
	}
	if m.explorer != nil {
		return m.handleExplorer(k)
	}
	if m.form != nil {
		return m.handleForm(k)
	}

	// A scrollable detail view (any tab) captures all keys.
	if m.detail != nil {
		return m.handleDetailKeys(k)
	}

	// TAB toggles focus between the slices sidebar and the main pane (collapsing a
	// focused function detail back out to the sidebar).
	if k == keyTab {
		if m.focus == focusSidebar {
			m.focus = focusMain
		} else {
			m.focus = focusSidebar
		}
		return false
	}
	if m.focus == focusSidebar {
		return m.handleSidebar(k)
	}
	// focusDetail: inside an Atomic function's settable fields (element / trigger /
	// schedule) — its own up/down/enter/esc nav; everything else is swallowed.
	if m.focus == focusDetail {
		return m.handleFnDetail(k)
	}

	// Slice + Backbone tabs have their own sub-view nav; they consume the keys
	// they own and let global keys (tab switch, refresh, quit) fall through.
	if m.tab == tabSlice && m.handleSlice(k) {
		return false
	}
	if m.tab == tabBackbone && m.handleBackbone(k) {
		return false
	}

	switch k {
	case keyBack: // Esc — collapse an open function, else back out to the sidebar
		m.goBack()
	case keySet: // 's' — toggle the Atomic service stat cards
		if m.tab == tabAtomic {
			m.hideCards = !m.hideCards
		}
	case keyNew: // 'N' — new function (Atomic tab)
		if m.tab == tabAtomic {
			m.chooser = &newChooser{kind: chooserFn}
		}
	case keyOpen: // Ctrl-O (or o) — open the active slice's URL
		m.openActiveURL()
	case key1, key2, key3, key4: // tabs switch only via the number keys now
		m.tab = int(k - key1)
		m.loadCached(m.tab)
	case keyRefresh:
		m.load(m.tab) // forced — bypasses the cache
		m.loadSlices()
		m.status = "refreshed"
	case keyUp:
		if m.sel[m.tab] > 0 {
			m.sel[m.tab]--
		}
	case keyDown:
		if m.sel[m.tab] < m.rowCount(m.tab)-1 {
			m.sel[m.tab]++
		}
	case keyEnter:
		m.act()
	case keyDelete:
		m.askDelete()
	}
	return false
}

// handleBackbone applies Backbone-explorer keys; returns true if it consumed k.
// Unconsumed keys (left/right/tab/1-4/refresh/quit) fall through to the global
// handler so main-tab navigation still works.
func (m *model) handleBackbone(k key) bool {
	// (A drill-down dump is handled generically by handleDetailKeys; this runs
	// only in the list view.)
	switch k {
	case keyUp:
		if m.bbSel > 0 {
			m.bbSel--
		}
	case keyDown:
		if m.bbSel < len(m.bbItems)-1 {
			m.bbSel++
		}
	case keyEnter:
		m.bbDrill()
	case keyPrevPrim:
		m.setPrim(-1)
	case keyNextPrim:
		m.setPrim(+1)
	case keySet:
		if m.bbPrim == primSecrets {
			m.input = &inputPrompt{
				label: "set secret (NAME=value):",
				run: func(s string) string {
					name, val, ok := strings.Cut(s, "=")
					name = strings.TrimSpace(name)
					if !ok || name == "" {
						return "✗ format is NAME=value"
					}
					if err := setSecret(name, val); err != nil {
						return "✗ " + err.Error()
					}
					return "set secret " + name
				},
			}
		}
	case keyDelete:
		if m.bbPrim == primSecrets && m.bbSel < len(m.bbItems) {
			name := m.bbItems[m.bbSel].name
			m.conf = &confirmAction{
				prompt: fmt.Sprintf("delete secret %q?", name),
				run: func() string {
					if err := deleteSecret(name); err != nil {
						return "✗ " + err.Error()
					}
					return "deleted secret " + name
				},
			}
		}
	default:
		return false
	}
	return true
}

// handleDetailKeys runs while a scrollable detail view is open (any tab). It
// captures every key: scroll, refresh (re-fetch), back, quit, and tab-switch
// (which closes the view first). Returns true to quit.
func (m *model) handleDetailKeys(k key) bool {
	switch k {
	case keyUp:
		if m.detail.scroll > 0 {
			m.detail.scroll--
		}
	case keyDown:
		if m.detail.scroll < len(m.detail.lines)-1 {
			m.detail.scroll++
		}
	case keyBack, keyLeft, keyEnter:
		m.detail = nil
	case keyRefresh:
		m.refreshDetail()
	case keyPrevPrim:
		m.detail = nil
		if m.tab == tabBackbone {
			m.setPrim(-1)
		}
	case keyNextPrim:
		m.detail = nil
		if m.tab == tabBackbone {
			m.setPrim(+1)
		}
	case keyTab:
		m.detail = nil
		m.focus = focusSidebar
	case key1, key2, key3, key4:
		m.detail = nil
		m.tab = int(k - key1)
		m.load(m.tab)
	case keyQuit:
		return true
	}
	return false
}

// refreshDetail re-runs the open view's reload closure (e.g. tail new logs).
func (m *model) refreshDetail() {
	if m.detail == nil || m.detail.reload == nil {
		return
	}
	title, lines, err := m.detail.reload()
	if err != nil {
		m.status = "✗ " + err.Error()
		return
	}
	m.detail.title, m.detail.lines = title, lines
	if m.detail.scroll >= len(lines) {
		m.detail.scroll = max(0, len(lines)-1)
	}
	m.status = "refreshed"
}

// applyInputByte applies one raw byte to the active inline input (the loop
// does the read so a resize can repaint while we wait).
func (m *model) applyInputByte(b byte) {
	switch {
	case b == 3 || b == 0x1b: // Ctrl-C / Esc → cancel
		m.input, m.deleting = nil, nil
		m.status = "cancelled"
	case b == '\r' || b == '\n':
		runFn, val, allowEmpty := m.input.run, strings.TrimSpace(m.input.buf), m.input.allowEmpty
		m.input, m.deleting = nil, nil
		if val == "" && !allowEmpty {
			m.status = "cancelled"
			return
		}
		m.status = runFn(val)
		m.load(m.tab)
	case b == 0x7f || b == 8: // backspace
		if n := len(m.input.buf); n > 0 {
			m.input.buf = m.input.buf[:n-1]
		}
	case b >= 0x20 && b < 0x7f: // printable ASCII
		m.input.buf += string(b)
	}
}

// tabCacheTTL bounds how long a tab's fetched data is reused before a tab switch
// re-fetches it. Mutations and explicit refresh always force a fresh load.
const tabCacheTTL = 8 * time.Second

// loadCached fetches a tab's data only if the cached copy is stale — this is what
// tab navigation uses, so flipping Slice↔Atomic↔Backbone doesn't re-hit the API
// every time. Forced paths (refresh, mutations, slice switch) call load directly.
func (m *model) loadCached(tab int) {
	if !m.loadedAt[tab].IsZero() && time.Since(m.loadedAt[tab]) < tabCacheTTL {
		return
	}
	m.load(tab)
}

// invalidateAll marks every tab's cache stale — used when the active slice
// changes, since all cached data belonged to the old slice.
func (m *model) invalidateAll() { m.loadedAt = [4]time.Time{} }

// load fetches the data for one tab (lazy — on switch / refresh / startup).
func (m *model) load(tab int) {
	m.loadErr[tab] = ""
	switch tab {
	case tabSlice:
		m.loadSlices() // keeps the sidebar fresh too
		m.loadSnaps()
		if rt, err := fetchRuntime(); err == nil {
			m.rt = rt // live memory for the overview header
		}
		if m.active != "" {
			if doc, err := fetchSliceDoc(m.active); err == nil {
				m.cfg = doc // current settings for the panel + configure pre-fill
				m.priceErr = ""
				if pr, err := fetchDocPrice(doc); err == nil {
					m.price = pr // itemized bill (#CLITUI1)
				} else {
					m.price, m.priceErr = nil, err.Error()
				}
			}
		}
	case tabAtomic:
		f, err := fetchFunctions()
		if err != nil {
			m.loadErr[tab] = err.Error()
		} else {
			m.fns = f
			m.loadFnMetrics()               // per-function RSS/req/err for the rail + detail cards
			m.triggers, _ = fetchTriggers() // queue/schedule/webhook bindings for the config card
			// No auto-selection: the detail pane stays empty until the user opens a
			// function with Enter. Drop a now-stale committed selection (e.g. the
			// function was deleted) and fall back out of the detail focus.
			if m.fnExp >= len(m.fns) {
				m.fnExp = -1
				if m.focus == focusDetail {
					m.focus = focusMain
				}
			} else if m.fnExp >= 0 {
				m.loadFnExpand() // keep the open function's metrics + logs fresh
			}
		}
	case tabBackbone:
		m.loadBackbone()
	case tabCanvas:
		// Canvas is a static help panel now — the memory census moved to the
		// Slice tab (#3), which loads m.rt itself above.
	}
	if n := m.rowCount(tab); m.sel[tab] >= n {
		m.sel[tab] = max(0, n-1)
	}
	if m.loadErr[tab] == "" { // only cache a clean load; errors retry next time
		m.loadedAt[tab] = time.Now()
	}
}

// rowCount = number of selectable rows on a tab (Backbone selects over secrets).
func (m *model) rowCount(tab int) int {
	switch tab {
	case tabSlice:
		return len(m.slices)
	case tabAtomic:
		return len(m.fns) + 1 // + the "[N] new function" row at the bottom
	}
	return 0 // Backbone manages its own cursor (bbSel) via handleBackbone
}

// act = Enter on the current tab.
func (m *model) act() {
	// Slice switching is the sidebar's job; on the Atomic tab Enter opens the
	// new-function chooser on the bottom row, otherwise it "expands" the selected
	// function — moving focus into its settable fields (element/trigger/schedule).
	if m.tab != tabAtomic {
		return
	}
	if m.sel[tabAtomic] >= len(m.fns) { // the "[N] new function" row
		m.chooser = &newChooser{kind: chooserFn}
		return
	}
	m.fnExp = m.sel[tabAtomic] // commit: the detail pane now shows this function
	m.fnExpLogs = nil
	m.loadFnExpand() // prime the committed function's metrics + logs
	m.focus = focusDetail
	m.detailSel = 0
	m.logsFull = false // each fresh open starts in the normal detail view
}

// handleFnDetail applies keys while focus is inside an Atomic function's detail:
// up/down move over its settable fields, Enter activates the selected one (the
// element/trigger/schedule setup — wired in #2), Esc returns to the rail. It is
// self-contained (like handleSidebar): tab-switch, refresh and quit still work;
// everything else is swallowed so the rail's hotkeys don't leak through.
func (m *model) handleFnDetail(k key) bool {
	switch k {
	case keyQuit:
		return true
	case keyUp:
		if m.detailSel > 0 {
			m.detailSel--
		}
	case keyDown:
		if m.detailSel < len(fnConfigKeys)-1 {
			m.detailSel++
		}
	case keyEnter:
		m.activateFnField()
	case keyFull: // 'f' — toggle full-screen logs for the open function
		m.logsFull = !m.logsFull
	case keySet: // 's' — toggle the service stat cards (also works while focused)
		m.hideCards = !m.hideCards
	case keyBack: // Esc — exit full-screen logs first, else back out to the rail
		if m.logsFull {
			m.logsFull = false
		} else {
			m.focus = focusMain
		}
	case keyRefresh:
		m.load(m.tab)
		m.status = "refreshed"
	case key1, key2, key3, key4:
		m.tab = int(k - key1)
		m.focus = focusMain
		m.logsFull = false
		m.loadCached(m.tab)
	}
	return false
}

// activateFnField is invoked by Enter on a field. These are display-only (#2):
// element is set at deploy, triggers/schedules are registered via the CLI — so
// Enter surfaces the command that changes the field rather than editing in place.
func (m *model) activateFnField() {
	if m.detailSel < 0 || m.detailSel >= len(fnConfigKeys) {
		return
	}
	switch fnConfigKeys[m.detailSel] {
	case "element":
		m.status = "element is set at deploy: drift atomic deploy --element <name>"
	case "trigger":
		m.status = "register: drift atomic trigger register queue <name> --queue <q> --target <url>"
	case "schedule":
		m.status = `register: drift atomic trigger register schedule <name> --cron "…" --target <url>`
	}
}

// goBack = Esc in the main pane: collapse an open function first, otherwise
// hand focus back to the slices sidebar.
func (m *model) goBack() {
	m.focus = focusSidebar
}

// askDelete = 'd' on the current tab; arms a confirm. (Backbone's secret delete
// is handled in handleBackbone, scoped to the Secrets primitive.)
func (m *model) askDelete() {
	if m.tab == tabAtomic {
		if i := m.sel[tabAtomic]; i < len(m.fns) {
			name := m.fns[i].FunctionName
			m.conf = &confirmAction{
				prompt: fmt.Sprintf("delete function %q?", name),
				run: func() string {
					if err := deleteFunction(name); err != nil {
						return "✗ " + err.Error()
					}
					return "deleted function " + name
				},
			}
		}
	}
}

// (render lives in layout.go — the full-screen 2-column compositor.)

// hint is one keybind row: the key (rendered bracketed) and what it does.
type hint struct{ key, desc string }

// hintGroup is one titled column in the header keybind guide.
type hintGroup struct {
	title string
	hints []hint
}

// hintGroups returns the context-sensitive keybinds as titled groups: CONTEXT
// (the current pane), then the always-there NAVIGATION globals. Modals own the
// pane wholesale, so they return only their CONTEXT group. (^S status / ^O open
// still work — the open hint now lives as a nudge above the slice URL.)
func (m *model) hintGroups() []hintGroup {
	nav := hintGroup{"NAVIGATION", []hint{
		{"tab", "switch panel"},
		{"1-4", "switch tab"},
		{"esc", "back"},
		{"^C", "quit"},
	}}
	only := func(hs []hint) []hintGroup { return []hintGroup{{"CONTEXT", hs}} }
	switch {
	case m.input != nil:
		return only([]hint{{"type", "enter value"}, {"enter", "submit"}, {"esc", "cancel"}})
	case m.chooser != nil:
		return only([]hint{{"↑/↓", "choose"}, {"enter", "select"}, {"esc", "cancel"}})
	case m.explorer != nil:
		return only([]hint{{"↑/↓", "move"}, {"enter", "open / deploy"}, {"←", "up a level"}, {"g", "go to path"}, {"esc", "cancel"}})
	case m.form != nil:
		apply := "create"
		if m.form.resize {
			apply = "apply"
		}
		return only([]hint{{"↑/↓", "move"}, {"←/→", "adjust"}, {"enter", "edit / fold"}, {"tab", apply}, {"esc", "cancel"}})
	}
	var ctx []hint
	switch {
	case m.detail != nil:
		ctx = []hint{{"↑/↓", "scroll"}, {"r", "refresh"}, {"esc", "back"}}
	case m.focus == focusSidebar:
		ctx = []hint{{"↑/↓", "move"}, {"enter", "open slice"}, {"o", "open URL"}, {"N", "new slice"}, {"D", "delete slice"}, {"r", "refresh"}}
	case m.focus == focusDetail && m.logsFull:
		ctx = []hint{{"f", "exit logs"}, {"s", "cards"}, {"esc", "back"}}
	case m.focus == focusDetail:
		ctx = []hint{{"↑/↓", "select field"}, {"enter", "set"}, {"f", "full logs"}, {"s", "cards"}, {"esc", "back"}}
	case m.tab == tabAtomic:
		ctx = []hint{{"↑/↓", "move"}, {"enter", "expand"}, {"N", "new function"}, {"s", "cards"}, {"d", "delete"}, {"r", "refresh"}}
	case m.tab == tabBackbone && m.bbPrim == primSecrets:
		ctx = []hint{{"↑/↓", "move"}, {"s", "set"}, {"d", "delete"}, {"[ ]", "primitive"}, {"r", "refresh"}}
	case m.tab == tabBackbone:
		ctx = []hint{{"↑/↓", "move"}, {"enter", "open"}, {"[ ]", "primitive"}, {"r", "refresh"}}
	case m.tab == tabSlice:
		ctx = []hint{{"↑/↓", "move"}, {"s", "snapshot"}, {"c", "configure"}, {"w", "download"}, {"d", "delete"}}
	default:
		ctx = []hint{{"↑/↓", "move"}}
	}
	return []hintGroup{{"CONTEXT", ctx}, nav}
}

// hintLines renders the grouped keybinds as a borderless, bracket-aligned guide:
// the titled groups flow into as many side-by-side columns as `width` allows,
// wrapping onto new rows when they don't fit.
func (m *model) hintLines(width int) []string {
	groups := m.hintGroups()
	keyW := 0
	for _, g := range groups {
		for _, h := range g.hints {
			if w := vlen("[" + h.key + "]"); w > keyW {
				keyW = w
			}
		}
	}
	rowOf := func(h hint) string {
		kb := "[" + h.key + "]"
		return " " + bold(kb) + strings.Repeat(" ", keyW-vlen(kb)+2) + dim(h.desc)
	}
	// twoCol lays a group's hints out in two columns (column-major: the first half
	// runs down the left, the rest down the right) rather than one tall stack.
	twoCol := func(cells []string) []string {
		if len(cells) <= 1 {
			return cells
		}
		half := (len(cells) + 1) / 2 // left column takes the extra on odd counts
		leftW := 0
		for k := 0; k < half; k++ {
			if v := vlen(cells[k]); v > leftW {
				leftW = v
			}
		}
		rows := make([]string, 0, half)
		for r := 0; r < half; r++ {
			if ri := r + half; ri < len(cells) {
				rows = append(rows, pad(cells[r], leftW)+"  "+cells[ri])
			} else {
				rows = append(rows, cells[r]) // last odd cell — no right column
			}
		}
		return rows
	}
	blocks := make([][]string, len(groups))
	for i, g := range groups {
		cells := make([]string, len(g.hints))
		for j, h := range g.hints {
			cells[j] = rowOf(h)
		}
		blocks[i] = append([]string{dim(g.title)}, twoCol(cells)...)
	}

	var out []string
	for i := 0; i < len(blocks); {
		// Greedily pack blocks into one row while they fit.
		var rowCols [][]string
		w := 0
		for i < len(blocks) {
			need := maxLineW(blocks[i])
			if len(rowCols) > 0 {
				need += 3
			}
			if len(rowCols) > 0 && w+need > width {
				break
			}
			rowCols = append(rowCols, blocks[i])
			w += need
			i++
		}
		h := 0
		for _, c := range rowCols {
			if len(c) > h {
				h = len(c)
			}
		}
		for r := 0; r < h; r++ {
			parts := make([]string, len(rowCols))
			for ci, c := range rowCols {
				cell := ""
				if r < len(c) {
					cell = c[r]
				}
				parts[ci] = pad(cell, maxLineW(c))
			}
			out = append(out, strings.Join(parts, "   "))
		}
		out = append(out, "") // gap between wrapped rows
	}
	if n := len(out); n > 0 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

// maxLineW = the widest visible line in a block.
func maxLineW(lines []string) int {
	w := 0
	for _, ln := range lines {
		if v := vlen(ln); v > w {
			w = v
		}
	}
	return w
}

// keyHints flattens the grouped keybinds onto one line (compact-mode footer).
func (m *model) keyHints() string {
	var parts []string
	for _, g := range m.hintGroups() {
		for _, h := range g.hints {
			parts = append(parts, "["+h.key+"] "+h.desc)
		}
	}
	return strings.Join(parts, " · ")
}

// Atomic table column widths (the FUNCTION column flexes to fill the pane).
const (
	colMethod = 7
	colLang   = 4
	colReqs   = 7
	colErr    = 6
	colAvg    = 8
	colRSS    = 11
)

func (m *model) renderAtomic(b *strings.Builder) {
	if len(m.fns) == 0 {
		b.WriteString("  No functions deployed yet.\r\n\r\n")
		m.renderNewFnRow(b)
		return
	}
	// [f] full-screen logs: the open function's live tail takes the whole pane.
	if m.logsFull && m.focus == focusDetail && m.fnExp >= 0 && m.fnExp < len(m.fns) {
		m.renderFullLogs(b)
		return
	}
	// Service-wide summary cards across the top (full width), above the rail +
	// detail — toggled with [s]. They sit on the function/traffic/trigger totals
	// for the whole Atomic service; the rail + detail below drill into one function.
	headerUsed := 0
	if !m.hideCards {
		header := statCards(m.contentW, 2, m.atomicServiceCards())
		for _, ln := range header {
			b.WriteString(ln + "\r\n")
		}
		b.WriteString("\r\n")
		headerUsed = len(header) + 1
	}

	// Left rail: one entry per function (METHOD route) + the "[N] new function"
	// row. Right pane: the opened function's live metadata + logs (fnExpandLines
	// on m.fnExp) — shown only once Enter commits a selection. (The language moved
	// out of the rail to the detail card; the rail stays narrow.)
	railW := 32
	if railW > m.contentW/2 {
		railW = m.contentW / 2
	}
	if railW < 16 {
		railW = 16
	}

	var rail []string
	for i, f := range m.fns {
		// Org-only routing: the element is never a route segment.
		route := "/" + f.FunctionName
		routeW := railW - 1 - colMethod - 1 // " METHOD route"
		if routeW < 6 {
			routeW = 6
		}
		if i == m.sel[tabAtomic] {
			plain := fmt.Sprintf(" %-*s %-*s", colMethod, f.Method, routeW, trunc(route, routeW))
			rail = append(rail, rev(pad(truncVis(plain, railW), railW)))
		} else {
			rail = append(rail, fmt.Sprintf(" %s%-*s%s %-*s",
				cOrange, colMethod, f.Method, cReset, routeW, trunc(route, routeW)))
		}
	}
	if m.sel[tabAtomic] == len(m.fns) { // the "[N] new function" row
		rail = append(rail, rev(pad(" [N] new function", railW)))
	} else {
		rail = append(rail, "  "+cOrange+"[N]"+cReset+" new function")
	}

	// The detail pane stays empty until a function is opened: navigate the rail,
	// then Enter commits the selection (act → focusDetail) and reveals the details.
	var detail []string
	if m.focus == focusDetail {
		detail = m.fnExpandLines(m.contentW - railW - 3)
	}
	if len(detail) == 0 {
		detail = []string{"", dim("  ↑/↓ to browse functions · Enter to open one")}
	}

	// A thin vertical rule separates the rail from the borderless details — no box,
	// just a hairline that runs the full height of the pane (even past the
	// rail/detail content) so the split reads as a real column divider. Drawn in
	// the muted grey ink so it sits a touch behind the content.
	div := cGrey + "│" + cReset
	n := max(len(rail), len(detail), m.contentH-headerUsed)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(rail) {
			l = rail[i]
		}
		if i < len(detail) {
			r = detail[i]
		}
		fmt.Fprintf(b, "%s %s %s\r\n", pad(truncVis(l, railW), railW), div, r)
	}
}

// renderFullLogs draws the open function's live log tail across the whole pane
// ([f] toggles it). A one-line header names the function, then the most recent
// lines that fit are shown (tail-style), so new logs keep streaming at the bottom.
func (m *model) renderFullLogs(b *strings.Builder) {
	f := m.fns[m.fnExp]
	// Org-only routing: the element is never a route segment.
	route := "/" + f.FunctionName
	fmt.Fprintf(b, "  %s %s  %s   %s\r\n\r\n",
		dim("logs"), cGreen+"●"+cReset, bold(route), dim("[f] exit full-screen"))

	lines := m.fnLogLines(m.contentW - 2)
	avail := m.contentH - 2 // minus the header + its blank line
	if avail < 1 {
		avail = 1
	}
	start := 0
	if len(lines) > avail { // tail: keep the newest lines on screen
		start = len(lines) - avail
	}
	for _, ln := range lines[start:] {
		b.WriteString("  " + ln + "\r\n")
	}
}

// renderNewFnRow draws the "[N] new function" affordance — the row after the
// last function (selectable at index len(m.fns); 'N' opens it from anywhere).
func (m *model) renderNewFnRow(b *strings.Builder) {
	if m.sel[tabAtomic] == len(m.fns) {
		fmt.Fprint(b, " \x1b[7m [N] new function \x1b[0m\r\n")
	} else {
		fmt.Fprintf(b, "  %s[N]%s new function\r\n", cOrange, cReset)
	}
}

// renderDetail draws a scrollable detail view (Backbone dump / Atomic
// metrics+logs) with a windowed viewport and a position indicator.
func (m *model) renderDetail(b *strings.Builder) {
	d := m.detail
	fmt.Fprintf(b, "  \x1b[1m%s\x1b[0m\r\n\r\n", d.title)
	win := m.viewport()
	end := min(d.scroll+win, len(d.lines))
	for _, ln := range d.lines[d.scroll:end] {
		fmt.Fprintf(b, "  %s\r\n", truncVis(ln, m.contentW-2))
	}
	if len(d.lines) > win {
		fmt.Fprintf(b, "\r\n  \x1b[2m[%d-%d of %d]\x1b[0m\r\n", d.scroll+1, end, len(d.lines))
	}
}

func (m *model) renderCanvas(b *strings.Builder) {
	b.WriteString("  Canvas serves your static site at the slice root, same-origin with\r\n")
	b.WriteString("  your functions (no CORS). Deploy a directory:\r\n\r\n")
	b.WriteString("    \x1b[36mdrift canvas deploy ./site --route /\x1b[0m\r\n\r\n")
}

// renderMemoryCensus draws the slice's real memory picture from the census,
// measured against the slice's DECLARED function_memory — never the pod's
// real cgroup ceiling. The platform pads that real ceiling with its own
// invisible runtime-startup headroom (e.g. compiled functions get a fixed
// allowance to boot) that the user never declared and was never meant to
// see; a function's own working set is what's shown here.
//
// "used" is footprintBytes — non-reclaimable memory only, same as the
// summary card above. Raw cgroup usage includes page cache (the slice's own
// NoSQL/blob/SQL data cached in RAM for speed), which costs nothing to hold
// and disappears instantly under real pressure. Counting it here would make
// a slice that's simply cached its own data look like it's over budget for
// something it doesn't control — the same category of noise as the
// invisible runtime headroom the limit itself already excludes.
func (m *model) renderMemoryCensus(b *strings.Builder) {
	rt := m.rt
	fmt.Fprintf(b, "  \x1b[1mSlice memory\x1b[0m  %s\r\n", dim("census · against your declared limit"))
	used := rt.footprintBytes()
	if rt.FunctionMemoryLimitBytes > 0 {
		limit := rt.FunctionMemoryLimitBytes
		pct := float64(used) / float64(limit) * 100
		col := cGreen
		if pct >= 90 {
			col = cRed
		} else if pct >= 80 {
			col = cOrange
		}
		free := limit - used
		if free < 0 {
			free = 0
		}
		fmt.Fprintf(b, "  %s  %s / %s  %s\r\n",
			meter(used, limit, col, 20),
			mib(used), mib(limit),
			dim(fmt.Sprintf("(%.0f%%, %s free)", pct, mib(free))))
	} else {
		fmt.Fprintf(b, "  %s resident  %s\r\n", mib(used), dim("(no declared limit visible)"))
	}
	b.WriteString("\r\n")
	row2 := func(label, val, note string) {
		fmt.Fprintf(b, "   %s%s  %s\r\n", pad(dim(label), 13), val, dim(note))
	}
	row2("working set", mib(rt.AnonymousBytes), "anonymous — unreclaimable")
	row2("file-backed", mib(rt.FileBackedBytes), "mmap (LMDB, .so)")
	if rt.CgroupKnown {
		row2("page cache", mib(rt.CgroupFileBytes), "your data, cached — free to reclaim, not counted above")
	}
	row2("idle floor", mib(rt.AnonymousFloorBytes), "min anonymous since boot")
	row2("peak RSS", mib(rt.PeakRSSBytes), "")
	lvl := rt.MemoryPressure
	if lvl < 0 {
		lvl = 0
	} else if lvl > 2 {
		lvl = 2
	}
	pword, pcol := []string{"none", "soft", "hard"}[lvl], cGreen
	if lvl == 2 {
		pcol = cRed
	} else if lvl == 1 {
		pcol = cOrange
	}
	row2("pressure", pcol+pword+cReset, "")
}

// row renders one selectable line: reverse-video + ▶ when selected.
func row(b *strings.Builder, selected bool, text string) {
	if selected {
		fmt.Fprintf(b, "  \x1b[7m▶ %s \x1b[0m\r\n", text)
	} else {
		fmt.Fprintf(b, "    %s\r\n", text)
	}
}

// langLabel maps a runtime to a short (≤ colLang) code so it never overflows the
// LANG column and shifts the table. Unknown languages are clipped, not widened.
func langLabel(l string) string {
	switch l {
	case "", "native", "go":
		return "go"
	case "python", "py":
		return "py"
	case "node", "nodejs", "javascript", "js":
		return "js"
	case "ruby", "rb":
		return "rb"
	case "deno":
		return "deno"
	case "bun":
		return "bun"
	default:
		return trunc(l, colLang)
	}
}

func mib(b int64) string {
	return fmt.Sprintf("%.1f MiB", float64(b)/(1024*1024))
}
