// Package mcp is a Model Context Protocol server over the Streamable HTTP
// transport: the part of the specification an agent's tools need, and
// nothing a client would only meet as an error.
//
// Hand-written rather than an SDK's. The official Go SDK
// (github.com/modelcontextprotocol/go-sdk) and mark3labs/mcp-go are each a
// module tree larger than this gateway's own dependencies put together — a
// JSON Schema implementation, OAuth helpers, a session manager, an SSE
// stream per client — for a server that answers six methods, keeps no
// session and pushes nothing. go.mod holds five direct dependencies and each
// has to earn its place; what this needs of the protocol fits in this file,
// and its tests are the specification's own examples.
//
// What is served (specification 2025-11-25, and the two before it, which
// share this transport):
//
//	initialize                 the version both speak, the server's name, its
//	                           one capability (tools) and its instructions
//	notifications/initialized  202, as every notification and response is
//	ping                       {}
//	tools/list                 every tool, in one page: no cursor is ever sent
//	tools/call                 the tool's answer, as text and as structured
//	                           content; a tool's own failure is isError, an
//	                           unknown tool or bad arguments a JSON-RPC error
//
// The transport, from the specification's "Streamable HTTP":
//
//   - POST carries one JSON-RPC message (or, from a 2025-03-26 client, a
//     batch). A request is answered with one application/json body; the
//     transport allows a server to answer with an SSE stream instead, and
//     this one never needs to, since it sends nothing but the answer. A
//     notification or a response is 202 with no body.
//   - GET is how a client opens a stream for the server's own messages; this
//     server has none, and answers 405, which the specification names as the
//     way to say so.
//   - DELETE ends a session; there are none, so 405 again.
//   - No Mcp-Session-Id is ever issued. The specification makes sessions the
//     server's choice ("A server using the Streamable HTTP transport MAY
//     assign a session ID at initialization time"), and a client that was
//     given none sends none. Every request here is answered from the key it
//     carries, so a session would hold nothing.
//   - MCP-Protocol-Version, sent on every request after initialize, is
//     checked: one this server does not speak is 400, as the specification
//     says; none at all is taken to be 2025-03-26, as it also says.
//   - Origin, when a browser sends one, must be the server's own: the
//     specification requires the check against DNS rebinding. A command-line
//     client sends none.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Versions are the protocol versions this server speaks, newest first. An
// initialize asking for one of them is answered in it; one asking for any
// other is answered in the newest, and the client decides whether it can
// speak that (specification, "Version Negotiation").
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// versioned are the methods whose MCP-Protocol-Version header is checked:
// the ones a client sends once it has negotiated a version. A request with no
// header is taken to speak 2025-03-26, as the specification says, which is
// one of Versions, so it is served.
var versioned = map[string]bool{"ping": true, "tools/list": true, "tools/call": true}

// JSON-RPC's error codes, and the one the specification adds.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// maxBody bounds a request: a tool's arguments are a few names.
const maxBody = 1 << 20

// Tool is one tool the server offers.
type Tool struct {
	Name string
	// Title is how a person's screen names it.
	Title string
	// Description is what the agent reads to decide when to call it.
	Description string
	// InputSchema is the JSON Schema its arguments are held to: an object.
	InputSchema json.RawMessage
	// ReadOnly says it changes nothing, Destructive that a change it makes
	// replaces something; both are hints a client may show or act on.
	ReadOnly, Destructive bool
	// Call runs it. args is the arguments object as sent ({} when none).
	// An error the agent should read is a *ToolError; any other is the
	// server's own failure, logged by the caller and answered as one.
	Call func(ctx context.Context, args json.RawMessage) (any, error)
}

// ToolError is a tool's own failure, which the agent is shown: a refusal it
// can act on, as the specification's isError result is meant for.
type ToolError struct{ Message string }

func (e *ToolError) Error() string { return e.Message }

// Refuse is a ToolError.
func Refuse(msg string) error { return &ToolError{Message: msg} }

// ParamsError is a tools/call whose arguments do not fit the tool's schema:
// a JSON-RPC invalid-params error rather than a tool's answer.
type ParamsError struct{ Message string }

func (e *ParamsError) Error() string { return e.Message }

// Server answers MCP requests for a set of tools.
type Server struct {
	// Name and Version are the server's, as initialize reports them.
	Name, Title, Version string
	// Instructions are what a client may put before its model: how the tools
	// fit together.
	Instructions string
	// Tools are offered to every caller; what one may do with them is each
	// tool's own to decide, from the request's context.
	Tools []Tool
	// Failed is told of a tool's failure that is the server's own, for the
	// log; nil says nothing.
	Failed func(ctx context.Context, tool string, err error)
}

