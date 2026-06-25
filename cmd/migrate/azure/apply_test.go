package azure

import "testing"

// TestApplyGate builds a real workspace through the pipeline (which produces
// refusals: the .NET app, the Node function, the Static Web App) and checks the
// integrity gate — apply must refuse to run silently around a refusal.
func TestApplyGate(t *testing.T) {
	export := t.TempDir()
	ws := t.TempDir()
	m, err := runSnapshot(fakeAz{t}, fakeProvider{}, nil, "sub", "demo-rg", export, "orders", true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	res, err := runTransform(export, ws, m)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if len(res.refusals) == 0 {
		t.Fatal("fixture should produce refusals to exercise the gate")
	}

	if err := applyGate(ws, false); err == nil {
		t.Error("gate should BLOCK when refusals exist and --accept-refusals is absent")
	}
	if err := applyGate(ws, true); err != nil {
		t.Errorf("gate should PASS with --accept-refusals: %v", err)
	}
	if err := applyGate(t.TempDir(), true); err == nil {
		t.Error("gate should fail when there's no Driftfile (transform not run)")
	}
}
