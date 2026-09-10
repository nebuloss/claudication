# What the stock client does between the prompt and the POST

A reference for the client side: what Claude Code assembles and sends, so that
judging whether a third-party client's request is unusual does not need the
client reverse-engineered again.

For what the *gateway* does about refusals, see
[refused-requests.md](refused-requests.md). For how any of this was learned and
how to re-check it, see [reversing.md](reversing.md).

Every claim is marked:

- **[measured]** — observed against the live API through this gateway, on one
  subscription account, varying one thing at a time. Facts about today's server
  behaviour, which could change.
- **[client]** — read out of the official client, version 2.1.263. Facts about
  what the client sends, not about what the server requires.

The two are kept apart because the interesting cases are where they disagree:
the client works around things the server has never told anyone about.

## 1. The pipeline

[client] Ordered, from a submitted prompt to bytes on the wire. Only the stages
that can change what the server sees are listed.

1. **Auto-compaction.** Threshold is `effectiveWindow − 13000`; a "warn" band
   at `−33000`; a hard block at `trueWindow − outputReserve − 3000` where the
   client refuses to send at all. A background "precompute" summary is armed at
   80% of the window. `region_004 @2453740`, `@4054900`.
2. **Microcompaction.** Replaces old tool results with
   `[Old tool result content cleared]`, but only if that frees ≥20000 tokens.
   Its sole trigger is a server "context hint", which is disabled through a
   gateway. `region_004 @4064601`.
3. **Message flattening**, tool-use/tool-result pairing repair, media caps.
   `Zar` → `yw`, `region_004 @4722766`.
4. **Block reordering.** `tool_use` blocks moved to the end of assistant
   content; `tool_result` blocks moved to the front of user content.
   `region_004 @5306950`, `@5308874`.
5. **System array assembly** and its cache breakpoints. `Sas`/`kkt`,
   `region_004 @4808014`, `@4689919`.
6. **Message array assembly** and its cache breakpoints. `_as`/`xar`,
   `region_004 @4806718`, `@4702575`.
7. **Body literal.** `region_004 @4743300`.
8. **Lone-surrogate sanitisation over the whole body.** The last thing before
   the POST. `region_004 @4743900`, telemetry `tengu_lone_surrogate_sanitized`.
9. **Headers, optional gzip, dispatch.** `region_004 @1749731`.

Retries can re-enter the pipeline: a retry may microcompact, strip thinking
blocks, drop `safeguards`, strip an offending media block, or drop the
`api_system` turn, each at most once.

---

## 2. The system array

[client] At most five blocks, never one per section:

| # | content | `cache_control` |
|---|---|---|
| 0 | `x-anthropic-billing-header: …` | none |
| 1 | one of three exact identity strings | none |
| 2 | `# Reporting outcomes…` (some models only) | none |
| 3 | every static section, joined | `{"type":"ephemeral","scope":"global"}` |
| 4 | every dynamic section, joined | `{"type":"ephemeral"}` |

The split at 3/4 is made by a sentinel, `__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__`,
which is removed before the wire. `region_000 @62184`.

The identity string is one of exactly three, and block 0 is recognised by its
`x-anthropic-billing-header:` prefix — so the server's "first block" check
almost certainly skips known preamble blocks rather than looking at index 0.
`DVt`/`PVt` `BIN @180696533`, `qAe` `BIN @180696661`.

[measured] **Block count is not constrained.** 1, 5, 20 and 60 system blocks
all return 200. The client keeping to five is its own housekeeping.

[measured] We cannot test the identity-block *position* from outside: this
gateway injects the attribution block at index 0 whenever the first block is
not already an accepted one, so all variants arrive normalised. The note in
`attribution.go` that position matters is from an earlier direct observation
and is neither confirmed nor refuted here.

### The environment block

[client] Two different formats, and this matters:

- The **main thread** emits `# Environment` with `Is a git repository: true`.
  `IAt()` `region_004 @720400`, constants `@716275`.
- The **subagent** prompt emits `<env>` with `Is directory a git repo: Yes`.
  One occurrence in the entire binary. `tis` `region_004 @4677170`,
  `BIN @186058488`.

`Workspace root folder:`, which opencode sends, exists nowhere in the client.

