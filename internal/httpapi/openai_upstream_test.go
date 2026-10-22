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
	"claudication/internal/service/clients"
	"claudication/internal/store"
)

// The second upstream, end to end: two stubs standing in for Anthropic and for
// ChatGPT's Codex backend, and requests from Codex and from Claude Code routed
// between them by the model their slot resolves to. Nothing leaves the
// process.

// codexTurn is a Codex request as Codex sends it through a custom provider,
// naming one of its slots.
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
		got := claude.record(r)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-5","created_at":"2026-09-01T00:00:00Z"},`+
				`{"type":"model","id":"claude-haiku-5","created_at":"2026-08-01T00:00:00Z"}],"has_more":false}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Claude Code's naming request asks for JSON, and gets it, as
		// structured output answers.
		if bytes.Contains(got.body, []byte("You are naming a coding session")) {
			_, _ = io.WriteString(w, strings.Replace(anthropicStream, `"text":"hi"`,
				`"text":"{\"title\":\"auth.go login bug\"}"`, 1))
			return
		}
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
		// No Content-Type: the real backend sends none on a stream
		// (measured 2026-10-20), and a stub that did hid two bugs. nil, so
		// the test server does not sniff one in either.
		w.Header()["Content-Type"] = nil
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

// newClient makes a client of a type with its type's slots under their
// names, each bound as binds says by slot id and following its default
// otherwise, and returns it with its key's plaintext.
func newClient(t *testing.T, st *store.Store, name, typ string,
	binds map[string]store.Binding) (store.Client, string) {
	t.Helper()
	ctx := context.Background()
	ty, _ := clients.Find(typ)
	var slots []store.NewSlot
	for _, a := range ty.Initial() {
		b := store.Binding{Follows: a.Follows}
		if got, ok := binds[a.Slot]; ok {
			b = got
		}
		slots = append(slots, store.NewSlot{Slot: a.Slot, Name: a.Name, Binding: b})
	}
	c, plain, err := st.CreateClient(ctx, name, ty.ID, store.KeyLimits{}, slots)

	if err != nil {
		t.Fatal(err)
	}
	// Naming chats off: a title request is a second request, sent after the
	// answer, which a test counting turns or reading the newest row would
	// race. The tests of naming switch it on. A type that names its own chats
	// has no switch, and the gateway sends nothing of its own for it.
	if !ty.NamesItsChats {
		if err := st.SetClientNameChats(ctx, c.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	return c, plain
}

// namingChats switches a test client's "Name chats" back on.
func namingChats(t *testing.T, st *store.Store, c store.Client) {
	t.Helper()
	if err := st.SetClientNameChats(context.Background(), c.ID, true); err != nil {
		t.Fatal(err)
	}
}

// A Codex slot bound to a GPT model goes to the ChatGPT backend as Codex
// wrote it — only the model resolved, the credential and the workspace
// added, Codex's own headers kept — and comes back naming the slot.
func TestCodexSlotBoundToGPTReachesChatGPTUnchanged(t *testing.T) {
	_, st, base, _, claude, chatgpt := twoUpstreams(t)
	ctx := context.Background()
	_, key := newClient(t, st, "ci", "codex", map[string]store.Binding{"main": {Model: "gpt-6-luna"}})

	sent := codexTurn("large")
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

	// The answer says the slot, in the events and in the header.
	if strings.Contains(string(body), "gpt-6-luna") || !strings.Contains(string(body), `"model":"large"`) {
		t.Errorf("the answer named the model, not the slot:\n%s", body)
	}
	if got := resp.Header.Get("OpenAI-Model"); got != "large" {
		t.Errorf("OpenAI-Model = %q, want the slot", got)
	}
	// Named for what it is, which the backend did not.
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// Recorded as ChatGPT's, under the client and the slot, its usage
	// normalised and its quota read off the answer's headers.
	e := nthEvent(t, st, 1)
	if e.Provider != "openai" || e.Model != "gpt-6-luna" || e.VirtualModel != "large" || e.KeyName != "ci" ||
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

// A slot following a ChatGPT line serves its newest version (by version: the
// backend dates nothing), a Claude family the newest of its line; a
// direct-mode client naming a listed model by its id reaches it the same way,
// and one naming a family by its id (gpt-luna) is served its newest.
func TestFollowTargets(t *testing.T) {
	_, st, base, _, claude, chatgpt := twoUpstreams(t)
	ctx := context.Background()
	c, key := newClient(t, st, "tool", "custom", nil)
	for name, f := range map[string]clients.Follow{"first": clients.FollowSol, "light": clients.FollowLuna,
		"sonnet": clients.FollowSonnet} {
		if err := st.AddSlot(ctx, c.ID, name, store.Binding{Follows: f}); err != nil {
			t.Fatal(err)
		}
	}
	_, direct := newClient(t, st, "direct", "custom", nil)
	if err := st.SetClientMode(ctx, keyID(t, st, "direct"), clients.Direct); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, asked, want string }{
		{key, "first", "gpt-6.1-sol"}, {key, "light", "gpt-6-luna"}, {direct, "gpt-6-astra", "gpt-6-astra"},
		{direct, "gpt-luna", "gpt-6-luna"},
	} {
		resp, body := send(t, base+"/v1/responses", tc.key, codexUA, codexTurn(tc.asked), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", tc.asked, resp.StatusCode, body)
		}
		turns := chatgpt.turns()
		var got struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(turns[len(turns)-1].body, &got)
		if got.Model != tc.want {
			t.Errorf("%s reached ChatGPT as %q, want %q", tc.asked, got.Model, tc.want)
		}
	}
	// The newest Claude Sonnet the account lists, translated.
	resp, body := send(t, base+"/v1/responses", key, codexUA, codexTurn("sonnet"), nil)
	turns := claude.turns()
	if resp.StatusCode != http.StatusOK || len(turns) != 1 || !strings.Contains(string(turns[0].body), `"model":"claude-sonnet-5"`) {
		t.Errorf("sonnet: %d %s; Anthropic got %d turns", resp.StatusCode, body, len(turns))
	}
}

// keyID is the id of the client named name.
func keyID(t *testing.T, st *store.Store, name string) string {
	t.Helper()
	all, err := st.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range all {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("no client %s", name)
	return ""
}

// A Codex slot bound to a Claude model goes to Anthropic, translated.
func TestCodexClaudeSlotTranslatesToAnthropic(t *testing.T) {
	_, st, base, _, claude, chatgpt := twoUpstreams(t)
	_, key := newClient(t, st, "ci", "codex", map[string]store.Binding{"main": {Model: "claude-sonnet-5"}})
	resp, body := send(t, base+"/v1/responses", key, codexUA, codexTurn("large"), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	turns := claude.turns()
	if len(turns) != 1 || turns[0].path != "/v1/messages" {
		t.Fatalf("did not reach Anthropic's Messages endpoint: %d turns", len(turns))
	}
	var up struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(turns[0].body, &up); err != nil || up.Model != "claude-sonnet-5" || len(up.Messages) == 0 {
		t.Errorf("reached Anthropic as %s", turns[0].body)
	}
	if e := nthEvent(t, st, 1); e.Provider != "anthropic" || e.VirtualModel != "large" {
		t.Errorf("recorded %+v", e)
	}
	if n := len(chatgpt.turns()); n != 0 {
		t.Errorf("ChatGPT was sent %d turns", n)
	}
}

// A Claude Code slot bound to a GPT model is refused, before anything is
// spent, in its own envelope, naming the slot and what to do — and its model
// list offers its own names that it can use and nothing else.
func TestClaudeCodeReachesAGPTSlot(t *testing.T) {
	_, st, base, _, claude, chatgpt := twoUpstreams(t)
	_, key := newClient(t, st, "laptop", "claude-code", map[string]store.Binding{"main": {Model: "gpt-6.1-sol"}})
	const claudeUA = "claude-cli/2.1.274 (external, sdk-cli)"
	session := map[string]string{
		"X-Claude-Code-Session-Id": "sess-1",
		"Anthropic-Beta":           "claude-code-20250219",
		"Anthropic-Version":        "2023-06-01",
		"X-Stainless-Lang":         "js",
	}

	// Streamed: Anthropic's events, the slot's name said back, usage as the
	// meter reads it.
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
	turns := chatgpt.turns()
	if len(turns) != 1 || turns[0].path != "/responses" {
		t.Fatalf("ChatGPT was sent %+v", turns)
	}
	sent := turns[0]
	for _, want := range []string{`"model":"gpt-6.1-sol"`, `"instructions":"be terse"`, `"stream":true`,
		`"store":false`, `"prompt_cache_key":"sess-1"`, `"text":"hi","type":"input_text"`, `"max_output_tokens":16`} {
		if !strings.Contains(string(sent.body), want) {
			t.Errorf("the translated request lacks %s: %s", want, sent.body)
		}
	}
	for _, h := range []string{"Anthropic-Beta", "Anthropic-Version", "X-Stainless-Lang", "X-Claude-Code-Session-Id"} {
		if sent.header.Get(h) != "" {
			t.Errorf("%s went to ChatGPT", h)
		}
	}
	if e := nthEvent(t, st, 1); e.Provider != "openai" || e.Model != "gpt-6.1-sol" || e.VirtualModel != "large" ||
		e.InputTokens != 20 || e.CacheReadTokens != 100 || e.OutputTokens != 9 {
		t.Errorf("the turn was recorded as %+v", e)
	}

	// Not streamed: the same answer, gathered, though the backend streamed.
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
	if got := chatgpt.turns(); len(got) != 2 || !strings.Contains(string(got[1].body), `"stream":true`) {
		t.Errorf("the second turn went up as %s", got[len(got)-1].body)
	}

	// count_tokens is answered here, an estimate that says so.
	resp, body = send(t, base+"/v1/messages/count_tokens", key, claudeUA,
		`{"model":"large","messages":[{"role":"user","content":"hello there, how are you"}]}`, session)
	var count struct {
		InputTokens int `json:"input_tokens"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &count) != nil || count.InputTokens < 6 ||
		resp.Header.Get("X-Claudication-Token-Estimate") == "" {
		t.Errorf("count_tokens: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if got := chatgpt.turns(); len(got) != 2 {
		t.Errorf("count_tokens reached ChatGPT: %d turns", len(got))
	}

	// And the model list offers the slot, which now serves.
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("User-Agent", claudeUA)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(listed), `"id":"large"`) || !strings.Contains(string(listed), `"id":"small"`) {
		t.Errorf("Claude Code was not offered its names: %s", listed)
	}
}

