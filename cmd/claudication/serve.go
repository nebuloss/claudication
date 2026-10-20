package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"claudication/internal/config"
	"claudication/internal/handover"
	"claudication/internal/httpapi"
	"claudication/internal/logging"
	"claudication/internal/memlimit"
	"claudication/internal/secret"
	"claudication/internal/store"
	"claudication/internal/version"
)

// handoverReadyTimeout bounds how long a reload waits for the new worker.
// Starting beside a serving one never migrates, so it is seconds; the bound
// is for a binary that hangs, which the old worker then outlives.
const handoverReadyTimeout = 45 * time.Second

// cmdServe runs the gateway: as a supervisor and its worker, which is what
// lets SIGHUP replace the binary without refusing a connection
// (internal/handover), or with -single as one process, the way it always ran.
//
// The same command is the worker: a supervisor starts the binary with the
// arguments it was given and the handover environment, and that environment,
// not a flag, is what tells the two apart — so a unit written for a binary
// from before the handover still starts it, and a rollback to one still runs.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config.yaml (optional)")
	single := fs.Bool("single", false, "serve in this one process; a reload is then a restart")
	if err := fs.Parse(args); err != nil {
		return err
	}

	w, err := handover.Inherited()
	if err != nil {
		return err
	}

	// Both signals drain the same way. SIGTERM is what containers and
	// systemd actually send.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch {
	case w != nil:
		// SIGHUP is the supervisor's. One reaching a worker — a signal to
		// the process group, a stray kill — would otherwise end it with no
		// drain at all, which is Go's default for it.
		signal.Ignore(syscall.SIGHUP)
		return serveWorker(ctx, w, *configPath)
	case *single || !handover.Supported:
		return serveOnce(ctx, *configPath, httpapi.Handoff{}, false)
	default:
		return supervise(ctx, args, *configPath)
	}
}

// supervise is the process the service manager tracks: it binds, starts the
// worker, and turns each SIGHUP into a handover. It opens no database and
// serves no request, so there is next to nothing in it that a release could
// need to change — it is replaced only by a real restart.
func supervise(ctx context.Context, args []string, configPath string) error {
	path := resolveConfigPath(configPath)
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format).With("role", "supervisor")

	exe, err := workerBinary()
	if err != nil {
		return err
	}

	reload := make(chan struct{}, 1)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			// One pending is enough: a second SIGHUP during a handover asks
			// for what is already happening.
			select {
			case reload <- struct{}{}:
			default:
			}
		}
	}()

	sup := &handover.Supervisor{
		Path: exe,
		Args: append([]string{"serve"}, args...),
		Listen: func() ([]handover.Listen, error) {
			// Read again on every reload, so a reload against a config that
			// now names other addresses is refused rather than half-applied.
			c, err := config.Load(path)
			if err != nil {
				return nil, err
			}
			return listenAddrs(c), nil
		},
		Log:          log,
		ReadyTimeout: handoverReadyTimeout,
	}
	return sup.Run(ctx, reload)
}

func listenAddrs(c config.Config) []handover.Listen {
	out := []handover.Listen{{Name: httpapi.ListenRelay, Addr: c.Listen}}
	if c.AdminListen != "" {
		out = append(out, handover.Listen{Name: httpapi.ListenAdmin, Addr: c.AdminListen})
	}
	return out
}

// workerBinary is the path a reload starts, absolute.
//
// The path rather than os.Executable: on Linux that reads /proc/self/exe,
// which follows the inode this process was started from — the old binary,
// still there under a deleted name once the installer has renamed the new one
// into place. A reload exists to run the new one.
func workerBinary() (string, error) {
	exe := os.Args[0]
	if filepath.Base(exe) == exe {
		p, err := exec.LookPath(exe)
		if err != nil {
			return "", fmt.Errorf("find own binary %q: %w", exe, err)
		}
		exe = p
	}
	return filepath.Abs(exe)
}

// serveWorker is the gateway under a supervisor: its sockets inherited, its
// database opened beside the worker it takes over from when there is one.
func serveWorker(ctx context.Context, w *handover.Worker, configPath string) error {
	// The supervisor gone without a word is a stop: its replacement will
	// want the ports.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-w.Orphaned():
			cancel()
		case <-ctx.Done():
		}
	}()
	return serveOnce(ctx, configPath, httpapi.Handoff{
		Listeners: w.Listeners(),
		Ready:     func() { w.Ready(version.Version) },
		Release:   w.Released(),
	}, w.Beside)
}

// serveOnce opens the state and serves until ctx is done or the handoff
// releases it. beside opens the database without migrating it; a database
// that needs one ends the process with handover.ExitBeside, which the
// supervisor answers by stopping the other worker first.
func serveOnce(ctx context.Context, configPath string, h httpapi.Handoff, beside bool) error {
	cfg, err := config.Load(resolveConfigPath(configPath))
	if err != nil {
		return err
	}
	if err := cfg.EnsureStateDir(); err != nil {
		return err
	}
	sealer, err := secret.Load(cfg.StateDir)
	if err != nil {
		return err
	}
	log := logging.New(cfg.Log.Level, cfg.Log.Format)

	open := store.Open
	if beside {
		open = store.OpenAlongside
	}
	st, err := open(ctx, cfg.DBPath(), sealer)
	if errors.Is(err, store.ErrSchemaChange) {
		log.Warn("not starting beside the serving worker", "err", err)
		return exitWith{code: handover.ExitBeside, err: err}
	}
	if err != nil {
		return err
	}
	defer st.Close()

	if err := st.CompactErr(); err != nil {
		log.Warn("database not compacted", "err", err)
	}

	// Before anything allocates in earnest. Validated by config.Load already.
	limit, _ := memlimit.ParseSize(cfg.MemoryLimit)
	memlimit.Apply(limit, log)

	srv, err := httpapi.New(cfg, log, st)
	if err != nil {
		return err
	}
	return srv.Serve(ctx, h)
}

// exitWith is an error that ends the process with a status of its own.
type exitWith struct {
	code int
	err  error
}

func (e exitWith) Error() string { return e.err.Error() }
func (e exitWith) Unwrap() error { return e.err }

func exitCode(err error) int {
	var e exitWith
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}
