// Package anthropic serves the Anthropic Messages API: to Claude as the
// identity, and to ChatGPT's Codex backend translated into OpenAI Responses.
//
// To Claude it is the identity adapter, and keeping it an implementation
// rather than a bypass is what makes the abstraction worth having. The
// relay's contract — the caller's bytes go upstream unchanged, the answer
// comes back verbatim — is expressed here, once, as a Protocol that does
// nothing (anthropic.go), instead of as a branch every other layer has to
// remember not to take.
//
// # Reaching ChatGPT
//
// A Messages request for a model a ChatGPT account serves is rebuilt as the
// Responses request that backend takes (toresponses.go), and its answer said
// back as Anthropic's (fromresponses.go, sink.go). The gateway's job is to
// make models usable from the client a person already runs: a Claude Code or
// crush pointed at the gateway reaches GPT with no change of API.
//
// This is the opposite of internal/api/openai, which takes Codex's Responses
// requests to Claude; read its doc for the Responses vocabulary. As there,
// correctness is "does the mapping mean the same thing", and every decision
// is beside its evidence. The backend was measured through the gateway's own
// native /v1/responses path on 2026-10-20 (one ChatGPT Plus account,
// gpt-6-luna at low effort), the clients by capture the same day: Claude
// Code 2.1.304 against a stub. internal/provider/openai has the backend's
// earlier record (2026-10-19).
//
// # The request
//
//	Messages                          Responses                         evidence
//	model                             model                             SetModel maps a slot as on every wire
//	system (string or blocks)         instructions, texts joined        measured: instructions accepted
//	  Claude Code's billing line      dropped                           Claude Code's attestation, not an instruction
//	  cache_control                   dropped                           no equivalent; the backend caches by
//	                                                                      prompt_cache_key
//	messages, user text               message user, input_text          measured
//	  image base64 / url              input_image image_url             measured: an 8x8 PNG answered "Red"
//	  document PDF base64             input_file file_data              measured: read, counted in input tokens
//	  document PDF url                input_file file_url               Responses' own field; not measured
//	  document text / content         input_text                        what Anthropic makes of it
//	  image or document "file"        refused                           an Anthropic file id means nothing there
//	  tool_result                     function_call_output, call_id     measured: round trip answered
//	    with images                   output as a list of parts         measured: the picture was seen ("Blue")
//	    is_error                      "Error:" before the output        Responses has no is_error
//	  tool_reference                  "Tool X is now available."        see deferred tools below
//	messages, role system             message developer                 Claude Code sends these (captured);
//	                                                                      developer measured accepted
//	messages, assistant text          message assistant, output_text    measured: history read ("zebra")
//	  tool_use                        function_call, call_id, arguments
//	                                  as a JSON string
//	  thinking, our signature         reasoning item, id, summary,      measured: replayed with and without
//	                                  encrypted_content                   its id, answered from it
//	  thinking, Anthropic's           dropped                           proof of a Claude turn; unreadable there
//	  server tool history             dropped                           the outcome is in the text after it
//	tools, custom                     function, strict false            strict needs a schema subset Anthropic
//	                                                                      does not require
//	  name over 64 bytes              cut, hashed, restored on answer   Responses' limit is 64, Anthropic's 128
//	  defer_loading                   offered once a tool search        what Anthropic does with them
//	                                  loaded it
//	  web_search_*                    web_search, allowed_domains,      measured: searched, listed the pages
//	                                  user_location
//	    blocked_domains               refused                           no equivalent; a blocklist ignored
//	                                                                      silently is worse than a refusal
//	  any other Anthropic tool        refused, by type                  nothing there runs it
//	mcp_servers                       refused                           no MCP connector there
//	tool_choice auto/any/tool/none    auto/required/function/none       measured: each behaved
//	  disable_parallel_tool_use       parallel_tool_calls false         else true: Anthropic's default
//	max_tokens                        max_output_tokens, at least 16    measured: 16 a floor, 1 a 400
//	thinking, output_config.effort    reasoning.effort, .summary        see below
//	output_config.format json_schema  text.format json_schema           measured: answered matching JSON
//	temperature, top_p, top_k         dropped                           measured: temperature and top_p are a 400,
//	                                                                      "Unsupported parameter"
//	stop_sequences                    dropped                           measured: stop is a 400
//	metadata.user_id                  dropped                           measured: metadata and user are a 400;
//	                                                                      the chat's id is the cache key instead
//	stream                            always true                       measured: false is a 400
//	                                  store false, include encrypted    nothing kept at OpenAI; see reasoning
//	(the conversation id)             prompt_cache_key                  the backend's cache, as Codex keys it
//	context_management, container,    dropped                           Anthropic-side features with no
//	service_tier                                                          meaning there
//
// Effort. output_config.effort's values — low, medium, high, xhigh, max — are
// the ones every model the backend lists supports (models.json,
// supported_reasoning_levels), so the one maps to the other unchanged; a
// thinking budget is mapped to the effort its size is the scale of; thinking
// disabled is low, the least every model lists; no thinking, or adaptive with
// no effort, leaves the effort to the model's default. measured: low, medium,
// high, xhigh accepted; "minimal" a 400 listing the supported values.
//
// Deferred tools. Claude Code marks most of its tools defer_loading and loads
// them with its ToolSearch tool, whose result names them in tool_reference
// blocks. The backend has no deferral, so a deferred tool is offered once a
// tool_reference in the conversation has named it, as Anthropic would.
//
// # Reasoning, carried through the client
//
// With store:false a reasoning item's only copy is the encrypted_content in
// the answer, and the backend wants it back on the next turn. A thinking
// block is where a Messages client keeps that, and sends it back: so the
// item is answered as a thinking block whose text is the item's summary and
// whose signature carries the item, marked so it is never mistaken for one
// of Anthropic's (reasoning.go). Thinking blocks are sent only to a caller
// that asked for thinking, as Anthropic does; display "omitted" (Claude
// Code's) is answered with the signature and no text, and no summary is
// asked for.
//
// A caller that did not ask for thinking is sent none, and so cannot send its
// reasoning back; the gateway keeps it instead, in memory, keyed by the call
// or text it led to, and puts it back in front of that call or text on the
// next turn (reasoningstore.go). Measured 2026-10-20 with a tool loop and no
// thinking asked: the same second turn under its own chat went up with the
// first turn's reasoning (218 input tokens, 47 output) and under another chat
// without it (121 input, 113 output: the model worked it out again).
//
// # The answer
//
// Anthropic's events in Anthropic's order: message_start (input 0, since the
// backend says its usage only at the end) and a ping, each block opened,
// streamed and closed, message_delta with the stop reason and usage,
// message_stop. fromresponses.go has the event table.
//
//	stop_reason   from
//	tool_use      a function call in the turn
//	max_tokens    response.incomplete, max_output_tokens (measured)
//	refusal       a refusal part, or incomplete for the content filter
//	end_turn      the rest
//
// Usage as the OpenAI meter normalises it: input_tokens less cached_tokens
// as input_tokens, cached_tokens as cache_read_input_tokens,
// cache_write_tokens as cache_creation_input_tokens, output_tokens (reasoning
// included) as output_tokens. Errors in Anthropic's envelope and types:
// usage_limit_reached and the rate codes are rate_limit_error, the overload
// codes overloaded_error, the request codes invalid_request_error, the rest
// by status; a refusal keeps the backend's status and its Retry-After.
//
// The backend streams every answer, so a caller that did not ask to stream is
// answered from the stream, gathered into one message. A stream is kept
// alive with pings while the model reasons in silence.
//
// count_tokens is answered by the gateway with an estimate that says it is
// one; counttokens.go says how and why.
//
// # What is not mapped
//
//   - Citations: url_citation annotations are not said back as citation
//     deltas; the links are in the text the model wrote.
//   - A thinking block of ours sent to Claude, after a slot is moved back
//     from GPT to Claude mid-chat, is not removed on the way to Anthropic.
//     Whether Anthropic refuses it was not measured (no Claude account on
//     the instance measured), and the relay's passes take no rewrite on a
//     guess (internal/relay/passes).
//   - Anthropic-defined client tools (bash, text_editor, memory, computer)
//     are refused rather than given a schema of the gateway's making.
package anthropic
