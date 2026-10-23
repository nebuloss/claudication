package anthropic

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"

	"claudication/internal/anthropicwire"
)

// The answer half of Messages → Responses: the backend's Responses events,
// said as the Anthropic events a Messages client reads. The events and their
// order are Anthropic's own — message_start, then each content block opened,
// streamed and closed, then message_delta with the stop reason and usage,
// then message_stop — because that is the order the Anthropic SDKs accumulate
// in, and a client built on one reads nothing else.
//
// The Responses events read, as measured 2026-10-20 (testdata/chatgpt-*.sse):
//
//	response.created                        message_start
//	output_item.added    message            (nothing: its parts open blocks)
//	content_part.added   output_text        content_block_start text
//	output_text.delta                       text_delta
//	content_part.done                       content_block_stop
//	output_item.added    reasoning          content_block_start thinking
//	reasoning_summary_text.delta            thinking_delta
//	output_item.done     reasoning          signature_delta, content_block_stop
//	output_item.added    function_call      content_block_start tool_use
//	function_call_arguments.delta           input_json_delta
//	output_item.done     function_call      content_block_stop
//	output_item.done     web_search_call    server_tool_use, web_search_tool_result
//	response.completed                      message_delta, message_stop
//	response.incomplete                     message_delta (max_tokens), message_stop
//	response.failed, error                  error
//
// response.completed's own output list is not read, because it is not always
// there: measured 2026-10-20, a turn whose text streamed in full ended in a
// completed event whose output was empty. The items as they streamed are the
// answer.

// emitter receives the Anthropic events: written as a stream, or gathered
// into one message (sink.go).
type emitter interface {
	emit(event string, payload any)
}

// converter is the state of one answer being translated.
type converter struct {
	tr  Translation
	out emitter

	started  bool
	finished bool
	next     int // the next Anthropic block index

	// The blocks open now, by the Responses position that opened them: an
	// item's output_index, and for a message's parts the content_index too.
	open map[string]*openBlock
	// seen are the positions that ever opened a block, so a done item
	// does not send again what already streamed.
	seen map[string]bool

	toolCalls bool
	refused   bool
	searches  int

	// pending are the reasoning items since the last call or message, kept
	// for what they lead to (reasoningstore.go); said is each message's text
	// as it streamed, for a done item that does not repeat it.
	pending []carriedReasoning
	said    map[int]*strings.Builder
}

type openBlock struct {
	index    int
	kind     string // text | thinking | tool_use
	streamed bool   // any delta went out
	parts    int    // a reasoning item's summary parts seen
}

func newConverter(tr Translation, out emitter) *converter {
	tr.Body = nil // gone upstream already; the answer never needs it
	return &converter{tr: tr, out: out, open: map[string]*openBlock{}, seen: map[string]bool{}}
}

func (c *converter) live() bool { return c.started && !c.finished }
func (c *converter) done() bool { return c.finished }

// whole reads a whole Responses object: its items as if each had streamed
// done, then its end.
func (c *converter) whole(body []byte) (func(), error) {
	var r responseObject
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	return func() {
		c.start(&r)
		for i := range r.Output {
			c.itemDone(i, &r.Output[i])
		}
		if r.Status == "incomplete" && r.IncompleteDetails != nil {
			c.finish(&r, r.IncompleteDetails.Reason)
		} else {
			c.finish(&r, "")
		}
	}, nil
}

// responsesEvent is any event of the stream, in the fields some event uses.
type responsesEvent struct {
	Type         string          `json:"type"`
	OutputIndex  int             `json:"output_index"`
	ContentIndex int             `json:"content_index"`
	SummaryIndex int             `json:"summary_index"`
	Delta        string          `json:"delta"`
	Item         *outputItem     `json:"item"`
	Part         *contentPart    `json:"part"`
	Response     *responseObject `json:"response"`
	// An error event's own fields.
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Error   *responsesError `json:"error"`
}

type outputItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id"`
	CallID           string        `json:"call_id"`
	Name             string        `json:"name"`
	Arguments        string        `json:"arguments"`
	Content          []contentPart `json:"content"`
	Summary          []contentPart `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`
	Action           *searchAction `json:"action"`
}

type contentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type searchAction struct {
	Query   string `json:"query"`
	Sources []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"sources"`
}

type responseObject struct {
	ID                string          `json:"id"`
	Status            string          `json:"status"`
	Error             *responsesError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage  *responsesUsage `json:"usage"`
	Output []outputItem    `json:"output"`
}

type responsesError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokens int `json:"output_tokens"`
}

// handle takes one event's payload.
func (c *converter) handle(data []byte) {
	if c.finished {
		return
	}
	var ev responsesEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return
	}
	switch ev.Type {
	case "response.created", "response.in_progress":
		c.start(ev.Response)

	case "response.output_item.added":
		c.start(nil)
		c.itemAdded(ev.OutputIndex, ev.Item)

	case "response.content_part.added":
		if ev.Part != nil {
			c.openText(partKey(ev.OutputIndex, ev.ContentIndex), ev.Part.Type == "refusal")
		}

	case "response.output_text.delta", "response.refusal.delta":
		if c.said == nil {
			c.said = map[int]*strings.Builder{}
		}
		if c.said[ev.OutputIndex] == nil {
			c.said[ev.OutputIndex] = &strings.Builder{}
		}
		c.said[ev.OutputIndex].WriteString(ev.Delta)
		b := c.openText(partKey(ev.OutputIndex, ev.ContentIndex), ev.Type == "response.refusal.delta")
		c.delta(b, map[string]any{"type": "text_delta", "text": ev.Delta})

	case "response.content_part.done":
		c.stop(partKey(ev.OutputIndex, ev.ContentIndex))

	case "response.reasoning_summary_part.added":
		if b := c.open[itemKey(ev.OutputIndex)]; b != nil && b.kind == "thinking" {
			// Parts are separate paragraphs of one summary, each its own
			// bold heading (testdata/chatgpt-reasoning.sse); one thinking
			// block holds them all, a blank line apart.
			if b.parts > 0 && !c.tr.Omitted {
				c.delta(b, map[string]any{"type": "thinking_delta", "thinking": "\n\n"})
			}
			b.parts++
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if b := c.open[itemKey(ev.OutputIndex)]; b != nil && b.kind == "thinking" && !c.tr.Omitted {
			c.delta(b, map[string]any{"type": "thinking_delta", "thinking": ev.Delta})
		}

	case "response.function_call_arguments.delta":
		if b := c.open[itemKey(ev.OutputIndex)]; b != nil && b.kind == "tool_use" && ev.Delta != "" {
			c.delta(b, map[string]any{"type": "input_json_delta", "partial_json": ev.Delta})
		}

	case "response.output_item.done":
		c.start(nil)
		c.itemDone(ev.OutputIndex, ev.Item)

	case "response.completed":
		c.finish(ev.Response, "")

	case "response.incomplete":
		reason := ""
		if ev.Response != nil && ev.Response.IncompleteDetails != nil {
			reason = ev.Response.IncompleteDetails.Reason
		}
		c.finish(ev.Response, reason)

	case "response.failed":
		var e responsesError
		if ev.Response != nil && ev.Response.Error != nil {
			e = *ev.Response.Error
		}
		c.fail(errorKind(0, e.Code, e.Type), cmp.Or(e.Message, "the upstream failed the response without saying why"))

	case "error":
		e := responsesError{Code: ev.Code, Message: ev.Message}
		if ev.Error != nil {
			e = *ev.Error
		}
		c.fail(errorKind(0, e.Code, e.Type), cmp.Or(e.Message, "the upstream sent an error without saying what"))
	}
}

func itemKey(output int) string       { return strconv.Itoa(output) }
func partKey(output, part int) string { return strconv.Itoa(output) + "/" + strconv.Itoa(part) }

// messageStart is message_start's message. A struct, so model comes before
// content: the renamer says a model name back in the first model field of
// message_start (passes.ModelRenamer), and that has to be this one.
type messageStart struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Model        string              `json:"model"`
	Content      []any               `json:"content"`
	StopReason   *string             `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence"`
	Usage        anthropicwire.Usage `json:"usage"`
}

