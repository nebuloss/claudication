package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/api"
)

// --- the api.Protocol surface ----------------------------------------------

// The ID is a stored settings key; renaming it would silently reset the
// operator's switch for this surface.
func TestSurfaceIdentity(t *testing.T) {
	var p api.Protocol = New("claude-sonnet-5", 1000)
	if p.ID() != "openai" {
		t.Errorf("ID = %q, want the stored key \"openai\"", p.ID())
	}
	if p.Title() == "" {
		t.Error("empty title: the admin UI would list a nameless surface")
	}
	if r := p.Routes(); len(r) != 1 || r[0] != "/v1/responses" {
		t.Errorf("Routes = %v, want only /v1/responses (all Codex speaks)", r)
	}
}

// Decode is where a Codex model name is swapped for a Claude one and the
// required max_tokens is filled in; an exchange that skipped either would be
// refused upstream for every default Codex install.
func TestDecodeAppliesTheSurfaceDefaults(t *testing.T) {
	ex, err := New("claude-sonnet-5", 1234).Decode([]byte(
		`{"model":"gpt-5-codex","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ex.Model() != "claude-sonnet-5" || !ex.Streaming() {
		t.Errorf("model/stream = %q/%v", ex.Model(), ex.Streaming())
	}
	body := ex.Request()
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "claude-sonnet-5" || sent.MaxTokens != 1234 || !sent.Stream {
		t.Errorf("sent = %+v", sent)
	}
	// The exchange outlives the request by the length of the stream; keeping
	// the body would pin the whole conversation in memory for minutes.
	if again := ex.Request(); again != nil {
		t.Errorf("second Request() = %d bytes, want nil: the body is handed over once", len(again))
	}

	if _, err := New("", 0).Decode([]byte(`{"input":[]}`)); err == nil {
		t.Error("no model anywhere was accepted; the upstream would refuse it anyway")
	}
	if _, err := New("m", 0).Decode([]byte(`not json`)); err == nil {
		t.Error("an unparseable body was accepted")
	}
}

// Codex reports an error it cannot parse as a bare stream failure, so even the
// gateway's own refusals must be in OpenAI's envelope.
func TestWriteErrorIsOpenAIShaped(t *testing.T) {
	rec := httptest.NewRecorder()
	New("m", 0).WriteError(rec, http.StatusNotFound, "not_found", "the OpenAI surface is switched off")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var env struct {
		Error responsesError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "not_found" || env.Error.Message != "the OpenAI surface is switched off" {
		t.Errorf("error = %+v, want the gateway's own kind passed through", env.Error)
	}
}

// session_id is sent on every request, conversation_id only once the thread
// exists; taking session_id first keeps a chat's first request in its group.
func TestConversationIDPrefersSessionID(t *testing.T) {
	p := New("m", 0)
	h := http.Header{}
	h.Set("Conversation_id", "conv-1")
	if got := p.ConversationID(h, nil); got != "conv-1" {
		t.Errorf("conversation_id alone = %q", got)
	}
	h.Set("Session_id", "sess-1")
	if got := p.ConversationID(h, nil); got != "sess-1" {
		t.Errorf("both = %q, want session_id", got)
	}
	if got := p.ConversationID(http.Header{}, []byte(`{"metadata":{}}`)); got != "" {
		t.Errorf("none = %q", got)
	}
}

// Headers that describe the OpenAI dialect describe nothing upstream, and a
// request that looks like nothing else is the kind that gets refused. Every
// other header must survive: auth and tracing are the caller's.
func TestHeadersDropOnlyTheDialect(t *testing.T) {
	ex, err := New("m", 0).Decode([]byte(`{"model":"claude-x","input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for _, name := range []string{"OpenAI-Beta", "OpenAI-Organization", "OpenAI-Project", "Originator",
		"Session_id", "Conversation_id", "X-Stainless-Lang", "X-Stainless-Runtime-Version"} {
		h.Set(name, "v")
	}
	h.Set("Authorization", "Bearer k")
	h.Set("Traceparent", "t")
	h.Set("X-Request-Id", "r")
	ex.Headers(h)
	if len(h) != 3 || h.Get("Authorization") != "Bearer k" || h.Get("Traceparent") != "t" || h.Get("X-Request-Id") != "r" {
		t.Errorf("headers after = %v, want only the three non-dialect ones", h)
	}
}

// --- answers through the reshaping sink -----------------------------------

func exchangeFor(t *testing.T, body string) api.Exchange {
	t.Helper()
	ex, err := New("claude-sonnet-5", 100).Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// The normal Codex path end to end: upstream SSE in, Responses SSE out, with
// the headers that stop a proxy from buffering the stream.
func TestSinkTranslatesAStream(t *testing.T) {
	ex := exchangeFor(t, `{"model":"gpt-5","stream":true,"input":[]}`)
	rec := httptest.NewRecorder()
	s := ex.Sink(rec)
	s.Header().Set("Content-Type", "text/event-stream")
	s.Header().Set("Request-Id", "req_1")
	s.WriteHeader(http.StatusOK)
	_, _ = s.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_9\"}}\n\n"))
	_, _ = s.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	s.Close(nil)

	fs := frames(t, rec.Body.String())
	if got := strings.Join(events(fs), ","); got != "response.created,response.completed" {
		t.Errorf("events = %s", got)
	}
	if id := last(fs).data["response"].(map[string]any)["id"]; id != "resp_9" {
		t.Errorf("id = %v, want the upstream message id carried over", id)
	}
	if rec.Header().Get("Request-Id") != "req_1" {
		t.Error("the upstream's request id was lost; it is what support asks for")
	}
}

// A non-streaming answer comes back whole and converted.
func TestSinkConvertsACompleteAnswer(t *testing.T) {
	ex := exchangeFor(t, `{"model":"gpt-5","input":[]}`)
	rec := httptest.NewRecorder()
	s := ex.Sink(rec)
	s.Header().Set("Content-Type", "application/json")
	s.Header().Set("Content-Length", "999")
	_, _ = s.Write([]byte(`{"id":"msg_5","type":"message","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`))
	s.Close(nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %s", rec.Body)
	}
	if out.ID != "resp_5" || out.Object != "response" {
		t.Errorf("out = %+v", out)
	}
	if rec.Header().Get("Content-Length") == "999" {
		t.Error("the upstream's Content-Length was forwarded for a body of a different length")
	}
}

// A refusal keeps its status — Codex backs off on 429 — and its words, in the
// envelope Codex parses.
func TestSinkTranslatesARefusal(t *testing.T) {
	ex := exchangeFor(t, `{"model":"gpt-5","input":[]}`)
	rec := httptest.NewRecorder()
	s := ex.Sink(rec)
	s.Header().Set("Retry-After", "30")
	s.WriteHeader(http.StatusTooManyRequests)
	_, _ = s.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`))
	s.Close(nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Errorf("status %d Retry-After %q: both must reach the client", rec.Code, rec.Header().Get("Retry-After"))
	}
	var env struct {
		Error responsesError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "slow_down" || !strings.Contains(env.Error.Message, "rate limit") {
		t.Errorf("error = %+v", env.Error)
	}
}

// An answer the gateway cannot read is reported in the dialect too.
func TestSinkReportsAnUnreadableAnswer(t *testing.T) {
	ex := exchangeFor(t, `{"model":"gpt-5","input":[]}`)
	rec := httptest.NewRecorder()
	s := ex.Sink(rec)
	_, _ = s.Write([]byte(`<html>bad gateway</html>`))
	s.Close(nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d", rec.Code)
	}
	var env struct {
		Error responsesError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not an envelope: %s", rec.Body)
	}
	if env.Error.Code != "server_error" || !strings.Contains(env.Error.Message, "could not read") {
		t.Errorf("error = %+v", env.Error)
	}
}

// Losing the upstream's own words is the failure this gateway exists to avoid,
// so even a body that is not an error envelope is passed on as the message.
func TestAnthropicErrorFields(t *testing.T) {
	cases := []struct{ body, kind, msg string }{
		{`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "overloaded_error", "Overloaded"},
		{`  upstream connect error  `, "", "upstream connect error"},
		{`{"error":{"type":"x"}}`, "", `{"error":{"type":"x"}}`},
		{``, "", "the upstream refused the request without saying why"},
	}
	for _, c := range cases {
		kind, msg := anthropicErrorFields([]byte(c.body))
		if kind != c.kind || msg != c.msg {
			t.Errorf("anthropicErrorFields(%q) = %q, %q; want %q, %q", c.body, kind, msg, c.kind, c.msg)
		}
	}
}

