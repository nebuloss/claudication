package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/store"
)

// The MCP server at /mcp, over HTTP as a client reaches it: the handshake,
// the tools, the key check /v1 makes, and the two tiers.

// mcpClient speaks to /mcp with one key, as an MCP client does after
// initialize.
type mcpClient struct {
	t      *testing.T
	url    string
	header string
	value  string
}

func (c mcpClient) post(body string, extra ...string) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.url+"/mcp", strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "claude-code/2.1.308 (sdk-cli)")
	if c.header != "" {
		req.Header.Set(c.header, c.value)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req.Header.Set(extra[i], extra[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			c.t.Fatalf("%d %s: %v", resp.StatusCode, b, err)
		}
	}
	return resp.StatusCode, out
}

// tool calls one tool and answers its result: the structured content, and
// the text when it is an error.
func (c mcpClient) tool(name, args string, meta ...string) (map[string]any, string, bool) {
	c.t.Helper()
	m := ""
	if len(meta) > 0 {
		m = `,"_meta":` + meta[0]
	}
	var headers []string
	if len(meta) > 1 {
		headers = meta[1:]
	}
	status, out := c.post(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+m+`}}`,
		append([]string{"Mcp-Protocol-Version", "2025-11-25"}, headers...)...)
	if status != http.StatusOK {
		c.t.Fatalf("%s: %d %v", name, status, out)
	}
	if e, ok := out["error"].(map[string]any); ok {
		return nil, e["message"].(string), true
	}
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	sc, _ := res["structuredContent"].(map[string]any)
	return sc, text, res["isError"] == true
}

func mcpServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	srv, st, _, _, _, _ := twoUpstreams(t)
	ts := httptest.NewServer(srv.routes(role{gateway: true}))
	t.Cleanup(ts.Close)
	return srv, st, ts.URL
}

