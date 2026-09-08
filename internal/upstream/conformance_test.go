package upstream

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Conformance with Anthropic's gateway compatibility contract.
//
//	https://code.claude.com/docs/en/llm-gateway-protocol
//
// Every test here quotes the clause it enforces. That is the point of the file:
// the rest of the suite tests what this gateway does, and these test what the
// contract says it must do — so a change that looks harmless locally has to
// argue with the specification rather than with an assertion someone wrote.
//
// These are written against the Anthropic Messages format, which is the only
// one claudication serves.

// relayFixture runs one request through the relay against a stub upstream, and
// hands back what the upstream saw and what the client got.
type relayFixture struct {
	sawHeader http.Header
	sawBody   []byte
	sawQuery  string
	rec       *httptest.ResponseRecorder
	res       Result
}

func relayThrough(t *testing.T, clientReq *http.Request, body []byte, upstream http.HandlerFunc) *relayFixture {
	t.Helper()
	f := &relayFixture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sawHeader = r.Header.Clone()
		f.sawQuery = r.URL.RawQuery
		f.sawBody, _ = io.ReadAll(r.Body)
		upstream(w, r)
	}))
	t.Cleanup(srv.Close)

	r := &Relay{
		Pool:    &recordingPool{},
		Client:  srv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: srv.URL,
	}
	f.rec = httptest.NewRecorder()
	f.res = r.Do(f.rec, clientReq, "anthropic", "/v1/messages", body, Peek(body))
	return f
}

func jsonUpstream(payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}
}

// "Forward `anthropic-version` and `anthropic-beta` unchanged"
//
// And: "Forward the header verbatim; don't allowlist individual values, because
// the set changes with Claude Code releases."
func TestContractForwardsAnthropicHeadersVerbatim(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("anthropic-version", "2023-06-01")
	// A capability that does not exist yet. A gateway pinned to an observed
	// list would drop it.
	req.Header.Set("anthropic-beta", "context-management-2025-06-27,some-future-beta-2099-01-01")

	f := relayThrough(t, req, []byte("{}"), jsonUpstream(`{"type":"message"}`))

	if got := f.sawHeader.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want it forwarded unchanged", got)
	}
	betas := f.sawHeader.Get("anthropic-beta")
	for _, want := range []string{"context-management-2025-06-27", "some-future-beta-2099-01-01"} {
		if !strings.Contains(betas, want) {
			t.Errorf("anthropic-beta = %q, missing %q; values must not be allowlisted", betas, want)
		}
	}
}

// "pass `anthropic-*` request headers and request body fields through unchanged
// rather than allowlisting the ones you see today. A gateway pinned to an
// observed list strips the next capability's header or field and breaks it on
// the release that introduces it."
func TestContractForwardsUnknownAnthropicHeadersAndBodyFields(t *testing.T) {
	const body = `{"model":"claude-opus-5","messages":[],` +
		`"context_management":{"edits":[]},` +
		`"output_config":{"effort":"high"},` +
		`"some_capability_from_2099":{"enabled":true}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-beta", "whatever-2099-01-01")
	req.Header.Set("anthropic-future-header", "value")
	req.Header.Set("x-claude-code-session-id", "sess-123")

	f := relayThrough(t, req, []byte(body), jsonUpstream(`{"type":"message"}`))

	if got := f.sawHeader.Get("anthropic-future-header"); got != "value" {
		t.Errorf("unknown anthropic-* header = %q, want it forwarded", got)
	}
	if string(f.sawBody) != body {
		t.Errorf("body was modified.\n got: %s\nwant: %s", f.sawBody, body)
	}
}

// "Forward `cache_control` unchanged wherever it appears, and don't convert
// block-form `system` or message content to plain strings."
//
// Attribution is on here, which is the case that could plausibly break it: the
// body already leads with an accepted block, so nothing is rewritten.
func TestContractPreservesCacheControlAndBlockFormSystem(t *testing.T) {
	body := `{"model":"claude-opus-5",` +
		`"system":[{"type":"text","text":"` + ClaudeCodeAttribution + `"},` +
		`{"type":"text","text":"project rules","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hi",` +
		`"cache_control":{"type":"ephemeral"}}]}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	f := relayThroughAttributed(t, req, []byte(body), jsonUpstream(`{"type":"message"}`))

	if string(f.sawBody) != body {
		t.Fatalf("body was modified.\n got: %s\nwant: %s", f.sawBody, body)
	}
	if strings.Count(string(f.sawBody), "cache_control") != 2 {
		t.Error("a cache_control marker was dropped")
	}
	// And the system array is still an array, not a string.
	var probe struct {
		System json.RawMessage `json:"system"`
	}
	if err := json.Unmarshal(f.sawBody, &probe); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(probe.System)), "[") {
		t.Errorf("system was converted out of block form: %s", probe.System)
	}
}

