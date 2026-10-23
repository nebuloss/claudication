package gemini

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// The requests here are shaped as gemini-cli builds them (its tools declared
// with parametersJsonSchema, its call ids name-time-random, its results
// {"output": …}); none was captured from it yet.

const cliTurn = `{
  "systemInstruction": {"parts": [{"text": "You are an interactive CLI agent."}]},
  "tools": [{"functionDeclarations": [
    {"name": "read_file", "description": "Reads a file", "parametersJsonSchema": {"type": "object", "properties": {"path": {"type": "string"}}}},
    {"name": "mcp.server:tool", "parameters": {"type": "OBJECT", "properties": {"q": {"type": "STRING"}}}}
  ]}, {"googleSearch": {}}],
  "generationConfig": {"temperature": 0.7, "thinkingConfig": {"includeThoughts": true, "thinkingBudget": 4000}},
  "contents": [
    {"role": "user", "parts": [{"text": "read go.mod"}]},
    {"role": "model", "parts": [
      {"text": "thinking about it", "thought": true},
      {"functionCall": {"id": "read_file-1730000000000-ab12", "name": "read_file", "args": {"path": "go.mod"}}, "thoughtSignature": "SIGNED"}]},
    {"role": "user", "parts": [{"functionResponse": {"id": "read_file-1730000000000-ab12", "name": "read_file", "response": {"output": "module x"}}}]}
  ]
}`

func TestEnvelopeCarriesThePath(t *testing.T) {
	e, err := readEnvelope(Envelope("gemini-2.5-pro", "streamGenerateContent", []byte(`{"contents":[]}`)))
	if err != nil || e.Model != "gemini-2.5-pro" || e.Method != "streamGenerateContent" || string(e.Request) != `{"contents":[]}` {
		t.Fatalf("envelope = %+v, %v", e, err)
	}
	if _, err := readEnvelope(Envelope("m", "embedContent", nil)); err == nil {
		t.Error("an unserved method was read")
	}
	if _, err := readEnvelope(Envelope("", "generateContent", nil)); err == nil {
		t.Error("no model was read")
	}
}

