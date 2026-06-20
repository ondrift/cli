package portal

// Full-screen, k9s-style layout: a top navbar with the A·B·C triad, a left
// sidebar of slices (TAB to focus, ↑/↓ to switch), a vertical divider, and the
// main pane (a brand-coloured tab strip + the active tab's content). Everything
// is drawn with absolute cursor positioning onto the alt-screen buffer.

import (
	"fmt"
	"strings"

	sliceCmd "github.com/ondrift/cli/cmd/slice"
	"github.com/ondrift/cli/common"
)

const sidebarW = 26

// Minimum terminal the dashboard needs to lay out its framed panels + sidebar +
// tab strip without garbling. Below this we show a btop-style "too small" screen
// instead of rendering (see renderTooSmall + the gate at the top of render()).
const (
	minCols = 130
	minRows = 35
)

// ABC brand truecolor — Atomic #f1a006 · Backbone #8269eb · Canvas #10b981.
const (
	cReset   = "\x1b[0m"
	cOrange  = "\x1b[38;2;241;160;6m"
	cPurple  = "\x1b[38;2;130;105;235m"
	cGreen   = "\x1b[38;2;16;185;129m"
	cBlue    = "\x1b[38;2;38;139;210m" // Slice (the container pillar)
	cGrey    = "\x1b[38;2;90;90;90m"   // backgrounded (unfocused) panel ink
	cRed     = "\x1b[38;2;229;83;75m"  // destructive (delete) accent
	bgOrange = "\x1b[48;2;241;160;6m"  // Create button fill (pressed)
	bgBlue   = "\x1b[48;2;38;139;210m" // new-slice chooser fill (pressed)
)

const (
	focusMain    = iota // the active tab's main pane (rail / list)
	focusSidebar        // the slices sidebar (TAB)
	focusDetail         // inside the Atomic function detail: its settable fields
)

func bold(s string) string { return "\x1b[1m" + s + cReset }
func dim(s string) string  { return "\x1b[2m" + s + cReset }
func rev(s string) string  { return "\x1b[7m" + s + cReset }
func cyan(s string) string { return "\x1b[36m" + s + cReset }

// statCard is one titled group for statCards.
type statCard struct {
	title string
	rows  [][2]string // {key, value}
	hl    bool        // orange outline — the card belongs to the in-focus element
	hlRow int         // the selected (settable) row within the card — the ▸ cursor (-1 = none)
}

