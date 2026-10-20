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
	"claudication/internal/service/clients"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/titles"
	"claudication/internal/store"
)

// inference is the one path every client-facing dialect takes.
//
// Everything that is the same for all of them lives here — reading the body,
// gunzipping it, choosing the upstream, the timeout, the relay, usage
// accounting, the log line — and everything that differs lives behind
// api.Protocol. The Anthropic surface is the identity protocol, so this
// function has no idea which dialect it is serving, and adding a third surface
// does not change it.
//
// It still does almost nothing to the request itself. It does not reshape the
// system array, filter events, normalise errors or inspect capabilities: every
// one of those would break something the gateway contract requires, and most
// of them are the defects found in auth2api. The body is read only because a
// retry on another account has to replay it.
func (s *Gateway) inference(p api.Protocol, route string) http.HandlerFunc {
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

		// The caller's request as it came, read for its model before anything
		// is translated: which upstream serves it is decided by that model,
		// and a request going to an upstream that speaks its dialect is not
		// translated at all. For the Anthropic surface this is the exchange
		// itself, which only peeks at the model and stream flag.
		first, err := decodeNative(p, body)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, err.Error())
			return
		}
		requested, streaming := first.Model(), first.Streaming()

		// Where it goes. A client that names its own slots asks for one, and
		// it becomes the real model the slot is bound to or follows now; the
		// answer is renamed back on its way out. Any other name it sends is
		// refused, listing its names. A direct-mode client names real models.
		// One model, no fallback: when it cannot serve, the request is refused
		// saying which slot to change. The provider is the one whose accounts
		// serve that model. See modelsvc.Route.
		//
		// The product the User-Agent names is recorded beside the key, as it
		// always was: which tool a client's key was used from.
		key, _ := httpx.APIKey(r.Context())
		// Open until the answer is done, refusals included: what the
		// client's Limits page shows it doing now.
		defer s.inflight.Begin(key.ID)()
		client := api.ClientName(r.UserAgent())
		dest := s.models.Route(r.Context(), key, requested)
		model, slot := dest.Model, dest.Slot

		// A model a direct-mode client has switched off, refused before
		// anything is spent and recorded under the key, because it is a client
		// to reconfigure rather than a stranger. 403 permission_error, as
		// Anthropic answers a credential not allowed something: a client stops
		// there rather than retrying, and the message says who can change it.
		// A client naming its slots is held to its slots instead (Route).
		if !key.NamesSlots() && requested != "" && !key.Allows(requested) {
			ev := rejection(r, key, http.StatusForbidden, "model not allowed for this client")
			ev.Model, ev.Path = requested, route
			s.recorder.Record(ev, 0)
			p.WriteError(w, http.StatusForbidden, api.KindPermission,
				"this client may not use model "+requested+
					"; an administrator can switch it on on the client's Models page")
			return
		}
		if dest.Why != "" {
			ev := rejection(r, key, http.StatusNotFound, dest.Why)
			ev.Model, ev.VirtualModel, ev.Path = dest.Model, dest.Slot, route
			if dest.Slot == "" {
				ev.VirtualModel = requested
			}
			s.recorder.Record(ev, 0)
			p.WriteError(w, http.StatusNotFound, api.KindNotFound, dest.Why)
			return
		}
		up, known := s.upstreams[dest.Provider]
		upstreamPath, reachable := up.Paths[route]
		if !known || !reachable {
			// The model is served, but not to this API: a ChatGPT model asked
			// for over Anthropic Messages. Translating Messages into
			// Responses is a mapping of its own, with its own pitfalls, and
			// not one this gateway has; refusing with what to change beats
			// sending a Claude Code turn somewhere that cannot read it.
			why := unreachable(p, up, dest, requested)
			ev := rejection(r, key, http.StatusBadRequest, why)
			ev.Model, ev.VirtualModel, ev.Path = dest.Model, dest.Slot, route
			s.recorder.Record(ev, 0)
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, why)
			return
		}

		// The request as it goes upstream: as it came, to an upstream that
		// speaks the caller's dialect; translated, to one that does not.
		ex := first
		if up.Native != p.ID() {
			if ex, err = p.Decode(body); err != nil {
				p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, err.Error())
				return
			}
		}
		outbound := ex.Request()
		rewrote := false
		if slot != "" {
			replaced, ok := passes.SetModel(outbound, model)
			if !ok {
				p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, "the request names no model to map")
				return
			}
			outbound, rewrote = replaced, true
			s.log.Debug("client model name", "slot", slot, "serving", model, "provider", dest.Provider,
				"client", key.Name, "request_id", httpx.RequestID(r.Context()))
		} else if up.Native != p.ID() {
			// Translated with no slot: the dialect chose the model it sends,
			// and that is the one served and recorded.
			model = ex.Model()
		}

		// Read before the clone, and before Headers strips anything.
		//
		// For a translating dialect the headers naming the conversation are
		// exactly the ones that must not travel upstream — Codex's session_id
		// is in dialectHeaders — so after the two lines below they are gone.
		// This is the only point at which the caller's own request is still
		// intact.
		//
		// Where the client's type says it names its chat first (its spec's
		// Sessions, internal/service/clients), then where the API reads any
		// client's, which is all a custom client has.
		spec, typed := clients.Find(key.ClientType)
		conversation := ""
		if typed {
			conversation = api.HeaderID(r.Header, spec.Sessions...)
		}
		if conversation == "" {
			conversation = p.ConversationID(r.Header, body)
		}
		// Which machine made this. A key says who is paying and a client says
		// what they are running; neither says where it ran, and when one host
		// out of several is burning a subscription that is the only question
		// worth asking. Read here with the rest of what the caller tells us
		// about itself.
		ip := httpx.ClientIP(r.Context())

		ctx, cancel := httpx.TimeoutContext(r, relay.Timeout(streaming))
		defer cancel()
		// Named for the pool, which keeps a conversation on the account that
		// served it last where the provider wants that (ChatGPT does: see
		// internal/pool, affinity.go).
		ctx = pool.WithConversation(ctx, conversation)
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
		if slot != "" {
			renamer := passes.NewModelRenamer(w, slot, p.ModelEvents())
			defer renamer.Finish()
			w = renamer
		}

		// A client naming its own conversation is answered with the name it
		// will display. Tee that one answer so the gateway can read it; every
		// other request is untouched, and the tee never gates the write.
		//
		// A type that names its own chats (crush, opencode) has its naming
		// request recognised by shape and filed as part of the chat's cost but
		// not one of its turns; a custom client's is noticed by the same
		// shape. Either way the answer is read only while the client's "Name
		// chats" switch is on, and only a client that may send one is looked
		// at — Claude Code and Codex never do, and parsing each of their small
		// requests would be spent for nothing. Only a small body is looked at:
		// a naming request is one message, and an agent turn is routinely
		// megabytes.
		// A type that names its own chats is looked at whatever the switch,
		// since its naming request is filed apart from its turns either way.
		mayName := spec.NamesItsChats || (spec.Custom && key.NameChats)
		naming := mayName && conversation != "" && len(outbound) < maxNamingBody && titles.IsClientTitleRequest(outbound)
		sink := ex.Sink(w)
		var captured *titles.CaptureSink
		if naming && key.NameChats {
			captured = &titles.CaptureSink{Sink: sink}
			sink = captured
		}
		naming = naming && spec.NamesItsChats

		// The body outlives the relay only if a title may be asked for it:
		// a client whose chats are named, of a type that does not name them
		// itself, on a request that is not a naming request.
		//
		// Anything still referring to it after Do keeps it in memory for the
		// whole stream — minutes, for a body that is routinely megabytes, once
		// per open stream — and the titler wants only a chat with no name yet,
		// or one due its one rename.
		var titleBody []byte
		if conversation != "" && key.NameChats && !spec.NamesItsChats && !naming {
			titleBody = outbound
		}

		started := time.Now()
		res := up.Relay.Do(sink, r, upstreamPath, outbound, prologue)
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
				Model: model, VirtualModel: slot, Path: route, Status: 0, Streaming: streaming,
				ConversationID: conversation, Client: client, IP: ip,
				Duration: elapsed, Error: res.Err.Error(), Naming: naming,
			}, key.TokenBudget)
			s.relayFailure(w, r, p, up, model, res.Err)
			return
		}

		s.recorder.Record(store.UsageEvent{
			At: started, KeyID: key.ID, KeyName: key.Name,
			AccountID: res.AccountID, AccountEmail: res.AccountEmail, Provider: res.Provider,
			Model: model, VirtualModel: slot, Path: route, Status: res.Status, Streaming: streaming,
			ConversationID:   conversation,
			Naming:           naming,
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
		if titleBody != nil {
			s.titles.Consider(titles.Request{
				Conversation: conversation,
				KeyID:        key.ID, KeyName: key.Name, Budget: key.TokenBudget,
				Allow:     func() bool { return s.spareFor(key) },
				Model:     model,
				AccountID: res.AccountID,
				Provider:  res.Provider,
				Body:      titleBody,
			})
		}

		attrs := []any{
			"api", p.ID(),
			"provider", dest.Provider,
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
func (s *Gateway) relayFailure(w http.ResponseWriter, r *http.Request, p api.Protocol, up Upstream, model string, err error) {
	switch {
	case errors.Is(err, pool.ErrNoAccounts):
		p.WriteError(w, http.StatusServiceUnavailable, api.KindNoAccounts,
			"model "+model+" is served by "+up.Title+" accounts, and no "+up.Title+
				" account is connected; add one in the admin UI")
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
		if wait := s.pool.RetryAfter(r.Context(), up.Relay.Wire.ID(), model); wait > 0 {
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

// maxNamingBody is the largest request looked at for a client's own naming
// request: one system prompt and one message. crush's sends the user's first
// message in it whole, so this is generous rather than tight.
const maxNamingBody = 256 << 10

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

// nativeDecoder is a dialect that can hand a request on as it came, to an
// upstream that speaks it: the OpenAI surface, to ChatGPT's backend. One that
// cannot — the Anthropic surface, whose Decode is already the identity — is
// decoded the ordinary way.
type nativeDecoder interface {
	DecodeNative(body []byte) (api.Exchange, error)
}

func decodeNative(p api.Protocol, body []byte) (api.Exchange, error) {
	if n, ok := p.(nativeDecoder); ok {
		return n.DecodeNative(body)
	}
	return p.Decode(body)
}

// unreachable says why a request was routed to an upstream its API cannot
// reach, and what to change: the slot it named, or the API to use instead.
func unreachable(p api.Protocol, up Upstream, dest modelsvc.Route, requested string) string {
	title := up.Title
	if title == "" {
		title = dest.Provider
	}
	what := "model " + dest.Model
	if dest.Slot != "" {
		what = dest.Slot + " (" + dest.Model + ")"
	}
	msg := what + " is served by a " + title + " account, which the " + p.Title() +
		" cannot reach on this gateway"
	switch {
	case dest.Slot != "":
		msg += "; an administrator can bind " + dest.Slot + " to a model this API can reach, or point the " +
			"client at the " + up.Title + " account's API, on the client's page"
	case up.Native != "":
		msg += "; ask for it over the API that account speaks, as Codex does over /v1/responses"
	case requested != "":
		msg += "; ask for a model this API can reach"
	}
	return msg
}
