//go:build unix

package handover

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Supported says whether this platform can hand over at all.
const Supported = true

// The environment a worker is started with. See the package comment.
const (
	// EnvMode says the process is a worker, and how it was started:
	// ModeAlone when no other worker is serving, ModeBeside when one is and
	// will go on serving until this one is ready.
	EnvMode = "CLAUDICATION_HANDOVER"
	// EnvListeners names the inherited sockets, comma-separated, in
	// descriptor order from fdFirstListener.
	EnvListeners = "CLAUDICATION_HANDOVER_LISTENERS"

	ModeAlone  = "alone"
	ModeBeside = "beside"
)

const (
	fdReady         = 3
	fdControl       = 4
	fdFirstListener = 5
)

// ExitBeside is the status a worker started beside another exits with when it
// cannot run beside it — a schema change is due — so the supervisor falls back
// to stopping the old one first. EX_TEMPFAIL: the same binary succeeds alone.
const ExitBeside = 75

// ExitListeners is the status a worker exits with when its config names other
// listeners than the sockets it was handed — the config was edited since the
// supervisor bound them. EX_CONFIG: a restart rebinds.
const ExitListeners = 78

// releaseLine is what the supervisor writes on the control pipe.
const releaseLine = "release\n"

// Listen is one address the supervisor binds, and the name a worker asks for
// it by.
type Listen struct {
	Name, Addr string
}

// Supervisor is the long-lived process: it holds the sockets and starts the
// workers. See the package comment.
type Supervisor struct {
	// Path and Args start a worker. Path is the binary on disk, absolute:
	// a reload runs whatever is there now, which is how a new release gets
	// in.
	Path string
	Args []string
	// Listen says which addresses to serve. Called once to bind, and again on
	// every reload to check nothing asks for an address the supervisor does
	// not hold.
	Listen func() ([]Listen, error)
	Log    *slog.Logger
	// ReadyTimeout bounds how long a worker started beside the serving one
	// may take to be ready before the reload is abandoned. A worker started
	// alone has no bound: it may be migrating the database, and there is
	// nothing to fall back to.
	ReadyTimeout time.Duration
	// Reloaded hears how each reload went. Optional; for tests.
	Reloaded func(Reload)

	bound   []Listen
	files   []*os.File
	addrMu  sync.Mutex
	addrs   []net.Addr
	cur     *worker
	workers []*worker
}

// Reload is the outcome of one reload.
type Reload struct {
	// Old and New are the worker pids before and after; equal when the
	// reload did not happen.
	Old, New int
	// Version is what the new worker reported.
	Version string
	// Restarted is a fallback: the old worker was stopped before the new
	// one started, because they could not run side by side.
	Restarted bool
	Err       error
}

type worker struct {
	cmd     *exec.Cmd
	ready   *os.File // our end of the ready pipe
	control *os.File // our end of the control pipe
	done    chan struct{}
	err     error
}

func (w *worker) pid() int { return w.cmd.Process.Pid }

func (w *worker) exited() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// release tells the worker to let go of the sockets and drain.
func (w *worker) release() {
	_, _ = w.control.WriteString(releaseLine)
	_ = w.control.Close()
}

// Addrs are the bound addresses, in Listen order, once Run has bound them.
func (s *Supervisor) Addrs() []net.Addr {
	s.addrMu.Lock()
	defer s.addrMu.Unlock()
	return s.addrs
}

// Run binds, starts the first worker, and supervises until ctx is done or the
// serving worker dies. Each value on reload is one handover.
//
// A worker that dies on its own is fatal: Run returns, and the service
// manager's own crash policy — the one it applies to every other daemon —
// decides what happens next, rather than a second, private one in here.
func (s *Supervisor) Run(ctx context.Context, reload <-chan struct{}) error {
	if err := s.bind(); err != nil {
		return err
	}
	defer s.closeSockets()

	w, err := s.start(ModeAlone)
	if err != nil {
		return err
	}
	if _, err := s.awaitReady(ctx, w, 0); err != nil {
		s.stopAll()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	s.cur = w

	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return nil
		case <-s.cur.done:
			s.stopAll()
			return fmt.Errorf("worker %d exited: %v", s.cur.pid(), s.cur.err)
		case <-reload:
			if err := s.reload(ctx); err != nil {
				s.stopAll()
				return err
			}
		}
	}
}

