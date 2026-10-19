package api

// ErrorKind is a failure the gateway answers itself, before or outside an
// exchange: a key that is missing or over its limits, a body that will not
// parse, a surface switched off, a pool with nothing to serve from.
//
// Typed, and a closed set, because as free strings the same 429 went out as
// "rate_limit" from one handler and "rate_limit_error" from the next, and the
// same 404 as "not_found" and "not_found_error"; a dialect could only map the
// spellings it happened to know, and the OpenAI one mapped one of them. Each
// dialect now maps every kind here, and a test holds it to that.
//
// The values are Anthropic's own error types, which is what the identity
// dialect writes, plus no_accounts, which is the gateway's and no one else's.
//
// What the upstream says when it refuses is not one of these. Its error types
// are its own vocabulary and arrive as text inside a body the relay forwards;
// a translating dialect maps those on the answer side (see Reshaper.Refusal),
// so they never pass through here.
type ErrorKind string

const (
	// KindInvalidRequest is a request the gateway cannot act on as sent.
	KindInvalidRequest ErrorKind = "invalid_request_error"
	// KindAuthentication is a missing or unknown API key.
	KindAuthentication ErrorKind = "authentication_error"
	// KindPermission is a key that may not do what it asked: a model switched
	// off for it.
	KindPermission ErrorKind = "permission_error"
	// KindNotFound is something the gateway does not serve: a surface that is
	// switched off, a model or a role that names nothing.
	KindNotFound ErrorKind = "not_found_error"
	// KindRateLimit is a key over its rate or its token budget.
	KindRateLimit ErrorKind = "rate_limit_error"
	// KindAPI is the gateway failing: the store unreachable, the upstream
	// unreachable.
	KindAPI ErrorKind = "api_error"
	// KindOverloaded is every account busy or cooling.
	KindOverloaded ErrorKind = "overloaded_error"
	// KindNoAccounts is a gateway with no account connected at all. Its own
	// kind rather than KindOverloaded, because waiting will not fix it: a
	// person has to connect one.
	KindNoAccounts ErrorKind = "no_accounts"
)

// ErrorKinds is every kind, for a dialect's test to check its mapping is
// whole.
var ErrorKinds = []ErrorKind{
	KindInvalidRequest, KindAuthentication, KindPermission, KindNotFound,
	KindRateLimit, KindAPI, KindOverloaded, KindNoAccounts,
}
