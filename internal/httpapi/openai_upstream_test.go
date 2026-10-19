package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/relay/passes"
	"claudication/internal/store"
)

// The second upstream, end to end: two stubs standing in for Anthropic and for
// ChatGPT's Codex backend, and requests from Codex and from Claude Code routed
// between them by the model their alias resolves to. Nothing leaves the
// process.

// codexTurn is a Codex request as Codex sends it through a custom provider,
// naming an alias.
func codexTurn(model string) string {
	return `{"model":"` + model + `","stream":true,"store":false,"instructions":"be terse",` +
		`"include":["reasoning.encrypted_content"],"reasoning":{"summary":"auto"},` +
		`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"say hi"}]}],` +
		`"prompt_cache_key":"3f0c","tools":[],"tool_choice":"auto","parallel_tool_calls":true}`
}

// chatgptModels is the backend's model list, in the fields read: a hidden one,
// and the rest ranked as measured on 2026-10-19.
const chatgptModels = `{"models":[
 {"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list","priority":1,"context_window":500000},
 {"slug":"gpt-reserve","display_name":"GPT-Reserve","visibility":"hide","priority":4},
 {"slug":"gpt-6-luna","display_name":"GPT-6-Luna","visibility":"list","priority":5,"context_window":272000},
 {"slug":"gpt-6-astra","display_name":"GPT-6-Astra","visibility":"list","priority":2,"context_window":272000},
 {"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","visibility":"list","priority":9,"context_window":272000}]}`

