# claudication

*claudication*, n. — a limp. In medicine, the kind caused by a narrowing that
starves the muscle under load: it comes on when you push, and eases when you
rest. Also contains Claude. The name is a promise about the failure mode — it
keeps walking, and it tells you where the narrowing is.

**A gateway that puts your Claude subscription — and your ChatGPT one — behind
the APIs your tools already speak.** Point Claude Code, opencode or crush at
its Anthropic Messages API, or Codex CLI at its OpenAI Responses API: the
gateway authenticates with subscription OAuth accounts, pools several of them
in an order you choose, and relays requests byte for byte — translating only
where a client's API is not its upstream's. One static binary, with the admin
UI inside it.

    Claude Code ─┐
    opencode  ───┼──►  /v1/messages  ──┐                   ┌──►  api.anthropic.com
    crush     ───┘                     ├──►  claudication ─┤     your Claude subscription
    Codex     ──────►  /v1/responses ──┘      your API key └──►  chatgpt.com (Codex backend)
                                                                 your ChatGPT subscription

Each tool is a **client** of the gateway: a type, one key, and model names of
its own — `large`, `small`, `review` — each bound on the client's page to a
real model or to the newest of a family. Which upstream serves a request is
decided by the model that name resolves to, served by the accounts that list
it. Every client reaches both: Codex speaks OpenAI Responses and Claude Code,
crush and opencode Anthropic Messages, and the gateway translates either way.

The gateway, each client and each account can be switched off from the admin
UI without a restart.

Each client type's configuration starts from a file in
[`configs/clients/`](configs/clients/):

| [`claude-code.sh`](configs/clients/claude-code.sh) | [`codex.toml`](configs/clients/codex.toml) + [`codex-models.json`](configs/clients/codex-models.json) | [`opencode.jsonc`](configs/clients/opencode.jsonc) | [`crush.json`](configs/clients/crush.json) |
|---|---|---|---|

A client's page in the admin UI writes them with its own address, key and
model names, and makes a one-line command that installs them.
[docs/clients.md](docs/clients.md) explains the parts that bite — chiefly that each client disagrees about whether the base URL carries
`/v1`, and none of them says so when you get it wrong.

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

## The admin UI

Five screens, at the gateway's own address. Each page has a path of its own, so
a link to one opens on it.

**Overview** — whether the gateway can serve a request, and today's traffic.

**Accounts** — the Claude and ChatGPT accounts the pool draws on, each
provider's in its own priority order, each showing its 5-hour and 7-day
subscription usage, which clients drew on it, and a switch to pause it.

**Clients** — the tools that use the gateway: each one's key and setup, its
model names, its usage with its limits beside it, its chats and its requests,
a switch to turn it off, and one to shrink images Claude would refuse.

**Activity** — where the traffic went, by model, by client and by account,
each with its traffic over days above its table; and the requests: the last
hour live, the outcomes over days, and the request log, every client's, with
the upstream's own error text, paged back as far as retention keeps it.

**Settings** — one page: the Gateway switch, the admin password and sessions,
and the configuration as loaded.

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

Each account can also turn **models** off, under Models on its card: the list is
what that account's own subscription serves, read with its token, since plans
differ. A request goes to the first account in the list that has its model on;
with it off on every account, the client is told the gateway does not serve it,
and the model leaves `/v1/models` and the configs the gateway hands out. Models
are on until switched off, so one Anthropic releases tomorrow is available
without anyone switching it on.

An account can be **paused** without deleting it. Deleting revokes its refresh
token upstream, so getting it back means going through the consent flow again.

A login does not last forever: Anthropic gives each one a lifetime, and the card
counts down to it. When it is close, or once the refresh token has been refused,
**Reconnect** on that account's card runs the consent flow for that account and
renews it in place — same place in the list, same history. The sign-in page is
pre-filled with the account's address, as `claude auth login --email` does. The
browser still approves as whichever Claude account it is signed in to, so the
gateway checks whose tokens came back: approving as someone else changes nothing
and the tokens are handed back. Sign out of claude.ai first, or open the link the
form offers in a private window. Adding an account that is already connected
renews it too, and says so.

