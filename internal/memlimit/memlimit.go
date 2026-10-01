// Package memlimit tells the Go runtime how much memory it may use, and gives
// back what a burst of requests left behind.
//
// Two problems, both seen on the DMZ container (512 MB, one core):
//
// The runtime does not know the ceiling. Without a memory limit the garbage
// collector aims for twice the live heap, so a burst of agents with megabyte
// request bodies grows the heap straight past the container's limit and the
// kernel kills the process — every open stream at once. And the container
// cannot see its own limit: Proxmox puts it on a parent cgroup outside the
// container's namespace, memory.max there reads "max", and /proc/meminfo
// reports the host's 61 GB. So the limit has to be configured where it cannot
// be detected, and is detected where it can be (Docker, systemd).
//
// And freed memory goes back slowly. The runtime returns idle heap to the
// system in the background, gradually, so after a burst the process keeps
// hundreds of megabytes it is no longer using — which on a small container is
// the headroom the next burst needs. Trim hands it back once the gateway has
// gone quiet.
package memlimit

import (
	"bufio"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// share is the part of a detected ceiling given to the Go heap. The rest is
// for what the limit does not count — goroutine stacks, the runtime itself,
// SQLite's page cache — and for the moment between the heap reaching the limit
// and the collector catching up.
const share = 0.8

// Apply sets the runtime's memory limit and reports where it came from.
//
// GOMEMLIMIT in the environment wins: the runtime has already read it, and an
// operator who set it meant it. Then the configured limit, which is the whole
// budget for the process and gets the same share as a detected one. Then a
// cgroup limit, where one is visible. Otherwise nothing is set and the runtime
// behaves as it always has.
func Apply(configured int64, log *slog.Logger) {
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		log.Info("memory limit", "source", "GOMEMLIMIT", "value", v)
		return
	}
	limit, source := configured, "config memory-limit"
	if limit <= 0 {
		limit, source = cgroupLimit(), "cgroup"
	}
	if limit <= 0 {
		log.Info("memory limit", "source", "none",
			"hint", "set memory-limit in the config on a container whose limit is not visible from inside it")
		return
	}
	heap := int64(float64(limit) * share)
	debug.SetMemoryLimit(heap)
	processLimit.Store(limit)
	log.Info("memory limit", "source", source, "process", HumanBytes(limit), "heap", HumanBytes(heap))
}

// processLimit is the whole-process ceiling Apply settled on, for Read to
// report; 0 when none was configured or detected.
var processLimit atomic.Int64

// Snapshot is the process's memory as the admin UI shows it.
type Snapshot struct {
	// RSS is resident memory: what a container's limit actually counts,
	// heap or not. 0 where it cannot be read (anything but Linux).
	RSS int64 `json:"rss_bytes"`
	// Limit is the process ceiling from memory-limit or the cgroup; 0 when
	// none is known. HeapLimit is the runtime's own target, 0 when unset.
	Limit     int64 `json:"limit_bytes"`
	HeapLimit int64 `json:"heap_limit_bytes"`
	// HeapInUse is live and recently freed heap; HeapIdle is held from the
	// system but unused — what the trimmer hands back.
	HeapInUse  uint64 `json:"heap_in_use_bytes"`
	HeapIdle   uint64 `json:"heap_idle_bytes"`
	Goroutines int    `json:"goroutines"`
	GCCycles   uint32 `json:"gc_cycles"`
}

// Read takes a snapshot. It stops the world for the memory statistics, which
// costs microseconds and is called when someone looks, not per request.
func Read() Snapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s := Snapshot{
		RSS:        rss(),
		Limit:      processLimit.Load(),
		HeapInUse:  m.HeapInuse,
		HeapIdle:   m.HeapIdle - m.HeapReleased,
		Goroutines: runtime.NumGoroutine(),
		GCCycles:   m.NumGC,
	}
	if h := debug.SetMemoryLimit(-1); h != math.MaxInt64 {
		s.HeapLimit = h
	}
	return s
}

