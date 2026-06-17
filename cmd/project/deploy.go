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
	"runtime"
	"strings"
	"sync"
	"time"

	atomic_cmd "github.com/ondrift/cli/cmd/atomic/cmd/deploy"
	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// driftfileName is the canonical filename. Per the spec, the CLI
// looks for `./Driftfile` and nowhere else.
const driftfileName = "Driftfile"

// atomicForce, when set by --force, makes applyAtomic redeploy every function
// regardless of whether its source digest matches what's already deployed.
// Bound directly to the flag in getDeployCmd; read in applyAtomic, which runs
// concurrently under applySliceTriad and can't easily take extra parameters.
var atomicForce bool

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

			// Atomic, Backbone, and Canvas are independent slice subsystems —
			// deploy all three concurrently (wall-clock = slowest, not sum).
			if err := applySliceTriad(m); err != nil {
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
	cmd.Flags().BoolVar(&atomicForce, "force", false, "Redeploy every function even if its source is unchanged")
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

// atomicJob is one function's resolved build inputs, captured in manifest order
// so the parallel pool's results can be reported back in that same order.
type atomicJob struct {
	name, dir, element, display string
}

func applyAtomic(m *Manifest, out io.Writer) error {
	a := m.Slice.Atomic
	if len(a.Functions) == 0 {
		return nil
	}

	fmt.Fprintf(out, "  %s\n", common.AtomicHeader())

	// Resolve dir + display name for every function up front, preserving
	// manifest order. The order drives the ordered result output below.
	jobs := make([]atomicJob, len(a.Functions))
	for i, fn := range a.Functions {
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = m.ResolvePath(dir)
		display := fn.Name
		if meta, err := atomic_common.ParseAtomicMetadataFromDir(dir); err == nil && meta.Path != "" {
			display = meta.Path
		}
		jobs[i] = atomicJob{name: fn.Name, dir: dir, element: fn.Element, display: display}
	}

	// Skip functions whose source is unchanged versus what's deployed. The
	// check is best-effort: if the deployed digests can't be fetched (brand-new
	// slice, transient error) we deploy everything; --force skips it entirely.
	// A skipped function does no build and no upload — the expensive part — so
	// this is where the wall-clock is actually saved.
	skip := make([]bool, len(jobs))
	skipByDir := make(map[string]bool, len(jobs))
	if !atomicForce {
		if deployed, err := atomic_cmd.DeployedDigests(); err == nil {
			for i, j := range jobs {
				key, kErr := atomic_cmd.FunctionName(j.dir)
				dig, dErr := atomic_cmd.FunctionDigest(j.dir, j.element)
				if kErr == nil && dErr == nil && dig != "" && deployed[key] == dig {
					skip[i] = true
					skipByDir[j.dir] = true
				}
			}
		}
	}

	results := deployAtomicJobsWith(jobs, func(j atomicJob) error {
		if skipByDir[j.dir] {
			return nil
		}
		return atomic_cmd.DeployFolder(j.dir, j.element, true)
	})

	// Ordered output: ✓ per success, ✓ … (unchanged) per skip, ✗ per failure,
	// in manifest order. Return the first failure with its real error so the
	// deploy chain surfaces the actual rejection reason (CLAUDE.md), never a
	// generic one. Unlike the old serial path, a mid-list failure no longer
	// skips the functions after it — every function is attempted, and you see
	// all failures at once.
	var firstErr error
	deployedCount, skippedCount := 0, 0
	for i, j := range jobs {
		switch {
		case results[i] != nil:
			fmt.Fprintf(out, "    %s %s\n", common.Cross(), j.display)
			if firstErr == nil {
				firstErr = fmt.Errorf("atomic deploy failed for %s: %w", j.name, results[i])
			}
		case skip[i]:
			fmt.Fprintf(out, "    %s %s %s\n", common.Check(), j.display, common.Hint("(unchanged)"))
			skippedCount++
		default:
			fmt.Fprintf(out, "    %s %s\n", common.Check(), j.display)
			deployedCount++
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if skippedCount > 0 {
		fmt.Fprintf(out, "    %s\n", common.Hint(fmt.Sprintf("%d deployed, %d unchanged", deployedCount, skippedCount)))
	}
	fmt.Fprintln(out)
	return nil
}

// applySliceTriad deploys the three independent slice subsystems — Atomic
// functions, Backbone data, and Canvas sites — CONCURRENTLY. They touch
// different parts of the slice and share no mutable state (each makes its own
// stateless HTTP calls), so the wall-clock becomes the slowest of the three
// instead of their sum. The spinner is single-line by contract, so concurrent
// phases can't animate it: each phase buffers its own section, one aggregate
// spinner runs while they work, then the sections print in a stable order
// (Atomic → Backbone → Canvas). The first failure in that order is returned
// with its real error; every phase is attempted so all failures are visible.
func applySliceTriad(m *Manifest) error {
	type phase struct {
		fn  func(*Manifest, io.Writer) error
		buf bytes.Buffer
		err error
	}
	phases := []*phase{
		{fn: applyAtomic},
		{fn: applyBackbone},
		{fn: applyCanvas},
	}

	sp := common.StartSpinner("  ", "Deploying Atomic, Backbone & Canvas…")
	var wg sync.WaitGroup
	for _, p := range phases {
		wg.Add(1)
		go func(p *phase) {
			defer wg.Done()
			p.err = p.fn(m, &p.buf)
		}(p)
	}
	wg.Wait()
	sp.Stop()

	var firstErr error
	for _, p := range phases {
		fmt.Print(p.buf.String())
		if p.err != nil && firstErr == nil {
			firstErr = p.err
		}
	}
	return firstErr
}

// deployAtomicJobs builds and pushes every Atomic function concurrently,
// bounded by a worker pool, returning each job's error (nil = success) in the
// input order. The build path is concurrency-safe by design — each build
// stages into its own tempdir and captures subprocess output rather than
// streaming it (see build_go.go) — so the only shared surfaces are the spinner
// and the results slice, both guarded here.
//
// Concurrency is capped: parallel Go/Rust compiles are CPU- and RAM-heavy, so
// an unbounded fan-out would thrash a small box. Most of the wall-clock win
// comes from overlapping the network-bound phases (SDK fetch, module tidy,
// upload to the operator) across workers, not from raw compile parallelism.
func deployAtomicJobs(jobs []atomicJob) []error {
	return deployAtomicJobsWith(jobs, func(j atomicJob) error {
		return atomic_cmd.DeployFolder(j.dir, j.element, true)
	})
}

// deployAtomicJobsWith is the pool itself, with the deploy step injected so the
// concurrency/ordering/error-surfacing contract is unit-testable without real
// builds. deploy is invoked once per job, possibly concurrently.
func deployAtomicJobsWith(jobs []atomicJob, deploy func(atomicJob) error) []error {
	results := make([]error, len(jobs))

	// One function: just run it. Progress feedback comes from the caller's
	// aggregate phase spinner; this returns results for ordered rendering.
	if len(jobs) == 1 {
		results[0] = deploy(jobs[0])
		return results
	}

	workers := min(runtime.NumCPU(), 8, len(jobs))

	// Each goroutine writes its own distinct results index, so no mutex is
	// needed; the semaphore bounds concurrency. No spinner here — applyAtomic
	// runs concurrently with Backbone/Canvas under one aggregate spinner, and a
	// nested one would clobber the single-line terminal.
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = deploy(jobs[i])
		}(i)
	}
	wg.Wait()
	return results
}

// ─── Backbone ───────────────────────────────────────────────────────

func applyBackbone(m *Manifest, out io.Writer) error {
	b := m.Slice.Backbone
	if len(b.NoSQL)+len(b.Queues)+len(b.Cache)+len(b.Secrets) == 0 {
		return nil
	}

	fmt.Fprintf(out, "  %s\n", common.BackboneHeader())

	for key, e := range b.Cache {
		label := fmt.Sprintf("Cache: %s", key)
		var value string
		var hint string
		if e.File != "" {
			raw, err := os.ReadFile(m.ResolvePath(e.File)) // #nosec G304
			if err != nil {
				return fmt.Errorf("cache %q: read file %s: %w", key, e.File, err)
			}
			value = string(raw)
			hint = fmt.Sprintf("(seeded from %s)", filepath.Base(e.File))
		} else {
			value = e.Value
		}
		if err := cacheSet(key, value, e.TTL); err != nil {
			return fmt.Errorf("cache set %q failed: %w", key, err)
		}
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if hint != "" {
			line += " " + common.Hint(hint)
		}
		fmt.Fprintln(out, line)
	}

	for _, c := range b.NoSQL {
		label := fmt.Sprintf("NoSQL: %s", c.Name)
		if err := nosqlInit(c.Name); err != nil {
			return fmt.Errorf("nosql init %q failed: %w", c.Name, err)
		}
		seeded := 0
		if c.Seed != "" {
			n, err := nosqlSeedJSONL(c.Name, m.ResolvePath(c.Seed))
			if err != nil {
				return fmt.Errorf("nosql seed %q failed: %w", c.Name, err)
			}
			seeded = n
		}
		line := fmt.Sprintf("    %s %s", common.Check(), label)
		if seeded > 0 {
			line += " " + common.Hint(fmt.Sprintf("(seeded %d docs)", seeded))
		}
		fmt.Fprintln(out, line)
	}

	for _, q := range b.Queues {
		label := fmt.Sprintf("Queue: %s", q)
		if err := queueInit(q); err != nil {
			return fmt.Errorf("queue init %q failed: %w", q, err)
		}
		fmt.Fprintf(out, "    %s %s\n", common.Check(), label)
	}

	if len(b.Secrets) > 0 {
		injected := 0
		for k, v := range b.Secrets {
			if err := secretSet(k, v); err != nil {
				return fmt.Errorf("secret set %q failed: %w", k, err)
			}
			injected++
		}
		if injected > 0 {
			fmt.Fprintf(out, "    %s Secrets: %d injected\n", common.Check(), injected)
		}
	}

	fmt.Fprintln(out)
	return nil
}

// ─── Canvas ─────────────────────────────────────────────────────────

func applyCanvas(m *Manifest, out io.Writer) error {
	sites := m.Slice.Canvas.Sites
	if len(sites) == 0 {
		return nil
	}

	fmt.Fprintf(out, "  %s\n", common.CanvasHeader())
	keep := make([]string, 0, len(sites))
	for _, s := range sites {
		dir := m.ResolvePath(s.Dir)
		route := canonicalRoute(s.Route)
		slug := SlugifyRoute(route)
		label := fmt.Sprintf("%s → %s", s.Dir, route)
		if err := deployCanvas(dir, slug, route); err != nil {
			return fmt.Errorf("canvas deploy failed for %s: %w", s.Dir, err)
		}
		fmt.Fprintf(out, "    %s %s\n", common.Check(), label)
		keep = append(keep, slug)
	}
	if err := pruneCanvas(keep); err != nil {
		return fmt.Errorf("canvas prune failed: %w", err)
	}
	fmt.Fprintln(out)
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
