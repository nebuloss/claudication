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

// BaseURL is the Code Assist backend: where gemini-cli sends a turn signed in
// with Google. Not generativelanguage.googleapis.com, which is the metered
// Gemini API and takes an API key, not a subscription. server.ts
// CODE_ASSIST_ENDPOINT.
const BaseURL = "https://cloudcode-pa.googleapis.com"

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

// Authorize implements provider.Wire: the account's bearer token. The project
// that bills the turn travels in the body (Stamp), not in a header.
//
// Nothing of gemini-cli's identity is added. Its User-Agent
// ("GeminiCLI/<version>/<model> (<platform>; <arch>; <surface>)") is the
// caller's when the caller is gemini-cli, and is relayed as it came; a
// caller that is not gemini-cli is not made to look like it.
func (Provider) Authorize(out *http.Request, c provider.Credential) {
	out.Header.Set("Authorization", "Bearer "+c.Token)
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

// ModelsRequest implements provider.Wire with retrieveUserQuota: the backend
// has no model list, and its quota answer has one bucket per model the
// account may call, which is the list as this account sees it. gemini-cli
// reads the same answer for /stats (server.ts retrieveUserQuota).
func (Provider) ModelsRequest(_ string, c provider.Credential) (string, string, []byte) {
	body, _ := json.Marshal(map[string]string{"project": c.AccountID})
	return http.MethodPost, "/" + apiVersion + ":retrieveUserQuota", body
}

// ModelList implements provider.Wire: the quota buckets' models as the
// gateway's list, newest first and Pro before Flash before Flash-Lite within
// a version, which is the order the Gemini families are ranked in
// (internal/service/models, family.go).
func (Provider) ModelList(body []byte) ([]byte, error) {
	var q quotaResponse
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, fmt.Errorf("read the Code Assist quota: %w", err)
	}
	ids := q.models()
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{
			"type":         "model",
			"id":           id,
			"provider":     ID,
			"display_name": displayName(id),
		})
	}
	list := map[string]any{"data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"] = data[0]["id"]
		list["last_id"] = data[len(data)-1]["id"]
	}
	return json.Marshal(list)
}

// modelRank orders Gemini model ids: by version, newest first; then by tier;
// then generally available before preview.
type modelRank struct {
	version []int
	tier    int
	preview bool
	gemini  bool
}

// tiers rank the size words, largest first. Anything else ranks after them.
var tiers = []string{"pro", "flash", "flash-lite"}

// flashTier is "flash"'s rank in tiers.
const flashTier = 1

// models are the buckets' model ids, each once, ranked.
func (q quotaResponse) models() []string {
	seen := map[string]bool{}
	var ids []string
	for _, b := range q.Buckets {
		if b.ModelID == "" || seen[b.ModelID] {
			continue
		}
		seen[b.ModelID] = true
		ids = append(ids, b.ModelID)
	}
	sort.SliceStable(ids, func(i, j int) bool { return rank(ids[i]).before(rank(ids[j])) })
	return ids
}

// rank reads an id like gemini-3.1-pro-preview-customtools: "gemini", a
// dotted version, a tier, and qualifiers.
func rank(id string) modelRank {
	r := modelRank{tier: len(tiers)}
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
	for i, t := range tiers {
		if rest == t || strings.HasPrefix(rest, t+"-") {
			// flash-lite also starts with flash-; the longer word wins.
			r.tier = i
		}
	}
	r.preview = strings.Contains(rest, "preview") || strings.Contains(rest, "exp")
	return r
}

func (a modelRank) before(b modelRank) bool {
	if a.gemini != b.gemini {
		return a.gemini
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
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	return !a.preview && b.preview
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
