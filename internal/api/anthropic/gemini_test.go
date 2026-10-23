package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// The Gemini answers here are written from gemini-cli's reading of Code
// Assist (internal/provider/google, doc.go), not captured: nothing has been
// measured yet.

func translateForGemini(t *testing.T, a API, body string) (geminiwire.Request, Translation) {
	t.Helper()
	ex, err := a.Translate([]byte(body), api.Target{Dialect: geminiDialect, Route: "/v1/messages", Conversation: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	e := ex.(*responsesExchange)
	tr := e.tr
	var req geminiwire.Request
	if err := json.Unmarshal(ex.Request(), &req); err != nil {
		t.Fatal(err)
	}
	return req, tr
}

const claudeCodeToolTurn = `{
  "model": "opus", "max_tokens": 4096, "stream": true, "temperature": 0.5,
  "system": [{"type":"text","text":"x-anthropic-billing-header: cc_version=2"},{"type":"text","text":"You are a coding agent."}],
  "tools": [
    {"name":"Bash","description":"Run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},
    {"name":"mcp__server.with spaces__tool","input_schema":{"type":"object"}},
    {"name":"Deferred","defer_loading":true,"input_schema":{"type":"object"}}
  ],
  "messages": [
    {"role":"user","content":"list the files"},
    {"role":"assistant","content":[
      {"type":"thinking","thinking":"I should run ls","signature":"claudication:gemini-thought:v1:SIGNED"},
      {"type":"text","text":"Running ls."},
      {"type":"tool_use","id":"call-from-gemini","name":"Bash","input":{"command":"ls"}}]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"call-from-gemini","content":[{"type":"text","text":"a.go\nb.go"}]},
      {"type":"text","text":"and now?"}]},
    {"role":"assistant","content":[
      {"type":"thinking","thinking":"Claude's","signature":"EqQBCkYIBxgCKkAanthropic=="},
      {"type":"tool_use","id":"toolu_gw_abc","name":"Bash","input":{"command":"pwd"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_gw_abc","is_error":true,"content":"denied"}]}
  ]
}`

func TestMessagesToGemini(t *testing.T) {
	req, tr := translateForGemini(t, New(), claudeCodeToolTurn)
	if req.Model != "opus" || req.Project != "" {
		t.Errorf("model %q project %q", req.Model, req.Project)
	}
	r := req.Request
	if r.SystemInstruction == nil || r.SystemInstruction.Parts[0].Text != "You are a coding agent." {
		t.Errorf("system = %+v", r.SystemInstruction)
	}
	if r.SessionID != "chat-1" {
		t.Errorf("session = %q", r.SessionID)
	}
	if g := r.GenerationConfig; g == nil || g.MaxOutputTokens != 4096 || g.Temperature == nil || *g.Temperature != 0.5 {
		t.Errorf("generation config = %+v", g)
	}

	decls := r.Tools[0].FunctionDeclarations
	if len(decls) != 2 {
		t.Fatalf("declarations = %+v", decls)
	}
	mcp := decls[1].Name
	if strings.ContainsAny(mcp, " ") || tr.Tools[mcp] != "mcp__server.with spaces__tool" {
		t.Errorf("sanitised name %q maps back to %q", mcp, tr.Tools[mcp])
	}
	if string(decls[0].ParametersJSONSchema) != `{"type":"object","properties":{"command":{"type":"string"}}}` {
		t.Errorf("schema = %s", decls[0].ParametersJSONSchema)
	}

	roles := []string{}
	for _, c := range r.Contents {
		roles = append(roles, c.Role)
	}
	if got := strings.Join(roles, " "); got != "user model user model user" {
		t.Fatalf("roles = %s", got)
	}
	first := r.Contents[1].Parts
	if first[0].Text != "Running ls." || first[1].FunctionCall == nil {
		t.Fatalf("first model turn = %+v", first)
	}
	if c := first[1]; c.FunctionCall.ID != "call-from-gemini" || c.ThoughtSignature != "SIGNED" || string(c.FunctionCall.Args) != `{"command":"ls"}` {
		t.Errorf("call = %+v sig %q", c.FunctionCall, c.ThoughtSignature)
	}
	result := r.Contents[2].Parts
	if fr := result[0].FunctionResponse; fr == nil || fr.Name != "Bash" || fr.ID != "call-from-gemini" || string(fr.Response) != `{"output":"a.go\nb.go"}` {
		t.Errorf("result = %+v", result[0].FunctionResponse)
	}
	if result[1].Text != "and now?" {
		t.Errorf("text after the result = %+v", result[1])
	}
	// Claude's signature is dropped, the gateway's own id is not sent, and
	// the call is sent with the signature gemini-cli uses for history it
	// holds none for.
	second := r.Contents[3].Parts[0]
	if second.FunctionCall.ID != "" || second.ThoughtSignature != syntheticSignature {
		t.Errorf("second call = %+v sig %q", second.FunctionCall, second.ThoughtSignature)
	}
	if fr := r.Contents[4].Parts[0].FunctionResponse; fr == nil || string(fr.Response) != `{"error":"denied"}` || fr.ID != "" {
		t.Errorf("error result = %+v", fr)
	}
}

func TestMessagesToGeminiRefusesWhatItCannotSend(t *testing.T) {
	for name, body := range map[string]string{
		"server tool": `{"model":"m","max_tokens":1,"tools":[{"type":"bash_20250124","name":"bash"}],"messages":[{"role":"user","content":"x"}]}`,
		"file image":  `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"f"}}]}]}`,
		"orphan":      `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"x"}]}]}`,
	} {
		if _, err := New().Translate([]byte(body), api.Target{Dialect: geminiDialect, Route: "/v1/messages"}); err == nil {
			t.Errorf("%s: translated", name)
		}
	}
}

func TestGeminiThinkingIsABudget(t *testing.T) {
	for _, tc := range []struct {
		body    string
		budget  *int
		include bool
	}{
		{`{"thinking":{"type":"enabled","budget_tokens":5000}}`, ptr(5000), true},
		{`{"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"low"}}`, ptr(1024), false},
		{`{"output_config":{"effort":"max"}}`, ptr(-1), false},
		{`{"thinking":{"type":"disabled"}}`, nil, false},
	} {
		var in messagesRequest
		_ = json.Unmarshal([]byte(tc.body), &in)
		tc2, _, _ := geminiThinking(in.Thinking, in.OutputConfig)
		switch {
		case tc.budget == nil && tc2 != nil:
			t.Errorf("%s: %+v", tc.body, tc2)
		case tc.budget != nil && (tc2 == nil || tc2.ThinkingBudget == nil || *tc2.ThinkingBudget != *tc.budget || tc2.IncludeThoughts != tc.include):
			t.Errorf("%s: %+v", tc.body, tc2)
		}
	}
}

func ptr(n int) *int { return &n }

// geminiStream is a Code Assist stream of the given envelopes.
func geminiStream(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\r\n\r\n")
	}
	return b.String()
}

// sseEvents reads a Messages stream back as its event names and payloads.
func sseEvents(t *testing.T, body string) ([]string, []map[string]any) {
	t.Helper()
	var names []string
	var payloads []map[string]any
	for _, frame := range strings.Split(body, "\n\n") {
		name, data := api.FrameFields([]byte(frame))
		if name == "" {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(data, &p)
		names = append(names, name)
		payloads = append(payloads, p)
	}
	return names, payloads
}

func relayGemini(tr Translation, status int, contentType, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s := newResponsesSink(rec, tr, fromGemini)
	if contentType != "" {
		s.Header().Set("Content-Type", contentType)
	}
	s.WriteHeader(status)
	_, _ = s.Write([]byte(body))
	s.Close(nil)
	return rec
}

const geminiToolAnswer = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Listing","thought":true}]}}]},"traceId":"tr1"}`

var geminiToolCall = `{"response":{"candidates":[{"content":{"role":"model","parts":[` +
	`{"text":"I'll look."},` +
	`{"functionCall":{"name":"mcp__server_with_spaces__tool_9","args":{"q": 1}},"thoughtSignature":"SIG2"}]},"finishReason":"STOP"}],` +
	`"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":40,"candidatesTokenCount":5,"thoughtsTokenCount":3}},"traceId":"tr1"}`

func TestGeminiStreamToMessages(t *testing.T) {
	tr := Translation{Model: "opus", Stream: true, Thinking: true,
		Tools: map[string]string{"mcp__server_with_spaces__tool_9": "mcp__server with spaces__tool"}}
	rec := relayGemini(tr, http.StatusOK, "text/event-stream", geminiStream(geminiToolAnswer, geminiToolCall))
	names, p := sseEvents(t, rec.Body.String())
	want := "message_start ping content_block_start content_block_delta content_block_stop " +
		"content_block_start content_block_delta content_block_stop " +
		"content_block_start content_block_delta content_block_stop " +
		"content_block_start content_block_delta content_block_stop message_delta message_stop"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("events:\n%s\nwant\n%s", got, want)
	}
	if id := p[0]["message"].(map[string]any)["id"]; id != "msg_tr1" {
		t.Errorf("message id = %v", id)
	}
	// thinking, text, the call's signature in a thinking block of its own,
	// then the call.
	if d := p[6]["delta"].(map[string]any); d["text"] != "I'll look." {
		t.Errorf("text delta = %v", d)
	}
	if d := p[9]["delta"].(map[string]any); d["signature"] != geminiSignaturePrefix+"SIG2" {
		t.Errorf("signature delta = %v", d)
	}
	block := p[11]["content_block"].(map[string]any)
	if block["name"] != "mcp__server with spaces__tool" || !strings.HasPrefix(block["id"].(string), generatedCallPrefix) {
		t.Errorf("tool_use = %v", block)
	}
	if d := p[12]["delta"].(map[string]any); d["partial_json"] != `{"q":1}` {
		t.Errorf("args = %v", d)
	}
	md := p[len(p)-2]
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Errorf("stop = %v", md["delta"])
	}
	u := md["usage"].(map[string]any)
	if u["input_tokens"] != 60.0 || u["cache_read_input_tokens"] != 40.0 || u["output_tokens"] != 8.0 {
		t.Errorf("usage = %v", u)
	}
}

