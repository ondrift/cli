package portal

// Atomic tab: a full-width function table (method · name · lang · reqs · errors
// · avg latency · peak RSS), and an inline expansion that opens "downwards" when
// you hit Enter on a row — metadata on the left, a live-tailing log view on the
// right. The expansion refreshes on a background ticker (see Run) so the logs
// stream without keypresses.

import (
	"fmt"
	"strings"
	"sync"
)

// loadFnMetrics fetches every function's observability snapshot concurrently so
// the table can show a peak-RSS column (and req/err/latency). Best-effort: a
// function that fails to report just shows "—". Called from load(tabAtomic).
func (m *model) loadFnMetrics() {
	if m.fnMet == nil {
		m.fnMet = map[string]fnMetrics{}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, f := range m.fns {
		if f.FunctionName == "" {
			continue
		}
		key := fnKey(f) // element/name — matches the slice's metrics key
		wg.Add(1)
		go func() {
			defer wg.Done()
			if met, err := fetchMetrics(key); err == nil {
				mu.Lock()
				m.fnMet[key] = met
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

// loadFnExpand primes the selected function's current logs + metrics. The Run
// ticker then keeps the selected function's tail live as the rail moves.
func (m *model) loadFnExpand() {
	if m.fnExp < 0 || m.fnExp >= len(m.fns) {
		return
	}
	key := fnKey(m.fns[m.fnExp]) // element/name — metrics + logs share this key
	if met, err := fetchMetrics(key); err == nil && m.fnMet != nil {
		m.fnMet[key] = met
	}
	if logs, err := fetchLogs(key); err == nil {
		m.fnExpLogs = logs
	}
}

// fnConfigKeys are the settable fields of the "configurable" card, in order —
// the rows focusDetail navigates with up/down. Must stay in lock-step with the
// `config` rows built by fnConfigRows (same keys, same order).
var fnConfigKeys = []string{"element", "trigger", "schedule"}

// fnConfigRows builds the configurable card's rows for function f, reflecting any
// trigger bound to it (display-only, #2): element (deploy-time, read-only), the
// event source (queue/webhook), and the cron schedule — "—" when none.
func (m *model) fnConfigRows(f fnRow) [][2]string {
	element := f.Element
	if element == "" {
		element = "—"
	}
	// Org-only routing: the route is the bare function name; the slice's trigger
	// FuncPath is likewise bare, so this binding key must not carry the element.
	route := f.FunctionName
	trigger, schedule := "—", "—"
	for _, t := range m.triggers {
		if !triggerBoundTo(t, route, f.FunctionName) {
			continue
		}
		switch t.Type {
		case "schedule":
			if t.Schedule != "" {
				schedule = t.Schedule
			}
		case "queue":
			trigger = "queue:" + t.Source
		case "webhook":
			trigger = "webhook:/" + strings.TrimPrefix(t.Path, "/")
		}
	}
	return [][2]string{
		{"element", element},
		{"trigger", trigger},
		{"schedule", schedule},
	}
}

// triggerBoundTo reports whether trigger t targets the function at `route`
// (org-only: the bare route path, no element) or named `name`. FuncPath is the
// precise binding — the route's :params are treated as wildcards, so
// "board/123/sync" binds "board/:id/sync".
// FunctionName is only a fallback (it can collide across elements), used when the
// slice couldn't resolve a func_path.
func triggerBoundTo(t triggerDef, route, name string) bool {
	if t.FuncPath != "" {
		return routePathMatch(route, t.FuncPath)
	}
	return t.FunctionName != "" && t.FunctionName == name
}

func routePathMatch(route, funcPath string) bool {
	rs := strings.Split(strings.Trim(route, "/"), "/")
	fs := strings.Split(strings.Trim(funcPath, "/"), "/")
	if len(rs) != len(fs) {
		return false
	}
	for i := range rs {
		if strings.HasPrefix(rs[i], ":") {
			continue // a path param matches any concrete segment
		}
		if rs[i] != fs[i] {
			return false
		}
	}
	return true
}

// atomicServiceCards summarise the Atomic service as a whole — its function
// inventory, aggregate traffic across every function, and the registered event
// triggers — for the row above the rail + detail.
func (m *model) atomicServiceCards() []statCard {
	elements := map[string]bool{}
	langs := map[string]bool{}
	var totalReq, totalErr int64
	var sumAvg float64
	var avgN int
	for _, f := range m.fns {
		if f.Element != "" {
			elements[f.Element] = true
		}
		langs[langLabel(f.Language)] = true
		met := m.fnMet[fnKey(f)]
		totalReq += met.TotalRequests
		totalErr += met.ErrorRequests
		if met.TotalRequests > 0 { // only functions with traffic count toward the avg
			sumAvg += met.AvgDurationMs
			avgN++
		}
	}
	errPct := 0.0
	if totalReq > 0 {
		errPct = float64(totalErr) / float64(totalReq) * 100
	}
	avg := 0.0
	if avgN > 0 {
		avg = sumAvg / float64(avgN)
	}
	var q, s, w int
	for _, t := range m.triggers {
		switch t.Type {
		case "queue":
			q++
		case "schedule":
			s++
		case "webhook":
			w++
		}
	}
	return []statCard{
		{title: "functions", rows: [][2]string{
			{"deployed", fmt.Sprintf("%d", len(m.fns))},
			{"elements", fmt.Sprintf("%d", len(elements))},
			{"languages", fmt.Sprintf("%d", len(langs))},
		}},
		{title: "traffic", rows: [][2]string{
			{"requests", fmt.Sprintf("%d", totalReq)},
			{"errors", fmt.Sprintf("%d (%.1f%%)", totalErr, errPct)},
			{"avg", fmt.Sprintf("%.1f ms", avg)},
		}},
		{title: "triggers", rows: [][2]string{
			{"queue", fmt.Sprintf("%d", q)},
			{"schedule", fmt.Sprintf("%d", s)},
			{"webhook", fmt.Sprintf("%d", w)},
		}},
	}
}

// fnExpandLines renders the expansion as a two-column box: metadata on the left,
// a tail of the live logs on the right. width is the available content width.
func (m *model) fnExpandLines(width int) []string {
	if m.fnExp < 0 || m.fnExp >= len(m.fns) {
		return nil
	}
	f := m.fns[m.fnExp]
	met := m.fnMet[fnKey(f)] // per-function metrics (kept live by the Run ticker)

	// Org-only routing: the path is the bare function name (no element segment).
	path := "/" + f.FunctionName
	errPct := 0.0
	if met.TotalRequests > 0 {
		errPct = float64(met.ErrorRequests) / float64(met.TotalRequests) * 100
	}
	cap := int64(0)
	if m.rt != nil {
		cap = m.rt.FunctionMemoryLimitBytes
	}
	// Interpreted (Python/Node) functions share one language-server interpreter,
	// so their RSS is that shared process's PSS — labelled so, not a private set.
	peakLbl, lastLbl := "RSS peak", "RSS last"
	if met.RSSShared {
		peakLbl = "RSS (shared " + langLabel(f.Language) + ")"
		lastLbl = "RSS last (shared)"
	}
	// Three grouped metadata columns: basic · configurable · metrics. Each sizes
	// to its content; on overflow the basic column (which holds the long path,
	// already shown in the rail) absorbs the deficit. A horizontal rule then
	// separates them from the full-width live log tail below. The thin dim │ that
	// separates all this from the rail is drawn by renderAtomic — no box here.
	basic := [][2]string{
		{"name", f.FunctionName},
		{"path", path},
		{"method", f.Method},
		{"language", langLabel(f.Language)},
	}
	config := m.fnConfigRows(f) // element + any bound trigger/schedule (display-only)
	metrics := [][2]string{
		{"requests", fmt.Sprintf("%d", met.TotalRequests)},
		{"errors", fmt.Sprintf("%d (%.1f%%)", met.ErrorRequests, errPct)},
		{"avg", fmt.Sprintf("%.1f ms", met.AvgDurationMs)},
		{peakLbl, mib(met.PeakRSSBytes)},
		{lastLbl, mib(met.LastRSSBytes)},
	}
	if cap > 0 && !met.RSSShared {
		metrics = append(metrics, [2]string{"mem cap",
			fmt.Sprintf("%s (%.0f%%)", mib(cap), float64(met.PeakRSSBytes)/float64(cap)*100)})
	} else if cap > 0 {
		metrics = append(metrics, [2]string{"mem cap", mib(cap)})
	}

	// While browsing the rail the cards stay dim; pressing Enter (focusDetail) gives
	// the whole function the orange outline — the "now you're working on this"
	// signal — and lands the ▸ cursor on the settable field being edited.
	basicCard := statCard{title: "basic", rows: basic, hlRow: -1}
	configCard := statCard{title: "configurable", rows: config, hlRow: -1}
	metricsCard := statCard{title: "metrics", rows: metrics, hlRow: -1}
	if m.focus == focusDetail {
		basicCard.hl, configCard.hl, metricsCard.hl = true, true, true
		configCard.hlRow = m.detailSel
	}
	out := statCards(width, 2, []statCard{basicCard, configCard, metricsCard})
	out = append(out, dim(strings.Repeat("─", maxi(1, width))),
		" "+dim("logs ")+cGreen+"●"+cReset+dim(" live"))
	return append(out, m.fnLogLines(width)...)
}

// fnLogLines renders the open function's live log tail (timestamp + wrapped line)
// to a width. Shared by the inline detail pane and the [f] full-screen view.
func (m *model) fnLogLines(width int) []string {
	const tsW = 8 // "HH:MM:SS"
	wrapW := width - 1 - tsW - 2
	if wrapW < 4 {
		wrapW = 4
	}
	var out []string
	for _, e := range m.fnExpLogs {
		ts := e.Timestamp.Local().Format("15:04:05")
		for j, ch := range hardWrap(strings.TrimRight(e.Line, "\r\n"), wrapW) {
			if j == 0 {
				out = append(out, " "+dim(ts)+"  "+ch)
			} else {
				out = append(out, " "+strings.Repeat(" ", tsW+2)+ch)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, " "+dim("(waiting for logs…)"))
	}
	return out
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// hardWrap splits plain s into chunks of at most w runes (breaking mid-token if
// needed), so long unbroken log lines wrap instead of being clipped.
func hardWrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}
	var out []string
	for len(runes) > w {
		out = append(out, string(runes[:w]))
		runes = runes[w:]
	}
	return append(out, string(runes))
}