func TestGeminiToMessages(t *testing.T) {
	ex, err := New().Decode(Envelope("large", "streamGenerateContent", []byte(cliTurn)))
	if err != nil {
		t.Fatal(err)
	}
	if ex.Model() != "large" || !ex.Streaming() {
		t.Errorf("model %q streaming %v", ex.Model(), ex.Streaming())
	}
	tr := ex.(*messagesExchange).tr
	var m struct {
		Model     string           `json:"model"`
		MaxTokens int              `json:"max_tokens"`
		System    string           `json:"system"`
		Stream    bool             `json:"stream"`
		Tools     []map[string]any `json:"tools"`
		Messages  []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Thinking    map[string]any `json:"thinking"`
		Temperature *float64       `json:"temperature"`
	}
	if err := json.Unmarshal(ex.Request(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Model != "large" || !m.Stream || m.System != "You are an interactive CLI agent." || m.MaxTokens != defaultMaxTokens {
		t.Errorf("request = %+v", m)
	}
	if len(m.Tools) != 3 || m.Tools[2]["type"] != "web_search_20250305" {
		t.Fatalf("tools = %v", m.Tools)
	}
	renamed, _ := m.Tools[1]["name"].(string)
	if strings.ContainsAny(renamed, ".:") || tr.tools[renamed] != "mcp.server:tool" {
		t.Errorf("renamed %q maps back to %q", renamed, tr.tools[renamed])
	}
	schema, _ := json.Marshal(m.Tools[1]["input_schema"])
	if string(schema) != `{"properties":{"q":{"type":"string"}},"type":"object"}` {
		t.Errorf("lowered schema = %s", schema)
	}
	if len(m.Messages) != 3 {
		t.Fatalf("messages = %+v", m.Messages)
	}
	call := m.Messages[1].Content[0]
	if call["type"] != "tool_use" || call["id"] != "read_file-1730000000000-ab12" || call["name"] != "read_file" {
		t.Errorf("call = %v", call)
	}
	result := m.Messages[2].Content[0]
	if result["type"] != "tool_result" || result["tool_use_id"] != "read_file-1730000000000-ab12" || result["content"] != "module x" {
		t.Errorf("result = %v", result)
	}
	// The call came back with Gemini's own signature, not a Claude thinking
	// block: Claude would refuse thinking on that history, so it is left off.
	if m.Thinking != nil {
		t.Errorf("thinking = %v", m.Thinking)
	}
	if m.Temperature == nil || *m.Temperature != 0.7 {
		t.Errorf("temperature = %v", m.Temperature)
	}
}

func TestClaudeThinkingRidesInTheSignature(t *testing.T) {
	sig := encodeThinking(carriedThinking{Thinking: "plan", Signature: "EqQBanthropic=="})
	body := `{"generationConfig":{"thinkingConfig":{"thinkingBudget":2048}},"contents":[
	  {"role":"user","parts":[{"text":"go"}]},
	  {"role":"model","parts":[{"functionCall":{"name":"run","args":{}},"thoughtSignature":"` + sig + `"}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"run","response":{"error":"denied"}}}]}]}`
	ex, err := New().Decode(Envelope("large", "generateContent", []byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		MaxTokens int `json:"max_tokens"`
		Messages  []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
		Thinking map[string]any `json:"thinking"`
	}
	_ = json.Unmarshal(ex.Request(), &m)
	turn := m.Messages[1].Content
	if turn[0]["type"] != "thinking" || turn[0]["thinking"] != "plan" || turn[0]["signature"] != "EqQBanthropic==" {
		t.Errorf("thinking block = %v", turn[0])
	}
	if turn[1]["type"] != "tool_use" || turn[1]["id"] != "toolu_gem_1_1" {
		t.Errorf("call = %v", turn[1])
	}
	if r := m.Messages[2].Content[0]; r["tool_use_id"] != "toolu_gem_1_1" || r["is_error"] != true || r["content"] != "denied" {
		t.Errorf("result = %v", r)
	}
	if m.Thinking["budget_tokens"] != 2048.0 || m.MaxTokens <= 2048 {
		t.Errorf("thinking %v, max_tokens %d", m.Thinking, m.MaxTokens)
	}
}

func TestGeminiRefusesWhatClaudeCannotTake(t *testing.T) {
	for name, body := range map[string]string{
		"audio":  `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"AAAA"}}]}]}`,
		"orphan": `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"x","response":{}}}]}]}`,
		"empty":  `{"contents":[]}`,
	} {
		if _, err := New().Decode(Envelope("m", "generateContent", []byte(body))); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}

// messagesStream is Claude's answer to a thinking tool turn.
func messagesStream() string {
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	return ev("message_start", `{"type":"message_start","message":{"id":"msg_01abc","model":"claude-opus-5-5","usage":{"input_tokens":20,"cache_read_input_tokens":100}}}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQB=="}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Reading."}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp_server_tool_1a2b3c4d","input":{}}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":2}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
}

func chunks(t *testing.T, body string) []geminiwire.GenerateResponse {
	t.Helper()
	var out []geminiwire.GenerateResponse
	for _, frame := range strings.Split(body, "\r\n\r\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(frame), "data: ")
		if !ok {
			continue
		}
		var r geminiwire.GenerateResponse
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		out = append(out, r)
	}
	return out
}

func relay(ex api.Exchange, status int, contentType, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s := ex.Sink(rec)
	if contentType != "" {
		s.Header().Set("Content-Type", contentType)
	}
	s.WriteHeader(status)
	_, _ = s.Write([]byte(body))
	s.Close(nil)
	return rec
}

func TestMessagesAnswerAsGemini(t *testing.T) {
	ex := &messagesExchange{tr: translation{model: "large", stream: true, thoughts: true,
		tools: map[string]string{"mcp_server_tool_1a2b3c4d": "mcp.server:tool"}}}
	rec := relay(ex, http.StatusOK, "text/event-stream", messagesStream())
	cs := chunks(t, rec.Body.String())
	if len(cs) != 4 {
		t.Fatalf("%d chunks:\n%s", len(cs), rec.Body)
	}
	if p := cs[0].Candidates[0].Content.Parts[0]; !p.Thought || p.Text != "plan" {
		t.Errorf("thought = %+v", p)
	}
	text := cs[1].Candidates[0].Content.Parts[0]
	if text.Text != "Reading." {
		t.Errorf("text = %+v", text)
	}
	carried, ok := decodeThinking(text.ThoughtSignature)
	if !ok || carried.Thinking != "plan" || carried.Signature != "EqQB==" {
		t.Errorf("the thinking block was not carried on the next part: %q", text.ThoughtSignature)
	}
	call := cs[2].Candidates[0].Content.Parts[0].FunctionCall
	if call == nil || call.Name != "mcp.server:tool" || string(call.Args) != `{"q":1}` || call.ID != "toolu_1" {
		t.Errorf("call = %+v", call)
	}
	last := cs[3]
	if last.Candidates[0].FinishReason != "STOP" || last.ModelVersion != "large" || last.ResponseID != "01abc" {
		t.Errorf("last = %+v", last)
	}
	if u := last.UsageMetadata; u == nil || u.PromptTokenCount != 120 || u.CachedContentTokenCount != 100 || u.CandidatesTokenCount != 9 {
		t.Errorf("usage = %+v", last.UsageMetadata)
	}
}

func TestMessagesAnswerWholeAndRefused(t *testing.T) {
	ex := &messagesExchange{tr: translation{model: "large"}}
	rec := relay(ex, http.StatusOK, "application/json",
		`{"id":"msg_9","model":"claude","stop_reason":"max_tokens","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	var r geminiwire.GenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Candidates[0].FinishReason != "MAX_TOKENS" || r.Candidates[0].Content.Parts[0].Text != "hi" || r.UsageMetadata.TotalTokenCount != 4 {
		t.Errorf("response = %+v", r)
	}

	rec = relay(&messagesExchange{tr: translation{model: "large", stream: true}}, http.StatusTooManyRequests, "application/json",
		`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	var e geminiwire.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusTooManyRequests || e.Error.Status != "RESOURCE_EXHAUSTED" || e.Error.Message != "slow down" || e.Error.Code != 429 {
		t.Errorf("refusal = %d %+v", rec.Code, e)
	}
}

// codeAssistStream is Code Assist's answer: each chunk wrapped.
const codeAssistStream = "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]}}],\"modelVersion\":\"gemini-2.5-pro\"},\"traceId\":\"tr9\"}\r\n\r\n" +
	"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]}}]},\"traceId\":\"tr9\"}\r\n\r\n" +
	"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"ls\",\"args\":{}},\"thoughtSignature\":\"S\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7}},\"traceId\":\"tr9\"}\r\n\r\n"

func TestNativeRewrapsAndUnwraps(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`
	ex, err := New().DecodeNative(Envelope("large", "streamGenerateContent", []byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(ex.Request()); got != `{"model":"large","request":`+body+`}` {
		t.Errorf("rewrapped = %s", got)
	}
	h := http.Header{"X-Goog-Api-Key": {"clc_x"}}
	ex.Headers(h)
	if h.Get("X-Goog-Api-Key") != "" {
		t.Error("the caller's key went upstream")
	}

	rec := relay(ex, http.StatusOK, "text/event-stream", codeAssistStream)
	cs := chunks(t, rec.Body.String())
	if len(cs) != 3 || cs[0].Candidates[0].Content.Parts[0].Text != "Hel" || cs[0].ResponseID != "tr9" {
		t.Fatalf("chunks = %+v", cs)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("type = %q", rec.Header().Get("Content-Type"))
	}

	// generateContent: the same stream gathered into one response.
	ex, _ = New().DecodeNative(Envelope("large", "generateContent", []byte(body)))
	rec = relay(ex, http.StatusOK, "text/event-stream", codeAssistStream)
	var r geminiwire.GenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	parts := r.Candidates[0].Content.Parts
	if len(parts) != 2 || parts[0].Text != "Hello" || parts[1].FunctionCall == nil || parts[1].ThoughtSignature != "S" {
		t.Errorf("gathered parts = %+v", parts)
	}
	if r.Candidates[0].FinishReason != "STOP" || r.UsageMetadata.PromptTokenCount != 7 || r.ModelVersion != "gemini-2.5-pro" {
		t.Errorf("gathered = %+v", r)
	}
}

func TestNativeErrorsPassAndACutStreamFails(t *testing.T) {
	refusal := `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`
	ex, _ := New().DecodeNative(Envelope("large", "streamGenerateContent", []byte(`{"contents":[]}`)))
	rec := relay(ex, http.StatusTooManyRequests, "application/json", refusal)
	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != refusal {
		t.Errorf("refusal = %d %s", rec.Code, rec.Body)
	}

	ex, _ = New().DecodeNative(Envelope("large", "streamGenerateContent", []byte(`{"contents":[]}`)))
	cut := strings.SplitAfter(codeAssistStream, "\r\n\r\n")[0]
	rec = relay(ex, http.StatusOK, "text/event-stream", cut)
	if !strings.Contains(rec.Body.String(), `"error":{"code":502`) {
		t.Errorf("a cut stream ended without an error chunk:\n%s", rec.Body)
	}
}

func TestCountIsEstimated(t *testing.T) {
	for _, decode := range []func([]byte) (api.Exchange, error){New().Decode, New().DecodeNative} {
		ex, err := decode(Envelope("large", "countTokens", []byte(cliTurn)))
		if err != nil {
			t.Fatal(err)
		}
		a, ok := ex.(api.Answerer)
		if !ok {
			t.Fatalf("%T is not answered here", ex)
		}
		rec := httptest.NewRecorder()
		a.Answer(rec)
		var n struct {
			TotalTokens int `json:"totalTokens"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &n)
		if n.TotalTokens < 20 || rec.Header().Get(estimateHeader) == "" {
			t.Errorf("count = %d, header %q", n.TotalTokens, rec.Header().Get(estimateHeader))
		}
	}
}

func TestEveryErrorKindHasAStatus(t *testing.T) {
	for _, k := range api.ErrorKinds {
		rec := httptest.NewRecorder()
		New().WriteError(rec, 400, k, "x")
		var e geminiwire.ErrorEnvelope
		if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error.Status == "" || e.Error.Code != 400 {
			t.Errorf("%s: %s", k, rec.Body)
		}
	}
}
