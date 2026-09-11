package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Options tunes the mapping where the two protocols do not agree.
type Options struct {
	// MaxTokens is what to ask for when the caller did not say. Anthropic
	// requires max_tokens and Codex sends no max_output_tokens at all, so
	// without a figure here every request would be rejected.
	MaxTokens int
	// Model is the Claude model to send when the caller asked for something
	// else. A caller that already names a Claude model keeps it, so an
	// operator who configured Codex with `model = "claude-opus-5"` stays in
	// control of what runs; a caller asking for gpt-5-codex gets this
	// instead, because that name means nothing upstream and the request would
	// simply be refused. Empty passes every model through untouched.
	Model string
}

// claudeModelPrefix marks a model name the upstream might actually know. It is
// a prefix test rather than a list because the list changes without us.
const claudeModelPrefix = "claude-"

// defaultMaxTokens is generous on purpose. It bounds one answer, not a budget:
// the account's own limits do that, and a ceiling low enough to truncate a
// long edit would look like the model stopping mid-thought.
const defaultMaxTokens = 32000

// ToolOrigin remembers where a flattened tool came from, so a call to it can
// be handed back in the shape Codex declared.
type ToolOrigin struct {
	// Namespace is the container tool this one was nested in.
	Namespace string
	// Name is what Codex called it, before we qualified it to break a
	// collision. Usually the same as the name we sent upstream.
	Name string
}

// Request is an Anthropic request built from a Responses one, together with
// what the response side needs to undo the mapping.
type Request struct {
	Body []byte
	// Tools maps a name we sent upstream back to how Codex declared it.
	// Anthropic has no nested tools, so namespaced ones go out flat; Codex
	// needs the namespace back on the call or it cannot route it. Only
	// entries that need undoing are present.
	Tools  map[string]ToolOrigin
	Model  string
	Stream bool
}

// responsesRequest is the part of a Responses request that maps onto
// Anthropic. Fields absent here are dropped deliberately — see the package doc
// for which and why.
type responsesRequest struct {
	Model             string          `json:"model"`
	Instructions      string          `json:"instructions"`
	Input             []responsesItem `json:"input"`
	Tools             []responsesTool `json:"tools"`
	ToolChoice        json.RawMessage `json:"tool_choice"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls"`
	MaxOutputTokens   *int            `json:"max_output_tokens"`
	Temperature       *float64        `json:"temperature"`
	Stream            bool            `json:"stream"`
}

type responsesItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content []responsesPart `json:"content"`
	// A function_call, as Codex sends it back on the next turn.
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	Namespace string `json:"namespace"`
	// A function_call_output.
	Output json.RawMessage `json:"output"`
}

type responsesPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// Only on a namespace tool: the tools it contains.
	Tools []responsesTool `json:"tools"`
}

// ResponsesToAnthropic converts one Codex request into one Anthropic request.
//
// What it does not do is add Claude Code's attribution block, normalise the
// system text or rewrite refused tool names. Those happen in internal/upstream
// on the way out, and they apply to this request exactly as to a relayed one —
// a synthesised request is still a request the subscription backend judges.
func ResponsesToAnthropic(body []byte, opts Options) (Request, error) {
	var in responsesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return Request{}, fmt.Errorf("not a Responses request: %w", err)
	}

	out := map[string]any{}

	model := in.Model
	if opts.Model != "" && !strings.HasPrefix(model, claudeModelPrefix) {
		model = opts.Model
	}
	if model == "" {
		return Request{}, fmt.Errorf("no model in the request and none configured")
	}
	out["model"] = model

	// Anthropic requires this and Codex never sends it.
	maxTokens := opts.MaxTokens
	if in.MaxOutputTokens != nil && *in.MaxOutputTokens > 0 {
		maxTokens = *in.MaxOutputTokens
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	out["max_tokens"] = maxTokens

	if in.Temperature != nil {
		out["temperature"] = *in.Temperature
	}
	if in.Stream {
		out["stream"] = true
	}

	system, messages := convertInput(in.Instructions, in.Input)
	if system != "" {
		// A single text block rather than the string form: EnsureAttribution
		// prepends to a block list, and giving it one already saves it the
		// conversion.
		out["system"] = []map[string]any{{"type": "text", "text": system}}
	}
	out["messages"] = messages

	tools, origins := convertTools(in.Tools)
	if len(tools) > 0 {
		out["tools"] = tools
		if choice := convertToolChoice(in.ToolChoice, in.ParallelToolCalls); choice != nil {
			out["tool_choice"] = choice
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return Request{}, fmt.Errorf("encode the Anthropic request: %w", err)
	}
	return Request{
		Body:   encoded,
		Tools:  origins,
		Model:  model,
		Stream: in.Stream,
	}, nil
}

// convertInput folds the instructions and any developer turns into one system
// string, and maps the rest onto Anthropic messages.
//
// `developer` is a role Anthropic does not have. Folding it into the system
// prompt is the reading that loses least: it is instruction rather than
// conversation, and the alternative — a user turn — would put it in the
// transcript as something the user said.
func convertInput(instructions string, items []responsesItem) (string, []map[string]any) {
	system := []string{}
	if strings.TrimSpace(instructions) != "" {
		system = append(system, instructions)
	}
	messages := []map[string]any{}

	// Consecutive blocks of the same role are merged, because Anthropic
	// rejects two user turns in a row and Codex sends them routinely — the
	// environment context and the prompt itself arrive as separate items.
	appendBlock := func(role string, block map[string]any) {
		if n := len(messages); n > 0 && messages[n-1]["role"] == role {
			content := messages[n-1]["content"].([]map[string]any)
			messages[n-1]["content"] = append(content, block)
			return
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": []map[string]any{block},
		})
	}

	for _, item := range items {
		switch item.Type {
		case "message", "":
			if item.Role == "developer" || item.Role == "system" {
				for _, part := range item.Content {
					if part.Text != "" {
						system = append(system, part.Text)
					}
				}
				continue
			}
			role := "user"
			if item.Role == "assistant" {
				role = "assistant"
			}
			for _, part := range item.Content {
				// input_text and output_text are the same thing to Anthropic;
				// anything else — an image, a file — is not handled yet and is
				// dropped rather than sent as something it is not.
				if part.Text == "" {
					continue
				}
				appendBlock(role, map[string]any{"type": "text", "text": part.Text})
			}

		case "function_call":
			// The model's own previous call, coming back as history. Arguments
			// are a JSON string here and an object in Anthropic.
			var input any = map[string]any{}
			if item.Arguments != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
					input = map[string]any{}
				}
			}
			appendBlock("assistant", map[string]any{
				"type":  "tool_use",
				"id":    item.CallID,
				"name":  item.Name,
				"input": input,
			})

		case "function_call_output":
			appendBlock("user", map[string]any{
				"type":        "tool_result",
				"tool_use_id": item.CallID,
				"content":     outputText(item.Output),
			})

		default:
			// reasoning, and anything a later Codex adds. Dropped: a block we
			// do not understand is worse sent than omitted, because Anthropic
			// would reject the whole request over it.
		}
	}
	return strings.Join(system, "\n\n"), messages
}

