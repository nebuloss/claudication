# Installing and running it

Installing, the first run, configuration, putting it behind a proxy,
updating it and backing it up. How an update hands over without dropping a
connection is [deploying.md](deploying.md).

## Install

As root, on Alpine or Debian/Ubuntu. The same command installs and updates:

    curl -fsSL https://raw.githubusercontent.com/nebuloss/claudication/main/scripts/install.sh | sh

It fetches the binary for the machine's architecture, verifies it against the
release checksums, installs a service (systemd or OpenRC), starts it, and waits
until it answers on `/health` — reporting the last log lines rather than a
timeout if it does not.

On a first install it also **claims the gateway**: a fresh one has no admin
account and the first person to reach the port takes it, which on a headless box
is a window nobody is watching. The script closes it by setting a random
password and printing it once.

Re-running is an update. The state directory, the admin account and the
connected Claude accounts are left alone.

    VERSION=v0.7.0         pin a release instead of taking the newest
    LISTEN=…               what the service binds (default 0.0.0.0:8317)
    STATE_DIR=…            where credentials live (default /var/lib/claudication)
    TRUSTED_PROXIES=…      set this when something fronts it — see below
    SET_PASSWORD=0         leave the gateway unclaimed
    SERVICE_NAME=…         run more than one on a host

Builds are published for linux, darwin and freebsd on amd64/arm64, plus arm and
riscv64 on linux. Windows binaries are on the [releases
page](https://github.com/nebuloss/claudication/releases); the install script is
POSIX sh and does not cover them.

## Run

    claudication passwd                 # set the admin password
    claudication clients add -name laptop  # a client and its key
    claudication serve

Then open **http://127.0.0.1:8317/** and connect a Claude account. A client made
from the shell is a custom one naming real models; point a tool at it with the
base URL and its key:

    export ANTHROPIC_BASE_URL=http://your-gateway:8317
    export ANTHROPIC_AUTH_TOKEN=clc_…
    claude

`claudication login-url` mints a single-use link that signs a browser in without
typing the password — the convenient path behind an SSH tunnel. It is spent the
first time it is used, so a link left in shell history is a spent link.

Other subcommands: `clients list|delete`, `backup`, `restore`, `vacuum`, `version`.

## Configuration

Optional; see [`configs/config.example.yaml`](../configs/config.example.yaml). With
no `-config`, `/etc/claudication/config.yaml` is read if it exists. The file is
read-only to claudication and never rewritten, so comments survive. Credentials
live in the state database, not in config.

    CLAUDICATION_LISTEN, CLAUDICATION_STATE_DIR, CLAUDICATION_LOG_LEVEL,
    CLAUDICATION_LOG_FORMAT, CLAUDICATION_REQUESTS_PER_MINUTE,
    CLAUDICATION_SECRET_KEY, CLAUDICATION_CLAUDE_CODE_ATTRIBUTION,
    CLAUDICATION_TRUSTED_PROXIES

Environment overrides win over the file.

### Behind a reverse proxy

**Set `trusted-proxies`** if anything fronts the gateway — nginx, Nginx Proxy
Manager, Traefik, a tunnel. Every request then arrives from the proxy, so without
it every client shares one anonymous rate-limit bucket, the access log cannot
tell them apart, and the session cookie cannot be marked `Secure`. The value is
the address the *proxy* connects from; take it from the `ip` field in the log.

`X-Forwarded-For` is honoured only from a trusted peer, so a client cannot spoof
its way past a limit.

### Exposing it

The admin API can add and remove Claude accounts and mint API keys, and by
default it shares a listener with `/v1` — so publishing the relay publishes the
admin surface too. **Set `admin-listen`** and it does not:

```yaml
listen: "0.0.0.0:8317"       # the relay, published
admin-listen: "127.0.0.1:8318"   # the UI, reachable over an SSH tunnel only
```

The relay listener then answers 404 to every `/admin/…` path and to the UI, and
a `?token=` sign-in link spends nothing there. That separation is the gateway's
own, so it holds even if the proxy in front is misconfigured — which is the
failure worth designing for, because it is silent. Its root serves a small
status page instead: that this is a private gateway, what to do if you were or
were not given access, and whether it is up and has been over 90 days, read
from `/status.json`. It names no provider, model, client, API or path, links
nowhere, and does not print the build; `make check` fails if its bundle ever
carries such a name. [status-page.md](status-page.md) has what is
recorded and why each published field is safe.

Two more things before exposing it. Use a **long random admin password**: the
sign-in limiter allows `anon-per-minute` attempts per IP (60 by default) with no
lockout, which is ample against real entropy and not against a memorable
password. And give each client **its own API key with a token budget**, so a
leaked key is bounded and shows up per-key in Usage.

There is no TLS in the binary — `listen` is plain HTTP. Terminate TLS in front,
and bind the gateway somewhere the proxy can reach and the network cannot.

## Deploy

`deploy/claudication.service` for systemd — the unit `scripts/install.sh`
writes, with the default paths — with the hardening a single-purpose daemon
should have: its own user, `ProtectSystem=strict`, a `SystemCallFilter`, and a
`TimeoutStopSec` longer than the drain grace so a stop does not sever a stream
mid-frame. A memory ceiling is `memory-limit` in the config, or the cgroup's,
which the gateway reads itself. The OpenRC path is supervised and registers logrotate.

An update refuses no connection. `serve` runs as a small supervisor holding
the sockets and a worker serving them; `reload` (`rc-service claudication
reload`, `systemctl reload claudication`) starts a worker from the binary on
disk beside the running one, which lets go only once the new one answers and
then finishes its open streams. `scripts/install.sh` updates that way whenever
the running gateway can, and otherwise stops and starts it as before. How, why
that shape for both OpenRC and systemd, what falls back to a stop, and what was
measured: [deploying.md](deploying.md).

The state directory has to be writable and claudication refuses to start if it is
not, which turns "the tokens were on a layer that got thrown away" into a startup
error rather than silent data loss.

**There is no TLS.** Terminate it in front — a reverse proxy or a tunnel — or the
API keys cross the network in clear.

No container image: the binary is static and installs as one file, so building
and signing an image was overhead with nothing on the other side of it.

### Backing it up

    claudication backup -out claudication-backup.tar.gz
    claudication restore -in claudication-backup.tar.gz

Two files matter: `claudication.db`, and the `secret.key` that seals every stored
OAuth token. The database alone restores to a list of accounts whose credentials
cannot be decrypted, so both travel together or the backup is decorative.

**Do not use `cp`.** The database runs in WAL mode, so committed data lives partly
in `claudication.db` and partly in the `-wal` beside it: copying one, or both
without synchronisation, produces a file that is torn or silently missing the
account you connected a minute ago. `backup` takes a consistent snapshot with
`VACUUM INTO` while the gateway keeps serving.

The archive holds the sealing key and the sealed tokens together, which makes it
**exactly as sensitive as the state directory**. It is created 0600; keep it
somewhere you would keep a password.

`restore` refuses to overwrite an existing database unless given `-force`. Stop
the gateway before restoring.

## Building from source

Go 1.26+ and Node for the UI. The binary is static (`CGO_ENABLED=0`) because
`modernc.org/sqlite` is pure Go.

    make web      # vite -> internal/httpapi/webdist, embedded into the binary
    make check    # gofmt, go vet, go test -race, tsc, and the UI build
    make build    # runs `make web` first -> dist/claudication
    make help     # every target, with a line each

The admin UI is React and Tailwind, built by Vite and typechecked separately by
`tsc` — Vite strips types without checking them, so the check has to be its own
step.
