package azure

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fakeAz returns fixture JSON instead of shelling out to `az`, so the whole
// estimate pipeline runs offline and deterministically.
type fakeAz struct{ t *testing.T }

func (f fakeAz) runJSON(args []string, out any) error {
	key := strings.Join(args, " ")
	// Kudu source retrieval mints a bearer first; hand back a fixed fake token.
	if strings.HasPrefix(key, "account get-access-token") {
		return json.Unmarshal([]byte(`{"accessToken":"faketoken"}`), out)
	}
	var file string
	switch {
	case strings.HasPrefix(key, "functionapp list"):
		file = "testdata/functionapp_list.json"
	case strings.HasPrefix(key, "resource list"):
		file = "testdata/resource_list.json"
	case strings.HasPrefix(key, "consumption usage list"):
		file = "testdata/consumption_usage.json"
	case strings.HasPrefix(key, "functionapp config appsettings list"):
		file = "testdata/appsettings_list.json"
	default:
		f.t.Fatalf("fakeAz: unexpected command: az %s", key)
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// runRaw is only used by the live storage-package adapter, which the fixture
// pipeline never exercises (it injects a fakeProvider). Fail loudly if a test
// path ever reaches it unexpectedly.
func (f fakeAz) runRaw(args []string) ([]byte, error) {
	f.t.Fatalf("fakeAz: unexpected raw command: az %s", strings.Join(args, " "))
	return nil, nil
}

func eq(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}

// TestBuildEstimate_Golden runs the full read-only pipeline against the
// fixtures and pins every number. Fixture totals:
//
//	Azure: Functions 120 + Cosmos 45.50 + Storage 30 + Bandwidth 12.25 = 207.75 movable
//	       + App Insights 60 (unmapped → stays on Azure) = 267.75 total
//	Drift: 2 movable apps (Python, Node) → 2 functions; 1 Cosmos → 1 collection
//	       base 100 + 2*5 + 1*100 = 210 cents
//	Saving: 20775 - 210 = 20565 cents
func TestBuildEstimate_Golden(t *testing.T) {
	res, err := buildEstimate(fakeAz{t}, "Contoso-Prod", "demo-rg")
	if err != nil {
		t.Fatalf("buildEstimate: %v", err)
	}

	eq(t, "azure total", res.Azure.TotalCents, 26775)
	eq(t, "azure movable", res.Azure.MovableCents, 20775)
	eq(t, "azure other (unmapped, stays on Azure)", res.Azure.OtherCents, 6000)
	// Drift estimate: base 100 + 2 movable functions × 5 = 110. The Cosmos
	// container maps to a NoSQL collection, which is now FREE by count (storage
	// GiB is sized at transform, not here) — so it no longer adds to the bill.
	eq(t, "drift monthly", res.Drift.MonthlyCents, 110)
	eq(t, "monthly saving", res.SavingCents, 20665) // azure movable 20775 − drift 110

	eq(t, "movable function apps", len(res.Movable), 2)
	eq(t, "refused/unverified function apps", len(res.Refused), 1)
	eq(t, "cosmos accounts", res.CosmosCount, 1)
	eq(t, "storage accounts", res.StorageCount, 2)

	if res.Refused[0].Runtime != "dotnet" || res.Refused[0].Class != Refused {
		t.Errorf("expected the .NET app refused, got runtime=%q class=%s", res.Refused[0].Runtime, res.Refused[0].Class)
	}
	if res.Currency != "EUR" {
		t.Errorf("currency = %q, want EUR", res.Currency)
	}
	// Integrity: the unmapped meter must NOT be folded into the saving.
	if res.Azure.MovableCents+res.Azure.OtherCents != res.Azure.TotalCents {
		t.Errorf("movable + other (%d + %d) != total (%d)", res.Azure.MovableCents, res.Azure.OtherCents, res.Azure.TotalCents)
	}

	// Show the populated table under `go test -v`.
	if testing.Verbose() {
		renderTable(res)
	}
}

func TestRuntimeClassification(t *testing.T) {
	cases := map[string]MoveClass{
		"python": Movable, "node": Movable,
		"dotnet": Refused, "java": Refused, "powershell": Refused,
		"unknown": Verify,
	}
	for rt, want := range cases {
		if got, _ := classify(rt); got != want {
			t.Errorf("classify(%q) = %s, want %s", rt, got, want)
		}
	}
}
