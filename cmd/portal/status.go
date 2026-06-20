package portal

// Ctrl-S platform status: a centered popup that fetches the public status page
// (status.ondrift.eu/api/status) and lists each capability with a coloured dot.
// Ctrl-O / o opens the active slice's URL in a browser. Both live here since
// they're "platform / external" actions rather than tab navigation.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ondrift/cli/common"
)

// platformStatus holds the result of a status fetch while the popup is open.
type platformStatus struct {
	items []statusItem
	err   string
}

// statusItem mirrors one capability from GET /api/status.
type statusItem struct {
	Name   string `json:"name"`
	Status string `json:"status"` // operational | degraded | down | checking
}

// openActiveURL opens the active slice's public URL (Ctrl-O / o in the main pane).
func (m *model) openActiveURL() {
	url := m.sliceURL()
	if url == "" {
		m.status = "✗ no active slice URL"
		return
	}
	if err := common.OpenBrowser(url); err != nil {
		m.status = "✗ " + err.Error()
		return
	}
	m.status = "opened " + url
}

// openStatus fetches the platform status and opens the popup.
func (m *model) openStatus() {
	ps := &platformStatus{}
	if items, err := fetchPlatformStatus(); err != nil {
		ps.err = err.Error()
	} else {
		ps.items = items
	}
	m.platform = ps
}

// fetchPlatformStatus GETs the public status page's JSON, derived from the
// configured API base (api.ondrift.eu → status.ondrift.eu).
func fetchPlatformStatus() ([]statusItem, error) {
	scheme := "http://"
	if strings.HasPrefix(common.APIBaseURL, "https://") {
		scheme = "https://"
	}
	host := strings.TrimPrefix(common.APIBaseURL, scheme)
	host = strings.TrimPrefix(host, "api.")
	url := scheme + "status." + host + "/api/status"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url) // #nosec G107 -- URL derived from the configured API base
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status page returned %d", resp.StatusCode)
	}
	var items []statusItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

// statusColor maps a capability state to its accent colour.
func statusColor(s string) string {
	switch s {
	case "operational":
		return cGreen
	case "degraded":
		return cOrange
	case "down":
		return cRed
	default: // checking / unknown
		return cGrey
	}
}

// modal surface colours: an opaque black fill with bright (theme-independent)
// ink so it reads cleanly over the dimmed UI behind it.
const (
	bgModal  = "\x1b[48;2;0;0;0m"
	inkModal = "\x1b[97m" // bright white
	dimModal = "\x1b[90m" // grey (still legible on black)
)

// drawStatusPopup renders the centered status window — an opaque black box (no
// shadow) on top of the dimmed UI. A blank row sits above the first item.
func (m *model) drawStatusPopup(b *strings.Builder) {
	ps := m.platform
	lines := []string{""} // a blank row above the first element
	switch {
	case ps.err != "":
		lines = append(lines, cRed+"✗ couldn't reach the status page"+cReset, "")
		for _, l := range wrapText(ps.err, 38) {
			lines = append(lines, inkModal+l)
		}
	case len(ps.items) == 0:
		lines = append(lines, inkModal+"no components reported")
	default:
		nameW := 0
		for _, it := range ps.items {
			if l := len(it.Name); l > nameW {
				nameW = l
			}
		}
		for _, it := range ps.items {
			c := statusColor(it.Status)
			lines = append(lines, " "+c+"●"+cReset+"  "+inkModal+pad(it.Name, nameW+2)+c+it.Status+cReset)
		}
	}
	lines = append(lines, "", dimModal+" any key to close")

	bw := maxLineW(lines) + 6
	if bw < 32 {
		bw = 32
	}
	bh := len(lines) + 2
	row := (m.rows - bh) / 2
	if row < 1 {
		row = 1
	}
	col := (m.cols - bw) / 2
	if col < 1 {
		col = 1
	}
	inner := bw - 2

	// Top border with the title (black-filled, blue frame).
	title := "\x1b[1m" + inkModal + " Platform status " + cReset
	if fillN := inner - vlen(title); fillN > 0 {
		at(b, row, col, bgModal+cBlue+"┌"+title+bgModal+cBlue+strings.Repeat("─", fillN)+"┐"+cReset)
	} else {
		at(b, row, col, bgModal+cBlue+"┌"+strings.Repeat("─", inner)+"┐"+cReset)
	}
	// Interior rows — each filled black across the full inner width.
	for i := 1; i < bh-1; i++ {
		content := ""
		if ci := i - 1; ci < len(lines) {
			content = truncVis(lines[ci], inner)
		}
		body := bgModal + strings.ReplaceAll(content, cReset, cReset+bgModal) // keep the fill through resets
		if d := inner - vlen(content); d > 0 {
			body += strings.Repeat(" ", d)
		}
		at(b, row+i, col, bgModal+cBlue+"│"+body+bgModal+cBlue+"│"+cReset)
	}
	// Bottom border.
	at(b, row+bh-1, col, bgModal+cBlue+"└"+strings.Repeat("─", inner)+"┘"+cReset)
}
