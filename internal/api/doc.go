// Package api adapts the client-facing API dialects onto the one protocol the
// gateway speaks upstream.
//
// The gateway talks Anthropic Messages to Claude and nothing else, because
// that is what a subscription account answers. Everything a caller may speak
// is therefore an adapter around that shape: a Protocol reads a request in its
// own dialect and produces the Anthropic request to relay, then wraps the
// caller's writer so the answer comes back in the dialect it asked in.
//
// Anthropic itself is one of those adapters rather than a special case, and
// that is the whole point of the interface. Everything below this line — the
// relay, the account pool, retry and refusal handling, rate limiting, usage
// accounting, logging — never learns which dialect asked. Adding a surface
// means writing a Protocol and mounting a route; nothing else moves.
//
// # The layout
//
//	internal/api            this package: the contract, and the parts every
//	                        dialect would otherwise reimplement
//	internal/api/anthropic  the Anthropic Messages surface — the identity
//	                        adapter, which translates nothing
//	internal/api/openai     the OpenAI surface: /v1/responses today, which is
//	                        what Codex CLI speaks
//
// One package per surface, and a surface may own several routes — Routes()
// returns a list for exactly that reason. A dialect's own translation code
// lives inside its package rather than in a shared one, because the mapping
// decisions are only defensible next to the evidence for them, and that
// evidence is per dialect.
//
// # Adding a dialect
//
// Say DeepSeek, or any of the many clients that speak OpenAI chat completions
// rather than Responses. Chat completions is the same family as what
// internal/api/openai already serves, so that would be another route on the
// OpenAI surface; a genuinely different shape would be its own package. Either
// way the work is the same four pieces:
//
//  1. The request mapping: the caller's body to an Anthropic one. Anthropic
//     requires max_tokens and a strict user/assistant alternation, and those
//     two rules are what most mappings get wrong.
//  2. The answer mapping: three of them, because an upstream answer arrives in
//     three shapes. NewReshapingSink already sorts them out and calls back
//     into a Reshaper, so a new dialect writes the conversions and not the
//     plumbing.
//  3. The error envelope, so a client can parse a refusal. An error a client
//     cannot read is an error it cannot act on, and it will usually retry.
//  4. Registration in internal/httpapi, which is the composition root: it
//     builds the registry and mounts each surface behind its own switch.
//
// What a dialect must not do is synthesise attestation. Shape — tool names,
// block structure, phrasing — is ours to normalise. The billing header's keyed
// checksum, x-cc-atis and the metadata.user_id blob are deliberately not
// forged, and a new surface does not change that.
package api