// relayThroughAttributed is relayThrough with the attribution feature on, which
// is how the gateway actually ships.
func relayThroughAttributed(t *testing.T, clientReq *http.Request, body []byte, upstream http.HandlerFunc) *relayFixture {
	t.Helper()
	f := &relayFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sawHeader = r.Header.Clone()
		f.sawBody, _ = io.ReadAll(r.Body)
		upstream(w, r)
	}))
	t.Cleanup(srv.Close)

	r := &Relay{
		Pool:        &recordingPool{},
		Attribution: true,
		Client:      srv.Client(),
		Log:         slog.New(slog.DiscardHandler),
		BaseURL:     srv.URL,
	}
	f.rec = httptest.NewRecorder()
	f.res = r.Do(f.rec, clientReq, "anthropic", "/v1/messages", body, Peek(body))
	return f
}

// "The strip is positional, so it only works when the gateway forwards the
// `system` array unchanged... prepending another system block, reordering the
// array, or converting it to a single string defeats the strip, and the block
// then reaches the model and the prompt cache key."
//
// This is the clause the attribution feature has to be careful about: it adds a
// block when the caller sent none, and must do nothing at all when the caller
// is Claude Code.
func TestContractLeavesAnAttributedBodyByteForByte(t *testing.T) {
	for _, attribution := range []string{
		ClaudeCodeAttribution,
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	} {
		t.Run(attribution[:28], func(t *testing.T) {
			body := `{"model":"claude-opus-5","system":[{"type":"text","text":"` +
				attribution + `"},{"type":"text","text":"rules"}],"messages":[]}`

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			f := relayThroughAttributed(t, req, []byte(body), jsonUpstream(`{"type":"message"}`))

			if string(f.sawBody) != body {
				t.Errorf("an already-attributed body was rewritten, which defeats the positional strip.\n got: %s\nwant: %s",
					f.sawBody, body)
			}
		})
	}
}

