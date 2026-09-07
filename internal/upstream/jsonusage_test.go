package upstream

import "testing"

// The body a non-streaming /v1/messages actually returns. Before this existed
// every such request was recorded as zero tokens, which reads as "cost
// nothing" when it means "not counted".
func TestJSONUsageFromAMessageResponse(t *testing.T) {
	body := `{"id":"msg_01","type":"message","role":"assistant",
	          "model":"claude-haiku-4-5",
	          "content":[{"type":"text","text":"PONG"}],
	          "stop_reason":"end_turn",
	          "usage":{"input_tokens":14,"output_tokens":6,
	                   "cache_read_input_tokens":3,"cache_creation_input_tokens":9}}`

	var got Usage
	j := newJSONUsage(&got)
	// Fed in slices, because that is how it arrives off the wire.
	for _, chunk := range split(body, 7) {
		j.feed([]byte(chunk))
	}
	j.done()

	if got.InputTokens != 14 || got.OutputTokens != 6 {
		t.Errorf("tokens = %d/%d, want 14/6", got.InputTokens, got.OutputTokens)
	}
	if got.CacheReadTokens != 3 || got.CacheCreationTokens != 9 {
		t.Errorf("cache = %d/%d, want 3/9", got.CacheReadTokens, got.CacheCreationTokens)
	}
}

// An error envelope has no usage, and must not be mistaken for zero usage on a
// successful call — it simply leaves the figures alone.
func TestJSONUsageIgnoresAnErrorEnvelope(t *testing.T) {
	var got Usage
	j := newJSONUsage(&got)
	j.feed([]byte(`{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`))
	j.done()

	if got != (Usage{}) {
		t.Errorf("usage = %+v, want zero", got)
	}
}

// Anything unparseable is a statistic we did not get, never a failed request.
func TestJSONUsageSurvivesRubbish(t *testing.T) {
	for _, body := range []string{"", "not json at all", `{"usage":`, `{"usage":"nope"}`} {
		var got Usage
		j := newJSONUsage(&got)
		j.feed([]byte(body))
		j.done()
		if got != (Usage{}) {
			t.Errorf("body %q produced %+v, want zero", body, got)
		}
	}
}

// The buffer is bounded, or a relay grows its memory with the response it is
// passing through.
func TestJSONUsageIsBounded(t *testing.T) {
	var got Usage
	j := newJSONUsage(&got)
	chunk := make([]byte, 64*1024)
	for i := 0; i < 64; i++ {
		j.feed(chunk)
	}
	if len(j.buf) > maxJSONUsageBytes {
		t.Errorf("buffered %d bytes, cap is %d", len(j.buf), maxJSONUsageBytes)
	}
}

func split(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}
