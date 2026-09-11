package openai

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// sink collects frames and counts flushes, because "did it flush" is not a
// detail here: Codex fails a turn on an idle gap.
type sink struct {
	bytes.Buffer
	flushes int
}

func (s *sink) Flush() { s.flushes++ }

type frame struct {
	event string
	data  map[string]any
}

func frames(t *testing.T, raw string) []frame {
	t.Helper()
	out := []frame{}
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		if block == "" {
			continue
		}
		f := frame{}
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				body := strings.TrimPrefix(line, "data: ")
				if err := json.Unmarshal([]byte(body), &f.data); err != nil {
					t.Fatalf("frame %q is not valid JSON: %v\n%s", f.event, err, body)
				}
			}
		}
		if f.event == "" {
			t.Fatalf("frame with no event name:\n%s", block)
		}
		out = append(out, f)
	}
	return out
}

func events(fs []frame) []string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.event
	}
	return names
}

func last(fs []frame) frame { return fs[len(fs)-1] }

func run(t *testing.T, req Request, sse string) ([]frame, *sink) {
	t.Helper()
	w := &sink{}
	s := NewStream(w, req)
	_ = s.Run(strings.NewReader(sse))
	fs := frames(t, w.String())
	if len(fs) == 0 {
		t.Fatal("no frames emitted")
	}
	return fs, w
}

// --- request direction ------------------------------------------------------

// The fixture is a real Codex 0.154.0 request. If the mapping breaks on it,
// the CLI breaks, so this is the test that matters most.
func TestFixtureBecomesAValidAnthropicRequest(t *testing.T) {
	body, err := os.ReadFile("testdata/codex-responses-request.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResponsesToAnthropic(body, Options{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}

	var out struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		System    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		ToolChoice map[string]any `json:"tool_choice"`
	}
	if err := json.Unmarshal(got.Body, &out); err != nil {
		t.Fatal(err)
	}

	// The capture was taken with Codex configured for a Claude model, and a
	// caller that already named one keeps it.
	if out.Model != "claude-opus-5" {
		t.Errorf("model = %q, want the caller's own Claude model", out.Model)
	}
	// Codex sends no max_output_tokens; Anthropic rejects a request without
	// max_tokens. Without a default here nothing works at all.
	if out.MaxTokens <= 0 {
		t.Errorf("max_tokens = %d, want a default", out.MaxTokens)
	}
	if !out.Stream {
		t.Error("stream should be carried through: Codex always streams")
	}
	if len(out.System) == 0 || len(out.System[0].Text) < 1000 {
		t.Errorf("system should hold the 17.5KB instructions, got %d blocks", len(out.System))
	}
	if len(out.Messages) == 0 {
		t.Fatal("no messages")
	}
	for i, m := range out.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			t.Errorf("messages[%d].role = %q: Anthropic has only user and assistant", i, m.Role)
		}
		if len(m.Content) == 0 {
			t.Errorf("messages[%d] has no content: Anthropic rejects empty blocks", i)
		}
		if i > 0 && m.Role == out.Messages[i-1].Role {
			t.Errorf("messages[%d] repeats role %q: Anthropic rejects two turns in a row", i, m.Role)
		}
	}
	if len(out.Tools) == 0 {
		t.Fatal("no tools survived")
	}
	for _, tool := range out.Tools {
		if tool.Name == "" || len(tool.InputSchema) == 0 {
			t.Errorf("tool %q has no input_schema", tool.Name)
		}
	}
	if out.ToolChoice["type"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", out.ToolChoice)
	}
}

