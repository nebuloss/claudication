package gemini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// ID names this surface in the settings table, the admin UI and the log. It
// is also the dialect Code Assist is reached in, whose envelope is this
// surface's own request wrapped (native.go).
const ID = "gemini"

// The routes, as the gateway names them. The Gemini API puts the model and
// the method in the path — /v1beta/models/gemini-2.5-pro:streamGenerateContent
// — so one pattern serves every model, and the method is the route: the
// gateway folds the path into an envelope (Envelope) and hands it to the
// pipeline under one of these.
const (
	StreamRoute   = "/v1beta/models:streamGenerateContent"
	GenerateRoute = "/v1beta/models:generateContent"
	CountRoute    = "/v1beta/models:countTokens"
)

// Methods are the methods served, by their name in the path, and the route
// each one is.
var Methods = map[string]string{
	"streamGenerateContent": StreamRoute,
	"generateContent":       GenerateRoute,
	"countTokens":           CountRoute,
}

// API serves the Gemini API, which is what gemini-cli speaks when it is
// pointed at a gateway: GOOGLE_GEMINI_BASE_URL with GEMINI_API_KEY, its
// "gateway" auth type (packages/core/src/core/contentGenerator.ts at
// a713410), which sends @google/genai's own requests to the address given,
// the key in x-goog-api-key. See doc.go.
type API struct{}

// New returns the surface.
func New() API { return API{} }

func (API) ID() string    { return ID }
func (API) Title() string { return "Gemini API" }

func (API) Routes() []string {
	return []string{
		"/v1beta/models/{model}:streamGenerateContent",
		"/v1beta/models/{model}:generateContent",
		"/v1beta/models/{model}:countTokens",
	}
}

// envelope is a request as the gateway hands it to this surface: the body as
// the client sent it, with the model and method the path named.
type envelope struct {
	Model   string          `json:"model"`
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

// Envelope folds what the path says into the body: the gateway's pipeline
// reads a request's model from its body, as every other dialect carries it
// there.
func Envelope(model, method string, body []byte) []byte {
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	out, _ := json.Marshal(envelope{Model: model, Method: method, Request: body})
	return out
}

func readEnvelope(body []byte) (envelope, error) {
	var e envelope
	if err := json.Unmarshal(body, &e); err != nil {
		return envelope{}, fmt.Errorf("not a Gemini request: %w", err)
	}
	if e.Model == "" {
		return envelope{}, fmt.Errorf("no model in the request path")
	}
	if _, ok := Methods[e.Method]; !ok {
		return envelope{}, fmt.Errorf("the Gemini API method %q is not served", e.Method)
	}
	return e, nil
}

// Decode reads a Gemini request into Messages, for Claude — and, through
// Messages, for ChatGPT (api.Chain).
func (API) Decode(body []byte) (api.Exchange, error) {
	e, err := readEnvelope(body)
	if err != nil {
		return nil, err
	}
	tr, err := toMessages(e)
	if err != nil {
		return nil, err
	}
	if e.Method == "countTokens" {
		return countExchange{model: e.Model, estimate: tr.estimate}, nil
	}
	return &messagesExchange{tr: tr}, nil
}

// DecodeNative reads a Gemini request for Code Assist, which takes this
// surface's own request wrapped (native.go): the gateway's exchange for a
// Gemini model. Any other upstream is reached through Decode.
func (API) DecodeNative(body []byte) (api.Exchange, error) {
	e, err := readEnvelope(body)
	if err != nil {
		return nil, err
	}
	if e.Method == "countTokens" {
		tr, err := toMessages(e)
		if err != nil {
			return nil, err
		}
		return countExchange{model: e.Model, estimate: tr.estimate}, nil
	}
	return newNative(e)
}

// WriteError writes Google's error envelope, google.rpc.Status, which is what
// @google/genai parses: code, message, and the status name that goes with
// the code.
func (API) WriteError(w http.ResponseWriter, status int, kind api.ErrorKind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(errorBody(status, kindStatus(kind, status), message))
}

func errorBody(code int, status, message string) []byte {
	body, _ := json.Marshal(geminiwire.ErrorEnvelope{Error: geminiwire.Status{Code: code, Message: message, Status: status}})
	return body
}

// kindStatus is the canonical status name for a gateway refusal. One case per
// api.ErrorKind; TestEveryErrorKindHasAStatus holds the mapping whole.
func kindStatus(kind api.ErrorKind, code int) string {
	switch kind {
	case api.KindInvalidRequest:
		return "INVALID_ARGUMENT"
	case api.KindAuthentication:
		return "UNAUTHENTICATED"
	case api.KindPermission:
		return "PERMISSION_DENIED"
	case api.KindNotFound:
		return "NOT_FOUND"
	case api.KindRateLimit:
		return "RESOURCE_EXHAUSTED"
	case api.KindOverloaded, api.KindNoAccounts:
		return "UNAVAILABLE"
	case api.KindAPI:
		return "INTERNAL"
	}
	return statusName(code)
}

// statusName is the canonical name for an HTTP status, as Google pairs them.
func statusName(code int) string {
	switch code {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 409:
		return "ABORTED"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case 501:
		return "UNIMPLEMENTED"
	case 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	return "INTERNAL"
}

// ConversationID names the chat. Nothing gemini-cli sends in gateway mode
// names one: its session id goes into Code Assist's body only on its own
// Code Assist path, and @google/genai's requests carry none. So the API's
// shared readers decide, which for this client find nothing.
func (API) ConversationID(h http.Header, _ []byte) string {
	return api.HeaderID(h, "X-Session-Id")
}

// ModelEvents is none: a Gemini stream names no events, and the model a turn
// was served by is in every chunk's modelVersion, which is the backend's
// own word for it and is left as it is.
func (API) ModelEvents() []string { return nil }