// rss reads resident memory from /proc/self/statm, in pages.
func rss() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// cgroupLimit reads this process's cgroup v2 memory.max, walking up to the
// nearest ancestor that sets one, or v1's limit_in_bytes. 0 when there is none
// to see.
func cgroupLimit() int64 {
	if path := cgroupPath(); path != "" {
		for dir := filepath.Join("/sys/fs/cgroup", path); ; dir = filepath.Dir(dir) {
			if n := readLimit(filepath.Join(dir, "memory.max")); n > 0 {
				return n
			}
			if dir == "/sys/fs/cgroup" || dir == "/" || dir == "." {
				break
			}
		}
	}
	return readLimit("/sys/fs/cgroup/memory/memory.limit_in_bytes")
}

// cgroupPath is the v2 path from /proc/self/cgroup ("0::/path"), or "".
func cgroupPath() string {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return rest
		}
	}
	return ""
}

// readLimit parses a limit file. "max", absent, or a figure so large it means
// "unlimited" (v1 reports one near 2^63) all read as 0.
func readLimit(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/2 {
		return 0
	}
	return n
}

// ParseSize reads "512MiB", "400MB", "1GiB", "1048576", case-insensitively. An
// empty string is 0, meaning "not set".
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	upper := strings.ToUpper(s)
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1},
	}
	mult := int64(1)
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			upper, mult = strings.TrimSpace(strings.TrimSuffix(upper, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseFloat(upper, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q: want a number with an optional unit like 512MiB", s)
	}
	return int64(n * float64(mult)), nil
}

// HumanBytes formats a size for a log line.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	default:
		return fmt.Sprintf("%dKiB", n>>10)
	}
}

// Trimmer gives memory the heap is no longer using back to the system, so the
// process does not sit at its high-water mark.
//
// Two triggers. After a burst, once nothing is in flight and nothing has
// finished for Quiet, whatever is idle goes back at once — the moment the
// most is reclaimable and nobody pays for the collection. And continuously,
// at most every Every, even while requests run, once a larger amount (Busy)
// has built up: a gateway that is never quiet would otherwise never give
// anything back, and its footprint would only ever ratchet up. Under load that
// costs one forced collection per interval — milliseconds against seconds —
// and the threshold keeps it from returning pages the next request would only
// fault straight back in.
type Trimmer struct {
	inFlight atomic.Int64
	lastDone atomic.Int64 // unix nanos
	lastTrim time.Time

	Quiet time.Duration // nothing finished for this long counts as quiet
	Min   uint64        // least idle heap worth returning when quiet
	Every time.Duration // least time between trims under load
	Busy  uint64        // least idle heap worth returning under load
	Log   *slog.Logger
}

// NewTrimmer returns a trimmer with defaults that suit a request gateway.
func NewTrimmer(log *slog.Logger) *Trimmer {
	return &Trimmer{
		Quiet: 15 * time.Second, Min: 16 << 20,
		Every: 30 * time.Second, Busy: 32 << 20,
		Log: log,
	}
}

// Begin marks a request in flight; the returned func marks it done.
func (t *Trimmer) Begin() func() {
	t.inFlight.Add(1)
	return func() {
		t.lastDone.Store(time.Now().UnixNano())
		t.inFlight.Add(-1)
	}
}

// Run checks every interval until stop closes.
func (t *Trimmer) Run(stop <-chan struct{}, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			t.tick()
		}
	}
}

func (t *Trimmer) tick() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// Held from the system and not in use: what a trim would give back.
	held := m.HeapIdle - m.HeapReleased

	reason := ""
	last := t.lastDone.Load()
	switch {
	case t.inFlight.Load() == 0 && last != 0 && time.Since(time.Unix(0, last)) >= t.Quiet && held >= t.Min:
		reason = "quiet"
	case t.Every > 0 && time.Since(t.lastTrim) >= t.Every && held >= t.Busy:
		reason = "periodic"
	default:
		return
	}

	start := time.Now()
	debug.FreeOSMemory()
	t.lastTrim = time.Now()
	runtime.ReadMemStats(&m)
	if t.Log != nil {
		t.Log.Info("returned idle memory to the system",
			"trigger", reason,
			"released", HumanBytes(int64(held)),
			"heap_in_use", HumanBytes(int64(m.HeapInuse)),
			"in_flight", t.inFlight.Load(),
			"took_ms", time.Since(start).Milliseconds())
	}
	if reason == "quiet" {
		// Once per quiet spell: the next one starts with the next request.
		t.lastDone.Store(0)
	}
}