// statCards renders the groups as a row of subtle dim-bordered stat cards (title
// in the top edge, " key  value" rows inside), all sharing the tallest card's
// height so their borders align. Each card floors to its title width — else a
// long title overruns the body and shifts the cards to its right; if the row
// still overflows `width`, the FIRST card absorbs the deficit (its values
// truncate). `gap` is the spaces between cards. Used by the Atomic details pane
// and the Slice / Backbone summary rows.
func statCards(width, gap int, cards []statCard) []string {
	if len(cards) == 0 {
		return nil
	}
	maxW := func(c statCard, idx int) int {
		w := 0
		for _, kv := range c.rows {
			if l := vlen(kv[idx]); l > w {
				w = l
			}
		}
		return w
	}
	titleW := func(t string) int { return vlen(" "+t+" ") + 2 } // " title " + the two corners
	ks := make([]int, len(cards))
	ws := make([]int, len(cards))
	ch, total := 0, gap*(len(cards)-1)
	for i, c := range cards {
		ks[i] = maxW(c, 0)
		ws[i] = maxi(2+1+ks[i]+1+maxW(c, 1), titleW(c.title)) // borders + " key value"
		total += ws[i]
		if len(c.rows) > ch {
			ch = len(c.rows)
		}
	}
	if over := total - width; over > 0 {
		ws[0] = maxi(ks[0]+6, ws[0]-over)
	} else if slack := width - total; slack > 0 {
		// Stretch the cards to span the FULL container width: spread the leftover
		// space evenly (the remainder to the leftmost cards). Each card keeps at
		// least its natural width, so content never truncates — it just gains right
		// padding. Result: any number of cards always fills the row edge to edge.
		base, extra := slack/len(cards), slack%len(cards)
		for i := range ws {
			ws[i] += base
			if i < extra {
				ws[i]++
			}
		}
	}
	col := func(c statCard, kw, cw int) []string {
		inner := maxi(1, cw-2)
		// Two states: a dim outline (┌─┐) while idle/browsing, and the orange outline
		// once the element is in focus (hl) — the "now you're working on this" signal.
		// hlRow (>= 0) marks the settable row with a bold ▸ cursor.
		bc, tc := dim, dim
		if c.hl {
			bc = func(s string) string { return cOrange + s + cReset }
			tc = func(s string) string { return cOrange + "\x1b[1m" + s + cReset }
		}
		t := " " + c.title + " "
		lines := []string{bc("┌") + tc(t) + bc(strings.Repeat("─", maxi(0, inner-vlen(t)))+"┐")}
		for r := 0; r < ch; r++ {
			content := ""
			if r < len(c.rows) {
				valW := maxi(1, inner-kw-2)
				if c.hl && c.hlRow >= 0 && r == c.hlRow {
					// the cursor row: bold ▸ + bold key, value plain — pops in mono too.
					content = cOrange + "\x1b[1m▸" + pad(c.rows[r][0], kw) + cReset + " " + truncVis(c.rows[r][1], valW)
				} else {
					content = " " + dim(pad(c.rows[r][0], kw)) + " " + truncVis(c.rows[r][1], valW)
				}
			}
			lines = append(lines, bc("│")+pad(truncVis(content, inner), inner)+bc("│"))
		}
		return append(lines, bc("└"+strings.Repeat("─", inner)+"┘"))
	}
	cols := make([][]string, len(cards))
	for i, c := range cards {
		cols[i] = col(c, ks[i], ws[i])
	}
	out := make([]string, ch+2) // top border + ch content rows + bottom border
	for i := range out {
		for j := range cols {
			if j > 0 {
				out[i] += strings.Repeat(" ", gap)
			}
			out[i] += cols[j][i]
		}
	}
	return out
}

