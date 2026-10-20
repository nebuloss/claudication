package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"claudication/internal/api"
)

// The request half of Messages → Responses: a Messages body rebuilt as the
// Responses body ChatGPT's Codex backend takes. doc.go has the table of
// every field and the measurement behind each line; the comments here say
// why the code is shaped the way it is.

// messagesRequest is the part of a Messages request that has a meaning on
// the ChatGPT backend, or that has to be refused because it has none. A field
// absent here is dropped, and doc.go names each one.
type messagesRequest struct {
	Model        string          `json:"model"`
	System       json.RawMessage `json:"system"`
	Messages     []message       `json:"messages"`
	Tools        []tool          `json:"tools"`
	ToolChoice   *toolChoice     `json:"tool_choice"`
	MaxTokens    int             `json:"max_tokens"`
	Stream       bool            `json:"stream"`
	Thinking     *thinking       `json:"thinking"`
	OutputConfig *outputConfig   `json:"output_config"`
	// The structured-outputs beta's spelling before output_config.format.
	OutputFormat json.RawMessage `json:"output_format"`
	MCPServers   json.RawMessage `json:"mcp_servers"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// block is any content block, in the fields some type of block uses.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Source    *source         `json:"source"`
	Title     string          `json:"title"`
	Context   string          `json:"context"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	// A tool_reference, inside a tool search's result.
	ToolName string `json:"tool_name"`
}

type source struct {
	Type      string          `json:"type"`
	MediaType string          `json:"media_type"`
	Data      string          `json:"data"`
	URL       string          `json:"url"`
	Content   json.RawMessage `json:"content"`
}

type tool struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	DeferLoading bool            `json:"defer_loading"`
	// web_search's.
	AllowedDomains []string      `json:"allowed_domains"`
	BlockedDomains []string      `json:"blocked_domains"`
	UserLocation   *userLocation `json:"user_location"`
}

type userLocation struct {
	Type     string `json:"type,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Country  string `json:"country,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type toolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
}

type thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
	Display      string `json:"display"`
}

type outputConfig struct {
	Effort string          `json:"effort"`
	Format json.RawMessage `json:"format"`
}

