# Pointing a client at the gateway

A **client** is one tool on one machine, as the gateway knows it: a type, one
API key, its own model names, its limits and its switches. Make one on the
**Clients** screen of the admin UI; its page then has everything that tool
needs — the key, the address, its files and a one-line install command.

| Type | What the gateway writes | API it speaks | `/v1` on the base URL? |
|---|---|---|---|
| Claude Code | [`claude-code.sh`](../configs/clients/claude-code.sh) | Anthropic Messages | no |
| Codex CLI | [`codex.toml`](../configs/clients/codex.toml) + [`codex-models.json`](../configs/clients/codex-models.json) | OpenAI Responses | yes |
| opencode | [`opencode.jsonc`](../configs/clients/opencode.jsonc) | Anthropic Messages | yes |
| crush | [`crush.json`](../configs/clients/crush.json) | Anthropic Messages | no |
| Custom | nothing: the key, both addresses, its names and a snippet per SDK | either | — |

Each type speaks its own API, fixed by the type: there is no choice to make,
because both APIs reach every model (see [client-apis.md](client-apis.md)).
The files in `configs/clients/` are each type's starting point, complete and
commented, with the names a new client of that type is given. A client's page
writes them with its own names and its own key. Each type's table — its model
fields, its default names, its API — is `go doc ./internal/service/clients`,
with the source each was read from.

The last column is what trips everyone up: each tool has its own opinion about
whether the base URL carries `/v1`, and none says so when you get it wrong. You
just get 404s. The client's page gives the right one.

## The key

Keys look like `clc_…`, one per client. A client's page shows its key until the
client is **configured** — the first time its install command is run or its key
is used, refused or not. From then on the page says when, and neither the key
nor the install command is offered again: a key that is in use has nowhere it
still needs to go. To set the client up again, regenerate its key on its page;
the old one stops working at once.

From a shell, for a provisioning script:

```sh
claudication clients add -name laptop               # a custom client, real model ids
claudication clients add -name laptop -type crush   # a crush client, with crush's names
```

**One key format, both APIs.** The same `clc_…` works on `/v1/messages` and
`/v1/responses`; only the header differs, and the gateway takes either on
either —

```
Authorization: Bearer clc_…     what Codex and the OpenAI SDKs send
x-api-key: clc_…                what Claude Code and the Anthropic SDKs send
```

— so the one thing to get right is that Codex reads its key from the
*environment variable* named by `env_key`, never from the config file.

## One command instead of files

A typed client's page makes a command that writes its files into place:

    curl -fsSL '<public url>/install?token=…&shell=sh' | sh       # Linux and macOS
    irm '<public url>/install?token=…&shell=ps1' | iex            # Windows

A command works **once**, for fifteen minutes. The page shows it waiting with
its countdown, then when, from which address and on which platform it was run,
or that it expired; a new one is a click. Running it marks the client
configured.

A tool's own config is merged, not replaced: crush gets its `claudication`
provider and its `large`/`small` picks, opencode its provider and default
models, Codex its model keys and its `[model_providers.claudication]` table,
and everything else in the file stays. In Codex's file the gateway's part sits
between `# >>> claudication` markers. JSON on Linux and macOS is merged with
`python3` or `node`; with neither, the gateway's version is put beside the file
as `.claudication`. JSON comments in the existing file do not survive a merge.
Every file it changes is kept beside it as `.bak.<time>`. On Linux and macOS,
Claude Code's file is sourced from `~/.bashrc` and `~/.zshrc`; on Windows its
variables are set for the user.

## Model names

There are no gateway-wide model names. Each client has **slots**: one per model
setting its tool has, each with a name the tool asks for and a binding the
gateway resolves it to.

| Type | Slots, and the name each starts with |
|---|---|
| Claude Code | `large` (`ANTHROPIC_MODEL`), `small` (`ANTHROPIC_SMALL_FAST_MODEL`), `opus`, `sonnet`, `haiku`, `fable` (`ANTHROPIC_DEFAULT_*_MODEL`), `auto` (`CLAUDE_CODE_AUTO_MODE_MODEL`), and any more the operator adds, for `/model` |
| Codex CLI | `large` (`model`), `review` (`review_model`), `agents` (`agents.default_subagent_model`), `small` (`memories.extract_model`, `memories.consolidation_model`), and any more the operator adds, for `/model` and `-m` |
| opencode | `large` (`model`), `small` (`small_model`), and any more the operator adds, for its model list |
| crush | `large` (`models.large`), `small` (`models.small`), and any more the operator adds, for its picker |
| Custom | `large`, and any more the operator adds |

