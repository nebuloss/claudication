//go:build !unix

package handover

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"
)

// Supported says whether this platform can hand over at all. Not here: there
// is no descriptor inheritance by number, and no service manager this
// gateway is installed under. `serve` runs as one process.
const Supported = false

const (
	EnvMode      = "CLAUDICATION_HANDOVER"
	EnvListeners = "CLAUDICATION_HANDOVER_LISTENERS"
	ExitBeside   = 75
)

type Listen struct{ Name, Addr string }

type Reload struct {
	Old, New  int
	Version   string
	Restarted bool
	Err       error
}

type Supervisor struct {
	Path         string
	Args         []string
	Listen       func() ([]Listen, error)
	Log          *slog.Logger
	ReadyTimeout time.Duration
	Reloaded     func(Reload)
}

func (s *Supervisor) Run(context.Context, <-chan struct{}) error {
	return errors.New("handover is not supported on this platform")
}

func (s *Supervisor) Addrs() []net.Addr { return nil }

type Worker struct{ Beside bool }

func Inherited() (*Worker, error) { return nil, nil }

func (w *Worker) Listeners() map[string]net.Listener { return nil }
func (w *Worker) Ready(string)                       {}
func (w *Worker) Released() <-chan struct{}          { return nil }
func (w *Worker) Orphaned() <-chan struct{}          { return nil }

// FileLock is a no-op: with no handover there is never a second process to
// exclude.
func FileLock(string) func(context.Context, string) (func(), error) {
	return func(context.Context, string) (func(), error) { return func() {}, nil }
}
