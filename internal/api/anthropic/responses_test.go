package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"claudication/internal/api"
)

// The Messages → Responses translation, read against the backend's own
// answers (testdata/chatgpt-*.sse, measured 2026-10-20 and scrubbed) and
// against a request shaped as Claude Code 2.1.304 sends one.

// claudeCodeTurn is a Claude Code request in the fields that matter: its
// billing line and cache_control on the system prompt, a system turn inside
// messages, deferred tools one of which a ToolSearch loaded, a tool round
// trip with a picture and an error, thinking of both kinds, and what the
// backend refuses (temperature, stop_sequences, metadata).
const claudeCodeTurn = `{
 "model":"claude-large","max_tokens":32000,"stream":true,
 "system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304; cc_entrypoint=cli;"},
           {"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],
 "messages":[
  {"role":"user","content":[{"type":"text","text":"read note.txt","cache_control":{"type":"ephemeral"}},
                            {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0K"}},
                            {"type":"document","title":"spec.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0x"}}]},
  {"role":"system","content":[{"type":"text","text":"# Environment"}]},
  {"role":"assistant","content":[
     {"type":"thinking","thinking":"from claude","signature":"EqQBCkgIARABGAIiQK+/="},
     {"type":"thinking","thinking":"**Reading**","signature":"claudication:openai-reasoning:v1:rs_1:gAAAAenc"},
     {"type":"text","text":"Reading it."},
     {"type":"tool_use","id":"call_1","name":"Read","input":{"file_path":"note.txt"}},
     {"type":"tool_use","id":"call_2","name":"ToolSearch","input":{"query":"web"}}]},
  {"role":"user","content":[
     {"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"hello"},
        {"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]},
     {"type":"tool_result","tool_use_id":"call_2","content":[{"type":"tool_reference","tool_name":"WebFetch"}]},
     {"type":"tool_result","tool_use_id":"call_3","is_error":true,"content":"<tool_use_error>no</tool_use_error>"},
     {"type":"text","text":"go on"}]}],
 "tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}},"cache_control":{"type":"ephemeral"}},
          {"name":"ToolSearch","input_schema":{"type":"object"}},
          {"name":"WebFetch","defer_loading":true,"input_schema":{"type":"object"}},
          {"name":"NeverLoaded","defer_loading":true,"input_schema":{"type":"object"}}],
 "tool_choice":{"type":"any","disable_parallel_tool_use":true},
 "thinking":{"type":"adaptive"},
 "output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object"}}},
 "context_management":{"edits":[]},
 "temperature":0.5,"top_p":0.9,"top_k":5,"stop_sequences":["x"],
 "metadata":{"user_id":"{\"session_id\":\"s\"}"}
}`

