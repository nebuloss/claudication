// Package openai serves the OpenAI API to clients, turning their requests into
// Anthropic ones and the answers back again, so Codex CLI can run on a Claude
// subscription.
//
// It implements api.Protocol; see internal/api for how the surfaces fit
// together and what a new one has to provide. openai.go is the adapter and the
// rest of the package is the mapping: request.go out, stream.go and
// complete.go back.
//
// # Why the mapping lives here and not in internal/upstream
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
// The evidence for every decision below was measured, and it is kept in this
// file so that the next person to change the mapping does not have to reverse
// Codex again to find out why it is the way it is.
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
//     multi_agent_v1. Anthropic has no such thing, so these are flattened on
//     the way out and their namespace restored on the way back; see below.
//     Flattened names also have to stay clear of the ones the upstream
//     refuses (see upstream.RewriteRefusedToolNames).
//   - "web_search" — a server-side tool with no schema at all:
//     {"type":"web_search","external_web_access":true}. Either mapped to
//     Anthropic's own server tool or dropped; it cannot become a function.
//
// # What the answer has to look like
//
// From Codex's own source at rust-v0.154.0 — codex-rs/codex-api/src/sse/responses.rs
// and codex-rs/protocol/src/models.rs — rather than from guesswork, which is
// what the first capture stub used.
//
// Codex ignores most of the Responses vocabulary. These are explicitly
// discarded with a trace line: content_part.added, content_part.done,
// function_call_arguments.delta, function_call_arguments.done,
// output_text.done, in_progress, metadata, reasoning_summary_part.done, and
// anything else ending .delta. Everything unrecognised is debug-logged and
// the stream carries on.
//
// So the set that actually does anything is small:
//
//	response.created                       the turn begins
//	response.output_item.added             an item begins
//	response.output_text.delta             assistant text, streamed
//	response.output_item.done              a COMPLETED item — see below
//	response.reasoning_summary_text.delta  reasoning, streamed
//	response.reasoning_text.delta
//	response.completed                     the end, carrying usage
//	response.failed / response.incomplete  the error paths
//
// Two consequences worth having in advance.
//
// Tool calls do not need streaming at all. They arrive whole, as a
// response.output_item.done carrying a function_call item — the incremental
// function_call_arguments events are on the ignore list. So the translator
// buffers Anthropic's tool_use input and emits one finished item.
//
// And `arguments` on that item is a JSON *string*, not an object:
//
//	{"type":"function_call","name":"exec_command","call_id":"…",
//	 "arguments":"{\"cmd\":\"ls\"}","namespace":"multi_agent_v1"}
//
// Anthropic's tool_use.input is an object, so it has to be marshalled back
// into a string on the way out, and parsed on the way in.
//
// # Namespaces go out flattened and come back whole
//
// FunctionCall carries an optional `namespace` field. That is the other half
// of the "namespace" tool type: Anthropic has no nesting, so the tools go out
// flattened, but the call has to come back with its namespace restored or
// Codex cannot route it to the right sub-tool.
//
// That is the same shape as upstream.RewriteRefusedToolNames — rewrite on the
// way out, keep a map, restore on the way in — and it should be built the same
// way, including its collision guard.
//
// # Every way the stream can fail
//
// Codex's parser defines these, and each one becomes an error the user sees.
// Corroborated by opencodex's own root-cause analysis of exactly this bridge
// (devlog/_fin/110_codex-stream-stability), which is worth more than the API
// documentation because it was written after the failures happened.
//
//   - The terminal event is response.completed, and nothing else. The
//     chat-completions `data: [DONE]` sentinel is ignored by this parser, so a
//     stream that ends without response.completed fails with "stream closed
//     before response.completed" — even though every byte before it was good.
//   - response.completed needs an `id`; usage and end_turn are optional. So a
//     turn with no usage reports zero tokens rather than failing, which is the
//     quiet kind of wrong.
//   - response.failed reads response.error and never last_error. An error
//     object that is absent or does not deserialise becomes a generic stream
//     failure, losing whatever the upstream actually said — the same defect
//     the relay exists to avoid, in the other direction.
//   - An idle timeout with no SSE frame at all fails the turn. Claude thinks
//     for a long time before its first token, so the translator has to keep
//     bytes moving across that gap. This is the mirror of the watchdog that
//     makes internal/upstream flush every chunk.
//   - A malformed SSE frame fails the turn outright.
//
// Which means an upstream error mid-stream cannot simply close the connection:
// it has to become a response.failed carrying a real error object, or Codex
// reports something that says nothing about what went wrong.
//
// Recognised error codes are context_length_exceeded, insufficient_quota,
// usage_not_included, invalid_prompt, cyber_policy, server_is_overloaded and
// slow_down. Notably rate_limit_exceeded is NOT among them and falls through
// to a generic retry — worth knowing, since a pooled gateway produces rate
// limits more often than anything else.
//
// # Things that will bite
//
//   - The request we send is synthesised here, so it is ours to get right:
//     it needs Claude Code's attribution block first in the system array or
//     the subscription backend serves haiku and nothing above it. That pass
//     runs in internal/upstream and applies to this request exactly as to a
//     relayed one — a synthesised request is still a request the backend
//     judges — so nothing here has to do it, and nothing here may skip it.
//   - `developer` is a role Anthropic does not have.
//   - `reasoning` and Anthropic's `thinking` are not the same feature.
//   - Codex reads usage off the final event; a stream that omits it reports
//     zero tokens rather than failing, which is the kind of wrong that goes
//     unnoticed.
package openai
