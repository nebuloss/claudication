package gemini

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"claudication/internal/geminiwire"
)

// The request half of Gemini → Messages: a Gemini API request rebuilt as the
// Messages body Claude takes. Read from @google/genai's types and from what
// gemini-cli puts in them; not measured.
//
//	Gemini                            Messages
//	systemInstruction                 system, its texts joined
//	contents, role user / model       messages, role user / assistant,
//	                                    a run of one role merged
//	text                              text
//	thought part                      dropped, unless it carries a thinking
//	                                    block this gateway gave it (below)
//	inlineData image/* / PDF          image / document, base64
//	fileData                          image / document, url
//	functionCall                      tool_use, its id the call's or made
//	                                    up and matched to its response by
//	                                    name and order
//	functionResponse {output}/{error} tool_result, is_error for {error}
//	functionDeclarations              tools: parametersJsonSchema as
//	                                    input_schema, parameters' OpenAPI
//	                                    types lowered to JSON Schema's
//	googleSearch                      web_search
//	toolConfig AUTO / ANY / NONE      tool_choice auto / any (tool, for one
//	                                    name) / none
//	generationConfig                  max_tokens (8192 when unset: Messages
//	                                    requires one), temperature, top_p,
//	                                    top_k, stop_sequences
//	thinkingConfig budget / level     thinking enabled with a budget
//	responseJsonSchema / Schema       output_config.format json_schema
//
// A Claude thinking block has no Gemini counterpart a client keeps, but a
// thought signature is one: gemini-cli keeps every part's thoughtSignature in
// its history and sends it back. So a thinking block — text and signature —
// goes to the client inside the thoughtSignature of the part that follows it
// (frommessages.go), and comes back from there as the block it was. Claude
// needs exactly that: with thinking on, an assistant turn that called a tool
// must start with its thinking block, signed.

// thinkingPrefix starts a thoughtSignature that carries a Claude thinking
// block. Gemini's own signatures are base64, which holds no colon.
const thinkingPrefix = "claudication:anthropic-thinking:v1:"

// carriedThinking is a thinking block as a signature holds it.
type carriedThinking struct {
	Thinking  string `json:"t,omitempty"`
	Signature string `json:"s,omitempty"`
	// Redacted holds a redacted_thinking block's data instead.
	Redacted string `json:"r,omitempty"`
}

