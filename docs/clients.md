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

**One key format, both APIs.** A Codex key is not a different kind of key, and
nothing has to look like `sk-…`: the same `clc_…` works on `/v1/messages` and
`/v1/responses` alike. Only the header the client chooses differs, and the
gateway takes either on either surface —

```
Authorization: Bearer clc_…     what Codex and the OpenAI SDKs send
x-api-key: clc_…                what Claude Code and the Anthropic SDKs send
```

— so the only thing to get right is that Codex reads its key from the
*environment variable* named by `env_key`, never from the config file.

## Which models

Whatever your accounts serve. The gateway has **no model allowlist**: it relays
the name a client sends, `/v1/models` is proxied straight from the upstream,
and the only model-name logic anywhere is the OpenAI surface substituting a
Claude model when a caller asks for something that is not one. Fable, Opus,
Sonnet and Haiku all work, on both APIs, and a model added upstream tomorrow
works without a gateway change.

Ask yours:

```sh
curl https://claudication.example.com/v1/models -H "x-api-key: clc_…"
```

The **Setup** tab lists the same thing and builds every configuration below
from it, which is the version that cannot go stale.

Two clients still need models named by hand, and both are the client's doing:
crush never calls `/v1/models`, and Codex looks names up in a catalog compiled
into its own binary. Everything else discovers them.

## Which base URL

The admin UI shows it under **Point a client at it**. Two cases:

- **One listener** (`admin-listen` empty) — the address your browser is on is
  also the relay. The UI uses it directly.
- **Split listeners** — the browser is on the *admin* address and the relay is
  somewhere else. The gateway cannot learn the hostname clients use, so set
  `public-url` in the config or the UI will say it is guessing.

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

**The model catalog.** Codex looks its model up in a catalog compiled into its
own binary, and a Claude name is not in it:

```
warning: Model metadata for `claude-sonnet-5` not found. Defaulting to
fallback metadata; this can degrade performance and cause issues.
```

The fallback is conservative, so a million-token model gets auto-compacted as
though it were far smaller and long sessions shed context early.
`model_context_window` in config.toml does **not** fix it — the lookup is by
name. Generate a catalog instead:

```sh
scripts/codex-model-catalog.py --codex ~/bin/codex
```

It writes an entry per model — Fable and Opus included — by cloning a real one
out of the Codex binary you are running, because an entry carries Codex's whole
system-prompt template and several dozen behaviour switches. Re-run it after
upgrading Codex.

## The recipes

<!-- GENERATED FROM configs/clients.json — edit that file, then run scripts/gen-client-docs.py -->

## Claude Code

No `/v1` — Claude Code appends the path itself. Use `ANTHROPIC_AUTH_TOKEN` rather than `ANTHROPIC_API_KEY`: it is the variable the client documents for a custom base URL.

Environment:

```sh
export ANTHROPIC_BASE_URL=https://claudication.example.com
export ANTHROPIC_AUTH_TOKEN=clc_…
export ANTHROPIC_MODEL=claude-opus-5
export ANTHROPIC_SMALL_FAST_MODEL=claude-haiku-4-5-20251001
claude
```

The two model variables are optional — without them Claude Code discovers models through `/v1/models`, which the gateway proxies. Set them to pin a model, and keep the small one small: it runs many times a session for titles and summaries.

## Codex CLI

With `/v1`, and this is the one client that goes through the OpenAI Responses API rather than the Anthropic one. `wire_api = "responses"` is not optional — Responses is the only wire format Codex has, and `chat_completions` appears nowhere in its binary.

`~/.codex/config.toml`:

```toml
model_provider = "claudication"
model = "claude-opus-5"
model_catalog_json = "~/.codex/claude-models.json"

[model_providers.claudication]
name = "claudication"
base_url = "https://claudication.example.com/v1"
env_key = "CLAUDICATION_API_KEY"
wire_api = "responses"
```

Then:

```sh
export CLAUDICATION_API_KEY=clc_…
codex
```

`env_key` names an environment variable, not a key — putting `clc_…` there directly does not work.

Set `model` to a Claude model. Codex asks for `gpt-5-codex` by default, which means nothing upstream, and the gateway then substitutes whatever `openai.model` says.

Codex looks its model up in a catalog compiled into its own binary, so a Claude name falls back to conservative limits and long sessions shed context early. Generate the file `model_catalog_json` points at with the script below.