func translate(t *testing.T, body string) (Translation, map[string]any) {
	t.Helper()
	tr, err := MessagesToResponses([]byte(body), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(tr.Body, &out); err != nil {
		t.Fatal(err)
	}
	return tr, out
}

func TestTheRequestMapsAsMeasured(t *testing.T) {
	tr, out := translate(t, claudeCodeTurn)
	if !tr.Thinking || tr.Omitted || !tr.Stream || tr.Model != "claude-large" {
		t.Errorf("translation = %+v", tr)
	}
	want := map[string]any{
		"model": "claude-large", "instructions": "You are Claude Code.", "stream": true, "store": false,
		"prompt_cache_key": "sess-1", "max_output_tokens": 32000.0, "tool_choice": "required",
		"parallel_tool_calls": false,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %#v, want %#v", k, out[k], v)
		}
	}
	for _, gone := range []string{"temperature", "top_p", "top_k", "stop", "stop_sequences", "metadata", "user",
		"context_management", "system", "messages", "thinking", "output_config"} {
		if _, ok := out[gone]; ok {
			t.Errorf("%s went upstream; the backend refuses it or has no such field", gone)
		}
	}
	if r := out["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", r)
	}
	if f := out["text"].(map[string]any)["format"].(map[string]any); f["type"] != "json_schema" || f["strict"] != false {
		t.Errorf("text.format = %v", f)
	}
	if inc := out["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", inc)
	}

	var names []string
	for _, tool := range out["tools"].([]any) {
		fn := tool.(map[string]any)
		names = append(names, fn["name"].(string))
		if fn["type"] != "function" || fn["strict"] != false || fn["parameters"] == nil {
			t.Errorf("tool = %v", fn)
		}
	}
	if strings.Join(names, ",") != "Read,ToolSearch,WebFetch" {
		t.Errorf("tools offered = %v; a deferred tool is offered once loaded and not before", names)
	}

	items, _ := json.Marshal(out["input"])
	got := string(items)
	for _, want := range []string{
		`{"content":[{"text":"read note.txt","type":"input_text"},{"image_url":"data:image/png;base64,iVBORw0K","type":"input_image"},` +
			`{"file_data":"data:application/pdf;base64,JVBERi0x","filename":"spec.pdf","type":"input_file"}],"role":"user","type":"message"}`,
		`{"content":[{"text":"# Environment","type":"input_text"}],"role":"developer","type":"message"}`,
		`{"encrypted_content":"gAAAAenc","id":"rs_1","summary":[{"text":"**Reading**","type":"summary_text"}],"type":"reasoning"}`,
		`{"content":[{"text":"Reading it.","type":"output_text"}],"role":"assistant","type":"message"}`,
		`{"arguments":"{\"file_path\":\"note.txt\"}","call_id":"call_1","name":"Read","type":"function_call"}`,
		`{"call_id":"call_1","output":[{"text":"hello","type":"input_text"},{"image_url":"https://example.com/a.png","type":"input_image"}],"type":"function_call_output"}`,
		`{"call_id":"call_2","output":"Tool WebFetch is now available.","type":"function_call_output"}`,
		`{"call_id":"call_3","output":"Error:\n\u003ctool_use_error\u003eno\u003c/tool_use_error\u003e","type":"function_call_output"}`,
		`{"content":[{"text":"go on","type":"input_text"}],"role":"user","type":"message"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("input lacks %s\n in %s", want, got)
		}
	}
	if strings.Contains(got, "from claude") || strings.Contains(got, "EqQB") || strings.Contains(got, "billing") {
		t.Errorf("an Anthropic thinking block or the billing line went to ChatGPT: %s", got)
	}
	// The order the backend needs: reasoning before what it led to.
	if strings.Index(got, `"type":"reasoning"`) > strings.Index(got, `"Reading it."`) {
		t.Errorf("reasoning after its message: %s", got)
	}
}

func TestThinkingAndEffort(t *testing.T) {
	for _, tc := range []struct {
		body           string
		effort, sum    string
		thinking, omit bool
	}{
		{`"thinking":{"type":"enabled","budget_tokens":2000}`, "low", "auto", true, false},
		{`"thinking":{"type":"enabled","budget_tokens":10000}`, "medium", "auto", true, false},
		{`"thinking":{"type":"enabled","budget_tokens":20000}`, "high", "auto", true, false},
		{`"thinking":{"type":"enabled","budget_tokens":64000}`, "xhigh", "auto", true, false},
		{`"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"max"}`, "max", "", true, true},
		{`"thinking":{"type":"disabled"}`, "low", "", false, false},
		{`"output_config":{"effort":"bogus"}`, "", "", false, false},
	} {
		tr, out := translate(t, `{"model":"m","max_tokens":1,`+tc.body+`,"messages":[]}`)
		r, _ := out["reasoning"].(map[string]any)
		effort, _ := r["effort"].(string)
		sum, _ := r["summary"].(string)
		if effort != tc.effort || sum != tc.sum || tr.Thinking != tc.thinking || tr.Omitted != tc.omit {
			t.Errorf("%s: effort %q summary %q thinking %v omitted %v", tc.body, effort, sum, tr.Thinking, tr.Omitted)
		}
		if out["max_output_tokens"] != 16.0 {
			t.Errorf("max_output_tokens = %v; the backend's floor is 16", out["max_output_tokens"])
		}
	}
}

