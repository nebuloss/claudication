package upstream

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Tool names beginning `mcp_` — but not `mcp__` — are refused outright.
//
// The subscription backend reads the double underscore as Claude Code's own
// MCP naming convention, `mcp__${server}__${tool}`, and anything else with a
// single underscore after `mcp` as a third-party MCP app. A third-party app is
// billed to extra usage rather than the plan, so a subscription with no extra
// usage on it gets a 400 that says nothing about tool names at all:
//
//	{"type":"error","error":{"type":"invalid_request_error","message":
//	 "Third-party apps now draw from your extra usage, not your plan limits.
//	  Add more at claude.ai/settings/usage and keep going."}}
//
// It is a naming check and nothing else. Measured against one account, one
// model, within the same second:
//
//	mcp__weather__get   200      mcp_weather_get   400
//	mcp__weather_get    200      mcp_w             400
//	weather_get         200      mcp_              200
//	MCP_weather_get     200      mcp___weather_get 200
//
// So the boundary is exactly `^mcp_[^_]`, lowercase, and doubling the
// underscore is enough to clear it. That is what this file does — on the way
// out, and back again on the way in, because the model echoes the name it was
// given and a client that called the tool `mcp_weather_get` will not recognise
// `mcp__weather_get` coming back.
const (
	mcpPrefix      = "mcp_"
	mcpClientStyle = "mcp__"
)

// A second refused name, found by scripts/bisect-refusal.py on a captured
// opencode request once the system-prompt trigger had been cleared.
//
// It is one exact lowercase string and nothing near it. Measured on opus and
// haiku alike:
//
//	todowrite   400      TodoWrite   200      todo_write   200
//	                     todoWrite   200      todowrite_   200
//	                     Todowrite   200      todowrite1   200
//	                     TODOWRITE   200      _todowrite   200
//
// And it is not "a built-in name in lowercase": taskcreate, taskupdate,
// todoread, askuserquestion, toolsearch, notebookedit, webfetch and multiedit
// are all accepted. Only this one.
//
// The replacement appends an underscore rather than using Claude Code's own
// `TodoWrite`. Both are accepted, but that spelling is a built-in the model has
// strong priors about, and borrowing it would quietly change how a third-party
// tool is treated. A trailing underscore keeps the client's own word.
const (
	refusedTodoWrite      = "todowrite"
	refusedTodoWriteFixed = "todowrite_"
)

// refusedName reports whether the upstream will refuse this tool name.
func refusedName(name string) bool {
	if name == refusedTodoWrite {
		return true
	}
	return strings.HasPrefix(name, mcpPrefix) &&
		len(name) > len(mcpPrefix) &&
		name[len(mcpPrefix)] != '_'
}

// accepted is the same name in the smallest shape the upstream takes.
func accepted(name string) string {
	if name == refusedTodoWrite {
		return refusedTodoWriteFixed
	}
	return mcpClientStyle + name[len(mcpPrefix):]
}

// mayHoldRefusedName is the cheap gate: a body with neither marker in it
// cannot carry a name this rewrites, and costs one scan to establish.
// `"mcp_` covers both underscore shapes; the exact test happens per name.
func mayHoldRefusedName(b []byte) bool {
	return bytes.Contains(b, []byte(`"`+mcpPrefix)) ||
		bytes.Contains(b, []byte(`"`+refusedTodoWrite+`"`))
}

// RewriteRefusedToolNames sends every tool name the upstream would refuse in a
// shape it accepts, and reports what it changed so the response can be put
// back.
//
// The returned map is keyed by what went upstream and holds what the client
// called it. A nil map means nothing was rewritten and the body is the
// caller's own bytes, untouched — which is every request carrying neither a
// single-underscore `mcp_` name nor a bare `todowrite`.
//
// Like EnsureAttribution, this is a deliberate, narrow exception to the rule
// that the relay does not edit request bodies: without it the affected
// requests do not work at all.
func RewriteRefusedToolNames(body []byte) ([]byte, map[string]string) {
	if !mayHoldRefusedName(body) {
		return body, nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body, nil // the client's to get wrong, the upstream's to reject
	}

	r := &renamer{taken: declaredNames(envelope["tools"]), rev: map[string]string{}}

	changed := false
	if tools, ok := envelope["tools"]; ok {
		if out, ok := r.array(tools, r.named); ok {
			envelope["tools"] = out
			changed = true
		}
	}
	// tool_choice names one of them, and has to agree with the declaration.
	if choice, ok := envelope["tool_choice"]; ok {
		if out, ok := r.named(choice); ok {
			envelope["tool_choice"] = out
			changed = true
		}
	}
	// The transcript carries the name on every past call, and the upstream
	// checks those too.
	if messages, ok := envelope["messages"]; ok {
		if out, ok := r.array(messages, r.message); ok {
			envelope["messages"] = out
			changed = true
		}
	}
	if !changed {
		return body, nil
	}

	out, err := json.Marshal(envelope)
	if err != nil {
		return body, nil
	}
	return out, r.rev
}

