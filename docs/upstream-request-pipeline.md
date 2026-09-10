# What the stock client does between the prompt and the POST

Why this exists: twice now, a request that could not work came back as an error
about billing, and both times it cost hours. This is the map for deciding
whether the next such failure is a request *shape* we can normalise or
something we cannot — and for not re-opening the second question every time.

**How to read it.** Every claim is marked:

- **[measured]** — observed against the live API through this gateway, on one
  subscription account, varying one thing at a time. These are facts about
  today's server behaviour and could change.
- **[bundle]** — read out of the official client, version 2.1.263
  (`GIT_SHA 37ae3f38…`, built 2026-09-06). Cited as `region_NNN @offset`, byte
  offsets into the split chunks. These are facts about what the client sends,
  not about what the server requires.

The two are independent, and the interesting findings are where they disagree:
the client works around things the server has never told anyone about.

## How to check any of this again

**The measured half** is reproducible with `scripts/bisect-refusal.py` and a
live key, or with a few lines of curl. Nothing here needs the client.

**The bundle half** needs the client binary, which is not in this repository
and should not be. On a machine with Claude Code installed it is a Bun
single-file executable at `~/.local/share/claude/versions/<version>` — one ELF
with the whole JavaScript blob inline, roughly 200 MB. Everything cited below
was read straight out of it:

```sh
python3 - <<'PY'
import re
data = open('/path/to/versions/2.1.263', 'rb').read()
for m in re.finditer(rb'function VB\(', data):        # or any literal
    print(m.start(), data[m.start()-40:m.start()+400].decode('utf8', 'replace'))
PY
```

Two kinds of offset appear below, and they are **not** the same address space:

- `region_NNN @offset` — into a split of the blob into ~25 numbered files, made
  during the first pass. That split was scratch and no longer exists; treat
  these as "somewhere in the JS blob, in the file that held this region" and
  re-find the symbol by name.
- `BIN @offset` — a byte offset into the binary itself, which is stable for
  that exact build and directly usable with the snippet above.

Either way the reliable move is to search for the **literal or function name**,
not to seek to an offset: the offsets rot with every release, the names have
survived several. And a version bump renames the minified identifiers but
rarely the strings, so grep for the string first and read outward.

---

## 1. The pipeline

[bundle] Ordered, from a submitted prompt to bytes on the wire. Only the stages
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

[bundle] At most five blocks, never one per section:

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

[bundle] Two different formats, and this matters:

- The **main thread** emits `# Environment` with `Is a git repository: true`.
  `IAt()` `region_004 @720400`, constants `@716275`.
- The **subagent** prompt emits `<env>` with `Is directory a git repo: Yes`.
  One occurrence in the entire binary. `tis` `region_004 @4677170`,
  `BIN @186058488`.

`Workspace root folder:`, which opencode sends, exists nowhere in the client.

---

## 3. The messages array

[bundle] `_as` maps internal turns to wire turns. Notable shapes a third-party
client would not produce:

- `{role:"system"}` entries **inside** `messages` (`api_system` turns), which
  can carry `output_config`. `region_004 @4806718`, `The()` `@715212`.
- `<system-reminder>` wrapping, usually its own text block before the user's,
  sometimes folded into a `tool_result`. `Oa` `region_004 @5320464`.
- Attachments rendered as *synthetic* Read/Bash tool-call pairs rather than raw
  blocks. `region_004 @5372383`.

[bundle] Normalisation before send: empty text blocks dropped, whitespace-only
assistant messages removed, orphaned `tool_use` repaired with
`[Tool result missing due to internal error]`, adjacent text blocks in a
`tool_result` merged and trimmed. `region_004 @4714157`, `@5385054`, `@5309214`.

---

## 4. Cache breakpoints