func TestOnlyANonClaudeModelIsSubstituted(t *testing.T) {
	for _, tc := range []struct {
		asked string
		want  string
	}{
		// gpt-5-codex means nothing upstream; sending it on would just be
		// refused, so the configured model stands in.
		{"gpt-5-codex", "claude-sonnet-5"},
		{"gpt-4.1", "claude-sonnet-5"},
		// An operator who put a real model in Codex's config keeps control of
		// which one runs.
		{"claude-opus-5", "claude-opus-5"},
	} {
		t.Run(tc.asked, func(t *testing.T) {
			body := []byte(`{"model":"` + tc.asked + `","input":[]}`)
			got, err := ResponsesToAnthropic(body, Options{Model: "claude-sonnet-5"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Model != tc.want {
				t.Errorf("model = %q, want %q", got.Model, tc.want)
			}
			var out struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(got.Body, &out); err != nil {
				t.Fatal(err)
			}
			if out.Model != tc.want {
				t.Errorf("body model = %q, want %q", out.Model, tc.want)
			}
		})
	}
}

func TestDeveloperTurnsFoldIntoSystem(t *testing.T) {
	body := []byte(`{
	  "model": "gpt-5",
	  "instructions": "be terse",
	  "input": [
	    {"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"env"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"say hi"}]}
	  ]
	}`)
	got, err := ResponsesToAnthropic(body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 1 || !strings.Contains(out.System[0].Text, "rules") {
		t.Errorf("developer turn should join the system prompt, got %+v", out.System)
	}
	if !strings.Contains(out.System[0].Text, "be terse") {
		t.Error("instructions should lead the system prompt")
	}
	// Two consecutive user items become one turn with two blocks, not two
	// turns, which Anthropic rejects.
	if len(out.Messages) != 1 {
		t.Fatalf("want 1 merged user turn, got %d", len(out.Messages))
	}
	if len(out.Messages[0].Content) != 2 {
		t.Errorf("want both user blocks kept, got %d", len(out.Messages[0].Content))
	}
}

func TestToolHistoryRoundTripsThroughAnthropicShapes(t *testing.T) {
	body := []byte(`{
	  "model": "gpt-5",
	  "input": [
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"list"}]},
	    {"type":"function_call","name":"exec","call_id":"toolu_1","arguments":"{\"cmd\":\"ls\"}"},
	    {"type":"function_call_output","call_id":"toolu_1","output":"a\nb"},
	    {"type":"reasoning","id":"rs_1"}
	  ]
	}`)
	got, err := ResponsesToAnthropic(body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("want user/assistant/user, got %d turns", len(out.Messages))
	}
	use := out.Messages[1].Content[0]
	if use["type"] != "tool_use" || use["id"] != "toolu_1" {
		t.Errorf("second turn should be the tool_use, got %v", use)
	}
	// arguments is a JSON string in Responses and an object in Anthropic.
	input, ok := use["input"].(map[string]any)
	if !ok || input["cmd"] != "ls" {
		t.Errorf("arguments should be parsed into an object, got %v", use["input"])
	}
	res := out.Messages[2].Content[0]
	if res["type"] != "tool_result" || res["tool_use_id"] != "toolu_1" {
		t.Errorf("third turn should be the tool_result, got %v", res)
	}
	if res["content"] != "a\nb" {
		t.Errorf("tool output lost: %v", res["content"])
	}
}

func TestNamespacedToolsFlattenAndAreRemembered(t *testing.T) {
	body := []byte(`{
	  "model": "gpt-5",
	  "input": [],
	  "tools": [
	    {"type":"function","name":"shell","parameters":{"type":"object"}},
	    {"type":"namespace","name":"multi_agent_v1","tools":[
	      {"type":"function","name":"spawn","parameters":{"type":"object"}},
	      {"type":"function","name":"shell","parameters":{"type":"object"}}
	    ]},
	    {"type":"web_search","external_web_access":true}
	  ]
	}`)
	got, err := ResponsesToAnthropic(body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got.Body, &out); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	// web_search has no schema and cannot become a function.
	want := []string{"shell", "spawn", "multi_agent_v1__shell"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want %v", names, want)
	}
	if got.Tools["spawn"].Namespace != "multi_agent_v1" {
		t.Errorf("spawn should remember its namespace: %+v", got.Tools["spawn"])
	}
	// The collision must not merge the two shells, and the qualified one has
	// to remember what Codex called it.
	if o := got.Tools["multi_agent_v1__shell"]; o.Name != "shell" || o.Namespace != "multi_agent_v1" {
		t.Errorf("qualified tool lost its origin: %+v", o)
	}
	if _, ours := got.Tools["shell"]; ours {
		t.Error("the top-level shell needs no undoing and should not be recorded")
	}
}

func TestParallelToolCallsInvertsOnlyWhenFalse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		disable any
	}{
		{"absent", `{"model":"m","input":[],"tools":[{"type":"function","name":"a"}]}`, nil},
		{"true", `{"model":"m","input":[],"parallel_tool_calls":true,"tools":[{"type":"function","name":"a"}]}`, nil},
		{"false", `{"model":"m","input":[],"parallel_tool_calls":false,"tools":[{"type":"function","name":"a"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResponsesToAnthropic([]byte(tc.body), Options{})
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				ToolChoice map[string]any `json:"tool_choice"`
			}
			if err := json.Unmarshal(got.Body, &out); err != nil {
				t.Fatal(err)
			}
			if out.ToolChoice["disable_parallel_tool_use"] != tc.disable {
				t.Errorf("disable_parallel_tool_use = %v, want %v",
					out.ToolChoice["disable_parallel_tool_use"], tc.disable)
			}
		})
	}
}

// --- response direction -----------------------------------------------------

func TestStreamEndsWithCompletedCarryingIDAndUsage(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_01abc","model":"claude-sonnet-5","usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`
	fs, w := run(t, Request{Model: "claude-sonnet-5"}, sse)

	want := []string{
		"response.created",
		"response.output_item.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_item.done",
		"response.completed",
	}
	if strings.Join(events(fs), ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v\nwant %v", events(fs), want)
	}

	// Codex treats a stream that ends any other way as a failure, whatever
	// came before it.
	end := last(fs)
	resp := end.data["response"].(map[string]any)
	if resp["id"] == "" || resp["id"] == nil {
		t.Error("response.completed needs an id or Codex fails the turn")
	}
	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		t.Fatal("no usage on response.completed: the turn would report zero tokens")
	}
	if usage["input_tokens"].(float64) != 15 {
		t.Errorf("input_tokens = %v, want cache reads folded in", usage["input_tokens"])
	}
	if usage["output_tokens"].(float64) != 7 {
		t.Errorf("output_tokens = %v, want the final count from message_delta", usage["output_tokens"])
	}

	// The item Codex actually keeps is the done one; the deltas are display.
	done := fs[4].data["item"].(map[string]any)
	content := done["content"].([]any)
	text := content[0].(map[string]any)["text"]
	if text != "Hi there" {
		t.Errorf("output_item.done text = %q, want the whole message", text)
	}

	if w.flushes < len(fs) {
		t.Errorf("flushed %d times for %d frames: a buffered frame is an idle gap", w.flushes, len(fs))
	}
}

func TestToolCallArrivesWholeWithArgumentsAsAString(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"multi_agent_v1__shell","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`
	req := Request{
		Model: "m",
		Tools: map[string]ToolOrigin{
			"multi_agent_v1__shell": {Namespace: "multi_agent_v1", Name: "shell"},
		},
	}
	fs, _ := run(t, req, sse)

	// The incremental argument events are on Codex's ignore list, so sending
	// them would be noise; the call has to be whole on output_item.done.
	for _, f := range fs {
		if strings.Contains(f.event, "function_call_arguments") {
			t.Errorf("emitted %s, which Codex discards", f.event)
		}
	}

	var item map[string]any
	for _, f := range fs {
		if f.event == "response.output_item.done" {
			item = f.data["item"].(map[string]any)
		}
	}
	if item == nil {
		t.Fatal("no output_item.done: the call would never reach Codex")
	}
	if item["type"] != "function_call" {
		t.Fatalf("item type = %v", item["type"])
	}
	// arguments is a string here and an object in Anthropic.
	args, ok := item["arguments"].(string)
	if !ok {
		t.Fatalf("arguments = %#v, want a JSON string", item["arguments"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments is not valid JSON: %v (%q)", err, args)
	}
	if parsed["cmd"] != "ls" {
		t.Errorf("arguments = %q, want the buffered deltas joined", args)
	}
	// Flattened on the way out, whole on the way back, or Codex cannot route
	// the call to the right sub-tool.
	if item["name"] != "shell" || item["namespace"] != "multi_agent_v1" {
		t.Errorf("namespace not restored: name=%v namespace=%v", item["name"], item["namespace"])
	}
	// call_id comes back on the next turn as function_call_output.call_id and
	// is mapped straight onto tool_use_id, so it must be Anthropic's own id.
	if item["call_id"] != "toolu_9" {
		t.Errorf("call_id = %v, want the Anthropic tool id", item["call_id"])
	}
}

func TestToolCallWithNoArgumentsStillSendsAnObject(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"now","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	for _, f := range fs {
		if f.event != "response.output_item.done" {
			continue
		}
		// "" is not valid JSON; Codex would fail to parse the call.
		if got := f.data["item"].(map[string]any)["arguments"]; got != "{}" {
			t.Errorf("arguments = %#v, want the empty object", got)
		}
	}
}

func TestPingBecomesAFrameCodexIgnoresRatherThanSilence(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: ping
data: {"type":"ping"}

event: message_stop
data: {"type":"message_stop"}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	// An idle gap fails the turn, so the ping has to become bytes; it must
	// also be an event Codex discards rather than acts on.
	if events(fs)[1] != "response.in_progress" {
		t.Errorf("events = %v, want the ping carried through as in_progress", events(fs))
	}
}

func TestUpstreamErrorMidStreamBecomesFailedWithARealError(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	end := last(fs)
	if end.event != "response.failed" {
		t.Fatalf("last event = %q, want response.failed", end.event)
	}
	resp := end.data["response"].(map[string]any)
	// Codex reads response.error and nothing else; an absent or unreadable
	// error object loses whatever the upstream said.
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatal("no error object: Codex would report a generic stream failure")
	}
	if errObj["code"] != "server_is_overloaded" {
		t.Errorf("code = %v, want a code Codex recognises", errObj["code"])
	}
	if errObj["message"] != "Overloaded" {
		t.Errorf("message = %v, want the upstream's own words", errObj["message"])
	}
}

func TestRateLimitMapsToSlowDownNotRateLimitExceeded(t *testing.T) {
	// rate_limit_exceeded is not one of the codes Codex acts on; slow_down is.
	// A pooled gateway hits rate limits more than anything else, so getting
	// this wrong turns the commonest failure into a silent generic retry.
	code, _ := mapError("rate_limit_error", "slow down")
	if code != "slow_down" {
		t.Errorf("code = %q, want slow_down", code)
	}
	code, _ = mapError("invalid_request_error", "prompt is too long: 300000 tokens")
	if code != "context_length_exceeded" {
		t.Errorf("code = %q, want context_length_exceeded", code)
	}
}

func TestStreamCutShortStillTerminates(t *testing.T) {
	// The upstream connection drops after a delta. Without a terminal event
	// Codex says "stream closed before response.completed", which tells the
	// user nothing about what happened.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}
`
	fs, _ := run(t, Request{Model: "m"}, sse)
	if last(fs).event != "response.failed" {
		t.Errorf("events = %v, want a terminal response.failed", events(fs))
	}
}

func TestTruncationIsReportedAsIncomplete(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":99}}

event: message_stop
data: {"type":"message_stop"}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	end := last(fs)
	if end.event != "response.incomplete" {
		t.Fatalf("last event = %q, want response.incomplete", end.event)
	}
	resp := end.data["response"].(map[string]any)
	details, ok := resp["incomplete_details"].(map[string]any)
	if !ok || details["reason"] != "max_output_tokens" {
		t.Errorf("incomplete_details = %v, want max_output_tokens", resp["incomplete_details"])
	}
}

func TestFailBeforeAnyUpstreamByteStillProducesAReadableTurn(t *testing.T) {
	w := &sink{}
	s := NewStream(w, Request{Model: "m"})
	if err := s.Fail("slow_down", "rate limited"); err != nil {
		t.Fatal(err)
	}
	fs := frames(t, w.String())
	if len(fs) != 1 || fs[0].event != "response.failed" {
		t.Fatalf("events = %v", events(fs))
	}
	resp := fs[0].data["response"].(map[string]any)
	if resp["id"] == nil || resp["id"] == "" {
		t.Error("even a failure needs an id")
	}
}

func TestTerminalEventIsSentOnlyOnce(t *testing.T) {
	// A late error after message_stop must not add a second terminal frame:
	// Codex reads the first, and the second belongs to no turn.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: message_stop
data: {"type":"message_stop"}

event: error
data: {"type":"error","error":{"type":"api_error","message":"late"}}

`
	fs, _ := run(t, Request{Model: "m"}, sse)
	terminals := 0
	for _, f := range fs {
		switch f.event {
		case "response.completed", "response.failed", "response.incomplete":
			terminals++
		}
	}
	if terminals != 1 {
		t.Errorf("events = %v, want exactly one terminal frame", events(fs))
	}
}

func TestEveryFrameIsWellFormedSSE(t *testing.T) {
	// A malformed frame fails the whole turn, so shape matters as much as
	// content: one event line, one data line, a blank line between frames.
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"line\nbreak"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}

`
	w := &sink{}
	s := NewStream(w, Request{Model: "m"})
	_ = s.Run(strings.NewReader(sse))

	for _, block := range strings.Split(strings.TrimSuffix(w.String(), "\n\n"), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 {
			t.Fatalf("frame is not two lines (a newline leaked into data?):\n%q", block)
		}
		if !strings.HasPrefix(lines[0], "event: ") || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("frame is malformed:\n%q", block)
		}
	}
	// Sequence numbers must not repeat or Codex cannot order the frames.
	seen := map[float64]bool{}
	for _, f := range frames(t, w.String()) {
		n := f.data["sequence_number"].(float64)
		if seen[n] {
			t.Errorf("sequence_number %v repeats", n)
		}
		seen[n] = true
	}
}

// --- non-streaming ----------------------------------------------------------

func TestCompleteAnswerConverts(t *testing.T) {
	body := []byte(`{
	  "id":"msg_01xyz","model":"claude-sonnet-5","stop_reason":"tool_use",
	  "content":[
	    {"type":"text","text":"running it"},
	    {"type":"tool_use","id":"toolu_2","name":"shell","input":{"cmd":"ls"}}
	  ],
	  "usage":{"input_tokens":4,"cache_read_input_tokens":6,"output_tokens":8}
	}`)
	got, err := AnthropicToResponses(body, Request{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID     string           `json:"id"`
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "completed" || out.ID == "" {
		t.Errorf("status=%q id=%q", out.Status, out.ID)
	}
	if len(out.Output) != 2 {
		t.Fatalf("want both blocks, got %d", len(out.Output))
	}
	if out.Output[1]["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("arguments = %#v, want the object as a string", out.Output[1]["arguments"])
	}
	if out.Usage.TotalTokens != 18 {
		t.Errorf("total_tokens = %d, want input+output with cache folded in", out.Usage.TotalTokens)
	}
}
