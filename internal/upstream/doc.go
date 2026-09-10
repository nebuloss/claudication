// Package upstream relays a request to Anthropic on behalf of a pooled
// subscription account.
//
// This doc comment is the record of what reverse-engineering the official
// client bought, so that nobody has to do it again. Claims marked [measured]
// were observed against the live API through this gateway, one variable at a
// time; claims marked [client] were read out of Claude Code 2.1.263. The two
// are kept apart because the interesting cases are where they disagree — the
// client works around things the server has never documented.
//
// The long form, with byte offsets into the bundle, is in
// docs/upstream-request-pipeline.md. Everything load-bearing is repeated here.
//
// # Read this before changing what goes on the wire
//
// The relay's rule is that the caller's bytes go upstream unchanged. There are
// five deliberate exceptions, and every one exists because without it the
// affected request cannot succeed at all. Each was found by narrowing a real
// failure until one character decided it. In the order Do applies them:
//
//  1. FixLoneSurrogates (surrogates.go) — an unpaired \uD800-\uDFFF escape.
//  2. EnsureAttribution (attribution.go) — Claude Code's identity block.
//  3. NormaliseSystem (systemtext.go) — one environment line, and empty blocks.
//  4. DropEmptyMessageText (systemtext.go) — empty blocks in messages.
//  5. RewriteRefusedToolNames (toolnames.go) — refused tool names, restored on
//     the response.
//
// Ordering matters in one place: the prologue is peeked from the body before
// any of this runs and holds the system array by reference, so a pass that
// rewrites system text must refresh it, or EnsureAttribution rebuilds the
// envelope from the bytes that were just replaced. That bug shipped once and
// was invisible except through the system path.
//
// # The three content checks, and the message that hides them
//
// [measured] These all answer with one message, and nothing in it is true:
//
//	Third-party apps now draw from your extra usage, not your plan limits.
//	Add more at claude.ai/settings/usage and keep going.
//
// It is a content check. Adding credit does not fix it, and the same key
// succeeds on the next request with slightly different content — which is what
// makes it look like a flaky quota problem. The account it was first seen on
// was at 20% of its five-hour window and 2% of its week, and served a 1.35 MB
// request in the same minute. It has cost two people hours each.
// ClassifyRefusal (errorkind.go) labels it in the request log for that reason.
//
// [client] Claude Code has no handler for that string anywhere in its bundle:
// the classification is server-side and the first-party client never meets it,
// so there is no upstream behaviour to copy.
//
// The three triggers found so far, all [measured]:
//
//	mcp__weather__get  200    mcp_weather_get    400    // exactly ^mcp_[^_]
//	mcp__weather_get   200    mcp_w              400
//	weather_get        200    mcp_               200
//	MCP_weather_get    200    mcp___weather_get  200
//
//	todowrite  400    TodoWrite  200    todo_write  200  // one exact string;
//	                  todoWrite  200    todowrite_  200  // taskcreate, todoread,
//	                  Todowrite  200    todowrite1  200  // webfetch, multiedit,
//	                  TODOWRITE  200    _todowrite  200  // notebookedit all 200
//
//	"Is directory a git repo:" inside a foreign system prompt   400
//	the same line alone, or in a user message                   200
//	the same line reworded, however slightly                    200
//
// That last one is the shape of all of them: not a banned string, but a check
// that fires when Claude Code's own phrasing turns up in a request that is not
// Claude Code's. [client] confirms the asymmetry — the main-thread prompt says
// "Is a git repository: true"; "Is directory a git repo:" appears exactly once
// in the whole binary, in the subagent prompt.
//
// Do not reason about the next one. Run scripts/bisect-refusal.py against a
// captured body: it narrows to the smallest failing part in a dozen requests,
// and found the todowrite trigger on its first run after two had already been
// bisected by hand. Reasoning found none of the three.
//
// # Other failures whose message names the wrong thing
//
// [measured] An unpaired surrogate is "The request body is not valid JSON: no
// low surrogate in string", in a user message, a system block and a
// tool_result alike; a well-formed pair is fine. [client] Claude Code
// sanitises the entire assembled body for these as the last thing before
// POSTing, which is the clearest possible statement that it matters.
//
// [measured] Empty text blocks are "text content blocks must be non-empty", in
// system and in messages, even with a good block beside them. An empty
// tool_result is accepted. [client] It filters falsy prompt entries at four
// separate levels and cannot emit one.
//
// [measured] A gzipped request body is the trap with no error of its own: it
// parses as nothing, so every pass above silently no-ops, and the symptom is
// whatever the first un-normalised thing causes — in practice the attribution
// gate's 429 "Error", which reads as rate limiting. [client] It gzips bodies
// over about 4096 characters when its feature gate is on. Handled on ingest in
// httpapi.decodeBody, bounded against the configured body limit because the
// inbound reader caps the compressed stream only.
//
// [measured] The attribution gate itself answers 429 with the message "Error",
// no rate-limit headers, x-should-retry true. Also reads as quota exhaustion,
// also is not.
//
// # What the client puts on the wire that we do not
//
// [client] The system array is at most five blocks, never one per section:
//
//	0  x-anthropic-billing-header: ...        no cache_control
//	1  one of three exact identity strings    no cache_control
//	2  "# Reporting outcomes..." (some models)
//	3  every static section, joined           ephemeral, scope global
//	4  every dynamic section, joined          ephemeral
//
// [client] Headers: x-app: cli (or cli-bg), User-Agent
// claude-cli/<version> (external, cli), X-Claude-Code-Session-Id,
// x-client-request-id, sometimes traceparent. OAuth scope defaults to
// user:inference. Every inference call goes through the beta namespace, hence
// ?beta=true on the path.
//
// [client] Body fields beyond the public API: context_management, safeguards,
// output_config, speed, thread, diagnostics, fallbacks,
// fallback_credit_token, and a top-level cache_control with
// evict_on_complete. It can also put {role:"system"} turns inside messages.
//
// [client] Response watchdogs, which is why relay flushes every chunk: a
// byte-level idle timeout of 180 s direct or 300 s through a custom base URL
// (clamped to [10 s, 30 min]), an event-level idle timeout of 300 s or more, a
// stall ladder at 15/30/60/120 s, and a synthesised ping when bytes are moving
// but no event has surfaced for 10 s.
//
// [client] Retries: ten by default, fifteen with CLAUDE_CODE_MAX_RETRIES, three
// hundred under its retry watchdog; backoff 500 ms doubling to 32 s with 0-25%
// jitter; retry-after honoured and winning when larger; x-should-retry: false
// terminal regardless of status.
//
// [client] Truncation: Bash output 30000 characters (BASH_MAX_OUTPUT_LENGTH,
// hard cap 150000), Read 25000 tokens, MCP results gated by
// MAX_MCP_OUTPUT_TOKENS and written to a file the model is told to page
// through. Token budgeting is local — last reported usage plus chars/4 — not a
// count_tokens call. It only calls count_tokens for its own /context display,
// and if a gateway answers 501 there it measures context with a real billed
// max_tokens:1 request instead. That is why httpapi proxies the route.
//
// # What is not ours to fix
//
// The backend decides first-party by attestation, not by request shape:
//
//   - x-anthropic-billing-header as system block 0, carrying
//     cc_version=<version>.<3 hex> where the hex is a salted SHA-256 over
//     characters 4, 7 and 20 of the first user message, plus a cch=00000
//     segment emitted only when the client believes it is first-party.
//   - x-cc-atis, a server-issued opaque token (v1.<x>.<a>.<b>.<c>) echoed back
//     from dynamic config. A relay cannot produce one.
//   - metadata.user_id, which is not an id but a JSON blob carrying device_id,
//     account_uuid and session_id.
//
// Normalising shape so a request is not rejected is compatibility work. Every
// trigger reproduced so far is shape. Synthesising the signal that decides
// which pot the usage bills to is not, and a keyed checksum over the user's
// own message says plainly that it is meant to be non-forgeable. This is
// settled; it does not need re-arguing.
//
// # Upstream constraints deliberately left alone
//
// [measured] Each is named precisely by the upstream, and papering over it
// would mean reshaping a conversation or changing what the caller is billed:
//
//   - tool_result blocks must come first in a user turn, or
//     "tool_use ids were found without tool_result blocks immediately after".
//     (tool_use ordering in an assistant turn is unconstrained — 200 either
//     way — though the client normalises it anyway.)
//   - At most 4 cache_control markers per request, counted across system and
//     messages together: "A maximum of 4 blocks with cache_control may be
//     provided." The client's 2 + 2 is exactly that budget.
//   - A message whose every content block is empty. Dropping them all leaves
//     an empty array, refused too; inventing filler text is the client's
//     decision, not ours.
//
// [measured] And one thing that turns out not to be a constraint at all:
// system block count. 1, 5, 20 and 60 blocks are all accepted, so the client
// keeping to five is its own housekeeping.
package upstream