// "Keep the block in its own array entry: the endpoint treats a merged block
// that starts with the attribution header as attribution in its entirety and
// drops everything merged into it, including the rest of the system prompt."
//
// So when the gateway does add the block, it must add an entry rather than
// growing the caller's first one.
func TestContractAddsAttributionAsItsOwnEntry(t *testing.T) {
	const body = `{"model":"claude-opus-5","system":[{"type":"text","text":"project rules"}],"messages":[]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	f := relayThroughAttributed(t, req, []byte(body), jsonUpstream(`{"type":"message"}`))

	var sent struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(f.sawBody, &sent); err != nil {
		t.Fatalf("body is not valid JSON after the rewrite: %v", err)
	}
	if len(sent.System) != 2 {
		t.Fatalf("system has %d entries, want 2 (attribution, then the caller's)", len(sent.System))
	}
	if sent.System[0].Text != ClaudeCodeAttribution {
		t.Errorf("first entry = %q, want the attribution alone", sent.System[0].Text)
	}
	if sent.System[1].Text != "project rules" {
		t.Errorf("second entry = %q, want the caller's block untouched", sent.System[1].Text)
	}
}

// "Claude Code counts every byte your gateway relays, including SSE `ping`
// events and comment lines, and aborts a stream that goes silent for 300
// seconds by default... if your gateway strips or buffers them, Claude Code
// aborts the stream during those pauses."
func TestContractRelaysPingsAndCommentLines(t *testing.T) {
	const stream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}` + "\n\n" +
		": this is a comment line\n\n" +
		"event: ping\n" +
		`data: {"type":"ping"}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	f := relayThrough(t, req, []byte("{}"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	})

	got := f.rec.Body.String()
	if got != stream {
		t.Errorf("the stream was not relayed byte for byte.\n got: %q\nwant: %q", got, stream)
	}
	for _, needed := range []string{"event: ping", ": this is a comment line"} {
		if !strings.Contains(got, needed) {
			t.Errorf("%q did not reach the client", needed)
		}
	}
}

// "Stream inference responses. Claude Code reads the stream as it arrives, so
// if your gateway buffers complete responses before relaying them, Claude Code
// stalls."
//
// A real server and a real client, because a ResponseRecorder cannot tell a
// streamed response from a buffered one.
func TestContractDoesNotBufferTheResponse(t *testing.T) {
	release := make(chan struct{})
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_start\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		// The rest of the response is withheld. A gateway that buffers cannot
		// deliver the first event until this is released.
		<-release
		_, _ = io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	defer upstreamSrv.Close()
	defer close(release)

	r := &Relay{
		Pool:    &recordingPool{},
		Client:  upstreamSrv.Client(),
		Log:     slog.New(slog.DiscardHandler),
		BaseURL: upstreamSrv.URL,
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.Do(w, req, "anthropic", "/v1/messages", []byte("{}"), Prologue{})
	}))
	defer gateway.Close()

	resp, err := gateway.Client().Post(gateway.URL+"/v1/messages", "application/json",
		strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The first event must arrive while the upstream is still holding the rest.
	firstLine := make(chan string, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		line, _ := br.ReadString('\n')
		firstLine <- line
	}()

	select {
	case line := <-firstLine:
		if !strings.Contains(line, "message_start") {
			t.Errorf("first line = %q, want the first event", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the client while the upstream was mid-response: the gateway is buffering")
	}
}

// "The retry logic matches on the upstream's error wording, so forward error
// response bodies unmodified. A gateway that wraps upstream errors in its own
// envelope breaks the recovery path, even when it preserves the status code."
func TestContractForwardsErrorBodiesUnmodified(t *testing.T) {
	// The wording Claude Code matches on to disable a capability and retry.
	const upstreamError = `{"type":"error","error":{"type":"invalid_request_error",` +
		`"message":"messages.1: Extra inputs are not permitted"}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	f := relayThrough(t, req, []byte("{}"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_upstream_123")
		// 400 is not retryable, so it reaches the client from the first attempt.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, upstreamError)
	})

	if f.rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want the upstream's 400", f.rec.Code)
	}
	if got := f.rec.Body.String(); got != upstreamError {
		t.Errorf("error body was modified.\n got: %s\nwant: %s", got, upstreamError)
	}
	if got := f.rec.Header().Get("request-id"); got != "req_upstream_123" {
		t.Errorf("upstream response headers were dropped: request-id = %q", got)
	}
}

// The same rule for a refusal the gateway retried past. Running out of accounts
// must answer with the upstream's words, not the gateway's own.
func TestContractForwardsARetriedRefusalUnmodified(t *testing.T) {
	const upstreamError = `{"type":"error","error":{"type":"rate_limit_error",` +
		`"message":"This request would exceed your organization's weekly limit for claude-opus-5."}}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	f := relayThrough(t, req, []byte("{}"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-unified-reset", "2026-09-08T12:00:00Z")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, upstreamError)
	})

	if f.rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the upstream's 429", f.rec.Code)
	}
	if got := f.rec.Body.String(); got != upstreamError {
		t.Errorf("the refusal was replaced.\n got: %s\nwant: %s", got, upstreamError)
	}
	if f.rec.Header().Get("anthropic-ratelimit-unified-reset") == "" {
		t.Error("the reset header was dropped; it is how the client knows when to come back")
	}
}

// "The developer's gateway credential, in one or both headers depending on
// which credential variable they set" — and it authenticates the client to the
// gateway, so it must not travel on to the upstream.
func TestContractDoesNotLeakTheClientCredential(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer clc_the_clients_own_key")
	req.Header.Set("x-api-key", "clc_the_clients_own_key")

	f := relayThrough(t, req, []byte("{}"), jsonUpstream(`{"type":"message"}`))

	if got := f.sawHeader.Get("Authorization"); strings.Contains(got, "clc_") {
		t.Errorf("the client's credential reached the upstream: %q", got)
	}
	if got := f.sawHeader.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key was forwarded to the upstream: %q", got)
	}
}