// initialize, initialized, tools/list and a tools/call, with the client's
// key as Authorization: Bearer — and as x-api-key, as /v1 takes it.
func TestMCPHandshakeAndTools(t *testing.T) {
	_, st, url := mcpServer(t)
	c, plain := newClient(t, st, "laptop", "claude-code", nil)
	for _, cl := range []mcpClient{
		{t: t, url: url, header: "Authorization", value: "Bearer " + plain},
		{t: t, url: url, header: "X-Api-Key", value: plain},
	} {
		status, out := cl.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.308"}}}`)
		res, _ := out["result"].(map[string]any)
		if status != http.StatusOK || res["protocolVersion"] != "2025-11-25" ||
			res["serverInfo"].(map[string]any)["name"] != "claudication" ||
			!strings.Contains(res["instructions"].(string), "/model in Claude Code") {
			t.Fatalf("initialize: %d %v", status, out)
		}
		if status, _ := cl.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`); status != http.StatusAccepted {
			t.Errorf("initialized: %d", status)
		}
		_, out = cl.post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "Mcp-Protocol-Version", "2025-11-25")
		var names []string
		for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
			tool := tl.(map[string]any)
			names = append(names, tool["name"].(string))
			if tool["description"] == "" || tool["inputSchema"].(map[string]any)["type"] != "object" {
				t.Errorf("a tool without a description or a schema: %v", tool)
			}
			if n := tool["name"]; n == "bind_model" && !strings.Contains(tool["description"].(string), "/model in Claude Code and Codex, ctrl+l in crush, /models in opencode") {
				t.Errorf("bind_model does not point at the per-chat pickers: %v", tool["description"])
			}
		}
		if strings.Join(names, ",") != "status,list_models,client,chats,name_chat,bind_model,add_model,remove_model,update_config" {
			t.Errorf("tools: %v", names)
		}
		sc, text, isErr := cl.tool("client", `{}`)
		if isErr || sc["id"] != c.ID || sc["type"] != "claude-code" || sc["agent_may_manage"] != false {
			t.Fatalf("client: %s", text)
		}
		first := sc["model_names"].([]any)[0].(map[string]any)
		if first["name"] != "large" || first["slot"] != "main" || first["bound_follows"] != "claude:opus" {
			t.Errorf("client's first name: %v", first)
		}
	}

	cl := mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}
	sc, text, isErr := cl.tool("list_models", `{}`)
	if isErr {
		t.Fatal(text)
	}
	if !strings.Contains(text, `"follows": "claude:sonnet"`) || !strings.Contains(text, `"model": "gpt-6.1-sol"`) ||
		strings.Contains(text, "claude:opus") {
		t.Errorf("list_models is not the client's choices:\n%s", text)
	}
	if sc["models"] == nil {
		t.Errorf("list_models: %v", sc)
	}
	sc, text, isErr = cl.tool("status", `{}`)
	if isErr || sc["gateway"].(map[string]any)["accepting"] != true || sc["limits"] == nil {
		t.Errorf("status: %s", text)
	}
}

// /mcp is refused as /v1 is: no key and a wrong one 401, a switched-off
// client 403, a switched-off gateway 404 — each as a JSON-RPC error.
func TestMCPRefusesAsV1Does(t *testing.T) {
	srv, st, url := mcpServer(t)
	c, plain := newClient(t, st, "laptop", "crush", nil)
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	expect := func(what string, cl mcpClient, want int) {
		t.Helper()
		status, out := cl.post(init)
		if status != want || out["error"] == nil || out["jsonrpc"] != "2.0" {
			t.Errorf("%s: %d %v, want %d", what, status, out, want)
		}
	}
	expect("no key", mcpClient{t: t, url: url}, http.StatusUnauthorized)
	expect("a wrong key", mcpClient{t: t, url: url, header: "Authorization", value: "Bearer clc_nope"}, http.StatusUnauthorized)
	if err := st.SetClientDisabled(context.Background(), c.ID, true); err != nil {
		t.Fatal(err)
	}
	expect("a disabled client", mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}, http.StatusForbidden)
	if err := st.SetClientDisabled(context.Background(), c.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := srv.serving.Set(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	expect("the gateway off", mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}, http.StatusNotFound)
	if err := srv.serving.Set(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if status, _ := (mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}).post(init); status != http.StatusOK {
		t.Errorf("back on: %d", status)
	}
}

// The manage tools refuse until the operator turns the client's switch on,
// and every call, refused or not, is in the request log under the client.
// Then a name binds only to what the client may be offered, an added name is
// the client's own and removable where a type's are not, and update_config
// hands back a command that carries no key.
func TestMCPManageTier(t *testing.T) {
	_, st, url := mcpServer(t)
	ctx := context.Background()
	c, plain := newClient(t, st, "laptop", "crush", nil)
	cl := mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}

	_, text, isErr := cl.tool("bind_model", `{"name":"large","follows":"claude:sonnet"}`)
	if !isErr || !strings.Contains(text, "Agent may manage this client") {
		t.Fatalf("bind_model with the switch off: %v %s", isErr, text)
	}
	if got, _ := st.Client(ctx, c.ID); got.Slots[0].Follows != "claude:opus" {
		t.Errorf("a refused bind changed the slot: %+v", got.Slots[0])
	}
	audit := func() []string {
		t.Helper()
		rows, err := st.DB().Query(`SELECT path, status, kind FROM usage_events WHERE key_id = ? AND path LIKE '/mcp/%' ORDER BY id`, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var path string
			var status, kind int
			if err := rows.Scan(&path, &status, &kind); err != nil {
				t.Fatal(err)
			}
			// A refusal is filed as one (kind 1), a change made as a
			// change (kind 3), which neither refusal counts nor totals take.
			out = append(out, fmt.Sprintf("%s %s %d", path, http.StatusText(status), kind))
		}
		return out
	}
	if got := audit(); len(got) != 1 || got[0] != "/mcp/bind_model Forbidden 1" {
		t.Errorf("the refused call's audit: %v", got)
	}

	if err := st.SetClientAgentManage(ctx, c.ID, true); err != nil {
		t.Fatal(err)
	}
	// Only what the client may be offered: no account lists claude-opus-4.
	if _, text, isErr := cl.tool("bind_model", `{"name":"large","model":"claude-opus-4"}`); !isErr || !strings.Contains(text, "not available to this client") {
		t.Errorf("an unavailable model: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("bind_model", `{"name":"large","follows":"claude:opus"}`); !isErr || !strings.Contains(text, "not available") {
		t.Errorf("an unresolved target: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("bind_model", `{"name":"nope","model":"claude-sonnet-5"}`); !isErr || !strings.Contains(text, "large, small") {
		t.Errorf("a name the client lacks: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("bind_model", `{"name":"large","model":"x","follows":"claude:sonnet"}`); !isErr || !strings.Contains(text, "not both") {
		t.Errorf("both: %v %s", isErr, text)
	}
	sc, text, isErr := cl.tool("bind_model", `{"name":"large","model":"gpt-6.1-sol"}`)
	if isErr || sc["serving"] != "gpt-6.1-sol" {
		t.Fatalf("bind_model: %s", text)
	}
	if got, _ := st.Client(ctx, c.ID); got.Slots[0].Model != "gpt-6.1-sol" || got.Slots[0].Name != "large" {
		t.Errorf("after bind_model: %+v", got.Slots[0])
	}

	if sc, text, isErr := cl.tool("add_model", `{"name":"deep","follows":"claude:sonnet"}`); isErr || sc["serving"] != "claude-sonnet-5" {
		t.Errorf("add_model: %s", text)
	}
	// With no binding, the admin page's rule: the newest Opus, made whether
	// or not an account serves one yet — none does here.
	if _, text, isErr := cl.tool("add_model", `{"name":"later"}`); isErr {
		t.Errorf("add_model with no binding: %s", text)
	}
	if got, _ := st.Client(ctx, c.ID); len(got.Slots) != 4 || got.Slots[3].Follows != "claude:opus" {
		t.Errorf("the default binding: %+v", got.Slots)
	}
	if _, text, isErr := cl.tool("add_model", `{"name":"nowhere","model":"claude-opus-4"}`); !isErr || !strings.Contains(text, "not available") {
		t.Errorf("add_model with an unavailable model: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("remove_model", `{"name":"later"}`); isErr {
		t.Errorf("remove_model: %s", text)
	}
	if _, text, isErr := cl.tool("remove_model", `{"name":"small"}`); !isErr || !strings.Contains(text, "stays") {
		t.Errorf("a type's own name removed: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("remove_model", `{"name":"deep"}`); isErr {
		t.Errorf("remove_model: %s", text)
	}
	if got, _ := st.Client(ctx, c.ID); len(got.Slots) != 2 {
		t.Errorf("after remove_model: %+v", got.Slots)
	}

	// update_config: its command holds no key. The client is set up by now:
	// a key that authenticated is one a client was set up with, on /mcp as
	// on /v1, so an agent can always ask for its update.
	if got, _ := st.Client(ctx, c.ID); got.ConfiguredAt == nil {
		t.Error("a call to /mcp did not mark the client set up")
	}
	if _, text, isErr := cl.tool("update_config", `{"platform":"mac"}`); !isErr || !strings.Contains(text, "unix or windows") {
		t.Errorf("a platform it does not know: %v %s", isErr, text)
	}
	// No address recorded for its machine: refused, rather than one read off
	// this request, which behind a proxy can be one the client cannot reach.
	if _, text, isErr := cl.tool("update_config", `{"platform":"unix"}`, `{}`, "X-Forwarded-Proto", "https"); !isErr ||
		!strings.Contains(text, "address this client was set up with") {
		t.Errorf("update_config with no address known: %v %s", isErr, text)
	}
	// The address its last fetched command recorded is the one written.
	if err := st.SetClientBase(ctx, c.ID, url); err != nil {
		t.Fatal(err)
	}
	for platform, want := range map[string]string{"unix": "| sh", "windows": "| iex"} {
		sc, text, isErr := cl.tool("update_config", `{"platform":"`+platform+`"}`)
		if isErr {
			t.Fatalf("update_config: %s", text)
		}
		cmd := sc["command"].(string)
		key, _ := st.Key(ctx, c.ID)
		if !strings.HasSuffix(cmd, want) || !strings.Contains(cmd, url+"/install?token=") ||
			strings.Contains(text, plain) || strings.Contains(text, store.KeyPrefix+key.Prefix) {
			t.Errorf("update_config %s: %s", platform, text)
		}
		// The command fetches the update, which carries neither.
		token := cmd[strings.Index(cmd, "token=")+6:]
		token = token[:strings.IndexAny(token, "&")]
		shell := map[string]string{"unix": "sh", "windows": "ps1"}[platform]
		resp, err := http.Get(url + "/install?token=" + token + "&shell=" + shell)
		if err != nil {
			t.Fatal(err)
		}
		script, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || strings.Contains(string(script), plain) ||
			strings.Contains(string(script), store.KeyPrefix+key.Prefix) ||
			// PowerShell carries the files base64-encoded.
			(platform == "unix" && !strings.Contains(string(script), `"id": "large"`)) {
			t.Errorf("the update script: %d\n%s", resp.StatusCode, script)
		}
	}

	got := audit()
	if len(got) != 16 || got[len(got)-1] != "/mcp/update_config OK 3" || got[1] != "/mcp/bind_model Bad Request 1" ||
		got[5] != "/mcp/bind_model OK 3" {
		t.Errorf("the audit: %v", got)
	}
	// None of it is a request: the client's traffic counts none, and the
	// request log's filters put each where it belongs.
	if use, err := st.KeyUsageOf(ctx, c.ID, time.Now().Add(-time.Hour)); err != nil || use[c.ID].Requests != 0 {
		t.Errorf("the changes counted as requests: %+v %v", use, err)
	}
	for kind, want := range map[string]int{"change": 7, "rejected": 9, "relayed": 0} {
		rows, _, err := st.RecentUsage(ctx, 100, store.UsageCursor{}, store.RequestFilter{KeyIDs: []string{c.ID}, Kinds: []string{kind}})
		if err != nil || len(rows) != want {
			t.Errorf("filter %s: %d rows, want %d (%v)", kind, len(rows), want, err)
		}
	}
}

// name_chat names a chat of this client's, as a manual title, and no other
// client's; chats lists them; a Codex call names its own chat by the session
// it says in _meta.
func TestMCPNameChat(t *testing.T) {
	_, st, url := mcpServer(t)
	ctx := context.Background()
	c, plain := newClient(t, st, "laptop", "codex", nil)
	other, _ := newClient(t, st, "desk", "codex", nil)
	at := time.Now().Add(-time.Minute)
	for _, e := range []store.UsageEvent{
		{At: at, KeyID: c.ID, Path: "/v1/responses", Status: 200, ConversationID: "sess-mine", Model: "gpt-6.1-sol", InputTokens: 10},
		{At: at.Add(time.Second), KeyID: c.ID, Path: "/v1/responses", Status: 200, ConversationID: "sess-codex", Model: "gpt-6.1-sol", InputTokens: 10},
		{At: at, KeyID: other.ID, Path: "/v1/responses", Status: 200, ConversationID: "sess-theirs", Model: "gpt-6.1-sol", InputTokens: 10},
		// A chat both keys sent requests under — the client can put any id on
		// a request of its own — is neither's to name.
		{At: at, KeyID: other.ID, Path: "/v1/responses", Status: 200, ConversationID: "sess-shared", Model: "gpt-6.1-sol", InputTokens: 10},
		{At: at, KeyID: c.ID, Path: "/v1/responses", Status: 200, ConversationID: "sess-shared", Model: "gpt-6.1-sol", InputTokens: 10},
		// A refusal names whatever chat it likes, and proves nothing.
		{At: at, KeyID: c.ID, Path: "/v1/responses", Status: 429, ConversationID: "sess-theirs", Rejected: true},
	} {
		if err := st.RecordUsage(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	cl := mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}

	sc, text, isErr := cl.tool("chats", `{}`, `{"sessionId":"sess-codex"}`)
	if isErr || sc["current"] != "sess-codex" {
		t.Fatalf("chats: %s", text)
	}
	list := sc["chats"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["id"] != "sess-codex" || list[0].(map[string]any)["current"] != true {
		t.Errorf("chats: %v", list)
	}

	if _, text, isErr := cl.tool("name_chat", `{"chat_id":"sess-mine","title":"  Fix the login bug  "}`); isErr {
		t.Fatalf("name_chat: %s", text)
	}
	rec, ok, err := st.TitleRecord(ctx, "sess-mine")
	if err != nil || !ok || rec.Title != "Fix the login bug" || !rec.Manual {
		t.Errorf("the title as stored: %+v %v %v", rec, ok, err)
	}
	// The client's own naming does not overwrite it.
	if err := st.SetClientTitle(ctx, "sess-mine", "something else", "m"); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := st.TitleRecord(ctx, "sess-mine"); rec.Title != "Fix the login bug" {
		t.Errorf("the client's title replaced the agent's: %q", rec.Title)
	}
	for _, id := range []string{"sess-theirs", "sess-nobody", "sess-shared"} {
		if _, text, isErr := cl.tool("name_chat", `{"chat_id":"`+id+`","title":"mine now"}`); !isErr || !strings.Contains(text, "no chat") {
			t.Errorf("%s: %v %s", id, isErr, text)
		}
		if _, ok, _ := st.TitleRecord(ctx, id); ok {
			t.Errorf("%s was named", id)
		}
	}
	// No id: the chat the call says it comes from, or a refusal asking for one.
	if _, text, isErr := cl.tool("name_chat", `{"title":"Codex's own"}`, `{"sessionId":"sess-codex","threadId":"t"}`); isErr {
		t.Errorf("name_chat by _meta: %s", text)
	}
	if rec, _, _ := st.TitleRecord(ctx, "sess-codex"); rec.Title != "Codex's own" || !rec.Manual {
		t.Errorf("the session's chat: %+v", rec)
	}
	if _, text, isErr := cl.tool("name_chat", `{"title":"where"}`); !isErr || !strings.Contains(text, "give chat_id") {
		t.Errorf("no chat said: %v %s", isErr, text)
	}
	if _, text, isErr := cl.tool("name_chat", `{"chat_id":"sess-mine","title":"   "}`); !isErr || !strings.Contains(text, "empty") {
		t.Errorf("an empty title: %v %s", isErr, text)
	}
}

// A custom client has no type to say which header names its chats, so a call
// is read as /v1 reads an unknown client's: every dialect's header.
func TestMCPCustomClientsChat(t *testing.T) {
	_, st, url := mcpServer(t)
	ctx := context.Background()
	c, plain := newClient(t, st, "sdk", "custom", nil)
	if err := st.RecordUsage(ctx, store.UsageEvent{At: time.Now(), KeyID: c.ID, Path: "/v1/messages", Status: 200,
		ConversationID: "ses_sdk1", Model: "claude-sonnet-5", InputTokens: 1}); err != nil {
		t.Fatal(err)
	}
	cl := mcpClient{t: t, url: url, header: "Authorization", value: "Bearer " + plain}
	if _, text, isErr := cl.tool("name_chat", `{"title":"From the SDK"}`, `{}`, "X-Session-Id", "ses_sdk1"); isErr {
		t.Fatalf("name_chat by the header: %s", text)
	}
	if rec, _, _ := st.TitleRecord(ctx, "ses_sdk1"); rec.Title != "From the SDK" {
		t.Errorf("the chat: %+v", rec)
	}
}

// The admin side: the switch on the client's page, and the files the page
// shows, which register the MCP server with the client's own key or the
// variable it is in.
func TestMCPAdminSwitchAndFiles(t *testing.T) {
	srv, st, _, _, _, _ := twoUpstreams(t)
	base, ck := adminAPI(t, srv)
	c, _ := newClient(t, st, "laptop", "opencode", nil)
	type page struct {
		Client struct {
			AgentManage bool `json:"agent_manage"`
		} `json:"client"`
	}
	if _, body := call(t, base, http.MethodGet, "/admin/clients/"+c.ID, "", ck); decode[page](t, body).Client.AgentManage {
		t.Error("a new client's agent may manage it")
	}
	status, body := call(t, base, http.MethodPost, "/admin/clients/"+c.ID+"/agent-manage", `{"enabled":true}`, ck)
	if status != http.StatusOK || !decode[page](t, body).Client.AgentManage {
		t.Fatalf("switch on: %d %s", status, body)
	}
	if k, _ := st.Key(context.Background(), c.ID); !k.AgentManage {
		t.Error("the key does not carry the switch")
	}
	if status, _ := call(t, base, http.MethodPost, "/admin/clients/"+c.ID+"/agent-manage", `{}`, ck); status != http.StatusBadRequest {
		t.Errorf("no value: %d", status)
	}

	status, body = call(t, base, http.MethodGet, "/admin/clients/"+c.ID+"/files?base=https://gw.example/", "", ck)
	files := decode[struct {
		Files []struct{ Label, Filename, Lang, Body string }
	}](t, body).Files
	if status != http.StatusOK || len(files) != 1 || files[0].Filename != "opencode.jsonc" ||
		!strings.Contains(files[0].Body, `"url": "https://gw.example/mcp"`) || !strings.Contains(files[0].Body, `"Authorization": "Bearer clc_..."`) {
		t.Errorf("files: %d %s", status, body)
	}
	if status, _ := call(t, base, http.MethodGet, "/admin/clients/"+c.ID+"/files?base=nope", "", ck); status != http.StatusBadRequest {
		t.Errorf("a bad base: %d", status)
	}
	custom, _ := newClient(t, st, "sdk", "custom", nil)
	if status, body := call(t, base, http.MethodGet, "/admin/clients/"+custom.ID+"/files?base=https://gw.example", "", ck); status != http.StatusOK ||
		!strings.Contains(string(body), `"files":[]`) {
		t.Errorf("a custom client's files: %d %s", status, body)
	}
}
