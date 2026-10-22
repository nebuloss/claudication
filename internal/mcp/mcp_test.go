package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer() *Server {
	return &Server{
		Name: "t", Version: "1", Instructions: "use the tools",
		Tools: []Tool{
			{Name: "echo", Description: "echoes", ReadOnly: true,
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Call: func(ctx context.Context, args json.RawMessage) (any, error) {
					var in map[string]any
					_ = json.Unmarshal(args, &in)
					in["meta"] = string(Meta(ctx))
					return in, nil
				}},
			{Name: "no", InputSchema: json.RawMessage(`{"type":"object"}`),
				Call: func(context.Context, json.RawMessage) (any, error) { return nil, Refuse("not allowed") }},
			{Name: "bad", InputSchema: json.RawMessage(`{"type":"object"}`),
				Call: func(context.Context, json.RawMessage) (any, error) { return nil, &ParamsError{Message: "wrong"} }},
			{Name: "broken", InputSchema: json.RawMessage(`{"type":"object"}`),
				Call: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("disk on fire") }},
		},
	}
}

func post(t *testing.T, h http.Handler, body string, header ...string) (int, map[string]any, http.Header) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	var out map[string]any
	if len(b) > 0 && b[0] == '{' {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
	}
	return w.Code, out, w.Header()
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	s := testServer()
	for asked, want := range map[string]string{
		"2025-06-18": "2025-06-18", "2025-11-25": "2025-11-25", "2025-03-26": "2025-03-26",
		"2024-11-05": Versions[0], "2099-01-01": Versions[0],
	} {
		status, out, h := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+asked+`","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)
		res, _ := out["result"].(map[string]any)
		if status != 200 || res["protocolVersion"] != want {
			t.Errorf("asked %s: %d %v", asked, status, out)
		}
		if h.Get("Mcp-Session-Id") != "" {
			t.Error("a session id was issued")
		}
		if h.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", h.Get("Content-Type"))
		}
		caps, _ := res["capabilities"].(map[string]any)
		if _, ok := caps["tools"]; !ok || res["instructions"] != "use the tools" {
			t.Errorf("initialize result: %v", res)
		}
	}
}

func TestTransport(t *testing.T) {
	s := testServer()
	// A notification and a response are accepted with nothing to say.
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"result":{}}`,
	} {
		if status, _, _ := post(t, s, body); status != http.StatusAccepted {
			t.Errorf("%s: %d", body, status)
		}
	}
	// No stream to open and no session to end.
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(m, "/mcp", nil))
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
			t.Errorf("%s: %d", m, w.Code)
		}
	}
	if status, out, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); status != 200 || out["result"] == nil {
		t.Errorf("ping: %d %v", status, out)
	}
	// Claude Code and crush probe with a method this server lacks, under a
	// version it does not speak, and fall back on the JSON-RPC error.
	status, out, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`, "Mcp-Protocol-Version", "2026-07-28")
	if e, _ := out["error"].(map[string]any); status != 200 || e["code"] != float64(CodeMethodNotFound) {
		t.Errorf("discover: %d %v", status, out)
	}
	// A negotiated method under a version it does not speak is 400.
	if status, _, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Mcp-Protocol-Version", "2026-07-28"); status != http.StatusBadRequest {
		t.Errorf("tools/list under an unknown version: %d", status)
	}
	if status, _, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Mcp-Protocol-Version", "2025-06-18"); status != 200 {
		t.Errorf("tools/list under a known version: %d", status)
	}
	if status, out, _ := post(t, s, `not json`); status != http.StatusBadRequest || out["error"] == nil {
		t.Errorf("parse error: %d %v", status, out)
	}
	// A browser page on another origin is refused.
	if status, _, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Origin", "https://evil.example"); status != http.StatusForbidden {
		t.Errorf("cross-origin: %d", status)
	}
	// A 2025-03-26 batch.
	r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"nope"}]`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var batch []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil || len(batch) != 2 {
		t.Errorf("batch: %s", w.Body.String())
	}
}

func TestTools(t *testing.T) {
	s := testServer()
	_, out, _ := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools, _ := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools/list: %v", out)
	}
	first := tools[0].(map[string]any)
	if first["name"] != "echo" || first["inputSchema"] == nil || first["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Errorf("a tool: %v", first)
	}

	call := func(name, args string) map[string]any {
		t.Helper()
		_, out, _ := post(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`,"_meta":{"sessionId":"s-1"}}}`)
		return out
	}
	res := call("echo", `{"a":"<b>"}`)["result"].(map[string]any)
	sc, _ := res["structuredContent"].(map[string]any)
	if sc["a"] != "<b>" || !strings.Contains(sc["meta"].(string), "s-1") || res["isError"] != nil {
		t.Errorf("echo: %v", res)
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"a": "<b>"`) {
		t.Errorf("echo as text: %q", text)
	}
	if res := call("no", `{}`)["result"].(map[string]any); res["isError"] != true ||
		res["content"].([]any)[0].(map[string]any)["text"] != "not allowed" {
		t.Errorf("a refusal: %v", res)
	}
	if e, _ := call("bad", `{}`)["error"].(map[string]any); e["code"] != float64(CodeInvalidParams) {
		t.Errorf("bad arguments: %v", e)
	}
	if e, _ := call("missing", `{}`)["error"].(map[string]any); e["code"] != float64(CodeInvalidParams) {
		t.Errorf("an unknown tool: %v", e)
	}
	if e, _ := call("echo", `[1]`)["error"].(map[string]any); e["code"] != float64(CodeInvalidParams) {
		t.Errorf("arguments not an object: %v", e)
	}
	res = call("broken", `{}`)["result"].(map[string]any)
	if text := res["content"].([]any)[0].(map[string]any)["text"].(string); res["isError"] != true || strings.Contains(text, "disk") {
		t.Errorf("the server's own failure leaks or is not an error: %v", res)
	}
}
