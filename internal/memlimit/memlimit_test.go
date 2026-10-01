package memlimit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"":        0,
		"512MiB":  512 << 20,
		"512mib":  512 << 20,
		"400MB":   400_000_000,
		"1GiB":    1 << 30,
		"1.5G":    3 << 29,
		"1048576": 1 << 20,
		" 64 M ":  64 << 20,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"lots", "-1MiB", "12XB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

// "max" and v1's near-2^63 sentinel both mean no limit.
func TestReadLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := readLimit(write("a", "536870912\n")); got != 512<<20 {
		t.Errorf("readLimit = %d", got)
	}
	if got := readLimit(write("b", "max\n")); got != 0 {
		t.Errorf("max read as %d", got)
	}
	if got := readLimit(write("c", "9223372036854771712\n")); got != 0 {
		t.Errorf("v1 unlimited read as %d", got)
	}
	if got := readLimit(filepath.Join(dir, "absent")); got != 0 {
		t.Errorf("absent read as %d", got)
	}
}

// The trimmer waits for quiet and gives memory back once per quiet spell —
// never with a request in flight.
func TestTrimmerWaitsForQuiet(t *testing.T) {
	// Min 0: whether the runtime's own scavenger got there first must not
	// decide the outcome.
	tr := &Trimmer{Quiet: 50 * time.Millisecond, Min: 0}

	done := tr.Begin()
	time.Sleep(60 * time.Millisecond)
	tr.tick() // in flight: must do nothing
	done()

	tr.tick() // too soon after the request ended
	if tr.lastDone.Load() == 0 {
		t.Fatal("trimmed before the quiet period passed")
	}
	time.Sleep(60 * time.Millisecond)
	tr.tick()
	if tr.lastDone.Load() != 0 {
		t.Fatal("did not trim after a quiet period with memory to give back")
	}
}
