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
		{"CentsBase", driftCentsBase, 100},
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
	// 2 functions: base 100 + 2*5 = 110.
	if got := priceDrift(driftResources{Functions: 2}).MonthlyCents; got != 110 {
		t.Errorf("priceDrift(2 fn) = %d, want 110", got)
	}
	// 64 MiB memory + 1 GiB storage: base 100 + 64*3 + 25 = 317.
	if got := priceDrift(driftResources{MemoryMiB: 64, StorageBytes: 1 * gib}).MonthlyCents; got != 317 {
		t.Errorf("priceDrift(64 MiB, 1 GiB) = %d, want 317", got)
	}
	// empty slice still pays the base floor.
	if got := priceDrift(driftResources{}).MonthlyCents; got != 100 {
		t.Errorf("priceDrift(empty) = %d, want 100", got)
	}
}
