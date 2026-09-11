# claudication docs

Five documents, and one rule that matters more than any of them.

**The rule:** when a request starts failing for a reason its error message does
not name, do not reason about it. Bisect it. None of the three content triggers
found so far was found by reasoning; two cost hours each by hand, and the third
took eleven requests once the script existed.

```sh
scripts/bisect-refusal.py --key clc_... --url http://127.0.0.1:8317 body.json
```

The other live probe checks the OpenAI surface the way Codex judges it —
whether the stream opens, ends and carries usage the way Codex requires:

```sh
scripts/probe-codex.py --key clc_... --url http://127.0.0.1:8317 --roundtrip
```

`scripts/codex-model-catalog.py` is the other Codex-side script: it writes the
model catalog that stops Codex falling back to conservative context limits for
a Claude model.

## The documents

| | |
|---|---|
| [refused-requests.md](refused-requests.md) | **Start here.** Every way a request is refused for a reason it does not name, what the relay rewrites to prevent it, what it deliberately leaves alone, and how to find the next one. |
| [upstream-request-pipeline.md](upstream-request-pipeline.md) | What the official client assembles and sends between a prompt and its POST — system array, messages, cache breakpoints, headers, timeouts, truncation. The reference for judging whether a third-party client's request is unusual. |
| [reversing.md](reversing.md) | How all of it was obtained, how to check any single claim again, and what could not be determined. |
| [clients.md](clients.md) | **Setting one up.** Working configuration for Claude Code, opencode, crush and Codex CLI, and what each failure mode actually means. |
| [client-apis.md](client-apis.md) | The APIs clients speak to the gateway — Anthropic Messages and OpenAI Responses — how the two are switched on and off, and how to add a third. |

The same record as `refused-requests.md`, condensed, is in the code:
`go doc ./internal/upstream`. That is the copy a maintainer meets first, so if
you correct something here, correct it there too.

## How claims are marked

- **[measured]** — observed against the live API through this gateway, one
  variable at a time. Facts about today's server behaviour, which can change.
- **[client]** — read out of the official client, version 2.1.263. Facts about
  what that client sends, not about what the server requires.

They are kept apart because the interesting cases are where they disagree: the
client works around things the server has never told anyone about, and those
workarounds are the best available evidence of what the server does.
