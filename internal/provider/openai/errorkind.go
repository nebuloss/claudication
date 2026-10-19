package openai

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The backend's failure vocabulary, from Codex's source (codex-api/src/
// api_bridge.rs, sse/responses_error.rs), which is what reads it:
//
//	usage_limit_reached        429, the plan's limit, with resets_at
//	usage_not_included         the plan does not include Codex
//	insufficient_quota,
//	credit_balance_exhausted   out of credits
//	server_is_overloaded,
//	slow_down                  503
//	invalid_prompt, cyber_policy, bio_policy,
//	misalignment_policy_violation          400/403, the content
//	context_length_exceeded,
//	rate_limit_exceeded        inside the stream, as response.failed
//
// The word worth keeping is the error's type where it has one other than
// "error", and its code otherwise: usage_limit_reached is a type, every one
// of the others a code. A bare {"detail": "..."} — what a lone surrogate was
// refused with, measured — names nothing, and is filed as bad_request.

// streamCode reads the code off a failure the meter recorded from inside a
// stream, which it writes as "code: message".
var streamCode = regexp.MustCompile(`^([a-z][a-z0-9_]*): `)

// ErrorCode classifies one outcome in one word, for storing beside it. Empty
// means the request worked.
func ErrorCode(status int, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if strings.HasPrefix(body, "{") {
		var e apiError
		if json.Unmarshal([]byte(body), &e) == nil {
			if k := e.kind(); k != "" {
				return k
			}
			if len(e.Detail) > 0 {
				return "bad_request"
			}
		}
	}
	if m := streamCode.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	if status == 0 {
		return "no_answer"
	}
	return "other"
}
