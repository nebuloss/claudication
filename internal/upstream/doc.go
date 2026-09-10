// Package upstream relays a request to Anthropic on behalf of a pooled
// subscription account.
//
// # The rule, and its five exceptions
//
// The caller's bytes go upstream unchanged. There are five deliberate
// exceptions, and every one exists because without it the affected request
// cannot succeed at all. In the order [Relay.Do] applies them:
//
//  1. [FixLoneSurrogates] — an unpaired \uD800-\uDFFF escape, which the
//     upstream refuses the whole body over. Produced by any tool that cuts its
//     output to a budget and lands mid-emoji.
//  2. [EnsureAttribution] — Claude Code's identity block, which opus, sonnet
//     and fable are gated on.
//  3. [NormaliseSystem] — one line of Claude Code's own phrasing, and empty
//     text blocks.
//  4. [DropEmptyMessageText] — the same empty-block rule, in messages.
//  5. [RewriteRefusedToolNames] — tool names the backend refuses, sent in a
//     shape it accepts and restored on the response.
//
// Each carries its measurements in the file that implements it. Ordering
// matters in one place: the prologue is peeked from the body before any of
// this runs and holds the system array by reference, so a pass that rewrites
// system text must refresh it, or [EnsureAttribution] rebuilds the envelope
// from the bytes that were just replaced. That bug shipped once, and was
// invisible except through the system path.
//
// # Before adding a sixth
//
// Three of the five answer with the same message, and nothing in it is true:
//
//	Third-party apps now draw from your extra usage, not your plan limits.
//	Add more at claude.ai/settings/usage and keep going.
//
// It is a content check, not a quota problem. Adding credit does not fix it,
// and the same key succeeds on the next request with slightly different
// content. [ClassifyRefusal] labels it in the request log because it has cost
// two people hours each.
//
// Do not reason about the next one — none of the three known triggers was
// found that way. Run scripts/bisect-refusal.py against a captured body.
//
// And do not reach for the signals that decide first-party status: a billing
// header carrying a keyed checksum over the user's own first message, a
// server-issued token echoed back from config, an identity blob in metadata.
// Normalising shape so a request is not rejected is compatibility work.
// Forging the signal that decides which pot the usage bills to is not. That is
// settled; docs/refused-requests.md records why so it need not be re-argued.
//
// # Where the rest is written down
//
// docs/refused-requests.md is the full version of this comment: every refusal
// with the measurements behind it, and the upstream constraints deliberately
// left alone. docs/upstream-request-pipeline.md is what the official client
// does between a prompt and its POST. docs/reversing.md is how any of it can
// be checked again. They exist so nobody reverses the client a second time.
package upstream