func TestWhatHasNoEquivalentIsRefused(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[],"tools":[{"type":"computer_20250124","name":"computer"}]}`,
		`{"model":"m","messages":[],"tools":[{"type":"bash_20250124","name":"bash"}]}`,
		`{"model":"m","messages":[],"tools":[{"type":"web_search_20250305","name":"web_search","blocked_domains":["a.com"]}]}`,
		`{"model":"m","messages":[],"mcp_servers":[{"type":"url","url":"https://x"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"f"}}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"text/csv","data":"x"}}]}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"container_upload","file_id":"f"}]}]}`,
	} {
		_, err := MessagesToResponses([]byte(body), "")
		var r refusal
		if !errors.As(err, &r) {
			t.Errorf("%s: err = %v, want a refusal", body, err)
		}
	}
}

func TestLongToolNamesAreShortenedAndRestored(t *testing.T) {
	long := "mcp__a_very_long_server_name_for_testing__and_an_even_longer_tool_name_here"
	tr, out := translate(t, `{"model":"m","max_tokens":100,"stream":true,"tools":[{"name":"`+long+`","input_schema":{}}],`+
		`"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"`+long+`","input":{}}]}]}`)
	up := out["tools"].([]any)[0].(map[string]any)["name"].(string)
	if len(up) > maxToolName || tr.Tools[up] != long {
		t.Fatalf("sent %q, map %v", up, tr.Tools)
	}
	if call := out["input"].([]any)[0].(map[string]any); call["name"] != up {
		t.Errorf("the history names %v, the tools %q", call["name"], up)
	}
	stream := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c2","name":"` + up + `"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c2","name":"` + up + `","arguments":"{}"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	got := run(t, tr, http.StatusOK, stream)
	if !strings.Contains(got, `"name":"`+long+`"`) {
		t.Errorf("the call came back as %s", got)
	}
}

// run feeds a backend answer through the sink, as the relay would, and
// returns what the client was sent.
func run(t *testing.T, tr Translation, status int, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s := newResponsesSink(rec, tr)
	s.WriteHeader(status)
	// In pieces, as a network delivers them.
	for len(body) > 0 {
		n := min(37, len(body))
		_, _ = s.Write([]byte(body[:n]))
		body = body[n:]
	}
	s.Close(nil)
	return rec.Body.String()
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// events reads an Anthropic stream into its event names and payloads.
func events(t *testing.T, stream string) ([]string, []map[string]any) {
	t.Helper()
	var names []string
	var data []map[string]any
	for _, frame := range strings.Split(strings.TrimSpace(stream), "\n\n") {
		lines := strings.SplitN(frame, "\n", 2)
		if len(lines) != 2 {
			t.Fatalf("frame %q", frame)
		}
		names = append(names, strings.TrimPrefix(lines[0], "event: "))
		var d map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &d); err != nil {
			t.Fatalf("frame %q: %v", frame, err)
		}
		data = append(data, d)
	}
	return names, data
}

func TestReasoningComesBackAsThinkingAndGoesUpAgain(t *testing.T) {
	tr := Translation{Model: "claude-large", Stream: true, Thinking: true}
	got := run(t, tr, http.StatusOK, fixture(t, "chatgpt-reasoning.sse"))
	names, data := events(t, got)
	want := "message_start ping content_block_start content_block_delta content_block_delta content_block_delta " +
		"content_block_delta content_block_stop content_block_start content_block_delta content_block_stop " +
		"message_delta message_stop"
	if strings.Join(names, " ") != want {
		t.Fatalf("events:\n got %s\nwant %s", strings.Join(names, " "), want)
	}
	if m := data[0]["message"].(map[string]any); m["model"] != "claude-large" || !strings.HasPrefix(m["id"].(string), "msg_") {
		t.Errorf("message_start = %v", m)
	}
	var thinking, signature string
	for _, d := range data {
		if delta, ok := d["delta"].(map[string]any); ok {
			switch delta["type"] {
			case "thinking_delta":
				thinking += delta["thinking"].(string)
			case "signature_delta":
				signature = delta["signature"].(string)
			}
		}
	}
	if thinking != "**Counting odd multiples**\n\n**Calculating the XOR count**" {
		t.Errorf("thinking = %q", thinking)
	}
	if !strings.HasPrefix(signature, signaturePrefix+"rs_0e817e24") {
		t.Fatalf("signature = %q", signature)
	}
	delta := data[len(data)-2]
	if delta["delta"].(map[string]any)["stop_reason"] != "end_turn" {
		t.Errorf("message_delta = %v", delta)
	}
	if u := delta["usage"].(map[string]any); u["input_tokens"] != 47.0 || u["output_tokens"] != 213.0 || u["cache_read_input_tokens"] != 0.0 {
		t.Errorf("usage = %v", u)
	}

	// The client sends the block back with its next turn, and it goes up as
	// the reasoning item it was.
	echo, _ := json.Marshal(map[string]any{"model": "m", "max_tokens": 100, "messages": []any{
		map[string]any{"role": "user", "content": "q"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": thinking, "signature": signature},
			map[string]any{"type": "text", "text": "104"}}},
		map[string]any{"role": "user", "content": "double it"}}})
	_, out := translate(t, string(echo))
	item := out["input"].([]any)[1].(map[string]any)
	enc := fixture(t, "chatgpt-reasoning.sse")
	if item["type"] != "reasoning" || item["id"] != "rs_0e817e24f91cb00c016ad75a8dbc648191beaf79ba127f4707" ||
		!strings.Contains(enc, `"encrypted_content":"`+item["encrypted_content"].(string)+`"`) {
		t.Errorf("replayed as %v", item)
	}
}

