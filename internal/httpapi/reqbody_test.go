package httpapi

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/upstream"
)

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func requestWith(encoding string, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	return r
}

// The one that was actually broken: a gzipped body could not be parsed, so the
// attribution block was never added and opus came back as a 429 saying
// "Error". Everything downstream depends on this returning readable JSON.
func TestDecodeBodyOpensGzipSoTheBodyCanBeRead(t *testing.T) {
	const want = `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`
	r := requestWith("gzip", gzipped(t, want))

	got, err := decodeBody(r, mustRead(t, r), 32<<20)
	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if string(got) != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	// Proof it is now usable by the passes that were silently no-oping.
	if p := upstream.Peek(got); p.Model != "claude-opus-5" {
		t.Errorf("prologue model = %q, want it readable", p.Model)
	}
	// The header described bytes that no longer exist.
	if enc := r.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want it removed", enc)
	}
}

func TestDecodeBodyLeavesAnUncompressedBodyAlone(t *testing.T) {
	const want = `{"model":"claude-opus-5"}`
	for _, encoding := range []string{"", "identity"} {
		r := requestWith(encoding, []byte(want))
		got, err := decodeBody(r, []byte(want), 32<<20)
		if err != nil {
			t.Fatalf("%q: %v", encoding, err)
		}
		if string(got) != want {
			t.Errorf("%q: body = %s, want it untouched", encoding, got)
		}
	}
}

// An unknown coding is the upstream's to refuse. Answering with our own error
// would replace the only message the caller could act on.
func TestDecodeBodyPassesAnUnknownCodingThrough(t *testing.T) {
	body := []byte("\x00\x01\x02not-gzip")
	r := requestWith("br", body)
	got, err := decodeBody(r, body, 32<<20)
	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Error("an unknown coding was altered")
	}
	if r.Header.Get("Content-Encoding") != "br" {
		t.Error("the coding header was dropped for a body we did not open")
	}
}

func TestDecodeBodyRejectsABodyThatIsNotGzip(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5"}`)
	r := requestWith("gzip", body)
	if _, err := decodeBody(r, body, 32<<20); err == nil {
		t.Error("a body claiming gzip and not being gzip was accepted")
	}
}

// The inbound MaxBytesReader bounds the compressed stream, which says nothing
// about what it expands to. Without this, a few KB is a memory exhaustion.
func TestDecodeBodyRefusesADecompressionBomb(t *testing.T) {
	bomb := gzipped(t, strings.Repeat("A", 8<<20))
	r := requestWith("gzip", bomb)

	if len(bomb) > 64<<10 {
		t.Fatalf("fixture is not compressed enough to be a bomb: %d bytes", len(bomb))
	}
	// Well under what it expands to.
	if _, err := decodeBody(r, bomb, 1<<20); err == nil {
		t.Error("a body expanding past the limit was accepted")
	}
}

func mustRead(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.Bytes()
}
