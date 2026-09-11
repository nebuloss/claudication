// Package anthropic serves the Anthropic Messages API — the dialect the
// gateway already speaks upstream.
//
// It is the identity adapter, and keeping it an implementation rather than a
// bypass is what makes the abstraction worth having. The relay's contract —
// the caller's bytes go upstream unchanged, the answer comes back verbatim —
// is expressed here, once, as a Protocol that does nothing, instead of as a
// branch every other layer has to remember not to take.
package anthropic

import (
	"encoding/json"
	"net/http"

	"claudication/internal/api"
	"claudication/internal/upstream"
)

// ID names this surface in the settings table, the admin UI and the log.
const ID = "anthropic"

// API is the Anthropic Messages surface.
type API struct{}

// New returns the surface. It takes nothing: there is nothing to configure
// about relaying a request unchanged.
func New() API { return API{} }

func (API) ID() string    { return ID }
func (API) Title() string { return "Anthropic Messages API" }

func (API) Routes() []string {
	return []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/models"}
}

// Decode reads the model and stream flag and hands the body straight back.
//
// It deliberately does not decode the whole body. Peek reads the three fields
// the gateway needs; decoding the envelope into raw values used to copy the
// transcript twice per request just to learn a model name.
func (API) Decode(body []byte) (api.Exchange, error) {
	return exchange{body: body, prologue: upstream.Peek(body)}, nil
}

// WriteError writes Anthropic's own error envelope, which is what a Claude
// Code client parses.
func (API) WriteError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    kind,
			"message": message,
		},
	})
}

type exchange struct {
	body     []byte
	prologue upstream.Prologue
}

func (e exchange) Request() []byte { return e.body }
func (e exchange) Model() string   { return e.prologue.Model }
func (e exchange) Streaming() bool { return e.prologue.Stream }

// Headers does nothing, which is the point: the caller already speaks what the
// upstream speaks, so every header it sent is meaningful there and the relay's
// contract is to preserve them.
func (exchange) Headers(http.Header) {}

func (exchange) Sink(w http.ResponseWriter) api.Sink { return api.Passthrough(w) }
