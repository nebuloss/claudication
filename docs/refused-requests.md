# Why a request gets refused, and what the relay does about it

The operational document: every way a request fails for a reason its error
message does not name, what this gateway rewrites to prevent it, and what it
deliberately leaves alone.

Every claim is marked:

- **[measured]** — observed against the live API through this gateway, on one
  subscription account, varying one thing at a time. Facts about today's server
  behaviour, which could change.
- **[client]** — read out of the official client, version 2.1.263. Facts about
  what the client sends, not about what the server requires.

The two are kept apart because the interesting cases are where they disagree:
the client works around things the server has never told anyone about.

If you are here because something just started failing, skip to
[How to find the next one](#how-to-find-the-next-one).

## What the relay rewrites, and why

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
[client] The client has **no handler for that string anywhere** — the
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

## What this gateway deliberately does not do

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

## Upstream constraints surfaced rather than papered over

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

## How to find the next one

Do not reason about it. None of the three content triggers was found by
reasoning; all three were found by bisecting a captured body, twice by hand at
a cost of hours each and once in eleven requests by the script that now does
it:

```sh
scripts/bisect-refusal.py --key clc_... --url http://127.0.0.1:8317 body.json
```

It confirms the failure reproduces, checks a trivial request still works so you
know it is the body and not the account, then narrows: which of `system`,
`tools` or `messages` carries it, delta-debugging within that, and for a single
tool whether it is the name or the schema.

Capture a body by pointing a client's base URL at a logging proxy. Every probe
asks for 16 output tokens and `--max-calls` bounds the run, but it does spend
real requests against a real account.
