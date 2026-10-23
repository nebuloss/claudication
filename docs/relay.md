# What the relay changes, and what it must not break

The relay sends a caller's bytes upstream unchanged, with a short list of
exceptions, each because the request cannot otherwise succeed.
[refused-requests.md](refused-requests.md) has the measurements behind each.

### The attribution block

`CLAUDICATION_CLAUDE_CODE_ATTRIBUTION` (`passthrough.claude-code-attribution`,
default **on**) is the one setting that changes what is sent upstream.

Anthropic's subscription backend gates opus, sonnet and fable on Claude Code's
attribution block arriving as the *first* system block, and refuses anything else
as a `429 rate_limit_error` with the message `"Error"` and no rate-limit
headers — which reads exactly like quota exhaustion and is nothing of the sort.
With this on, a client that did not send that block has it prepended, so it
reaches the models the subscription pays for. A body that already leads with an
accepted block is never rewritten.

Set it `false` for strict byte-for-byte passthrough, and accept that
non-Claude-Code clients then get haiku and nothing above it.

### Other requests the backend refuses on content

Attribution is not the only one. A tool named `mcp_x` rather than `mcp__x`, and
Claude Code's own `Is directory a git repo:` line inside a system prompt that
is not Claude Code's, are both refused — with a message about billing that
mentions neither tool names nor prompts:

> Third-party apps now draw from your extra usage, not your plan limits.

It is not a quota problem, and adding credit does not fix it. The relay
rewrites both on the way out, restores the tool names on the way back, and
labels the refusal in the request log if a third trigger ever turns up.

Unpaired UTF-16 surrogates (a tool cutting its output mid-emoji), empty text
blocks, and gzipped request bodies are handled in the same place and for the
same reason: without it those requests cannot succeed at all.

[`refused-requests.md`](refused-requests.md) has the measurements
behind each of these and the signals this gateway deliberately does not
synthesise; [the other documents](README.md) also covers what the official client sends and how
any of it can be checked again. The short version is in the code, as
`go doc ./internal/relay/passes`.

If a request starts failing for no visible reason, do not reason about it —
none of the three known triggers was found that way. Point
[`scripts/bisect-refusal.py`](../scripts/bisect-refusal.py) at a captured body and
a key, and it narrows to the smallest failing part in about a dozen requests:

```sh
scripts/bisect-refusal.py --key clc_... --url http://127.0.0.1:8317 body.json
```

## What the relay must not break

Anthropic publishes a [gateway compatibility
contract](https://code.claude.com/docs/en/llm-gateway-protocol) describing what
Claude Code sends a proxy and what a proxy must forward untouched. Three rules
matter most, and each is held by a test that quotes the clause it enforces:

1. **Forward error bodies unmodified.** Claude Code decides whether to retry, and
   whether to permanently disable a capability, by substring-matching the
   upstream's error prose. Rewrap an error and graceful degradation stops
   working.
2. **Relay every byte, including pings.** A byte-level watchdog aborts a stream
   silent for 300 seconds, and during long thinking pauses SSE `ping` events are
   the only traffic. Buffering a complete response before relaying stalls the
   client outright.
3. **Do not reshape the `system` array.** The attribution strip is positional:
   reorder the array, merge the block, or flatten it to a string and the block
   lands in the model's prompt and the cache key.

The relay follows from those: it never parses what it does not need to, which is
what keeps it working with capabilities that do not exist yet. Headers and body
fields are forwarded as open lists rather than allowlists, because a gateway
pinned to an observed list strips the next capability's field and breaks it on
the release that introduces it.
