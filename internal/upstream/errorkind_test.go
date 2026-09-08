package upstream

import "testing"

func TestClassifyRefusalNamesTheContentCheck(t *testing.T) {
	// The body as it actually arrives, verbatim from a recorded request.
	body := `{"type":"error","error":{"type":"invalid_request_error","message":` +
		`"Third-party apps now draw from your extra usage, not your plan limits. ` +
		`Add more at claude.ai/settings/usage and keep going."},"request_id":"req_011Ceqz"}`

	if got := ClassifyRefusal(body); got != KindContentCheck {
		t.Errorf("got %q, want %q", got, KindContentCheck)
	}
}

func TestClassifyRefusalLeavesOtherErrorsAlone(t *testing.T) {
	for _, body := range []string{
		`{"error":{"type":"invalid_request_error","message":"system: text content blocks must be non-empty"}}`,
		`{"error":{"type":"invalid_request_error","message":"adaptive thinking is not supported on this model"}}`,
		`{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`,
		// Mentions extra usage, but is a real billing answer and must not be
		// relabelled as a content problem.
		`{"error":{"type":"invalid_request_error","message":"extra usage is required for this model"}}`,
		"",
	} {
		if got := ClassifyRefusal(body); got != "" {
			t.Errorf("%q: got %q, want no kind", body, got)
		}
	}
}
