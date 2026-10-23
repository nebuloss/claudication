// Package google is Gemini as reached with a Google account, signed in
// through Antigravity: the Code Assist backend Antigravity talks to — where
// requests go and how they are authenticated, how usage, quota and errors are
// read back out of answers, the sign-in that connects an account and sets it
// up, and the account endpoints behind the admin screens.
//
// It is the only package that knows any of those details. The relay sees it
// through provider.Wire and provider.Stamper, and everything else through
// provider.Accounts.
//
// This is the subscription, not the Gemini API. The metered API at
// generativelanguage.googleapis.com takes an AI Studio key and bills per
// token; Code Assist takes the account's OAuth token and draws on the
// account's own allowance — a free account's starter quota, or Google AI Pro's
// and Ultra's.
//
// No client speaks Code Assist as it is: it wraps a Gemini request in an
// envelope naming the model and the account's project. So every request that
// reaches it is the gateway's own — a Gemini API request from gemini-cli
// rewrapped (internal/api/gemini), or a Messages or Responses one translated
// (internal/api/anthropic, and through it internal/api/openai) — and the
// project is stamped in per attempt (Provider.Stamp), once the account is
// known.
//
// # Why Antigravity, and what it costs
//
// gemini-cli's sign-in reached this backend first, and on 2026-10-23 it no
// longer did: loadCodeAssist lists the free tier as ineligible,
// UNSUPPORTED_CLIENT, "This client is no longer supported for Gemini Code
// Assist for individuals. To continue using Gemini, please migrate to the
// Antigravity suite of products". Antigravity's OAuth client alone did not
// change that: the same refusal came with it. What did is Antigravity's
// User-Agent — with it, the same token was put on the free tier, "Antigravity",
// and served; without it a turn is a 403 SUBSCRIPTION_REQUIRED
// (testdata/antigravity-*.json, both sides).
//
// So every call this package makes says it is Antigravity (UserAgent). That
// is the one place the gateway claims another client's identity, which
// everywhere else it will not; the owner chose it (2026-10-23) as the only
// way a free Google account serves at all, and dropped the gemini-cli
// sign-in for it. It may breach Google's terms, as pooling subscriptions may
// anyone's; the docs say so where accounts are described.
//
// # Where the facts come from
//
//   - CLIProxyAPI (router-for-me/CLIProxyAPI, MIT), main read 2026-10-23:
//     internal/auth/antigravity, internal/misc/antigravity_version.go,
//     internal/runtime/executor/antigravity_*. Antigravity itself is not open
//     source.
//   - gemini-cli main at a713410 (Apache-2.0), for the envelope, the stream
//     and the error vocabulary, which are the same backend's.
//   - scripts/probe-antigravity.py, run 2026-10-23 against the real backend
//     with one free personal account. Its records, scrubbed of tokens, the
//     project and the person, are testdata/antigravity-*.json.
//
// Rows marked "measured" were seen; "source" is what those clients do.
//
// # Sign-in
//
//	fact            value                                         source
//	client          Antigravity's installed-app id and secret     CLIProxyAPI constants.go; measured
//	scopes          cloud-platform, userinfo.email, .profile,     CLIProxyAPI; measured
//	                cclog, experimentsandconfigs
//	consent         accounts.google.com/o/oauth2/v2/auth,         measured
//	                PKCE S256, access_type=offline, prompt=consent
//	redirect        http://localhost:51121/oauth-callback, which  CLIProxyAPI; measured: the
//	                nothing serves: its address is pasted back    address carries code and state
//	exchange        POST oauth2.googleapis.com/token, form,       measured
//	                with the secret and the verifier
//	refresh         the same endpoint, grant refresh_token;       measured; not rotated
//	                invalid_grant is final
//	identity        GET googleapis.com/oauth2/v2/userinfo         measured
//	revoke          POST oauth2.googleapis.com/revoke             Google's documented endpoint
//
// # Setting an account up
//
// The sign-in asks loadCodeAssist, with Antigravity's metadata ({"ideType":
// "ANTIGRAVITY"}) and User-Agent, which tier the account is on and which
// project bills it, and onboards it onto its default tier when it is on
// none. Measured: a free account came back on the free tier, "Antigravity",
// its paid tier "Antigravity Starter Quota", with a managed project; the
// project is stored as account_uuid and the tier's name as the plan.
//
// # Inference
//
//	POST https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse
//	Authorization: Bearer <access>
//	User-Agent: antigravity/hub/2.25.0 (aidev_client; os_type=linux; arch=amd64)
//	{"model": "<id>", "project": "<project>", "request": {"contents": […], …}}
//
// Measured: that plain envelope is answered; CLIProxyAPI's extra fields
// (userAgent, requestType, requestId) are not needed, and the answer is the
// same with them (antigravity-turn-fields.json). The stream is bare `data:`
// lines, each a whole envelope {"response": {…}, "traceId": "…",
// "metadata": {}}, with no event names; the last carries the finish reason,
// the usage and, for a thinking model, the thought signature in a part of
// its own with empty text. So the relay's stall hold, which tells events
// apart by name, is off on this wire.
//
// # Quota
//
// retrieveUserQuota, POST with the project, answers one bucket per model:
// the fraction left and when it refills, nothing more. Measured on a free
// account by spending tokens between snapshots (usage.go has the figures):
// one allowance per line of models — every Gemini model shares one, GPT-OSS
// and each Claude have their own — counted in tokens, thinking included,
// over a week that starts at the line's first use. So nothing maps onto the
// account-wide five-hour and weekly columns; the accounts screen shows a row
// per allowance, and a refusal that was one model's sits the account out for
// that model alone (provider.Verdict Model).
//
// # Models
//
// fetchAvailableModels, POST with the project, is Antigravity's own model
// list (testdata/antigravity-models.json): 23 models for the account
// measured — Gemini 2.5 Pro, 3 Flash, 3.1 and 3.5 Flash Lite, 3.5 and 3.8
// Flash at three thinking levels each, Claude Opus and Sonnet 4.6, GPT-OSS
// 120B, and Antigravity's own autocomplete models — each with a display
// name, a context window, an output cap and its quota. What the gateway
// lists of them is Provider.ModelList's to say.
package google
