package google

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"claudication/internal/provider"
)

// ID is the provider name accounts are stored under. "google", as models.dev
// names the vendor of Gemini, so the catalogue's facts are found under the
// same key.
const ID = "google"

// BaseURL is the Code Assist backend as Antigravity reaches it: the daily
// one, where CLIProxyAPI sends a consumer account
// (antigravity_executor_request.go resolveAntigravityRequestBaseURL) and
// where every call was measured to answer (2026-10-23). Not
// generativelanguage.googleapis.com, which is the metered Gemini API and
// takes an API key, not a subscription.
const BaseURL = "https://daily-cloudcode-pa.googleapis.com"

// UserAgent is Antigravity's, which Google serves the free tier to and
// nothing else: measured 2026-10-23, the same token, client and metadata
// with this gateway's own User-Agent were refused at loadCodeAssist as
// UNSUPPORTED_CLIENT ("please migrate to the Antigravity suite of
// products") and a turn as 403 SUBSCRIPTION_REQUIRED
// (testdata/antigravity-*.json). The form is CLIProxyAPI's
// (internal/misc/antigravity_version.go); 2.25.0 was measured served.
//
// It is the one place this gateway claims another client's identity, which
// everywhere else it will not (internal/relay/passes; the ChatGPT
// originator). The owner chose it (2026-10-23) as the only way a free Google
// account serves at all, and it goes on Google's calls alone.
const UserAgent = "antigravity/hub/2.25.0 (aidev_client; os_type=linux; arch=amd64)"

// apiVersion prefixes every method: {BaseURL}/v1internal:{method}. server.ts
// CODE_ASSIST_API_VERSION.
const apiVersion = "v1internal"

// The paths a relayed turn is sent to, streamed and whole. ?alt=sse is what
// turns the streamed method's answer into server-sent events (server.ts
// requestStreamingPost); without it the backend answers one JSON array at
// the end.
const (
	StreamPath   = "/" + apiVersion + ":streamGenerateContent?alt=sse"
	GeneratePath = "/" + apiVersion + ":generateContent"
	CountPath    = "/" + apiVersion + ":countTokens"
)

// Provider is the Code Assist backend, as the relay sees it.
type Provider struct{}

var (
	_ provider.Wire    = Provider{}
	_ provider.Stamper = Provider{}
)

// ID implements provider.Wire.
func (Provider) ID() string { return ID }

// BaseURL implements provider.Wire.
func (Provider) BaseURL() string { return BaseURL }

// Authorize implements provider.Wire: the account's bearer token, and
// Antigravity's User-Agent in place of the caller's, without which Google
// refuses the turn (UserAgent). The project that bills the turn travels in
// the body (Stamp), not in a header.
func (Provider) Authorize(out *http.Request, c provider.Credential) {
	out.Header.Set("Authorization", "Bearer "+c.Token)
	out.Header.Set("User-Agent", UserAgent)
	if out.Header.Get("Content-Type") == "" && out.Body != nil {
		out.Header.Set("Content-Type", "application/json")
	}
}

// Stamp implements provider.Stamper: the account's project as the envelope's
// "project", put first.
//
// The body is the gateway's own — every request reaching this backend was
// built by a translation, none is relayed as a client sent it — and the
// translations leave "project" out, because which account serves an attempt
// is not known when they run. So it is added rather than replaced, and a
// body that is not an object is sent as it is, to be refused in the
// backend's words.
func (Provider) Stamp(body []byte, c provider.Credential) []byte {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if c.AccountID == "" || len(trimmed) == 0 || trimmed[0] != '{' {
		return body
	}
	project, _ := json.Marshal(c.AccountID)
	rest := bytes.TrimLeft(trimmed[1:], " \t\r\n")
	out := make([]byte, 0, len(body)+len(project)+12)
	out = append(out, `{"project":`...)
	out = append(out, project...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, rest...)
}

// Event implements provider.Wire. The backend's stream names no events — each
// is a bare `data:` line holding a whole answer so far — so there is nothing
// to tell apart by name, and every event is content. The stall hold, which is
// what reads this, is off on this wire (see the composition root).
func (Provider) Event([]byte) provider.EventKind { return provider.EventContent }

// Meter implements provider.Wire; see meter.go.
func (Provider) Meter(contentType string, into provider.Reading) provider.Meter {
	return newMeter(contentType, into)
}

// Inspect implements provider.Wire: nothing rides on a successful answer's
// headers, so only a refusal says anything about the account; see errors.go.
func (Provider) Inspect(status int, _ http.Header, body []byte) provider.Verdict {
	if status < 400 {
		return provider.Verdict{}
	}
	return verdict(status, body)
}

// ModelsRequest implements provider.Wire with fetchAvailableModels, the
// list Antigravity's model picker reads (measured 2026-10-23,
// testdata/antigravity-models.json).
func (Provider) ModelsRequest(_ string, c provider.Credential) (string, string, []byte) {
	body, _ := json.Marshal(map[string]string{"project": c.AccountID})
	return http.MethodPost, "/" + apiVersion + ":fetchAvailableModels", body
}

