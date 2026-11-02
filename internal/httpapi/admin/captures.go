package admin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/capture"
	"claudication/internal/version"
)

// Recording a client's content (internal/service/capture): switched on per
// client for a while, held in memory, shown per request and exported as HAR.

// handleSetClientCapture switches recording on for a client, for the minutes
// asked or the default hour; asking again restarts the clock.
func (s *Admin) handleSetClientCapture(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	var body struct {
		Minutes int `json:"minutes"`
	}
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Minutes < 0 {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "minutes is a positive number")
		return
	}
	until := s.capture.Record(c.ID, time.Duration(body.Minutes)*time.Minute)
	s.log.Info("client content recording on", "id", c.ID, "until", until.UTC().Format(time.RFC3339))
	s.writeClient(w, r, c.ID, nil)
}

// handleStopClientCapture switches it off. What it recorded stays.
func (s *Admin) handleStopClientCapture(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadClient(w, r)
	if !ok {
		return
	}
	s.capture.Stop(c.ID)
	s.log.Info("client content recording off", "id", c.ID)
	s.writeClient(w, r, c.ID, nil)
}

// handleCaptures lists what is held — for one client with ?key=, else all —
// with the recorder's state.
func (s *Admin) handleCaptures(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":   s.capture.Status(),
		"captures": s.capture.List(key),
	})
}

// handleClearCaptures drops what is held, for one client or all.
func (s *Admin) handleClearCaptures(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	s.capture.Clear(key)
	s.log.Info("recorded content cleared", "key", key)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Admin) loadCapture(w http.ResponseWriter, r *http.Request) (*capture.Exchange, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such recording")
		return nil, false
	}
	x, ok := s.capture.Get(id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound,
			"no such recording: it was dropped to make room, cleared, or lost when the gateway restarted")
		return nil, false
	}
	return x, true
}

// handleCapture is one exchange, every leg with its bodies.
func (s *Admin) handleCapture(w http.ResponseWriter, r *http.Request) {
	x, ok := s.loadCapture(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.exchangeJSON(x))
}

// handleCaptureHAR downloads one exchange as HAR.
func (s *Admin) handleCaptureHAR(w http.ResponseWriter, r *http.Request) {
	x, ok := s.loadCapture(w, r)
	if !ok {
		return
	}
	s.writeHAR(w, []*capture.Exchange{x}, fmt.Sprintf("claudication-%d.har", x.ID))
}

// handleExportCaptures downloads what is held, for one client or all, as HAR.
func (s *Admin) handleExportCaptures(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	name := "claudication-" + time.Now().UTC().Format("20060102-150405") + ".har"
	s.writeHAR(w, s.capture.Exchanges(key), name)
}

func (s *Admin) writeHAR(w http.ResponseWriter, xs []*capture.Exchange, name string) {
	b, err := capture.HAR(xs, "claudication", version.Version)
	if err != nil {
		httpx.Internal(w, s.log, "write the HAR", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(b)
}

// Normalizer reads one leg's request and answer, each in its dialect, into
// Anthropic Messages — the gateway's common form — for the admin UI to draw
// every API's content one way. The composition root supplies it, since only
// it may import the dialects.
type Normalizer func(dialect string, request []byte, status int, header http.Header, answer []byte) Normalized

// Normalized is a leg read into Messages: the request, and the answer as one
// message, each nil with the reason it could not be read.
type Normalized struct {
	Request      json.RawMessage
	RequestError string
	Answer       json.RawMessage
	AnswerError  string
}

// ExchangeJSON is one recorded exchange as the admin UI reads it.
type ExchangeJSON struct {
	capture.Summary
	Client   LegJSON   `json:"client"`
	Upstream []LegJSON `json:"upstream"`
}

// LegJSON is one side's request and answer.
type LegJSON struct {
	Dialect  string       `json:"dialect"`
	Account  string       `json:"account,omitempty"`
	Started  time.Time    `json:"started"`
	Ended    time.Time    `json:"ended,omitzero"`
	Error    string       `json:"error,omitempty"`
	Request  MessageJSON  `json:"request"`
	Response *MessageJSON `json:"response"`
}

// MessageJSON is a request or an answer.
type MessageJSON struct {
	Method string      `json:"method,omitempty"`
	URL    string      `json:"url,omitempty"`
	Status int         `json:"status,omitempty"`
	Header [][2]string `json:"header"`
	Body   BodyJSON    `json:"body"`
	// Messages is the body read into Anthropic Messages, when it could be,
	// and MessagesError why when it could not.
	Messages      json.RawMessage `json:"messages,omitempty"`
	MessagesError string          `json:"messages_error,omitempty"`
}

// BodyJSON is a body's text, or its bytes in base64 when they are not text.
type BodyJSON struct {
	Text      string `json:"text"`
	Base64    bool   `json:"base64,omitempty"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated,omitempty"`
	// Parts is how many writes it arrived in: one for a whole body, about
	// one per event for a stream.
	Parts int `json:"parts"`
	// LastMS is when its last part arrived, from the exchange's start.
	LastMS int64 `json:"last_ms,omitempty"`
}

func (s *Admin) exchangeJSON(x *capture.Exchange) ExchangeJSON {
	out := ExchangeJSON{Summary: x.Summary(), Client: s.legJSON(&x.Client), Upstream: make([]LegJSON, 0, len(x.Upstream))}
	for _, l := range x.Upstream {
		out.Upstream = append(out.Upstream, s.legJSON(l))
	}
	return out
}

func (s *Admin) legJSON(l *capture.Leg) LegJSON {
	out := LegJSON{
		Dialect: l.Dialect, Account: l.Account, Started: l.Started, Ended: l.Ended, Error: l.Error,
		Request: messageJSON(&l.Request),
	}
	var answer []byte
	var status int
	var header http.Header
	if l.Response != nil {
		m := messageJSON(l.Response)
		out.Response = &m
		answer, status, header = l.Response.Body.Bytes(), l.Response.Status, l.Response.Header
	}
	if s.normalize == nil {
		return out
	}
	// A cut body is not a document: what is kept of it is shown raw.
	if l.Request.Body.Truncated() {
		out.Request.MessagesError = "it was cut at the limit per body"
		return out
	}
	if l.Response != nil && l.Response.Body.Truncated() {
		answer = nil
		out.Response.MessagesError = "it was cut at the limit per body"
	}
	n := s.normalize(l.Dialect, l.Request.Body.Bytes(), status, header, answer)
	out.Request.Messages, out.Request.MessagesError = n.Request, n.RequestError
	if out.Response != nil && answer != nil {
		out.Response.Messages, out.Response.MessagesError = n.Answer, n.AnswerError
	}
	return out
}

func messageJSON(m *capture.Message) MessageJSON {
	out := MessageJSON{Method: m.Method, URL: m.URL, Status: m.Status, Header: [][2]string{}}
	names := make([]string, 0, len(m.Header))
	for k := range m.Header {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range m.Header[k] {
			out.Header = append(out.Header, [2]string{k, v})
		}
	}
	b := m.Body.Bytes()
	chunks := m.Body.Chunks()
	out.Body = BodyJSON{Size: m.Body.Size(), Truncated: m.Body.Truncated(), Parts: len(chunks)}
	if len(chunks) > 0 {
		out.Body.LastMS = chunks[len(chunks)-1].AtMS
	}
	if utf8.Valid(b) {
		out.Body.Text = string(b)
	} else {
		out.Body.Text = base64.StdEncoding.EncodeToString(b)
		out.Body.Base64 = true
	}
	return out
}
