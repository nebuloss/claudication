# Deploying without refusing a connection

An update used to be a stop and a start, and a stop drains: the listeners
close at once and the process lives on until its longest open stream ends, up
to `shutdown.grace` (two minutes). The service manager starts the new process
only once the old one has exited, so for the whole drain nothing accepted a
connection. In production on 2026-10-20 one 30-second stream was 30 seconds
in which the admin UI and every new client request were refused.

Now `scripts/install.sh` hands over instead, and `reload` is the restart that
refuses nothing:

    rc-service claudication reload      # OpenRC
    systemctl reload claudication       # systemd

`restart` is still a stop, recorded as one.

## How

`claudication serve` is two processes. The one the service manager starts is a
**supervisor**: it binds every listener, starts a **worker** from the binary on
disk, passes it the sockets, and does nothing else for the life of the
service. The worker is the gateway.

On SIGHUP — which is what `reload` sends — the supervisor starts a second
worker from whatever binary is at the path now, passes it the same sockets,
and waits for it to say it is serving. Then, and only then, it tells the first
to let go. The first closes its copy of the sockets (they stay open, held by
the supervisor and the new worker), answers what it already accepted, finishes
its open streams, and exits. The ports are never closed, so nothing is
refused; the kernel's accept queue is one queue, so a connection arriving
during the overlap is taken by whichever worker accepts first, and after the
release only by the new one.

The full protocol, and why each part of it is the way it is, is
`go doc ./internal/handover`.

### Why this shape

The process a service manager tracks has to outlive the handover, because the
manager reads that process exiting as the service having stopped.

| | OpenRC `supervise-daemon` | systemd |
|---|---|---|
| Tracks | one pid, the child it forked | `MainPID` |
| That pid exits | respawns at once | restarts or marks failed |
| Can be told the pid changed | no | yes: `Type=notify`, `NotifyAccess=all`, `MAINPID=` |

So a tableflip-style handover — the old process forks the new one and exits —
works under systemd and not under OpenRC, where supervise-daemon would start a
third process into the ports (`supervise-daemon.c`: `waitpid(child_pid)`,
then `fork` again). Production is OpenRC. `SO_REUSEPORT` on its own does not
help either way: the manager still starts the new process only after the old
one exits. Socket activation turns refusals into waits but has the same
ordering, and OpenRC has none.

A supervisor whose pid never changes is the same mechanism under both: stop,
crash-respawn and status keep their meaning, and the handover is invisible to
the manager. The cost is a second, small process, and that the supervisor
itself is replaced only by a real restart — which is why it does as little as
it does: it binds, it starts workers, and it relays three messages.

## The database during the overlap

For up to `shutdown.grace` both workers have the database open. SQLite in WAL
mode with a busy timeout is safe for two writers. A schema change under a
worker still running the old schema is not, so a worker started beside another
never migrates: when the binary needs a schema change, it exits with status 75
and the supervisor falls back to a stop and a start — it stops the old worker,
waits for it to exit, and starts the new one alone, which migrates. The
sockets stay bound throughout, so connections wait in the accept queue rather
than being refused, but nothing answers them until the new worker is up, and
the uptime record counts it as an outage.

The other thing two processes must not do at once is refresh the same
account's token: providers rotate refresh tokens, and the slower process would
spend one that is already spent, taking the account offline until someone
signs it in again. Every exchange holds a per-account file lock under
`$STATE_DIR/locks`, across processes.

## The uptime record

A handover is not an outage and is not recorded as one: nothing was refused.
Every window in which connections are refused or go unanswered still is — a
stop, a restart, the schema-change fallback — from the moment the worker stops
accepting to the moment the next one serves (`internal/service/uptime`).

## What `install.sh` does

1. Fetch the new binary and prove it runs; snapshot the state with the
   installed binary. Unchanged.
2. Rewrite the service file: the OpenRC script gains `reload`, the systemd unit
   `ExecReload` and `KillMode=mixed`.
3. Ask `/health` whether the running gateway is `"reloadable":true` — only a
   worker under the supervisor says so.
   - **Yes**: rename the new binary into place, reload, and wait until
     `/health` reports the new version on every port. Then claim the gateway
     (`passwd --if-unset`), which runs the new binary against the database and
     so waits until the new worker has opened it.
   - **No** — a release from before the handover, a stopped service, or
     `HANDOVER=0`: stop, wait for every port to be released, swap, start, as
     before. It says so, because this path refuses connections.
4. On failure, roll back:
   - the new worker never got ready (the old version is still answering after
     `HANDOVER_TIMEOUT`, 60 s): the supervisor already abandoned the reload,
     so putting the previous binary back is all there is to do;
   - the new version took over and then failed: reload back to the previous
     binary if it was itself serving under the supervisor, otherwise stop and
     start it.

The new binary is copied beside the old one and renamed over it, never written
in place: a running executable refuses to be written ("Text file busy"), and
the rename leaves the running process its own inode.

`scripts/test-install.sh` (part of `make check`) checks these decisions
against stubs.

## The first update onto a release from before the handover

That release cannot be handed over to or from — it has no supervisor and says
nothing about `reloadable` — so the first update onto this one is a stop and a
start, with the outage that has always come with it. From then on the
supervisor is running, and every later update is a handover.

## Measured

On dev-build, 2026-10-20. Four client threads opening a new connection for
every `/health` request, on the relay and the admin port, and one request held
open for 22 seconds on the admin port (its body sent a byte at a time) across
the update.

Under OpenRC 0.63.2, in an Alpine 3.24 user namespace, with the real
`install.sh` driving `rc-service` and `supervise-daemon`:

| update | requests | refused | other errors | long request |
|---|---|---|---|---|
| the release before this one → this one (stop and start) | 69,871 | 9,663, over 19.0 s | 2 (reset, EOF) | completed |
| this one → this one (handover) | 287,171 | **0** | **0** | completed on the old worker, 19 s after the handover |
| this one → a binary that cannot serve (handover, rolled back) | 777,162 | **0** | **0** | completed; nothing was stopped |

The uptime record for the first two: an outage of 19 s, the length of the refusals, and none.

Under systemd 257 (a `--user` unit with this unit's `Type=exec`,
`ExecReload` and `KillMode=mixed`), binary renamed into place and
`systemctl reload`: 281,673 requests, none refused, the long request
completed, and `MainPID` the same before and after.

The container's stand-ins, for the record: `supervise-daemon` and `su` were
wrapped to skip `setgroups`, which an unprivileged user namespace denies, and
`networking` was a stub that only provides `net`. Nothing in `install.sh` or the
init script it writes was changed.

## What a handover does not carry over

Everything held in memory, as a restart lost it: admin sessions (the UI asks
for the password again), OAuth sign-ins waiting for their callback, per-minute
rate-limit windows, the image cache.

A keep-alive connection idle on the old worker when it lets go is closed, as
any HTTP server's graceful shutdown closes one; a client that sends on it at
that instant sees the connection close and retries on a new one, which the new
worker answers. Connections the old worker has accepted are answered before it
exits (`handover.Pending`).

Listen addresses are the supervisor's, bound once. A reload whose config names
different ones is refused — logged, the old worker carries on — and needs a
restart.

`MemoryMax` in the systemd unit covers both workers for the length of the
drain; at tens of megabytes each, there is room.
