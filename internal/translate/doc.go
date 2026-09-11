// Package translate turns OpenAI-shaped requests into Anthropic ones, and the
// answers back again, so Codex CLI can run on a Claude subscription.
//
// # Why this is not in internal/upstream
//
// That package relays: the caller's bytes go upstream unchanged, and its five
// exceptions are each a few bytes rewritten under protest. This one does the
// opposite — it reads a request apart and builds a different one. Keeping them
// in separate packages keeps that rule honest, because "the bytes go through
// unchanged" and "the bytes are rebuilt in another protocol" cannot both be
// the law of one package.
//
// Correctness here is not "did the bytes survive" but "does the mapping mean
// the same thing", which needs different tests and a different kind of care.
//
// # What Codex actually sends
//
// Measured, not read from documentation: Codex CLI 0.154.0 pointed at a
// capture stub with a custom model_provider. testdata/codex-responses-request.json
// is that capture, sanitised.
//
//	POST /v1/responses
//	authorization: Bearer <the key from env_key>
//	accept: text/event-stream
//	originator: codex_exec
//
// Two things that shape everything:
//
//   - It speaks ONLY the Responses API. `chat_completions`, `ChatCompletions`
//     and `v1/chat` appear nowhere in the binary, while `/responses` appears
//     41 times. Serving /v1/chat/completions would help other OpenAI clients
//     and would do nothing at all for Codex.
//   - It always streams. `accept: text/event-stream`, `stream: true`. There is
//     no non-streaming path to get working first.
//
// The body of a bare "say hi" is 39 KB, because every turn carries the whole
// instruction set and tool list:
//
//	model                the value configured for the provider; ours to map
//	instructions  17.5KB  the system prompt
//	input                 the turns, as content blocks, roles user/developer/assistant
//	tools         17.5KB  nine of them, in three shapes (see below)
//	tool_choice           "auto"
//	parallel_tool_calls   true
//	reasoning             {"summary":"auto"}
//	include               ["reasoning.encrypted_content"]
//	store / stream        false / true
//	prompt_cache_key      a uuid
//	client_metadata       ids and workspace facts
//
// # The three tool shapes
//
//   - "function" — name, description, strict, parameters. Maps to an Anthropic
//     tool directly: parameters becomes input_schema.
//   - "namespace" — a container holding nested function tools, used for
//     multi_agent_v1. Anthropic has no such thing, so these have to be
//     flattened, and the flattened names have to stay clear of the names the
//     upstream refuses (see upstream.RewriteRefusedToolNames).
//   - "web_search" — a server-side tool with no schema at all:
//     {"type":"web_search","external_web_access":true}. Either mapped to
//     Anthropic's own server tool or dropped; it cannot become a function.
//
// # Things that will bite
//
//   - The request we send is synthesised here, so it is ours to get right:
//     it needs Claude Code's attribution block first in the system array or
//     the subscription backend serves haiku and nothing above it.
//   - `developer` is a role Anthropic does not have.
//   - `reasoning` and Anthropic's `thinking` are not the same feature.
//   - Codex reads usage off the final event; a stream that omits it reports
//     zero tokens rather than failing, which is the kind of wrong that goes
//     unnoticed.
package translate