// start sends message_start, once, before anything else. The input it
// reports is zero: the backend says what the input was only at the end, and
// message_delta carries it then, as Anthropic's own message_delta does.
func (c *converter) start(r *responseObject) {
	if c.started {
		return
	}
	c.started = true
	id := "msg_gateway"
	if r != nil && r.ID != "" {
		id = "msg_" + strings.TrimPrefix(r.ID, "resp_")
	}
	c.out.emit(anthropicwire.EventMessageStart, map[string]any{
		"type": anthropicwire.EventMessageStart,
		"message": messageStart{
			ID: id, Type: "message", Role: "assistant", Model: c.tr.Model, Content: []any{},
		},
	})
	// Anthropic sends one after message_start; a client may count on it.
	c.out.emit(anthropicwire.EventPing, map[string]any{"type": anthropicwire.EventPing})
}

// begin opens an Anthropic block.
func (c *converter) begin(key, kind string, block map[string]any) *openBlock {
	b := &openBlock{index: c.next, kind: kind}
	c.next++
	c.open[key] = b
	c.seen[key] = true
	c.out.emit(anthropicwire.EventContentBlockStart, map[string]any{
		"type": anthropicwire.EventContentBlockStart, "index": b.index, "content_block": block,
	})
	return b
}

func (c *converter) delta(b *openBlock, d map[string]any) {
	if b == nil {
		return
	}
	b.streamed = true
	c.out.emit(anthropicwire.EventContentBlockDelta, map[string]any{
		"type": anthropicwire.EventContentBlockDelta, "index": b.index, "delta": d,
	})
}

func (c *converter) stop(key string) {
	b := c.open[key]
	if b == nil {
		return
	}
	delete(c.open, key)
	c.out.emit(anthropicwire.EventContentBlockStop, map[string]any{
		"type": anthropicwire.EventContentBlockStop, "index": b.index,
	})
}

// openText is the text block for a message's part, opened if it is not.
// A refusal part is text too: what the model said in declining, with the
// turn's stop reason saying it declined.
func (c *converter) openText(key string, refusal bool) *openBlock {
	if refusal {
		c.refused = true
	}
	if b := c.open[key]; b != nil {
		return b
	}
	c.start(nil)
	return c.begin(key, "text", map[string]any{"type": "text", "text": ""})
}

