# Pointing a client at the gateway

A **client** is one tool on one machine, as the gateway knows it: a type, one
API key, its own model names, its limits and its switches. Make one on the
**Clients** screen of the admin UI; its page then has everything that tool
needs — the key, the address, its files and a one-line install command.

| Type | What the gateway writes | API it is pointed at | `/v1` on the base URL? |
|---|---|---|---|
| Claude Code | [`claude-code.sh`](../configs/clients/claude-code.sh) | Anthropic Messages | no |
| Codex CLI | [`codex.toml`](../configs/clients/codex.toml) + [`codex-models.json`](../configs/clients/codex-models.json) | OpenAI Responses | yes |
| opencode | [`opencode.jsonc`](../configs/clients/opencode.jsonc) | Responses (default) or Messages | yes |
| crush | [`crush.json`](../configs/clients/crush.json) | Messages (default) or Responses | no on Messages, yes on Responses |
| Custom | nothing: the key, both addresses, its names and a snippet per SDK | either | — |

The files in `configs/clients/` are each type's starting point, complete and
commented, with the names a new client of that type is given. A client's page
writes them with its own names, its own API and its own key. Each type's table
— its model fields, its default names, which APIs it can use and why — is
`go doc ./internal/service/clients`, with the source each was read from.

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
| Claude Code | `claude-large` (`ANTHROPIC_MODEL`), `claude-small` (`ANTHROPIC_SMALL_FAST_MODEL`), `claude-opus`, `claude-sonnet`, `claude-haiku`, `claude-fable` (`ANTHROPIC_DEFAULT_*_MODEL`), `claude-auto` (`CLAUDE_CODE_AUTO_MODE_MODEL`) |
| Codex CLI | `large` (`model`), `review` (`review_model`), `agents` (`agents.default_subagent_model`), `small` (`memories.extract_model`, `memories.consolidation_model`) |
| opencode | `large` (`model`), `small` (`small_model`) |
| crush | `large` (`models.large`), `small` (`models.small`) |
| Custom | `large`, and any more the operator adds |

A name is the client's own: two clients can both have a `large`, bound to
different models. It can be renamed on the client's page, within a rule some
tools impose — Claude Code keeps only model ids containing `claude`, opencode
streams tool calls over Messages only for such ids, and crush speaks Responses
only for an id that looks like a GPT one, so its Responses names carry `gpt-5`.

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

### Which API, and what reaches what

Messages reaches Claude only; Responses reaches both, ChatGPT as the request
came and Claude through the gateway's translation (which does not carry
extended thinking). A slot bound to a GPT model on a Messages client is
refused rather than translated, saying which slot to rebind, and the client's
page warns beside it. See [client-apis.md](client-apis.md).

Changing a client's type, or its API, shows what happens to each slot before
anything is applied — kept, renamed to fit the new type's rule, added or
removed — matched by what each slot is for.

### The files carry the models' facts

crush and Codex do not discover a model's limits, and opencode takes them only
from its config, so a client's files describe each name with the model it
serves now: its context window, output cap and effort levels. They are written
when the page or the install command writes them; after rebinding a slot to a
model with other limits, run the install command again (`make test-web` checks
every rewrite against the committed files; `web/src/ui/clients/configs.ts`).

## Chats

A client's chats are on its page. The gateway groups requests into chats by the
session header its type sends (in `internal/service/clients`), and names them
while the client's **Name chats** switch is on: crush and opencode name their
own sessions, and the gateway reads that name off the answer — that request is
counted in the chat's cost but not as one of its turns, and nothing more is
sent. For the others it asks the chat's own model for a title, under the
client's key, on the provider that served the chat, and not while the client
is at a limit.

## Switches and limits

A client can be switched off from its row or its page: its requests are then
refused `403 this client is disabled`, and recorded as refused. Each has a
request rate and a rolling 24-hour token budget, set on its Usage page beside
the figures they bound, with suggestions from its own last 7 or 30 days and a
preview of what a limit would have refused. Above them all is the **Gateway**
switch in Settings: off, every client request is refused, and `/health` says
`"accepting": false`.

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
| 400 naming the client's model names | A typed client asked for a name that is none of its slots — usually a model typed into its picker by hand. |
| 400 saying a slot's model is not on this client's API | A Messages client's slot is bound to a GPT model. Rebind it, or point the client at Responses. |
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
type's tests and the sources cited in `internal/service/clients`; crush on
Responses with `gpt-5` names follows from its source and has not been run.
