package azure

// Drift-side pricing — a deliberate MIRROR of the platform's billing constants
// at platform/core/common/plan/pricing.go. The CLI is its own module and must
// not import core internals, so the numbers are copied here and pinned by a
// tripwire test (pricing_drift_test.go). If the platform's prices change, that
// test fails and reminds us to update this mirror.
//
// Source of truth: docs/memos/done/pricing-v2-ram-storage-model.md (and the
// constants in platform/core/common/plan/pricing.go). Integer cents, never
// floats (same discipline as the platform). The model charges for the box:
// RAM (function memory, realtime) + storage (per GiB) + a tiny function
// token; collections / queues / SQL dbs / blob count are FREE. No flat base
// fee — removed platform-side (see pricing.go's own comment) since it was
// the one line that didn't map to a configured resource.
const (
	driftCentsPerFunction     = 5  // per @atomic function (surface token)
	driftCentsPerScheduledJob = 30 // per scheduled (cron) job (background load)
	driftCentsPerMiBMemory    = 3  // per MiB of function-memory cap (RAM, primary lever)
	driftCentsPerRealtimeConn = 1  // per concurrent realtime connection (RAM)
	driftCentsPerGiBStorage   = 25 // per GiB of pooled storage (nosql+blob+sql+canvas)
	bytesPerGiB               = 1024 * 1024 * 1024
)

// driftResources is the priced shape of a synthesized slice — only the inputs
// that move the bill under the RAM+storage model. Collections / queues / SQL
// databases / blob count are free (quota-only), so they aren't priced here.
type driftResources struct {
	Functions     int
	ScheduledJobs int
	MemoryMiB     int   // function-memory cap; 0 = not charged
	StorageBytes  int64 // pooled nosql + blob + sql + canvas bytes
	RealtimeConns int
}

type driftLine struct {
	Key           string `json:"key"`
	Label         string `json:"label"`
	Quantity      int    `json:"quantity"`
	UnitCents     int    `json:"unit_cents"`
	SubtotalCents int    `json:"subtotal_cents"`
}

type driftBreakdown struct {
	Lines        []driftLine `json:"lines"`
	MonthlyCents int         `json:"monthly_cents"`
}

func clampNonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// priceDrift mirrors plan.PriceConfig: base + RAM + storage + tokens → monthly
// cents. Kept structurally aligned so the tripwire test can compare totals.
func priceDrift(r driftResources) driftBreakdown {
	funcs := clampNonNeg(r.Functions)
	sched := clampNonNeg(r.ScheduledJobs)
	mem := clampNonNeg(r.MemoryMiB)
	conns := clampNonNeg(r.RealtimeConns)
	storage := r.StorageBytes
	if storage < 0 {
		storage = 0
	}
	// Byte-accurate, rounded half-up to the cent (same as plan.PriceConfig).
	storageCents := int((storage*int64(driftCentsPerGiBStorage) + bytesPerGiB/2) / bytesPerGiB)

	lines := []driftLine{
		{"atomic_functions", "Atomic functions", funcs, driftCentsPerFunction, funcs * driftCentsPerFunction},
		{"atomic_scheduled", "Scheduled jobs", sched, driftCentsPerScheduledJob, sched * driftCentsPerScheduledJob},
		{"realtime_connections", "Realtime connections", conns, driftCentsPerRealtimeConn, conns * driftCentsPerRealtimeConn},
	}
	if mem > 0 {
		lines = append(lines, driftLine{"atomic_memory", "Function memory (MiB)", mem, driftCentsPerMiBMemory, mem * driftCentsPerMiBMemory})
	}
	lines = append(lines, driftLine{"bb_storage", "Storage (per GiB)", int(storage / (1024 * 1024)), driftCentsPerGiBStorage, storageCents})

	total := 0
	for _, l := range lines {
		total += l.SubtotalCents
	}
	return driftBreakdown{Lines: lines, MonthlyCents: total}
}