func (s *Supervisor) bind() error {
	want, err := s.Listen()
	if err != nil {
		return err
	}
	var addrs []net.Addr
	for _, l := range want {
		ln, err := net.Listen("tcp", l.Addr)
		if err != nil {
			s.closeSockets()
			return fmt.Errorf("listen on %s: %w", l.Addr, err)
		}
		f, err := keepable(ln.(*net.TCPListener))
		// The duplicate is what is kept and handed down; the listener it came
		// from has served its purpose.
		addr := ln.Addr()
		_ = ln.Close()
		if err != nil {
			s.closeSockets()
			return fmt.Errorf("listen on %s: %w", l.Addr, err)
		}
		s.files = append(s.files, f)
		addrs = append(addrs, addr)
	}
	s.bound = want
	s.addrMu.Lock()
	s.addrs = addrs
	s.addrMu.Unlock()
	return nil
}

// keepable is a copy of the listener's socket that can be handed to every
// worker without changing it under the ones already serving.
//
// Not TCPListener.File as it comes: that file is one Go put into non-blocking
// mode itself, and Fd — which os/exec calls on every ExtraFiles entry at each
// start — puts such a file back into blocking mode. O_NONBLOCK belongs to the
// socket, not to a descriptor, so every reload switched the serving worker's
// socket to blocking: its accept loop then sat in accept(2) where closing the
// listener cannot wake it, and a released worker with no new connection
// coming never finished draining (measured: a worker still "shutting down"
// eight minutes after a quiet stop). os.NewFile on a descriptor already
// non-blocking records nothing to undo, so its Fd leaves the socket alone.
func keepable(ln *net.TCPListener) (*os.File, error) {
	f, err := ln.File()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), ln.Addr().String()), nil
}

// closeSockets lets go of the supervisor's own copy of every socket. Once the
// workers have closed theirs too, the ports refuse: done first on a stop, so
// that a stop refuses connections rather than queueing them for nobody.
func (s *Supervisor) closeSockets() {
	for _, f := range s.files {
		_ = f.Close()
	}
	s.files = nil
}

func (s *Supervisor) start(mode string) (*worker, error) {
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	controlR, controlW, err := os.Pipe()
	if err != nil {
		readyR.Close()
		readyW.Close()
		return nil, err
	}
	names := make([]string, len(s.bound))
	for i, l := range s.bound {
		names[i] = l.Name
	}

	cmd := exec.Command(s.Path, s.Args...)
	cmd.Env = append(workerEnviron(), EnvMode+"="+mode, EnvListeners+"="+strings.Join(names, ","))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = append([]*os.File{readyW, controlR}, s.files...)
	err = cmd.Start()
	// The child's ends. Kept open here, the ready pipe would never report a
	// worker that died before writing, and the control pipe never tell a
	// worker that the supervisor did.
	readyW.Close()
	controlR.Close()
	if err != nil {
		readyR.Close()
		controlW.Close()
		return nil, fmt.Errorf("start worker %s: %w", s.Path, err)
	}
	w := &worker{cmd: cmd, ready: readyR, control: controlW, done: make(chan struct{})}
	go func() {
		w.err = cmd.Wait()
		close(w.done)
	}()
	// The workers that have drained and gone are forgotten here, the one
	// place the list grows.
	s.workers = slices.DeleteFunc(s.workers, func(w *worker) bool {
		if w.exited() {
			_ = w.control.Close()
			return true
		}
		return false
	})
	s.workers = append(s.workers, w)
	s.Log.Info("worker started", "pid", w.pid(), "mode", mode, "binary", s.Path)
	return w, nil
}

// workerEnviron is this process's environment without the variables that
// would tell a worker something stale.
func workerEnviron() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, EnvMode+"=") || strings.HasPrefix(kv, EnvListeners+"=")
	})
}

var errNotReady = errors.New("worker exited before it was ready")

