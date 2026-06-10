package project

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The parallel Atomic pool must keep three guarantees the design called out:
// results map back by index (ordered output), every job's REAL error is
// surfaced, and concurrency is bounded.

func TestDeployAtomicJobs_OrderedResultsAndRealErrors(t *testing.T) {
	jobs := []atomicJob{
		{name: "alpha", display: "a"},
		{name: "bravo", display: "b"},
		{name: "charlie", display: "c"},
		{name: "delta", display: "d"},
	}

	var mu sync.Mutex
	calls := map[string]int{}
	results := deployAtomicJobsWith(jobs, func(j atomicJob) error {
		mu.Lock()
		calls[j.name]++
		mu.Unlock()
		if j.name == "bravo" {
			return fmt.Errorf("boom-%s", j.name)
		}
		return nil
	})

	// Every function is attempted exactly once — a mid-list failure no longer
	// skips the ones after it (the behaviour change vs. the old serial path).
	for _, j := range jobs {
		if calls[j.name] != 1 {
			t.Errorf("job %s called %d times, want 1", j.name, calls[j.name])
		}
	}

	// Results land at the right index, and the failure carries its real error.
	if results[0] != nil || results[2] != nil || results[3] != nil {
		t.Errorf("alpha/charlie/delta should succeed, got %v %v %v", results[0], results[2], results[3])
	}
	if results[1] == nil || !strings.Contains(results[1].Error(), "boom-bravo") {
		t.Errorf("bravo should fail with its own error, got %v", results[1])
	}
}

func TestDeployAtomicJobs_BoundedConcurrency(t *testing.T) {
	const n = 20
	jobs := make([]atomicJob, n)
	for i := range jobs {
		jobs[i] = atomicJob{name: fmt.Sprintf("f%d", i), display: fmt.Sprintf("f%d", i)}
	}

	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
	)
	deployAtomicJobsWith(jobs, func(atomicJob) error {
		mu.Lock()
		inFlight++
		if inFlight > maxSeen {
			maxSeen = inFlight
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond) // force overlap so the pool actually fills
		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	})

	limit := min(runtime.NumCPU(), 8, n)
	if maxSeen > limit {
		t.Errorf("max in-flight %d exceeded the cap %d", maxSeen, limit)
	}
	// On any multi-core box it must actually run in parallel, not serialise.
	if runtime.NumCPU() > 1 && maxSeen < 2 {
		t.Errorf("expected parallel execution, peak in-flight was %d", maxSeen)
	}
}

// A single function takes the serial fast path but must still return its result
// at index 0.
func TestDeployAtomicJobs_SingleFunction(t *testing.T) {
	got := deployAtomicJobsWith([]atomicJob{{name: "solo", display: "solo"}}, func(atomicJob) error {
		return fmt.Errorf("nope")
	})
	if len(got) != 1 || got[0] == nil || got[0].Error() != "nope" {
		t.Errorf("single-function path lost the result: %v", got)
	}
}
