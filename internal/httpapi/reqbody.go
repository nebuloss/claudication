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
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, fmt.Errorf("could not read the gzipped body: %w", err)
	}
	if int64(len(out)) > limit {
		return nil, errors.New("the decompressed body is larger than the configured limit")
	}

	r.Header.Del("Content-Encoding")
	return out, nil
}
