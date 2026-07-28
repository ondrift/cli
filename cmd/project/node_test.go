package project

import "testing"

// The two bugs the struct-based mergers had, pinned so they cannot come back.
func TestDeepMergeFixesTheStructMergerBugs(t *testing.T) {
	t.Run("a value can be overridden TO zero", func(t *testing.T) {
		// The old mergers gated on `if overlay.DeployHistory != 0`, so this
		// override was silently discarded and the base value survived.
		base := Node{"atomic": Node{"deploy_history": 5}}
		overlay := Node{"atomic": Node{"deploy_history": 0}}

		got := DeepMerge(base, overlay)
		if v := got.Int("atomic", "deploy_history"); v != 0 {
			t.Errorf("deploy_history should be overridable to 0, got %d", v)
		}
	})

	t.Run("a bool can be overridden to false", func(t *testing.T) {
		base := Node{"domains": Node{"wildcard": true}}
		overlay := Node{"domains": Node{"wildcard": false}}

		if DeepMerge(base, overlay).Bool("domains", "wildcard") {
			t.Error("wildcard should be overridable to false")
		}
	})

	t.Run("a field nobody taught the merger about still merges", func(t *testing.T) {
		// The whole class: the old mergeAtomic had a clause per field, so a
		// field added without one was silently not merged. This merge does not
		// know what a field is, so there is nothing to forget.
		base := Node{"atomic": Node{"a_field_invented_after_this_code": "base"}}
		overlay := Node{"atomic": Node{"a_field_invented_after_this_code": "overridden"}}

		if got := DeepMerge(base, overlay).Str("atomic", "a_field_invented_after_this_code"); got != "overridden" {
			t.Errorf("an unknown field must merge like any other, got %q", got)
		}
	})
}

func TestDeepMergeSemantics(t *testing.T) {
	t.Run("nested objects merge rather than replace", func(t *testing.T) {
		base := Node{"atomic": Node{"function_memory": "64MB", "rate_limit": "100/min"}}
		overlay := Node{"atomic": Node{"function_memory": "256MB"}}

		got := DeepMerge(base, overlay)
		if v := got.Str("atomic", "function_memory"); v != "256MB" {
			t.Errorf("overlay should win: %q", v)
		}
		if v := got.Str("atomic", "rate_limit"); v != "100/min" {
			t.Errorf("untouched sibling should survive: %q", v)
		}
	})

	t.Run("lists replace wholesale", func(t *testing.T) {
		base := Node{"atomic": Node{"functions": []any{Node{"name": "a"}, Node{"name": "b"}}}}
		overlay := Node{"atomic": Node{"functions": []any{Node{"name": "only"}}}}

		fns := DeepMerge(base, overlay).Nodes("atomic", "functions")
		if len(fns) != 1 || fns[0].Str("name") != "only" {
			t.Errorf("an environment listing functions means exactly those, got %v", fns)
		}
	})

	t.Run("the base is not mutated", func(t *testing.T) {
		base := Node{"atomic": Node{"function_memory": "64MB"}}
		DeepMerge(base, Node{"atomic": Node{"function_memory": "256MB"}})

		if v := base.Str("atomic", "function_memory"); v != "64MB" {
			t.Errorf("merging must not write through to the base, got %q", v)
		}
	})
}

func TestNodeAccessorsAreTotal(t *testing.T) {
	n := Node{
		"name": "demo",
		"atomic": Node{
			"deploy_history": 3,
			"functions":      []any{Node{"name": "hello"}, "not-an-object"},
		},
		"canvas": Node{"sites": []any{"./a", "./b"}},
	}

	if n.Str("name") != "demo" {
		t.Error("Str")
	}
	if n.Int("atomic", "deploy_history") != 3 {
		t.Error("Int")
	}
	if got := n.Nodes("atomic", "functions"); len(got) != 1 || got[0].Str("name") != "hello" {
		t.Errorf("Nodes should skip non-objects, got %v", got)
	}
	if got := n.Strings("canvas", "sites"); len(got) != 2 {
		t.Errorf("Strings, got %v", got)
	}

	// Missing and wrongly-typed paths yield zero values, never panics — the
	// schema is what rejects a bad document, not these readers.
	if n.Str("nope", "deeper") != "" || n.Int("name") != 0 || n.Sub("nope") == nil {
		t.Error("accessors must be total")
	}
	if len(n.Nodes("nope")) != 0 {
		t.Error("a missing list is empty, not a panic")
	}
}

// Has is what makes present-and-zero distinguishable from absent.
func TestNodeHas(t *testing.T) {
	n := Node{"atomic": Node{"deploy_history": 0, "rate_limit": ""}}

	if !n.Has("atomic", "deploy_history") {
		t.Error("a key set to 0 is still present")
	}
	if !n.Has("atomic", "rate_limit") {
		t.Error(`a key set to "" is still present`)
	}
	if n.Has("atomic", "never_set") {
		t.Error("an absent key must not report present")
	}
}

func TestNodeSet(t *testing.T) {
	n := Node{}
	n.Set("prod-app", "name")
	n.Set("256MB", "atomic", "function_memory")

	if n.Str("name") != "prod-app" {
		t.Error("Set at the root")
	}
	if n.Str("atomic", "function_memory") != "256MB" {
		t.Error("Set should create intermediate objects")
	}
}