// A client naming its slots names them and nothing else: a real model, or
// another client's name, is refused listing its own.
func TestATypedClientNamesOnlyItsSlots(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	_, key := newClient(t, st, "box", "crush", nil)
	for _, asked := range []string{"claude-sonnet-5", "auto", "main"} {
		resp, body := send(t, base+"/v1/messages", key, "Charm Crush/0.9",
			`{"model":"`+asked+`","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "large, small") {
			t.Errorf("%s: %d %s", asked, resp.StatusCode, body)
		}
	}
	if n := len(claude.turns()); n != 0 {
		t.Errorf("%d turns reached Anthropic", n)
	}
}

// Two clients may each have a "large", and each is served its own.
func TestNamesAreTheClientsOwn(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	_, a := newClient(t, st, "a", "crush", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	_, b := newClient(t, st, "b", "crush", map[string]store.Binding{"large": {Model: "claude-haiku-5"}})
	for _, tc := range []struct{ key, want string }{{a, "claude-sonnet-5"}, {b, "claude-haiku-5"}} {
		resp, body := send(t, base+"/v1/messages", tc.key, "Charm Crush/0.9",
			`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		turns := claude.turns()
		if !strings.Contains(string(turns[len(turns)-1].body), `"model":"`+tc.want+`"`) {
			t.Errorf("large reached Anthropic as %s, want %s", turns[len(turns)-1].body, tc.want)
		}
	}
}

// A direct-mode client names real models, less the ones switched off for it,
// which are refused before anything is spent; in binding mode its switches
// stay stored and do not apply.
func TestDirectModeHoldsItsSwitches(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	ctx := context.Background()
	c, key := newClient(t, st, "tool", "custom", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	if err := st.SetClientMode(ctx, c.ID, clients.Direct); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKeyModel(ctx, c.ID, "claude-haiku-5", false); err != nil {
		t.Fatal(err)
	}
	ask := func(model string) int {
		resp, _ := send(t, base+"/v1/messages", key, "curl/8",
			`{"model":"`+model+`","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
		return resp.StatusCode
	}
	if s := ask("claude-sonnet-5"); s != http.StatusOK {
		t.Errorf("an allowed model: %d", s)
	}
	if s := ask("claude-haiku-5"); s != http.StatusForbidden {
		t.Errorf("a switched-off model: %d", s)
	}
	if err := st.SetClientMode(ctx, c.ID, clients.Binding); err != nil {
		t.Fatal(err)
	}
	if s := ask("large"); s != http.StatusOK {
		t.Errorf("its slot in binding mode: %d", s)
	}
	if s := ask("claude-sonnet-5"); s != http.StatusNotFound {
		t.Errorf("a real model in binding mode: %d", s)
	}
	if n := len(claude.turns()); n != 2 {
		t.Errorf("%d turns reached Anthropic, want the two served", n)
	}
}

// A switched-off client is refused before anything is routed, in its own
// dialect, and recorded under its name; switched back on, it is served.
func TestADisabledClientIsRefused(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	ctx := context.Background()
	c, key := newClient(t, st, "box", "crush", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	if err := st.SetClientDisabled(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	turn := `{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	resp, body := send(t, base+"/v1/messages", key, "Charm Crush/0.9", turn, nil)
	if resp.StatusCode != http.StatusForbidden || errType(t, body) != "permission_error" ||
		!strings.Contains(string(body), "disabled") {
		t.Errorf("disabled: %d %s", resp.StatusCode, body)
	}
	if e := nthEvent(t, st, 1); !e.Rejected || e.KeyName != "box" {
		t.Errorf("recorded %+v", e)
	}
	if err := st.SetClientDisabled(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	if resp, body := send(t, base+"/v1/messages", key, "Charm Crush/0.9", turn, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("enabled again: %d %s", resp.StatusCode, body)
	}
	if n := len(claude.turns()); n != 1 {
		t.Errorf("%d turns reached Anthropic", n)
	}
}

// A slot bound to a model no list names is sent to the default provider, as
// such a name always was, and is not refused as unserved: where it goes and
// whether an account there can take it are one answer.
func TestASlotBoundToAnUnlistedModelIsServedByTheDefault(t *testing.T) {
	_, st, base, _, claude, chatgpt := twoUpstreams(t)
	_, key := newClient(t, st, "box", "crush", map[string]store.Binding{"large": {Model: "mystery-7"}})
	resp, body := send(t, base+"/v1/messages", key, "Charm Crush/0.9",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
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

// A crush chat is named by crush: its naming request (the shape of
// GenerateTitle in crush's source) is recognised, its answer becomes the
// chat's title, the request is the chat's cost but not one of its turns, and
// the gateway's titler asks nothing of its own. The chat is grouped by
// crush's x-session-id, its type's header. No switch is touched: crush has
// none, and its chats are always named.
func TestCrushNamesItsOwnChat(t *testing.T) {
	_, st, base, _, claude, _ := twoUpstreams(t)
	_, key := newClient(t, st, "box", "crush", map[string]store.Binding{
		"large": {Model: "claude-sonnet-5"}, "small": {Model: "claude-haiku-5"}})
	session := map[string]string{"X-Session-Id": "9f2c", "X-Session-Affinity": "9f2c"}
	naming := `{"model":"small","max_tokens":40,"stream":true,` +
		`"system":[{"type":"text","text":"You will generate a short title based on the first message a user begins a conversation with.\n /no_think"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"Generate a concise title for the following content:\n\nsay hi\n <think>\n\n</think>"}]}]}`
	turn := `{"model":"large","max_tokens":100,"stream":true,"tools":[{"name":"bash","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"say hi"}]}`
	for _, body := range []string{naming, turn} {
		if resp, out := send(t, base+"/v1/messages", key, "Charm-Crush/0.9", body, session); resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}
	nthEvent(t, st, 2)
	deadline := time.Now().Add(5 * time.Second)
	var rec store.TitleRecord
	for time.Now().Before(deadline) {
		var found bool
		if rec, found, _ = st.TitleRecord(context.Background(), "9f2c"); found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Title != "hi" || rec.Requests != 0 {
		t.Errorf("title %+v, want crush's own", rec)
	}
	chats, err := st.Chats(context.Background(), time.Now().Add(-time.Hour), 10)
	if err != nil || len(chats.Chats) != 1 || chats.Chats[0].ID != "9f2c" || chats.Chats[0].Requests != 2 ||
		chats.Chats[0].Turns != 1 {
		t.Errorf("chats %+v, %v: want one chat of two requests and one turn", chats.Chats, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(claude.turns()); n != 2 {
		t.Errorf("Anthropic was sent %d turns, want crush's two and nothing of the gateway's", n)
	}
}

// A Claude Code chat is named by Claude Code: its session title request (the
// shape captured from 2.1.304, internal/service/clients) is recognised, the
// title read out of the JSON its answer is, and the request filed as the
// chat's cost but not one of its turns, with nothing asked by the gateway.
// It has no "Name chats" switch: the admin API refuses to set one, and
// reports its chats named.
func TestClaudeCodeNamesItsOwnChat(t *testing.T) {
	srv, st, base, _, claude, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	c, key := newClient(t, st, "laptop", "claude-code", map[string]store.Binding{
		"main": {Model: "claude-sonnet-5"}, "fast": {Model: "claude-haiku-5"}})
	const session = "e966da91-ed4b-4451-9a11-478849a2cd97"
	naming := `{"model":"small","messages":[{"role":"user","content":[{"type":"text",` +
		`"text":"<session>\nexplain the login bug in auth.go please\n</session>\n\nWrite the title in the predominant language of the session."}]}],` +
		`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304.644; cc_entrypoint=cli;"},` +
		`{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},` +
		`{"type":"text","text":"You are naming a coding session so the user can pick it out of a long list of sessions. ` +
		`Return JSON with a single \"title\" field."}],"tools":[],` +
		`"max_tokens":32000,"output_config":{"format":{"type":"json_schema","schema":{"type":"object",` +
		`"properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}},"stream":true}`
	turn := `{"model":"large","max_tokens":32000,"stream":true,` +
		`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"explain the login bug in auth.go please"}]}`
	for _, body := range []string{naming, turn} {
		resp, out := send(t, base+"/v1/messages", key, "claude-cli/2.1.304 (external, cli)", body,
			map[string]string{"X-Claude-Code-Session-Id": session})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}
	nthEvent(t, st, 2)
	deadline := time.Now().Add(5 * time.Second)
	var rec store.TitleRecord
	for time.Now().Before(deadline) {
		var found bool
		if rec, found, _ = st.TitleRecord(context.Background(), session); found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec.Title != "auth.go login bug" || rec.Requests != 0 {
		t.Errorf("title %+v, want Claude Code's own", rec)
	}
	chats, err := st.Chats(context.Background(), time.Now().Add(-time.Hour), 10)
	if err != nil || len(chats.Chats) != 1 || chats.Chats[0].ID != session || chats.Chats[0].Requests != 2 ||
		chats.Chats[0].Turns != 1 {
		t.Errorf("chats %+v, %v: want one chat of two requests and one turn", chats.Chats, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(claude.turns()); n != 2 {
		t.Errorf("Anthropic was sent %d turns, want Claude Code's two and nothing of the gateway's", n)
	}

	if status, body := call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/name-chats",
		`{"enabled":false}`, ck); status != http.StatusBadRequest {
		t.Errorf("name-chats on Claude Code: %d %s, want it refused", status, body)
	}
	status, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"name_chats":true`)) {
		t.Errorf("client: %d %s, want its chats reported named", status, body)
	}
}

// A custom client's chat — it names nothing itself — is named by the gateway
// under the client's own key, on the chat's model and account, while its
// "Name chats" switch is on; switched off, nothing is sent.
func TestGatewayNamesAChatUnderTheClientsKey(t *testing.T) {
	srv, st, base, _, claude, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	c, key := newClient(t, st, "tool", "custom", map[string]store.Binding{"large": {Model: "claude-sonnet-5"}})
	namingChats(t, st, c)
	turn := `{"model":"large","max_tokens":100,"stream":true,` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"say hi"}]}`
	ask := func(session string) {
		t.Helper()
		resp, out := send(t, base+"/v1/messages", key, "mytool/1.0", turn,
			map[string]string{"X-Session-Id": session})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
	}
	waitTurns := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(claude.turns()) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	ask("s1")
	waitTurns(2)
	turns := claude.turns()
	if len(turns) != 2 || !strings.Contains(string(turns[1].body), "short title") ||
		!strings.Contains(string(turns[1].body), `"model":"claude-sonnet-5"`) {
		t.Fatalf("Anthropic got %d turns; the second: %s", len(turns), turns[len(turns)-1].body)
	}
	e := nthEvent(t, st, 2)
	if e.Path != store.InternalPath || e.KeyID != c.ID || e.KeyName != "tool" || e.ConversationID != "s1" {
		t.Errorf("the title request was recorded as %+v, want the client's", e)
	}

	if status, body := call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/name-chats",
		`{"enabled":false}`, ck); status != http.StatusOK {
		t.Fatalf("name-chats: %d %s", status, body)
	}
	ask("s2")
	time.Sleep(200 * time.Millisecond)
	if n := len(claude.turns()); n != 3 {
		t.Errorf("Anthropic got %d turns with naming off, want the client's one more", n)
	}
}

// A rate period longer than the budget's day is counted over its whole
// length, and the day's tokens over the day alone.
func TestClientLimitsReadTheWholeRatePeriod(t *testing.T) {
	srv, st, _, _, _, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	c, _ := newClient(t, st, "weekly", "custom", nil)
	if err := st.UpdateKey(context.Background(), c.ID, "weekly",
		store.KeyLimits{RPMLimit: 100, RatePeriod: 7 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	spend(t, st, c.ID, 500, time.Now().Add(-3*24*time.Hour))
	spend(t, st, c.ID, 40, time.Now().Add(-time.Hour))
	status, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID+"/limits", "", ck)
	if status != http.StatusOK {
		t.Fatalf("limits: %d %s", status, body)
	}
	got := decode[map[string]any](t, body)
	if got["in_period"] != float64(2) || got["tokens_day"] != float64(40) {
		t.Errorf("in_period %v, tokens_day %v; want 2 over the week and 40 over the day", got["in_period"], got["tokens_day"])
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
