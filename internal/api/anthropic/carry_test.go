package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudication/internal/api"
)

// A whole JSON answer to a streaming caller is written by Close, which used
// to start a pinger after the one place that stops it had run: a goroutine
// left behind per answer.
func TestAWholeAnswerStartsNoPinger(t *testing.T) {
	for _, body := range []string{
		`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`,
		``,
	} {
		rec := httptest.NewRecorder()
		s := newResponsesSink(rec, Translation{Model: "m", Stream: true})
		s.WriteHeader(http.StatusOK)
		if body != "" {
			_, _ = s.Write([]byte(body))
		}
		s.Close(nil)
		if s.pinging != nil {
			t.Errorf("%q: a pinger was started after Close", body)
		}
		if body != "" && !strings.Contains(rec.Body.String(), `"text":"hi"`) {
			t.Errorf("%q: answered %s", body, rec.Body)
		}
	}
}

// An encrypted reasoning item sent back is not text the model reads, and
// counting its ciphertext made a count several times what it was.
func TestCountTokensReadsOnlyWhatTheModelReads(t *testing.T) {
	sig := encodeSignature("rs_1", strings.Repeat("gAAAAB", 20000))
	body := `{"model":"m","messages":[{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + sig + `"},` +
		`{"type":"tool_use","id":"call_abcdefghijklmnopqrstuvwxyz","name":"Read","input":{"path":"a"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_abcdefghijklmnopqrstuvwxyz","content":"ok"}]}],` +
		`"tools":[{"type":"computer_20250124","name":"computer"},{"name":"Read","input_schema":{"type":"object"}}],` +
		`"mcp_servers":[{"type":"url","url":"https://x"}]}`
	ex, err := New().Translate([]byte(body), api.Target{Dialect: "openai", Route: countTokensRoute})
	if err != nil {
		t.Fatalf("a count refused over what it cannot send: %v", err)
	}
	c := ex.(countExchange)
	// hello, the call's arguments and name, "ok", the tool's name and schema.
	if c.tr.Estimate < 5 || c.tr.Estimate > 20 {
		t.Errorf("estimate = %d", c.tr.Estimate)
	}
	if c.tr.Body != nil {
		t.Error("a count built a body to send")
	}
	// And a request that is sent carries no estimate, which only a count needs.
	tr, err := MessagesToResponses([]byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`), "")
	if err != nil || tr.Estimate != 0 {
		t.Errorf("estimate %d on a request sent, %v", tr.Estimate, err)
	}
}

// A caller that did not ask for thinking is sent no thinking blocks, so it
// cannot send back the reasoning that led to its last call. The gateway puts
// it back in front of that call on the next turn.
func TestReasoningComesBackForACallerThatDoesNotCarryIt(t *testing.T) {
	a := New()
	turn := `data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_9","summary":[{"type":"summary_text","text":"look first"}],"encrypted_content":"gAAAAenc9"}}` + "\n\n" +
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_7","name":"Read"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_7","name":"Read","arguments":"{}"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":2,"item":{"type":"reasoning","id":"rs_10","summary":[],"encrypted_content":"gAAAAenc10"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","output_index":3,"item":{"type":"message","content":[{"type":"output_text","text":"done."}]}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	ex, err := a.Translate([]byte(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"go"}]}`),
		api.Target{Dialect: "openai", Route: "/v1/messages", Conversation: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	got := run(t, ex.(*responsesExchange).tr, http.StatusOK, turn)
	if strings.Contains(got, "thinking") {
		t.Fatalf("thinking sent unasked: %s", got)
	}

	next := `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"call_7","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_7","content":"x"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"done."}]},` +
		`{"role":"user","content":"again"}]}`
	ex, err = a.Translate([]byte(next), api.Target{Dialect: "openai", Route: "/v1/messages", Conversation: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(ex.Request(), &out); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, it := range out.Input {
		k, _ := it["type"].(string)
		if id, ok := it["id"].(string); ok {
			k += ":" + id
		}
		kinds = append(kinds, k)
	}
	want := "message reasoning:rs_9 function_call function_call_output reasoning:rs_10 message message"
	if strings.Join(kinds, " ") != want {
		t.Errorf("input = %v\nwant %s", kinds, want)
	}

	// Another conversation does not get them, and a caller that sends its
	// own thinking block is not given a second copy.
	ex, _ = a.Translate([]byte(next), api.Target{Dialect: "openai", Route: "/v1/messages", Conversation: "chat-2"})
	if strings.Contains(string(ex.Request()), "gAAAAenc") {
		t.Error("reasoning crossed conversations")
	}
	own := strings.Replace(next, `[{"type":"tool_use"`,
		`[{"type":"thinking","thinking":"","signature":"`+encodeSignature("rs_9", "gAAAAenc9")+`"},{"type":"tool_use"`, 1)
	ex, _ = a.Translate([]byte(own), api.Target{Dialect: "openai", Route: "/v1/messages", Conversation: "chat-1"})
	if n := strings.Count(string(ex.Request()), "gAAAAenc9"); n != 1 {
		t.Errorf("rs_9 sent %d times", n)
	}
}

func TestTheReasoningStoreIsBounded(t *testing.T) {
	s := newReasoningStore(200)
	for i := range 10 {
		s.put("c", callKey(string(rune('a'+i))), []carriedReasoning{{ID: "rs", Encrypted: strings.Repeat("x", 50)}})
	}
	if s.bytes > 200 || s.get("c", callKey("a")) != nil || s.get("c", callKey("j")) == nil {
		t.Errorf("bytes %d; oldest kept %v; newest kept %v", s.bytes, s.get("c", callKey("a")) != nil, s.get("c", callKey("j")) != nil)
	}
	var none *reasoningStore
	none.put("c", "k", []carriedReasoning{{ID: "x"}})
	if none.get("c", "k") != nil {
		t.Error("a nil store kept something")
	}
}
