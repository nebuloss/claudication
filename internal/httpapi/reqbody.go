package httpapi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// readSized reads r whole, into a buffer sized from hint when hint is
// believable.
//
// io.ReadAll does not know how much is coming, so it doubles its way up and
// leaves every smaller buffer behind: about 2.5 times the body allocated to
// hold it once, for every request, and request bodies here are megabytes. The
// size is usually known — Content-Length, or the length a gzip stream records
// in its trailer — so the buffer is made once at the right size. The spare
// MinRead is what bytes.Buffer insists on having free for its last read, which
// would otherwise double the buffer just to discover EOF.
//
// A hint is only a hint: a wrong one costs a regrow, not a wrong answer, and one
// over the limit is ignored rather than trusted with an allocation.
func readSized(r io.Reader, hint, limit int64) ([]byte, error) {
	if hint <= 0 || (limit > 0 && hint > limit) {
		return io.ReadAll(r)
	}
	buf := bytes.NewBuffer(make([]byte, 0, hint+bytes.MinRead))
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

// gzipSize is the uncompressed length a gzip stream records in its last four
// bytes, modulo 2^32 — exact for any body this gateway would accept — or 0.
func gzipSize(gz []byte) int64 {
	if len(gz) < 18 {
		return 0
	}
	t := gz[len(gz)-4:]
	return int64(t[0]) | int64(t[1])<<8 | int64(t[2])<<16 | int64(t[3])<<24
}

// decodeBody returns the request body as the JSON the caller meant, opening a
// content coding if one was applied.
//
// The relay's rule is that the caller's bytes go upstream unchanged, and this
// does not break it: content coding is a per-hop negotiation, so a hop may
// decode what the previous hop encoded. What it does break is the pretence
// that we can leave the body alone entirely — the attribution block, the MCP
// tool names and the system prompt all have to be read, and none of them can
// be read through gzip.
//
// The header is removed on success, because after this it describes bytes that
// no longer exist: build() forwards the client's headers, and a
// Content-Encoding that disagrees with the body is worse than none.
func decodeBody(r *http.Request, body []byte, limit int64) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return body, nil
	}
	if encoding != "gzip" {
		// Anything else is the upstream's to accept or refuse; passing it on
		// unopened at least gives the caller the upstream's own answer, which
		// is more use than ours.
		return body, nil
	}

	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("the body is not valid gzip: %w", err)
	}
	defer zr.Close()

	// The inbound limit applies to the compressed stream, so it says nothing
	// about what this expands to. Reading one byte past the cap is how the cap
	// is detected rather than trusted.
	if limit <= 0 {
		limit = 32 << 20
	}
	out, err := readSized(io.LimitReader(zr, limit+1), gzipSize(body), limit)
	if err != nil {
		return nil, fmt.Errorf("could not read the gzipped body: %w", err)
	}
	if int64(len(out)) > limit {
		return nil, errors.New("the decompressed body is larger than the configured limit")
	}

	r.Header.Del("Content-Encoding")
	return out, nil
}
