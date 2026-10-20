//go:build unix

package handover

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The test binary is its own worker: a supervisor under test starts it again
// with the handover environment, and then it is the small server below
// instead of the suite.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv(EnvMode); ok {
		os.Exit(testWorker())
	}
	os.Exit(m.Run())
}

// What the second worker does, from the environment the test sets.
const envBeside = "HANDOVER_TEST_BESIDE"

// testWorker serves /pid and /stream on the inherited socket, and follows the
// protocol: ready once serving, drain on a release or a stop.
func testWorker() int {
	// A binary that dies before it has looked at anything: the broken
	// release, which never touches the sockets it was handed.
	if os.Getenv(EnvMode) == ModeBeside && os.Getenv(envBeside) == "fail" {
		return 1
	}
	w, err := Inherited()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if w.Beside {
		switch os.Getenv(envBeside) {
		case "refuse":
			return ExitBeside
		case "hang":
			select {}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pid", func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, os.Getpid())
	})
	// A stream as long as asked for, a line every 50 ms: what a draining
	// worker has to finish.
	mux.HandleFunc("/stream", func(rw http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		fmt.Fprintf(rw, "pid %d\n", os.Getpid())
		rw.(http.Flusher).Flush()
		for end := time.Now().Add(time.Duration(ms) * time.Millisecond); time.Now().Before(end); {
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(rw, "tick\n")
			rw.(http.Flusher).Flush()
		}
		fmt.Fprint(rw, "done\n")
	})
	srv := &http.Server{Handler: mux}
	pending := Track(srv)
	var loops sync.WaitGroup
	for _, ln := range w.Listeners() {
		loops.Add(1)
		go func() {
			defer loops.Done()
			_ = srv.Serve(ln)
		}()
	}
	w.Ready("test")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	select {
	case <-w.Released():
	case <-w.Orphaned():
	case <-stop:
	}
	// As httpapi.Server drains: stop accepting, let what was accepted reach
	// a handler, then Shutdown.
	srv.SetKeepAlivesEnabled(false)
	for _, ln := range w.Listeners() {
		ln.Close()
	}
	loops.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pending.Settle(ctx)
	if err := srv.Shutdown(ctx); err != nil {
		return 1
	}
	return 0
}

type harness struct {
	t       *testing.T
	sup     *Supervisor
	cancel  context.CancelFunc
	reload  chan struct{}
	done    chan error
	reloads chan Reload
	base    string
	// listen is the address the config names, changed by a test between
	// reloads.
	listen atomic.Value
}

func startSupervisor(t *testing.T, readyTimeout time.Duration) *harness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, reload: make(chan struct{}, 1), done: make(chan error, 1), reloads: make(chan Reload, 4)}
	bound := make(chan struct{})
	h.listen.Store("127.0.0.1:0")
	h.sup = &Supervisor{
		Path: exe,
		Listen: func() ([]Listen, error) {
			return []Listen{{Name: "main", Addr: h.listen.Load().(string)}}, nil
		},
		Log:          slog.New(slog.DiscardHandler),
		ReadyTimeout: readyTimeout,
		Reloaded:     func(r Reload) { h.reloads <- r },
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- h.sup.Run(ctx, h.reload) }()
	go func() {
		for h.sup.Addrs() == nil {
			time.Sleep(5 * time.Millisecond)
		}
		close(bound)
	}()
	select {
	case <-bound:
	case err := <-h.done:
		t.Fatalf("supervisor exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor never bound")
	}
	h.base = "http://" + h.sup.Addrs()[0].String()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(30 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	h.waitPID()
	return h
}

// client makes every request on a new connection, so each one is a fresh
// accept: a refused connect is exactly what a handover must never produce.
var client = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true},
	Timeout:   10 * time.Second,
}

func (h *harness) pid() (int, error) {
	resp, err := client.Get(h.base + "/pid")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(b))
}

func (h *harness) waitPID() int {
	h.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if p, err := h.pid(); err == nil {
			return p
		}
	}
	h.t.Fatal("no worker answered")
	return 0
}