// chatgptStream is the backend's answer, in its own key order: model after
// the instructions it echoes.
func chatgptStream(model string) string {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	resp := `{"id":"resp_1","object":"response","status":"in_progress","instructions":"be terse","model":"` + model + `","output":[]}`
	done := `{"id":"resp_1","object":"response","status":"completed","instructions":"be terse","model":"` + model +
		`","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],` +
		`"usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":0},` +
		`"output_tokens":9,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":129}}`
	return ev("response.created", `{"type":"response.created","response":`+resp+`}`) +
		ev("response.in_progress", `{"type":"response.in_progress","response":`+resp+`}`) +
		ev("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message"}}`) +
		ev("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`) +
		ev("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message"}}`) +
		ev("response.completed", `{"type":"response.completed","response":`+done+`}`)
}

// upstreamStub records every request it was sent.
type upstreamStub struct {
	mu   sync.Mutex
	reqs []stubRequest
}

type stubRequest struct {
	path   string
	header http.Header
	body   []byte
}

func (u *upstreamStub) record(r *http.Request) stubRequest {
	body, _ := io.ReadAll(r.Body)
	got := stubRequest{path: r.URL.Path, header: r.Header.Clone(), body: body}
	u.mu.Lock()
	u.reqs = append(u.reqs, got)
	u.mu.Unlock()
	return got
}

// turns are the requests that were not model lists.
func (u *upstreamStub) turns() []stubRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []stubRequest
	for _, r := range u.reqs {
		if !strings.HasSuffix(r.path, "/models") {
			out = append(out, r)
		}
	}
	return out
}

// twoUpstreams is a gateway with a Claude account and a ChatGPT account, each
// provider's relay pointed at its stub. It returns the gateway's address, a
// key, and the two stubs.
func twoUpstreams(t *testing.T) (*Server, *store.Store, string, string, *upstreamStub, *upstreamStub) {
	t.Helper()
	srv, st, _ := newTestServer(t)

	claude := &upstreamStub{}
	claudeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claude.record(r)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-5","created_at":"2026-09-01T00:00:00Z"},`+
				`{"type":"model","id":"claude-haiku-5","created_at":"2026-08-01T00:00:00Z"}],"has_more":false}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicStream)
	}))
	t.Cleanup(claudeSrv.Close)

	chatgpt := &upstreamStub{}
	chatgptSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := chatgpt.record(r)
		if r.Header.Get("Authorization") != "Bearer chatgpt-access" || r.Header.Get("ChatGPT-Account-ID") != "acct-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/models" {
			_, _ = io.WriteString(w, chatgptModels)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(got.body, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("OpenAI-Model", body.Model)
		w.Header().Set("X-Codex-Primary-Used-Percent", "37")
		w.Header().Set("X-Codex-Primary-Window-Minutes", "300")
		w.Header().Set("X-Codex-Primary-Reset-At", "1792446828")
		w.Header().Set("X-Codex-Secondary-Used-Percent", "12")
		w.Header().Set("X-Codex-Secondary-Window-Minutes", "10080")
		_, _ = io.WriteString(w, chatgptStream(body.Model))
	}))
	t.Cleanup(chatgptSrv.Close)

	srv.relay.BaseURL = claudeSrv.URL
	srv.relays["openai"].BaseURL = chatgptSrv.URL

	ctx := context.Background()
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertAccount(ctx, store.Account{Provider: "openai", Email: "codex@example.com",
		AccountUUID: "acct-123", Plan: "plus", ExpiresAt: time.Now().Add(time.Hour)},
		store.Tokens{AccessToken: "chatgpt-access", RefreshToken: "chatgpt-refresh"}); err != nil {
		t.Fatal(err)
	}
	_, key, err := st.CreateKey(ctx, "client", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	t.Cleanup(func() { cancel(); <-done })
	return srv, st, base, key, claude, chatgpt
}

// send posts a request as the given client would, with headers of its own.
func send(t *testing.T, url, key, userAgent, body string, extra map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

const codexUA = "codex_cli_rs/0.155.0 (Linux 6.12; x86_64) xterm"

// A Codex request for an alias bound to a GPT model goes to the ChatGPT
// backend as Codex wrote it — only the model resolved, the credential and the
// workspace added, Codex's own headers kept — and comes back naming the alias.
func TestCodexAliasBoundToGPTReachesChatGPTUnchanged(t *testing.T) {
	_, st, base, key, claude, chatgpt := twoUpstreams(t)
	ctx := context.Background()
	if err := st.SetClientRole(ctx, "Codex", store.RoleLarge, "gpt-6-luna"); err != nil {
		t.Fatal(err)
	}

	sent := codexTurn("claude-large")
	resp, body := send(t, base+"/v1/responses", key, codexUA, sent, map[string]string{
		"Originator": "codex_cli_rs", "Session-Id": "sess-1", "X-Codex-Turn-State": "opaque",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if n := len(claude.turns()); n != 0 {
		t.Errorf("Anthropic was sent %d turns", n)
	}
	turns := chatgpt.turns()
	if len(turns) != 1 {
		t.Fatalf("ChatGPT was sent %d turns, want 1", len(turns))
	}
	up := turns[0]
	if up.path != "/responses" {
		t.Errorf("path = %q, want /responses", up.path)
	}
	want, _ := passes.SetModel([]byte(sent), "gpt-6-luna")
	if !bytes.Equal(up.body, want) {
		t.Errorf("body changed beyond its model:\n sent %s\n  got %s", want, up.body)
	}
	for name, v := range map[string]string{
		"ChatGPT-Account-ID": "acct-123", "Authorization": "Bearer chatgpt-access",
		"Originator": "codex_cli_rs", "Session-Id": "sess-1", "X-Codex-Turn-State": "opaque",
		"User-Agent": codexUA,
	} {
		if got := up.header.Get(name); got != v {
			t.Errorf("%s upstream = %q, want %q", name, got, v)
		}
	}

	// The answer says the alias, in the events and in the header.
	if strings.Contains(string(body), "gpt-6-luna") || !strings.Contains(string(body), `"model":"claude-large"`) {
		t.Errorf("the answer named the model, not the alias:\n%s", body)
	}
	if got := resp.Header.Get("OpenAI-Model"); got != "claude-large" {
		t.Errorf("OpenAI-Model = %q, want the alias", got)
	}

	// Recorded as ChatGPT's, its usage normalised and its quota read off the
	// answer's headers.
	e := nthEvent(t, st, 1)
	if e.Provider != "openai" || e.Model != "gpt-6-luna" || e.VirtualModel != "claude-large" ||
		e.InputTokens != 20 || e.CacheReadTokens != 100 || e.OutputTokens != 9 || e.ReasoningTokens != 4 ||
		e.ConversationID != "sess-1" {
		t.Errorf("recorded %+v", e)
	}
	// Written off the request's path, so waited for.
	deadline := time.Now().Add(5 * time.Second)
	var a store.Account
	for {
		var err error
		if a, err = st.AccountByIdentity(ctx, "openai", "acct-123", "codex@example.com"); err != nil {
			t.Fatal(err)
		}
		if a.Quota.Known() || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.Quota.FiveHourUtil != 37 || a.Quota.SevenDayUtil != 12 {
		t.Errorf("quota after the answer = %+v", a.Quota)
	}
}

// gpt-large, unbound, follows the backend's first-ranked model, and a Codex
// that names a listed model by its id reaches it the same way.
func TestGPTRolesFollowTheBackendsRanking(t *testing.T) {
	_, _, base, key, _, chatgpt := twoUpstreams(t)
	for asked, want := range map[string]string{
		"gpt-large": "gpt-6.1-sol", "gpt-small": "gpt-6-luna", "gpt-6-astra": "gpt-6-astra",
	} {
		resp, body := send(t, base+"/v1/responses", key, codexUA, codexTurn(asked), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", asked, resp.StatusCode, body)
		}
		turns := chatgpt.turns()
		var got struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(turns[len(turns)-1].body, &got)
		if got.Model != want {
			t.Errorf("%s reached ChatGPT as %q, want %q", asked, got.Model, want)
		}
	}
}

// A Claude alias from Codex still goes to Anthropic, translated, as it always
// has; so does a Codex default id no list names.
func TestCodexClaudeAliasStillTranslatesToAnthropic(t *testing.T) {
	_, st, base, key, claude, chatgpt := twoUpstreams(t)
	if err := st.SetModelRole(context.Background(), "", store.RoleLarge, "claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	for i, asked := range []string{"claude-large", "gpt-5-codex"} {
		resp, body := send(t, base+"/v1/responses", key, codexUA, codexTurn(asked), nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "response.completed") {
			t.Fatalf("%s: status = %d: %s", asked, resp.StatusCode, body)
		}
		turns := claude.turns()
		if len(turns) == 0 || turns[len(turns)-1].path != "/v1/messages" {
			t.Fatalf("%s did not reach Anthropic's Messages endpoint", asked)
		}
		var up struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(turns[len(turns)-1].body, &up); err != nil || up.Model != "claude-sonnet-5" || len(up.Messages) == 0 {
			t.Errorf("%s reached Anthropic as %s", asked, turns[len(turns)-1].body)
		}
		if e := nthEvent(t, st, i+1); e.Provider != "anthropic" {
			t.Errorf("%s recorded as %q's", asked, e.Provider)
		}
	}
	if n := len(chatgpt.turns()); n != 0 {
		t.Errorf("ChatGPT was sent %d turns", n)
	}
}

// Claude Code asking for an alias bound to a GPT model is refused, before
// anything is spent, in its own envelope, naming the alias and what to do —
// and its model list does not offer what it cannot use.
func TestClaudeCodeIsRefusedAGPTAlias(t *testing.T) {
	srv, st, base, key, claude, chatgpt := twoUpstreams(t)
	if err := st.SetModelRole(context.Background(), "", store.RoleLarge, "gpt-6.1-sol"); err != nil {
		t.Fatal(err)
	}
	const claudeUA = "claude-cli/2.1.274 (external, sdk-cli)"
	for _, asked := range []string{"claude-large", "gpt-large", "gpt-6-luna"} {
		resp, body := send(t, base+"/v1/messages", key, claudeUA,
			`{"model":"`+asked+`","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if resp.StatusCode != http.StatusBadRequest || errType(t, body) != "invalid_request_error" {
			t.Errorf("%s: status = %d: %s", asked, resp.StatusCode, body)
			continue
		}
		msg := string(body)
		if !strings.Contains(msg, asked) || !strings.Contains(msg, "ChatGPT") || !strings.Contains(msg, "/v1/responses") {
			t.Errorf("%s: the refusal does not say what or how: %s", asked, msg)
		}
	}
	if n := len(claude.turns()) + len(chatgpt.turns()); n != 0 {
		t.Errorf("%d turns reached an upstream", n)
	}
	if e := nthEvent(t, st, 3); !e.Rejected || e.Status != http.StatusBadRequest {
		t.Errorf("the refusal was recorded as %+v", e)
	}

	// The list Claude Code reads offers no GPT model and no alias serving
	// one; a Codex config written from the gateway's list still names them.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("User-Agent", claudeUA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(listed), "gpt-") || strings.Contains(string(listed), `"id":"claude-large"`) {
		t.Errorf("Claude Code was offered what it cannot use: %s", listed)
	}
	if !strings.Contains(string(listed), "claude-sonnet-5") {
		t.Errorf("Claude Code lost its own models: %s", listed)
	}
	all, err := srv.models.For(context.Background(), store.APIKey{}, "Codex")
	if err != nil || !strings.Contains(string(all), `"id":"gpt-large"`) || !strings.Contains(string(all), `"id":"gpt-6.1-sol"`) {
		t.Errorf("the gateway's own list = %s, %v", all, err)
	}
}