// Each Anthropic kind must land on a code Codex acts on; the wrong one turns a
// quota problem into a retry loop or an outage into a permanent failure.
func TestErrorEnvelopeCodes(t *testing.T) {
	cases := []struct{ kind, msg, code, wantMsg string }{
		{"api_error", "boom", "server_is_overloaded", "boom"},
		{"authentication_error", "bad key", "usage_not_included", "bad key"},
		{"permission_error", "no", "usage_not_included", "no"},
		{"invalid_request_error", "bad", "invalid_prompt", "bad"},
		{"invalid_request_error", "input exceeds the context window", "context_length_exceeded", "input exceeds the context window"},
		{"api_error", "Too many tokens", "context_length_exceeded", "Too many tokens"},
		{"", "", "server_error", "upstream error"},
	}
	for _, c := range cases {
		var env struct {
			Error responsesError `json:"error"`
		}
		if err := json.Unmarshal(ErrorEnvelope(c.kind, c.msg), &env); err != nil {
			t.Fatal(err)
		}
		if env.Error.Code != c.code || env.Error.Message != c.wantMsg {
			t.Errorf("ErrorEnvelope(%q, %q) = %+v, want %s/%s", c.kind, c.msg, env.Error, c.code, c.wantMsg)
		}
	}
}

// --- request mapping details ------------------------------------------------