A name is the client's own: two clients can both have a `large`, bound to
different models. It can be renamed on the client's page to anything a config
can carry — lower case, digits, dots and dashes. Names say nothing of a
provider, because a slot can serve either; a Claude Code tier is named for the
tier (`opus` is what `/model opus` asks for), not for the model behind it.

### What a client reads off a name

Some clients switch features on a model's id. None needs the name for it: each
switch has a setting that says the same thing, and the client's files set it.
Each was read from the client and checked against a stub server with the
generated files (the full record is `go doc ./internal/service/clients`):

| Client | Keyed off the id | The setting the files use instead |
|---|---|---|
| Claude Code 2.1.304 | prompt profile, thinking and effort defaults, window — by family substring and full versioned id; `/v1/models` keeps ids with `claude`, but only with `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`, which is not set | `modelPicker` rows with `behavesAs`, one per name but the classifier's: requests under `large`, `opus`… match those under `claude-large`, `claude-opus`… byte for byte but for the model field |
| opencode 1.17.13 | tool-call streaming off on the Anthropic provider for an id without `claude`; the Anthropic-tuned system prompt for one with it | `"options": {"toolStreaming": true}` on each name, which brings `eager_input_streaming` back on every tool. The prompt has no per-model setting: a plain name gets opencode's generic one |
| crush (fantasy 0.45.1) | summarized thinking asked for only for real Opus, Sonnet and Fable 5 ids, which return empty thinking text otherwise — no name matches, marked or not | `"provider_options": {"thinking_display": "summarized"}` on the provider |
| Codex | nothing: everything it decides about a model is a field of the catalog entry its name keys | the catalog, `codex-models.json` |

Claude Code and opencode names used to carry `claude` for the first two rows.
Clients made then keep their names, which go on working; running the install
command again writes the settings above for them too.

A slot is bound either to a **model** — any model an account serves, Claude or
GPT — or to a **target it follows**: the newest Opus, Sonnet, Haiku or Fable,
ChatGPT's first-ranked model or its light one. A new client's slots follow
targets, so it moves to a new release on its own.

A typed client may ask for its slot names and nothing else: every model it can
ask for of its own accord has a slot, so a real id typed into its model picker
is refused, naming its slots. That is what makes moving a client one change on
its page and nowhere else.

A **custom** client has two modes, and keeps both settings: in **binding** mode
it names its slots like a typed one; in **direct** mode it names real model ids,
and a Models page on the client says which it may use. Keys from before clients
existed became custom clients in direct mode.

### What reaches what

Both APIs reach both providers: a model of the provider that speaks the
client's API as the request came, one of the other through the gateway's
translation. See [client-apis.md](client-apis.md).

A client is only ever offered what it can be served now: its page's menus,
a direct-mode client's Models page and its `/v1/models` list a model only when
a connected, switched-on account lists it and has it on, and a target only
while it resolves to such a model. A slot whose binding has since become
unavailable keeps it, shown with the reason — no account lists it, or it is
switched off on every account — and leaves the client's model list until it
is rebound to something available.

A client keeps the type it was made with. Its key was issued for that one
client, and its slots are its type's configuration fields, so another type
is another client: make one, with its own key, rather than change one.

A client made before the API became its type's — an opencode or a crush once
pointed at Responses — speaks its type's API from then on, and needs its
install command run again for its files to say so. Its names stay as they
were.

### The files carry the models' facts

crush and Codex do not discover a model's limits, and opencode takes them only
from its config, so a client's files describe each name with the model it
serves now: its context window, output cap and effort levels. They are written
when the page or the install command writes them; after rebinding a slot to a
model with other limits, run the install command again (`make test-web` checks
every rewrite against the committed files; `web/src/ui/clients/configs.ts`).

## Chats

A client's chats are on its page. The gateway groups requests into chats by the
session header its type sends (in `internal/service/clients`), and names them.

Claude Code, crush and opencode name their own sessions, and the gateway reads
that name off the answer: that request is counted in the chat's cost but not
as one of its turns, and nothing more is sent. Since that costs nothing, those
types have no switch and their chats are always named. (Claude Code names only
interactive sessions: `claude -p` sends no naming request, so a headless chat
stays unnamed.)

For Codex and custom clients the gateway asks the chat's own model for a
title itself, under the client's key, on the provider that served the chat,
and not while the client is at a limit — a request on the operator's
subscription, which the client's **Name chats** switch turns off.

## Switches and limits

