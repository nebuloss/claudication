# claudication

*claudication*, n. — a limp. In medicine, the kind caused by a narrowing that
starves the muscle under load: it comes on when you push, and eases when you
rest. Also contains Claude. The name is a promise about the failure mode — it
keeps walking, and it tells you where the narrowing is.

**A gateway that puts your Claude subscription behind the Anthropic Messages
API.** Point Claude Code, or anything else that speaks that API, at it: the
gateway authenticates to Anthropic with a subscription OAuth account, pools
several of them in an order you choose, and relays requests byte for byte. One
static binary, with the admin UI inside it.

    Claude Code  ──►  claudication  ──►  api.anthropic.com
                       your API key       your subscription

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
    claudication keys add -name laptop  # a client key, for inference
    claudication serve

Then open **http://127.0.0.1:8317/** and connect a Claude account. Point a client
at it with the base URL and one of your keys:

    export ANTHROPIC_BASE_URL=http://your-gateway:8317
    export ANTHROPIC_AUTH_TOKEN=clc_…
    claude

`claudication login-url` mints a single-use link that signs a browser in without
typing the password — the convenient path behind an SSH tunnel. It is spent the
first time it is used, so a link left in shell history is a spent link.

Other subcommands: `keys list|delete`, `backup`, `restore`, `vacuum`, `version`.

## The admin UI

Five tabs, at the gateway's own address. Each lives in the URL fragment, so a
link to one opens on it.

**Overview** — whether the gateway can serve a request, and the base URL plus
snippets to point Claude Code or curl at it.

**Claude accounts** — the OAuth accounts the pool draws on, in priority order,
each showing its 5-hour and 7-day subscription usage.

**API keys** — mint, rename and withdraw the credentials clients present, each
with an optional request rate limit and daily token budget.

**Usage** — totals, breakdowns by day, model, key and account, and the request
log with the upstream's own error text, paged back as far as retention keeps it.

**Settings** — the admin password, and what this instance is.

## Claude accounts

The UI drives the OAuth flow. "Start Claude login" opens Anthropic's consent
screen in a new tab; approve it, and the page shows a code to copy back into the
form. That is the same paste flow `claude auth login` uses, and the reason there
is no callback listener to run: the redirect belongs to Claude Code's own OAuth
client, which is almost never where the gateway runs.

Add as many as you have subscriptions. They form a **priority list**: the gateway
serves from the top and falls through when Anthropic says the one above is out of
room. That order is the operator's rather than something inferred — a personal
subscription and a work one are not interchangeable, and "spread the load evenly"
is the wrong answer when one of them is the account you would rather not spend.

An account can be **paused** without deleting it. Deleting revokes its refresh
token upstream, so getting it back means going through the consent flow again.

### Subscription usage

Each account shows what it has spent, as the same rows `/usage` prints in the
client, because they come from the same place — `GET /api/oauth/usage`:

    Current session           29%  resets in 2h
    Current week (all models) 45%  resets in 21h  · binding
    Current week (Fable)       0%

Polled every five minutes and on demand, rather than read off relayed responses.
Headers were the first attempt and were wrong in a way that mattered: an account
showed nothing until it had served a request, and they carry only the two
headline windows, missing the per-model weekly limits.

These are the subscription's own figures, so they count every client on the
account, not only traffic through this gateway.

Quota never reorders the priority list; it only decides whether to skip. If every
account looks exhausted the gateway tries anyway — the figures are a poll, and
being refused by the upstream beats refusing on its behalf.

**Test** sends a real minimal request upstream and reports the model, token
counts and latency, or the upstream's own error text unmodified, which is the
only thing that distinguishes an expired token from a plan restriction.

Tokens are sealed with AES-256-GCM before they reach the database. The key lives
beside it as `secret.key` (0600), or comes from `CLAUDICATION_SECRET_KEY`.

## API keys and limits

A key is shown once at creation and is unrecoverable afterwards: the store keeps
`sha256(key)` and a lookup prefix, never the key.

Each key can carry two limits, both off by default:

- **A request rate** — a count and the period it is measured over, minute, hour
  or day. The whole allowance is available at once and refills over the period,
  so 200 an hour permits a burst of 200 and then runs dry. Off means the
  gateway's own `limits.requests-per-minute`.
- **A token budget** — tokens per rolling 24 hours, counting input, output and
  both cache columns, because all four are billed. Rolling rather than
  calendar-aligned, so it frees up continuously instead of everything becoming
  possible again at midnight. Off means no ceiling.

A key can exceed its budget by one request, necessarily: what a request costs is
only known once it has been served. The refusal carries `Retry-After` computed
from when the oldest counted request ages out.

### Per-request history

One row per proxied request: which key, which account, which model, how many
tokens, how long, and what failed. It survives the key or account it refers to
being deleted — otherwise the record you go looking for after removing a key is
precisely the one that disappears.

`usage.retention-days` bounds it (30 by default; 0 records nothing) and a daily
prune enforces it, returning the freed pages to the filesystem.

## Configuration

Optional; see [`configs/config.example.yaml`](configs/config.example.yaml). With
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

### The attribution block

`CLAUDICATION_CLAUDE_CODE_ATTRIBUTION` (`passthrough.claude-code-attribution`,
default **on**) is the one setting that changes what is sent upstream.

Anthropic's subscription backend gates opus, sonnet and fable on Claude Code's
attribution block arriving as the *first* system block, and refuses anything else
as a `429 rate_limit_error` with the message `"Error"` and no rate-limit
headers — which reads exactly like quota exhaustion and is nothing of the sort.
With this on, a client that did not send that block has it prepended, so it
reaches the models the subscription pays for. A body that already leads with an
accepted block is never rewritten.

