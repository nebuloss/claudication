// Package google is Gemini as reached with a Google account: the Code Assist
// backend that gemini-cli talks to after "Login with Google" — where requests
// go and how they are authenticated, how usage, quota and errors are read back
// out of answers, the sign-in that connects an account and sets it up, and the
// account endpoints behind the admin screens.
//
// It is the only package that knows any of those details. The relay sees it
// through provider.Wire and provider.Stamper, and everything else through
// provider.Accounts.
//
// This is the subscription, not the Gemini API. The metered API at
// generativelanguage.googleapis.com takes an AI Studio key and bills per
// token; Code Assist takes the account's OAuth token and draws on the
// account's own allowance — the free tier's, or Google AI Pro's and Ultra's —
// exactly as gemini-cli does.
//
// No client speaks Code Assist as it is: it wraps a Gemini request in an
// envelope naming the model and the account's project. So every request that
// reaches it is the gateway's own — a Gemini API request from gemini-cli
// rewrapped (internal/api/gemini), or a Messages or Responses one translated
// (internal/api/anthropic, internal/api/openai) — and the project is stamped
// in per attempt (Provider.Stamp), once the account is known.
//
// # Where the facts come from
//
// One reading, and no measurement yet:
//
//   - gemini-cli main at a713410, read 2026-10-23, Apache-2.0:
//     packages/core/src/code_assist/{oauth2,setup,server,converter,types}.ts
//     and utils/googleQuotaErrors.ts. Citations name files and symbols.
//
// Every row below marked "source" is what gemini-cli does, not what the
// backend was seen to do. They are to be measured against a live account, as
// the other two providers were, and this table corrected where they differ.
//
// The constants are gemini-cli's own OAuth client and backend, which are not
// a documented public API. Pooling several people's Google accounts behind
// shared keys may breach Google's terms, as it may Anthropic's and OpenAI's;
// the docs say so where accounts are described.
//
// # Sign-in
//
//	fact            value                                         source
//	client          gemini-cli's installed-app id and secret      oauth2.ts OAUTH_CLIENT_{ID,SECRET}
//	scopes          cloud-platform, userinfo.email, .profile      oauth2.ts OAUTH_SCOPE
//	consent         accounts.google.com/o/oauth2/v2/auth,         oauth2.ts authWithUserCode
//	                PKCE S256, access_type=offline
//	redirect        https://codeassist.google.com/authcode,       oauth2.ts authWithUserCode
//	                a page that shows the code to paste
//	exchange        POST oauth2.googleapis.com/token, form,       google-auth-library getToken
//	                with the secret and the verifier
//	refresh         the same endpoint, grant refresh_token;       RFC 6749; not rotated
//	                invalid_grant is final
//	identity        GET googleapis.com/oauth2/v2/userinfo         oauth2.ts fetchAndCacheUserInfo
//	revoke          POST oauth2.googleapis.com/revoke             Google's documented endpoint
//
// The paste flow rather than gemini-cli's default, which redirects to a
// listener on 127.0.0.1 on the operator's own machine: nothing has to reach
// the gateway from the browser. prompt=consent is added (see AuthURL): an
// account that has used gemini-cli would otherwise get no refresh token.
//
// # Setting an account up
//
// Before its first turn gemini-cli asks loadCodeAssist which tier the account
// is on and which project bills it, and onboards it onto its default tier
// with onboardUser when it is on none (setup.ts setupUser). The sign-in does
// the same, once, and stores the project as account_uuid and the tier's name
// as the plan. The free tier's project is managed by Google and must not be
// named when onboarding. An account whose tier wants a project of its own —
// Workspace, Cloud — is refused at sign-in with the backend's reasons.
//
// # Inference
//
//	POST https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse
//	POST …/v1internal:generateContent
//	POST …/v1internal:countTokens
//	Authorization: Bearer <access>
//	{"model": "<id>", "project": "<project>", "user_prompt_id": "…",
//	 "request": {"contents": […], "systemInstruction": …, "tools": […],
//	             "generationConfig": {…}, "session_id": "…"}}
//
// The stream is bare `data:` lines, each a whole envelope {"response": {…},
// "traceId": "…"}, with no event names (server.ts requestStreamingPost). So
// the relay's stall hold, which tells events apart by name, is off on this
// wire. Usage rides on the events; the last one's is the turn's
// (meter.go applyUsage for how it is normalised).
//
// gemini-cli sends its User-Agent, GeminiCLI/<version>/<model> (…); when the
// caller is gemini-cli that header is the caller's and travels as it came,
// and nothing of it is made up for a caller that is not.
//
// # Quota
//
// retrieveUserQuota, POST with the project, answers one bucket per model:
// the fraction left and when it refills (usage.go). The limits are per model
// — a spent Pro leaves Flash serving — and their window's length is not
// said, so nothing maps onto the five-hour and weekly columns; the accounts
// screen shows a row per model. A 429 says why in its google.rpc details
// (errors.go): a short rate limit or a spent quota, each with a RetryInfo
// delay the pool waits out; the model out of capacity at Google's end, which
// is not the account's; or a quota of nothing, which is the plan lacking the
// model.
//
// # Models
//
// There is no model list. The quota buckets name every model the account may
// call, and are read as its list (Provider.ModelsRequest), ranked newest
// first and Pro before Flash before Flash-Lite. gemini-cli knows of
// gemini-2.5-pro, gemini-3-pro-preview, gemini-3.1-pro-preview,
// gemini-3-flash-preview, gemini-3.5-flash and gemini-3.1-flash-lite among
// others (config/models.ts), and maps some of its names onto others on this
// backend (getBackendModelMappings): which ones an account is offered is the
// quota's to say.
package google
