package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nebuloss/claudication/internal/pool"
	"github.com/nebuloss/claudication/internal/store"
	"github.com/nebuloss/claudication/internal/upstream"
)

// handleMessages is Lane A: Anthropic in, Anthropic out, byte for byte.
//
// It deliberately does almost nothing. It does not decode the body, rewrite
// the system array, filter events, normalise errors, or inspect capabilities —
// every one of those would break something the gateway contract requires, and
// most of them are the defects found in auth2api. It reads the body only
// because a retry on another account has to replay it.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	s.passthrough(w, r, "/v1/messages", "/v1/messages?beta=true")
}

// handleCountTokens is the same relay against the token-counting endpoint.
// Serving it matters: without it Claude Code falls back to counting context
// through the inference endpoint, spending real requests on arithmetic.
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	s.passthrough(w, r, "/v1/messages/count_tokens", "/v1/messages/count_tokens?beta=true")
}

func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, route, upstreamPath string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not read the request body")
		return
	}

	// Peeked, never rewritten: the model name is for logging and routing, and
	// the body that goes upstream is the caller's bytes unchanged.
	model, streaming := peekModel(body)

	ctx, cancel := contextWithTimeout(r, upstream.Timeout(streaming))
	defer cancel()
	r = r.WithContext(ctx)

	started := time.Now()
	res := s.relay.Do(w, r, "anthropic", upstreamPath, body)

	elapsed := time.Since(started)
	key, _ := APIKeyFrom(r.Context())

	if res.Err != nil && res.Status == 0 {
		// Nothing reached the client, but something was attempted and it is
		// the failures that are worth having a record of.
		s.recordUsage(store.UsageEvent{
			At: started, KeyID: key.ID, KeyName: key.Name,
			AccountID: res.AccountID, AccountEmail: res.AccountEmail,
			Model: model, Path: route, Status: 0, Streaming: streaming,
			Duration: elapsed, Error: res.Err.Error(),
		})
		s.relayFailure(w, r, res.Err)
		return
	}

	s.recordUsage(store.UsageEvent{
		At: started, KeyID: key.ID, KeyName: key.Name,
		AccountID: res.AccountID, AccountEmail: res.AccountEmail,
		Model: model, Path: route, Status: res.Status, Streaming: streaming,
		InputTokens:      res.Usage.InputTokens,
		OutputTokens:     res.Usage.OutputTokens,
		CacheReadTokens:  res.Usage.CacheReadTokens,
		CacheWriteTokens: res.Usage.CacheCreationTokens,
		Duration:         elapsed,
		Error:            res.StreamError,
	})

	attrs := []any{
		"model", model,
		"stream", streaming,
		"status", res.Status,
		"account", res.AccountEmail,
		"attempts", res.Attempts,
		"bytes", res.BytesOut,
		"duration_ms", elapsed.Milliseconds(),
		"request_id", requestIDFrom(r.Context()),
	}
	if res.Usage.InputTokens > 0 || res.Usage.OutputTokens > 0 {
		attrs = append(attrs,
			"in_tokens", res.Usage.InputTokens,
			"out_tokens", res.Usage.OutputTokens,
			"cache_read", res.Usage.CacheReadTokens)
	}
	if res.StreamError != "" {
		attrs = append(attrs, "stream_error", res.StreamError)
		s.log.Warn("upstream failed mid-stream", attrs...)
		return
	}
	s.log.Info("relayed", attrs...)
}

// relayFailure answers when no bytes reached the client. The shape is
// Anthropic's own error envelope so a Claude Code client can parse it.
func (s *Server) relayFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pool.ErrNoAccounts):
		writeError(w, http.StatusServiceUnavailable, "no_accounts",
			"no Claude account is connected; add one in the admin UI")
	case errors.Is(err, pool.ErrAllCoolingUp):
		if wait := s.pool.RetryAfter(); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		writeError(w, http.StatusServiceUnavailable, "overloaded_error",
			"every connected account is rate limited or cooling down")
	case r.Context().Err() != nil:
		// The caller hung up; there is nobody left to answer.
		return
	default:
		s.log.Error("relay failed", "err", err, "request_id", requestIDFrom(r.Context()))
		writeError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
	}
}

// peekModel reads the model and stream flag without disturbing the body.
func peekModel(body []byte) (model string, streaming bool) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Model, probe.Stream
}
