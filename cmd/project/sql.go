// sql.go — Driftfile reconcile of the SQL primitive. For each
// declared `sql:` entry the CLI uploads the schema + seed SQL files
// to the slice's admin endpoints. Idempotent — schemas are expected
// to be `CREATE … IF NOT EXISTS`, seeds are only applied when the
// database has no user tables yet (the slice handles this).
//
// Removal: a database that's live on the slice but not in the
// Driftfile is dropped. The same shape as `applyDomains` /
// `applyAlerts` so the deploy chain reads consistently.
package project

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ondrift/cli/v2/common"
)

type liveSQL struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

func applySQL(m *Manifest) error {
	if len(m.Slice.Backbone.SQL) == 0 {
		return nil
	}

	live, err := fetchLiveSQL()
	if err != nil {
		fmt.Printf("  %s sql reconcile skipped: %v\n", common.Hint("·"), err)
		return nil
	}
	declared := map[string]SQLEntry{}
	for _, e := range m.Slice.Backbone.SQL {
		name := strings.ToLower(strings.TrimSpace(e.Name))
		if name == "" {
			continue
		}
		e.Name = name
		declared[name] = e
	}

	for name, entry := range declared {
		if entry.Schema != "" {
			if err := uploadSchema(m.baseDir, name, entry.Schema); err != nil {
				fmt.Printf("  %s sql %s schema: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Printf("  %s sql schema applied: %s ← %s\n",
				common.Check(), name, entry.Schema)
		}
		if entry.Seed != "" {
			if err := uploadSeed(m.baseDir, name, entry.Seed); err != nil {
				fmt.Printf("  %s sql %s seed: %v\n", common.Hint("·"), name, err)
				continue
			}
			fmt.Printf("  %s sql seed applied (if empty): %s ← %s\n",
				common.Check(), name, entry.Seed)
		}
	}

	for _, l := range live {
		if _, keep := declared[l.Name]; keep {
			continue
		}
		if err := dropSQL(l.Name); err != nil {
			fmt.Printf("  %s sql drop %s: %v\n", common.Hint("·"), l.Name, err)
			continue
		}
		fmt.Printf("  %s sql database removed: %s\n", common.Check(), l.Name)
	}
	return nil
}

func fetchLiveSQL() ([]liveSQL, error) {
	resp, err := common.DoRequest(http.MethodGet,
		common.APIBaseURL+"/ops/backbone/sql/admin/list", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list sql")
	if err != nil {
		return nil, err
	}
	var out []liveSQL
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func uploadSchema(baseDir, name, schemaPath string) error {
	abs := schemaPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, schemaPath)
	}
	raw, err := os.ReadFile(abs) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", schemaPath, err)
	}
	body, _ := json.Marshal(map[string]any{
		"db":     name,
		"schema": string(raw),
	})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/load-schema",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "load schema")
		return e
	}
	return nil
}

func uploadSeed(baseDir, name, seedPath string) error {
	abs := seedPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(baseDir, seedPath)
	}
	raw, err := os.ReadFile(abs) // #nosec G304 -- caller-supplied path inside the project root.
	if err != nil {
		return fmt.Errorf("read %s: %w", seedPath, err)
	}
	body, _ := json.Marshal(map[string]any{
		"db":   name,
		"seed": string(raw),
	})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/load-seed",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "load seed")
		return e
	}
	return nil
}

func dropSQL(name string) error {
	body, _ := json.Marshal(map[string]string{"db": name})
	resp, err := common.DoJSONRequest(http.MethodPost,
		common.APIBaseURL+"/ops/backbone/sql/admin/drop",
		strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, e := common.CheckResponse(resp, "drop sql")
		return e
	}
	return nil
}
