package portal

// Backbone tab = a mini data explorer mirroring the browser portal's Data page:
// a sub-tab bar across the six primitives, a list per primitive, and a
// scrollable drill-down dump for NoSQL collections / queues / blob buckets.
// Cache and Locks are flat tables; Secrets adds set ('s') + delete ('d').

import (
	"encoding/json"
	"fmt"
	"strings"
)

var bbPrims = []string{"NoSQL", "Cache", "Queues", "Blobs", "Locks", "Secrets"}

const (
	primNoSQL = iota
	primCache
	primQueues
	primBlobs
	primLocks
	primSecrets
)

// bbItem is one row in the active primitive's list.
type bbItem struct {
	name  string // identifier (collection / queue / bucket / lock / cache key / secret)
	cols  string // pre-formatted display line
	drill bool   // Enter opens a dump (NoSQL docs / queue messages / blob keys)
}

// detailView is a scrollable text view — a Backbone drill-down dump or an
// Atomic function's metrics+logs. Shared across tabs, rendered by renderDetail.
// reload (if set) is re-run when the user presses 'r' inside the view.
type detailView struct {
	title  string
	lines  []string
	scroll int
	reload func() (string, []string, error)
}

// loadBackbone (re)loads the status summary plus the active primitive's list.
func (m *model) loadBackbone() {
	if bb, err := fetchBackboneStatus(); err == nil {
		m.bb = bb
	}
	m.loadBBPrim()
}

// loadBBPrim loads the list for the currently-selected primitive.
func (m *model) loadBBPrim() {
	m.loadErr[tabBackbone] = ""
	m.detail = nil
	m.bbItems = nil
	var err error
	switch m.bbPrim {
	case primNoSQL:
		var names []string
		names, err = fetchNoSQLCollections()
		for _, n := range names {
			m.bbItems = append(m.bbItems, bbItem{name: n, cols: n, drill: true})
		}
	case primCache:
		var ents []cacheEntry
		ents, err = fetchCacheList()
		for _, e := range ents {
			m.bbItems = append(m.bbItems, bbItem{name: e.Key,
				cols: fmt.Sprintf("%-32s %10s  %s", trunc(e.Key, 32), fmtBytes(e.Bytes), expiryLabel(e.ExpiresAt))})
		}
	case primQueues:
		var names []string
		names, err = fetchQueueList()
		for _, n := range names {
			m.bbItems = append(m.bbItems, bbItem{name: n, cols: n, drill: true})
		}
	case primBlobs:
		var names []string
		names, err = fetchBlobBuckets()
		for _, n := range names {
			m.bbItems = append(m.bbItems, bbItem{name: n, cols: n, drill: true})
		}
	case primLocks:
		var locks []lockEntry
		locks, err = fetchLockList()
		for _, l := range locks {
			m.bbItems = append(m.bbItems, bbItem{name: l.Name,
				cols: fmt.Sprintf("%-30s %-16s %s", trunc(l.Name, 30), trunc(l.Owner, 16), l.ExpiresAt)})
		}
	case primSecrets:
		var names []string
		names, err = fetchSecrets()
		for _, n := range names {
			m.bbItems = append(m.bbItems, bbItem{name: n, cols: n})
		}
	}
	if err != nil {
		m.loadErr[tabBackbone] = err.Error()
	}
	if m.bbSel >= len(m.bbItems) {
		m.bbSel = max(0, len(m.bbItems)-1)
	}
}

// bbDrill opens the dump for the selected (drillable) row.
func (m *model) bbDrill() {
	if m.bbSel >= len(m.bbItems) || !m.bbItems[m.bbSel].drill {
		return
	}
	name, prim := m.bbItems[m.bbSel].name, m.bbPrim
	fetch := func() (string, []string, error) {
		switch prim {
		case primNoSQL:
			docs, err := fetchNoSQLDump(name)
			return fmt.Sprintf("%s — %d docs", name, len(docs)), jsonLines(docs), err
		case primQueues:
			msgs, err := fetchQueueDump(name)
			return fmt.Sprintf("%s — %d messages", name, len(msgs)), jsonLines(msgs), err
		case primBlobs:
			keys, err := fetchBlobKeys(name)
			title := fmt.Sprintf("%s — %d keys", name, len(keys))
			if len(keys) == 0 {
				return title, []string{"(empty bucket)"}, err
			}
			return title, keys, err
		}
		return "", nil, nil
	}
	title, lines, err := fetch()
	if err != nil {
		m.status = "✗ " + err.Error()
		return
	}
	m.detail = &detailView{title: title, lines: lines, reload: fetch}
}

// setPrim switches the active primitive (delta ±1, wrapping) and reloads.
func (m *model) setPrim(delta int) {
	m.bbPrim = (m.bbPrim + delta + len(bbPrims)) % len(bbPrims)
	m.bbSel = 0
	m.loadBBPrim()
}