// responsesRequest is the body sent. Field order is the order Codex sends
// them in, which nothing depends on; omitempty keeps out what was not asked.
type responsesRequest struct {
	Model             string     `json:"model"`
	Instructions      string     `json:"instructions,omitempty"`
	Input             []any      `json:"input"`
	Tools             []any      `json:"tools,omitempty"`
	ToolChoice        any        `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool      `json:"parallel_tool_calls,omitempty"`
	Reasoning         *reasoning `json:"reasoning,omitempty"`
	MaxOutputTokens   int        `json:"max_output_tokens,omitempty"`
	Text              any        `json:"text,omitempty"`
	Store             bool       `json:"store"`
	Stream            bool       `json:"stream"`
	Include           []string   `json:"include"`
	PromptCacheKey    string     `json:"prompt_cache_key,omitempty"`
}

type reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Translation is a Messages request rebuilt for the ChatGPT backend, with
// what the answer side needs to say it back in Messages.
type Translation struct {
	Body []byte
	// Model and Stream are what the caller asked for: the answer names the
	// model it asked for, and is streamed or not as it asked, whatever went
	// upstream (which always streams).
	Model  string
	Stream bool
	// Thinking is whether the caller asked for thinking, and so is answered
	// with thinking blocks; Omitted that it asked for them without their
	// text (display: "omitted").
	Thinking bool
	Omitted  bool
	// Tools maps a function name sent upstream back to the caller's, for the
	// names that had to be shortened. Nil when none was.
	Tools map[string]string
	// Estimate is the input as count_tokens estimates it (counttokens.go):
	// set only for a count, which is the one translation that needs it.
	Estimate int

	// conversation and reasoning are where the answer's reasoning items are
	// kept for a caller that does not carry them itself (reasoningstore.go).
	conversation string
	reasoning    *reasoningStore
}

// options are what a translation is for, beyond the body.
type options struct {
	conversation string
	// reasoning keeps the answer's reasoning items for the next turn; nil
	// keeps none.
	reasoning *reasoningStore
	// count is a translation for count_tokens: nothing is sent, so nothing
	// is refused — what has no equivalent upstream is left out of the
	// count rather than failing it — and the body is estimated, not built.
	count bool
}

// builder carries the options through the conversion of the parts.
type builder struct{ options }

// skip reports whether err, from converting one part, leaves that part out
// rather than failing the whole: in a count, a part with no equivalent.
func (b builder) skip(err error) bool {
	var r refusal
	return b.count && errors.As(err, &r)
}

// minOutputTokens is the backend's floor for max_output_tokens. Measured
// 2026-10-20: 1 is a 400, "Expected a value >= 16".
const minOutputTokens = 16

// maxToolName is the longest function name the Responses API takes
// (^[a-zA-Z0-9_-]{1,64}$); Anthropic takes 128, and MCP tools, named
// mcp__<server>__<tool>, reach past 64 routinely.
const maxToolName = 64

// billingHeader starts the first system block Claude Code sends: its
// attestation line, not an instruction (capture of claude-cli 2.1.304,
// 2026-10-20). It means nothing to another vendor and is not sent there.
const billingHeader = "x-anthropic-billing-header:"

// refusal is a request the backend cannot be sent: it says why, in words for
// the person reading the client's log.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refuse(format string, args ...any) error { return refusal{fmt.Sprintf(format, args...)} }

// MessagesToResponses rebuilds a Messages request for ChatGPT's Codex backend.
// conversation names the chat it belongs to, the backend's prompt cache key.
func MessagesToResponses(body []byte, conversation string) (Translation, error) {
	return translateMessages(body, options{conversation: conversation})
}

func translateMessages(body []byte, o options) (Translation, error) {
	b := builder{o}
	conversation := o.conversation
	var in messagesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return Translation{}, fmt.Errorf("not a Messages request: %w", err)
	}
	if in.Model == "" {
		return Translation{}, fmt.Errorf("no model in the request")
	}
	if !o.count && len(in.MCPServers) > 0 && string(in.MCPServers) != "null" && string(in.MCPServers) != "[]" {
		return Translation{}, refuse("mcp_servers (Anthropic's MCP connector) has no equivalent on the ChatGPT backend; " +
			"connect the MCP server to the client itself")
	}

	out := responsesRequest{
		Model: in.Model,
		// The backend answers a request without stream:true with a 400,
		// "Stream must be set to true" (measured 2026-10-20). A caller that
		// did not ask to stream is answered whole from the stream (sink.go).
		Stream: true,
		// Nothing is kept on OpenAI's side; the reasoning is carried in the
		// answer instead, encrypted, for the caller to send back (doc.go).
		Store:          false,
		Include:        []string{"reasoning.encrypted_content"},
		PromptCacheKey: api.CleanIdentifier(conversation),
	}
	tr := Translation{Model: in.Model, Stream: in.Stream, conversation: conversation, reasoning: o.reasoning}

	instructions, err := systemText(in.System)
	if err != nil {
		return Translation{}, err
	}
	out.Instructions = instructions

	// Tools before the messages: a deferred tool is offered only once a tool
	// search in the history has loaded it, and a shortened name has to be
	// the same in a call coming back as history as in the tool list.
	loaded := loadedTools(in.Messages)
	names := map[string]string{}
	webSearch := false
	for _, t := range in.Tools {
		converted, err := convertTool(t, loaded, names)
		if b.skip(err) {
			continue
		}
		if err != nil {
			return Translation{}, err
		}
		if converted == nil {
			continue
		}
		if t.Type != "" && t.Type != "custom" {
			webSearch = true
		}
		out.Tools = append(out.Tools, converted)
	}
	if len(names) > 0 {
		tr.Tools = map[string]string{}
		for up, own := range names {
			if up != own {
				tr.Tools[up] = own
			}
		}
		if len(tr.Tools) == 0 {
			tr.Tools = nil
		}
	}
	if webSearch {
		// Without it the web_search_call items carry the query and not the
		// pages found, and a Messages client is owed the pages
		// (web_search_tool_result). Measured 2026-10-20: the sources come
		// back as a list of URLs.
		out.Include = append(out.Include, "web_search_call.action.sources")
	}
	if len(out.Tools) > 0 {
		out.ToolChoice, out.ParallelToolCalls = convertToolChoice(in.ToolChoice)
	}

	input, err := b.convertMessages(in.Messages)
	if err != nil {
		return Translation{}, err
	}
	out.Input = input

	if in.MaxTokens > 0 {
		out.MaxOutputTokens = max(in.MaxTokens, minOutputTokens)
	}

	out.Reasoning, tr.Thinking, tr.Omitted = convertThinking(in.Thinking, in.OutputConfig)

	format := in.OutputFormat
	if in.OutputConfig != nil && len(in.OutputConfig.Format) > 0 {
		format = in.OutputConfig.Format
	}
	if text, err := convertFormat(format); err != nil && !b.skip(err) {
		return Translation{}, err
	} else if text != nil {
		out.Text = text
	}

	if o.count {
		tr.Estimate = estimateTokens(out)
		return tr, nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return Translation{}, fmt.Errorf("encode the Responses request: %w", err)
	}
	tr.Body = encoded
	return tr, nil
}

// systemText is the system prompt as one string: a string as it is, a block
// list as its texts joined by blank lines, less Claude Code's billing line.
// cache_control has no equivalent and goes with the blocks: the backend
// caches a prefix by itself, keyed by prompt_cache_key.
func systemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("system is neither a string nor a list of blocks: %w", err)
	}
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" || strings.TrimSpace(b.Text) == "" || strings.HasPrefix(b.Text, billingHeader) {
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

// contentBlocks reads a message's content, which is a string or a list.
func contentBlocks(raw json.RawMessage) ([]block, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []block{{Type: "text", Text: s}}, nil
	}
	var blocks []block
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("content is neither a string nor a list of blocks: %w", err)
	}
	return blocks, nil
}

// convertMessages maps the turns onto Responses input items, in order.
//
// A Messages turn is a list of blocks of several kinds, and a Responses input
// is a flat list of items, each one kind. So a turn becomes several items:
// its text and pictures one message item, each tool call or tool result an
// item of its own, each reasoning an item of its own, in the order the blocks
// came — which is the order the backend needs, a reasoning item before the
// call or message it led to.
func (b builder) convertMessages(messages []message) ([]any, error) {
	items := []any{}
	for i, m := range messages {
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		switch m.Role {
		case "assistant":
			items = append(items, b.assistantItems(blocks)...)
		case "user":
			more, err := b.userItems(blocks, "user")
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", i, err)
			}
			items = append(items, more...)
		case "system":
			// Claude Code sends system turns inside messages (its api_system
			// turns: the environment, captured 2026-10-20). Responses has the
			// developer role for exactly that, measured accepted in input
			// (internal/provider/openai, no-instructions.json).
			more, err := b.userItems(blocks, "developer")
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", i, err)
			}
			items = append(items, more...)
		default:
			return nil, refuse("messages[%d] has role %q, which the Messages API does not have", i, m.Role)
		}
	}
	return items, nil
}

// inputMessage is a Responses message item being built from a run of blocks.
type inputMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

// userItems maps a user (or system) turn. Text, pictures and documents go in
// one message item; a tool result is an item of its own, so a run of parts
// is closed before one and a new one opened after.
func (bd builder) userItems(blocks []block, role string) ([]any, error) {
	var items []any
	cur := &inputMessage{Type: "message", Role: role}
	flush := func() {
		if len(cur.Content) > 0 {
			items = append(items, cur)
			cur = &inputMessage{Type: "message", Role: role}
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			flush()
			out, err := bd.toolResultOutput(b)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": b.ToolUseID,
				"output":  out,
			})
		default:
			parts, err := inputParts(b)
			if bd.skip(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			cur.Content = append(cur.Content, parts...)
		}
	}
	flush()
	return items, nil
}

// inputParts maps one block of a user turn onto Responses content parts.
func inputParts(b block) ([]any, error) {
	switch b.Type {
	case "text":
		if strings.TrimSpace(b.Text) == "" {
			return nil, nil
		}
		return []any{inputText(b.Text)}, nil
	case "image":
		part, err := imagePart(b.Source)
		if err != nil {
			return nil, err
		}
		return []any{part}, nil
	case "document":
		return documentParts(b)
	case "search_result":
		// A search result the client fetched itself: its text, said where it
		// came from, because there is no block that cites it here.
		var texts []string
		if b.Title != "" {
			texts = append(texts, b.Title)
		}
		if b.Source != nil && b.Source.URL != "" {
			texts = append(texts, b.Source.URL)
		}
		inner, _ := contentBlocks(b.Content)
		for _, c := range inner {
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		return []any{inputText(strings.Join(texts, "\n"))}, nil
	case "tool_reference":
		return []any{inputText("Tool " + b.ToolName + " is now available.")}, nil
	case "thinking", "redacted_thinking", "server_tool_use", "web_search_tool_result",
		"web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result", "mcp_tool_use", "mcp_tool_result":
		// Only an assistant turn carries these, and a client that puts one in
		// a user turn is echoing history; what it found is in the text after.
		return nil, nil
	}
	return nil, refuse("a %q content block has no equivalent on the ChatGPT backend", b.Type)
}

func inputText(s string) map[string]any { return map[string]any{"type": "input_text", "text": s} }

// imagePart is an image as input_image: base64 as a data URL, a URL as it
// is. Measured 2026-10-20: an 8x8 PNG as a data URL answered "Red".
func imagePart(s *source) (map[string]any, error) {
	if s == nil {
		return nil, refuse("an image block has no source")
	}
	switch s.Type {
	case "base64":
		return map[string]any{"type": "input_image", "image_url": "data:" + s.MediaType + ";base64," + s.Data}, nil
	case "url":
		return map[string]any{"type": "input_image", "image_url": s.URL}, nil
	}
	return nil, refuse("an image whose source is %q (an Anthropic file) cannot be sent to the ChatGPT backend; "+
		"send it as base64 or a URL", s.Type)
}

// documentParts maps a document. A PDF goes as input_file, which the backend
// read (measured 2026-10-20: a one-page PDF in file_data was answered, the
// file counted in the input tokens). Plain text and a list of blocks go as
// text, which is what Anthropic makes of them too.
func documentParts(b block) ([]any, error) {
	s := b.Source
	if s == nil {
		return nil, refuse("a document block has no source")
	}
	title := b.Title
	if title == "" {
		title = "document.pdf"
	}
	switch s.Type {
	case "base64":
		if s.MediaType != "application/pdf" {
			return nil, refuse("a %s document cannot be sent to the ChatGPT backend; only PDF and text are", s.MediaType)
		}
		return []any{map[string]any{"type": "input_file", "filename": title,
			"file_data": "data:application/pdf;base64," + s.Data}}, nil
	case "url":
		return []any{map[string]any{"type": "input_file", "file_url": s.URL}}, nil
	case "text":
		return []any{inputText(documentText(b.Title, b.Context, s.Data))}, nil
	case "content":
		inner, err := contentBlocks(s.Content)
		if err != nil {
			return nil, err
		}
		var texts []string
		var parts []any
		for _, c := range inner {
			switch c.Type {
			case "text":
				texts = append(texts, c.Text)
			case "image":
				p, err := imagePart(c.Source)
				if err != nil {
					return nil, err
				}
				parts = append(parts, p)
			}
		}
		return append([]any{inputText(documentText(b.Title, b.Context, strings.Join(texts, "\n")))}, parts...), nil
	}
	return nil, refuse("a document whose source is %q (an Anthropic file) cannot be sent to the ChatGPT backend", s.Type)
}

func documentText(title, context, text string) string {
	var head []string
	if title != "" {
		head = append(head, title)
	}
	if context != "" {
		head = append(head, context)
	}
	if len(head) == 0 {
		return text
	}
	return strings.Join(head, "\n") + "\n\n" + text
}

// toolResultOutput is a tool result's content as function_call_output takes
// it: a string when it is all text, a list of parts when it holds pictures
// (measured 2026-10-20: an image in an output list was seen, "Blue").
//
// Responses has no is_error. An error is said in the text instead, so the
// model reads that the call failed rather than that it returned this.
func (bd builder) toolResultOutput(b block) (any, error) {
	inner, err := contentBlocks(b.Content)
	if err != nil {
		return nil, fmt.Errorf("tool_result %s: %w", b.ToolUseID, err)
	}
	var texts []string
	var parts []any
	pictures := false
	for _, c := range inner {
		switch c.Type {
		case "text":
			texts = append(texts, c.Text)
			parts = append(parts, inputText(c.Text))
		case "tool_reference":
			t := "Tool " + c.ToolName + " is now available."
			texts = append(texts, t)
			parts = append(parts, inputText(t))
		default:
			more, err := inputParts(c)
			if bd.skip(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, p := range more {
				if m, ok := p.(map[string]any); ok && m["type"] != "input_text" {
					pictures = true
				}
			}
			parts = append(parts, more...)
		}
	}
	if b.IsError {
		prefix := inputText("Error:")
		texts = append([]string{"Error:"}, texts...)
		parts = append([]any{prefix}, parts...)
	}
	if pictures {
		return parts, nil
	}
	return strings.Join(texts, "\n"), nil
}

// assistantItems maps an assistant turn: its text as an output message, each
// tool call as a function_call, each of our reasoning blocks as the
// reasoning item it came from. Anything else is history of work done on
// Anthropic's side (server tools, thinking signed by Anthropic) that this
// backend cannot read, and its outcome is in the text that follows it.
func (bd builder) assistantItems(blocks []block) []any {
	var items []any
	var text []any
	var said strings.Builder
	// carried is whether the item about to go out has its reasoning before
	// it already, sent back by the caller as a thinking block.
	carried := false
	// restore puts back the reasoning the backend gave before an item, when
	// the caller did not send it back (reasoningstore.go).
	restore := func(key string) {
		if !carried {
			for _, r := range bd.reasoning.get(bd.conversation, key) {
				items = append(items, r.item())
			}
		}
		carried = false
	}
	flush := func() {
		if len(text) > 0 {
			restore(textKey(said.String()))
			items = append(items, map[string]any{"type": "message", "role": "assistant", "content": text})
			text = nil
			said.Reset()
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			// Measured 2026-10-20: an assistant message with output_text and
			// no id or status is read as history ("zebra" recalled).
			text = append(text, map[string]any{"type": "output_text", "text": b.Text})
			said.WriteString(b.Text)
		case "tool_use":
			flush()
			args := "{}"
			if len(b.Input) > 0 && string(b.Input) != "null" {
				args = string(b.Input)
			}
			restore(callKey(b.ID))
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   b.ID,
				"name":      upstreamToolName(b.Name),
				"arguments": args,
			})
		case "thinking":
			r, ok := decodeSignature(b.Signature)
			if !ok {
				// Signed by Anthropic: proof of a Claude turn, which the
				// ChatGPT backend can neither check nor read.
				continue
			}
			flush()
			r.Summary = b.Thinking
			items = append(items, r.item())
			carried = true
		}
	}
	flush()
	return items
}

// loadedTools are the deferred tools a tool search in this conversation has
// loaded: the ones a tool_reference in a tool result names.
//
// Claude Code marks most of its tools defer_loading (capture of 2.1.304:
// DeferredToolPlaceholder and every MCP tool), and Anthropic shows the model
// such a tool only once its ToolSearch has returned a reference to it. The
// ChatGPT backend has no deferral, so the same rule is applied here: a
// deferred tool is offered once it has been loaded, and not before.
func loadedTools(messages []message) map[string]bool {
	loaded := map[string]bool{}
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_reference" {
				loaded[b.ToolName] = true
			}
			if b.Type != "tool_result" {
				continue
			}
			inner, _ := contentBlocks(b.Content)
			for _, c := range inner {
				if c.Type == "tool_reference" {
					loaded[c.ToolName] = true
				}
			}
		}
	}
	return loaded
}

// convertTool maps one tool, or returns nil for one not offered now.
//
// A client tool becomes a function, its input_schema the parameters, with
// strict off: Anthropic does not hold a schema to the strict subset, and a
// strict function whose schema is outside it is a 400. web_search becomes
// the backend's own web search (measured 2026-10-20: searched, and listed the
// pages). Every other Anthropic-defined tool runs on Anthropic's servers or
// to Anthropic's schema, has nothing on this backend to run it, and is
// refused by name rather than dropped: a model offered a tool that does
// nothing produces a turn nobody can explain.
func convertTool(t tool, loaded map[string]bool, names map[string]string) (any, error) {
	switch {
	case t.Type == "" || t.Type == "custom":
		if t.DeferLoading && !loaded[t.Name] {
			return nil, nil
		}
		up := upstreamToolName(t.Name)
		if other, taken := names[up]; taken && other != t.Name {
			return nil, refuse("tools %q and %q shorten to the same name on the ChatGPT backend", other, t.Name)
		}
		names[up] = t.Name
		params := t.InputSchema
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		fn := map[string]any{"type": "function", "name": up, "parameters": params, "strict": false}
		if t.Description != "" {
			fn["description"] = t.Description
		}
		return fn, nil
	case strings.HasPrefix(t.Type, "web_search_"):
		if len(t.BlockedDomains) > 0 {
			return nil, refuse("web_search's blocked_domains has no equivalent on the ChatGPT backend, " +
				"which can only be told which domains to search (allowed_domains)")
		}
		ws := map[string]any{"type": "web_search"}
		if len(t.AllowedDomains) > 0 {
			ws["filters"] = map[string]any{"allowed_domains": t.AllowedDomains}
		}
		if t.UserLocation != nil {
			loc := *t.UserLocation
			loc.Type = "approximate"
			ws["user_location"] = loc
		}
		return ws, nil
	}
	return nil, refuse("tool type %q has no equivalent on the ChatGPT backend; "+
		"bind this client's model to a Claude model to use it", t.Type)
}

// upstreamToolName is a tool's name as the backend takes it: unchanged when
// it fits, else cut and given a hash of the whole, so that two long names
// sharing a prefix stay two names and the same name always maps the same way
// — which is what lets a call in the history be named without remembering
// the last request.
func upstreamToolName(name string) string {
	if len(name) <= maxToolName {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:maxToolName-9] + "_" + hex.EncodeToString(sum[:4])
}

// convertToolChoice maps tool_choice, and parallel calls: Anthropic allows
// them unless told not to, which Responses spells as a flag that turns them
// on. Measured 2026-10-20: required and a named function both forced the
// call, none kept the model from calling.
func convertToolChoice(c *toolChoice) (any, *bool) {
	parallel := true
	if c == nil {
		return "auto", &parallel
	}
	parallel = !c.DisableParallelToolUse
	switch c.Type {
	case "any":
		return "required", &parallel
	case "none":
		return "none", &parallel
	case "tool":
		return map[string]any{"type": "function", "name": upstreamToolName(c.Name)}, &parallel
	}
	return "auto", &parallel
}

// The efforts every model the backend lists supports (models.json,
// supported_reasoning_levels, 2026-10-19), which are also Anthropic's
// output_config.effort values: the one vocabulary maps to itself.
var efforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// convertThinking maps thinking and effort onto reasoning, and says whether
// the answer carries thinking blocks.
//
//   - output_config.effort, when it is one the backend lists, is the effort.
//   - else thinking enabled with a budget is the effort that budget is the
//     scale of: under 4k low, under 16k medium, under 32k high, then xhigh.
//   - thinking disabled is low, the least every model lists ("none" was
//     accepted by gpt-6-luna but is listed by no model).
//   - thinking absent, or adaptive with no effort, leaves the effort to the
//     model's own default (default_reasoning_level), as a Claude model
//     chooses its own when adaptive.
//
// A summary is asked for only when the caller will show thinking: it is the
// thinking text the caller sees, and Claude Code's display "omitted" says it
// will not show any (captured 2026-10-20).
func convertThinking(t *thinking, oc *outputConfig) (*reasoning, bool, bool) {
	r := &reasoning{}
	on, omitted := false, false
	if t != nil {
		switch t.Type {
		case "enabled", "adaptive":
			on = true
			omitted = t.Display == "omitted"
			if t.Type == "enabled" && t.BudgetTokens > 0 {
				switch b := t.BudgetTokens; {
				case b < 4096:
					r.Effort = "low"
				case b < 16384:
					r.Effort = "medium"
				case b < 32768:
					r.Effort = "high"
				default:
					r.Effort = "xhigh"
				}
			}
		case "disabled":
			r.Effort = "low"
		}
	}
	if oc != nil && efforts[oc.Effort] {
		r.Effort = oc.Effort
	}
	if on && !omitted {
		r.Summary = "auto"
	}
	if r.Effort == "" && r.Summary == "" {
		return nil, on, omitted
	}
	return r, on, omitted
}

// convertFormat maps structured output, output_config.format, onto
// text.format. Claude Code asks for its session titles this way. Measured
// 2026-10-20: a json_schema format with strict off was answered with JSON
// matching it. Strict off for the reason function tools have it off.
func convertFormat(raw json.RawMessage) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
		Name   string          `json:"name"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("output format: %w", err)
	}
	if f.Type != "json_schema" {
		return nil, refuse("output format %q has no equivalent on the ChatGPT backend", f.Type)
	}
	name := f.Name
	if name == "" {
		name = "output"
	}
	return map[string]any{"format": map[string]any{
		"type": "json_schema", "name": name, "schema": f.Schema, "strict": false,
	}}, nil
}
