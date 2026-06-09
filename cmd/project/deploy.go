package project

// run.go is the deploy driver. Reads a Driftfile, optionally fetches
// the live slice + diffs, prompts for cost-confirm, grows the slice
// envelope when needed, then walks atomic → backbone → canvas
// applying every declared resource via the api gateway.
//
// Flags:
//   --plan                Print the diff (resources + envelope + cost),
//                         exit non-zero if oversized, never apply.
//                         Skips file-existence checks for canvas dirs
//                         so it works in CI where canvas isn't mounted.
//   --no-slice-reconcile  Skip the slice diff entirely; deploy code
//                         only. Used as the escape hatch when the
//                         abort path fires and the user wants to
//                         leave the slice alone.
//   --yes                 Auto-confirm the cost prompt. For CI use.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	atomic_cmd "github.com/ondrift/cli/cmd/atomic/cmd/deploy"
	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// driftfileName is the canonical filename. Per the spec, the CLI
// looks for `./Driftfile` and nowhere else.
const driftfileName = "Driftfile"

func getDeployCmd() *cobra.Command {
	var (
		planOnly      bool
		noReconcile   bool
		autoYes       bool
		billingMonths int
		envName       string
	)

	cmd := &cobra.Command{
		Use:     "deploy",
		Short:   "Deploy all resources declared in a Driftfile manifest",
		Example: "  drift project deploy\n  drift project deploy --plan\n  drift project deploy --env=staging\n  drift project deploy --no-slice-reconcile",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if envName != "" {
				if err := os.Setenv("ENV", envName); err != nil {
					return fmt.Errorf("set ENV=%s: %w", envName, err)
				}
			}

			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return fmt.Errorf("resolve manifest path: %w", err)
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}

			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}

			// In --plan mode, the slice diff drives output and we
			// never call code-deploy paths. Fetch live + classify
			// + render, and exit with the appropriate status.
			if planOnly {
				return runPlan(m)
			}

			// Reconcile the slice's shape unless explicitly skipped.
			if !noReconcile {
				if err := reconcileSlice(m, autoYes, billingMonths); err != nil {
					return err
				}
			}

			// At this point the slice exists at >= the declared shape.
			// Set it as the active slice for subsequent api calls.
			if err := common.SaveActiveSlice(m.Slice.Name); err != nil {
				return fmt.Errorf("set active slice: %w", err)
			}

			start := time.Now()
			fmt.Printf("\n  Deploying %s...\n\n", common.Highlight(m.Slice.Name))

			if err := applyAtomic(m); err != nil {
				return err
			}
			if err := applyBackbone(m); err != nil {
				return err
			}
			if err := applyCanvas(m); err != nil {
				return err
			}
			if err := applyDomains(m); err != nil {
				return err
			}
			if err := applyAlerts(m); err != nil {
				return err
			}
			if err := applySQL(m); err != nil {
				return err
			}
			if err := applyEgress(m); err != nil {
				return err
			}

			elapsed := time.Since(start).Seconds()
			fmt.Printf("  %s\n", common.Hint(fmt.Sprintf("Done in %.1fs!", elapsed)))

			if siteURL := buildSiteURL(); siteURL != "" {
				fmt.Printf("\n  %s  %s\n", common.Check(), siteURL)
			}
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&planOnly, "plan", false, "Print the slice diff and exit; do not deploy")
	cmd.Flags().BoolVar(&noReconcile, "no-slice-reconcile", false, "Skip the slice diff; deploy code into the active slice as-is")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period for new slices and grow operations")
	cmd.Flags().StringVar(&envName, "env", "", "Sets ENV before parsing the Driftfile, so ${ENV} substitutes to this value (typical: staging, prod)")
	return cmd
}

// ─── Plan mode ──────────────────────────────────────────────────────

func runPlan(m *Manifest) error {
	d, err := computeDiff(m)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(RenderDiff(d))
	if d.Verdict == VerdictAbort {
		return fmt.Errorf("deploy would abort (slice oversized vs manifest)")
	}
	return nil
}

// ─── Reconcile (used by run, not plan) ──────────────────────────────