A client can be switched off from its row or its page: its requests are then
refused `403 this client is disabled`, and recorded as refused. Each has a
request rate and a rolling 24-hour token budget, set on its Usage page beside
the figures they bound, with suggestions from its own last 7 or 30 days and a
preview of what a limit would have refused. Above them all is the **Gateway**
switch in Settings: off, every client request is refused, and `/health` says
`"accepting": false`.

A client that sends many screenshots has one more, on its page: **Shrink
images when a request carries more than 20**. Past 20 images in one request
Claude refuses any over 2000 px on a side, and images from earlier turns
count, so a long conversation starts being refused over a picture already in
its history. Switched on, the gateway caps those images at 2000 px on a
request Claude serves, and touches nothing else. Off by default, being the one
change it makes to what the model is shown; why, and what it costs, is in
[refused-requests.md](refused-requests.md#the-one-rewrite-you-have-to-ask-for).

### Names of your own, for a client's picker

Every typed client can have more names than its settings take: **Add a model**
on its page adds one, bound to any model or target like the others, and the
next file the page writes declares it, so the client's own model picker offers
it for a chat. Such a name can be renamed, rebound and removed; the type's own
names stay, since its settings always name them. Where each one appears:

| Client | Where an added name is declared | How a chat picks it |
|---|---|---|
| Claude Code | a `modelPicker` row with `behavesAs` the model it serves; no variable | `/model <name>`, or the `/model` list |
| Codex CLI | an entry in `claudication-models.json` at its model's window; no key of `config.toml` | `/model`, or `codex -m <name>` |
| opencode | a model on the `anthropic` provider at its model's limits, with `toolStreaming` | the model list (`/models`) |
| crush | an entry of the provider's `models`, named `<name> · <model>` | `ctrl+l`, either tab (below) |

Claude Code takes a `/model <name>` that is a `modelPicker` row at once, and
handles it as the model its `behavesAs` names; a name with no row is first
probed with a one-token request and then run as an unknown model, at a guessed
200k window. Measured on 2.1.308 against a stub; the record is in
`go doc ./internal/service/clients` (ClaudeCode).

### crush's picker

crush runs its coder, task and plan agents and its summaries on the **large**
model, and only a session's title and the web-fetch tool's own agent on the
**small** one. `ctrl+l` opens its picker, which lists every model the file
declares, in a Large and a Small tab, by each one's name — so `crush.json`
names each entry for its slot and the model it serves now, `large · Claude
Opus 5.5`.

A name added on a crush client's page is listed beside `large` and `small`:
the way to keep, say, a GPT model and a second Claude one a keystroke away.

The file's `models.large` and `models.small` are only where a first run
starts. crush saves every pick to its data file —
`~/.local/share/crush/crush.json` — which it reads after `~/.config/crush/crush.json`,
and which wins. A pick also saves the `max_tokens` of the moment, and crush
sends it on every request after; when the slot is later rebound to a model
with a smaller output cap, the gateway lowers that budget to the cap of the
model the slot serves now rather than let the upstream refuse it (for every
client naming its slots; `go doc ./internal/relay/passes`).

### When crush compacts

crush summarises a session when the room left in `context_window` falls to
20k tokens (or to 20% of the window, for windows of 200k and under). It counts
the context correctly — input, cache reads and cache writes, as the gateway
passes them through — so the figure in the config is the one that decides.

`crush.json` declares **600k** for the models whose real window is 1M, so crush
compacts at about 580k. Declared at 1M it compacted at 980k, and sessions ran
there: on the request log over four days of crush traffic, the median Opus turn
carried 458k tokens and the largest 981k. Every turn re-reads its whole cached
context, so that is quota and time to first token spent on every turn, and
models do their weakest work at the far end of a long context. 600k keeps most
sessions whole and stops the longest ones well short of the edge.

The number only moves crush's compaction point and its context gauge; nothing
is sent upstream, and the window Anthropic allows stays 1M. Raise or lower it
in your own copy to taste — 800k compacts rarely, 400k often.

## Before you run Codex

Two prerequisites that have nothing to do with the gateway. Both look like the
gateway failing, and neither is.

**bubblewrap.** Codex sandboxes every shell command, and without `bwrap` the
first tool call panics:

```
bubblewrap is unavailable: no system bwrap was found on PATH and no bundled
codex-resources/bwrap binary was found next to the Codex executable
```

The model then explains, at length and convincingly, that it cannot run
anything — which reads like a broken tool bridge. Install `bubblewrap` from
your package manager, or drop the one from the Codex release next to the binary
at `codex-resources/bwrap`.

**The model catalog**, which is what `codex-models.json` is. Codex looks its
model up in a catalog compiled into its own binary, and the client's names are
not in it:

```
warning: Model metadata for `large` not found. Defaulting to
fallback metadata; this can degrade performance and cause issues.
```

The fallback is conservative, so a million-token model gets auto-compacted as
though it were far smaller and long sessions shed context early.
`model_context_window` in config.toml does **not** fix it — the lookup is by
name. The gateway writes one entry per slot name, at its model's window.

Codex refuses an entry carrying neither `base_instructions` nor
`model_messages.instructions_template`, so every catalog must contain a system
prompt. The one in `codex-models.json` is a short stand-in, because Codex's own
lives inside its binary. Measured on the same task, that is 10,148 tokens a
turn against 19,346 with Codex's real prompt — cheaper, and less good at
editing. To use the real one:

```sh
scripts/codex-model-catalog.py --codex $(which codex)
```

It clones a real entry per model out of the binary you are running. Re-run it
after upgrading Codex.

## curl, to check the plumbing

With a custom client's key in direct mode, which names real models:

```sh
curl https://claudication.example.com/v1/messages \
  -H "x-api-key: clc_…" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-haiku-4-5","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'

curl -N https://claudication.example.com/v1/responses \
  -H "authorization: Bearer clc_…" \
  -H "content-type: application/json" \
  -d '{"model":"claude-opus-5","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'
```

With a typed client's key, name one of its slots instead. `/v1/models` lists
what that key may ask for:

```sh
curl https://claudication.example.com/v1/models -H "x-api-key: clc_…"
```

A real Codex request, replayed and judged the way Codex judges it:

```sh
scripts/probe-codex.py --key clc_… --url https://claudication.example.com --roundtrip
```

## Which base URL

The client's page shows it. Two cases:

- **One listener** (`admin-listen` empty) — the address your browser is on is
  also the relay, and the UI uses it.
- **Split listeners** — the browser is on the *admin* address and the relay is
  somewhere else. The gateway cannot learn the hostname clients use, so set
  `public-url` in the config or the UI will say it is guessing.

## From the shell

On the machine running the gateway. Shell access to the state directory is
already the higher privilege, so none of these asks for the admin password.

| Command | What it does |
|---|---|
| `claudication clients add -name NAME [-type TYPE]` | Make a client and print its key, for a provisioning script. |
| `claudication clients list`, `clients delete -id ID` | List clients; delete one and its key. |
| `claudication passwd` | Reset the admin password. The recovery path when it is lost. |
| `claudication login-url` | A single-use link that signs a browser in. Spent on first use. |
| `claudication backup FILE` | A consistent snapshot without stopping the service. Holds the sealing key and every stored token, so it is as sensitive as the state directory. |

## When it does not work

| What you see | What it usually is |
|---|---|
| 404 on every request | The `/v1` question above. Add it or drop it. |
| 404 saying the gateway is switched off | The **Gateway** switch in Settings is off. |
| 403 "this client is disabled" | The client's own switch is off, on its row or its page. |
| 400 "dimensions exceed max allowed size for many-image requests" | More than 20 images in the request and one over 2000 px. Switch on **Shrink images** on the client's page. |
| 400 naming the client's model names | A typed client asked for a name that is none of its slots — usually a model typed into its picker by hand. |
| 401 | The key is wrong, regenerated, or in a header this client does not send. Either header works, on either API. |
| 429 whose message is the single word `Error` | Not a rate limit. The attribution gate — see [refused-requests.md](refused-requests.md). Leave `passthrough.claude-code-attribution` on. |
| "Third-party apps now draw from your extra usage" | Not a billing message. A content check refused the request; bisect it with `scripts/bisect-refusal.py`. |
| Only haiku works, opus and sonnet 429 | Same attribution gate, from the other side. |
| Codex: "stream closed before response.completed" | The gateway sends a terminal event on every path, so this points at something between it and Codex — usually a proxy buffering the stream. |

## A note on what is verified

All four configurations have been run. The opencode, crush and Claude Code ones
are in use on this network; the Codex one was checked by installing Codex CLI
0.154.0 and pointing it at the gateway, which held multi-turn sessions with
real shell tool calls against a live subscription on both Sonnet 5 and Opus 5 —
with the shipped `codex-models.json` and with a cloned one. Fable was checked
directly against both APIs. The per-client names were checked against each
type's tests and the sources cited in `internal/service/clients`.
