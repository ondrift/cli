package project

import (
	"path/filepath"

	atomic_cmd "github.com/ondrift/cli/cmd/atomic/cmd/deploy"
	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
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

// CountScheduledFunctions returns how many `@atomic cron=` (scheduled)
// callables exist — the authoritative scheduled-job count used to size the
// slice. Like CountAtomicFunctions, it mirrors the deploy branch.
func CountScheduledFunctions(m *Manifest) (int, error) {
	elements, err := atomic_cmd.DiscoverElements(m.ResolvePath("atomic"))
	if err != nil {
		return 0, err
	}
	if shouldUseElementPath(elements) {
		total := 0
		for _, el := range elements {
			for _, f := range el.Funcs {
				if f.Trigger == "cron" {
					total++
				}
			}
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
		for _, meta := range metas {
			if meta.Trigger == "cron" {
				total++
			}
		}
	}
	return total, nil
}