// Codex sends a tool's output in more than one shape; whichever it is, the
// model must see the text, not an empty result or a Go zero value.
func TestOutputTextShapes(t *testing.T) {
	cases := []struct{ raw, want string }{
		{``, ""},
		{`"plain"`, "plain"},
		{`{"output":"from output"}`, "from output"},
		{`{"content":"from content"}`, "from content"},
		{`{"text":"from text"}`, "from text"},
		{`{"output":"","text":"skips empty"}`, "skips empty"},
		{`{"other":1}`, `{"other":1}`},
		{`[1,2]`, `[1,2]`},
		{`42`, `42`},
	}
	for _, c := range cases {
		if got := outputText(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("outputText(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// Responses and Anthropic name the same choices differently; "required" in
// particular is "any" upstream, and an unmapped one must fall back to auto
// rather than send a shape the upstream rejects.
func TestConvertToolChoice(t *testing.T) {
	f := false
	cases := []struct {
		raw      string
		parallel *bool
		want     string
	}{
		{``, nil, `{"type":"auto"}`},
		{`"auto"`, nil, `{"type":"auto"}`},
		{`"none"`, nil, `{"type":"none"}`},
		{`"required"`, nil, `{"type":"any"}`},
		{`"something-new"`, nil, `{"type":"auto"}`},
		{`{"type":"function","name":"shell"}`, nil, `{"name":"shell","type":"tool"}`},
		{`{"type":"function"}`, nil, `{"type":"auto"}`},
		{`"required"`, &f, `{"disable_parallel_tool_use":true,"type":"any"}`},
	}
	for _, c := range cases {
		got, _ := json.Marshal(convertToolChoice(json.RawMessage(c.raw), c.parallel))
		if string(got) != c.want {
			t.Errorf("convertToolChoice(%s) = %s, want %s", c.raw, got, c.want)
		}
	}
}

// --- reasoning --------------------------------------------------------------

// Thinking must come through as a reasoning item of its own, not merged into
// the message text: Codex shows the two differently, and a signature (which
// has no counterpart) must not leak into either.
func TestThinkingBecomesAReasoningItem(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_r"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	want := "response.created,response.output_item.added,response.reasoning_text.delta,response.reasoning_text.delta," +
		"response.output_item.done,response.output_item.added,response.output_text.delta,response.output_item.done,response.completed"
	if got := strings.Join(events(fs), ","); got != want {
		t.Fatalf("events = %s\nwant %s", got, want)
	}
	added := fs[1].data["item"].(map[string]any)
	if added["type"] != "reasoning" || added["id"] != "rs_resp_r_0" {
		t.Errorf("added = %v", added)
	}
	done := fs[4].data["item"].(map[string]any)
	content := done["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "let me think" {
		t.Errorf("reasoning content = %v, want the whole thought and no signature", content)
	}
	output := last(fs).data["response"].(map[string]any)["output"].([]any)
	if len(output) != 2 || output[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "answer" {
		t.Errorf("output = %v, want the reasoning then the message, the message text alone", output)
	}
}

// A delta with no open block has nowhere to go; attaching it to the last item
// would put text into an item Codex has already been told is done.
func TestDeltaWithoutAnOpenBlockIsDropped(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_x"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orphan"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	if got := strings.Join(events(fs), ","); got != "response.created,response.completed" {
		t.Errorf("events = %s, want the orphan delta and stop ignored", got)
	}
}

// The non-streaming path must agree with the streaming one on reasoning,
// unknown blocks and truncation, or the same answer reads differently by
// transport.
func TestCompleteAnswerWithThinkingAndTruncation(t *testing.T) {
	body := []byte(`{"id":"msg_t","stop_reason":"max_tokens","content":[
	  {"type":"thinking","thinking":"hmm","signature":"s"},
	  {"type":"server_tool_use","id":"x"},
	  {"type":"text","text":"partial"},
	  {"type":"tool_use","id":"toolu_7","name":"run"}
	],"usage":{"input_tokens":1,"cache_creation_input_tokens":2,"output_tokens":3}}`)
	got, err := AnthropicToResponses(body, Request{Model: "fallback-model",
		Tools: map[string]ToolOrigin{"run": {Namespace: "shell", Name: "run"}}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Status     string           `json:"status"`
		Model      string           `json:"model"`
		Incomplete map[string]any   `json:"incomplete_details"`
		Output     []map[string]any `json:"output"`
		Usage      struct {
			InputTokens int `json:"input_tokens"`
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "incomplete" || out.Incomplete["reason"] != "max_output_tokens" {
		t.Errorf("status %q details %v: truncation must not read as a finished turn", out.Status, out.Incomplete)
	}
	if out.Model != "fallback-model" {
		t.Errorf("model = %q, want the request's when the answer names none", out.Model)
	}
	if len(out.Output) != 3 {
		t.Fatalf("output = %v, want reasoning, message, call and the unknown block skipped", out.Output)
	}
	if out.Output[0]["type"] != "reasoning" || out.Output[1]["type"] != "message" || out.Output[2]["type"] != "function_call" {
		t.Errorf("types = %v %v %v", out.Output[0]["type"], out.Output[1]["type"], out.Output[2]["type"])
	}
	call := out.Output[2]
	if call["arguments"] != "{}" || call["namespace"] != "shell" || call["call_id"] != "toolu_7" || call["id"] != "fc_7" {
		t.Errorf("call = %v: no input must still be an object, and the namespace must come back", call)
	}
	if out.Usage.InputTokens != 3 || out.Usage.TotalTokens != 6 {
		t.Errorf("usage = %+v, want cache creation folded into input", out.Usage)
	}

	if _, err := AnthropicToResponses([]byte(`[`), Request{}); err == nil {
		t.Error("an unreadable answer converted without error")
	}
}
