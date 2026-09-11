package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"claudication/internal/api"
	"claudication/internal/pool"
	"claudication/internal/store"
	"claudication/internal/upstream"
)

// inference is the one path every client-facing dialect takes.
//
// Everything that is the same for all of them lives here — reading the body,
// gunzipping it, the timeout, the relay, usage accounting, the log line — and
// everything that differs lives behind api.Protocol. The Anthropic surface is
// the identity protocol, so this function has no idea which dialect it is
// serving, and adding a third surface does not change it.
//
// It still does almost nothing to the request itself. It does not reshape the
// system array, filter events, normalise errors or inspect capabilities: every
// one of those would break something the gateway contract requires, and most
// of them are the defects found in auth2api. The body is read only because a
// retry on another account has to replay it.
func (s *Server) inference(p api.Protocol, route, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, "invalid_request", "could not read the request body")
			return
		}

		// A compressed body has to be opened before anything can read it.
		//
		// Claude Code gzips request bodies past about 4 KB, and every pass
		// that looks at the body — the attribution block above all — silently
		// does nothing on bytes it cannot parse. The failure is not a parse
		// error, it is a 429 whose message is the single word "Error": the
		// attribution gate, which reads exactly like rate limiting and is not.
		// Measured on the same account in the same minute, an 11 KB body was
		// served at 200 as identity and refused at 429 gzipped.
		if body, err = decodeBody(r, body, s.cfg.Limits.MaxBodyBytes); err != nil {
			p.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		// For the Anthropic surface this only peeks at the model and stream
		// flag. For a translating one it builds a different request entirely,
		// and a body it cannot read is the caller's fault, not the upstream's.
		ex, err := p.Decode(body)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}

		outbound := ex.Request()
		model, streaming := ex.Model(), ex.Streaming()

		ctx, cancel := contextWithTimeout(r, upstream.Timeout(streaming))
		defer cancel()
		// Cloned rather than re-contexted, because the protocol is about to
		// edit the headers and they must not be the caller's own map.
		r = r.Clone(ctx)
		ex.Headers(r.Header)

		// Peeked from the bytes actually going upstream, which for a
		// translating protocol are not the ones that arrived.
		prologue := upstream.Peek(outbound)

		sink := ex.Sink(w)
		started := time.Now()
		res := s.relay.Do(sink, r, "anthropic", upstreamPath, outbound, prologue)
		elapsed := time.Since(started)

		// The protocol's last word, before anything else answers. A dialect
		// whose stream must end in a terminal event gets to send one here even
		// when the upstream simply stopped.
		sink.Close(closingCause(res))

		key, _ := APIKeyFrom(r.Context())

		if res.Err != nil && res.Status == 0 {
			// Nothing reached the client, but something was attempted and it
			// is the failures that are worth having a record of.
			s.recordUsage(store.UsageEvent{
				At: started, KeyID: key.ID, KeyName: key.Name,
				AccountID: res.AccountID, AccountEmail: res.AccountEmail,
				Model: model, Path: route, Status: 0, Streaming: streaming,
				Duration: elapsed, Error: res.Err.Error(),
			}, key.TokenBudget)
			s.relayFailure(w, r, p, res.Err)
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
			// A stream that died after its 200 is the more specific fact, so
			// it wins; otherwise record whatever the upstream said when it
			// refused.
			Error: firstNonEmpty(res.StreamError, res.UpstreamError),
		}, key.TokenBudget)

		attrs := []any{
			"api", p.ID(),
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
}

// closingCause is what to tell the protocol ended the exchange: the gateway's
// own failure to get an answer, or the upstream's failure part-way through one.
func closingCause(res upstream.Result) error {
	if res.Err != nil {
		return res.Err
	}
	if res.StreamError != "" {
		return errors.New(res.StreamError)
	}
	return nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// relayFailure answers when no bytes reached the client. The shape is the
// caller's own error envelope, because an error a client cannot parse is an
// error it cannot act on.
func (s *Server) relayFailure(w http.ResponseWriter, r *http.Request, p api.Protocol, err error) {
	switch {
	case errors.Is(err, pool.ErrNoAccounts):
		p.WriteError(w, http.StatusServiceUnavailable, "no_accounts",
			"no Claude account is connected; add one in the admin UI")
	case errors.Is(err, pool.ErrAllCoolingUp):
		if wait := s.pool.RetryAfter(); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		p.WriteError(w, http.StatusServiceUnavailable, "overloaded_error",
			"every connected account is rate limited or cooling down")
	case r.Context().Err() != nil:
		// The caller hung up; there is nobody left to answer.
		return
	default:
		s.log.Error("relay failed", "err", err, "request_id", requestIDFrom(r.Context()))
		p.WriteError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error())
	}
}
