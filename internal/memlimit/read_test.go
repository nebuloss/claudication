package memlimit

import (
	"log/slog"
	"math"
	"runtime"
	"runtime/debug"
	"testing"
)

// The overview's memory card reads this: the ceiling Apply settled on, the
// runtime's own target, and figures that are live, not zero.
func TestReadReportsTheProcessAgainstItsCeiling(t *testing.T) {
	prevHeap := debug.SetMemoryLimit(-1)
	prevProc := processLimit.Load()
	t.Cleanup(func() {
		debug.SetMemoryLimit(prevHeap)
		processLimit.Store(prevProc)
	})
	t.Setenv("GOMEMLIMIT", "")

	Apply(512<<20, slog.New(slog.DiscardHandler))
	s := Read()
	if s.Limit != 512<<20 {
		t.Errorf("Limit = %d, want the configured 512 MiB", s.Limit)
	}
	if want := int64(float64(s.Limit) * share); s.HeapLimit != want {
		t.Errorf("HeapLimit = %d, want the runtime's share of it", s.HeapLimit)
	}
	if s.HeapInUse == 0 || s.Goroutines == 0 {
		t.Errorf("live figures read as zero: %+v", s)
	}
	if runtime.GOOS == "linux" && s.RSS <= 0 {
		t.Errorf("RSS = %d on Linux, where /proc/self/statm is readable", s.RSS)
	}

	// With no runtime limit set, there is no heap target to report.
	debug.SetMemoryLimit(math.MaxInt64)
	if s := Read(); s.HeapLimit != 0 {
		t.Errorf("HeapLimit = %d with no limit set, want 0", s.HeapLimit)
	}
}
