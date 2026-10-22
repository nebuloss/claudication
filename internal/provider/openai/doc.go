// Package openai is ChatGPT's Codex backend as reached with a ChatGPT
// subscription: where requests go and how they are authenticated, how usage,
// quota and errors are read back out of answers, the device-code sign-in that
// connects an account, and the account endpoints — usage, models, a test
// turn — behind the admin screens.
//
// It is the only package that knows any of those details. The relay sees it
// through provider.Wire, and everything else through provider.Accounts.
//
// The gateway relays to it what an OpenAI Responses client sent — Codex, above
// all — as it came (see internal/api/openai for the exchange that does): the
// caller already speaks this backend's protocol, so there is nothing to
// translate. A Messages client asking for a model this backend serves — Claude
// Code, crush, opencode — is translated into Responses on the way
// (internal/api/anthropic), and its answer back into Messages.
//
// # Where the facts come from
//
// Two readings and one measurement, which agree except where the tables say:
//
//   - Codex main, codex-rs/, read 2026-10-19 at about e4568a35. Codex moved
//     while it was read, so the citations name symbols, not lines.
//   - crush at 76cc5c57, internal/oauth/openai, for facts only: its licence
//     (FSL-1.1-MIT) is why nothing of it is copied here.
//   - scripts/probe-chatgpt.py, run 2026-10-19 against the real backend with
//     one ChatGPT Plus account. Its records, scrubbed of every token, id and
//     address, are testdata/*.json, and the tests here read them.
//
// The constants are Codex's own OAuth client and backend, which are not a
// documented public API. Pooling several people's ChatGPT subscriptions
// behind shared keys may breach OpenAI's terms; the owner accepted that risk
// knowingly (2026-10-19), and the docs say so where accounts are described.
//
// # Sign-in
//
//	fact            value                                    source
//	issuer          https://auth.openai.com                  Codex login/src/server.rs DEFAULT_ISSUER; crush oauth.go
//	client_id       app_EMoamEEZ73f0CkXaXp7hrann             Codex login/src/auth/manager.rs CLIENT_ID; crush
//	device code     POST /api/accounts/deviceauth/usercode   Codex login/src/device_code_auth.rs; measured
//	device poll     POST /api/accounts/deviceauth/token      403/404 pending; measured
//	operator page   https://auth.openai.com/codex/device     Codex; measured
//	code exchange   POST /oauth/token, form, redirect_uri    Codex, crush; measured (device redirect)
//	                https://auth.openai.com/deviceauth/callback
//	refresh         POST /oauth/token, JSON, no scope        Codex manager.rs (crush sends a form)
//	refresh refusal refresh_token_{expired,reused,           Codex classify_refresh_token_failure
//	                invalidated}, invalid_grant, any 401
//	revoke          POST /oauth/revoke                       Codex REVOKE_TOKEN_URL; body not measured
//
// Device code rather than Codex's browser flow, because nothing has to reach
// the gateway from the browser: the browser flow redirects to a listener on
// 127.0.0.1:1455 on the operator's own machine. The device flow needed no
// per-account opt-in on the account measured.
//
// The identity is the id_token's payload, read unverified (measured, see
// testdata/identity.json): email at the top level; under
// https://api.openai.com/auth chatgpt_account_id, chatgpt_plan_type ("plus"),
// chatgpt_user_id and the subscription's dates. The account id is the one
// stored as account_uuid and sent as ChatGPT-Account-ID on every request:
// one sign-in can reach a personal workspace and a Team one under the same
// address, and each is an account of its own. The access token lived
// 3453 s after the sign-in measured, an hour less a minute; the pool
// refreshes two minutes ahead, which is inside Codex's own five.
//
// # Inference
//
//	POST https://chatgpt.com/backend-api/codex/responses
//	Authorization: Bearer <access>
//	ChatGPT-Account-ID: <chatgpt_account_id>
//	originator: <the caller's, or claudication>
//
// Codex sends more — its User-Agent, version, session-id and thread-id
// (hyphenated on main), x-client-request-id, x-codex-* turn metadata and
// turn-state — and all of it is the caller's, relayed as it came. The gateway
// adds the credential and the account, and an originator only when the caller
// sent none, naming itself: never codex_cli_rs, and nothing of Codex's
// installation or turn metadata is made up. Normalising a request's shape so
// it is not refused is compatibility work; forging another client's identity
// is not (see internal/relay/passes).
//
// What the backend was measured to accept, 2026-10-19:
//
//	request                                     answer
//	the minimal turn (testdata/baseline.json)    200
//	no originator header (no-originator.json)    200   the backend does not gate on it
//	instructions as a developer message instead
//	  of the top-level field (no-instructions)   200   neither form is required
//	max_output_tokens: 256 (max-output.json)     200   crush strips it as refused; not so
//	a lone \ud800 in the text (surrogate.json)   400   {"detail":"Bad Request"}
//	reasoning, effort medium (reasoning.json)    200   one reasoning item, encrypted_content
//	that item replayed next turn, same account   200   right answer (replay.json)
//	that item replayed on another account        not measured: there was one account
//
// So the OpenAI pipeline repairs lone surrogates (passes.OpenAI) and strips
// nothing else; and the pool keeps a conversation on the account that served
// it last (pool affinity), because whether an encrypted reasoning item
// decrypts on another account is unknown and a conversation that cannot
// would be refused for the rest of its life.
//
// The stream's events, measured: response.created, in_progress, metadata,
// output_item.added and .done, content_part.added and .done,
// output_text.delta and .done, completed. Codex also reads
// reasoning_summary_text.delta, reasoning_text.delta, custom_tool_call_
// input.delta, and the terminal response.incomplete and response.failed. The
// relay's stall hold is not used on this wire: with reasoning summaries off,
// which is the backend's default, a reasoning model is silent until its
// reasoning item is done, and a hold would resend a turn that was thinking.
//
// Usage arrives on response.completed: input_tokens with
// input_tokens_details.{cached_tokens, cache_write_tokens}, output_tokens with
// output_tokens_details.reasoning_tokens. Cached input is inside input_tokens
// here and outside it at Anthropic, so the meter takes it out; see
// usageJSON.applyTo.
//
// # Quota
//
// On every answer, x-codex-{primary,secondary}-{used-percent, window-minutes,
// reset-at, reset-after-seconds} — reset-after-seconds is on the wire though
// not in Codex's source — and GET https://chatgpt.com/backend-api/wham/usage
// for the poll (testdata/usage.json). Primary was 300 minutes and secondary
// 10080 on the plan measured; they are mapped by length all the same.
//
// A 429 for the plan's limit is {"error":{"type":"usage_limit_reached",
// "resets_at":<unix>,...}} (Codex api_bridge.rs; not measured — the account
// never ran out), and the pool sits the account out until resets_at.
//
// # Models
//
// GET {codex}/models?client_version=999.0.0 (crush models.go; Codex sends its
// own version, and the backend leaves out what that version is too old for).
// Measured: nine models, gpt-6.1-sol first at priority 1 with a 500k
// context, then gpt-6-astra, gpt-6-sol, gpt-6-luna, gpt-5.6-sol, -terra and
// -luna; gpt-reserve and codex-auto-review hidden. Only the listed ones are
// served, ranked by priority, which orders the ChatGPT families
// (internal/service/models, family.go).
package openai