// backboneCards summarises each primitive's live counts as a stat card. Secrets
// has no global count in the status payload (it's listed lazily), so — like the
// status line it replaces — it's omitted here.
func backboneCards(s *bbStatus) []statCard {
	return []statCard{
		{title: "NoSQL", rows: [][2]string{
			{"colls", fmt.Sprintf("%d", s.NoSQL.Collections)},
			{"disk", fmtBytes(s.NoSQL.DiskBytes)},
		}},
		{title: "Cache", rows: [][2]string{
			{"entries", fmt.Sprintf("%d", s.Cache.Entries)},
			{"bytes", fmtBytes(s.Cache.Bytes)},
		}},
		{title: "Queues", rows: [][2]string{
			{"queues", fmt.Sprintf("%d", s.Queues.Count)},
			{"msgs", fmt.Sprintf("%d", s.Queues.TotalMessages)},
		}},
		{title: "Blobs", rows: [][2]string{
			{"buckets", fmt.Sprintf("%d", s.Blobs.Buckets)},
			{"blobs", fmt.Sprintf("%d", s.Blobs.TotalBlobs)},
		}},
		{title: "Locks", rows: [][2]string{
			{"active", fmt.Sprintf("%d", s.Locks.Active)},
		}},
	}
}

// renderBackbone draws the status line, primitive sub-bar, and list or dump.
func (m *model) renderBackbone(b *strings.Builder) {
	if s := m.bb; s != nil {
		for _, ln := range statCards(m.contentW-2, 2, backboneCards(s)) {
			fmt.Fprintf(b, "  %s\r\n", ln)
		}
		b.WriteString("\r\n")
	}

	b.WriteString("  ")
	for i, p := range bbPrims {
		if i == m.bbPrim {
			fmt.Fprintf(b, "\x1b[1;4m%s\x1b[0m  ", p)
		} else {
			fmt.Fprintf(b, "\x1b[2m%s\x1b[0m  ", p)
		}
	}
	b.WriteString("\r\n\r\n")

	// List (the drill-down dump is rendered generically by renderDetail).
	if len(m.bbItems) == 0 {
		fmt.Fprintf(b, "  %s\r\n", emptyPrimMsg(m.bbPrim))
		return
	}
	win := m.viewport()
	start := 0
	if m.bbSel >= win {
		start = m.bbSel - win + 1
	}
	end := min(start+win, len(m.bbItems))
	for i := start; i < end; i++ {
		row(b, i == m.bbSel, m.bbItems[i].cols)
	}
}

// viewport is the number of content rows available for a list/dump, derived
// from the terminal height minus the fixed chrome (header, tab bar, status
// line, sub-bar, title, footer).
func (m *model) viewport() int {
	if m.contentH >= 3 {
		return m.contentH
	}
	return 3
}

func emptyPrimMsg(p int) string {
	switch p {
	case primNoSQL:
		return "No collections — `drift backbone nosql write <coll> <json>`."
	case primCache:
		return "Cache is empty."
	case primQueues:
		return "No queues — `drift backbone queue push <q> <json>`."
	case primBlobs:
		return "No blobs — `drift backbone blob put <bucket> <key> <file>`."
	case primLocks:
		return "No active locks."
	case primSecrets:
		return "No secrets — press 's' to set one."
	}
	return ""
}

// jsonLines pretty-prints a slice of raw JSON values into syntax-highlighted
// display lines (keys purple, strings green, numbers orange, bool/null blue).
func jsonLines(docs []json.RawMessage) []string {
	if len(docs) == 0 {
		return []string{"(empty)"}
	}
	out, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		return []string{"(unprintable)"}
	}
	lines := strings.Split(string(out), "\n")
	for i, ln := range lines {
		lines[i] = highlightJSON(ln)
	}
	return lines
}

// highlightJSON colourises one line of pretty-printed JSON: object keys (the
// string before a ':') purple, string values green, numbers orange, true/false
// blue, null + punctuation dim. ANSI-aware width helpers handle the escapes.
func highlightJSON(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '"': // a string — key if the next non-space is ':', else a value
			j := i + 1
			for j < len(r) {
				if r[j] == '\\' && j+1 < len(r) {
					j += 2
					continue
				}
				if r[j] == '"' {
					break
				}
				j++
			}
			end := min(j+1, len(r))
			str := string(r[i:end])
			k := end
			for k < len(r) && r[k] == ' ' {
				k++
			}
			if k < len(r) && r[k] == ':' {
				b.WriteString(cPurple + str + cReset)
			} else {
				b.WriteString(cGreen + str + cReset)
			}
			i = end
		case c == '{' || c == '}' || c == '[' || c == ']' || c == ',' || c == ':':
			b.WriteString(dim(string(c)))
			i++
		case c == '-' || (c >= '0' && c <= '9'):
			j := i
			for j < len(r) && (r[j] == '-' || r[j] == '+' || r[j] == '.' || r[j] == 'e' || r[j] == 'E' || (r[j] >= '0' && r[j] <= '9')) {
				j++
			}
			b.WriteString(cOrange + string(r[i:j]) + cReset)
			i = j
		default:
			switch rest := string(r[i:]); {
			case strings.HasPrefix(rest, "true"):
				b.WriteString(cBlue + "true" + cReset)
				i += 4
			case strings.HasPrefix(rest, "false"):
				b.WriteString(cBlue + "false" + cReset)
				i += 5
			case strings.HasPrefix(rest, "null"):
				b.WriteString(dim("null"))
				i += 4
			default:
				b.WriteRune(c)
				i++
			}
		}
	}
	return b.String()
}

func fmtBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.2f MB", float64(n)/(1024*1024))
	}
}

func expiryLabel(s string) string {
	if s == "" {
		return "no expiry"
	}
	return s
}

// trunc hard-truncates s to n bytes (names/keys are ASCII; keeps columns aligned).
func trunc(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}
