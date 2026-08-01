package project

import (
	"path/filepath"

	atomic_cmd "github.com/ondrift/cli/v2/cmd/atomic/cmd/deploy"
	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

// CountAtomicFunctions returns the total number of `@atomic`-decorated
// callables the deploy will ship. It MIRRORS the deploy branch: when the
// Element layout is in play (a Default element or any multi-function element)
// it counts across discovered elements; otherwise it counts across the
// folders listed in the Driftfile (the legacy path, which honors custom
// `dir:` overrides). Keeping it in lockstep with applyAtomic is what stops a
// flat app from provisioning zero function slots.
//
// An Atomic function IS a decorated callable; un-annotated helpers don't count.
func CountAtomicFunctions(m *Manifest) (int, error) {
	elements, err := atomic_cmd.DiscoverElements(m.ResolvePath("atomic"))
	if err != nil {
		return 0, err
	}
	if shouldUseElementPath(elements) {
		total := 0
		for _, el := range elements {
			total += len(el.Funcs)
		}
		return total, nil
	}
	total := 0
	for _, fn := range m.Slice.Atomic.Functions {
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = m.ResolvePath(dir)
		metas, err := atomic_common.ParseAllAtomicMetadataFromDir(dir)
		if err != nil {
			return 0, err
		}
		total += len(metas)
	}
	return total, nil
}

// CountScheduledFunctions returns how many scheduled jobs the deploy will
// register — the count used to size the slice's envelope. Like
// CountAtomicFunctions, it mirrors the deploy branch.
//
// It counts BOTH declaration sites, because both consume a scheduled-job slot
// and both are billed (tier.CentsPerScheduledJob):
//
//   - `atomic.functions[].cron` in the Driftfile — the documented one, and now
//     the one that actually deploys (see applyAtomic's schedule wiring).
//   - `@atomic cron=` in source — counted for as long as the annotation parses
//     at all, so the envelope never under-counts a project mid-migration.
//
// Counting only the annotation is what made this wrong: the Driftfile field was
// dismissed in code as "vestigial" while the spec documented it with worked
// examples, so a project declaring `cron:` sized its envelope at ZERO scheduled
// jobs. That is not merely a small number — enforceScheduleLimit treats
// `max <= 0` as "no quota to enforce", so the slice was billed for none and
// gated at none.
func CountScheduledFunctions(m *Manifest) (int, error) {
	// The Driftfile half needs no source access, so it is counted first and
	// unconditionally — a preflight that cannot read the tree still sizes the
	// envelope for every declared schedule.
	declared := map[string]bool{}
	for _, fn := range m.Slice.Atomic.Functions {
		if fn.Cron != "" {
			declared[fn.Name] = true
		}
	}

	elements, err := atomic_cmd.DiscoverElements(m.ResolvePath("atomic"))
	if err != nil {
		return len(declared), err
	}
	if shouldUseElementPath(elements) {
		total := len(declared)
		for _, el := range elements {
			for _, f := range el.Funcs {
				// Don't double-count a function carrying both declarations.
				if f.Trigger == "cron" && !declared[f.Path] {
					total++
				}
			}
		}
		return total, nil
	}
	total := len(declared)
	for _, fn := range m.Slice.Atomic.Functions {
		dir := fn.Dir
		if dir == "" {
			dir = filepath.Join("atomic", fn.Name)
		}
		dir = m.ResolvePath(dir)
		metas, err := atomic_common.ParseAllAtomicMetadataFromDir(dir)
		if err != nil {
			return total, err
		}
		for _, meta := range metas {
			if meta.Trigger == "cron" && !declared[fn.Name] {
				total++
			}
		}
	}
	return total, nil
}
