package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/store"
)

// codexRequest is the smallest thing Codex sends: a Responses request that
// streams and names a model the upstream has never heard of.
const codexRequest = `{"model":"claude-sonnet-5","stream":true,"instructions":"be terse",
"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"say hi"}]}]}`

// anthropicStream is one complete Anthropic answer, as the upstream sends it.
const anthropicStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":9,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// relayTo stands a gateway up in front of an upstream handler and returns its
// base URL and a working API key.
func relayTo(t *testing.T, srv *Server, st *store.Store, upstream http.Handler) (string, string) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)

	srv.relay.BaseURL = up.URL
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	base, cancel, done := startServer(t, srv)
	t.Cleanup(func() { cancel(); <-done })

	_, key, err := st.CreateKey(context.Background(), "client", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return base, key
}

func post(t *testing.T, url, key, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A Codex request has to come back as a Responses stream that ends in
// response.completed. Anything else and Codex reports the turn as failed,
// whatever arrived before it.
func TestCodexRequestComesBackAsAResponsesStream(t *testing.T) {
	var seen []byte
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStream)
	}))

	resp := post(t, base+"/v1/responses", key, codexRequest)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// What went upstream must be Anthropic-shaped, with the model substituted
	// and a max_tokens Codex never sent.
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(seen, &sent); err != nil {
		t.Fatalf("the upstream got something that is not JSON: %v", err)
	}
	if sent.Model != "claude-sonnet-5" {
		t.Errorf("upstream model = %q, want the configured substitute", sent.Model)
	}
	if sent.MaxTokens <= 0 {
		t.Error("no max_tokens: Anthropic refuses a request without one")
	}
	// The relay's attribution pass applies to a synthesised request exactly as
	// to a relayed one, and without it the account is served haiku and nothing
	// above it.
	if len(sent.System) == 0 || !strings.Contains(sent.System[0].Text, "Claude Code") {
		t.Error("the attribution block did not lead the system array")
	}

	out := string(body)
	if !strings.Contains(out, "event: response.created") {
		t.Errorf("no response.created:\n%s", out)
	}
	if !strings.Contains(out, `"delta":"hi"`) {
		t.Errorf("the text never arrived:\n%s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Errorf("no terminal response.completed — Codex would fail the turn:\n%s", out)
	}
}

// An upstream that refuses keeps its status, because that is what a client
// backs off on; only the envelope is translated.
func TestCodexRefusalKeepsTheStatusAndTranslatesTheEnvelope(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w,
			`{"type":"error","error":{"type":"rate_limit_error","message":"slow down please"}}`)
	}))

	resp := post(t, base+"/v1/responses", key, codexRequest)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the upstream's own 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q: the one header the client can act on", got)
	}
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not an OpenAI error envelope: %v\n%s", err, body)
	}
	// rate_limit_exceeded is not a code Codex acts on; slow_down is.
	if out.Error.Code != "slow_down" {
		t.Errorf("code = %q, want slow_down", out.Error.Code)
	}
	if !strings.Contains(out.Error.Message, "slow down please") {
		t.Errorf("the upstream's own words were lost: %q", out.Error.Message)
	}
}

// The request going upstream is one the gateway built, so Codex's own headers
// describe a request that no longer exists. Anthropic ignores headers it does
// not know, so this is not load-bearing — but the backend has refused requests
// over their content three times, every one found by bisection, so sending it
// something that looks like nothing else on earth is a risk taken for no gain.
func TestCodexOwnHeadersDoNotTravelUpstream(t *testing.T) {
	var got http.Header
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStream)
	}))

	req, err := http.NewRequest(http.MethodPost, base+"/v1/responses", strings.NewReader(codexRequest))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", "codex_exec")
	req.Header.Set("Session_id", "00000000-0000-0000-0000-000000000000")
	req.Header.Set("Openai-Beta", "responses=experimental")
	req.Header.Set("X-Stainless-Lang", "js")
	// Not ours to drop: a client's own identity is forwarded on every other
	// surface too, and nothing has ever shown it matters upstream.
	req.Header.Set("User-Agent", "codex_cli_rs/0.54.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	for _, name := range []string{"Originator", "Session_id", "Openai-Beta", "X-Stainless-Lang"} {
		if v := got.Get(name); v != "" {
			t.Errorf("%s reached the upstream as %q", name, v)
		}
	}
	if got.Get("User-Agent") == "" {
		t.Error("User-Agent was dropped; only the dialect's own headers should be")
	}
}

