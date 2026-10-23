package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// The third upstream, end to end: a stub standing in for Code Assist, and
// requests from Claude Code and from Codex routed to it by the model their
// slot resolves to. Nothing leaves the process. The stub answers in the
// shapes gemini-cli reads (internal/provider/google, doc.go), which have not
// been measured yet.

// geminiQuota is retrieveUserQuota's answer: the account's models.
const geminiQuota = `{"buckets":[
 {"modelId":"gemini-2.5-pro","tokenType":"REQUESTS","remainingFraction":0.9},
 {"modelId":"gemini-3-pro-preview","tokenType":"REQUESTS","remainingFraction":1},
 {"modelId":"gemini-2.5-flash","tokenType":"REQUESTS","remainingFraction":1}]}`

// geminiAnswer is a streamed turn saying "hi".
const geminiAnswer = "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hi\"}]}}]},\"traceId\":\"t1\"}\r\n\r\n" +
	"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]},\"finishReason\":\"STOP\"}]," +
	"\"usageMetadata\":{\"promptTokenCount\":120,\"cachedContentTokenCount\":100,\"candidatesTokenCount\":5,\"thoughtsTokenCount\":4}},\"traceId\":\"t1\"}\r\n\r\n"

// geminiUpstream is a gateway with a Claude account and a Google account,
// each provider's relay pointed at its stub.
func geminiUpstream(t *testing.T) (*store.Store, string, *upstreamStub, *upstreamStub) {
	t.Helper()
	srv, st, _ := newTestServer(t)

	claude := &upstreamStub{}
	claudeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claude.record(r)
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-5"}],"has_more":false}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicStream)
	}))
	t.Cleanup(claudeSrv.Close)

	google := &upstreamStub{}
	googleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := google.record(r)
		if r.Header.Get("Authorization") != "Bearer google-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1internal:retrieveUserQuota":
			if !strings.Contains(string(got.body), `"project":"proj-9"`) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, geminiQuota)
		case "/v1internal:streamGenerateContent":
			if r.URL.Query().Get("alt") != "sse" || !strings.HasPrefix(string(got.body), `{"project":"proj-9",`) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"no project"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, geminiAnswer)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(googleSrv.Close)

	srv.relay.BaseURL = claudeSrv.URL
	srv.relays["google"].BaseURL = googleSrv.URL

	ctx := context.Background()
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertAccount(ctx, store.Account{Provider: "google", Email: "gemini@example.com",
		AccountUUID: "proj-9", Plan: "Google AI Pro", ExpiresAt: time.Now().Add(time.Hour)},
		store.Tokens{AccessToken: "google-access", RefreshToken: "google-refresh"}); err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	t.Cleanup(func() { cancel(); <-done })
	return st, base, claude, google
}

// geminiTurns are the requests that were turns, not quota reads.
func (u *upstreamStub) geminiTurns() []stubRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []stubRequest
	for _, r := range u.reqs {
		if strings.HasSuffix(r.path, ":streamGenerateContent") {
			out = append(out, r)
		}
	}
	return out
}