func reconcileSlice(m *Manifest, autoYes bool, billingMonths int) error {
	d, err := computeDiff(m)
	if err != nil {
		return err
	}

	switch d.Verdict {
	case VerdictMatch:
		// Nothing to do. Continue silently.
		return nil

	case VerdictAbort:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		return fmt.Errorf("deploy aborted (slice oversized vs manifest)")

	case VerdictCreate:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		if !confirm(autoYes, "Apply?") {
			return fmt.Errorf("aborted by user")
		}
		// Pick the cheapest tier that fits: hacker if zero cost, custom otherwise.
		tier := "custom"
		if d.WantedCostCents == 0 {
			tier = "hacker"
		}
		manifestCfg, err := ManifestToSliceConfig(m)
		if err != nil {
			return err
		}
		fmt.Printf("\n  Creating slice %q...\n", m.Slice.Name)
		if err := CreateSlice(m.Slice.Name, tier, manifestCfg, billingMonths); err != nil {
			return err
		}
		// Wait for the slice to provision before we deploy code into it.
		if err := waitForSliceReady(m.Slice.Name); err != nil {
			return fmt.Errorf("slice did not become ready: %w", err)
		}
		return nil

	case VerdictGrow:
		fmt.Println()
		fmt.Println(RenderDiff(d))
		if !confirm(autoYes, "Apply?") {
			return fmt.Errorf("aborted by user")
		}
		manifestCfg, err := ManifestToSliceConfig(m)
		if err != nil {
			return err
		}
		fmt.Printf("\n  Growing slice %q...\n", m.Slice.Name)
		return ResizeSlice(m.Slice.Name, manifestCfg, billingMonths)
	}

	return fmt.Errorf("unexpected verdict: %s", d.Verdict)
}

// computeDiff is shared by plan and reconcile. It builds the
// manifest's SliceConfig, fetches the live slice (if any), prices
// both, and classifies via Diff().
func computeDiff(m *Manifest) (DiffResult, error) {
	manifestCfg, err := ManifestToSliceConfig(m)
	if err != nil {
		return DiffResult{}, err
	}
	wantedCost, err := PriceConfig(manifestCfg)
	if err != nil {
		return DiffResult{}, fmt.Errorf("price manifest config: %w", err)
	}

	live, err := FetchLiveSlice(m.Slice.Name)
	if err != nil {
		return DiffResult{}, fmt.Errorf("fetch live slice: %w", err)
	}

	var (
		liveCfg  *SliceConfig
		liveCost int
	)
	if live != nil {
		liveCfg = &live.Config
		liveCost = live.MonthlyCostCents
	}

	return Diff(m.Slice.Name, manifestCfg, liveCfg, liveCost, wantedCost), nil
}

