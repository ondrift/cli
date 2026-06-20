package portal

// The "what's new" strip above the main panel is fed by a hosted JSON file on the
// public site — the same pattern as the status page (status.go). Posting news is
// just editing that file; no platform deploy or CLI release is involved.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ondrift/cli/common"
)

// announcement is one entry in the hosted feed (announcements.json). All optional.
type announcement struct {
	Date  string `json:"date"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// fetchAnnouncements GETs the public announcements feed from the status site,
// derived from the API base (api.ondrift.eu → status.ondrift.eu/api/announcements)
// — the same host the status page uses. Failures are non-fatal — the strip stays hidden.
func fetchAnnouncements() ([]announcement, error) {
	scheme := "http://"
	if strings.HasPrefix(common.APIBaseURL, "https://") {
		scheme = "https://"
	}
	host := strings.TrimPrefix(common.APIBaseURL, scheme)
	host = strings.TrimPrefix(host, "api.")
	url := scheme + "status." + host + "/api/announcements"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url) // #nosec G107 -- URL derived from the configured API base
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("announcements feed returned %d", resp.StatusCode)
	}
	var items []announcement
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

// newsLines formats up to `limit` of the latest announcements as one line each
// (title — body  (date)), for the "what's new" strip above the main panel.
func (m *model) newsLines(limit int) []string {
	out := make([]string, 0, limit)
	for i, a := range m.news {
		if i >= limit {
			break
		}
		line := bold(a.Title)
		if a.Body != "" {
			line += dim(" — " + a.Body)
		}
		if a.Date != "" {
			line += dim("  (" + a.Date + ")")
		}
		out = append(out, line)
	}
	return out
}
