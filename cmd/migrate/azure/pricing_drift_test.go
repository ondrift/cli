package azure

import "testing"

// TestDriftConstantsPinned is the tripwire for the pricing mirror. These values
// are copied from platform/core/common/plan/pricing.go because the CLI can't
// import core. If the platform changes a price, this test fails — update the
// mirror in pricing_drift.go and this pin together (and the pricing-ladder doc).
func TestDriftConstantsPinned(t *testing.T) {
	pins := []struct {
		name      string
		got, want int
	}{
		{"CentsPerFunction", driftCentsPerFunction, 5},
		{"CentsPerScheduledJob", driftCentsPerScheduledJob, 30},
		{"CentsPerMiBMemory", driftCentsPerMiBMemory, 3},
		{"CentsPerRealtimeConn", driftCentsPerRealtimeConn, 1},
		{"CentsPerGiBStorage", driftCentsPerGiBStorage, 25},
	}
	for _, p := range pins {
		if p.got != p.want {
			t.Errorf("drift price %s drifted from the platform: got %d, want %d — re-sync pricing_drift.go with platform/core/common/plan/pricing.go", p.name, p.got, p.want)
		}
	}
}

func TestPriceDrift(t *testing.T) {
	const gib = 1024 * 1024 * 1024
	// 2 functions: 2*5 = 10.
	if got := priceDrift(driftResources{Functions: 2}).MonthlyCents; got != 10 {
		t.Errorf("priceDrift(2 fn) = %d, want 10", got)
	}
	// 64 MiB memory + 1 GiB storage: 64*3 + 25 = 217.
	if got := priceDrift(driftResources{MemoryMiB: 64, StorageBytes: 1 * gib}).MonthlyCents; got != 217 {
		t.Errorf("priceDrift(64 MiB, 1 GiB) = %d, want 217", got)
	}
	// empty slice costs nothing — no base fee anymore.
	if got := priceDrift(driftResources{}).MonthlyCents; got != 0 {
		t.Errorf("priceDrift(empty) = %d, want 0", got)
	}
}