// confirm prompts the user with [y/N]. autoYes short-circuits the
// prompt — used by CI flags. Returns true if the user accepts.
func confirm(autoYes bool, prompt string) bool {
	if autoYes {
		fmt.Printf("  %s [y/N] (auto-yes)\n", prompt)
		return true
	}
	fmt.Printf("  %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// waitForSliceReady polls /ops/slice/status until all components
// report Ready, or until 60s elapses. Returns the last error if any.
func waitForSliceReady(name string) error {
	deadline := time.Now().Add(60 * time.Second)
	u := common.APIBaseURL + "/ops/slice/status?name=" + url.QueryEscape(name)
	for time.Now().Before(deadline) {
		resp, err := common.DoJSONRequest(http.MethodGet, u, nil)
		if err == nil {
			body, cerr := common.CheckResponse(resp, "slice status")
			resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
			if cerr == nil {
				var s struct {
					Ready bool `json:"ready"`
				}
				if jerr := json.Unmarshal(body, &s); jerr == nil && s.Ready {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("slice %q not ready after 60s", name)
}

// ─── Atomic ─────────────────────────────────────────────────────────

func applyAtomic(m *Manifest) error {
	a := m.Slice.Atomic
	if len(a.Functions) == 0 {
		return nil
	}

	fmt.Printf("  %s\n", common.AtomicHeader())
	for _, fn := range a.Functions {
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = m.ResolvePath(dir)

		meta, metaErr := atomic_common.ParseAtomicMetadataFromDir(dir)
		displayName := meta.Path
		if metaErr != nil || displayName == "" {
			displayName = fn.Name
		}

		sp := common.StartSpinner("    ", displayName)
		err := atomic_cmd.DeployFolder(dir, fn.Element, true)
		sp.Stop()
		if err != nil {
			return fmt.Errorf("atomic deploy failed for %s: %w", fn.Name, err)
		}
		fmt.Printf("    %s %s\n", common.Check(), displayName)
	}
	fmt.Println()
	return nil
}

// ─── Backbone ───────────────────────────────────────────────────────

func applyBackbone(m *Manifest) error {
	b := m.Slice.Backbone
	if len(b.NoSQL)+len(b.Queues)+len(b.Cache)+len(b.Secrets) == 0 {
		return nil
	}

	fmt.Printf("  %s\n", common.BackboneHeader())

	for key, e := range b.Cache {
		label := fmt.Sprintf("Cache: %s", key)
		sp := common.StartSpinner("    ", label)
		var value string
		var hint string
		if e.File != "" {
			raw, err := os.ReadFile(m.ResolvePath(e.File)) // #nosec G304
			if err != nil {
				sp.Stop()
				return fmt.Errorf("cache %q: read file %s: %w", key, e.File, err)
			}
			value = string(raw)
			hint = fmt.Sprintf("(seeded from %s)", filepath.Base(e.File))
		} else {
			value = e.Value
		}
		if err := cacheSet(key, value, e.TTL); err != nil {
			sp.Stop()
			return fmt.Errorf("cache set %q failed: %w", key, err)
		}
		sp.Stop()
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if hint != "" {
			line += " " + common.Hint(hint)
		}
		fmt.Println(line)
	}

	for _, c := range b.NoSQL {
		label := fmt.Sprintf("NoSQL: %s", c.Name)
		sp := common.StartSpinner("    ", label)
		if err := nosqlInit(c.Name); err != nil {
			sp.Stop()
			return fmt.Errorf("nosql init %q failed: %w", c.Name, err)
		}
		seeded := 0
		if c.Seed != "" {
			n, err := nosqlSeedJSONL(c.Name, m.ResolvePath(c.Seed))
			if err != nil {
				sp.Stop()
				return fmt.Errorf("nosql seed %q failed: %w", c.Name, err)
			}
			seeded = n
		}
		sp.Stop()
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if seeded > 0 {
			line += " " + common.Hint(fmt.Sprintf("(seeded %d docs)", seeded))
		}
		fmt.Println(line)
	}

	for _, q := range b.Queues {
		label := fmt.Sprintf("Queue: %s", q)
		sp := common.StartSpinner("    ", label)
		if err := queueInit(q); err != nil {
			sp.Stop()
			return fmt.Errorf("queue init %q failed: %w", q, err)
		}
		sp.Stop()
		fmt.Printf("    %s %s\n", common.Check(), label)
	}

	if len(b.Secrets) > 0 {
		sp := common.StartSpinner("    ", "Secrets: injecting…")
		injected := 0
		for k, v := range b.Secrets {
			if err := secretSet(k, v); err != nil {
				sp.Stop()
				return fmt.Errorf("secret set %q failed: %w", k, err)
			}
			injected++
		}
		sp.Stop()
		if injected > 0 {
			fmt.Printf("    %s Secrets: %d injected\n", common.Check(), injected)
		}
	}

	fmt.Println()
	return nil
}

// ─── Canvas ─────────────────────────────────────────────────────────

func applyCanvas(m *Manifest) error {
	sites := m.Slice.Canvas.Sites
	if len(sites) == 0 {
		return nil
	}

	fmt.Printf("  %s\n", common.CanvasHeader())
	keep := make([]string, 0, len(sites))
	for _, s := range sites {
		dir := m.ResolvePath(s.Dir)
		route := canonicalRoute(s.Route)
		slug := SlugifyRoute(route)
		label := fmt.Sprintf("%s → %s", s.Dir, route)
		sp := common.StartSpinner("    ", label)
		if err := deployCanvas(dir, slug, route); err != nil {
			sp.Stop()
			return fmt.Errorf("canvas deploy failed for %s: %w", s.Dir, err)
		}
		sp.Stop()
		fmt.Printf("    %s %s\n", common.Check(), label)
		keep = append(keep, slug)
	}
	if err := pruneCanvas(keep); err != nil {
		return fmt.Errorf("canvas prune failed: %w", err)
	}
	fmt.Println()
	return nil
}

// canonicalRoute normalises the Driftfile's optional route value. Empty or
// missing means "/", trailing slash is stripped (except the bare "/").
func canonicalRoute(route string) string {
	if route == "" {
		return "/"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	route = strings.TrimRight(route, "/")
	if route == "" {
		return "/"
	}
	return route
}

// SlugifyRoute derives the per-site directory name from a route. The slug
// is what the slice uses to lay sites out under /data/canvas/<slug>/.
//
//	"/"               -> "default"
//	"/reviewer"       -> "reviewer"
//	"/admin/portal"   -> "admin-portal"
func SlugifyRoute(route string) string {
	r := canonicalRoute(route)
	if r == "/" {
		return "default"
	}
	r = strings.TrimPrefix(r, "/")
	return strings.ReplaceAll(r, "/", "-")
}

// ─── API gateway calls (resource-application path) ─────────────────

func deployCanvas(dir, slug, route string) error {
	zipData, err := common.ZipFolder(dir)
	if err != nil {
		return fmt.Errorf("zip folder: %w", err)
	}
	q := url.Values{}
	q.Set("site", slug)
	q.Set("route", route)
	resp, err := common.DoRequestWithHeaders(
		http.MethodPost,
		common.APIBaseURL+"/ops/canvas?"+q.Encode(),
		zipData,
		map[string]string{"Content-Type": "application/zip"},
	)
	if err != nil {
		return common.TransportError("deploy canvas site", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "deploy canvas site")
	return err
}

func pruneCanvas(keep []string) error {
	body, _ := json.Marshal(map[string]any{"keep": keep})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/canvas/prune", bytes.NewBuffer(body))
	if err != nil {
		return common.TransportError("prune canvas sites", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "prune canvas sites")
	return err
}

func cacheSet(key, value string, ttl int) error {
	payload, _ := json.Marshal(map[string]any{"key": key, "value": value, "ttl": ttl})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/cache/set", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("seed cache key", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "seed cache key")
	return err
}

// nosqlInit creates the collection if it doesn't already exist. Used to
// be a {_setup: true} sentinel write — replaced with /nosql/ensure so
// no visible artefact lands in the collection. Also cleans up legacy
// sentinel rows left behind by older deploys, so templates can stop
// filtering them out at read time.
func nosqlInit(collection string) error {
	target := fmt.Sprintf("%s/ops/backbone/nosql/ensure?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	resp, err := common.DoJSONRequest(http.MethodPost, target, nil)
	if err != nil {
		return common.TransportError("initialise NoSQL collection", err)
	}
	defer resp.Body.Close()
	if _, err := common.CheckResponse(resp, "initialise NoSQL collection"); err != nil {
		return err
	}
	return purgeLegacySentinels(collection)
}

// purgeLegacySentinels removes any {_setup: true} rows left in the
// collection by previous deploys (the older nosqlInit wrote one per
// invocation and never cleaned up). Idempotent — a no-op once the
// collection is clean.
func purgeLegacySentinels(collection string) error {
	listURL := fmt.Sprintf("%s/ops/backbone/nosql/list?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	listResp, err := common.DoJSONRequest(http.MethodGet, listURL, nil)
	if err != nil {
		return common.TransportError("list nosql for sentinel sweep", err)
	}
	defer listResp.Body.Close()
	body, err := common.CheckResponse(listResp, "list nosql for sentinel sweep")
	if err != nil {
		return err
	}
	var rows []map[string]any
	if len(body) == 0 || json.Unmarshal(body, &rows) != nil {
		return nil
	}
	for _, row := range rows {
		if setup, _ := row["_setup"].(bool); !setup {
			continue
		}
		key, _ := row["_key"].(string)
		if key == "" {
			continue
		}
		delURL := fmt.Sprintf("%s/ops/backbone/nosql/delete?collection=%s&key=%s",
			common.APIBaseURL, url.QueryEscape(collection), url.QueryEscape(key))
		dResp, derr := common.DoJSONRequest(http.MethodPost, delURL, nil)
		if derr != nil {
			return common.TransportError("purge legacy sentinel", derr)
		}
		dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	}
	return nil
}

// nosqlSeedJSONL drops the named collection and re-seeds it from the
// JSONL file. Drop-then-seed is the right semantic for seed data
// because the JSONL IS the canonical state of that collection — there's
// no notion of "merge with prior". Going forward the platform's write
// path upserts by `_id`, so even within a single seed run repeated
// `_id`s get the right end-state. Apps that want runtime-mutable data
// should use a separate (non-seeded) collection.
func nosqlSeedJSONL(collection, path string) (int, error) {
	data, err := os.ReadFile(path) // #nosec G304 — manifest-declared path, validated at parse
	if err != nil {
		return 0, fmt.Errorf("read seed: %w", err)
	}
	// Drop, then ensure, then seed — guarantees the JSONL fully describes
	// the collection's state, with no carry-over from prior deploys.
	dropURL := fmt.Sprintf("%s/ops/backbone/nosql/drop?collection=%s",
		common.APIBaseURL, url.QueryEscape(collection))
	dResp, err := common.DoJSONRequest(http.MethodPost, dropURL, nil)
	if err != nil {
		return 0, common.TransportError("drop seeded collection", err)
	}
	if dResp.StatusCode != http.StatusNoContent && dResp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(dResp.Body)
		dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		return 0, fmt.Errorf("drop seeded collection: HTTP %d: %s", dResp.StatusCode, string(body))
	}
	dResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	if err := nosqlInit(collection); err != nil {
		return 0, err
	}
	count := 0
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(ln), &doc); err != nil {
			return count, fmt.Errorf("parse seed line %d: %w", count+1, err)
		}
		doc["collection"] = collection
		body, _ := json.Marshal(doc)
		resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/write", bytes.NewBuffer(body))
		if err != nil {
			return count, common.TransportError("seed nosql doc", err)
		}
		_, cerr := common.CheckResponse(resp, "seed nosql doc")
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		if cerr != nil {
			return count, cerr
		}
		count++
	}
	return count, nil
}

func queueInit(name string) error {
	payload, _ := json.Marshal(map[string]any{"queue": name, "body": map[string]any{"_setup": true}})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/queue/push", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("initialise queue", err)
	}
	defer resp.Body.Close()
	if _, err = common.CheckResponse(resp, "initialise queue"); err != nil {
		return err
	}

	popURL := fmt.Sprintf("%s/ops/backbone/queue/pop?queue=%s", common.APIBaseURL, url.QueryEscape(name))
	popResp, err := common.DoJSONRequest(http.MethodPost, popURL, nil)
	if err == nil {
		popResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
	}
	return nil
}

func secretSet(name, value string) error {
	payload, _ := json.Marshal(map[string]string{"name": name, "value": value})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/secret/set", bytes.NewBuffer(payload))
	if err != nil {
		return common.TransportError("store secret", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "store secret")
	return err
}

// ─── URL builder ────────────────────────────────────────────────────

func buildSiteURL() string {
	username := common.GetUsername()
	slice := common.GetActiveSlice()
	if username == "" || slice == "" {
		return ""
	}
	apiURL := common.APIBaseURL
	scheme := "http://"
	if strings.HasPrefix(apiURL, "https://") {
		scheme = "https://"
	}
	host := strings.TrimPrefix(apiURL, scheme)
	host = strings.TrimPrefix(host, "api.")
	if slice == "default" {
		return scheme + username + "." + host
	}
	return scheme + username + "-" + slice + "." + host
}
