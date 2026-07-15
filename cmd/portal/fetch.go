package portal

// Portal-local fetch/act helpers over the /ops/* surface. Every request is
// auto-scoped to the active slice via the X-Slice header (common.DoRequest), so
// these follow whatever the Slice tab selects. Data-only — rendering lives in
// portal.go. (The backbone/canvas commands are print-only inline, so the portal
// owns its own thin data layer rather than churning six command files.)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ondrift/cli/common"
)

// ─── Atomic functions ───────────────────────────────────────────────────────

type fnRow struct {
	FunctionName string `json:"function_name"`
	Method       string `json:"method"`
	Element      string `json:"element"`
	Language     string `json:"language"`
}

func fetchFunctions() ([]fnRow, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/atomic/list", nil)
	if err != nil {
		return nil, common.TransportError("list functions", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list functions")
	if err != nil {
		return nil, err
	}
	var all []fnRow
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, fmt.Errorf("unexpected /ops/atomic/list response: %w", err)
	}
	deployed := make([]fnRow, 0, len(all))
	for _, f := range all {
		if f.FunctionName != "" { // skip pre-warmed (empty) slots
			deployed = append(deployed, f)
		}
	}
	return deployed, nil
}

func deleteFunction(name string) error {
	resp, err := common.DoRequest(http.MethodDelete,
		common.APIBaseURL+"/ops/atomic/delete?name="+url.QueryEscape(name), nil)
	if err != nil {
		return common.TransportError("delete function", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "delete function")
	return err
}

// ─── Backbone ────────────────────────────────────────────────────────────────

type bbStatus struct {
	Cache struct {
		Entries int   `json:"entries"`
		Bytes   int64 `json:"bytes"`
	} `json:"cache"`
	NoSQL struct {
		Collections int   `json:"collections"`
		DiskBytes   int64 `json:"disk_bytes"`
	} `json:"nosql"`
	Queues struct {
		Count         int `json:"count"`
		TotalMessages int `json:"total_messages"`
	} `json:"queues"`
	Blobs struct {
		Buckets    int `json:"buckets"`
		TotalBlobs int `json:"total_blobs"`
	} `json:"blobs"`
	Locks struct {
		Active int `json:"active"`
	} `json:"locks"`
}

func fetchBackboneStatus() (*bbStatus, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/backbone/admin/status", nil)
	if err != nil {
		return nil, common.TransportError("backbone status", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "backbone status")
	if err != nil {
		return nil, err
	}
	var s bbStatus
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("unexpected /ops/backbone/admin/status response: %w", err)
	}
	return &s, nil
}

// triggerDef mirrors the slice's TriggerDefinition (atomic/triggers.rs). A trigger
// binds an event source — a Backbone queue (queue), a cron expression (schedule),
// or an inbound URL suffix (webhook) — to a function, resolved as FuncPath (the
// path after "/api/", e.g. "board/sync"). Registered CLI-side with only a
// target_url; the slice computes FuncPath/FunctionName, so FuncPath is the match key.
type triggerDef struct {
	Name         string `json:"name"`
	FunctionName string `json:"function_name"`
	Type         string `json:"type"` // "queue" | "schedule" | "webhook"
	Source       string `json:"source"`
	Path         string `json:"path"`
	Schedule     string `json:"schedule"`
	FuncPath     string `json:"func_path"`
}

func fetchTriggers() ([]triggerDef, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/trigger/list", nil)
	if err != nil {
		return nil, common.TransportError("list triggers", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list triggers")
	if err != nil {
		return nil, err
	}
	var ts []triggerDef
	_ = json.Unmarshal(body, &ts) // empty / "no triggers" → nil, which renders fine
	return ts, nil
}

func fetchSecrets() ([]string, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/backbone/secret/list", nil)
	if err != nil {
		return nil, common.TransportError("list secrets", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "list secrets")
	if err != nil {
		return nil, err
	}
	var names []string
	_ = json.Unmarshal(body, &names) // empty body / "no secrets" → nil, which renders fine
	return names, nil
}

func deleteSecret(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := common.DoJSONRequest(http.MethodDelete,
		common.APIBaseURL+"/ops/backbone/secret/delete", bytes.NewReader(body))
	if err != nil {
		return common.TransportError("delete secret", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "delete secret")
	return err
}

// ─── Slice runtime (memory / limits) — powers the Canvas tab's slice panel ───

type rtStats struct {
	HeapAllocBytes           int64 `json:"heap_alloc_bytes"`
	FunctionMemoryLimitBytes int64 `json:"function_memory_limit_bytes"`
	// Memory Census (real figures; the cgroup is the OOM-enforced ground truth
	// and is the only thing that sees the page cache for blobs/canvas/sqlite).
	ResidentBytes       int64 `json:"resident_bytes"`
	AnonymousBytes      int64 `json:"anonymous_bytes"`
	AnonymousFloorBytes int64 `json:"anonymous_floor_bytes"`
	FileBackedBytes     int64 `json:"file_backed_bytes"`
	PeakRSSBytes        int64 `json:"peak_rss_bytes"`
	CgroupKnown         bool  `json:"cgroup_known"`
	CgroupCurrentBytes  int64 `json:"cgroup_current_bytes"`
	CgroupMaxBytes      int64 `json:"cgroup_max_bytes"`
	CgroupFileBytes     int64 `json:"cgroup_file_bytes"`
	CgroupHeadroomBytes int64 `json:"cgroup_headroom_bytes"`
	MemoryPressure      int   `json:"memory_pressure"`
}

// footprintBytes is what "your usage" means everywhere the dashboard shows
// it: the non-reclaimable memory a slice is actually holding onto — anon +
// dirty + unevictable + kernel slab. Deliberately excludes CgroupFileBytes
// (page cache: clean LMDB/SQLite/blob pages), which costs nothing to hold
// and vanishes instantly under real pressure — counting it would make an
// idle slice that simply cached its own data look like it's hoarding
// memory it needs. Mirrors slice/src/memguard.rs's own pressure formula
// exactly (cgroup_current - cgroup_file), so what the dashboard calls
// "used" and what the platform's own guard calls "pressure" are always
// telling the same story. Falls back to ResidentBytes when the cgroup
// itself isn't readable (local/standalone runs).
func (rt *rtStats) footprintBytes() int64 {
	if !rt.CgroupKnown || rt.CgroupMaxBytes == 0 {
		return rt.ResidentBytes
	}
	f := rt.CgroupCurrentBytes - rt.CgroupFileBytes
	if f < 0 {
		return 0
	}
	return f
}

func fetchRuntime() (*rtStats, error) {
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/runtime", nil)
	if err != nil {
		return nil, common.TransportError("slice runtime", err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, "slice runtime")
	if err != nil {
		return nil, err
	}
	var rt rtStats
	if err := json.Unmarshal(body, &rt); err != nil {
		return nil, fmt.Errorf("unexpected /ops/slice/runtime response: %w", err)
	}
	return &rt, nil
}

// ─── Backbone data explorer (admin browse endpoints) ─────────────────────────
//
// These mirror the browser portal's Data page: list everything in each
// primitive, then dump a chosen collection/queue/bucket. A nil/`null` body
// decodes to the zero value (empty list), which renders cleanly.

// getJSON GETs path and decodes the JSON body into T. Empty / "null" bodies
// (what the API returns for an empty list) yield the zero value, no error.
func getJSON[T any](path, label string) (T, error) {
	var out T
	resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+path, nil)
	if err != nil {
		return out, common.TransportError(label, err)
	}
	defer resp.Body.Close()
	body, err := common.CheckResponse(resp, label)
	if err != nil {
		return out, err
	}
	if len(bytes.TrimSpace(body)) == 0 || string(bytes.TrimSpace(body)) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("unexpected %s response: %w", label, err)
	}
	return out, nil
}

type cacheEntry struct {
	Key       string `json:"key"`
	Bytes     int64  `json:"bytes"`
	ExpiresAt string `json:"expires_at"`
}

type lockEntry struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	ExpiresAt string `json:"expires_at"`
}

func fetchNoSQLCollections() ([]string, error) {
	return getJSON[[]string]("/ops/backbone/admin/nosql/collections", "list collections")
}

func fetchNoSQLDump(collection string) ([]json.RawMessage, error) {
	return getJSON[[]json.RawMessage]("/ops/backbone/admin/nosql/dump?collection="+url.QueryEscape(collection), "dump collection")
}

func fetchCacheList() ([]cacheEntry, error) {
	return getJSON[[]cacheEntry]("/ops/backbone/admin/cache/list", "list cache")
}

func fetchQueueList() ([]string, error) {
	return getJSON[[]string]("/ops/backbone/admin/queue/list", "list queues")
}

func fetchQueueDump(queue string) ([]json.RawMessage, error) {
	return getJSON[[]json.RawMessage]("/ops/backbone/admin/queue/dump?queue="+url.QueryEscape(queue), "dump queue")
}

func fetchBlobBuckets() ([]string, error) {
	return getJSON[[]string]("/ops/backbone/admin/blob/buckets", "list buckets")
}

func fetchBlobKeys(bucket string) ([]string, error) {
	return getJSON[[]string]("/ops/backbone/blob/list?bucket="+url.QueryEscape(bucket), "list blob keys")
}

func fetchLockList() ([]lockEntry, error) {
	return getJSON[[]lockEntry]("/ops/backbone/admin/lock/list", "list locks")
}

func setSecret(name, value string) error {
	body, _ := json.Marshal(map[string]string{"name": name, "value": value})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/backbone/secret/set", bytes.NewReader(body))
	if err != nil {
		return common.TransportError("set secret", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "set secret")
	return err
}

// ─── Atomic function detail: metrics + logs ──────────────────────────────────

// fnMetrics is the per-function observability snapshot from the slice.
type fnMetrics struct {
	TotalRequests int64   `json:"total_requests"`
	ErrorRequests int64   `json:"error_requests"`
	ErrorRate     float64 `json:"error_rate"`
	AvgDurationMs float64 `json:"avg_duration_ms"`
	PeakRSSBytes  int64   `json:"peak_rss_bytes"`
	LastRSSBytes  int64   `json:"last_rss_bytes"`
	// RSSShared marks the RSS as the SHARED language-server interpreter's PSS
	// (Python/Node) rather than this function's own per-call peak. The table
	// renders it as "shared" so an interpreted function doesn't masquerade as
	// having a private resident set.
	RSSShared bool `json:"rss_shared"`
}

// fnKey is the metrics/logs key the slice stores a function under: "element/name"
// (or bare "name"). It MUST match the slice's function_key (atomic/observe.rs);
// the portal queries /ops/atomic/{metrics,logs} and keys m.fnMet with this — not
// the bare name — so element-scoped functions resolve instead of showing "—".
func fnKey(f fnRow) string {
	if f.Element != "" {
		return f.Element + "/" + f.FunctionName
	}
	return f.FunctionName
}

type logEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Line      string    `json:"line"`
}

func fetchMetrics(function string) (fnMetrics, error) {
	return getJSON[fnMetrics]("/ops/atomic/metrics?function="+url.QueryEscape(function), "function metrics")
}

func fetchLogs(function string) ([]logEntry, error) {
	return getJSON[[]logEntry]("/ops/atomic/logs?function="+url.QueryEscape(function), "function logs")
}

// ─── Snapshots (portable per-slice backups) ──────────────────────────────────

type snapshotRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func fetchSnapshots() ([]snapshotRow, error) {
	return getJSON[[]snapshotRow]("/ops/slice/snapshot/list", "list snapshots")
}

func createSnapshot(name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/snapshot", bytes.NewReader(body))
	if err != nil {
		return common.TransportError("create snapshot", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "create snapshot")
	return err
}

func deleteSnapshot(id string) error {
	resp, err := common.DoRequest(http.MethodDelete,
		fmt.Sprintf("%s/ops/slice/snapshot?id=%s", common.APIBaseURL, url.QueryEscape(id)), nil)
	if err != nil {
		return common.TransportError("delete snapshot", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "delete snapshot")
	return err
}

// ─── Slice configurator: live pricing + configured create ────────────────────

type lineItem struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Quantity      int    `json:"quantity"`
	UnitCents     int    `json:"unit_cents"`
	SubtotalCents int    `json:"subtotal_cents"`
}

type priceResult struct {
	Items               []lineItem `json:"items"`
	MonthlyCents        int        `json:"monthly_cents"`
	PrepaidCents        int        `json:"prepaid_cents"`
	BillingPeriodMonths int        `json:"billing_period_months"`
}

// fetchPrice prices a config (the configurator's live-pricing call).
func fetchPrice(config map[string]any, months int) (*priceResult, error) {
	body, _ := json.Marshal(map[string]any{"config": config, "billing_period_months": months})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/price", bytes.NewReader(body))
	if err != nil {
		return nil, common.TransportError("price slice", err)
	}
	defer resp.Body.Close()
	b, err := common.CheckResponse(resp, "price slice")
	if err != nil {
		return nil, err
	}
	var pr priceResult
	if err := json.Unmarshal(b, &pr); err != nil {
		return nil, fmt.Errorf("unexpected /ops/slice/price response: %w", err)
	}
	return &pr, nil
}

// createSlice provisions a slice. tier "hacker" = free (no config); any other
// tier sends the full config + billing period (the configurator's create).
func createSlice(name, tier string, config map[string]any, months int) error {
	payload := map[string]any{"name": name, "tier": tier}
	if tier != "hacker" {
		payload["config"] = config
		payload["billing_period_months"] = months
	}
	body, _ := json.Marshal(payload)
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/create", bytes.NewReader(body))
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "create slice")
	return err
}

// resizeSlice replaces a slice's config (the upgrade / "configure" path).
func resizeSlice(name string, config map[string]any, months int) error {
	body, _ := json.Marshal(map[string]any{
		"name": name, "config": config, "billing_period_months": months,
	})
	resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/resize", bytes.NewReader(body))
	if err != nil {
		return common.TransportError("resize slice", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "resize slice")
	return err
}

// sliceDoc mirrors the fields of GET /ops/slice/get we use: the current config
// (to display + pre-fill the configurator) plus tier/billing/price.
type sliceDoc struct {
	Name                string   `json:"name"`
	Tier                string   `json:"tier"`
	MonthlyCostCents    int      `json:"monthly_cost_cents"`
	BillingPeriodMonths int      `json:"billing_period_months"`
	Config              sliceCfg `json:"config"`
}

// sliceCfg parses the leaf SliceConfig fields (no json tags server-side, so the
// keys are the Go field names verbatim).
type sliceCfg struct {
	Canvas struct {
		TotalMaxSizeInBytes int64
	} `json:"canvas"`
	Atomic struct {
		MaxNumberOfFunctions            int
		MaxFunctionRuntimeInSeconds     int
		MaxNumberOfDeploymentsInHistory int
		MaxNumberOfHoursForLogRetention int
		MaxNumberOfRequestsPerMinute    int
		MaxNumberOfScheduledJobs        int
		MaxFunctionMemoryBytes          int64
	} `json:"atomic"`
	Backbone struct {
		Secrets             struct{ MaxCount, MaxSizeInBytesEach int }                  `json:"secrets"`
		Blobs               struct{ MaxCount, MaxSizeInBytesEach, MaxStorageBytes int } `json:"blobs"`
		NoSQL               struct{ MaxCollections, MaxStorageBytes int }               `json:"nosql"`
		SQL                 struct{ MaxDatabases, MaxStorageBytes int }                 `json:"sql"`
		Queues              struct{ MaxQueues, MaxDepthEach int }                       `json:"queues"`
		Realtime            struct{ MaxConcurrentConnections int }                      `json:"realtime"`
		Locks               struct{ MaxConcurrent int }                                 `json:"locks"`
		BackupRetentionDays int                                                         `json:"backup_retention_days"`
	} `json:"backbone"`
	// Deed — the fourth pillar's quota surface, a peer of Backbone on
	// SliceConfig (models.Deed), not nested under Backbone.
	Deed struct {
		Vault  struct{ MaxSizeInBytesEach, MaxEntriesPerUID int } `json:"vault"`
		Pocket struct{ MaxSizeInBytesEach int }                   `json:"pocket"`
	} `json:"deed"`
}

func fetchSliceDoc(name string) (*sliceDoc, error) {
	doc, err := getJSON[sliceDoc]("/ops/slice/get?name="+url.QueryEscape(name), "get slice")
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// downloadSnapshot streams the snapshot archive to path and returns bytes written.
func downloadSnapshot(id, path string) (int64, error) {
	resp, err := common.DoRequest(http.MethodGet,
		fmt.Sprintf("%s/ops/slice/snapshot/download?id=%s", common.APIBaseURL, url.QueryEscape(id)), nil)
	if err != nil {
		return 0, common.TransportError("download snapshot", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, e := common.CheckResponse(resp, "download snapshot")
		return 0, e
	}
	f, err := os.Create(path) // #nosec G304 -- user-chosen output path for their own backup
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, resp.Body)
}