func encodeThinking(c carriedThinking) string {
	raw, _ := json.Marshal(c)
	return thinkingPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func decodeThinking(sig string) (carriedThinking, bool) {
	rest, ok := strings.CutPrefix(sig, thinkingPrefix)
	if !ok {
		return carriedThinking{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return carriedThinking{}, false
	}
	var c carriedThinking
	if json.Unmarshal(raw, &c) != nil || (c.Signature == "" && c.Redacted == "") {
		return carriedThinking{}, false
	}
	return c, true
}

// defaultMaxTokens is the output budget of a request that names none.
// Messages requires one and the Gemini API does not; gemini-cli sets none
// unless configured to. The gateway lowers it to a slot's model's cap.
const defaultMaxTokens = 8192

// translation is a Gemini request rebuilt for Messages, with what the answer
// side needs to say it back.
type translation struct {
	body   []byte
	model  string
	stream bool
	// thoughts is whether the caller asked to be shown thinking
	// (includeThoughts).
	thoughts bool
	// tools maps a name sent to Claude back to the caller's, for the names
	// that had to change.
	tools map[string]string
	// estimate is the input as countTokens estimates it.
	estimate int
}

// request is the part of a Gemini request that has a meaning on Messages.
type request struct {
	Contents          []geminiwire.Content   `json:"contents"`
	SystemInstruction *geminiwire.Content    `json:"systemInstruction"`
	Tools             []tool                 `json:"tools"`
	ToolConfig        *geminiwire.ToolConfig `json:"toolConfig"`
	GenerationConfig  *generationConfig      `json:"generationConfig"`
}

// tool is a Gemini tool in the fields read: declarations in either schema
// field, and the backend tools.
type tool struct {
	FunctionDeclarations []declaration   `json:"functionDeclarations"`
	GoogleSearch         json.RawMessage `json:"googleSearch"`
	CodeExecution        json.RawMessage `json:"codeExecution"`
	URLContext           json.RawMessage `json:"urlContext"`
}

type declaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
	Parameters           json.RawMessage `json:"parameters"`
}

type generationConfig struct {
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"topP"`
	TopK               *float64        `json:"topK"`
	MaxOutputTokens    int             `json:"maxOutputTokens"`
	StopSequences      []string        `json:"stopSequences"`
	ResponseMimeType   string          `json:"responseMimeType"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
	ResponseSchema     json.RawMessage `json:"responseSchema"`
	ThinkingConfig     *struct {
		IncludeThoughts bool   `json:"includeThoughts"`
		ThinkingBudget  *int   `json:"thinkingBudget"`
		ThinkingLevel   string `json:"thinkingLevel"`
	} `json:"thinkingConfig"`
}

// messages is the Messages body sent.
type messages struct {
	Model         string         `json:"model"`
	MaxTokens     int            `json:"max_tokens"`
	System        string         `json:"system,omitempty"`
	Messages      []message      `json:"messages"`
	Tools         []any          `json:"tools,omitempty"`
	ToolChoice    map[string]any `json:"tool_choice,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	TopK          *float64       `json:"top_k,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
	Thinking      map[string]any `json:"thinking,omitempty"`
	OutputConfig  map[string]any `json:"output_config,omitempty"`
	Stream        bool           `json:"stream"`
}

type message struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

// refusal is a request Claude cannot be sent, said for the person reading
// the client's log.
type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refuse(format string, args ...any) error { return refusal{fmt.Sprintf(format, args...)} }

func toMessages(e envelope) (translation, error) {
	var in request
	if err := json.Unmarshal(e.Request, &in); err != nil {
		return translation{}, fmt.Errorf("not a Gemini request: %w", err)
	}
	tr := translation{model: e.Model, stream: e.Method == "streamGenerateContent", tools: map[string]string{}}
	out := messages{Model: e.Model, Stream: tr.stream, MaxTokens: defaultMaxTokens}

	if si := in.SystemInstruction; si != nil {
		var texts []string
		for _, p := range si.Parts {
			if strings.TrimSpace(p.Text) != "" {
				texts = append(texts, p.Text)
			}
		}
		out.System = strings.Join(texts, "\n\n")
	}

	names := map[string]string{}
	search := false
	for _, t := range in.Tools {
		for _, d := range t.FunctionDeclarations {
			up := claudeToolName(d.Name)
			if other, taken := names[up]; taken && other != d.Name {
				return translation{}, refuse("tools %q and %q map to the same name on Claude", other, d.Name)
			}
			names[up] = d.Name
			schema := d.ParametersJSONSchema
			if len(schema) == 0 || string(schema) == "null" {
				schema = lowerSchema(d.Parameters)
			}
			if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			def := map[string]any{"name": up, "input_schema": schema}
			if d.Description != "" {
				def["description"] = d.Description
			}
			out.Tools = append(out.Tools, def)
		}
		if len(t.GoogleSearch) > 0 {
			search = true
		}
	}
	if search {
		out.Tools = append(out.Tools, map[string]any{"type": "web_search_20250305", "name": "web_search"})
	}
	for up, own := range names {
		if up != own {
			tr.tools[up] = own
		}
	}
	if len(out.Tools) > 0 {
		out.ToolChoice = toolChoice(in.ToolConfig)
	}

	msgs, carriedAll, err := convertContents(in.Contents)
	if err != nil {
		return translation{}, err
	}
	if len(msgs) == 0 && e.Method != "countTokens" {
		return translation{}, refuse("the request has no contents")
	}
	out.Messages = msgs

	if g := in.GenerationConfig; g != nil {
		if g.MaxOutputTokens > 0 {
			out.MaxTokens = g.MaxOutputTokens
		}
		out.Temperature, out.TopP, out.TopK, out.StopSequences = g.Temperature, g.TopP, g.TopK, g.StopSequences
		if tc := g.ThinkingConfig; tc != nil {
			tr.thoughts = tc.IncludeThoughts
			budget := 0
			switch {
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0:
				budget = *tc.ThinkingBudget
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget < 0, strings.EqualFold(tc.ThinkingLevel, "high"):
				budget = 16384
			case strings.EqualFold(tc.ThinkingLevel, "low"), strings.EqualFold(tc.ThinkingLevel, "medium"),
				strings.EqualFold(tc.ThinkingLevel, "minimal"):
				budget = 1024
			}
			// Claude refuses thinking on a turn whose earlier tool calls came
			// back without their thinking blocks; such history is left to
			// run without it rather than refused.
			if budget > 0 && carriedAll {
				out.Thinking = map[string]any{"type": "enabled", "budget_tokens": max(budget, 1024)}
				// The budget must be below max_tokens, and sampling is
				// Claude's own while it thinks.
				out.MaxTokens = max(out.MaxTokens, budget+1024)
				out.Temperature, out.TopP, out.TopK = nil, nil, nil
			}
		}
		schema := g.ResponseJSONSchema
		if len(schema) == 0 || string(schema) == "null" {
			schema = lowerSchema(g.ResponseSchema)
		}
		if len(schema) > 0 && string(schema) != "null" {
			out.OutputConfig = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}
		}
	}

	tr.estimate = estimate(out)
	body, err := json.Marshal(out)
	if err != nil {
		return translation{}, fmt.Errorf("encode the Messages request: %w", err)
	}
	tr.body = body
	return tr, nil
}

