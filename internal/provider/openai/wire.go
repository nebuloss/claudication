package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"claudication/internal/provider"
)

// ID is the provider name accounts are stored under. "openai", as models.dev
// names the vendor, so the catalogue's facts are found under the same key.
const ID = "openai"

// BaseURL is the Codex backend: where Codex sends a ChatGPT-signed-in turn.
// Not api.openai.com, which is the metered API and does not take a ChatGPT
// token. Codex: model-provider-info/src/lib.rs CHATGPT_CODEX_BASE_URL; crush:
// internal/oauth/openai/oauth.go.
const BaseURL = "https://chatgpt.com/backend-api/codex"

// backendURL is the rest of the ChatGPT backend, where the usage endpoint
// lives — beside /codex, not under it.
const backendURL = "https://chatgpt.com/backend-api"

// Originator is what the gateway calls itself when a caller named nothing.
// Never codex_cli_rs: a client that is not Codex is not made to look like
// Codex. crush sends "crush" for the same reason.
const Originator = "claudication"

// accountHeader names the ChatGPT workspace a request bills. Codex spells it
// ChatGPT-Account-ID (model-provider/src/bearer_auth_provider.rs); headers
// are case-insensitive, and Go canonicalises it to Chatgpt-Account-Id.
const accountHeader = "ChatGPT-Account-ID"

// Provider is ChatGPT's Codex backend, as the relay sees it.
type Provider struct{}

var _ provider.Wire = Provider{}

// ID implements provider.Wire.
func (Provider) ID() string { return ID }

// BaseURL implements provider.Wire.
func (Provider) BaseURL() string { return BaseURL }

// Authorize implements provider.Wire: the account's bearer token and the
// workspace it bills, and an originator when the caller sent none.
//
// That is all. Codex's own originator, User-Agent, session-id and x-codex-*
// headers are the caller's and travel as they came (the relay copies them);
// nothing here makes up one it did not send. Measured 2026-10-19: the backend
// answers 200 with no originator at all, so the fallback is honesty, not a
// requirement.
func (Provider) Authorize(out *http.Request, c provider.Credential) {
	out.Header.Set("Authorization", "Bearer "+c.Token)
	if c.AccountID != "" {
		out.Header.Set(accountHeader, c.AccountID)
	}
	if out.Header.Get("Originator") == "" {
		out.Header.Set("Originator", Originator)
	}
}

// Meter implements provider.Wire. As for Anthropic, the shape of the answer
// decides, not what was asked: a stream that fails before it starts answers
// in JSON.
func (Provider) Meter(contentType string, into provider.Reading) provider.Meter {
	if strings.Contains(contentType, "text/event-stream") {
		return newSSEScanner(into)
	}
	return newJSONUsage(into.Usage)
}

// The Responses stream's events, as Codex reads them
// (codex-api/src/sse/responses.rs) and as measured on 2026-10-19.
const (
	evCreated         = "response.created"
	evInProgress      = "response.in_progress"
	evCompleted       = "response.completed"
	evIncomplete      = "response.incomplete"
	evFailed          = "response.failed"
	evError           = "error"
	evItemAdded       = "response.output_item.added"
	evItemDone        = "response.output_item.done"
	evPartAdded       = "response.content_part.added"
	evPartDone        = "response.content_part.done"
	evTextDone        = "response.output_text.done"
	evMetadata        = "response.metadata"
	evCodexRateLimits = "codex.rate_limits"
)

// Event implements provider.Wire.
//
//	quiet     the response and its items opening, metadata, rate limits
//	settling  an item or a part finished, the response finished
//	error     response.failed, error
//	content   everything else: the deltas
func (Provider) Event(name []byte) provider.EventKind {
	switch string(name) {
	case evCreated, evInProgress, evItemAdded, evPartAdded, evMetadata, evCodexRateLimits:
		return provider.EventQuiet
	case evItemDone, evPartDone, evTextDone, evCompleted, evIncomplete:
		return provider.EventSettling
	case evFailed, evError:
		return provider.EventError
	}
	return provider.EventContent
}

// Inspect implements provider.Wire: the quota windows the x-codex-* headers
// carry on every answer, and what a refusal's body says about the account.
func (Provider) Inspect(status int, h http.Header, body []byte) provider.Verdict {
	var v provider.Verdict
	if q, ok := quotaFromHeaders(h); ok {
		v.Quota = &q
	}
	if status < 400 {
		return v
	}
	e := parseError(body)
	switch e.kind() {
	case "usage_limit_reached":
		// The plan's own limit, with its reset: sit the account out until
		// then rather than for a minute's backoff, and say it is out.
		v.Until = e.resetsAt()
		if v.Quota == nil {
			v.Quota = &provider.Quota{}
		}
		v.Quota.Limited = true
	case "usage_not_included":
		// The plan does not include Codex at all. Not a rate limit to wait
		// out: another account may have a plan that does, and this one will
		// not change until someone upgrades it.
		v.Kind = "forbidden"
	}
	return v
}

// clientVersion is what the model list is asked for as. The backend leaves out
// a model whose minimal_client_version is above the asker's (Codex sends its
// own version); crush sends 999.0.0 to be shown them all
// (internal/oauth/openai/models.go), and so does this. Measured 2026-10-19:
// 200, nine models, two of them hidden.
const clientVersion = "999.0.0"

// ModelsPath implements provider.Wire. The caller's paging is not passed on:
// the backend answers the whole list and takes no limit.
func (Provider) ModelsPath(string) string { return "/models?client_version=" + clientVersion }

// backendModel is one entry of the backend's list, in the fields a model list
// needs. Each also carries its whole base_instructions — tens of kilobytes —
// which is why this names fields rather than keeping the entry.
type backendModel struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Visibility    string `json:"visibility"`
	ContextWindow int    `json:"context_window"`
	Priority      *int   `json:"priority"`
}

// ModelList implements provider.Wire: the backend's {"models":[...]} as the
// gateway's list, in the backend's own ranking.
//
// Only visibility "list": the others are the backend's own (gpt-reserve,
// codex-auto-review on 2026-10-19), which Codex does not offer in its picker
// either. priority is kept on each entry, because it is the backend's
// ranking of its own models and what gpt-large and gpt-small follow (see
// internal/service/models).
func (Provider) ModelList(body []byte) ([]byte, error) {
	var in struct {
		Models []backendModel `json:"models"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("read the ChatGPT model list: %w", err)
	}
	listed := make([]backendModel, 0, len(in.Models))
	for _, m := range in.Models {
		if m.Slug != "" && m.Visibility == "list" {
			listed = append(listed, m)
		}
	}
	// Ranked first, then by the list's own order: an entry without a
	// priority goes after the ranked ones rather than being guessed into
	// their middle.
	sort.SliceStable(listed, func(i, j int) bool {
		a, b := listed[i].Priority, listed[j].Priority
		switch {
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return *a < *b
	})
	data := make([]map[string]any, 0, len(listed))
	for _, m := range listed {
		e := map[string]any{
			"type":     "model",
			"id":       m.Slug,
			"provider": ID,
		}
		if m.DisplayName != "" {
			e["display_name"] = m.DisplayName
		}
		if m.Description != "" {
			e["description"] = m.Description
		}
		if m.ContextWindow > 0 {
			e["max_input_tokens"] = m.ContextWindow
		}
		if m.Priority != nil {
			e["priority"] = *m.Priority
		}
		data = append(data, e)
	}
	list := map[string]any{"data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"] = data[0]["id"]
		list["last_id"] = data[len(data)-1]["id"]
	}
	return json.Marshal(list)
}