func (h *harness) handOver() Reload {
	h.t.Helper()
	h.reload <- struct{}{}
	select {
	case r := <-h.reloads:
		return r
	case err := <-h.done:
		h.t.Fatalf("supervisor exited during the reload: %v", err)
	case <-time.After(30 * time.Second):
		h.t.Fatal("reload never finished")
	}
	return Reload{}
}

// hammer requests /pid as fast as it can until stopped, and counts what it
// saw: every pid that answered, and every request that failed.
type hammer struct {
	stop     chan struct{}
	wg       sync.WaitGroup
	ok, fail atomic.Int64
	mu       sync.Mutex
	pids     map[int]int
	errs     []string
}

func (h *harness) hammer(workers int) *hammer {
	m := &hammer{stop: make(chan struct{}), pids: map[int]int{}}
	for range workers {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			for {
				select {
				case <-m.stop:
					return
				default:
				}
				p, err := h.pid()
				m.mu.Lock()
				if err != nil {
					m.fail.Add(1)
					if len(m.errs) < 5 {
						m.errs = append(m.errs, err.Error())
					}
				} else {
					m.ok.Add(1)
					m.pids[p]++
				}
				m.mu.Unlock()
			}
		}()
	}
	return m
}

func (m *hammer) finish(t *testing.T) map[int]int {
	t.Helper()
	close(m.stop)
	m.wg.Wait()
	if n := m.fail.Load(); n > 0 {
		t.Errorf("%d of %d requests failed, e.g. %v", n, n+m.ok.Load(), m.errs)
	}
	t.Logf("%d requests, none refused; answered by %v", m.ok.Load(), m.pids)
	return m.pids
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if !alive(pid) {
			return
		}
	}
	t.Errorf("worker %d still running after %s", pid, within)
}

// The whole point: through a handover, every connection is accepted, the
// stream open on the old worker runs to its end, new requests reach the new
// worker, and the old one exits once it has nothing left to answer.
func TestHandoverNeverRefusesAConnection(t *testing.T) {
	h := startSupervisor(t, 10*time.Second)
	old := h.waitPID()

	// A stream on the old worker that outlives the handover by a second.
	type result struct {
		body string
		err  error
		at   time.Time
	}
	streamed := make(chan result, 1)
	resp, err := client.Get(h.base + "/stream?ms=1500")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if first != fmt.Sprintf("pid %d\n", old) {
		resp.Body.Close()
		t.Fatalf("stream opened on %q, want the old worker %d", first, old)
	}
	go func() {
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		streamed <- result{string(b), err, time.Now()}
	}()

	load := h.hammer(4)
	time.Sleep(200 * time.Millisecond)
	r := h.handOver()
	if r.Err != nil || r.Restarted || r.New == old || r.Old != old {
		t.Fatalf("reload = %+v, want a handover from %d", r, old)
	}
	// Released, and still answering the stream.
	if !alive(old) {
		t.Error("the old worker exited with a stream still open")
	}
	// Past the release, only the new worker accepts.
	time.Sleep(100 * time.Millisecond)
	for range 20 {
		if p, err := h.pid(); err != nil || p != r.New {
			t.Errorf("after the release: pid %d, err %v; want the new worker %d", p, err, r.New)
		}
	}

	s := <-streamed
	if s.err != nil || !strings.HasSuffix(s.body, "done\n") {
		t.Errorf("stream on the old worker ended %q, %v; want it to run to the end", s.body, s.err)
	}
	waitGone(t, old, 5*time.Second)
	pids := load.finish(t)
	if pids[old] == 0 || pids[r.New] == 0 {
		t.Errorf("answered by %v; want both %d and %d", pids, old, r.New)
	}
}

// A new binary that cannot start costs nothing: the old worker was never told
// to let go.
func TestAFailedHandoverLeavesTheOldServing(t *testing.T) {
	for _, mode := range []string{"fail", "hang"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(envBeside, mode)
			h := startSupervisor(t, time.Second)
			old := h.waitPID()
			load := h.hammer(2)
			r := h.handOver()
			if r.Err == nil || r.New != old {
				t.Errorf("reload = %+v, want it abandoned on %d", r, old)
			}
			load.finish(t)
			if p := h.waitPID(); p != old {
				t.Errorf("serving %d after the failed reload, want %d", p, old)
			}
		})
	}
}