// claudeNameChars are what a Claude tool name may not hold: it is
// ^[a-zA-Z0-9_-]{1,128}$, and Gemini's allows dots and colons.
var claudeNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// claudeToolName is a declaration's name as Claude takes it, unchanged when
// it fits; else with what does not fit replaced, and a hash of the original
// so two names that become alike stay two.
func claudeToolName(name string) string {
	up := claudeNameChars.ReplaceAllString(name, "_")
	if up == name && len(name) <= 128 && name != "" {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return up[:min(len(up), 119)] + "_" + hex.EncodeToString(sum[:4])
}

// toolChoice maps functionCallingConfig.
func toolChoice(c *geminiwire.ToolConfig) map[string]any {
	if c == nil || c.FunctionCallingConfig == nil {
		return nil
	}
	fc := c.FunctionCallingConfig
	switch strings.ToUpper(fc.Mode) {
	case "ANY", "VALIDATED":
		if len(fc.AllowedFunctionNames) == 1 {
			return map[string]any{"type": "tool", "name": claudeToolName(fc.AllowedFunctionNames[0])}
		}
		return map[string]any{"type": "any"}
	case "NONE":
		return map[string]any{"type": "none"}
	}
	return map[string]any{"type": "auto"}
}

// lowerSchema turns a Gemini Schema — the OpenAPI subset, its types written
// STRING, OBJECT — into JSON Schema, which writes them in lower case. Every
// other keyword is spelled the same in both.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if k == "type" {
					if s, ok := e.(string); ok {
						x[k] = strings.ToLower(s)
						continue
					}
				}
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return out
}

// convertContents maps the turns. carriedAll is false when an assistant turn
// that called a tool came back without the thinking block it was given,
// which a thinking request to Claude would be refused for.
func convertContents(contents []geminiwire.Content) ([]message, bool, error) {
	var out []message
	carriedAll := true
	// The calls of the last model turn not yet answered, by name, in order:
	// a response with no id is matched to the first of its name.
	type call struct{ id, name string }
	var pending []call
	ids := idMap{}
	add := func(role string, blocks []map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, message{Role: role, Content: blocks})
	}
	for i, c := range contents {
		switch c.Role {
		case "model":
			var blocks []map[string]any
			pending = pending[:0]
			hasCall, hasThinking := false, false
			n := 0
			for _, p := range c.Parts {
				if t, ok := decodeThinking(p.ThoughtSignature); ok {
					hasThinking = true
					if t.Redacted != "" {
						blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": t.Redacted})
					} else {
						blocks = append(blocks, map[string]any{"type": "thinking", "thinking": t.Thinking, "signature": t.Signature})
					}
				}
				switch {
				case p.FunctionCall != nil:
					hasCall = true
					n++
					id := ids.claude(p.FunctionCall.ID, fmt.Sprintf("toolu_gem_%d_%d", i, n))
					pending = append(pending, call{id: id, name: p.FunctionCall.Name})
					var input any = map[string]any{}
					if len(p.FunctionCall.Args) > 0 && string(p.FunctionCall.Args) != "null" {
						input = p.FunctionCall.Args
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_use", "id": id, "name": claudeToolName(p.FunctionCall.Name), "input": input,
					})
				case p.Thought:
				case strings.TrimSpace(p.Text) != "":
					blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
				}
			}
			if hasCall && !hasThinking {
				carriedAll = false
			}
			add("assistant", blocks)
		case "user", "":
			var results, rest []map[string]any
			for _, p := range c.Parts {
				switch {
				case p.FunctionResponse != nil:
					fr := p.FunctionResponse
					// By id when both sides have one, else the first
					// unanswered call of the name.
					match := -1
					if fr.ID != "" {
						want := ids.claude(fr.ID, "")
						for j, pc := range pending {
							if pc.id == want {
								match = j
								break
							}
						}
					}
					if match < 0 {
						for j, pc := range pending {
							if pc.name == fr.Name {
								match = j
								break
							}
						}
					}
					id := ""
					if match >= 0 {
						id = pending[match].id
						pending = append(pending[:match], pending[match+1:]...)
					}
					if id == "" {
						return nil, false, refuse("contents[%d] answers a function call %q that the turn before did not make", i, fr.Name)
					}
					text, isErr := responseText(fr.Response)
					b := map[string]any{"type": "tool_result", "tool_use_id": id, "content": text}
					if isErr {
						b["is_error"] = true
					}
					results = append(results, b)
				default:
					b, err := userBlock(p)
					if err != nil {
						return nil, false, fmt.Errorf("contents[%d]: %w", i, err)
					}
					if b != nil {
						rest = append(rest, b)
					}
				}
			}
			// Claude takes a turn's tool results first.
			add("user", append(results, rest...))
		default:
			return nil, false, refuse("contents[%d] has role %q, which the Gemini API does not have", i, c.Role)
		}
	}
	// Claude's first turn is the user's.
	if len(out) > 0 && out[0].Role != "user" {
		out = append([]message{{Role: "user", Content: []map[string]any{{"type": "text", "text": "Continue."}}}}, out...)
	}
	return out, carriedAll, nil
}

