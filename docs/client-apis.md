# The client-facing APIs

The gateway has two upstreams: Claude subscription accounts, which answer
Anthropic Messages, and ChatGPT subscription accounts, which answer the Codex
backend's OpenAI Responses API. Everything a *client* may speak is an adapter
onto one of them.

Two surfaces exist today, and each client type speaks one of them, fixed by
the type; a custom client's key works on both.

| Surface | Routes | Who speaks it |
|---|---|---|
| Anthropic Messages | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/models` | Claude Code, opencode and crush, and anything written against the Anthropic SDK |
| OpenAI Responses | `/v1/responses` | Codex CLI |

Both are served while the **Gateway** switch in Settings is on, and neither
is while it is off: one switch, because what an operator turns off when
something is wrong is the gateway, not one dialect of it. A change takes effect
on the next request with no restart. Switched off, every client-facing route
answers 404 in its own dialect's error envelope, so a client reports it and
stops rather than retrying, and `/health` says `"accepting": false`. Each
client and each account has a switch of its own besides.

The switch is kept in the database rather than the config file, because
"edit a file, restart, drop every stream in flight" is not an answer to
something going wrong. It shows up in the configuration table with origin
`database`, so one screen still tells the whole story of where each value came
from.

## Models, and which upstream serves a request

A request is routed by the model it names and the client whose key it
carries, never by guessing from the name:

1. A typed client — and a custom one in binding mode — names one of its own
   slots. The slot resolves through that client's table: the model it is
   bound to, or what the target it follows points at now. Any other name is
   refused, listing the client's names.
2. A custom client in direct mode names real models.
3. The real model is served by the provider whose accounts list it. A name no
   list knows goes to Claude unchanged, which says whether it knows it.

Then the dialect and the upstream decide what happens:

| client API → upstream | |
|---|---|
| Messages → Claude | as it came (the passes aside; see [refused-requests.md](refused-requests.md)) |
| Responses → Claude | translated, in `internal/api/openai` |
| Responses → ChatGPT | as it came, to `/responses`; only the credential and the workspace are added |
| Messages → ChatGPT | **refused**, `400 invalid_request_error`, naming the model or slot and what to change |

The last is refused because translating Messages into Responses is a mapping
of its own — thinking against encrypted reasoning, tool ids, cache control —
and not one this gateway has. So a Messages client is never offered a
ChatGPT model: not in its page's menus, not in its `/v1/models`. A slot bound
to one before is shown as unavailable, with the reason, until it is rebound.

Two of the targets a slot can follow are ChatGPT's: its first-ranked model and
its light one, read from the backend's own ranking of the models an account
lists (the `priority` each carries, which is Codex's picker order) — the first
choice, and the last of that model's generation: `gpt-6.1-sol` and
`gpt-6-luna` on 2026-10-19. Any slot may be bound to either provider's model.

So every model an account serves works on every API that can reach it, and a
new one works the day it appears. A client's page reads the live list through
`GET /admin/models` and writes the client's files from it.

## Where the code is

```
internal/api/            the contract, and what every dialect would otherwise rewrite
internal/api/anthropic/  the Anthropic surface — the identity adapter
internal/api/openai/     the OpenAI surface: its mapping both ways, and the
                         identity exchange (native.go) for the ChatGPT backend
internal/service/clients each client type's model fields, names and session headers
internal/service/models  the targets a slot follows, and the route a model takes (route.go)
internal/provider/       the upstreams, one package each
```

`go doc ./internal/api` is the short version: `Protocol` is a dialect,
`Exchange` is one request in flight, `Sink` wraps the caller's writer so the
answer comes back in the caller's shape. `NewReshapingSink` is the part worth
knowing about — it classifies an upstream answer into the three shapes one can
arrive in and hands each to the dialect, so a new surface writes conversions
and not plumbing.

Anthropic is an implementation rather than a bypass. That is deliberate: the
relay's contract is expressed once, as an adapter that translates nothing,
instead of as a branch every other layer has to remember not to take.

## Pointing Codex at it

Make a Codex client; its page writes `~/.codex/config.toml` and the model
catalog, and its install command puts them in place. The gist:

```toml
model_provider = "claudication"
model = "large"
review_model = "review"