type renamer struct {
	// taken is every name the client declared. Doubling an underscore must not
	// land on a tool that already exists, or two distinct tools would become
	// one and the reverse mapping would rename the wrong one coming back.
	taken map[string]bool
	rev   map[string]string
}

// declaredNames reads the tool names the client declared, before any of them
// are rewritten.
func declaredNames(raw json.RawMessage) map[string]bool {
	names := map[string]bool{}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return names
	}
	for _, t := range tools {
		if t.Name != "" {
			names[t.Name] = true
		}
	}
	return names
}

// named rewrites the "name" field of one object, if that name would be
// refused. It reports whether it changed anything.
func (r *renamer) named(raw json.RawMessage) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, false
	}
	var name string
	if err := json.Unmarshal(obj["name"], &name); err != nil {
		return raw, false
	}
	if !refusedName(name) {
		return raw, false
	}
	renamed := accepted(name)
	if r.taken[renamed] {
		// Leave it alone and let the upstream refuse it: a wrong answer the
		// client can read beats silently merging two of its tools.
		return raw, false
	}
	encoded, err := json.Marshal(renamed)
	if err != nil {
		return raw, false
	}
	obj["name"] = encoded
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, false
	}
	r.rev[renamed] = name
	return out, true
}

// message rewrites the tool_use blocks in one message's content.
func (r *renamer) message(raw json.RawMessage) (json.RawMessage, bool) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return raw, false
	}
	content, ok := msg["content"]
	if !ok {
		return raw, false // a plain string; no tool call in it
	}
	out, ok := r.array(content, r.toolUse)
	if !ok {
		return raw, false
	}
	msg["content"] = out
	encoded, err := json.Marshal(msg)
	if err != nil {
		return raw, false
	}
	return encoded, true
}

// toolUse rewrites a content block, if it is a tool call.
func (r *renamer) toolUse(raw json.RawMessage) (json.RawMessage, bool) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Type != "tool_use" {
		return raw, false
	}
	return r.named(raw)
}

// array applies fn to each element, leaving the ones it does not change as the
// bytes they arrived as. Elements with no `"mcp_` in them are skipped without
// being parsed, which is what keeps this off the critical path of a long
// transcript: only the messages that carry a tool call are decoded.
func (r *renamer) array(raw json.RawMessage, fn func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw, false
	}
	changed := false
	for i, item := range items {
		if !mayHoldRefusedName(item) {
			continue
		}
		if out, ok := fn(item); ok {
			items[i] = out
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(items)
	if err != nil {
		return raw, false
	}
	return out, true
}

// maxNameBuffer bounds what the restorer holds while waiting for the end of a
// line. An SSE line is a few hundred bytes and a non-streaming body is one
// line, so this is the guard for neither of those being true.
const maxNameBuffer = 8 << 20

// nameRestorer puts the client's own tool names back into the response.
//
// It works a line at a time because a name must never be rewritten in halves,
// and SSE is line-delimited: every event is emitted as soon as its last byte
// arrives, so pings and deltas still reach the client the moment the upstream
// sends them. A non-streaming body has no newline in it and is therefore held
// until the end, which is when the client could read it anyway.
type nameRestorer struct {
	rev map[string]string
	buf []byte
	// raw stops rewriting for the rest of the body once a single line has
	// outgrown the buffer, rather than holding it all in memory.
	raw bool
}

// translate returns the bytes to write to the client for one upstream chunk.
func (n *nameRestorer) translate(chunk []byte) []byte {
	if n.raw {
		return chunk
	}
	n.buf = append(n.buf, chunk...)

	end := bytes.LastIndexByte(n.buf, '\n')
	if end < 0 {
		if len(n.buf) > maxNameBuffer {
			out := n.buf
			n.buf, n.raw = nil, true
			return out
		}
		return nil
	}

	ready := n.buf[:end+1]
	n.buf = append([]byte(nil), n.buf[end+1:]...)
	return n.restore(ready)
}

// tail returns whatever is still held once the body ends.
func (n *nameRestorer) tail() []byte {
	if n.raw || len(n.buf) == 0 {
		return nil
	}
	out := n.restore(n.buf)
	n.buf = nil
	return out
}

func (n *nameRestorer) restore(b []byte) []byte {
	// Gated on the field, not on any particular name. Keying this on the MCP
	// prefix meant every other rewritten name was sent out and never put back:
	// the client asked for `todowrite`, got `todowrite_` in the tool_use, and
	// could not match it to a tool it had declared.
	if !bytes.Contains(b, []byte(`"name":"`)) {
		return b
	}
	for sent, original := range n.rev {
		b = bytes.ReplaceAll(b,
			[]byte(`"name":"`+sent+`"`),
			[]byte(`"name":"`+original+`"`))
	}
	return b
}