// idMap gives a Gemini call id a Claude tool_use id: the same id when it
// fits Claude's pattern, else a stable one made from it.
type idMap map[string]string

func (m idMap) claude(geminiID, fallback string) string {
	if geminiID == "" {
		return fallback
	}
	if id, ok := m[geminiID]; ok {
		return id
	}
	id := geminiID
	if claudeNameChars.MatchString(id) {
		sum := sha256.Sum256([]byte(geminiID))
		id = "toolu_gem_" + hex.EncodeToString(sum[:8])
	}
	m[geminiID] = id
	return id
}

// responseText is a function response as tool_result text: gemini-cli's
// {"output": …} as the text, {"error": …} as an error, anything else as the
// JSON it is.
func responseText(raw json.RawMessage) (string, bool) {
	var r map[string]json.RawMessage
	if json.Unmarshal(raw, &r) == nil {
		for _, key := range []string{"error", "output"} {
			v, ok := r[key]
			if !ok || len(r) != 1 {
				continue
			}
			var s string
			if json.Unmarshal(v, &s) != nil {
				s = string(v)
			}
			return s, key == "error"
		}
	}
	return string(raw), false
}

// userBlock maps one part of a user turn.
func userBlock(p geminiwire.Part) (map[string]any, error) {
	switch {
	case p.Thought:
		return nil, nil
	case p.InlineData != nil:
		return media(p.InlineData.MimeType, map[string]any{
			"type": "base64", "media_type": p.InlineData.MimeType, "data": p.InlineData.Data,
		})
	case p.FileData != nil:
		return media(p.FileData.MimeType, map[string]any{"type": "url", "url": p.FileData.FileURI})
	case strings.TrimSpace(p.Text) != "":
		return map[string]any{"type": "text", "text": p.Text}, nil
	}
	return nil, nil
}

// media is an image or a PDF; Claude takes no other kind of file.
func media(mime string, source map[string]any) (map[string]any, error) {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return map[string]any{"type": "image", "source": source}, nil
	case mime == "application/pdf":
		return map[string]any{"type": "document", "source": source}, nil
	case strings.HasPrefix(mime, "text/") && source["type"] == "base64":
		data, err := base64.StdEncoding.DecodeString(source["data"].(string))
		if err != nil {
			return nil, refuse("a %s part is not base64", mime)
		}
		return map[string]any{"type": "document", "source": map[string]any{
			"type": "text", "media_type": "text/plain", "data": string(data),
		}}, nil
	}
	return nil, refuse("a %q part has no equivalent on Claude, which takes images, PDFs and text", mime)
}

// The countTokens estimate's rule, the Anthropic surface's: the text the
// model reads at four bytes a token, a picture at a flat 1,600.
const (
	bytesPerToken  = 4
	tokensPerImage = 1600
)

func estimate(m messages) int {
	n := len(m.System)
	media := 0
	for _, msg := range m.Messages {
		for _, b := range msg.Content {
			switch b["type"] {
			case "image", "document":
				media += tokensPerImage
			case "text":
				s, _ := b["text"].(string)
				n += len(s)
			case "tool_result":
				s, _ := b["content"].(string)
				n += len(s)
			case "tool_use":
				raw, _ := json.Marshal(b["input"])
				n += len(raw)
			}
		}
	}
	for _, t := range m.Tools {
		raw, _ := json.Marshal(t)
		n += len(raw)
	}
	return (n+bytesPerToken-1)/bytesPerToken + media
}