Set it `false` for strict byte-for-byte passthrough, and accept that
non-Claude-Code clients then get haiku and nothing above it.

### Other requests the backend refuses on content

Attribution is not the only one. A tool named `mcp_x` rather than `mcp__x`, and
Claude Code's own `Is directory a git repo:` line inside a system prompt that
is not Claude Code's, are both refused — with a message about billing that
mentions neither tool names nor prompts:

> Third-party apps now draw from your extra usage, not your plan limits.

It is not a quota problem, and adding credit does not fix it. The relay
rewrites both on the way out, restores the tool names on the way back, and
labels the refusal in the request log if a third trigger ever turns up.

Unpaired UTF-16 surrogates (a tool cutting its output mid-emoji), empty text
blocks, and gzipped request bodies are handled in the same place and for the
same reason: without it those requests cannot succeed at all.

[`docs/refused-requests.md`](docs/refused-requests.md) has the measurements
behind each of these and the signals this gateway deliberately does not
synthesise; [`docs/`](docs/) also covers what the official client sends and how
any of it can be checked again. The short version is in the code, as
`go doc ./internal/upstream`.

If a request starts failing for no visible reason, do not reason about it —
none of the three known triggers was found that way. Point
[`scripts/bisect-refusal.py`](scripts/bisect-refusal.py) at a captured body and
a key, and it narrows to the smallest failing part in about a dozen requests:

```sh
scripts/bisect-refusal.py --key clc_... --url http://127.0.0.1:8317 body.json
```

## What the relay must not break

Anthropic publishes a [gateway compatibility
contract](https://code.claude.com/docs/en/llm-gateway-protocol) describing what
Claude Code sends a proxy and what a proxy must forward untouched. Three rules
matter most, and each is held by a test that quotes the clause it enforces:

1. **Forward error bodies unmodified.** Claude Code decides whether to retry, and
   whether to permanently disable a capability, by substring-matching the
   upstream's error prose. Rewrap an error and graceful degradation stops
   working.
2. **Relay every byte, including pings.** A byte-level watchdog aborts a stream
   silent for 300 seconds, and during long thinking pauses SSE `ping` events are
   the only traffic. Buffering a complete response before relaying stalls the
   client outright.
3. **Do not reshape the `system` array.** The attribution strip is positional:
   reorder the array, merge the block, or flatten it to a string and the block
   lands in the model's prompt and the cache key.

The relay follows from those: it never parses what it does not need to, which is
what keeps it working with capabilities that do not exist yet. Headers and body
fields are forwarded as open lists rather than allowlists, because a gateway
pinned to an observed list strips the next capability's field and breaks it on
the release that introduces it.

## Endpoints

| Endpoint                                      | Auth  | Notes                                      |
| --------------------------------------------- | ----- | ------------------------------------------ |
| `GET /health`                                 | no    | Liveness. Leaks no operational detail.     |
| `HEAD /api/hello`                             | no    | Claude Code's connection-warming probe.    |
| `GET /v1/models`                              | key   | Discovery, proxied from the upstream.      |
| `POST /v1/messages`                           | key   | The relay, streaming and not.              |
| `POST /v1/messages/count_tokens`              | key   | So counting does not spend inference.      |
| `GET /`                                       | —     | Admin UI. `?token=` spends a sign-in link. |
| `GET,POST /admin/setup`                       | —     | Fresh install, and claiming it.            |
| `POST /admin/session`                         | —     | Password for a session cookie.             |
| `POST /admin/password`                        | admin | Change it; ends every other session.       |
| `GET /admin/overview`                         | admin | Status, counts, last 24h.                  |
| `GET /admin/accounts`                         | admin | Upstream accounts.                         |
| `POST /admin/accounts/order`                  | admin | Set the priority list.                     |
| `POST /admin/accounts/oauth/{start,complete}` | admin | The login flow.                            |
| `POST /admin/accounts/{id}/{test,refresh}`    | admin | Probe or refresh.                          |
| `POST /admin/accounts/{id}/disabled`          | admin | Pause or resume.                           |
| `GET,POST /admin/keys`                        | admin | Client API keys.                           |
| `PATCH,DELETE /admin/keys/{id}`               | admin | Edit or withdraw one.                      |
| `GET /admin/usage`                            | admin | Totals and breakdowns over a window.       |
| `GET /admin/requests`                         | admin | The request log, paged.                    |

Model discovery is pinned by the contract: `GET /v1/models?limit=1000`, a
3-second timeout, **any redirect counts as failure**, and Claude Code keeps only
ids containing `claude` or `anthropic`.

## Deploy

`deploy/claudication.service` for systemd, with the hardening a single-purpose
daemon should have: its own user, `ProtectSystem=strict`, a `SystemCallFilter`,
`GOMEMLIMIT` so a burst degrades into GC pressure rather than an OOM kill, and a
`TimeoutStopSec` longer than the drain grace so a stop does not sever a stream
mid-frame. The OpenRC path is supervised and registers logrotate.

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

## Build

Go 1.26+ and Node for the UI. The binary is static (`CGO_ENABLED=0`) because
`modernc.org/sqlite` is pure Go.

    make web      # vite -> internal/httpapi/webdist, embedded into the binary
    make check    # gofmt, go vet, go test -race, tsc, and the UI build
    make build    # runs `make web` first -> dist/claudication
    make help     # every target, with a line each

The admin UI is React and Tailwind, built by Vite and typechecked separately by
`tsc` — Vite strips types without checking them, so the check has to be its own
step.

## Prior art

[auth2api](https://github.com/AmazingAng/auth2api) (TypeScript) and
[claude-code-proxy](https://github.com/fuergaosi233/claude-code-proxy) (Python)
solve the two halves separately. claudication is the union, rebuilt against the
official contract.