---

## 3. The messages array

[client] `_as` maps internal turns to wire turns. Notable shapes a third-party
client would not produce:

- `{role:"system"}` entries **inside** `messages` (`api_system` turns), which
  can carry `output_config`. `region_004 @4806718`, `The()` `@715212`.
- `<system-reminder>` wrapping, usually its own text block before the user's,
  sometimes folded into a `tool_result`. `Oa` `region_004 @5320464`.
- Attachments rendered as *synthetic* Read/Bash tool-call pairs rather than raw
  blocks. `region_004 @5372383`.

[client] Normalisation before send: empty text blocks dropped, whitespace-only
assistant messages removed, orphaned `tool_use` repaired with
`[Tool result missing due to internal error]`, adjacent text blocks in a
`tool_result` merged and trimmed. `region_004 @4714157`, `@5385054`, `@5309214`.

---

## 4. Cache breakpoints

[client] Two in the system array, at most two in messages (one trailing, one
optional fork pin), none on tools, and the message pin is disabled whenever a
top-level `cache_control` is sent. There is no counter anywhere — the ceiling
is held structurally. `xar` `region_004 @4702575`.

[measured] The ceiling is real and it is global:

```
4 markers                     200
5 markers                     400  A maximum of 4 blocks with cache_control may be provided. Found 5.
4 in system + 2 in messages   400  … Found 6.
```

So the client's 2 + 2 is exactly the budget.

---

## 5. The send path

[client] Headers: `x-app: cli` (or `cli-bg`),
`User-Agent: claude-cli/2.1.263 (external, cli)`, `X-Claude-Code-Session-Id`,
`x-client-request-id`, optional `traceparent`, and `anthropic-version`.
Bodies over 4096 characters may be gzipped. `region_004 @1727215`, `@1749731`.

OAuth scope defaults to `["user:inference"]`. An `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN` or `apiKeyHelper` disables the OAuth path entirely;
`apiKey` and `authToken` are mutually exclusive. `region_001 @838575`.

Retries: default 10, `CLAUDE_CODE_MAX_RETRIES` clamped to 15, 300 under
`CLAUDE_CODE_RETRY_WATCHDOG`. `retry-after` and `x-should-retry` are honoured,
and `x-should-retry: false` is terminal regardless of status.
`region_004 @2437756`, `@2450849`.

Timeouts that concern a gateway:

| watchdog | direct first-party | through a custom base URL |
|---|---|---|
| byte-level idle on the response | 180 s | **300 s** |
| SSE event idle | ≥300 s | ≥300 s |
| first byte | that, plus 1 s per 32 KB of request body | same |

`Rno` `region_004 @1744092`, `pae` `@1743969`, `Lno` `@1744873`. **This is why
the relay flushes after every chunk**: during a long generation the upstream's
pings are the only traffic, and anything that batches them kills the request.

[client] Things that switch off when `ANTHROPIC_BASE_URL` is not an Anthropic
host: `x-client-request-id`, the compaction and fallback-stamp headers,
`anthropic-usage-limit: extended`, fine-grained tool streaming, the larger
image cap, and the request-side byte watchdog. The *response*-side watchdog
stays on.

[client] A gateway that returns 501 for `/v1/messages/count_tokens` makes the
client measure context by issuing a **real, billed** `max_tokens: 1` request
instead. `region_004 @1757645`. This gateway proxies `count_tokens` for exactly
that reason.

---

## 6. Truncation, and the MCP story

[client] There is no central cap on `tool_result` size at assembly time; each
tool truncates its own output.

- Bash: 30000 characters by default, `BASH_MAX_OUTPUT_LENGTH`, hard cap 150000.
  Marker `... [N lines truncated] ...`. `region_004 @686175`.
- Read: 25000 tokens, `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS`.
- MCP: gated by `MAX_MCP_OUTPUT_TOKENS`. Over the limit, the result is
  **written to a file** and replaced with a message telling the model to read
  it in chunks of about 80000 characters. `qgt` `region_004 @2687006`.

Token budgeting is local, not a `count_tokens` call: last reported usage plus
`chars/4` for everything appended since, images and documents charged a flat
2000 each. `region_004 @2463318`.

---
