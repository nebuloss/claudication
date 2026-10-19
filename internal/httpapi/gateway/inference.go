package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudication/internal/api"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/pool"
	"claudication/internal/relay"
	"claudication/internal/relay/passes"
	"claudication/internal/request"
	"claudication/internal/service/titles"
	"claudication/internal/store"
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
func (s *Gateway) inference(p api.Protocol, route, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Counted, so memory is only handed back once nothing is in flight.
		defer s.trimmer.Begin()()

		body, err := httpx.ReadSized(r.Body, r.ContentLength, s.cfg.Limits.MaxBodyBytes)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, "could not read the request body")
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
		if body, err = httpx.DecodeBody(r, body, s.cfg.Limits.MaxBodyBytes); err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, err.Error())
			return
		}

		// For the Anthropic surface this only peeks at the model and stream
		// flag. For a translating one it builds a different request entirely,
		// and a body it cannot read is the caller's fault, not the upstream's.
		ex, err := p.Decode(body)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, err.Error())
			return
		}

		outbound := ex.Request()
		model, streaming := ex.Model(), ex.Streaming()

		// A model this key has been kept off, refused before anything is spent
		// and recorded under the key, because it is a client to reconfigure
		// rather than a stranger. 403 permission_error, as Anthropic answers a
		// credential not allowed something: a client stops there rather than
		// retrying, and the message says who can change it.
		if key, ok := httpx.APIKey(r.Context()); ok && !key.Allows(model) {
			ev := rejection(r, key, http.StatusForbidden, "model not allowed for this API key")
			ev.Model, ev.Path = model, route
			s.recorder.Record(ev, 0)
			p.WriteError(w, http.StatusForbidden, api.KindPermission,
				"this API key may not use model "+model+
					"; an administrator can allow it on the key's Models page")
			return
		}

		// A model role — claude-large, claude-small — becomes the real model
		// it is bound to for this key, else for this client, else for the
		// gateway, else the newest of its family; the answer is renamed back
		// on its way out. One model, no fallback: when it cannot serve, the
		// request is refused saying which one to change. See passes.SetModel.
		//
		// The client is the one the User-Agent names, as the request log
		// records it, so a binding made for "Codex" is the one the log's
		// Codex rows were served by.
		virtual := ""
		rewrote := false
		key, _ := httpx.APIKey(r.Context())
		client := api.ClientName(r.UserAgent())
		if s.models.IsAlias(r.Context(), model) {
			role := store.ModelKey(model)
			target, why := s.models.RoleTarget(r.Context(), key, client, role)
			if why != "" {
				ev := rejection(r, key, http.StatusNotFound, why)
				ev.Model, ev.VirtualModel, ev.Path = target, role, route
				s.recorder.Record(ev, 0)
				p.WriteError(w, http.StatusNotFound, api.KindNotFound,
					why+"; an administrator can change "+role+" in Settings › Aliases")
				return
			}
			replaced, ok := passes.SetModel(outbound, target)
			if !ok {
				p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, "the request names no model to map")
				return
			}
			outbound, virtual, model, rewrote = replaced, role, target, true
			s.log.Debug("model role", "role", role, "serving", target, "client", client,
				"request_id", httpx.RequestID(r.Context()))
		}

		// Read before the clone, and before Headers strips anything.
		//
		// For a translating dialect the headers naming the conversation are
		// exactly the ones that must not travel upstream — Codex's session_id
		// is in dialectHeaders — so after the two lines below they are gone.
		// This is the only point at which the caller's own request is still
		// intact.
		conversation := p.ConversationID(r.Header, body)
		// Which machine made this. A key says who is paying and a client says
		// what they are running; neither says where it ran, and when one host
		// out of several is burning a subscription that is the only question
		// worth asking. Read here with the rest of what the caller tells us
		// about itself.
		ip := httpx.ClientIP(r.Context())

		ctx, cancel := httpx.TimeoutContext(r, relay.Timeout(streaming))
		defer cancel()
		// Cloned rather than re-contexted, because the protocol is about to
		// edit the headers and they must not be the caller's own map.
		r = r.Clone(ctx)
		ex.Headers(r.Header)

		// Peeked from the bytes actually going upstream, which for a
		// translating protocol are not the ones that arrived. A dialect that
		// sends the caller's bytes as they are has already read it; a second
		// Peek was one more full parse of every request for nothing.
		var prologue request.Prologue
		if pe, ok := ex.(peekedExchange); ok && !rewrote {
			prologue = pe.Prologue()
		} else {
			prologue = request.Peek(outbound)
		}

		// The answer names the model the client asked for. Wrapped beneath
		// the dialect's sink, so the Responses translation is renamed too.
		if virtual != "" {
			renamer := passes.NewModelRenamer(w, virtual, p.ModelEvents())
			defer renamer.Finish()
			w = renamer
		}

		// A client naming its own conversation is answered with the name it
		// will display. Tee that one answer so the gateway can read it; every
		// other request is untouched, and the tee never gates the write.
		sink := ex.Sink(w)
		var captured *titles.CaptureSink
		if conversation != "" && s.titles.Capturing() && titles.IsClientTitleRequest(outbound) {
			captured = &titles.CaptureSink{Sink: sink}
			sink = captured
		}

		// The body outlives the relay only if a title may be asked for it.
		//
		// Anything still referring to it after Do keeps it in memory for the
		// whole stream — minutes, for a body that is routinely megabytes, once
		// per open stream. Titles are off by default and only want a chat that
		// has no name yet, so for nearly every request this is nil and the
		// body can go as soon as the upstream has it.
		var titleBody []byte
		if conversation != "" && s.titles.On() {
			titleBody = outbound
		}

		started := time.Now()
		res := s.relay.Do(sink, r, upstreamPath, outbound, prologue)
		elapsed := time.Since(started)

		// The protocol's last word, before anything else answers. A dialect
		// whose stream must end in a terminal event gets to send one here even
		// when the upstream simply stopped.
		sink.Close(closingCause(res))

		if res.Err != nil && res.Status == 0 {
			// Nothing reached the client, but something was attempted and it
			// is the failures that are worth having a record of.
			s.recorder.Record(store.UsageEvent{
				At: started, KeyID: key.ID, KeyName: key.Name,
				AccountID: res.AccountID, AccountEmail: res.AccountEmail, Provider: res.Provider,
				Model: model, VirtualModel: virtual, Path: route, Status: 0, Streaming: streaming,
				ConversationID: conversation, Client: client, IP: ip,
				Duration: elapsed, Error: res.Err.Error(),
			}, key.TokenBudget)
			s.relayFailure(w, r, p, model, res.Err)
			return
		}

		s.recorder.Record(store.UsageEvent{
			At: started, KeyID: key.ID, KeyName: key.Name,
			AccountID: res.AccountID, AccountEmail: res.AccountEmail, Provider: res.Provider,
			Model: model, VirtualModel: virtual, Path: route, Status: res.Status, Streaming: streaming,
			ConversationID:   conversation,
			Client:           client,
			IP:               ip,
			InputTokens:      res.Usage.InputTokens,
			OutputTokens:     res.Usage.OutputTokens,
			CacheReadTokens:  res.Usage.CacheReadTokens,
			CacheWriteTokens: res.Usage.CacheCreationTokens,
			ReasoningTokens:  res.Usage.ReasoningTokens,
			Duration:         elapsed,
			FirstToken:       res.FirstContent,
			// A stream that died after its 200 is the more specific fact, so
			// it wins; otherwise record whatever the upstream said when it
			// refused.
			Error: firstNonEmpty(res.StreamError, res.UpstreamError),
		}, key.TokenBudget)

		// Both after the answer, never before it: naming a chat is a
		// convenience and the client's turn must not wait on one.
		if captured != nil && res.Status == http.StatusOK {
			s.titles.CaptureTitle(conversation, model, captured.Seen.Bytes())
		}
		// Returns at once unless titling is on and this conversation has no
		// name yet.
		s.titles.Consider(titles.Request{
			Conversation: conversation,
			Model:        model,
			AccountID:    res.AccountID,
			Body:         titleBody,
		})

		attrs := []any{
			"api", p.ID(),
			"model", model,
			"stream", streaming,
			"status", res.Status,
			"account", res.AccountEmail,
			"attempts", res.Attempts,
			"bytes", res.BytesOut,
			"duration_ms", elapsed.Milliseconds(),
			"request_id", httpx.RequestID(r.Context()),
		}
		if res.FirstContent > 0 {
			attrs = append(attrs, "first_token_ms", res.FirstContent.Milliseconds())
		}
		if res.Stalls > 0 {
			attrs = append(attrs, "stalls", res.Stalls)
		}
		// Which passes changed the body, so a request that was rewritten says
		// so without debug logging. See internal/relay/passes.
		if len(res.Rewrites) > 0 {
			attrs = append(attrs, "rewrites", strings.Join(res.Rewrites, ","))
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
func closingCause(res relay.Result) error {
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
func (s *Gateway) relayFailure(w http.ResponseWriter, r *http.Request, p api.Protocol, model string, err error) {
	switch {
	case errors.Is(err, pool.ErrNoAccounts):
		p.WriteError(w, http.StatusServiceUnavailable, api.KindNoAccounts,
			"no Claude account is connected; add one in the admin UI")
	case errors.Is(err, pool.ErrModelOff):
		// 404 and not_found_error, as Anthropic answers a model it does not
		// serve: that is what this is, from the client's side, and it stops
		// a client retrying rather than waiting out a cooldown that is not
		// coming. The message says who can change it, because the person
		// reading it in a client log is usually not the one who switched it.
		p.WriteError(w, http.StatusNotFound, api.KindNotFound,
			"model "+model+" is turned off on this gateway for every connected account; "+
				"an administrator can turn it on under Accounts")
	case errors.Is(err, pool.ErrAllCoolingUp):
		if wait := s.pool.RetryAfter(r.Context(), s.relay.Wire.ID(), model); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		p.WriteError(w, http.StatusServiceUnavailable, api.KindOverloaded,
			"every connected account is rate limited or cooling down")
	case r.Context().Err() != nil:
		// The caller hung up; there is nobody left to answer.
		return
	default:
		s.log.Error("relay failed", "err", err, "request_id", httpx.RequestID(r.Context()))
		p.WriteError(w, http.StatusBadGateway, api.KindAPI, "upstream request failed: "+err.Error())
	}
}

// peekedExchange is an exchange that has already read its request's prologue:
// one that sends the caller's bytes upstream as they arrived, and so read it
// while decoding. The Anthropic dialect is one.
//
// Named here rather than on api.Exchange because the prologue is
// internal/request's type, and api sits on the same layer as request and so
// may not import it. Optional for the same reason: a translating dialect
// builds a new body and has nothing to hand over, and the gateway peeks that
// body itself.
type peekedExchange interface {
	api.Exchange
	Prologue() request.Prologue
}
