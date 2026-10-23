package gemini

import (
	"encoding/json"
	"net/http"

	"claudication/internal/api"
)

// countTokens is answered here, for every upstream, with an estimate that
// says it is one. Code Assist has a countTokens of its own, but its body
// names the model inside the request as models/<id>, where a client's model
// name cannot be mapped onto it; Claude's count takes a Messages body, which
// would make one route of this surface behave unlike the rest; ChatGPT
// counts nothing. gemini-cli asks to decide when to compress its history,
// which is what an estimate is good enough for — and errs high, as the
// Anthropic surface's does, since compressing early costs less than
// overflowing.

// estimateHeader marks the count as estimated, as the Anthropic surface's
// X-Claudication-Token-Estimate does.
const estimateHeader = "X-Claudication-Token-Estimate"

type countExchange struct {
	model    string
	estimate int
}

func (e countExchange) Request() []byte                     { return nil }
func (e countExchange) Model() string                       { return e.model }
func (e countExchange) Streaming() bool                     { return false }
func (e countExchange) Headers(http.Header)                 {}
func (e countExchange) Sink(w http.ResponseWriter) api.Sink { return api.Passthrough(w) }

// Answer writes the estimate in countTokens' shape.
func (e countExchange) Answer(w http.ResponseWriter) int {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(estimateHeader, "bytes/4, images 1600; no upstream is asked to count")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]int{"totalTokens": e.estimate})
	return http.StatusOK
}
