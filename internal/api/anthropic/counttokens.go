package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"claudication/internal/api"
)

// count_tokens for a model ChatGPT serves.
//
// The backend has no endpoint that counts a request's tokens, and the
// gateway has no tokenizer of OpenAI's, so the honest answers were two: a
// refusal, or an estimate that says it is one. A refusal is the worse of
// them for the client the route exists for: Claude Code, answered with an
// error here, measures the context by sending a real max_tokens:1 inference
// instead (see the route in internal/httpapi/gateway) — and on this backend
// max_output_tokens has a floor of 16, so that costs more than it does on
// Claude, once per measurement.
//
// So the answer is an estimate, computed in the open and marked as one with
// the X-Claudication-Token-Estimate header:
//
//   - the text the model reads, as the request would go upstream — the
//     instructions, every message, call and result, every tool's name,
//     description and schema, but not the structure around them nor an
//     encrypted reasoning item — at four bytes a token, the usual figure for
//     OpenAI's tokenizers on English and code;
//   - each picture at a flat 1,600 tokens, about what a high-detail image
//     costs; counting its base64 at four bytes a token would make one
//     screenshot look like a quarter of a million tokens and set off the
//     client's compaction for nothing;
//   - each PDF at its decoded size over eight bytes a token, a rough figure
//     for text-heavy PDFs, whose structure is not text the model reads.
//
// It is meant for what a client does with the figure — decide when to
// compact — and errs high rather than low, since compacting a little early
// costs less than overflowing the window.

const (
	bytesPerToken    = 4
	tokensPerImage   = 1600
	pdfBytesPerToken = 8
)

// EstimateHeader marks a count as estimated, and how.
const EstimateHeader = "X-Claudication-Token-Estimate"

// visible are the fields whose text the model reads: what it is told and
// shown, a call's arguments, a result, a tool's name, description and schema.
// Everything else is structure — type, role, id, call_id — or opaque, as an
// encrypted reasoning item is: its text was the model's own, and counting
// its ciphertext as input would make a replayed reasoning item look like
// several times the reasoning it was.
var visible = map[string]bool{
	"text": true, "arguments": true, "output": true,
	"name": true, "description": true, "parameters": true,
}

func estimateTokens(r responsesRequest) int {
	n := len(r.Instructions)
	media := 0
	var walk func(v any, counted bool)
	walk = func(v any, counted bool) {
		switch x := v.(type) {
		case map[string]any:
			switch x["type"] {
			case "input_image":
				media += tokensPerImage
				return
			case "input_file":
				if data, ok := x["file_data"].(string); ok {
					if _, b64, ok := strings.Cut(data, ","); ok {
						media += base64.StdEncoding.DecodedLen(len(b64)) / pdfBytesPerToken
						return
					}
				}
				media += tokensPerImage
				return
			}
			for k, e := range x {
				walk(e, visible[k])
			}
		case *inputMessage:
			for _, c := range x.Content {
				walk(c, false)
			}
		case []any:
			for _, e := range x {
				walk(e, counted)
			}
		case string:
			if counted {
				n += len(x)
			}
		case json.RawMessage:
			if counted {
				n += len(x)
			}
		}
	}
	walk(r.Input, false)
	walk(r.Tools, false)
	return (n+bytesPerToken-1)/bytesPerToken + media
}

// countExchange answers count_tokens itself: there is nothing upstream to
// ask.
type countExchange struct{ tr Translation }

func (e countExchange) Request() []byte     { return nil }
func (e countExchange) Model() string       { return e.tr.Model }
func (e countExchange) Streaming() bool     { return false }
func (e countExchange) Headers(http.Header) {}

// Sink is never used: the gateway calls Answer instead of relaying.
func (e countExchange) Sink(w http.ResponseWriter) api.Sink { return api.Passthrough(w) }

// Answer writes the estimate.
func (e countExchange) Answer(w http.ResponseWriter) int {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(EstimateHeader, "bytes/4, images 1600, pdf bytes/8; the ChatGPT backend counts no tokens")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": e.tr.Estimate})
	return http.StatusOK
}
