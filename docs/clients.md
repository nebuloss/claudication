# Pointing a client at the gateway

Every client needs the same two things: a **base URL** and an **API key**. The
rest is per-client detail, and the detail that trips people up is whether the
base URL includes `/v1` — each client has its own opinion and none of them
warn you when you get it wrong. You just get 404s.

Mint a key in **Settings → API keys**, or from a shell:

```sh
claudication keys add -name laptop
```

Keys look like `clc_…` and are shown once.

## Which base URL

The admin UI shows it under **Point a client at it**. Two cases:

- **One listener** (`admin-listen` empty) — the address your browser is on is
  also the relay. The UI uses it directly.
- **Split listeners** — the browser is on the *admin* address and the relay is
  somewhere else. The gateway cannot learn the hostname clients use, so set
  `public-url` in the config or the UI will say it is guessing.

## Claude Code

No `/v1`. Claude Code appends the path itself.

```sh
export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_…
claude
```

`ANTHROPIC_AUTH_TOKEN`, not `ANTHROPIC_API_KEY`: the token form is sent as a
bearer credential, which is what the gateway expects. Both work today, but the
token variable is the one the client documents for a custom base URL.

## opencode

**With** `/v1`. It overrides the Anthropic provider's endpoint rather than
declaring a new provider, so models come from `/v1/models` and nothing needs
listing by hand.

`~/.config/opencode/opencode.jsonc`:

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "https://claudication.example.com/v1",
        "apiKey": "clc_…"
      }
    }
  },
  "model": "anthropic/claude-opus-5"
}
```

If opencode reports a model from some other vendor at startup — `deepseek-…`,
say — it is not using this provider at all and a stale key elsewhere is
winning. Check what it prints before believing a test that passed.

## crush

**Without** `/v1`, and it needs the model list spelled out: crush does not call
`/v1/models`, so a model absent from this file cannot be selected however well
the gateway serves it.

`~/.config/crush/crush.json`:

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "claudication": {
      "name": "Claudication Gateway",
      "base_url": "https://claudication.example.com",
      "type": "anthropic",
      "api_key": "clc_…",
      "models": [
        {
          "id": "claude-opus-5",
          "name": "Claude Opus 5",
          "context_window": 1000000,
          "default_max_tokens": 128000,
          "can_reason": true,
          "supports_attachments": true,
          "cost_per_1m_in": 0,
          "cost_per_1m_out": 0,
          "cost_per_1m_in_cached": 0,
          "cost_per_1m_out_cached": 0,
          "reasoning_levels": ["low", "medium", "high", "xhigh", "max"],
          "default_reasoning_effort": "medium"
        },
        {
          "id": "claude-haiku-4-5-20251001",
          "name": "Claude Haiku 4.5",
          "context_window": 200000,
          "default_max_tokens": 64000,
          "can_reason": false,
          "supports_attachments": true,
          "cost_per_1m_in": 0,
          "cost_per_1m_out": 0,
          "cost_per_1m_in_cached": 0,
          "cost_per_1m_out_cached": 0
        }
      ]
    }
  },
  "models": {
    "large": { "provider": "claudication", "model": "claude-opus-5", "think": true },
    "small": { "provider": "claudication", "model": "claude-haiku-4-5-20251001" }
  }
}
```

The zero costs are deliberate: a subscription is not billed per token, and
leaving real prices in makes crush display a running total that is fiction.

## Codex CLI

**With** `/v1`, and this one goes through the OpenAI surface rather than the
Anthropic one — see [client-apis.md](client-apis.md).

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

```sh
export CLAUDICATION_API_KEY=clc_…
codex
```

Three things are not optional:

- **`wire_api = "responses"`.** Responses is the only wire format Codex has;
  `chat_completions` appears nowhere in its binary. Leave this out and it
  defaults to a shape the gateway does not serve.
- **`env_key`** names an environment variable, not a key. Codex reads the key
  from the environment; putting `clc_…` here directly does not work.
- **`model`** should name a Claude model. Codex asks for `gpt-5-codex` by
  default, which means nothing upstream; the gateway then substitutes
  `openai.model` from its config. Naming one here keeps the choice visible.

## curl, to check the plumbing

Anthropic surface:

```sh
curl https://claudication.example.com/v1/messages \
  -H "x-api-key: clc_…" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-haiku-4-5","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'
```

OpenAI surface:

```sh
curl -N https://claudication.example.com/v1/responses \
  -H "authorization: Bearer clc_…" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-5","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'
```

A real Codex request, replayed and judged the way Codex judges it:

```sh
scripts/probe-codex.py --key clc_… --url https://claudication.example.com --roundtrip
```

## When it does not work

| What you see | What it usually is |
|---|---|
| 404 on every request | The `/v1` question above. Add it or drop it. |
| 404 saying the API is "turned off" | The surface is switched off in **Settings → API surfaces**. |
| 401 | The key is wrong, revoked, or in the wrong header. The Anthropic surface takes `x-api-key` or a bearer token; the OpenAI surface takes a bearer token. |
| 429 whose message is the single word `Error` | Not a rate limit. The attribution gate — see [refused-requests.md](refused-requests.md). Leave `passthrough.claude-code-attribution` on. |
| "Third-party apps now draw from your extra usage" | Not a billing message. A content check refused the request; bisect it with `scripts/bisect-refusal.py`. |
| Only haiku works, opus and sonnet 429 | Same attribution gate, from the other side. |
| 503 "no Claude account is connected" | Add one under **Accounts**. |
| Codex: "stream closed before response.completed" | The gateway sends a terminal event on every path, so this means the connection died between it and Codex — look at a proxy in the middle before looking here. |

## A note on what is verified

The opencode, crush and Claude Code stanzas above are the ones in use on this
network. The Codex stanza is the shape a real Codex 0.154.0 install produced
when pointed at a capture stub, and the gateway path behind it is checked by
replaying that captured request — including a tool call and handing its output
back — against the live subscription. Running the `codex` binary itself against
the gateway end to end has not been done here; if something in its config
handling differs from the capture, that is where it would show.
