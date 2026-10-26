package google

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"claudication/internal/geminiwire"
	"claudication/internal/provider"
)

// The backend's failure vocabulary, from gemini-cli's reading of it
// (utils/googleQuotaErrors.ts classifyGoogleError), which is the only
// documentation it has. Every error is a google.rpc.Status; what it means is
// in the ErrorInfo detail's reason when there is one, in the status name
// otherwise:
//
//	429 RATE_LIMIT_EXCEEDED          a short limit, with a RetryInfo delay
//	429 QUOTA_EXHAUSTED              the model's quota for the period is spent
//	429 "limit: 0" in the message    the plan does not include the model
//	429/503 MODEL_CAPACITY_EXHAUSTED Google is out of capacity for the model:
//	                                 nobody's account would do better
//	403 VALIDATION_REQUIRED          the account must be verified, with a link
//	404                              no such model, or not for this account
//	400 INVALID_ARGUMENT             the request, which no account fixes
//
// gemini-cli retries a delay of up to five minutes and treats a longer one as
// final; the pool is given the delay either way and sits the account out for
// it.

// parseStatus reads an error body. A body that is not a Google error yields
// the zero Status.
func parseStatus(body []byte) geminiwire.Status {
	var e geminiwire.ErrorEnvelope
	if json.Unmarshal(body, &e) == nil && (e.Error.Code != 0 || e.Error.Status != "" || e.Error.Message != "") {
		return e.Error
	}
	// streamGenerateContent without ?alt=sse answers a failure as an array
	// holding one.
	var arr []geminiwire.ErrorEnvelope
	if json.Unmarshal(body, &arr) == nil && len(arr) > 0 {
		return arr[0].Error
	}
	return geminiwire.Status{}
}

// details are a Status's typed details, decoded.
func details(s geminiwire.Status) []geminiwire.Detail {
	out := make([]geminiwire.Detail, 0, len(s.Details))
	for _, raw := range s.Details {
		var d geminiwire.Detail
		if json.Unmarshal(raw, &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

// reason is the ErrorInfo's reason, or "".
func reason(ds []geminiwire.Detail) string {
	for _, d := range ds {
		if d.Type == geminiwire.TypeErrorInfo && d.Reason != "" {
			return d.Reason
		}
	}
	return ""
}

// maxRetryDelay bounds a delay read off an answer: past it, the figure is
// not one to sit an account out on, and a float that large would not
// convert to a Duration sensibly.
const maxRetryDelay = 30 * 24 * time.Hour

// retryDelay is the RetryInfo's delay, or zero. Google writes it as a
// protobuf Duration: "3.5s", and gemini-cli also reads "120ms".
func retryDelay(ds []geminiwire.Detail) time.Duration {
	for _, d := range ds {
		if d.Type != geminiwire.TypeRetryInfo || d.RetryDelay == "" {
			continue
		}
		unit, v := time.Second, d.RetryDelay
		if ms, ok := strings.CutSuffix(v, "ms"); ok {
			unit, v = time.Millisecond, ms
		} else if s, ok := strings.CutSuffix(v, "s"); ok {
			v = s
		} else {
			continue
		}
		n, err := strconv.ParseFloat(v, 64)
		// Written so NaN, for which every comparison is false, fails too.
		if err != nil || !(n > 0 && n*float64(unit) <= float64(maxRetryDelay)) {
			continue
		}
		return time.Duration(n * float64(unit))
	}
	return 0
}

// zeroLimit matches the message Google gives a quota of nothing: the model is
// not in the plan. gemini-cli's /limit:\s*0(?!\d|\.\d)/; Go's regexp has no
// lookahead, so what follows the 0 is matched instead: the end, or anything
// but a digit or a decimal point, or a point not followed by a digit.
var zeroLimit = regexp.MustCompile(`limit:\s*0([^\d.]|\.(\D|$)|$)`)

// verdict is what a refusal says about the account that served it.
func verdict(status int, body []byte) provider.Verdict {
	var v provider.Verdict
	s := parseStatus(body)
	ds := details(s)
	r := reason(ds)
	switch {
	case r == "VALIDATION_REQUIRED":
		// Until someone opens the link, nothing this account sends will be
		// answered. Another account may well be. The account's, not a
		// model's.
		v.Kind = "forbidden"
	case r == "MODEL_CAPACITY_EXHAUSTED" || r == "MODEL_CAPACITY_EXCEEDED":
		// Google's capacity for the model, not the account's quota: a
		// server problem to back off from briefly, for that model.
		v.Kind, v.Model = "server", true
	case status == 429 && zeroLimit.MatchString(strings.ToLower(s.Message)):
		// The plan has no quota for the model at all. Waiting will not help;
		// another account's plan may include it, and this one still serves
		// the models it has.
		v.Kind, v.Model = "forbidden", true
	case status == 429:
		// Every quota Code Assist has is a model's (usage.go), so a limit
		// reached is that model's on this account.
		v.Model = true
		// A rate limit, or the model's quota spent (QUOTA_EXHAUSTED, or a
		// QuotaFailure naming a per-day quota): either way the delay says
		// when to come back,
		// and the pool's own backoff decides when it does not. No quota is
		// reported: the account's figures are per model and live in its
		// detail, which a quota with nothing in it would wipe.
		if d := retryDelay(ds); d > 0 {
			v.Until = time.Now().Add(d)
		}
	}
	return v
}

// streamCode reads the code off a failure the meter recorded from inside a
// stream, which it writes as "code: message".
var streamCode = regexp.MustCompile(`^([a-z][a-z0-9_]*): `)

// ErrorCode classifies one outcome in one word, for storing beside it: the
// ErrorInfo reason when there is one, the status name otherwise, lower case
// as the other providers' codes are. Empty means the request worked.
func ErrorCode(status int, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if body[0] == '{' || body[0] == '[' {
		if s := parseStatus([]byte(body)); s.Code != 0 || s.Status != "" {
			return errorCode(s)
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

// errorCode is a Status's one word.
func errorCode(s geminiwire.Status) string {
	if r := reason(details(s)); r != "" {
		return strings.ToLower(r)
	}
	if s.Status != "" {
		return strings.ToLower(s.Status)
	}
	return "error"
}

// describe is an error body as an operator should read it: the Status's
// message and the link Google gave with it, else the body itself.
func describe(raw []byte) string {
	s := parseStatus(raw)
	if s.Message == "" {
		return clip(strings.TrimSpace(string(raw)), 400)
	}
	msg := s.Message
	for _, d := range details(s) {
		if d.Type == geminiwire.TypeHelp && len(d.Links) > 0 && d.Links[0].URL != "" {
			msg += " (" + d.Links[0].URL + ")"
			break
		}
	}
	return clip(msg, 2000)
}
