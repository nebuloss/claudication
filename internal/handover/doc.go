// Package handover lets the gateway replace its own binary without a moment
// in which its ports refuse a connection.
//
// # The problem it solves
//
// A stop drains: the listeners close at once, and the process lives on until
// the longest open stream ends, up to shutdown.grace. A service manager starts
// the replacement only once the old process has exited, so for that whole
// drain nothing accepts. Measured in production on 2026-10-20: one 30-second
// stream was 30 seconds in which the admin UI and every new client request
// were refused.
//
// # The shape
//
// `serve` runs two kinds of process. The one the service manager starts is
// the supervisor: it binds every listener, starts a worker from the binary on
// disk, hands it the sockets, and does nothing else for as long as the
// service runs. The worker is the gateway itself.
//
// On SIGHUP the supervisor starts a second worker from whatever binary is on
// disk now, hands it the same sockets, and waits for it to say it is ready.
// Only then does it tell the first one to let go. The first closes its copy of
// the sockets — the sockets themselves stay open, held by the supervisor and
// the new worker — finishes what it was answering, and exits. At every
// instant some process is accepting on every port, and the kernel's accept
// queue is one queue: a connection that arrives during the overlap is taken by
// whichever worker calls accept first, and after the release only the new one
// does.
//
// # Why not the alternatives
//
// The process a service manager tracks must outlive the handover, because the
// manager reads that process exiting as the service having stopped:
//
//   - OpenRC's supervise-daemon tracks one pid, the child it forked, and
//     respawns as soon as that pid exits (supervise-daemon.c: waitpid on
//     child_pid, then fork again). A tableflip-style handover — the old
//     process forks the new one and exits — would have it start a third
//     process into ports already taken, or, with SO_REUSEPORT, alongside the
//     second. It has no MAINPID and no way to be told the pid changed.
//   - systemd can follow a changed main pid (Type=notify, NotifyAccess=all,
//     MAINPID= from the child), so tableflip works there; but it would be one
//     mechanism for systemd and another for OpenRC, and the production box is
//     OpenRC.
//   - SO_REUSEPORT alone does not help: the manager still starts the new
//     process only after the old has exited, and with both bound the kernel
//     load-balances new connections onto a process that is draining.
//   - Socket activation (systemd holding the socket) turns refusals into
//     waits, but the new process still starts only after the old one exits,
//     so a 30-second stream is still 30 seconds of nothing answered, and
//     OpenRC has no equivalent.
//
// A supervisor whose pid never changes is the same thing under both: the
// manager sees one long-lived process, stop and crash-respawn keep their
// meaning, and the handover is invisible to it. The cost is a second small
// process, and that the supervisor itself is replaced only by a real restart —
// which is why it does as little as it does: it binds, it forks, it relays
// three messages. Everything that changes from release to release is in the
// worker.
//
// # The protocol
//
// A worker finds out it is one from its environment (EnvMode). Its file
// descriptors are fixed: 3 is a pipe it writes one line to when it is
// serving, 4 is a pipe it reads from, and 5 onwards are the listening
// sockets, named in EnvListeners.
//
//   - Ready: the worker writes its version and a newline on 3 once every
//     listener is being served. Exiting before that is a failed start.
//   - Release: the supervisor writes "release" on 4. The worker stops
//     accepting and drains, and records no outage, because nothing was
//     refused.
//   - Stop: SIGTERM, as before, or end-of-file on 4 without a release first —
//     the supervisor died, and a worker that kept the ports would leave the
//     restarted supervisor unable to bind them. A stop is an outage and is
//     recorded as one (internal/service/uptime).
//
// # The database during the overlap
//
// For up to shutdown.grace both workers have the database open. SQLite in
// WAL mode with a busy timeout is safe for two writers; what is not safe is a
// schema change, because the old worker would go on writing with the old
// one. So a worker started beside another opens the store with
// store.OpenAlongside, which refuses to migrate, and exits with ExitBeside
// when a migration is due. The supervisor then falls back to what a restart
// always did: it stops the old worker (an outage, recorded), waits for it to
// exit, and starts the new one alone, which migrates. The sockets stay bound
// throughout, so connections in that gap wait in the accept queue rather than
// being refused — but the gateway answered nothing, and the record says so.
//
// The second thing two processes must not do at once is refresh the same
// account's token: providers rotate refresh tokens, and the loser's is
// already spent. FileLock is held around every exchange, across processes.
//
// # What a handover does not carry over
//
// Everything held in memory: per-minute rate-limit windows, admin sessions
// (the UI asks for the password again), OAuth sign-ins waiting for their
// callback, the image cache. A restart lost them too.
// Listen addresses are the supervisor's, bound once; a reload whose config
// names different ones is refused, and takes a restart. A worker that finds
// its config naming other listeners than the sockets it was handed exits
// with ExitListeners rather than leave one with nobody accepting on it.
//
// # What two workers count apart
//
// For the length of the drain, the counts held in memory are per worker:
// concurrent requests per key, leases per account. A key with streams open
// on the old worker can open as many again on the new one. That is accepted,
// not shared: the excess is bounded by what was in flight at the release and
// lasts only while it finishes, and a count passed over the pipe would be
// stale the moment one of those streams ended. Token budgets are read from
// the database both write to, so they hold.
package handover