func TestClaudeCodeReachesAGeminiSlot(t *testing.T) {
	st, base, claude, google := geminiUpstream(t)
	_, key := newClient(t, st, "laptop", "claude-code", map[string]store.Binding{"main": {Model: "gemini-2.5-pro"}})
	const claudeUA = "claude-cli/2.1.274 (external, sdk-cli)"
	session := map[string]string{
		"X-Claude-Code-Session-Id": "sess-1",
		"Anthropic-Beta":           "claude-code-20250219",
		"Anthropic-Version":        "2023-06-01",
	}

	resp, body := send(t, base+"/v1/messages", key, claudeUA,
		`{"model":"large","max_tokens":10,"stream":true,"system":"be terse",`+
			`"messages":[{"role":"user","content":"hi"}]}`, session)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status = %d, type %q: %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	for _, want := range []string{
		"event: message_start", `"model":"large"`, `"text":"hi","type":"text_delta"`,
		`"stop_reason":"end_turn"`, `"cache_read_input_tokens":100`, `"input_tokens":20`, "event: message_stop",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the stream lacks %s:\n%s", want, body)
		}
	}
	if n := len(claude.turns()); n != 0 {
		t.Errorf("%d turns reached Claude", n)
	}
	turns := google.geminiTurns()
	if len(turns) != 1 {
		t.Fatalf("Code Assist was sent %d turns", len(turns))
	}
	sent := turns[0]
	var env struct {
		Model, Project string
		Request        struct {
			SystemInstruction struct{ Parts []struct{ Text string } }
			Contents          []struct {
				Role  string
				Parts []struct{ Text string }
			}
			SessionID string `json:"session_id"`
		}
	}
	if err := json.Unmarshal(sent.body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Model != "gemini-2.5-pro" || env.Project != "proj-9" || env.Request.SessionID != "sess-1" ||
		env.Request.SystemInstruction.Parts[0].Text != "be terse" || env.Request.Contents[0].Parts[0].Text != "hi" {
		t.Errorf("the translated request was %s", sent.body)
	}
	for _, h := range []string{"Anthropic-Beta", "Anthropic-Version", "X-Claude-Code-Session-Id"} {
		if sent.header.Get(h) != "" {
			t.Errorf("%s went to Code Assist", h)
		}
	}
	if e := nthEvent(t, st, 1); e.Provider != "google" || e.Model != "gemini-2.5-pro" || e.VirtualModel != "large" ||
		e.InputTokens != 20 || e.CacheReadTokens != 100 || e.OutputTokens != 9 {
		t.Errorf("the turn was recorded as %+v", e)
	}

	// Not streamed: gathered from the stream the backend was asked for.
	resp, body = send(t, base+"/v1/messages", key, claudeUA,
		`{"model":"large","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`, session)
	var msg struct {
		Model   string `json:"model"`
		Content []struct {
			Type, Text string
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &msg) != nil || msg.Model != "large" ||
		len(msg.Content) != 1 || msg.Content[0].Text != "hi" || msg.StopReason != "end_turn" {
		t.Errorf("status = %d: %s", resp.StatusCode, body)
	}
}

// Codex reaches Gemini through Messages: its Responses request read into
// Messages, translated on, and the answer said back as Responses events.
func TestCodexReachesAGeminiSlotThroughMessages(t *testing.T) {
	st, base, _, google := geminiUpstream(t)
	_, key := newClient(t, st, "ci", "codex", map[string]store.Binding{"main": {Model: "gemini-2.5-flash"}})

	resp, body := send(t, base+"/v1/responses", key, codexUA, codexTurn("large"), map[string]string{
		"Originator": "codex_cli_rs", "Session-Id": "sess-2",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"event: response.created", `"delta":"hi"`, "event: response.completed"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the stream lacks %s:\n%s", want, body)
		}
	}
	turns := google.geminiTurns()
	if len(turns) != 1 {
		t.Fatalf("Code Assist was sent %d turns", len(turns))
	}
	sent := string(turns[0].body)
	for _, want := range []string{`"model":"gemini-2.5-flash"`, `"text":"say hi"`, `"text":"be terse"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("the request lacks %s: %s", want, sent)
		}
	}
	if turns[0].header.Get("Originator") != "" || turns[0].header.Get("Session-Id") != "" {
		t.Errorf("Codex's headers went to Code Assist: %v", turns[0].header)
	}
}

// A name following Gemini Pro is served the newest Pro the account has.
func TestAGeminiFamilyServesTheNewestVersion(t *testing.T) {
	st, base, _, google := geminiUpstream(t)
	_, key := newClient(t, st, "laptop", "claude-code",
		map[string]store.Binding{"main": {Follows: clients.Follow("gemini:pro")}})
	resp, body := send(t, base+"/v1/messages", key, "claude-cli/2.1.274",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	turns := google.geminiTurns()
	if len(turns) != 1 || !strings.Contains(string(turns[0].body), `"model":"gemini-3-pro-preview"`) {
		t.Fatalf("sent %+v", turns)
	}
}