[model_providers.claudication]
name = "claudication"
base_url = "https://claudication.example.com/v1"
env_key = "CLAUDICATION_API_KEY"
wire_api = "responses"
```

Then `export CLAUDICATION_API_KEY=clc_…` and run `codex`.

`wire_api = "responses"` is not optional: Responses is the only wire format
Codex has. `chat_completions` appears nowhere in its binary, while `/responses`
appears 41 times.

`large`, `review` and the rest are the client's slots, each bound on its page
to a Claude model — translated — or a GPT one, sent to the ChatGPT backend as
Codex wrote it. Moving one is a change on the page and nothing on the machine.
None of these names is in Codex's built-in catalog, so the client's
`codex-models.json` has one entry per name at its model's window (see
[clients.md](clients.md#before-you-run-codex)).

## What the ChatGPT backend is sent

The request as Codex wrote it, to `{backend}/responses`, with Codex's own
`originator`, `User-Agent`, `session-id` and `x-codex-*` headers. The gateway
adds the account's bearer token and `ChatGPT-Account-ID`, the workspace it
bills, and an `originator: claudication` only for a client that sent none. It
never claims to be Codex. A slot name is resolved in the body's `model` field
and said back in the answer's events and `OpenAI-Model` header.

A conversation stays on the ChatGPT account that served it last: Codex sends
its earlier reasoning back encrypted, and whether another account can read it
is not measured. The measurements behind this wire are in
`go doc ./internal/provider/openai`.

## What the translation does, and what it refuses to do

The full record, with the measurements behind every decision, is in
`go doc ./internal/api/openai`. It is kept in the code rather than here because
it is what a maintainer meets first, and because reversing Codex again to
recover it would cost a day.

The parts that surprise people:

- **Anthropic requires `max_tokens`; Codex never sends one.** The gateway
  asks for 32,000 — one long answer, not a budget; the account's own limits are
  the budget. Without a figure every request would be refused.
- **`developer` is a role Anthropic does not have.** Those turns fold into the
  system prompt — they are instruction, not conversation, and making them user
  turns would put them in the transcript as something the user said.
- **Consecutive turns of the same role are merged**, because Anthropic rejects
  two in a row and Codex sends them routinely.
- **Tool arguments are a JSON string on one side and an object on the other.**
- **Namespaced tools go out flattened and come back whole**, because Anthropic
  has no nested tools and Codex cannot route a call without its namespace.
- **The stream must end in `response.completed`.** A stream that ends any other
  way fails the turn in Codex whatever preceded it, so the translator always
  sends a terminal event — including when the upstream simply stopped.
- **Anthropic's `ping` becomes `response.in_progress`**, an event Codex
  explicitly ignores. An idle gap also fails a turn, so the keepalive has to
  become bytes that Codex reads and discards.
- **A rate limit maps to `slow_down`, not `rate_limit_exceeded`**, which is not
  a code Codex acts on. A pooled gateway produces rate limits more than
  anything else, so getting this wrong would turn the commonest failure into a
  silent retry.

What a surface must never do is synthesise attestation. Shape — tool names,
block structure, phrasing — is ours to normalise. The billing header's keyed
checksum, `x-cc-atis` and the `metadata.user_id` blob are deliberately not
forged. See [refused-requests.md](refused-requests.md).

## Adding another

DeepSeek, or any of the many clients that speak OpenAI *chat completions*
rather than Responses, would be another route on the existing OpenAI surface
rather than a new one — which is why `Routes()` returns a list. A genuinely
different shape gets its own package under `internal/api/`.

Either way the work is five pieces: the request mapping, the three answer
conversions, the error envelope, one registration in
`internal/httpapi/server.go`, which is the composition root and the only place
that knows which dialects exist, and its route in `internal/httpapi/gateway`,
which serves every client-facing API the same way.