func TestReasoningIsNotSentToACallerThatDidNotAskForThinking(t *testing.T) {
	got := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, fixture(t, "chatgpt-reasoning.sse"))
	if strings.Contains(got, "thinking") {
		t.Errorf("thinking sent unasked: %s", got)
	}
	omitted := run(t, Translation{Model: "m", Stream: true, Thinking: true, Omitted: true}, http.StatusOK, fixture(t, "chatgpt-reasoning.sse"))
	if strings.Contains(omitted, "thinking_delta") || !strings.Contains(omitted, "signature_delta") {
		t.Errorf("display omitted: %s", omitted)
	}
}

func TestAToolCallStreamsAsToolUse(t *testing.T) {
	got := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, fixture(t, "chatgpt-tool-call.sse"))
	for _, want := range []string{
		`"content_block":{"id":"call_5dd1ef55666c4a5e918589e004090e8d","input":{},"name":"get_weather","type":"tool_use"}`,
		`"delta":{"partial_json":"{\"","type":"input_json_delta"}`,
		`"stop_reason":"tool_use"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %s:\n%s", want, got)
		}
	}
}

func TestANonStreamingCallerIsAnsweredWhole(t *testing.T) {
	got := run(t, Translation{Model: "m"}, http.StatusOK, fixture(t, "chatgpt-tool-call.sse"))
	var msg struct {
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal([]byte(got), &msg); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if msg.Type != "message" || msg.Model != "m" || msg.StopReason != "tool_use" || len(msg.Content) != 1 ||
		msg.Content[0].Name != "get_weather" || string(msg.Content[0].Input) != `{"location":"Paris"}` || msg.Usage["output_tokens"] == 0 {
		t.Errorf("message = %s", got)
	}

	text := run(t, Translation{Model: "m"}, http.StatusOK, fixture(t, "chatgpt-tool-result.sse"))
	if !strings.Contains(text, `"text":"Paris is sunny and 18°C."`) || !strings.Contains(text, `"stop_reason":"end_turn"`) {
		t.Errorf("message = %s", text)
	}
}

func TestRunningOutOfTokensIsMaxTokens(t *testing.T) {
	got := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, fixture(t, "chatgpt-max-output.sse"))
	if !strings.Contains(got, `"stop_reason":"max_tokens"`) || !strings.Contains(got, `"text":"1"`) {
		t.Errorf("%s", got)
	}
}

func TestAWebSearchIsSaidAsAnthropicSaysOne(t *testing.T) {
	got := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, fixture(t, "chatgpt-web-search.sse"))
	for _, want := range []string{
		`"name":"web_search","type":"server_tool_use"`,
		`"partial_json":"{\"query\":\"site:go.dev/dl latest stable Go release\"}"`,
		`"type":"web_search_tool_result"`, `"url":"https://go.dev/dl/"`,
		`"server_tool_use":{"web_search_requests":1}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %s", want)
		}
	}
	whole := run(t, Translation{Model: "m"}, http.StatusOK, fixture(t, "chatgpt-web-search.sse"))
	if !strings.Contains(whole, "The latest stable Go release is **Go 1.27.3**.") {
		t.Errorf("the answer's text: %s", whole)
	}
	_, out := translate(t, `{"model":"m","max_tokens":100,"messages":[],"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["go.dev"],"user_location":{"type":"approximate","city":"Paris"}}]}`)
	ws, _ := json.Marshal(out["tools"])
	if string(ws) != `[{"filters":{"allowed_domains":["go.dev"]},"type":"web_search","user_location":{"city":"Paris","type":"approximate"}}]` {
		t.Errorf("tools = %s", ws)
	}
	if inc, _ := json.Marshal(out["include"]); !strings.Contains(string(inc), "web_search_call.action.sources") {
		t.Errorf("include = %s", inc)
	}
}

func TestRefusalsAreAnthropicErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		kind, msg string
	}{
		{400, `{"detail":"Unsupported parameter: temperature"}`, "invalid_request_error", "Unsupported parameter: temperature"},
		{400, `{"error":{"message":"Invalid 'max_output_tokens'","type":"invalid_request_error","param":"max_output_tokens","code":"integer_below_min_value"}}`,
			"invalid_request_error", "Invalid 'max_output_tokens'"},
		{429, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":1792446828}}`,
			"rate_limit_error", "The usage limit has been reached"},
		{503, ``, "overloaded_error", "the ChatGPT backend refused the request without saying why"},
		{500, `oops`, "api_error", "oops"},
	} {
		rec := httptest.NewRecorder()
		s := newResponsesSink(rec, Translation{Model: "m", Stream: true})
		s.Header().Set("Retry-After", "30")
		s.Header().Set("X-Codex-Primary-Used-Percent", "100")
		s.WriteHeader(tc.status)
		_, _ = s.Write([]byte(tc.body))
		s.Close(nil)
		var env struct {
			Type  string `json:"type"`
			Error struct{ Type, Message string }
		}
		if rec.Code != tc.status || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Type != "error" ||
			env.Error.Type != tc.kind || env.Error.Message != tc.msg {
			t.Errorf("%d %s: answered %d %s", tc.status, tc.body, rec.Code, rec.Body)
		}
		if rec.Header().Get("Retry-After") != "30" || rec.Header().Get("X-Codex-Primary-Used-Percent") != "" {
			t.Errorf("headers = %v", rec.Header())
		}
	}
}

func TestAFailureMidStreamIsAnErrorEvent(t *testing.T) {
	failed := `data: {"type":"response.created","response":{"id":"resp_9"}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"par"}` + "\n\n" +
		`data: {"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"busy"}}}` + "\n\n"
	got := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, failed)
	names, data := events(t, got)
	if names[len(names)-1] != "error" || names[len(names)-2] != "content_block_stop" {
		t.Fatalf("events = %v", names)
	}
	if e := data[len(data)-1]["error"].(map[string]any); e["type"] != "overloaded_error" || e["message"] != "busy" {
		t.Errorf("error = %v", e)
	}

	rec := httptest.NewRecorder()
	s := newResponsesSink(rec, Translation{Model: "m"})
	_, _ = s.Write([]byte(failed))
	s.Close(nil)
	if rec.Code != 529 || !strings.Contains(rec.Body.String(), `"overloaded_error"`) {
		t.Errorf("non-streaming: %d %s", rec.Code, rec.Body)
	}

	cut := run(t, Translation{Model: "m", Stream: true}, http.StatusOK, strings.Join(strings.Split(failed, "\n\n")[:2], "\n\n")+"\n\n")
	if names, _ := events(t, cut); names[len(names)-1] != "error" {
		t.Errorf("a stream cut short ended %v", names)
	}
}

func TestASilentStreamIsPinged(t *testing.T) {
	defer func(d time.Duration) { pingEvery = d }(pingEvery)
	pingEvery = 20 * time.Millisecond
	rec := httptest.NewRecorder()
	s := newResponsesSink(rec, Translation{Model: "m", Stream: true})
	s.WriteHeader(http.StatusOK)
	_, _ = s.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n"))
	time.Sleep(120 * time.Millisecond)
	s.Close(errors.New("gone"))
	if n := strings.Count(rec.Body.String(), "event: ping"); n < 2 {
		t.Errorf("%d pings in %s", n, rec.Body)
	}
}

func TestSignaturesAreToldApart(t *testing.T) {
	for _, sig := range []string{"", "EqQBCkgIARABGAIiQK+/=", "claudication:openai-reasoning:v1:", "claudication:openai-reasoning:v1:rs_1"} {
		if _, ok := decodeSignature(sig); ok {
			t.Errorf("%q read as ours", sig)
		}
	}
	if r, ok := decodeSignature(encodeSignature("rs_1", "gAAAA_-x")); !ok || r.ID != "rs_1" || r.Encrypted != "gAAAA_-x" {
		t.Errorf("round trip = %+v %v", r, ok)
	}
}

