# Pointing a client at the gateway

Ready-made configuration lives in [`configs/clients/`](../configs/clients/).
Each file is complete and commented; change the one address in it and it works.

| Client | File | `/v1` on the base URL? |
|---|---|---|
| Claude Code | [`claude-code.sh`](../configs/clients/claude-code.sh) | no |
| Codex CLI | [`codex.toml`](../configs/clients/codex.toml) + [`codex-models.json`](../configs/clients/codex-models.json) | yes |
| opencode | [`opencode.jsonc`](../configs/clients/opencode.jsonc) | yes |
| crush | [`crush.json`](../configs/clients/crush.json) | no |

That last column is the thing that trips everyone up: each client has its own
opinion about whether the base URL carries `/v1`, and none of them says so when
you get it wrong. You just get 404s.

The **Setup** tab in the admin UI offers the same files with your gateway's own
address already substituted, and a Download button on each — which matters more
than it sounds, because a browser will not give a page the clipboard over plain
HTTP, and a gateway on a private network usually is plain HTTP.

## The key

Mint one in **Settings → API keys**, or from a shell:

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

## Which base URL

The admin UI shows it under **Setup**. Two cases:

- **One listener** (`admin-listen` empty) — the address your browser is on is
  also the relay. The UI uses it directly.
- **Split listeners** — the browser is on the *admin* address and the relay is
  somewhere else. The gateway cannot learn the hostname clients use, so set
  `public-url` in the config or the UI will say it is guessing.

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

Two clients need models named by hand, and both are the client's doing: crush
never calls `/v1/models`, and Codex looks names up in a catalog. The files in
`configs/clients/` list the models that existed when they were written; the
Setup tab shows the live list beside them.

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
model up in a catalog compiled into its own binary, and a Claude name is not in
it:

```
warning: Model metadata for `claude-sonnet-5` not found. Defaulting to
fallback metadata; this can degrade performance and cause issues.
```

The fallback is conservative, so a million-token model gets auto-compacted as
though it were far smaller and long sessions shed context early.
`model_context_window` in config.toml does **not** fix it — the lookup is by
name.

One thing to know about the shipped catalog, measured rather than assumed:
Codex refuses an entry carrying neither `base_instructions` nor
`model_messages.instructions_template`, so every catalog must contain a system
prompt. The one in `codex-models.json` is a short stand-in, because Codex's own
lives inside its binary and nothing here can read it. Measured on the same
task, that is 10,148 tokens a turn against 19,346 with Codex's real prompt —
cheaper, and less good at editing. To use the real one:

```sh
scripts/codex-model-catalog.py --codex $(which codex)
```

It clones a real entry per model out of the binary you are running. Re-run it
after upgrading Codex.

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
  -d '{"model":"claude-opus-5","stream":true,
       "input":[{"type":"message","role":"user",
                 "content":[{"type":"input_text","text":"say hi"}]}]}'
```

A real Codex request, replayed and judged the way Codex judges it:

```sh
scripts/probe-codex.py --key clc_… --url https://claudication.example.com --roundtrip
```

## From the shell

On the machine running the gateway. Shell access to the state directory is
already the higher privilege, so none of these asks for the admin password.

| Command | What it does |
|---|---|
| `claudication keys add -name NAME` | Mint an API key, for a provisioning script. |
| `claudication passwd` | Reset the admin password. The recovery path when it is lost. |
| `claudication login-url` | A single-use link that signs a browser in. Spent on first use. |
| `claudication backup FILE` | A consistent snapshot without stopping the service. Holds the sealing key and every stored token, so it is as sensitive as the state directory. |

## When it does not work

| What you see | What it usually is |
|---|---|
| 404 on every request | The `/v1` question above. Add it or drop it. |
| 404 saying the API is turned off | That surface is switched off in **Settings → API surfaces**. |
| 401 | The key is wrong, revoked, or in a header this client does not send. Either header works, on either API. |
| 429 whose message is the single word `Error` | Not a rate limit. The attribution gate — see [refused-requests.md](refused-requests.md). Leave `passthrough.claude-code-attribution` on. |
| "Third-party apps now draw from your extra usage" | Not a billing message. A content check refused the request; bisect it with `scripts/bisect-refusal.py`. |
| Only haiku works, opus and sonnet 429 | Same attribution gate, from the other side. |
| A model works in one client and not another | The gateway does not filter models, so this is the client: crush needs it listed in `crush.json`, Codex in its catalog. The others discover them. |
| Codex: "stream closed before response.completed" | The gateway sends a terminal event on every path, so this points at something between it and Codex — usually a proxy buffering the stream. |

## A note on what is verified

All four configurations have been run. The opencode, crush and Claude Code ones
are in use on this network; the Codex one was checked by installing Codex CLI
0.154.0 and pointing it at the gateway, which held multi-turn sessions with
real shell tool calls against a live subscription on both Sonnet 5 and Opus 5 —
with the shipped `codex-models.json` and with a cloned one. Fable was checked
directly against both APIs.
