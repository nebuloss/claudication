//go:build unix

package handover

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
)

// Worker is the gateway's side of the protocol: the sockets it was handed,
// and the two messages it exchanges with the supervisor.
type Worker struct {
	// Beside is true when another worker is serving and goes on serving
	// until this one is ready: the database must be opened without
	// migrating it (store.OpenAlongside).
	Beside bool

	listeners map[string]net.Listener
	ready     *os.File
	readyOnce sync.Once

	released chan struct{}
	orphaned chan struct{}
}

// Inherited is the worker this process was started as, or nil when it was
// not started by a supervisor.
//
// The variables are cleared once read, so nothing this process might start
// mistakes itself for a worker.
func Inherited() (*Worker, error) {
	mode, ok := os.LookupEnv(EnvMode)
	if !ok {
		return nil, nil
	}
	names := os.Getenv(EnvListeners)
	_ = os.Unsetenv(EnvMode)
	_ = os.Unsetenv(EnvListeners)
	if mode != ModeAlone && mode != ModeBeside {
		return nil, fmt.Errorf("%s=%q: not a mode", EnvMode, mode)
	}

	w := &Worker{
		Beside:    mode == ModeBeside,
		listeners: map[string]net.Listener{},
		released:  make(chan struct{}),
		orphaned:  make(chan struct{}),
	}
	if names != "" {
		for i, name := range strings.Split(names, ",") {
			fd := fdFirstListener + i
			f := os.NewFile(uintptr(fd), name)
			ln, err := net.FileListener(f)
			// FileListener holds a duplicate; the inherited descriptor itself
			// would otherwise leak into anything this process starts.
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("inherited listener %s (fd %d): %w", name, fd, err)
			}
			w.listeners[name] = ln
		}
	}
	syscall.CloseOnExec(fdReady)
	syscall.CloseOnExec(fdControl)
	w.ready = os.NewFile(fdReady, "handover-ready")
	go w.watch(os.NewFile(fdControl, "handover-control"))
	return w, nil
}

// Listeners are the inherited sockets, by name.
func (w *Worker) Listeners() map[string]net.Listener { return w.listeners }

// Ready tells the supervisor this worker is serving every listener. The
// version goes into the supervisor's log line, which is how an operator
// reading it sees which binary took over.
func (w *Worker) Ready(version string) {
	w.readyOnce.Do(func() {
		_, _ = w.ready.WriteString(version + "\n")
		_ = w.ready.Close()
	})
}

// Released is closed when the supervisor hands the sockets to another worker:
// stop accepting, drain, exit, and record no outage.
func (w *Worker) Released() <-chan struct{} { return w.released }

// Orphaned is closed when the supervisor is gone without having released this
// worker. A stop: the supervisor's replacement will want the ports.
func (w *Worker) Orphaned() <-chan struct{} { return w.orphaned }

func (w *Worker) watch(control *os.File) {
	defer control.Close()
	r := bufio.NewReader(control)
	for {
		line, err := r.ReadString('\n')
		if line == releaseLine {
			close(w.released)
			// Nothing after a release means anything: the supervisor closes
			// the pipe straight after, and that end-of-file is not a death.
			return
		}
		if err != nil {
			close(w.orphaned)
			return
		}
	}
}