Codex needs `bubblewrap` installed before it can run any shell command. Without it the first tool call panics and the model then explains, convincingly, that nothing works — which reads like a broken tool bridge and is not one.

**`scripts/codex-model-catalog.py`** — Writes the file `model_catalog_json` points at. Run it on the machine Codex is installed on: the catalog has to carry Codex's own system-prompt template, which only its binary has, so the gateway cannot generate it for you. `python3 codex-model-catalog.py --codex $(which codex)`

The admin UI offers it as a download under Setup, which is the only
way to get it on a machine that installed a release binary and has no
checkout.

## opencode

With `/v1`. It overrides the built-in Anthropic provider rather than declaring a new one, so every model the gateway serves is available without listing any of them.

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
  "model": "anthropic/claude-opus-5",
  "small_model": "anthropic/claude-haiku-4-5-20251001"
}
```

If opencode announces a model from some other vendor at startup, it is not using this provider at all and a stale key elsewhere is winning. Read what it prints before believing a test that passed.

## crush

Without `/v1`, and every model has to be spelled out: crush never calls `/v1/models`, so one absent from this file cannot be selected however well the gateway serves it. The Setup tab generates the whole stanza from the live model list.

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

The zero costs are deliberate. A subscription is not billed per token, and leaving real prices in makes crush display a running total that is fiction.

The context windows are a best guess per model. They affect what crush displays and when it truncates, never what the gateway will serve.

## curl

For checking the plumbing without a client in the way. Both APIs accept either credential header.

Anthropic Messages:

```sh
curl https://claudication.example.com/v1/messages \
  -H "x-api-key: clc_…" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-haiku-4-5-20251001","max_tokens":64,
       "messages":[{"role":"user","content":"hello"}]}'
```

OpenAI Responses:

```sh
curl -N https://claudication.example.com/v1/responses \
  -H "authorization: Bearer clc_…" \
  -H "content-type: application/json" \
  -d '{"model":"claude-opus-5","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'
```

Every model this gateway can serve:

```sh
curl https://claudication.example.com/v1/models \
  -H "x-api-key: clc_…"
```

## From the shell

On the machine running the gateway. Shell access to the state directory is
already the higher privilege, so none of these asks for the admin password.

| Command | What it does |
|---|---|
| `claudication keys add -name NAME` | Mint an API key. The same thing the API keys tab does, for a provisioning script. |
| `claudication passwd` | Reset the admin password. The recovery path when it is lost — no old password needed. |
| `claudication login-url` | A single-use link that signs a browser in without typing the password. Spent the first time it is used. |
| `claudication backup FILE` | A consistent snapshot without stopping the service. Holds the sealing key and every stored token, so it is exactly as sensitive as the state directory. |

## When it does not work

| What you see | What it usually is |
|---|---|
| 404 on every request | Almost always the `/v1` question. Claude Code and crush want the base URL without it; opencode and Codex want it with. Neither says so when it is wrong. |
| 404 saying the API is turned off | That surface is switched off under Settings → API surfaces. The message names which one. |
| 401 | The key is wrong, revoked, or in a header this client does not send. Either `x-api-key` or a bearer token works, on either API. |
| 429 whose message is the single word "Error" | Not a rate limit. The subscription backend refuses opus and sonnet to anything that is not Claude Code, and says so misleadingly. Leave `passthrough.claude-code-attribution` on and it is handled for you. |
| "Third-party apps now draw from your extra usage" | Also not a billing message. A content check refused the request. The Usage tab labels these; `scripts/bisect-refusal.py` finds the trigger. |
| Only haiku works; opus and sonnet 429 | The same attribution gate, seen from the other side. |
| A model works in one client and not another | The gateway does not filter models, so this is the client: crush needs every model listed in its own config, and Codex needs one in its catalog. The others discover them. |
| Codex: "stream closed before response.completed" | The gateway sends a terminal event on every path, including failures — so this points at something between it and Codex, usually a proxy buffering the stream. |

<!-- END GENERATED -->

## A note on what is verified

All four stanzas have been run. The opencode, crush and Claude Code ones are in
use on this network; the Codex one was checked by installing Codex CLI 0.154.0
and pointing it at the gateway, which held multi-turn sessions with real shell
tool calls against a live subscription on both Sonnet 5 and Opus 5. The `bwrap`
and model-catalog notes above come from those runs. Fable was checked directly
against both APIs.
