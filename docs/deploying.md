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
     `/health` reports the new version on every port *from another
     process*: each worker answers a random `instance`, and the handover is
     done when it changes. The version alone cannot say so when the same
     release is installed again. Then claim the gateway
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
every `/health` request, on the relay and the admin port; four keep-alive
clients sending POSTs on one connection each, reconnecting only when told
`Connection: close`, with no retry; and one request held open for 22 seconds
on the admin port (its body sent a byte at a time) across the update.

Under OpenRC 0.63.2, in an Alpine 3.24 user namespace, with the real
`install.sh` driving `rc-service` and `supervise-daemon`:

| update | new connections | refused | pooled POSTs failed | long request |
|---|---|---|---|---|
| the release before this one → this one (stop and start) | 68,165 | 10,165, over 19.1 s | 1,248 of 1,608 | completed |
| this one → this one (handover) | 281,893 | **0** | **0** of 1,753 | completed on the old worker, 19 s after the handover |
| this one → the same version again (handover) | 282,798 | **0** | **0** of 1,753 | completed |
| this one → a binary that cannot serve (handover, rolled back; earlier run) | 777,162 | **0** | — | completed; nothing was stopped |

In the handovers, four of the pooled connections were answered with
`Connection: close` by the old worker and reopened to the new one; none was
closed under a request. The uptime record: an outage of 19.1 s for the stop
and start, the length of the refusals, and none for either handover. The
same-version run is the one the version string could not tell apart; the
installer waited for the new worker's instance.

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

## Connections on the old worker

Nothing a client could be writing on is closed by a handover. Every
connection the old worker accepted is answered. An idle keep-alive connection
stays open: its next request is answered, with `Connection: close`, and the
client opens its next connection to the new worker. The old worker exits once
every connection is closed — by that, by the client, by the 120 s idle
timeout — or at `shutdown.grace`, whichever is first. Closing idle
connections at once, as `http.Server.Shutdown` does, failed a POST written on
a pooled connection at that instant: in TestPooledPOSTsSurviveAHandover, 1–2 of
~2,600 pooled POSTs in two runs out of three, none since (`handover.Conns`).

A stop still closes idle connections at once: the gateway is going away.

## While two workers overlap

For as long as the old worker drains — at most `shutdown.grace` — some
limits are counted per worker, and this is accepted rather than shared:

- **Concurrent requests per key** are counted in memory. The new worker
  starts at zero, so a key with streams open on the old worker can open as
  many again on the new one. The excess is bounded by what was in flight at
  the release, it lasts only while those streams finish, and it can only
  ever be the streams of one deploy. Sharing the count would mean the new
  worker taking a snapshot over the pipe that is stale the moment a stream
  on the old one ends: a second source of truth to keep right, for an
  overshoot that ends by itself within the grace.
- **Token budgets** are read from the database, where both workers record
  what they spend, and each re-reads within 30 s: the drift is the same 30 s
  a single worker already has.
- **Per-minute rate limits** start empty in the new worker, as after a
  restart.
- **Leases per account** are counted per worker too, so an account's in-flight
  figure on the admin page is the new worker's.

Listen addresses are the supervisor's, bound once. A reload whose config names
different ones is refused — logged, the old worker carries on — and needs a
restart.

A cgroup memory limit covers both workers for the length of the drain; at
tens of megabytes each, there is room.