## ChatGPT accounts

A ChatGPT Plus, Pro or Team subscription can serve Codex. **Add account →
ChatGPT** asks OpenAI for a device code; enter it on OpenAI's own device page,
in any browser signed in to the account, and the page notices and adds it. It
is the flow `codex login --device-auth` uses, and it needs nothing to come back
from that browser, so it works for a gateway on another machine.

Each ChatGPT workspace is an account of its own: a personal one and a Team one
under the same address are added separately, and each request names the one it
bills. The account shows the plan the sign-in named.

What a ChatGPT account serves is what a Responses client sends it, as it was
written — the gateway adds the account's credential and nothing that claims to
be Codex. A client on Anthropic Messages — Claude Code, crush, opencode —
whose slot is bound to a GPT model is served there too, its requests
translated into Responses and the answers back, thinking and tool calls
included ([docs/client-apis.md](docs/client-apis.md)). Usage comes
from the backend's own endpoint and from the headers on every answer, in the
same 5-hour and 7-day columns.

The Codex backend is not a public API, and pooling several people's ChatGPT
subscriptions behind shared keys may breach OpenAI's terms and get accounts
flagged — as with Claude, that is the operator's decision to make knowingly.
What was measured against it, and how, is in
`go doc ./internal/provider/openai`.

### Subscription usage

Each Claude account shows what it has spent, as the same rows `/usage` prints
in the client, because they come from the same place — `GET /api/oauth/usage`
(a ChatGPT account's come from the backend's `/wham/usage`, one row per window):

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

**Test connection** sends a real minimal request upstream, on the account's own
credential and no client's key, and reports the model, token counts and
latency, or the upstream's own error text unmodified, which is the only thing
that distinguishes an expired token from a plan restriction. The account's
Models page tests each model the same way, two at a time for **Test all**,
keeps each one's last result beside its switch, and offers to switch off the
ones that failed for a reason that lasts — never on its own.

Tokens are sealed with AES-256-GCM before they reach the database. The key lives
beside it as `secret.key` (0600), or comes from `CLAUDICATION_SECRET_KEY`.

## Clients, keys and limits

Every client holds one key. A key authenticates by `sha256(key)` and a lookup
prefix, and is also kept sealed with the same key that seals the account
tokens, so the client's page can show it, and write it into the client's
files, until the client is configured: the first time its install command runs
or its key is used. From then on neither the key nor the install command is
offered; regenerating the key starts over. A copy of the state directory
therefore holds working keys as well as working account tokens, which it
already did.

Each client can carry two limits, both off by default, set on its Usage page
beside the figures they bound, with suggestions from its own last 7 or 30 days
and a preview of what a limit would have refused:

- **A request rate** — a count and the period it is measured over, minute, hour
  or day. The whole allowance is available at once and refills over the period,
  so 200 an hour permits a burst of 200 and then runs dry. Off means the
  gateway's own `limits.requests-per-minute`.
- **A token budget** — tokens per rolling 24 hours, counting input, output and
  both cache columns, because all four are billed. Rolling rather than
  calendar-aligned, so it frees up continuously instead of everything becoming
  possible again at midnight. Off means no ceiling.

A client can exceed its budget by one request, necessarily: what a request costs is
only known once it has been served. The refusal carries `Retry-After` computed
from when the oldest counted request ages out.

### Per-request history

One row per proxied request: which client, which of its names, which account,
which model, how many tokens, how long, and what failed. It survives the client
or account it refers to being deleted — otherwise the record you go looking for
after removing a client is precisely the one that disappears.

`usage.retention-days` bounds it (30 by default; 0 records nothing) and a daily
prune enforces it, returning the freed pages to the filesystem.

## Model names

There are no gateway-wide model names. Each client has **slots**, one per model
setting its tool has, each with a name the tool asks for:

