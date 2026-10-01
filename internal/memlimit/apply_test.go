package memlimit

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"time"
)

// keepLimit restores the runtime's memory limit after a test that sets it, so
// one test's ceiling cannot make the rest of the package collect under
// pressure.
func keepLimit(t *testing.T) int64 {
	t.Helper()
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	return prev
}

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// An operator who set GOMEMLIMIT meant it, and the runtime has already applied
// it; a configured limit must not quietly replace theirs.
func TestApplyDefersToGOMEMLIMIT(t *testing.T) {
	prev := keepLimit(t)
	t.Setenv("GOMEMLIMIT", "300MiB")
	log, buf := captureLog()

	Apply(512<<20, log)

	if got := debug.SetMemoryLimit(-1); got != prev {
		t.Errorf("limit changed to %d despite GOMEMLIMIT; was %d", got, prev)
	}
	if !strings.Contains(buf.String(), "source=GOMEMLIMIT") {
		t.Errorf("log does not say where the limit came from:\n%s", buf.String())
	}
}

// The configured figure is the whole process budget, and the heap gets only
// its share: the rest is for stacks, the runtime and SQLite, which the limit
// does not count and the container's OOM killer does.
func TestApplyUsesTheConfiguredLimit(t *testing.T) {
	keepLimit(t)
	t.Setenv("GOMEMLIMIT", "")
	log, buf := captureLog()

	Apply(500<<20, log)

	want := int64(float64(500<<20) * share)
	if got := debug.SetMemoryLimit(-1); got != want {
		t.Errorf("heap limit = %d, want %d (%.0f%% of 500MiB)", got, want, share*100)
	}
	if !strings.Contains(buf.String(), `source="config memory-limit"`) {
		t.Errorf("log does not name the config as the source:\n%s", buf.String())
	}
}

// With nothing configured, whatever the cgroup says is used, and with nothing
// visible the runtime is left alone — but the operator is told how to fix it,
// because on Proxmox that silence is the out-of-memory kill.
func TestApplyFallsBackToTheCgroup(t *testing.T) {
	prev := keepLimit(t)
	t.Setenv("GOMEMLIMIT", "")
	log, buf := captureLog()

	Apply(0, log)

	got := debug.SetMemoryLimit(-1)
	if cg := cgroupLimit(); cg > 0 {
		if want := int64(float64(cg) * share); got != want {
			t.Errorf("heap limit = %d, want %d from cgroup limit %d", got, want, cg)
		}
		if !strings.Contains(buf.String(), "source=cgroup") {
			t.Errorf("log does not name the cgroup:\n%s", buf.String())
		}
		return
	}
	if got != prev {
		t.Errorf("no limit visible, yet the runtime limit changed to %d", got)
	}
	if !strings.Contains(buf.String(), "source=none") || !strings.Contains(buf.String(), "hint=") {
		t.Errorf("no hint given when no limit is visible:\n%s", buf.String())
	}
}

// The cgroup path is what the walk up the hierarchy starts from; on cgroup v2
// it is absolute, and anything else must read as absent rather than send the
// walk somewhere arbitrary.
func TestCgroupPathIsAbsoluteOrEmpty(t *testing.T) {
	if p := cgroupPath(); p != "" && !strings.HasPrefix(p, "/") {
		t.Errorf("cgroupPath = %q, want an absolute path or empty", p)
	}
}

// Every form a limit file takes in the wild: a figure, "max", v1's
// unlimited sentinel, rubbish, and the impossible.
func TestReadLimitEdgeCases(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]int64{
		"1073741824":          1 << 30,
		"  268435456  \n":     256 << 20,
		"0\n":                 0,
		"-1\n":                0,
		"garbage\n":           0,
		"":                    0,
		"9223372036854775807": 0,
	} {
		p := filepath.Join(dir, "limit")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readLimit(p); got != want {
			t.Errorf("readLimit(%q) = %d, want %d", content, got, want)
		}
	}
	// The cut-off between a real figure and "unlimited" is half of MaxInt64.
	for n, want := range map[int64]int64{math.MaxInt64 / 2: math.MaxInt64 / 2, math.MaxInt64/2 + 1: 0} {
		p := filepath.Join(dir, "limit")
		if err := os.WriteFile(p, []byte(strconv.FormatInt(n, 10)), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readLimit(p); got != want {
			t.Errorf("readLimit(%d) = %d, want %d", n, got, want)
		}
	}
}

// Units are what operators type; every suffix has to mean what it says,
// decimal for KB/MB/GB and binary for the rest.
func TestParseSizeUnits(t *testing.T) {
	for in, want := range map[string]int64{
		"1KiB": 1 << 10,
		"1kb":  1000,
		"2K":   2 << 10,
		"3GB":  3_000_000_000,
		"10B":  10,
		"1 g":  1 << 30,
		"0":    0,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseSize("MiB"); err == nil {
		t.Error("a unit with no number was accepted")
	}
}

// The log line is what an operator reads to check the limit took; each range
// has to print in a unit that makes the figure readable.
func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0:               "0KiB",
		1536:            "1KiB",
		512 << 20:       "512MiB",
		(1 << 30) - 1:   "1023MiB",
		1 << 30:         "1.0GiB",
		3 << 29:         "1.5GiB",
		int64(61) << 30: "61.0GiB",
	} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

// The defaults are the tuning the package comment argues for; a zero here
// would mean trimming on every tick, or never.
func TestNewTrimmerDefaults(t *testing.T) {
	log, _ := captureLog()
	tr := NewTrimmer(log)
	if tr.Quiet <= 0 || tr.Every <= 0 || tr.Min == 0 || tr.Busy < tr.Min || tr.Log != log {
		t.Errorf("unexpected defaults: %+v", tr)
	}
}

// Run is the loop the server starts; it has to trim on its ticker and return
// promptly when told to stop, or shutdown waits on it.
func TestTrimmerRunTrimsAndStops(t *testing.T) {
	log, _ := captureLog()
	tr := &Trimmer{Quiet: time.Hour, Every: time.Nanosecond, Busy: 0, Log: log}

	// Each round runs the loop briefly and stops it before looking, so
	// lastTrim is only read once Run's goroutine has returned.
	for round := 0; round < 100; round++ {
		stop, done := make(chan struct{}), make(chan struct{})
		go func() { tr.Run(stop, time.Millisecond); close(done) }()
		time.Sleep(10 * time.Millisecond)
		close(stop)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Run did not return after stop closed")
		}
		if !tr.lastTrim.IsZero() {
			return
		}
	}
	t.Fatal("Run never trimmed")
}

// A trim is logged with what triggered it, which is how an operator tells a
// quiet-spell trim from one forced under load.
func TestTrimmerLogsTheTrigger(t *testing.T) {
	log, buf := captureLog()
	tr := &Trimmer{Quiet: time.Millisecond, Min: 0, Log: log}
	tr.Begin()()
	time.Sleep(5 * time.Millisecond)
	tr.tick()
	if !strings.Contains(buf.String(), "trigger=quiet") {
		t.Errorf("quiet trim not logged as such:\n%s", buf.String())
	}
}