func TestASwitchedOffGatewayAnswersInEachDialect(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, key := relayTo(t, srv, st, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the upstream was reached for a surface that is off")
		w.WriteHeader(http.StatusOK)
	}))

	{
		if err := srv.serving.Set(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("anthropic", func(t *testing.T) {
		resp := post(t, base+"/v1/messages", key,
			`{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		// Anthropic's envelope: {"type":"error","error":{"type":…,"message":…}}
		var out struct {
			Type  string `json:"type"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Type != "error" {
			t.Fatalf("not an Anthropic error envelope: %s", body)
		}
		if !strings.Contains(out.Error.Message, "switched off") {
			t.Errorf("the message does not say why: %q", out.Error.Message)
		}
	})

	t.Run("openai", func(t *testing.T) {
		resp := post(t, base+"/v1/responses", key, codexRequest)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		// OpenAI's envelope has no "type" at the top and a "code" inside.
		var out struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Error.Message == "" {
			t.Fatalf("not an OpenAI error envelope: %s", body)
		}
		if strings.Contains(string(body), `"type":"error"`) {
			t.Errorf("answered in Anthropic's shape on the OpenAI surface: %s", body)
		}
	})
}

// A refusal from the key check — a bad key, a key going too fast — comes back
// in the caller's own envelope, like every other failure on the same path. It
// used to come back in the admin API's generic shape, which neither Claude Code
// nor Codex reads: Codex looks for error.code, and slow_down is the one that
// makes it back off.
func TestAKeyRefusalAnswersInTheCallersDialect(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, _ := relayTo(t, srv, st, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStream)
	}))
	const messages = `{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`

	anthropicType := func(t *testing.T, body []byte) string {
		t.Helper()
		var out struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Type != "error" {
			t.Fatalf("not an Anthropic error envelope: %s", body)
		}
		return out.Error.Type
	}
	openaiCode := func(t *testing.T, body []byte) string {
		t.Helper()
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Error.Code == "" {
			t.Fatalf("not an OpenAI error envelope: %s", body)
		}
		return out.Error.Code
	}
	call := func(t *testing.T, path, key, body string, want int) []byte {
		t.Helper()
		resp := post(t, base+path, key, body)
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s: status = %d, want %d: %s", path, resp.StatusCode, want, got)
		}
		return got
	}

	const bad = "clc_deadbeefdeadbeefdeadbeef"
	if got := anthropicType(t, call(t, "/v1/messages", bad, messages, http.StatusUnauthorized)); got != "authentication_error" {
		t.Errorf("anthropic, bad key: type = %q, want authentication_error", got)
	}
	if got := openaiCode(t, call(t, "/v1/responses", bad, codexRequest, http.StatusUnauthorized)); got != "usage_not_included" {
		t.Errorf("openai, bad key: code = %q, want usage_not_included", got)
	}

	_, slow, err := st.CreateKey(context.Background(), "slow", store.KeyLimits{RPMLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	call(t, "/v1/responses", slow, codexRequest, http.StatusOK)
	if got := openaiCode(t, call(t, "/v1/responses", slow, codexRequest, http.StatusTooManyRequests)); got != "slow_down" {
		t.Errorf("openai, rate limited: code = %q, want slow_down", got)
	}
	if got := anthropicType(t, call(t, "/v1/messages", slow, messages, http.StatusTooManyRequests)); got != "rate_limit_error" {
		t.Errorf("anthropic, rate limited: type = %q, want rate_limit_error", got)
	}
}