func TestGeminiSignatureIsKeptForACallerWithoutThinking(t *testing.T) {
	a := New()
	tr := Translation{Model: "opus", Stream: false, conversation: "chat-2", reasoning: a.reasoning}
	rec := relayGemini(tr, http.StatusOK, "text/event-stream", geminiStream(geminiToolCall))
	var msg struct {
		StopReason string           `json:"stop_reason"`
		Content    []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if msg.StopReason != "tool_use" || len(msg.Content) != 2 || msg.Content[1]["type"] != "tool_use" {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Content[1]["input"].(map[string]any)["q"] != 1.0 {
		t.Errorf("input = %v", msg.Content[1]["input"])
	}
	id := msg.Content[1]["id"].(string)

	// The next turn sends the call back with no thinking block: the gateway
	// puts the signature back on it.
	next := `{"model":"opus","max_tokens":10,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"mcp__server with spaces__tool","input":{"q":1}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"done"}]}]}`
	ex, err := a.Translate([]byte(next), api.Target{Dialect: geminiDialect, Route: "/v1/messages", Conversation: "chat-2"})
	if err != nil {
		t.Fatal(err)
	}
	var req geminiwire.Request
	_ = json.Unmarshal(ex.Request(), &req)
	if sig := req.Request.Contents[1].Parts[0].ThoughtSignature; sig != "SIG2" {
		t.Errorf("signature restored = %q", sig)
	}
}

func TestGeminiAnswerEnds(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"max tokens": {geminiStream(`{"response":{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"MAX_TOKENS"}]}}`), "max_tokens"},
		"safety":     {geminiStream(`{"response":{"candidates":[{"finishReason":"SAFETY"}]}}`), "refusal"},
		"blocked":    {geminiStream(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}}`), "refusal"},
	} {
		rec := relayGemini(Translation{Model: "m", Stream: true}, http.StatusOK, "text/event-stream", tc.body)
		names, p := sseEvents(t, rec.Body.String())
		if names[len(names)-1] != "message_stop" {
			t.Errorf("%s: ended with %v", name, names)
			continue
		}
		if got := p[len(p)-2]["delta"].(map[string]any)["stop_reason"]; got != tc.want {
			t.Errorf("%s: stop = %v", name, got)
		}
	}
	// A stream that stops before any finish reason is an error, not a
	// silent end.
	rec := relayGemini(Translation{Model: "m", Stream: true}, http.StatusOK, "text/event-stream",
		geminiStream(`{"response":{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}}`))
	if names, _ := sseEvents(t, rec.Body.String()); names[len(names)-1] != "error" {
		t.Errorf("cut stream ended with %v", names)
	}
}

func TestGeminiRefusalInAnthropicsEnvelope(t *testing.T) {
	body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity"}}`
	rec := relayGemini(Translation{Model: "m", Stream: true}, http.StatusTooManyRequests, "application/json", body)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d", rec.Code)
	}
	var e struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Error.Type != "rate_limit_error" || e.Error.Message != "You have exhausted your capacity" {
		t.Errorf("error = %+v", e.Error)
	}
	// An array, as the streamed method answers a failure without ?alt=sse.
	if kind, _ := geminiError(400, []byte(`[{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"bad"}}]`)); kind != "invalid_request_error" {
		t.Errorf("array kind = %q", kind)
	}
}

func TestGeminiCountIsEstimated(t *testing.T) {
	ex, err := New().Translate([]byte(claudeCodeToolTurn), api.Target{Dialect: geminiDialect, Route: countTokensRoute})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := ex.(api.Answerer)
	if !ok {
		t.Fatalf("%T answers nothing itself", ex)
	}
	rec := httptest.NewRecorder()
	a.Answer(rec)
	if !strings.Contains(rec.Header().Get(EstimateHeader), "Gemini") {
		t.Errorf("header = %q", rec.Header().Get(EstimateHeader))
	}
	var n struct {
		InputTokens int `json:"input_tokens"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &n)
	if n.InputTokens < 20 {
		t.Errorf("estimate = %d", n.InputTokens)
	}
}