// awaitReady waits for the worker's ready line. timeout 0 waits as long as the
// worker lives.
func (s *Supervisor) awaitReady(ctx context.Context, w *worker, timeout time.Duration) (string, error) {
	type line struct {
		s   string
		err error
	}
	got := make(chan line, 1)
	go func() {
		v, err := bufio.NewReader(w.ready).ReadString('\n')
		w.ready.Close()
		got <- line{strings.TrimSpace(v), err}
	}()
	var expire <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	select {
	case l := <-got:
		if l.err != nil {
			// The pipe closes when the worker exits; wait for the exit, so
			// the caller can read the status.
			<-w.done
			return "", fmt.Errorf("%w: %v", errNotReady, w.err)
		}
		return l.s, nil
	case <-expire:
		return "", fmt.Errorf("worker %d not ready after %s", w.pid(), timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// reload hands over to a new worker. Only an error that leaves nothing
// serving is returned; one the old worker survives is logged and reported.
func (s *Supervisor) reload(ctx context.Context) error {
	old := s.cur
	r := Reload{Old: old.pid(), New: old.pid()}
	report := func() {
		if r.Err != nil {
			s.Log.Error("reload abandoned; the serving worker carries on", "pid", old.pid(), "err", r.Err)
		}
		if s.Reloaded != nil {
			s.Reloaded(r)
		}
	}

	want, err := s.Listen()
	if err == nil && !slices.Equal(want, s.bound) {
		err = fmt.Errorf("the listen addresses changed (%v, bound %v): a restart rebinds them", want, s.bound)
	}
	if err != nil {
		r.Err = err
		report()
		return nil
	}

	w, err := s.start(ModeBeside)
	if err != nil {
		r.Err = err
		report()
		return nil
	}
	version, err := s.awaitReady(ctx, w, s.ReadyTimeout)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return nil // Run is stopping everything
	case exitStatus(w) == ExitBeside:
		return s.restart(ctx, &r, report)
	case exitStatus(w) == ExitListeners:
		r.Err = fmt.Errorf("the new worker's config names other listeners than the supervisor holds: a restart rebinds them")
		report()
		return nil
	default:
		s.kill(w)
		r.Err = err
		report()
		return nil
	}

	s.cur = w
	old.release()
	r.New, r.Version = w.pid(), version
	s.Log.Info("handed over", "old", r.Old, "new", r.New, "version", version)
	report()
	return nil
}

// restart is the fallback when the new worker cannot run beside the old: stop
// the old one, wait for it to exit, and start the new one alone. The sockets
// stay bound, so connections wait instead of being refused, but nothing
// answers them until the new worker is ready — the old one records that as an
// outage when it stops.
func (s *Supervisor) restart(ctx context.Context, r *Reload, report func()) error {
	old := s.cur
	r.Restarted = true
	s.Log.Warn("the new worker cannot run beside the old (a schema change is due); restarting instead", "pid", old.pid())
	_ = old.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-old.done:
	case <-ctx.Done():
		return nil
	}
	w, err := s.start(ModeAlone)
	if err != nil {
		r.Err = err
		report()
		return err
	}
	version, err := s.awaitReady(ctx, w, 0)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		r.Err = err
		report()
		return err
	}
	s.cur = w
	r.New, r.Version = w.pid(), version
	s.Log.Info("restarted", "old", r.Old, "new", r.New, "version", version)
	report()
	return nil
}

func exitStatus(w *worker) int {
	if !w.exited() {
		return -1
	}
	var ee *exec.ExitError
	if errors.As(w.err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

func (s *Supervisor) kill(w *worker) {
	_ = w.cmd.Process.Kill()
	<-w.done
}

// stopAll stops every worker still running and waits for them. No deadline of
// its own: the service manager's stop schedule bounds it, and it is the one
// that should.
func (s *Supervisor) stopAll() {
	s.closeSockets()
	for _, w := range s.workers {
		if !w.exited() {
			_ = w.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	for _, w := range s.workers {
		<-w.done
		_ = w.control.Close()
	}
	s.workers = nil
}