func (c *converter) itemAdded(output int, item *outputItem) {
	if item == nil {
		return
	}
	key := itemKey(output)
	switch item.Type {
	case "reasoning":
		if c.tr.Thinking {
			c.begin(key, "thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
		}
	case "function_call":
		c.toolCalls = true
		c.begin(key, "tool_use", map[string]any{
			"type": "tool_use", "id": item.CallID, "name": c.toolName(item.Name), "input": map[string]any{},
		})
	}
}

// itemDone closes an item, sending whatever of it did not stream: a backend
// answering whole (sink.go, a JSON answer) sends only these.
func (c *converter) itemDone(output int, item *outputItem) {
	if item == nil {
		return
	}
	key := itemKey(output)
	switch item.Type {
	case "message":
		var text strings.Builder
		for _, p := range item.Content {
			text.WriteString(cmp.Or(p.Text, p.Refusal))
		}
		if text.Len() == 0 && c.said[output] != nil {
			text.WriteString(c.said[output].String())
		}
		c.keep(textKey(text.String()))
		for i, p := range item.Content {
			pk := partKey(output, i)
			if c.seen[pk] {
				c.stop(pk)
				continue
			}
			text := p.Text
			if p.Type == "refusal" {
				text = p.Refusal
			}
			if text == "" {
				continue
			}
			b := c.openText(pk, p.Type == "refusal")
			c.delta(b, map[string]any{"type": "text_delta", "text": text})
			c.stop(pk)
		}
		// A part the done item no longer lists is closed all the same.
		prefix := key + "/"
		for k := range c.open {
			if strings.HasPrefix(k, prefix) {
				c.stop(k)
			}
		}

	case "reasoning":
		var parts []string
		for _, s := range item.Summary {
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		}
		summary := strings.Join(parts, "\n\n")
		if item.EncryptedContent != "" {
			c.pending = append(c.pending, carriedReasoning{ID: item.ID, Encrypted: item.EncryptedContent, Summary: summary})
		}
		if !c.tr.Thinking {
			return
		}
		b := c.open[key]
		if b == nil {
			b = c.begin(key, "thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
		}
		if !b.streamed && !c.tr.Omitted && summary != "" {
			c.delta(b, map[string]any{"type": "thinking_delta", "thinking": summary})
		}
		if item.EncryptedContent != "" {
			c.delta(b, map[string]any{"type": "signature_delta", "signature": encodeSignature(item.ID, item.EncryptedContent)})
		}
		c.stop(key)

	case "function_call":
		c.toolCalls = true
		c.keep(callKey(item.CallID))
		b := c.open[key]
		if b == nil {
			b = c.begin(key, "tool_use", map[string]any{
				"type": "tool_use", "id": item.CallID, "name": c.toolName(item.Name), "input": map[string]any{},
			})
		}
		if !b.streamed && item.Arguments != "" {
			c.delta(b, map[string]any{"type": "input_json_delta", "partial_json": item.Arguments})
		}
		c.stop(key)

	case "web_search_call":
		c.webSearch(key, item)
	}
}

// keep files the reasoning since the last item under what it led to.
func (c *converter) keep(key string) {
	if len(c.pending) > 0 {
		c.tr.reasoning.put(c.tr.conversation, key, c.pending)
		c.pending = nil
	}
}

// webSearch says a search the backend ran as Anthropic says one: the call
// (server_tool_use) and what it found (web_search_tool_result). The backend
// lists the pages by URL alone (measured 2026-10-20), so a page's title is
// its URL when it gave none; there is no encrypted page content to carry,
// and a client sending this back is sending history the backend drops.
func (c *converter) webSearch(key string, item *outputItem) {
	c.searches++
	id := "srvtoolu_" + strings.TrimPrefix(item.ID, "ws_")
	query := ""
	var results []any
	if item.Action != nil {
		query = item.Action.Query
		for _, s := range item.Action.Sources {
			title := s.Title
			if title == "" {
				title = s.URL
			}
			results = append(results, map[string]any{
				"type": "web_search_result", "url": s.URL, "title": title, "encrypted_content": "", "page_age": nil,
			})
		}
	}
	if results == nil {
		results = []any{}
	}
	input, _ := json.Marshal(map[string]string{"query": query})
	b := c.begin(key+"/call", "server_tool_use", map[string]any{
		"type": "server_tool_use", "id": id, "name": "web_search", "input": map[string]any{},
	})
	c.delta(b, map[string]any{"type": "input_json_delta", "partial_json": string(input)})
	c.stop(key + "/call")
	c.begin(key+"/result", "web_search_tool_result", map[string]any{
		"type": "web_search_tool_result", "tool_use_id": id, "content": results,
	})
	c.stop(key + "/result")
}

// toolName is the caller's name for a function the backend called.
func (c *converter) toolName(up string) string {
	if own, ok := c.tr.Tools[up]; ok {
		return own
	}
	return up
}

// finish ends a turn the backend ended: the blocks still open closed, the
// stop reason and the usage said, message_stop.
//
// The stop reason, from what the turn did: a refusal part is refusal (and so
// is a stop for the content filter); a tool call is tool_use, which is what
// tells a Messages client to run it; running out of max_output_tokens is
// max_tokens (measured 2026-10-20: response.incomplete, reason
// max_output_tokens); anything else is end_turn.
func (c *converter) finish(r *responseObject, incomplete string) {
	c.start(r)
	c.closeAll()
	reason := "end_turn"
	switch {
	case c.refused || incomplete == "content_filter":
		reason = "refusal"
	case incomplete == "max_output_tokens":
		reason = "max_tokens"
	case c.toolCalls:
		reason = "tool_use"
	}
	usage := map[string]any{}
	if r != nil && r.Usage != nil {
		u := r.Usage
		cached := u.InputTokensDetails.CachedTokens
		// As the OpenAI meter normalises it (internal/provider/openai,
		// usageJSON.applyTo): OpenAI counts cached input inside
		// input_tokens, Anthropic beside it.
		usage["input_tokens"] = max(u.InputTokens-cached, 0)
		usage["cache_read_input_tokens"] = cached
		usage["cache_creation_input_tokens"] = u.InputTokensDetails.CacheWriteTokens
		usage["output_tokens"] = u.OutputTokens
	}
	if c.searches > 0 {
		usage["server_tool_use"] = map[string]any{"web_search_requests": c.searches}
	}
	c.out.emit(anthropicwire.EventMessageDelta, map[string]any{
		"type":  anthropicwire.EventMessageDelta,
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
		"usage": usage,
	})
	c.out.emit(anthropicwire.EventMessageStop, map[string]any{"type": anthropicwire.EventMessageStop})
	c.finished = true
}

// fail ends a turn with Anthropic's error event, which is how Anthropic
// itself ends a stream that fails after its 200.
func (c *converter) fail(kind, message string) {
	if c.finished {
		return
	}
	c.closeAll()
	c.out.emit(anthropicwire.EventError, map[string]any{
		"type":  anthropicwire.EventError,
		"error": map[string]string{"type": kind, "message": message},
	})
	c.finished = true
}

// closeAll stops every block still open, in the order they were opened.
func (c *converter) closeAll() {
	for len(c.open) > 0 {
		first, idx := "", -1
		for k, b := range c.open {
			if idx < 0 || b.index < idx {
				first, idx = k, b.index
			}
		}
		c.stop(first)
	}
}

// errorKind is the Anthropic error type for a backend error: by its code
// when it has one this knows, else by the status.
//
// The codes are the ones Codex acts on (internal/api/openai, doc.go) and the
// ones measured on 2026-10-20 (unsupported_value, integer_below_min_value);
// usage_limit_reached is the plan's limit (internal/provider/openai). A
// Messages client retries a rate_limit_error and an overloaded_error after a
// wait, and stops at the rest, so which is which decides whether it retries.
func errorKind(status int, code, typ string) string {
	for _, k := range []string{code, typ} {
		switch k {
		case "usage_limit_reached", "rate_limit_exceeded", "slow_down", "rate_limit_error", "insufficient_quota":
			return "rate_limit_error"
		case "server_is_overloaded", "overloaded", "overloaded_error":
			return "overloaded_error"
		case "context_length_exceeded", "invalid_prompt", "invalid_request_error", "unsupported_value",
			"unsupported_parameter", "integer_below_min_value", "invalid_value", "cyber_policy":
			return "invalid_request_error"
		case "usage_not_included", "permission_error":
			return "permission_error"
		case "authentication_error", "invalid_api_key":
			return "authentication_error"
		}
	}
	switch {
	case status == 400 || status == 422:
		return "invalid_request_error"
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 413:
		return "request_too_large"
	case status == 429:
		return "rate_limit_error"
	case status == 503 || status == 529:
		return "overloaded_error"
	}
	return "api_error"
}

// kindStatus is the status Anthropic answers an error type with, for a
// non-streaming caller whose answer failed after the backend's 200.
func kindStatus(kind string) int {
	switch kind {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "overloaded_error":
		return 529
	}
	return 500
}

// backendError reads a refusal's body. The backend answers in two shapes
// (measured 2026-10-20): {"detail":"Unsupported parameter: temperature"},
// and {"error":{"message","type","param","code"}}.
func backendError(status int, body []byte) (kind, message string) {
	var e struct {
		Detail json.RawMessage `json:"detail"`
		Error  *responsesError `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Error != nil && (e.Error.Message != "" || e.Error.Code != "" || e.Error.Type != "") {
			return errorKind(status, e.Error.Code, e.Error.Type),
				cmp.Or(e.Error.Message, e.Error.Code, e.Error.Type)
		}
		var detail string
		if json.Unmarshal(e.Detail, &detail) == nil && detail != "" {
			return errorKind(status, "", ""), detail
		}
		if len(e.Detail) > 0 && string(e.Detail) != "null" {
			return errorKind(status, "", ""), string(e.Detail)
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		if len(s) > 2000 {
			s = s[:2000]
		}
		return errorKind(status, "", ""), s
	}
	return errorKind(status, "", ""), "the ChatGPT backend refused the request without saying why"
}