// nthEvent is the newest recorded request once n have been recorded: a row is
// written after the answer, which the client may have finished reading first.
func nthEvent(t *testing.T, st *store.Store, n int) store.UsageEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, _, err := st.RecentUsage(context.Background(), 100, store.UsageCursor{}, store.RequestFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) >= n {
			return events[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fewer than %d requests were recorded", n)
	return store.UsageEvent{}
}

// A key kept off an alias does not reach it by naming a model no list knows,
// which the Responses API serves as claude-large: the alias a request is sent
// to is held to the key as the name it sent is.
func TestAKeyKeptOffAnAliasCannotReachItByFallback(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	ctx := context.Background()
	if err := st.SetModelRole(ctx, "", store.RoleLarge, "claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	k, plain, err := st.CreateKey(ctx, "kept-off", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKeyModel(ctx, k.ID, store.RoleLarge, false); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, base+"/v1/responses", plain, codexUA, codexTurn("gpt-5-codex"), nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "claude-large") {
		t.Errorf("status = %d: %s, want 403 naming claude-large", resp.StatusCode, body)
	}
	if n := len(claude.turns()); n != 0 {
		t.Errorf("%d turns reached Anthropic", n)
	}
}

// An alias bound to a model no list names is sent to the default provider, as
// it was before there was a second one, and is not refused as unserved: where
// it goes and whether an account there can take it are one answer.
func TestAnAliasBoundToAnUnlistedModelIsServedByTheDefault(t *testing.T) {
	_, st, base, key, claude, chatgpt := twoUpstreams(t)
	if err := st.SetModelRole(context.Background(), "", "team-mystery", "mystery-7"); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, base+"/v1/messages", key, "claude-cli/2.1.274 (external, sdk-cli)",
		`{"model":"team-mystery","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	turns := claude.turns()
	if len(turns) != 1 || !strings.Contains(string(turns[0].body), `"model":"mystery-7"`) {
		t.Errorf("Anthropic was sent %d turns: %v", len(turns), turns)
	}
	if n := len(chatgpt.turns()); n != 0 {
		t.Errorf("ChatGPT was sent %d turns", n)
	}
}
