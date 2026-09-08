package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolNames pulls every "name" the body declares, wherever it declares it, so
// a test can assert on the shape without depending on key order.
func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var envelope struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice struct {
			Name string `json:"name"`
		} `json:"tool_choice"`
		Messages []struct {
			// Raw, because the Messages API accepts either a bare string or a
			// list of blocks and both shapes appear in these fixtures.
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("rewritten body does not parse: %v", err)
	}
	var names []string
	for _, tool := range envelope.Tools {
		names = append(names, tool.Name)
	}
	if envelope.ToolChoice.Name != "" {
		names = append(names, envelope.ToolChoice.Name)
	}
	for _, m := range envelope.Messages {
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue // a bare string carries no tool call
		}
		for _, block := range blocks {
			if block.Type == "tool_use" {
				names = append(names, block.Name)
			}
		}
	}
	return names
}

func TestRewriteRefusedToolNamesDoublesTheUnderscore(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5",
		"tools": [
			{"name": "mcp_weather_get", "description": "d"},
			{"name": "mcp__already__fine", "description": "d"},
			{"name": "Bash", "description": "d"}
		],
		"tool_choice": {"type": "tool", "name": "mcp_weather_get"},
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "one moment"},
				{"type": "tool_use", "id": "toolu_1", "name": "mcp_weather_get", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"}
			]}
		]
	}`)

	out, names := RewriteRefusedToolNames(body)

	want := []string{"mcp__weather_get", "mcp__already__fine", "Bash", "mcp__weather_get", "mcp__weather_get"}
	got := toolNames(t, out)
	if len(got) != len(want) {
		t.Fatalf("names: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("name %d: got %q, want %q", i, got[i], want[i])
		}
	}

	// Every place the name appears must move together, or the upstream sees a
	// tool_use for a tool that was never declared.
	if len(names) != 1 || names["mcp__weather_get"] != "mcp_weather_get" {
		t.Errorf("reverse map: got %v, want one entry mcp__weather_get -> mcp_weather_get", names)
	}
}

func TestRewriteRefusedToolNamesLeavesEverythingElseAlone(t *testing.T) {
	// Each of these is a name the upstream accepts as it stands: rewriting any
	// of them would break a request that works today.
	for _, name := range []string{
		"mcp__weather__get", // the client's own convention
		"mcp_",              // prefix and nothing else
		"mcp___weather",     // three underscores
		"MCP_weather_get",   // the check is lowercase
		"mcpweather_get",    // no underscore after mcp
		"weather_get",
	} {
		body := []byte(`{"tools":[{"name":"` + name + `"}]}`)
		out, names := RewriteRefusedToolNames(body)
		if names != nil {
			t.Errorf("%q: reported a rewrite %v", name, names)
		}
		if string(out) != string(body) {
			t.Errorf("%q: body changed to %s", name, out)
		}
	}
}

func TestRewriteRefusedToolNamesReturnsTheSameBytesWhenItChangesNothing(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	out, names := RewriteRefusedToolNames(body)
	if names != nil {
		t.Errorf("reported a rewrite: %v", names)
	}
	if &out[0] != &body[0] {
		t.Error("body was copied when nothing needed rewriting")
	}
}

func TestRewriteRefusedToolNamesWillNotMergeTwoTools(t *testing.T) {
	// Doubling the underscore here would land on a tool that already exists.
	// Renaming it anyway would send two different tools under one name and
	// then rename the wrong one on the way back.
	body := []byte(`{"tools":[
		{"name":"mcp_weather_get"},
		{"name":"mcp__weather_get"}
	]}`)

	out, names := RewriteRefusedToolNames(body)
	if len(names) != 0 {
		t.Errorf("rewrote into a collision: %v", names)
	}
	got := toolNames(t, out)
	if got[0] != "mcp_weather_get" || got[1] != "mcp__weather_get" {
		t.Errorf("names changed: %v", got)
	}
}

func TestRewriteRefusedToolNamesLeavesAnUnparseableBody(t *testing.T) {
	body := []byte(`{"tools":[{"name":"mcp_x"`) // truncated
	out, names := RewriteRefusedToolNames(body)
	if names != nil || string(out) != string(body) {
		t.Errorf("touched a body it could not parse: %s %v", out, names)
	}
}

// restoreAll drives the restorer the way relay does, in chunks.
func restoreAll(rev map[string]string, chunks ...string) string {
	n := &nameRestorer{rev: rev}
	var out strings.Builder
	for _, c := range chunks {
		out.Write(n.translate([]byte(c)))
	}
	out.Write(n.tail())
	return out.String()
}

func TestNameRestorerPutsTheClientsNameBack(t *testing.T) {
	rev := map[string]string{"mcp__weather_get": "mcp_weather_get"}

	stream := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"mcp__weather_get","input":{}}}` + "\n\n"

	got := restoreAll(rev, stream)
	if !strings.Contains(got, `"name":"mcp_weather_get"`) {
		t.Errorf("name was not restored: %s", got)
	}
	if strings.Contains(got, `"name":"mcp__weather_get"`) {
		t.Errorf("upstream name still present: %s", got)
	}
}

func TestNameRestorerHandlesANameSplitAcrossChunks(t *testing.T) {
	// The one that matters: TCP does not respect JSON. A name arriving in two
	// reads must still be rewritten, and must not be rewritten in halves.
	rev := map[string]string{"mcp__weather_get": "mcp_weather_get"}
	full := `data: {"type":"tool_use","name":"mcp__weather_get"}` + "\n\n"

	for cut := 1; cut < len(full); cut++ {
		got := restoreAll(rev, full[:cut], full[cut:])
		want := `data: {"type":"tool_use","name":"mcp_weather_get"}` + "\n\n"
		if got != want {
			t.Fatalf("split at %d: got %q, want %q", cut, got, want)
		}
	}
}

func TestNameRestorerEmitsEachLineAsItCompletes(t *testing.T) {
	// A ping must not be held back waiting for the next event: Claude Code
	// aborts a stream that goes quiet, and during a long generation the pings
	// are the only traffic.
	n := &nameRestorer{rev: map[string]string{"mcp__x": "mcp_x"}}

	out := n.translate([]byte("event: ping\ndata: {}\n\n"))
	if string(out) != "event: ping\ndata: {}\n\n" {
		t.Errorf("ping was not passed straight through: %q", out)
	}

	// A partial line is held, because half a name must never be written.
	if out := n.translate([]byte(`data: {"name":"mcp_`)); len(out) != 0 {
		t.Errorf("emitted a partial line: %q", out)
	}
}

func TestNameRestorerLeavesAnUnrelatedBodyAlone(t *testing.T) {
	rev := map[string]string{"mcp__weather_get": "mcp_weather_get"}
	body := `{"id":"msg_1","content":[{"type":"text","text":"no tools here"}]}`
	if got := restoreAll(rev, body); got != body {
		t.Errorf("body changed: %q", got)
	}
}

func TestNameRestorerRestoresANonStreamingBody(t *testing.T) {
	// No newline anywhere, so the whole body is one line and comes out at the
	// end.
	rev := map[string]string{"mcp__weather_get": "mcp_weather_get"}
	body := `{"content":[{"type":"tool_use","id":"toolu_1","name":"mcp__weather_get","input":{}}]}`

	got := restoreAll(rev, body[:20], body[20:])
	if !strings.Contains(got, `"name":"mcp_weather_get"`) {
		t.Errorf("name was not restored: %s", got)
	}
}

func TestNameRestorerGivesUpRatherThanHoardMemory(t *testing.T) {
	n := &nameRestorer{rev: map[string]string{"mcp__x": "mcp_x"}}
	var emitted int
	// One line, no newline, past the cap.
	for emitted <= maxNameBuffer {
		out := n.translate(make([]byte, 1<<20))
		emitted += len(out)
		if emitted > 0 {
			break
		}
	}
	if emitted == 0 {
		t.Fatal("held an oversized line instead of giving up on it")
	}
	// Once it gives up it stays out of the way for the rest of the body.
	if got := n.translate([]byte(`"name":"mcp__x"`)); string(got) != `"name":"mcp__x"` {
		t.Errorf("kept rewriting after giving up: %q", got)
	}
}

func TestRewriteRefusedToolNamesFixesTheBareTodoWrite(t *testing.T) {
	// Found by scripts/bisect-refusal.py on a captured opencode request, once
	// the system-prompt trigger had been cleared out of the way.
	body := []byte(`{
		"tools": [{"name": "todowrite", "description": "d"}, {"name": "bash", "description": "d"}],
		"messages": [
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "toolu_1", "name": "todowrite", "input": {}}
			]}
		]
	}`)

	out, names := RewriteRefusedToolNames(body)

	got := toolNames(t, out)
	if len(got) != 3 || got[0] != "todowrite_" || got[1] != "bash" || got[2] != "todowrite_" {
		t.Fatalf("names = %v, want the tool and its past call both renamed", got)
	}
	if len(names) != 1 || names["todowrite_"] != "todowrite" {
		t.Errorf("reverse map = %v", names)
	}
}

func TestRewriteRefusedToolNamesLeavesNamesNearTodoWriteAlone(t *testing.T) {
	// Measured: only the exact lowercase string is refused. Rewriting any of
	// these would rename a tool that already works.
	for _, name := range []string{
		"TodoWrite", "todoWrite", "Todowrite", "TODOWRITE",
		"todo_write", "todowrite_", "todowrite1", "_todowrite",
		"todoread", "taskcreate", "notebookedit", "webfetch",
	} {
		body := []byte(`{"tools":[{"name":"` + name + `"}]}`)
		out, names := RewriteRefusedToolNames(body)
		if names != nil {
			t.Errorf("%q: reported a rewrite %v", name, names)
		}
		if string(out) != string(body) {
			t.Errorf("%q: body changed to %s", name, out)
		}
	}
}

func TestRewriteRefusedToolNamesWillNotMergeOntoAnExistingTodoWrite(t *testing.T) {
	body := []byte(`{"tools":[{"name":"todowrite"},{"name":"todowrite_"}]}`)
	out, names := RewriteRefusedToolNames(body)
	if len(names) != 0 {
		t.Errorf("rewrote into a collision: %v", names)
	}
	got := toolNames(t, out)
	if got[0] != "todowrite" || got[1] != "todowrite_" {
		t.Errorf("names changed: %v", got)
	}
}
