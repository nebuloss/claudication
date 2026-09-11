package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicMessage is a complete (non-streaming) Anthropic answer.
type anthropicMessage struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		CacheReadInput     int `json:"cache_read_input_tokens"`
		CacheCreationInput int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// AnthropicToResponses converts a complete Anthropic answer into a complete
// Responses one.
//
// Codex never takes this path — it always streams — so this exists for the
// other OpenAI clients that point at /v1/responses with stream:false, and
// because a protocol we only half-implement is a protocol that fails in a way
// nobody can diagnose.
func AnthropicToResponses(body []byte, req Request) ([]byte, error) {
	var in anthropicMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("not an Anthropic message: %w", err)
	}

	s := &Stream{req: req, model: in.Model}
	if s.model == "" {
		s.model = req.Model
	}
	s.respID = "resp_" + strings.TrimPrefix(in.ID, "msg_")

	output := []json.RawMessage{}
	for i, block := range in.Content {
		var item map[string]any
		switch block.Type {
		case "tool_use":
			s.toolName = block.Name
			s.toolID = block.ID
			s.itemID = "fc_" + strings.TrimPrefix(block.ID, "toolu_")
			args := "{}"
			if len(block.Input) > 0 {
				// Anthropic's input is an object and the Responses one is a
				// string holding that object's JSON.
				args = string(block.Input)
			}
			item = s.functionCallItem("completed", args)
		case "thinking", "redacted_thinking":
			s.itemID = fmt.Sprintf("rs_%s_%d", s.respID, i)
			item = s.reasoningItem("completed", block.Thinking)
		case "text":
			s.itemID = fmt.Sprintf("msg_%s_%d", s.respID, i)
			item = s.messageItem("completed", block.Text)
		default:
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		output = append(output, raw)
	}
	s.items = output

	usage := responsesUsage{
		InputTokens:        in.Usage.InputTokens + in.Usage.CacheReadInput + in.Usage.CacheCreationInput,
		InputTokensDetails: inputDetails{CachedTokens: in.Usage.CacheReadInput},
		OutputTokens:       in.Usage.OutputTokens,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens

	status := "completed"
	var incomplete map[string]any
	if in.StopReason == "max_tokens" {
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	}
	return json.Marshal(s.responseWith(status, &usage, nil, incomplete))
}

// ErrorEnvelope renders a failure as an OpenAI error body, for every path that
// answers with a status rather than a stream: a refusal forwarded from the
// upstream, a surface that is switched off, a body that would not parse.
func ErrorEnvelope(kind, message string) []byte {
	code, msg := mapError(kind, message)
	body, _ := json.Marshal(map[string]any{
		"error": responsesError{Code: code, Message: msg},
	})
	return body
}