// message is a JSON-RPC message as it arrives: a request has a method and an
// id, a notification a method and no id, a response no method.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (m message) isRequest() bool { return m.Method != "" && len(m.ID) > 0 && string(m.ID) != "null" }

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func reply(id json.RawMessage, result any) response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func fail(id json.RawMessage, code int, msg string) response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// WriteError answers a request refused before it reached the server — no key,
// a switched-off client — as a JSON-RPC error with no id, under the HTTP
// status that says why: the status is what a client acts on, and the body is
// what it can show.
func WriteError(w http.ResponseWriter, status int, msg string) {
	code := CodeInvalidRequest
	if status >= 500 {
		code = CodeInternal
	}
	writeJSON(w, status, fail(nil, code, msg))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// ServeHTTP is the transport.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		WriteError(w, http.StatusForbidden, "requests from a web page are not served here")
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet, http.MethodDelete:
		// No stream to open and no session to end (see the package comment).
		w.Header().Set("Allow", "POST")
		WriteError(w, http.StatusMethodNotAllowed, "this server sends nothing of its own: POST requests to it")
		return
	default:
		w.Header().Set("Allow", "POST")
		WriteError(w, http.StatusMethodNotAllowed, "POST requests to this server")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, fail(nil, CodeInvalidRequest, "request too large"))
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		s.serveBatch(w, r, body)
		return
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		writeJSON(w, http.StatusBadRequest, fail(nil, CodeParse, "not a JSON-RPC message: "+err.Error()))
		return
	}
	// The version header is held to what this server speaks on the methods
	// it serves after initialize, as the specification says. Not on the rest:
	// Claude Code 2.1.308 and crush first probe with server/discover under a
	// version newer than any here (2026-07-28), and fall back to initialize on
	// the JSON-RPC error that method gets — a 400 there would be a refusal of
	// a question this server can answer.
	if v := r.Header.Get("Mcp-Protocol-Version"); v != "" && !slices.Contains(Versions, v) && versioned[m.Method] {
		WriteError(w, http.StatusBadRequest, "unsupported MCP-Protocol-Version "+v+"; this server speaks "+strings.Join(Versions, ", "))
		return
	}
	resp, answer := s.handle(r.Context(), r, m)
	if !answer {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// serveBatch answers a JSON-RPC batch, which 2025-03-26 allowed and later
// versions removed: each request answered, in one array; nothing to answer is
// 202.
func (s *Server) serveBatch(w http.ResponseWriter, r *http.Request, body []byte) {
	var batch []message
	if err := json.Unmarshal(body, &batch); err != nil || len(batch) == 0 {
		writeJSON(w, http.StatusBadRequest, fail(nil, CodeInvalidRequest, "not a JSON-RPC batch"))
		return
	}
	var out []response
	for _, m := range batch {
		if resp, answer := s.handle(r.Context(), r, m); answer {
			out = append(out, resp)
		}
	}
	if len(out) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handle is one message: its response, and whether there is one to send.
func (s *Server) handle(ctx context.Context, r *http.Request, m message) (response, bool) {
	if m.JSONRPC != "2.0" {
		return fail(m.ID, CodeInvalidRequest, `not JSON-RPC 2.0: "jsonrpc" must be "2.0"`), true
	}
	if !m.isRequest() {
		// A notification (initialized, cancelled) or a response to a request
		// this server never sends: nothing to answer.
		return response{}, false
	}
	switch m.Method {
	case "initialize":
		return reply(m.ID, s.initialize(m.Params)), true
	case "ping":
		return reply(m.ID, struct{}{}), true
	case "tools/list":
		return reply(m.ID, map[string]any{"tools": s.list()}), true
	case "tools/call":
		return s.call(ctx, m), true
	}
	return fail(m.ID, CodeMethodNotFound, "no method "+m.Method+" here: this server offers tools"), true
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := Versions[0]
	if slices.Contains(Versions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	info := map[string]any{"name": s.Name, "version": s.Version}
	if s.Title != "" {
		info["title"] = s.Title
	}
	out := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      info,
	}
	if s.Instructions != "" {
		out["instructions"] = s.Instructions
	}
	return out
}

func (s *Server) list() []map[string]any {
	out := make([]map[string]any, 0, len(s.Tools))
	for _, t := range s.Tools {
		e := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema}
		if t.Title != "" {
			e["title"] = t.Title
		}
		e["annotations"] = map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.ReadOnly,
			"destructiveHint": t.Destructive,
			"idempotentHint":  t.ReadOnly,
			// Every tool acts on this gateway and nothing beyond it.
			"openWorldHint": false,
		}
		out = append(out, e)
	}
	return out
}

func (s *Server) call(ctx context.Context, m message) response {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Name == "" {
		return fail(m.ID, CodeInvalidParams, "tools/call names a tool and its arguments")
	}
	i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
	if i < 0 {
		return fail(m.ID, CodeInvalidParams, "no tool "+p.Name+" here")
	}
	args := p.Arguments
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	if args[0] != '{' {
		return fail(m.ID, CodeInvalidParams, "a tool's arguments are an object")
	}
	if len(p.Meta) > 0 {
		ctx = context.WithValue(ctx, metaKey{}, p.Meta)
	}
	out, err := s.Tools[i].Call(ctx, args)
	var te *ToolError
	var pe *ParamsError
	switch {
	case errors.As(err, &pe):
		return fail(m.ID, CodeInvalidParams, pe.Message)
	case errors.As(err, &te):
		return reply(m.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": te.Message}},
			"isError": true,
		})
	case err != nil:
		if s.Failed != nil {
			s.Failed(ctx, p.Name, err)
		}
		return reply(m.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "the gateway could not do that; its log says why"}},
			"isError": true,
		})
	}
	return reply(m.ID, result(out))
}

type metaKey struct{}

// Meta is the _meta object of the tools/call being answered, as the client
// sent it, or nil: where a client says what the call belongs to. Codex puts
// its session and thread there (codex-rs core/src/mcp_tool_call.rs at
// rust-v0.163.0); Claude Code 2.1.308 its tool_use id alone.
func Meta(ctx context.Context) json.RawMessage {
	m, _ := ctx.Value(metaKey{}).(json.RawMessage)
	return m
}

// result is a tool's answer: as text, which every client shows its model, and
// as structured content, which a client of 2025-06-18 on can read as data.
// A string is text alone.
func result(v any) map[string]any {
	if s, ok := v.(string); ok {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": strings.TrimSpace(b.String())}}}
	// structuredContent is an object; an answer that is not one is text
	// alone.
	var probe map[string]any
	if json.Unmarshal(b.Bytes(), &probe) == nil {
		out["structuredContent"] = probe
	}
	return out
}

// sameOrigin reports whether a request carries no Origin, or one naming the
// host it was sent to.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}