// outputText reads a function_call_output's payload, which Codex sends either
// as a bare string or as a structure with the text inside it.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var wrapped struct {
		Output  string `json:"output"`
		Content string `json:"content"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		for _, s := range []string{wrapped.Output, wrapped.Content, wrapped.Text} {
			if s != "" {
				return s
			}
		}
	}
	// Something else entirely: hand it over as JSON rather than lose it.
	return string(raw)
}

// convertTools flattens the three shapes Codex sends into the one Anthropic
// takes, and reports how to put each flattened name back.
func convertTools(tools []responsesTool) ([]map[string]any, map[string]ToolOrigin) {
	out := []map[string]any{}
	origins := map[string]ToolOrigin{}
	taken := map[string]bool{}

	add := func(t responsesTool, namespace string) {
		if t.Name == "" {
			return
		}
		name := t.Name
		// A child name that collides with a tool already declared would make
		// two different tools one, and the namespace we put back on the way
		// home would be the wrong one. Qualify rather than merge.
		if taken[name] && namespace != "" {
			name = namespace + "__" + t.Name
		}
		if taken[name] {
			return
		}
		taken[name] = true

		schema := t.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		entry := map[string]any{
			"name":         name,
			"input_schema": json.RawMessage(schema),
		}
		if t.Description != "" {
			entry["description"] = t.Description
		}
		out = append(out, entry)
		if namespace != "" || name != t.Name {
			origins[name] = ToolOrigin{Namespace: namespace, Name: t.Name}
		}
	}

	for _, t := range tools {
		switch t.Type {
		case "function", "":
			add(t, "")
		case "namespace":
			for _, child := range t.Tools {
				add(child, t.Name)
			}
		default:
			// web_search and anything like it: a server-side tool with no
			// schema. It cannot become a function, and inventing one would
			// offer the model a tool that does nothing.
		}
	}
	return out, origins
}

// convertToolChoice maps the choice and the parallel flag, which Anthropic
// carries in the same object.
func convertToolChoice(raw json.RawMessage, parallel *bool) map[string]any {
	choice := map[string]any{"type": "auto"}

	if len(raw) > 0 {
		var name string
		if err := json.Unmarshal(raw, &name); err == nil {
			switch name {
			case "auto":
				choice = map[string]any{"type": "auto"}
			case "none":
				choice = map[string]any{"type": "none"}
			case "required":
				choice = map[string]any{"type": "any"}
			}
		} else {
			var named struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &named); err == nil && named.Name != "" {
				choice = map[string]any{"type": "tool", "name": named.Name}
			}
		}
	}

	// Inverted, and only when false: Anthropic's flag turns parallel use off,
	// where the Responses one turns it on.
	if parallel != nil && !*parallel {
		choice["disable_parallel_tool_use"] = true
	}
	return choice
}