func TestCountTokensIsAnEstimateThatSaysSo(t *testing.T) {
	ex, err := API{}.Translate([]byte(`{"model":"m","system":"abcdefgh","messages":[{"role":"user","content":[`+
		`{"type":"text","text":"abcdefghijkl"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+
		strings.Repeat("A", 40000)+`"}}]}]}`), api.Target{Dialect: "openai", Route: "/v1/messages/count_tokens"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := ex.(api.Answerer)
	if !ok {
		t.Fatalf("%T answers nothing itself", ex)
	}
	rec := httptest.NewRecorder()
	if a.Answer(rec) != http.StatusOK || rec.Header().Get(EstimateHeader) == "" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	// 8 + 12 bytes of text, plus the few of the parts' types, and one picture.
	if got.InputTokens < tokensPerImage+5 || got.InputTokens > tokensPerImage+20 {
		t.Errorf("estimate = %d", got.InputTokens)
	}
}

func TestOnlyChatGPTIsATranslationTarget(t *testing.T) {
	if _, err := (API{}).Translate([]byte(`{"model":"m","messages":[]}`), api.Target{Dialect: "anthropic"}); err == nil {
		t.Error("translated Messages for a Messages upstream")
	}
	ex, err := API{}.Translate([]byte(`{"model":"m","messages":[]}`), api.Target{Dialect: "openai", Route: "/v1/messages"})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"Anthropic-Beta": {"x"}, "X-Stainless-Os": {"Linux"}, "X-App": {"cli"}, "User-Agent": {"claude-cli/2"}}
	ex.Headers(h)
	if len(h) != 3 || h.Get("User-Agent") != "claude-cli/2" || h.Get("Accept") != "text/event-stream" {
		t.Errorf("headers = %v", h)
	}
}

// The second request of a real Claude Code tool turn against a GPT slot
// (2.1.304, captured through the gateway 2026-10-20, device id scrubbed).
func TestClaudeCodesOwnTurnTranslates(t *testing.T) {
	tr, out := translate(t, fixture(t, "claude-code-tool-turn.json"))
	if !tr.Thinking || !tr.Omitted || !tr.Stream {
		t.Errorf("translation = %+v; Claude Code asks for adaptive thinking, display omitted", tr)
	}
	if r := out["reasoning"].(map[string]any); r["effort"] != "high" || r["summary"] != nil {
		t.Errorf("reasoning = %v", r)
	}
	if in := out["instructions"].(string); strings.Contains(in, billingHeader) || !strings.HasPrefix(in, "You are a Claude agent") {
		t.Errorf("instructions start %.80q", in)
	}
	for _, tool := range out["tools"].([]any) {
		if tool.(map[string]any)["name"] == "DeferredToolPlaceholder" {
			t.Error("a deferred tool no search loaded was offered")
		}
	}
	var roles, calls []string
	for _, it := range out["input"].([]any) {
		m := it.(map[string]any)
		switch m["type"] {
		case "message":
			roles = append(roles, m["role"].(string))
		case "function_call", "function_call_output":
			calls = append(calls, m["type"].(string)+":"+m["call_id"].(string))
		}
	}
	if strings.Join(roles, ",") != "user,developer" || len(calls) != 2 ||
		strings.TrimPrefix(calls[0], "function_call:") != strings.TrimPrefix(calls[1], "function_call_output:") {
		t.Errorf("roles %v, calls %v", roles, calls)
	}
}

// A crush turn sending back the thinking block a GPT answer gave it, after a
// tool call (crush v0.97.2, captured through the gateway 2026-10-20).
func TestCrushSendsTheReasoningBack(t *testing.T) {
	_, out := translate(t, fixture(t, "crush-tool-turn.json"))
	items := out["input"].([]any)
	reasoning := items[1].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_080d436828ec16d2016ad75f60f7dc8191a281645d195684d5" ||
		!strings.HasPrefix(reasoning["encrypted_content"].(string), "gAAAAAB") {
		t.Errorf("item 1 = %v", reasoning)
	}
	if items[2].(map[string]any)["type"] != "function_call" || items[3].(map[string]any)["type"] != "function_call_output" {
		t.Errorf("items = %v", items)
	}
	if out["tool_choice"] != "auto" || out["parallel_tool_calls"] != true || len(out["tools"].([]any)) != 26 {
		t.Errorf("tool_choice %v parallel %v", out["tool_choice"], out["parallel_tool_calls"])
	}
	if r := out["reasoning"].(map[string]any); r["effort"] != "xhigh" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", r)
	}
}