// A worker that cannot run beside the old one — a schema change is due —
// gets a stop and a start instead. The sockets stay bound throughout, so even
// then a connection waits rather than being refused.
func TestASchemaChangeFallsBackToARestart(t *testing.T) {
	t.Setenv(envBeside, "refuse")
	h := startSupervisor(t, 10*time.Second)
	old := h.waitPID()
	load := h.hammer(2)
	r := h.handOver()
	if r.Err != nil || !r.Restarted || r.New == old {
		t.Fatalf("reload = %+v, want a restart onto a new worker", r)
	}
	waitGone(t, old, 5*time.Second)
	load.finish(t)
	if p := h.waitPID(); p != r.New {
		t.Errorf("serving %d, want %d", p, r.New)
	}
}

// Starting a worker must not switch the shared socket to blocking mode (see
// keepable). If it does, the serving worker's next accept after a connection
// is a blocking accept(2) that closing the listener cannot wake, and once the
// traffic stops, a stop never finishes. A worker that dies before touching
// its sockets is what leaves the mode switched; the traffic afterwards is what
// puts the old worker into accept(2); the quiet stop is what then hangs.
func TestAStopAfterAFailedReloadFinishes(t *testing.T) {
	t.Setenv(envBeside, "fail")
	h := startSupervisor(t, 10*time.Second)
	h.waitPID()
	if r := h.handOver(); r.Err == nil {
		t.Fatal("the failing worker took over")
	}
	for range 50 {
		if _, err := h.pid(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a quiet stop after a failed reload did not finish")
	}
	h.done <- nil
}

// A stop is a stop: the ports refuse once the workers have drained.
func TestStopLetsGoOfThePorts(t *testing.T) {
	h := startSupervisor(t, 10*time.Second)
	old := h.waitPID()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Run = %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	h.done <- nil // for the cleanup
	if alive(old) {
		t.Errorf("worker %d outlived the supervisor's stop", old)
	}
	if _, err := h.pid(); err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("after the stop: %v, want connection refused", err)
	}
}

// A worker that dies on its own takes the supervisor with it, so the service
// manager's crash policy is the one that applies.
func TestAWorkerCrashIsTheSupervisors(t *testing.T) {
	h := startSupervisor(t, 10*time.Second)
	pid := h.waitPID()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	select {
	case err := <-h.done:
		if err == nil {
			t.Error("Run returned nil after its worker was killed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor outlived its worker")
	}
	h.done <- nil
}

// A config that names other addresses than the ones bound cannot be handed
// over: the new worker would serve sockets the operator no longer asked for.
func TestAReloadWithNewAddressesIsRefused(t *testing.T) {
	h := startSupervisor(t, 10*time.Second)
	old := h.waitPID()
	h.listen.Store("127.0.0.1:1")
	r := h.handOver()
	if r.Err == nil || r.New != old {
		t.Errorf("reload = %+v, want it refused", r)
	}
}

// The control pipe: a release is a release, and the end of the pipe without
// one is the supervisor gone.
func TestWorkerReadsTheControlPipe(t *testing.T) {
	for _, tc := range []struct {
		send     string
		released bool
	}{{releaseLine, true}, {"", false}, {"noise\n", false}} {
		r, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w := &Worker{released: make(chan struct{}), orphaned: make(chan struct{})}
		go w.watch(r)
		_, _ = wr.WriteString(tc.send)
		wr.Close()
		select {
		case <-w.released:
			if !tc.released {
				t.Errorf("%q: released", tc.send)
			}
		case <-w.orphaned:
			if tc.released {
				t.Errorf("%q: orphaned", tc.send)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: neither", tc.send)
		}
	}
}

// Two holders of one name take turns; two names do not wait on each other.
func TestFileLockExcludesAcrossHolders(t *testing.T) {
	lock := FileLock(t.TempDir())
	ctx := context.Background()
	unlock, err := lock(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := lock(ctx, "acct-2")
	if err != nil {
		t.Fatalf("a second name waited on the first: %v", err)
	}
	other()

	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if u, err := lock(short, "acct-1"); err == nil {
		u()
		t.Fatal("the same name was locked twice")
	}
	unlock()
	u, err := lock(ctx, "acct-1")
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	u()
}
