# The client-facing APIs

The gateway speaks Anthropic Messages upstream and nothing else, because that
is what a Claude subscription account answers. Everything a *client* may speak
is an adapter around that shape.

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

## Models

Neither surface filters them. `/v1/models` is proxied from the upstream, the
relay forwards whatever model a caller names, and the only model-name logic in
the tree is one prefix test in `internal/api/openai/request.go`: a Responses
request asking for something that is not a `claude-` model gets `openai.model`
substituted, because the name it sent means nothing upstream.

So every model an account serves works on both APIs, and a new one works the
day it appears. The Setup tab reads the live list through `GET /admin/models`
and builds each client's configuration from it.

## Where the code is

```
internal/api/            the contract, and what every dialect would otherwise rewrite
internal/api/anthropic/  the Anthropic surface — the identity adapter
internal/api/openai/     the OpenAI surface, and its mapping both ways
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
model = "claude-sonnet-5"

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

The `model` line is worth setting. Codex asks for `gpt-5-codex` by default,
which means nothing upstream, so the gateway substitutes `openai.model` from
its config. Naming a Claude model in Codex's own config keeps the choice where
you can see it — a request that already asks for a `claude-` model is passed
through untouched.

## What the translation does, and what it refuses to do

The full record, with the measurements behind every decision, is in
`go doc ./internal/api/openai`. It is kept in the code rather than here because
it is what a maintainer meets first, and because reversing Codex again to
recover it would cost a day.

The parts that surprise people:

- **Anthropic requires `max_tokens`; Codex never sends one.** The gateway
  supplies `openai.max-tokens`. Without it every request would be refused.
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

Either way the work is four pieces: the request mapping, the three answer
conversions, the error envelope, and one registration in
`internal/httpapi/server.go`, which is the composition root and the only place
that knows which dialects exist.
