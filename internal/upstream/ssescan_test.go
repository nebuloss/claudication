package upstream

import "testing"

// feedIn splits the stream at an awkward boundary to prove the scanner
// reassembles records that arrive across chunks — which is the normal case on
// a real connection, not an edge case.
func feedIn(s *sseScanner, stream string, chunk int) {
	for i := 0; i < len(stream); i += chunk {
		end := min(i+chunk, len(stream))
		s.feed([]byte(stream[i:end]))
	}
}

const usageStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":29,"cache_read_input_tokens":12}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":5}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestScannerCollectsUsage(t *testing.T) {
	for _, chunk := range []int{1, 7, 64, 4096} {
		var usage Usage
		var streamErr string
		feedIn(newSSEScanner(&usage, &streamErr), usageStream, chunk)

		if usage.InputTokens != 29 {
			t.Errorf("chunk %d: input = %d, want 29", chunk, usage.InputTokens)
		}
		// message_delta carries only output tokens; it must not clear the
		// input count that arrived on message_start.
		if usage.OutputTokens != 5 {
			t.Errorf("chunk %d: output = %d, want 5", chunk, usage.OutputTokens)
		}
		if usage.CacheReadTokens != 12 {
			t.Errorf("chunk %d: cache read = %d, want 12", chunk, usage.CacheReadTokens)
		}
		if streamErr != "" {
			t.Errorf("chunk %d: unexpected stream error %q", chunk, streamErr)
		}
	}
}

// The reason this scanner exists: an error after a 200 must not be recorded as
// a success. auth2api misses exactly this and never cools the account down.
func TestScannerCatchesMidStreamError(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"

	for _, chunk := range []int{1, 13, 4096} {
		var usage Usage
		var streamErr string
		feedIn(newSSEScanner(&usage, &streamErr), stream, chunk)

		if streamErr == "" {
			t.Fatalf("chunk %d: mid-stream error was not detected", chunk)
		}
		if streamErr != "overloaded_error: Overloaded" {
			t.Errorf("chunk %d: streamErr = %q", chunk, streamErr)
		}
	}
}

// Nothing the scanner sees is worth breaking a response over.
func TestScannerToleratesGarbage(t *testing.T) {
	var usage Usage
	var streamErr string
	s := newSSEScanner(&usage, &streamErr)

	s.feed([]byte("event: message_delta\ndata: not json at all\n\n"))
	s.feed([]byte(": a comment line\n\n"))
	s.feed([]byte("data: {\"unterminated\": \n"))
	s.feed([]byte("event: message_delta\ndata: {\"usage\":{\"output_tokens\":9}}\n\n"))

	if streamErr != "" {
		t.Errorf("garbage should not produce a stream error, got %q", streamErr)
	}
	if usage.OutputTokens != 9 {
		t.Errorf("scanner should recover after garbage, output = %d", usage.OutputTokens)
	}
}

// A record with no trailing newline must not be able to grow the buffer
// without bound.
func TestScannerBoundsItsBuffer(t *testing.T) {
	var usage Usage
	var streamErr string
	s := newSSEScanner(&usage, &streamErr)

	blob := make([]byte, 64*1024)
	for i := range blob {
		blob[i] = 'x'
	}
	for range 32 {
		s.feed(blob)
	}
	if s.buf.Len() > 1<<20 {
		t.Errorf("buffer grew to %d bytes", s.buf.Len())
	}
}
