package project

import (
	"path/filepath"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

// CountAtomicFunctions walks every directory listed under
// atomic.functions in the Driftfile and returns the total number of
// `@atomic`-decorated callables across all of them. An Atomic function
// IS a decorated callable; helpers (callables without an annotation)
// don't count.
//
// Returns an error if any directory has stacked decorators on a
// single sentinel; those are syntax errors per the parser.
func CountAtomicFunctions(m *Manifest) (int, error) {
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
// callables exist across every directory under atomic.functions. A
// schedule is declared in the source directive, not the Driftfile, so
// this — not the vestigial functions[].cron field — is the authoritative
// scheduled-job count used to size the slice.
func CountScheduledFunctions(m *Manifest) (int, error) {
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