// stripSGR drops ANSI SGR colour/style escapes from s (content carries only SGR;
// cursor moves are added later by at()).
func stripSGR(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// greyOut renders s in a single muted grey, discarding its own colours — the
// "backgrounded" look for the panel that doesn't currently have focus.
func greyOut(s string) string {
	if s == "" {
		return ""
	}
	return cGrey + stripSGR(s) + cReset
}

func tabFG(i int) string {
	switch i {
	case tabAtomic:
		return cOrange
	case tabBackbone:
		return cPurple
	case tabCanvas:
		return cGreen
	}
	return cBlue
}

// at writes s at a 1-indexed (row, col) using a cursor-position escape.
func at(b *strings.Builder, row, col int, s string) {
	fmt.Fprintf(b, "\x1b[%d;%dH%s", row, col, s)
}

// renderTooSmall paints a btop-style "terminal too small" screen, shown instead
// of the dashboard until the window reaches minCols × minRows. The deficient
// dimension shows red, the satisfied one green.
func (m *model) renderTooSmall() {
	colour := func(ok bool, n int) string {
		c := "\x1b[31m" // red — below minimum
		if ok {
			c = "\x1b[32m" // green — satisfied
		}
		return fmt.Sprintf("%s%d\x1b[0m", c, n)
	}
	wOK, hOK := m.cols >= minCols, m.rows >= minRows
	lines := [][2]string{ // {plain text for centring width, rendered with colour}
		{"drift", "\x1b[1m\x1b[38;5;208mdrift\x1b[0m"},
		{"", ""},
		{"Terminal too small to render the dashboard.", "\x1b[2mTerminal too small to render the dashboard.\x1b[0m"},
		{"", ""},
		{fmt.Sprintf("current   %d x %d", m.cols, m.rows),
			fmt.Sprintf("current   %s x %s", colour(wOK, m.cols), colour(hOK, m.rows))},
		{fmt.Sprintf("required  %d x %d", minCols, minRows),
			fmt.Sprintf("\x1b[2mrequired  %d x %d\x1b[0m", minCols, minRows)},
		{"", ""},
		{"resize the window  -  q to quit", "\x1b[2mresize the window  -  q to quit\x1b[0m"},
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[2J\x1b[H") // synchronized output — see render()
	top := (m.rows - len(lines)) / 2
	if top < 0 {
		top = 0
	}
	for i, ln := range lines {
		col := (m.cols-len(ln[0]))/2 + 1
		if col < 1 {
			col = 1
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s", top+i+1, col, ln[1])
	}
	b.WriteString("\x1b[?2026l")
	fmt.Print(b.String())
}

func (m *model) render() {
	// Gate: don't render the dashboard into a window too small to hold it —
	// show the size requirement instead (btop-style). Covers every repaint path
	// (resize watcher, log ticker, key loop) since they all funnel through here.
	if m.cols < minCols || m.rows < minRows {
		m.renderTooSmall()
		return
	}
	w, h := m.cols, m.rows
	if w < 50 {
		w = 50
	}
	if h < 14 {
		h = 14
	}
	const sbW = sidebarW
	mainCol := sbW + 3 // 1 col sidebar shadow + 1 gap, then the main panel
	mainW := w - mainCol
	if mainW < 20 {
		mainW = 20
	}

	var b strings.Builder
	// Synchronized output (DEC mode 2026): the terminal buffers this whole frame
	// and swaps it atomically, so the clear-then-redraw never flickers — matters
	// most under trackpad scroll (a burst of repaints). Terminals without 2026
	// ignore the markers. Closed at the end of render().
	b.WriteString("\x1b[?2026h\x1b[2J")

	// Header: the Tinos "drift" wordmark when there's vertical room; otherwise a
	// compact one-line navbar. user · slice sits to the right either way.
	top := 3
	keysInHeader := false
	if h >= len(driftLogo)+16 {
		for i, ln := range driftLogo {
			at(&b, 1+i, 2, "\x1b[0m"+ln) // default fg → adapts to light/dark
		}
		// The brand's two underlines, a couple of rows below the wordmark (clear
		// of the f/t descenders): a tri-colour bar in equal thirds under the
		// "dri" run (cols 3-20: Atomic orange · Backbone purple · Canvas green),
		// and a separate ink bar under the "t" (no bar under "f"). (Tied to the
		// 9-row logo + its glyph columns; re-run tools/driftlogo.py on regen.)
		ulRow := len(driftLogo) + 0
		at(&b, ulRow, 4, cOrange+strings.Repeat("▀", 9)+cPurple+strings.Repeat("▀", 8)+cGreen+strings.Repeat("▀", 4)+cReset)
		at(&b, ulRow, 30, "\x1b[0m"+strings.Repeat("▀", 9)) // ink bar under the "t"
		top = ulRow + 2                                     // a clean gap below the wordmark — no divider rule

		// Fill the empty space to the right of the wordmark with a minimalist,
		// borderless keybind guide (grouped CONTEXT + NAVIGATION). Moving the hints
		// up here frees the bottom row so the panels reach all the way down.
		logoW := 0
		for _, ln := range driftLogo {
			if v := vlen(ln); v > logoW {
				logoW = v
			}
		}
		if logoW < 40 { // clear the underline + ink bar, not just the glyphs
			logoW = 40
		}
		hintsCol := logoW + 4
		if hintsW := w - hintsCol; hintsW >= 22 {
			for i, ln := range m.hintLines(hintsW) {
				if 1+i >= top { // never spill into the panels
					break
				}
				at(&b, 1+i, hintsCol, truncVis(ln, hintsW))
			}
			keysInHeader = true
		}
		// else: keysInHeader stays false → the compact bottom keybind line is used.
	} else {
		at(&b, 1, 1, m.navbar(w))
		at(&b, 2, 1, dim(strings.Repeat("─", w)))
	}

	// Panel height: with the keybinds in the header, the bottom hint row is gone,
	// so the panels run down to just above the status row (h); otherwise they sit
	// above both the status line (h-1) and the keybind line (h).
	boxH := h - top - 2
	if keysInHeader {
		boxH = h - top
	}
	if boxH < 6 {
		boxH = 6
	}

	// "What's new" strip: a small box above the MAIN panel (only in the roomy logo
	// header, and only when there's something to show). It pushes the main panel
	// down — the sidebar keeps full height — with one gap row beneath it carrying
	// the URL nudge. mainTop/mainBoxH are the main panel's geometry; the sidebar
	// still uses top/boxH.
	var newsContent []string
	if keysInHeader && len(m.news) > 0 {
		newsContent = m.newsLines(2) // up to 2 latest, one line each
	}
	mainTop, mainBoxH, stripH := top, boxH, 0
	if len(newsContent) > 0 {
		stripH = len(newsContent) + 2 // + top/bottom border
		if boxH-stripH-1 >= 6 {       // only if the panel stays usable underneath
			mainTop = top + stripH + 1
			mainBoxH = boxH - stripH - 1
		} else {
			stripH, newsContent = 0, nil // too short — drop the strip
		}
	}

	m.contentW = mainW - 4    // inside the main box: │ + pad … pad + │
	m.contentH = mainBoxH - 3 // inside, minus the leading blank + the bottom-border tabs
	m.sideRows = boxH - 2     // sidebar content rows (top + bottom border)

	// Focus affordance: the panel WITHOUT focus is greyed out ("backgrounded").
	// When the status popup is open the whole UI dims so the popup pops.
	dimmed := m.platform != nil
	sideFocused := m.focus == focusSidebar && !dimmed
	// focusDetail is a sub-state of the main pane being active (inside a function's
	// settable fields) — it must keep the main panel lit, not background it.
	mainFocused := (m.focus == focusMain || m.focus == focusDetail) && !dimmed

	// In its transient modes the right pane retitles + recolours (green "New
	// slice" while creating, red "Deleting <name>" while confirming) with no tabs,
	// name, or URL. In the normal view drawPanel gets no title — the slice name is
	// incrusted top-RIGHT, the tabs top-LEFT, and the URL bottom-LEFT (below).
	creating := m.chooser != nil || m.explorer != nil || m.form != nil
	panelTitle, mainColor := "", tabFG(m.tab)
	switch {
	case m.deleting != nil:
		panelTitle, mainColor = "Deleting "+m.deleting.name, cRed
	case m.form != nil && m.form.resize:
		panelTitle, mainColor = "Configure "+m.form.name, cBlue
	case m.creatingFunction():
		panelTitle, mainColor = "New function", cOrange
	case creating:
		panelTitle, mainColor = "New slice", cGreen
	}

	// Two panels: SLICES sidebar (left, full height) + the active slice's pane
	// (right), the latter pushed below the "what's new" strip when present.
	m.drawPanel(&b, top, 1, sbW, boxH, "SLICES", cBlue, sideFocused, !sideFocused, m.sidebarInner())
	if stripH > 0 {
		m.drawPanel(&b, top, mainCol, mainW, stripH, "WHAT'S NEW", cPurple, false, false, newsContent)
	}
	m.drawPanel(&b, mainTop, mainCol, mainW, mainBoxH, panelTitle, mainColor, mainFocused, !mainFocused, m.mainLines())

	if !creating && m.deleting == nil {
		// Top border: tabs (the first one is the slice name) top-LEFT, the clickable
		// slice URL inset top-RIGHT.
		rightBound := mainCol + mainW - 1 // the ┐ column
		if url := m.sliceURL(); url != "" {
			urlStart := mainCol + mainW - 1 - vlen(url) - 2
			if urlStart > mainCol+10 { // keep room for at least a stub of tabs
				link := "\x1b[4m" + cyan(url) // underlined cyan = clickable link
				if !mainFocused {
					link = greyOut(url)
				}
				at(&b, mainTop, urlStart, " "+link+" ")
				rightBound = urlStart
				// A small nudge in the gap row above, pointing down at the URL.
				if nudge := "Cmd+O to open this ↓"; mainTop-1 >= 1 {
					nudgeStart := urlStart + 1 + vlen(url) - vlen(nudge)
					if nudgeStart < mainCol {
						nudgeStart = mainCol
					}
					at(&b, mainTop-1, nudgeStart, dim(nudge))
				}
			}
		}
		tabSeg := m.tabStrip() + "  " // extra padding after the last tab
		if !mainFocused {
			tabSeg = greyOut(tabSeg)
		}
		tabCol := mainCol + 2
		if maxTabW := rightBound - 1 - tabCol; maxTabW > 0 {
			at(&b, mainTop, tabCol, truncVis(tabSeg, maxTabW))
		}
	}

	// Footer: status on the left of the last row, "logged in as X" on the right.
	// The compact bottom keybind line only appears when the header keys panel
	// couldn't be drawn.
	if keysInHeader {
		at(&b, h, 1, m.footerStatus())
		login := "logged in as " + m.user
		at(&b, h, w-vlen(login)-1, dim(login))
	} else {
		at(&b, h-1, 1, m.footerStatus())
		at(&b, h, 1, dim(truncVis(m.keyHints(), w-1)))
	}

	if dimmed { // the Ctrl-S status popup sits on top of the dimmed UI
		m.drawStatusPopup(&b)
	}

	b.WriteString("\x1b[?2026l") // end synchronized update → atomic frame swap (no flicker)
	fmt.Print(b.String())
}

// drawPanel renders a sharp-cornered, titled box (border in color, brighter
// title when focused) and places content inside. When muted, the whole panel —
// border, title and content — is greyed out as the "backgrounded" (unfocused)
// pane, overriding the content's own colours.
func (m *model) drawPanel(b *strings.Builder, row, col, w, h int, title, color string, focused, muted bool, content []string) {
	mute := func(s string) string { return s }
	if muted {
		color = cGrey
		mute = greyOut
	}
	titleSeg := ""
	if title != "" {
		t := dim(title)
		switch {
		case muted:
			t = greyOut(title)
		case focused:
			t = bold(title)
		}
		titleSeg = " " + t + " "
	}
	fill := w - 3 - vlen(titleSeg) // ┌ ─ <title> <fill> ┐
	if fill < 0 {
		fill = 0
	}
	at(b, row, col, color+"┌─"+cReset+titleSeg+color+strings.Repeat("─", fill)+"┐"+cReset)
	for i := 1; i < h-1; i++ {
		at(b, row+i, col, color+"│"+cReset)
		at(b, row+i, col+w-1, color+"│"+cReset)
		if ci := i - 1; ci < len(content) {
			at(b, row+i, col+2, truncVis(mute(content[ci]), w-4))
		}
	}
	at(b, row+h-1, col, color+"└"+strings.Repeat("─", w-2)+"┘"+cReset)
}

// triad is the three-square A·B·C brand mark.
func triad() string {
	return cOrange + "■" + cPurple + "■" + cGreen + "■" + cReset
}

// meter is a solid-fill bar (btop-style, no gradient): a brand-coloured run of
// blocks over a dim track, sized to used/capacity across width cells.
func meter(used, capacity int64, color string, width int) string {
	filled := 0
	if capacity > 0 {
		filled = int(float64(used) / float64(capacity) * float64(width))
	}
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return color + strings.Repeat("█", filled) + cReset + dim(strings.Repeat("░", width-filled))
}

// button renders a brand "toy" button: a sharp four-edged box that sits RAISED
// on a hard offset shadow (the brand's `0 4px 0`), and SLAMS FLAT — drops a row
// into the shadow and fills with its colour — when focused. Disabled = greyed,
// no shadow. Always 4 rows tall so the layout doesn't jump as it presses.
func button(label, fg, bg string, focused, disabled bool) []string {
	inner := " " + label + " "
	w := vlen(inner)
	dash := strings.Repeat("─", w)
	blank := strings.Repeat(" ", w+2)
	switch {
	case disabled:
		g := "\x1b[2m"
		return []string{
			g + "┌" + dash + "┐" + cReset,
			g + "│" + cReset + dim(inner) + g + "│" + cReset,
			g + "└" + dash + "┘" + cReset,
			blank,
		}
	case focused: // slammed flat: dropped a row, filled, shadow absorbed
		return []string{
			blank,
			fg + "┌" + dash + "┐" + cReset,
			fg + "│" + bg + "\x1b[1;97m" + inner + cReset + fg + "│" + cReset,
			fg + "└" + dash + "┘" + cReset,
		}
	default: // raised: outlined box over a hard colored shadow
		return []string{
			fg + "┌" + dash + "┐" + cReset,
			fg + "│" + cReset + bold(inner) + fg + "│" + cReset,
			fg + "└" + dash + "┘" + cReset,
			fg + strings.Repeat("▀", w+2) + cReset,
		}
	}
}

func (m *model) navbar(w int) string {
	left := triad() + "  " + bold("drift")
	right := dim(m.user) + dim("  ·  ") + cyan("★ "+m.activeOrDash())
	gap := w - vlen(left) - vlen(right) - 2
	if gap < 1 {
		gap = 1
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

// sidebarInner builds the slice list with the "+ new slice" affordance pinned
// to the bottom of the panel (the SLICES title is drawn by the panel border).
// The focused row is reverse-video; the active slice is starred.
func (m *model) sidebarInner() []string {
	out := []string{""}
	for i, s := range m.slices {
		sel := m.focus == focusSidebar && m.sideSel == i
		star := "  "
		if s.Name == m.active {
			star = "★ "
		}
		text := star + trunc(s.Name, sidebarW-6)
		switch {
		case sel:
			out = append(out, rev(pad(" "+text, sidebarW)))
		case s.Name == m.active:
			out = append(out, " "+cyan(text))
		default:
			out = append(out, " "+text)
		}
	}
	newRow := " " + cGreen + "[N]" + cReset + " new slice"
	if m.focus == focusSidebar && m.sideSel == len(m.slices) {
		newRow = rev(pad(" [N] new slice", sidebarW))
	}
	// Stick "+ new slice" to the last content row (pad the gap with blanks).
	for len(out) < m.sideRows-1 {
		out = append(out, "")
	}
	out = append(out, newRow)
	return out
}

// tabStrip is the minimalist (borderless) tab selector incrusted in the panel's
// top-left border. The active tab is bold in its pillar colour, the rest dim.
// "[N]" is the 1-4 hotkey. The first tab's label is the SLICE NAME (not "Slice")
// — that's how the slice's identity sits top-left, like SLICES on the sidebar.
func (m *model) tabStrip() string {
	parts := make([]string, len(tabs))
	for i, t := range tabs {
		if i == tabSlice {
			t = m.activeOrDash()
		}
		label := fmt.Sprintf("[%d] %s", i+1, t)
		if i == m.tab {
			parts[i] = "\x1b[1m" + tabFG(i) + label + cReset
		} else {
			parts[i] = dim(label)
		}
	}
	return " " + strings.Join(parts, "  ")
}

// mainLines = the borderless tab row + a blank line + the active tab's content,
// captured from the existing renderers (which write \r\n-terminated lines) and
// clipped to the main pane width.
func (m *model) mainLines() []string {
	// Delete confirmation + "+ New slice" modals take over the main pane (no tabs).
	if m.deleting != nil {
		return m.deleteLines()
	}
	if m.chooser != nil {
		return m.chooserLines()
	}
	if m.explorer != nil {
		return m.explorerLines()
	}
	if m.form != nil {
		return m.formLines()
	}
	out := []string{""} // breathing room under the title (tabs live in the bottom border)
	var cb strings.Builder
	switch {
	case m.detail != nil:
		m.renderDetail(&cb)
	case m.loadErr[m.tab] != "":
		fmt.Fprintf(&cb, "  \x1b[31m✗ %s\x1b[0m\r\n", m.loadErr[m.tab])
	default:
		switch m.tab {
		case tabSlice:
			m.renderSliceTab(&cb)
		case tabAtomic:
			m.renderAtomic(&cb)
		case tabBackbone:
			m.renderBackbone(&cb)
		case tabCanvas:
			m.renderCanvas(&cb)
		}
	}
	for _, ln := range strings.Split(strings.TrimRight(cb.String(), "\r\n"), "\r\n") {
		out = append(out, truncVis(ln, m.contentW))
	}
	return out
}

// footerStatus renders the status / confirm / inline-input line (row h-1).
func (m *model) footerStatus() string {
	switch {
	case m.input != nil:
		return " \x1b[33m" + m.input.label + cReset + " " + m.input.buf + rev(" ")
	case m.conf != nil:
		return " \x1b[33m⚠ " + m.conf.prompt + cReset + dim("  (y / n)")
	case m.status != "":
		return " " + cyan(m.status)
	}
	return ""
}

// ── Sidebar focus + slice loading ────────────────────────────────────────────

// loadSlices refreshes the sidebar's slice list (independent of the active tab).
func (m *model) loadSlices() {
	if s, err := sliceCmd.FetchSlices(); err == nil {
		m.slices = s
	}
	if m.sideSel > len(m.slices) {
		m.sideSel = len(m.slices)
	}
}

// handleSidebar applies keys while the sidebar is focused. Returns true to quit.
func (m *model) handleSidebar(k key) bool {
	switch k {
	case keyQuit:
		return true
	case keyUp:
		if m.sideSel > 0 {
			m.sideSel--
		}
	case keyDown:
		if m.sideSel < len(m.slices) { // last index = "+ new slice"
			m.sideSel++
		}
	case keyRight: // → enters the main pane (tabs switch only via 1-4)
		m.focus = focusMain
	case keyRefresh:
		m.loadSlices()
		m.status = "refreshed"
	case keyNew: // 'N' — new slice from anywhere in the sidebar
		m.openNewSlice()
	case keyDelete: // 'D' — delete the selected slice (roomy red confirmation)
		if m.sideSel < len(m.slices) {
			m.startDeleteSlice(m.slices[m.sideSel].Name)
		}
	case keyOpen: // 'o' — open the highlighted slice's Canvas URL in a browser
		if m.sideSel < len(m.slices) {
			url := m.sliceURLFor(m.slices[m.sideSel].Name)
			if url == "" {
				m.status = "✗ no URL for this slice"
			} else if err := common.OpenBrowser(url); err != nil {
				m.status = "✗ " + err.Error()
			} else {
				m.status = "opened " + url
			}
		}
	case keyEnter:
		if m.sideSel < len(m.slices) {
			name := m.slices[m.sideSel].Name
			if err := common.SaveActiveSlice(name); err != nil {
				m.status = "✗ " + err.Error()
				return false
			}
			m.active = name
			m.invalidateAll()   // cached data belonged to the previous slice
			m.load(m.tab)       // re-scope the main pane to the new active slice
			m.focus = focusMain // jump focus into the slice's pane
			m.status = "active slice → " + name
		} else {
			m.openNewSlice() // the "[N] new slice" row
		}
	}
	return false
}

// openNewSlice opens the "+ New slice" method chooser (empty configurator or
// from a Driftfile) and moves focus into it.
func (m *model) openNewSlice() {
	m.chooser = &newChooser{}
	m.focus = focusMain
}

// validSliceName mirrors the platform's slice naming rule (^[a-z0-9]{1,30}$).
func validSliceName(s string) bool {
	if len(s) < 1 || len(s) > 30 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ── visible-width helpers (ANSI-aware) ───────────────────────────────────────

// vlen is the visible width of s, ignoring ANSI SGR escape sequences.
func vlen(s string) int {
	n, inEsc := 0, false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			n++
		}
	}
	return n
}

// pad right-pads s with spaces to a visible width of w.
func pad(s string, w int) string {
	if d := w - vlen(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// truncVis clips s to a visible width of w, preserving ANSI escapes and closing
// with a reset so styling never bleeds past the cell.
func truncVis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	var out strings.Builder
	vis, inEsc, styled := 0, false, false
	for _, r := range s {
		switch {
		case inEsc:
			out.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc, styled = true, true
			out.WriteRune(r)
		default:
			if vis >= w {
				if styled {
					out.WriteString(cReset)
				}
				return out.String()
			}
			out.WriteRune(r)
			vis++
		}
	}
	return out.String()
}