| Client | Slots |
|---|---|
| Claude Code | `large` (`ANTHROPIC_MODEL`), `small` (`ANTHROPIC_SMALL_FAST_MODEL`), `opus`, `sonnet`, `haiku`, `fable` (the `/model` tiers), `auto` (auto mode's classifier), and any more the operator adds, for `/model` |
| Codex | `large` (`model`), `review` (`review_model`), `agents` (sub-agents), `small` (memories), and any more the operator adds, for `/model` and `-m` |
| opencode | `large` (`model`), `small` (`small_model`), and any more the operator adds, for its model list |
| crush | `large` (`models.large`), `small` (`models.small`), and any more the operator adds, for its picker |
| Custom | `large`, and any the operator adds — or real model ids, in direct mode |

A name is the client's own, so two clients can both have a `large` on different
models. Each slot is bound to a model — any account's, Claude or GPT — or
follows a target: the newest Opus, Sonnet, Haiku or Fable by release date, or
ChatGPT's first-ranked or light model. Moving a client to a new model is one
change on its page, with no config to edit; the client is answered in its own
name, and the request log records both. A typed client may ask for its slot
names and nothing else, which is why every model it can ask for of its own
accord has one. `go doc ./internal/service/clients` has each type's slots and
the sources they were read from.

What the gateway knows about models comes from [models.dev](https://models.dev),
every provider's: names, release dates, context windows, output limits. Its
model list is that catalogue matched against what the connected accounts list,
less what each account's switches turn off. A model an account lists that the
catalogue does not know yet is listed anyway, after the rest. Until an account
can be asked, the gateway lists the catalogue's Anthropic models as they stand,
for the admin screens; no client is offered any of them until an account serves it.
`models-catalog: off` never asks, and the list is then the accounts' own.

Each client's files also carry what each name's model can do — Claude Code's
`behavesAs`, opencode's declared limits, the crush and Codex model lists —
because a client sizes its requests from those. Rebinding a slot to a model
with a smaller window means running the client's install command again. They
also set what a client would otherwise read off a real model's id — opencode's
tool-call streaming, crush's thinking display — so a plain name behaves as the
model it serves ([docs/clients.md](docs/clients.md), "What a client reads off
a name").

Rewriting the model is the relay's seventh exception to sending the caller's
bytes as they are; see `go doc ./internal/relay/passes`.

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
carries such a name. [docs/status-page.md](docs/status-page.md) has what is
recorded and why each published field is safe.

Two more things before exposing it. Use a **long random admin password**: the
sign-in limiter allows `anon-per-minute` attempts per IP (60 by default) with no
lockout, which is ample against real entropy and not against a memorable
password. And give each client **its own API key with a token budget**, so a
leaked key is bounded and shows up per-key in Usage.

There is no TLS in the binary — `listen` is plain HTTP. Terminate TLS in front,
and bind the gateway somewhere the proxy can reach and the network cannot.

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
`go doc ./internal/relay/passes`.

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
| `GET,POST /admin/clients`                     | admin | Clients, and making one with its key.      |
| `GET /admin/clients/{id}`, `…/usage`          | admin | One client, and what it used.              |
| `PUT,DELETE /admin/clients/{id}/slots/{slot}` | admin | Bind, rename or remove a model name.       |
| `PATCH,DELETE /admin/keys/{id}`               | admin | Edit a client's limits, or delete it.      |
| `POST /admin/gateway`                         | admin | The Gateway switch.                        |
| `GET /admin/usage`                            | admin | Totals and breakdowns over a window.       |
| `GET /admin/requests`                         | admin | The request log, paged.                    |

Model discovery is pinned by the contract: `GET /v1/models?limit=1000`, a
3-second timeout, **any redirect counts as failure**, and Claude Code keeps only
ids containing `claude` or `anthropic` — when it asks at all, which is only
with `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` set. The files the gateway
writes do not set it: they name every model the client may use.

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
measured: [docs/deploying.md](docs/deploying.md).

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