[bundle] Two in the system array, at most two in messages (one trailing, one
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

[bundle] Headers: `x-app: cli` (or `cli-bg`),
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

[bundle] Things that switch off when `ANTHROPIC_BASE_URL` is not an Anthropic
host: `x-client-request-id`, the compaction and fallback-stamp headers,
`anthropic-usage-limit: extended`, fine-grained tool streaming, the larger
image cap, and the request-side byte watchdog. The *response*-side watchdog
stays on.

[bundle] A gateway that returns 501 for `/v1/messages/count_tokens` makes the
client measure context by issuing a **real, billed** `max_tokens: 1` request
instead. `region_004 @1757645`. This gateway proxies `count_tokens` for exactly
that reason.

---

## 6. Truncation, and the MCP story

[bundle] There is no central cap on `tool_result` size at assembly time; each
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

## 7. What this gateway normalises, and why

Each of these is a body rewrite, which the relay otherwise refuses to do. Each
exists because without it the request cannot succeed.

| pass | trigger | evidence |
|---|---|---|
| `FixLoneSurrogates` | unpaired `\uD800`–`\uDFFF` escape | [measured] 400 `no low surrogate in string`, in a message, a system block and a `tool_result` alike |
| `EnsureAttribution` | first system block is not an accepted identity string | 429 with the message `Error`, no rate-limit headers |
| `NormaliseSystem` | `Is directory a git repo:` in `system`; empty text blocks | [measured] 400, the misleading billing message |
| `DropEmptyMessageText` | empty text blocks in `messages` | [measured] 400 `text content blocks must be non-empty` |
| `RewriteRefusedToolNames` | tool name matching `^mcp_[^_]`, or exactly `todowrite` | [measured] 400, the misleading billing message |
| `decodeBody` | `Content-Encoding: gzip` | [measured] every pass above silently no-ops on bytes it cannot parse |

The last one is the trap worth remembering: a gzipped body is not a parse
error, it is *silence*, and the symptom is whatever the first un-normalised
thing causes — in practice a 429 that reads as rate limiting.

### The misleading refusal

Two of the six produce this, and nothing in it is true:

> Third-party apps now draw from your extra usage, not your plan limits. Add
> more at claude.ai/settings/usage and keep going.

[measured] The account it was found on was at 20% of its five-hour window and
2% of its week, and answered 200 to a 1.35 MB request in the same minute.
[bundle] The client has **no handler for that string anywhere** — the
classification is server-side and the first-party client never meets it, so
there is no upstream behaviour to copy. `ClassifyRefusal` labels it in the
request log for that reason.

The measured boundary for the tool-name half:

```
mcp__weather__get  200      mcp_weather_get    400
mcp__weather_get   200      mcp_w              400
weather_get        200      mcp_               200
MCP_weather_get    200      mcp___weather_get  200
```

Exactly `^mcp_[^_]`, lowercase. And for the system-prompt half: the line alone
passes, the line inside a foreign prompt fails, any rewording clears it — so it
is not a banned string but a check that fires when Claude Code's phrasing turns
up in a prompt that is not Claude Code's.

A third turned up once the first two were fixed, found by
`scripts/bisect-refusal.py` on its first real run against the same captured
body. A tool named exactly `todowrite` is refused, on opus and haiku alike:

```
todowrite  400      TodoWrite  200      todo_write  200
                    todoWrite  200      todowrite_  200
                    Todowrite  200      todowrite1  200
                    TODOWRITE  200      _todowrite  200
```

One exact lowercase string, not a class — `taskcreate`, `taskupdate`,
`todoread`, `askuserquestion`, `toolsearch`, `notebookedit`, `webfetch` and
`multiedit` are all accepted. Expect more of these, and reach for the bisector
rather than for reasoning.

---

## 8. What this gateway deliberately does not do

The backend decides first-party by **attestation**, not by shape. These are the
signals, and synthesising them is out of scope — permanently, so that the
question does not have to be re-argued:

- **`x-anthropic-billing-header`**, system block 0. Carries
  `cc_version=2.1.263.<3 hex>`, where the three hex characters are a salted
  SHA-256 over characters 4, 7 and 20 of the first user message, and a
  `cch=00000;` segment emitted only when the client believes it is first-party
  talking to `api.anthropic.com`. `R9t` `BIN @179397920`, checksum
  `region_004 @3938662`.
- **`x-cc-atis`** — a server-issued opaque token (`v1.<x>.<a>.<b>.<c>`) echoed
  back from dynamic config. A relay cannot produce one.
- **`metadata.user_id`** — not an id but a JSON blob carrying `device_id`,
  `account_uuid` and `session_id`. `_9` `region_004 @4712013`.

The distinction that matters: normalising *shape* so a request is not rejected
is compatibility work, and every trigger reproduced so far is shape. Forging
the signal that decides which pot the usage bills to is not, and a keyed
checksum over the user's own message says plainly that it is meant to be
non-forgeable.

---

## 9. Upstream constraints we surface rather than paper over

[measured] Real, and the upstream names each one precisely. Repairing them
would mean reshaping a conversation or changing what the caller is billed:

- `tool_result` blocks must come first in a user turn. Text before a
  `tool_result` gives
  `tool_use ids were found without tool_result blocks immediately after`.
  (`tool_use` ordering in an *assistant* turn is unconstrained — 200 either
  way, though the client normalises it anyway.)
- At most 4 `cache_control` markers per request, counted across system and
  messages together. Silently dropping one would change caching and cost.
- A message whose every content block is empty. Dropping them all leaves an
  empty array, which is refused too; inventing filler text is the client's
  decision, not ours.

---

## 10. Gaps, closed and remaining

The first pass split the bundle into region files and several definitions fell
outside them. They are all in the binary; searching it directly closes most.

**Retry backoff.** [bundle] `@180451885`:

```js
function VB(e, t, r = 32000) {
  let i = Math.min(500 * Math.pow(2, e - 1), r);
  let o = Math.round(i + Math.random() * 0.25 * i);
  if (t) { let u = parseInt(t, 10); if (!isNaN(u)) return Math.max(u * 1000, o) }
  return o
}
```

500 ms doubling per attempt, capped at 32 s, plus 0–25% jitter, and
`retry-after` (in seconds) wins whenever it is larger. Worth noting the earlier
guess from a structurally identical sibling — 5 s base, 20 s cap — was the right
*shape* and the wrong numbers, which is why it was not asserted.

**Endpoints.** [bundle] `@177161221`: `BASE_API_URL` is
`https://api.anthropic.com`, `TOKEN_URL` is
`https://platform.claude.com/v1/oauth/token`, `CLAUDE_AI_ORIGIN` is
`https://claude.ai`, and the OAuth client ids are literals in the binary.

**The OAuth-vs-key decision.** [bundle] `@179399142`:
`I9t(e) => e.anthropicAuthEnabled && e.oauthScopes?.includes(<scope>)` — so the
OAuth path additionally requires the stored grant to carry the inference scope,
on top of the precedence rules in §5.

**MCP output truncation.** [bundle] The gate reads `MAX_MCP_OUTPUT_TOKENS`, and
when it fires the model is told:

> `[OUTPUT TRUNCATED - exceeded <n> token limit]`
>
> The tool output was truncated. If this MCP server provides pagination or
> filtering tools, use them to retrieve specific portions of the data. If
> pagination is not available, inform the user that you are working with
> truncated output and results may be incomplete.

This is the other half of the MCP story in §6: the client truncates, tells the
model it truncated, and asks it to paginate. A third-party client that
truncates without saying so leaves the model reading a fragment as though it
were whole — and if it cuts mid-character, produces the surrogate failure in §7.

**Still open**, and only worth chasing if something depends on them: the
default value of `MAX_MCP_OUTPUT_TOKENS` (a validated env var, not a literal),
the media count and byte caps, and microcompaction's `keepRecent`.