// available is fetchAvailableModels' answer, in the fields read (measured
// 2026-10-23, testdata/antigravity-models.json): every model by id, with
// what Antigravity's picker shows of it.
type available struct {
	Models map[string]struct {
		DisplayName     string `json:"displayName"`
		MaxTokens       int    `json:"maxTokens"`
		MaxOutputTokens int    `json:"maxOutputTokens"`
		ModelProvider   string `json:"modelProvider"`
		QuotaInfo       *struct {
			RemainingFraction *float64 `json:"remainingFraction"`
		} `json:"quotaInfo"`
	} `json:"models"`
}

// ModelList implements provider.Wire: the models an account may chat with,
// as the gateway's list, ranked Pro, then Flash, then Flash-Lite, each
// newest first, which is the order the Gemini families are offered in
// (internal/service/models, family.go).
//
// Of the 23 the account measured was offered, kept are those Antigravity's
// picker would show and could serve: with a display name (the rest —
// chat_20706, tab_flash_lite_preview — are its autocomplete's) and a quota
// (gemini-4-argon-disabled had none). Anthropic's models are left out —
// claude-opus-4-6-thinking, claude-sonnet-4-6 — because a claude- id is
// always routed to the Claude accounts (internal/service/models,
// route.go), so offering them here would offer what no request reaches.
func (Provider) ModelList(body []byte) ([]byte, error) {
	var in available
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("read the Antigravity model list: %w", err)
	}
	var ids []string
	for id, m := range in.Models {
		if m.DisplayName == "" || m.QuotaInfo == nil || m.ModelProvider == "MODEL_PROVIDER_ANTHROPIC" {
			continue
		}
		ids = append(ids, id)
	}
	sortModels(ids)
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		m := in.Models[id]
		e := map[string]any{
			"type":         "model",
			"id":           id,
			"provider":     ID,
			"display_name": m.DisplayName,
		}
		if m.MaxTokens > 0 {
			e["max_input_tokens"] = m.MaxTokens
		}
		if m.MaxOutputTokens > 0 {
			e["max_tokens"] = m.MaxOutputTokens
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

// modelRank orders Gemini model ids: by tier, largest first; then by version,
// newest first; then generally available before preview; then by thinking
// level, most first. Tier first because the order the list gives is the
// order the families are offered in, and an operator looks for Pro before
// Flash-Lite whichever had the newer release; level last because
// Antigravity offers each recent model at several (gemini-3.8-flash-high,
// -medium, -low), and a family is served the first.
type modelRank struct {
	version []int
	tier    int
	preview bool
	level   int
	gemini  bool
}

// tiers rank the size words, largest first. Anything else ranks after them.
var tiers = []string{"pro", "flash", "flash-lite"}

// levels rank Antigravity's thinking levels, most first; an id naming none
// sits between high and medium.
var levels = map[string]int{"high": 0, "": 1, "medium": 2, "low": 3, "extra-low": 4}

// flashTier is "flash"'s rank in tiers.
const flashTier = 1

// models are the buckets' model ids, each once, ranked, less Antigravity's
// internal ones (chat_20706, tab_jump_flash_lite_preview), which carry an
// underscore no model a person picks does.
func (q quotaResponse) models() []string {
	seen := map[string]bool{}
	var ids []string
	for _, b := range q.Buckets {
		if b.ModelID == "" || seen[b.ModelID] || strings.Contains(b.ModelID, "_") {
			continue
		}
		seen[b.ModelID] = true
		ids = append(ids, b.ModelID)
	}
	sortModels(ids)
	return ids
}

// sortModels ranks ids, the id itself breaking a tie so the order never
// depends on a map's.
func sortModels(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		a, b := rank(ids[i]), rank(ids[j])
		if a.before(b) {
			return true
		}
		if b.before(a) {
			return false
		}
		return ids[i] < ids[j]
	})
}

// rank reads an id like gemini-3.8-flash-high or gemini-3-pro-preview:
// "gemini", a dotted version, a tier, and qualifiers.
func rank(id string) modelRank {
	r := modelRank{tier: len(tiers), level: levels[""]}
	rest, ok := strings.CutPrefix(id, "gemini-")
	if !ok {
		return r
	}
	r.gemini = true
	ver, rest, _ := strings.Cut(rest, "-")
	for _, p := range strings.Split(ver, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		r.version = append(r.version, n)
	}
	qualifier := ""
	for i, t := range tiers {
		if rest == t || strings.HasPrefix(rest, t+"-") {
			// flash-lite also starts with flash-; the longer word wins.
			r.tier = i
			qualifier = strings.TrimPrefix(strings.TrimPrefix(rest, t), "-")
		}
	}
	if l, ok := levels[qualifier]; ok {
		r.level = l
	}
	r.preview = strings.Contains(rest, "preview") || strings.Contains(rest, "exp")
	return r
}

func (a modelRank) before(b modelRank) bool {
	if a.gemini != b.gemini {
		return a.gemini
	}
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	for i := 0; i < max(len(a.version), len(b.version)); i++ {
		var x, y int
		if i < len(a.version) {
			x = a.version[i]
		}
		if i < len(b.version) {
			y = b.version[i]
		}
		if x != y {
			return x > y
		}
	}
	if a.preview != b.preview {
		return !a.preview
	}
	return a.level < b.level
}

// displayName is an id as a person would write it: "gemini-2.5-pro" becomes
// "Gemini 2.5 Pro". The backend names none.
func displayName(id string) string {
	words := strings.Split(id, "-")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
