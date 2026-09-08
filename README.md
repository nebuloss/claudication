# claudication

*claudication*, n. — a limp. In medicine, the kind caused by a narrowing that
starves the muscle under load: it comes on when you push, and eases when you
rest. Also contains Claude. The name is a promise about the failure mode — it
keeps walking, and it tells you where the narrowing is.

A multi-provider LLM gateway. One binary that speaks the Anthropic Messages,
OpenAI Chat Completions and OpenAI Responses dialects to clients, and routes
them to either a subscription OAuth backend or a keyed OpenAI-compatible
endpoint — with a TypeScript admin UI on top.

Status: **S1b — Lane A passthrough.** Accounts can be authorised, tested and
proxied against; the translation lane is next.

## Design: two lanes

Anthropic publishes a [gateway compatibility
contract](https://code.claude.com/docs/en/llm-gateway-protocol) describing what
Claude Code sends a proxy and what a proxy must forward untouched. It rules out
the obvious design, so claudication has two request paths:

**Lane A — passthrough.** Same wire format in and out. Resolve the account,
swap the credential, relay bytes. Headers and body forwarded unchanged, error
bodies forwarded verbatim, SSE pings and unknown events included. It never
parses what it does not need to, which is what keeps it forward-compatible
with capabilities that do not exist yet.

The contract is explicit: *"pass `anthropic-*` request headers and request body
fields through unchanged rather than allowlisting the ones you see today. A
gateway pinned to an observed list strips the next capability's header or field
and breaks it on the release that introduces it."*

**Lane B — translation.** Cross-format only: an OpenAI client on a Claude
backend, Claude Code on a keyed OpenAI-compatible model, Chat ↔ Responses. A
canonical request and event model collapses N×M translators into N+M codecs.
Knowingly lossy and version-pinned; this is where capability drift shows up.

Splitting them means the path you use daily does not inherit the translation
layer's blast radius.

## Three rules Lane A must not break

1. **Forward error bodies unmodified.** Claude Code decides whether to retry
   and permanently disable a capability by substring-matching the upstream's
   error prose. Rewrap an error and graceful degradation stops working. A
   gateway that must rewrite an error can emit `capability_rejected: <cap>` in
   the message and recovery still fires.
2. **Relay every byte, including pings.** A byte-level watchdog aborts a stream
   silent for 300 seconds, and during long thinking pauses SSE `ping` events
   are the only traffic. Buffering a complete response before relaying stalls
   the client outright.
3. **Do not reshape the `system` array.** The attribution block Claude Code
   prepends is stripped by `api.anthropic.com` only when it arrives unchanged
   as the first block. Reorder it and it lands in the model's prompt and the
   cache key.

## Install

As root, on Alpine or Debian/Ubuntu. The same command installs and updates:

    curl -fsSL https://raw.githubusercontent.com/nebuloss/claudication/main/scripts/install.sh | sh

It fetches the binary for the machine's architecture, verifies it against the
release checksums, installs a service (systemd or OpenRC), starts it, and waits
until it answers on `/health` — reporting the last log lines rather than a
timeout if it does not.

On a first install it also **claims the gateway**: a fresh one has no admin
account and the first person to reach the port takes it, which on a headless
box is a window nobody is watching. The script closes it by setting a random
password and printing it once. `SET_PASSWORD=0` opts out.

Re-running is an update. It stops the service, replaces the binary, rewrites
the unit and starts it again; the state directory, the admin account and the
connected Claude accounts are left alone.

    VERSION=v0.2.0   pin a release instead of taking the newest
    LISTEN=…         what the service binds (default 0.0.0.0:8317)
    STATE_DIR=…      where credentials live (default /var/lib/claudication)
    SERVICE_NAME=…   run more than one on a host

Everything is one static binary with the UI inside it. Builds are published for
linux, darwin and freebsd on amd64/arm64, plus arm and riscv64 on linux;
Windows binaries are on the [releases
page](https://github.com/nebuloss/claudication/releases) but the install script
is POSIX sh and does not cover them.

## Build

Go 1.26+ (`golang.org/x/crypto` requires it) and Node for the UI. The binary is
static (`CGO_ENABLED=0`) because `modernc.org/sqlite` is pure Go, so it runs in
a scratch image with no runtime.

    make web      # vite -> internal/httpapi/webdist, embedded into the binary
    make check    # gofmt, go vet, go test -race, and tsc
    make build    # runs `make web` first -> dist/claudication
    make help     # every target, with a line each

The admin UI is React and Tailwind, built by Vite and typechecked separately by
`tsc` — Vite strips types without checking them, so the check has to be its own
step.

## Run

    claudication passwd                 # set the admin password
    claudication keys add -name laptop  # a client key, for inference
    claudication serve -config config.yaml

Then open **http://127.0.0.1:8317/**. On a gateway nobody has set up yet the
UI asks for a password instead of a sign-in — whoever answers claims it, so do
it before the port is reachable by anyone else.

`claudication login-url` mints a single-use link that signs a browser in
without typing the password, which is the convenient path when the gateway is
behind an SSH tunnel. It is spent the first time it is used, so a link left in
shell history is a spent link.

### The admin UI

Five tabs, at the gateway's own address:

**Overview** — whether the proxy can serve a request, and the base URL plus
snippets to point Claude Code or curl at it. **Claude accounts** — the OAuth
accounts the pool draws on, in priority order, each showing its 5-hour and
7-day subscription usage. **API keys** — mint and withdraw the
credentials clients present, with the traffic each one accounted for.
**Usage** — totals, breakdowns by day, model, key and account, and the last
fifty requests with the upstream's own error text. **Settings** — the admin
password, and what this instance is.

Each tab lives in the URL fragment, so a link to one opens on it.

### Per-request history

The gateway records one row per proxied request: which key, which account,
which model, how many tokens, how long, and what failed. It is what the Usage
tab reads, and it survives the key or account it refers to being deleted —
otherwise the record you go looking for after removing a key is precisely the
one that disappears.

`usage.retention-days` bounds it (30 by default; 0 records nothing at all) and
a daily prune enforces that.

### The admin account

One password, no username: this is one operator's own gateway, not a
multi-tenant service. Changing the password ends every other session, which is
the point of changing it, and deleting the account returns the gateway to its
first-run state without touching the upstream accounts or API keys it holds.

`claudication passwd` is the recovery path, and does not ask for the old
password — whoever can run it can already read the database it protects. It
reads without echo from a terminal and plainly from a pipe, so provisioning can
do `claudication passwd < secret`.

## Authorising a Claude account

The admin UI drives the OAuth flow. "Start Claude login" opens Anthropic's
consent screen in a new tab; approve it, and the page shows a code to copy back
into the form. That is the same paste flow `claude auth login` uses, and the
reason there is no callback listener to run: the redirect belongs to Claude
Code's own OAuth client, which is almost never where the gateway runs.

Add as many as you have subscriptions. They form a **priority list**: drag them,
or use the arrows, and the gateway serves from the top, falling through to the
next when Anthropic says the one above is out of room.

That order is deliberately the operator's rather than something inferred. A
personal subscription and a work one are not interchangeable, and "spread the
load evenly" is the wrong answer when one of them is the account you would
rather not spend.

### Subscription usage

Each account shows what it has spent, as the same rows `/usage` prints in the
client — because they come from the same place. The client's `fetchUtilization`
calls `GET /api/oauth/usage`, so the gateway does too:

    Current session          29%   resets in 2h
    Current week (all models) 45%  resets in 21h  · binding
    Current week (Fable)       0%

The gateway polls that endpoint every five minutes and on demand, rather than
reading the rate-limit headers off relayed responses. Headers were the first
attempt and were wrong in a way that mattered: an account showed nothing at all
until it had served a request, and a gateway you have just connected an account
to has served none. They also carry only the two headline windows, missing the
per-model weekly limits.

These are the subscription's own figures, so they count every client on the
account, not only traffic through this gateway — the Usage tab's per-account
breakdown is the other number.

Quota never reorders the priority list; it only decides whether to skip. And if
every account looks exhausted the gateway tries anyway: the figures are a poll,
and being refused by the upstream beats refusing on its behalf.

Each account then has a **Test** button that sends a real minimal request
upstream and reports the model, token counts and latency — or the upstream's
own error text, unmodified, which is the only thing that distinguishes an
expired token from a plan restriction.

Tokens are sealed with AES-256-GCM before they reach the database. The key
lives beside it as `secret.key` (0600), or comes from `CLAUDICATION_SECRET_KEY` for
deployments that inject secrets from a vault.

Configuration is optional; see `config.example.yaml`. The file is **read-only**
to claudication and is never rewritten, so comments survive. Credentials live in the
state database, not in config.

    CLAUDICATION_LISTEN, CLAUDICATION_STATE_DIR, CLAUDICATION_LOG_LEVEL, CLAUDICATION_LOG_FORMAT,
    CLAUDICATION_REQUESTS_PER_MINUTE, CLAUDICATION_SECRET_KEY,
    CLAUDICATION_CLAUDE_CODE_ATTRIBUTION, CLAUDICATION_TRUSTED_PROXIES

`CLAUDICATION_CLAUDE_CODE_ATTRIBUTION` (`passthrough.claude-code-attribution`,
default **on**) is the one setting that changes what is sent upstream. Anthropic's
subscription backend gates opus, sonnet and fable on Claude Code's attribution
block arriving as the *first* system block, and refuses anything else as a
`429 rate_limit_error` with the message `"Error"` and no rate-limit headers —
which reads exactly like quota exhaustion and is nothing of the sort. With this
on, a client that did not send that block has it prepended, so it reaches the
models the subscription pays for. Set it to `false` for strict byte-for-byte
passthrough, and accept that non-Claude-Code clients then get haiku and nothing
above it. A body that already leads with an accepted block is never rewritten.

## Endpoints

| Endpoint            | Auth | Notes                                          |
| ------------------- | ---- | ---------------------------------------------- |
| `GET /health`       | no   | Liveness. Leaks no operational detail.         |
| `HEAD /api/hello`   | no   | Claude Code's connection-warming probe.        |
| `GET /v1/models`    | yes  | Discovery, proxied from the upstream.          |
| `POST /v1/messages` | yes  | Lane A passthrough, streaming and not.         |
| `GET /`             | —    | Admin UI. `?token=` spends a sign-in link.     |
| `GET /admin/setup`  | —    | Fresh install, or just a lapsed session?       |
| `POST /admin/setup` | —    | Claim an unclaimed gateway. Refused after.     |
| `POST /admin/session` | —  | Password for a session cookie.                 |
| `POST /admin/password` | admin | Change it; ends every other session.      |
| `POST /admin/account/delete` | admin | Back to the first-run state.        |
| `GET /admin/overview` | admin | Status, counts, last 24h.                   |
| `GET /admin/accounts` | admin | Upstream accounts.                          |
| `POST /admin/accounts/order` | admin | Set the priority list.               |
| `POST /admin/accounts/oauth/{start,complete}` | admin | The login flow. |
| `POST /admin/accounts/{id}/{test,refresh}` | admin | Probe or refresh.  |
| `POST /admin/accounts/{id}/usage` | admin | Re-read the subscription usage. |
| `GET,POST /admin/keys` | admin | Client API keys.                           |
| `GET /admin/usage` | admin | Totals and breakdowns over a window.          |
| `GET /admin/requests` | admin | The last N proxied requests.                |

Model discovery is pinned by the contract: `GET /v1/models?limit=1000`, a
3-second timeout, **any redirect counts as failure**, and Claude Code keeps
only ids containing `claude` or `anthropic`.

## Deploy

`deploy/claudication.service` for systemd, with the hardening a single-purpose
daemon should have: its own user, `ProtectSystem=strict`, a `SystemCallFilter`,
and a `TimeoutStopSec` longer than the drain grace so a stop does not sever a
stream mid-frame.

The state directory has to be writable and claudication refuses to start if it
is not, which turns "the tokens were on a layer that got thrown away" into a
startup error rather than silent data loss.

### Backing it up

    claudication backup -out claudication-backup.tar.gz
    claudication restore -in claudication-backup.tar.gz

Two files matter: `claudication.db`, and the `secret.key` that seals every
stored OAuth token. The database on its own restores to a list of accounts whose
credentials cannot be decrypted, so both travel together or the backup is
decorative.

Do not use `cp` for this. The database runs in WAL mode, so at any moment
committed data lives partly in `claudication.db` and partly in the `-wal` file
beside it: copying one, or both without synchronisation, can produce a file that
is torn or silently missing the account you connected a minute ago. `backup`
takes a consistent snapshot with `VACUUM INTO` while the gateway keeps serving,
so there is no window to schedule around.

The archive holds the sealing key and the sealed tokens together, which makes it
**exactly as sensitive as the state directory** — anyone holding it holds every
connected Claude account. It is created 0600; keep it somewhere you would keep a
password.

`restore` refuses to overwrite an existing database unless given `-force`, and
removes any stale `-wal` left behind so SQLite cannot replay an old log over the
restored file. Stop the gateway before restoring.

No container image is published. The binary is static and has no runtime
dependencies, so a `FROM scratch` image is three lines if you want one — but
shipping and signing one for a tool that installs as a single file was
overhead with nothing on the other side of it.

## Roadmap

| Stage  | Scope                                                            |
| ------ | ---------------------------------------------------------------- |
| **S0** | Skeleton: config, SQLite, key auth, rate limit, graceful shutdown |
| **S1a** | Claude OAuth, sealed credentials, admin API + UI, account probe  |
| **S1b** | Lane A passthrough, account pool, `/v1/models`, `count_tokens`   |
| S2     | Contract conformance tests                                       |
| S3     | Lane B canonical model and the three codecs                      |
| S4     | Keyed backends (OpenAI-compatible, Anthropic API)                 |
| S5     | Codex OAuth                                                      |
| S6     | Admin REST + TypeScript UI, embedded                              |
| S7     | Container, CI                                                    |

## Renaming

The project was called `claudiquement` before its first release. Nothing was
published under that name, so there is no compatibility shim — see
[docs/renaming-from-claudiquement.md](docs/renaming-from-claudiquement.md) for
what moved and how to bring an existing state directory across.

## Prior art

[auth2api](https://github.com/AmazingAng/auth2api) (TypeScript) and
[claude-code-proxy](https://github.com/fuergaosi233/claude-code-proxy) (Python)
solve the two halves separately. claudication is the union, rebuilt against the
official contract.
