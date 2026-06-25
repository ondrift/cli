package azure

import (
	"strings"
	"testing"
)

func TestRequireSnapshotTools(t *testing.T) {
	orig := onPath
	defer func() { onPath = orig }()

	withCosmosAndApp := Inventory{
		Cosmos:       []NamedResource{{Name: "cosmos-x"}},
		FunctionApps: []FunctionApp{{Name: "api", Class: Movable}},
	}

	// Nothing installed → both prerequisites reported, loudly.
	onPath = func(string) bool { return false }
	err := requireSnapshotTools(withCosmosAndApp, nil)
	if err == nil || !strings.Contains(err.Error(), "mongoexport") || !strings.Contains(err.Error(), "unsquashfs") {
		t.Errorf("expected both tools reported, got: %v", err)
	}

	// Both installed → nil.
	onPath = func(string) bool { return true }
	if err := requireSnapshotTools(withCosmosAndApp, nil); err != nil {
		t.Errorf("expected nil when both present, got: %v", err)
	}

	// mongoexport present, unsquashfs missing → only unsquashfs reported.
	onPath = func(tool string) bool { return tool == "mongoexport" }
	err = requireSnapshotTools(withCosmosAndApp, nil)
	if err == nil || strings.Contains(err.Error(), "mongoexport") || !strings.Contains(err.Error(), "unsquashfs") {
		t.Errorf("expected only unsquashfs reported, got: %v", err)
	}

	// No Cosmos + every app --source'd → no live fetch, no Cosmos → nil even with
	// nothing installed (the --source escape works).
	onPath = func(string) bool { return false }
	sourced := Inventory{FunctionApps: []FunctionApp{{Name: "api", Class: Movable}}}
	if err := requireSnapshotTools(sourced, map[string]string{"api": "/local/api"}); err != nil {
		t.Errorf("expected nil when no Cosmos and all apps --source'd, got: %v", err)
	}

	// A refused (non-movable) app doesn't trigger the unsquashfs requirement.
	refusedOnly := Inventory{FunctionApps: []FunctionApp{{Name: "legacy", Class: Refused}}}
	if err := requireSnapshotTools(refusedOnly, nil); err != nil {
		t.Errorf("expected nil when only refused apps, got: %v", err)
	}
}
