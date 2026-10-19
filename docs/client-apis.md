# The client-facing APIs

The gateway has two upstreams: Claude subscription accounts, which answer
Anthropic Messages, and ChatGPT subscription accounts, which answer the Codex
backend's OpenAI Responses API. Everything a *client* may speak is an adapter
onto one of them.

Two surfaces exist today.

| Surface | Routes | Who speaks it |
|---|---|---|
| Anthropic Messages | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/models` | Claude Code, and anything written against the Anthropic SDK |
| OpenAI Responses | `/v1/responses` | Codex CLI |

Each has a switch in **Settings → API surfaces**. Either can be turned off,
both can be off at once, and a change takes effect on the next request with no
restart. A switched-off surface answers 404 in its own dialect's error
envelope, so a client reports it and stops rather than retrying.

Those switches are the only setting the gateway keeps in the database rather
than the config file, and they are there because turning an API off is what an
operator does when something is wrong — "edit a file, restart, drop every
stream in flight" is not an answer to that. They show up in the configuration
table with origin `database`, so one screen still tells the whole story of
where each value came from.

## Models, and which upstream serves a request

A request is routed by the model it names, never by guessing from the name:

1. An alias — `claude-large`, `gpt-small`, a family's, one of the operator's —
   resolves as it always has: the key's binding, else the client's, else the
   gateway's, else what the built-in follows (Settings › Aliases).
2. The real model it resolves to, or the one a client named directly, is
   served by the provider whose accounts list it.
3. A name no list knows goes where its dialect has always sent it: a Messages
   request to Claude unchanged, a Responses request to `claude-large` — which
   is what keeps a Codex left on its own default model working on a gateway
   with no ChatGPT account.

Then the dialect and the upstream decide what happens:

| client API → upstream | |
|---|---|
| Messages → Claude | as it came (the passes aside; see [refused-requests.md](refused-requests.md)) |
| Responses → Claude | translated, in `internal/api/openai` |
| Responses → ChatGPT | as it came, to `/responses`; only the credential and the workspace are added |
| Messages → ChatGPT | **refused**, `400 invalid_request_error`, naming the model or alias and what to change |

The last is refused because translating Messages into Responses is a mapping
of its own — thinking against encrypted reasoning, tool ids, cache control —
and not one this gateway has. Settings › Aliases warns when a `claude-*` alias
is bound to a ChatGPT model, since Claude Code would meet that refusal; and
the `/v1/models` Claude Code reads leaves out what it could not use.

`gpt-large` and `gpt-small` follow the ChatGPT backend's own ranking of the
models an account lists (the `priority` each carries, which is Codex's picker
order): large its first choice, small the last of that model's generation —
`gpt-6.1-sol` and `gpt-6-luna` on 2026-10-19. Any alias may be bound to either
provider's model.

So every model an account serves works on every API that can reach it, and a
new one works the day it appears. The Setup tab reads the live list through
`GET /admin/models` and builds each client's configuration from it.

## Where the code is

```
internal/api/            the contract, and what every dialect would otherwise rewrite
internal/api/anthropic/  the Anthropic surface — the identity adapter
internal/api/openai/     the OpenAI surface: its mapping both ways, and the
                         identity exchange (native.go) for the ChatGPT backend
internal/service/models  the aliases and the route a model takes (route.go)
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

`~/.codex/config.toml`:

```toml
model_provider = "claudication"
model = "claude-large"

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

The `model` line is worth setting. `claude-large` runs Codex on a Claude
account, translated; `gpt-large` runs it on a ChatGPT account, sent to the
backend as Codex wrote it, once one is connected. Either alias can be moved to
any model in Settings › Aliases, for Codex alone if need be, and the configs a
key's Clients page writes name the one that key is served. A name no account
lists — Codex's own default `gpt-5-codex` on a gateway with no ChatGPT account
— is sent as `claude-large`, as it always was.

`gpt-large` is no more in Codex's built-in model catalog than `claude-large`
is, so Codex falls back to generic metadata for it; the shipped
`codex-models.json` covers the Claude names only (see
[clients.md](clients.md#before-you-run-codex)).

## What the ChatGPT backend is sent

The request as Codex wrote it, to `{backend}/responses`, with Codex's own
`originator`, `User-Agent`, `session-id` and `x-codex-*` headers. The gateway
adds the account's bearer token and `ChatGPT-Account-ID`, the workspace it
bills, and an `originator: claudication` only for a client that sent none. It
never claims to be Codex. A model alias is resolved in the body's `model`
field and said back in the answer's events and `OpenAI-Model` header.

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
