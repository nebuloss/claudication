package anthropic

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"claudication/internal/api"
	"claudication/internal/geminiwire"
)

// The request half of Messages → Gemini: a Messages body rebuilt as the Code
// Assist envelope a Google account's Gemini takes (internal/provider/google).
// The envelope's project is left out; the provider stamps it per attempt.
//
// Nothing here is measured yet. The shapes are gemini-cli's — what it sends
// is what the backend is known to take — and the choices are noted where
// they are made, to be checked against a live account:
//
//	Messages                         Gemini
//	system (string or blocks)        systemInstruction, the texts joined
//	user / assistant turn            content, role user / model
//	system turn inside messages      a user turn: Gemini has no other role
//	text                             {text}
//	image, base64 / url              inlineData / fileData
//	document, PDF / text             inlineData application/pdf / {text}
//	tool_use                         functionCall {id, name, args}
//	tool_result                      functionResponse {id, name,
//	                                   response: {output} or {error}},
//	                                   gemini-cli's own shape
//	thinking carrying our signature  the signature, on the call it led to
//	other thinking, server tools     dropped: Anthropic's own history
//	tools (custom)                   functionDeclarations, the schema as
//	                                   parametersJsonSchema, as gemini-cli
//	                                   declares its own and MCP tools
//	web_search                       googleSearch, when no function is offered
//	tool_choice auto/any/none/tool   AUTO / ANY / NONE / ANY + the one name
//	max_tokens                       maxOutputTokens
//	temperature, top_p, top_k,
//	  stop_sequences                 the same in generationConfig
//	thinking, output_config.effort   thinkingConfig.thinkingBudget
//	output_config.format             responseMimeType + responseJsonSchema

// geminiDialect is the dialect Code Assist is reached in: internal/api/gemini's
// ID, spelled here because this package may not import that one.
const geminiDialect = "gemini"

// sampling are the Messages fields that have a Gemini meaning and no
// Responses one, so messagesRequest does not carry them.
type sampling struct {
	Temperature   *float64 `json:"temperature"`
	TopP          *float64 `json:"top_p"`
	TopK          *float64 `json:"top_k"`
	StopSequences []string `json:"stop_sequences"`
}

// syntheticSignature is what a function call is sent with when its real
// signature is not known. Gemini 3 refuses a model turn whose first call has
// none; gemini-cli puts this on its own history for the same reason
// (core/geminiChat.ts SYNTHETIC_THOUGHT_SIGNATURE,
// ensureActiveLoopHasThoughtSignatures).
const syntheticSignature = "skip_thought_signature_validator"

// geminiSignaturePrefix starts a thinking block's signature that carries a
// Gemini thought signature, as reasoning.go's prefix does an OpenAI
// reasoning item. Neither Anthropic's base64 signatures nor OpenAI's carry
// one can be taken for this.
const geminiSignaturePrefix = "claudication:gemini-thought:v1:"

func encodeGeminiSignature(sig string) string { return geminiSignaturePrefix + sig }

func decodeGeminiSignature(sig string) (string, bool) {
	rest, ok := strings.CutPrefix(sig, geminiSignaturePrefix)
	return rest, ok && rest != ""
}

// generatedCallPrefix starts a tool_use id the gateway made up for a call
// Gemini gave no id. A call carrying one goes back to Gemini without an id —
// it never had one — and is matched to its result by name.
const generatedCallPrefix = "toolu_gw_"

func newCallID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return generatedCallPrefix + hex.EncodeToString(b[:])
}

// geminiCallID is the id a call goes back to Gemini with: its own, or none
// for one the gateway named.
func geminiCallID(id string) string {
	if strings.HasPrefix(id, generatedCallPrefix) {
		return ""
	}
	return id
}

// translateGemini rebuilds a Messages request for Code Assist.
func translateGemini(body []byte, o options) (Translation, error) {
	b := builder{o}
	var in messagesRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return Translation{}, fmt.Errorf("not a Messages request: %w", err)
	}
	if in.Model == "" {
		return Translation{}, fmt.Errorf("no model in the request")
	}
	if !o.count && len(in.MCPServers) > 0 && string(in.MCPServers) != "null" && string(in.MCPServers) != "[]" {
		return Translation{}, refuse("mcp_servers (Anthropic's MCP connector) has no equivalent on Gemini; " +
			"connect the MCP server to the client itself")
	}
	var smp sampling
	_ = json.Unmarshal(body, &smp)

	out := geminiwire.Request{
		Model: in.Model,
		Request: geminiwire.GenerateRequest{
			SessionID: api.CleanIdentifier(o.conversation),
		},
	}
	tr := Translation{Model: in.Model, Stream: in.Stream, conversation: o.conversation, reasoning: o.reasoning}

	system, err := systemText(in.System)
	if err != nil {
		return Translation{}, err
	}
	if system != "" {
		out.Request.SystemInstruction = &geminiwire.Content{Parts: []geminiwire.Part{{Text: system}}}
	}

	loaded := loadedTools(in.Messages)
	names := map[string]string{}
	var decls []geminiwire.FunctionDeclaration
	search := false
	for _, t := range in.Tools {
		d, isSearch, err := geminiTool(t, loaded, names)
		if b.skip(err) {
			continue
		}
		if err != nil {
			return Translation{}, err
		}
		search = search || isSearch
		if d != nil {
			decls = append(decls, *d)
		}
	}
	tr.Tools = renamed(names)
	switch {
	case len(decls) > 0:
		// Search beside functions is left out: Gemini has refused the two
		// together on the models it was documented for, and a turn that is
		// refused serves nobody. Not measured on Code Assist.
		out.Request.Tools = []geminiwire.Tool{{FunctionDeclarations: decls}}
		out.Request.ToolConfig = geminiToolChoice(in.ToolChoice)
	case search:
		out.Request.Tools = []geminiwire.Tool{{GoogleSearch: &struct{}{}}}
	}

	contents, err := b.geminiContents(in.Messages)
	if err != nil {
		return Translation{}, err
	}
	if len(contents) == 0 && !o.count {
		return Translation{}, refuse("the request has no messages")
	}
	out.Request.Contents = contents

	gc := geminiwire.GenerationConfig{
		Temperature:     smp.Temperature,
		TopP:            smp.TopP,
		TopK:            smp.TopK,
		StopSequences:   smp.StopSequences,
		MaxOutputTokens: in.MaxTokens,
	}
	gc.ThinkingConfig, tr.Thinking, tr.Omitted = geminiThinking(in.Thinking, in.OutputConfig)

	format := in.OutputFormat
	if in.OutputConfig != nil && len(in.OutputConfig.Format) > 0 {
		format = in.OutputConfig.Format
	}
	if schema, err := geminiFormat(format); err != nil && !b.skip(err) {
		return Translation{}, err
	} else if schema != nil {
		gc.ResponseMimeType, gc.ResponseSchema = "application/json", schema
	}
	out.Request.GenerationConfig = &gc

	if o.count {
		tr.Estimate = estimateGemini(out)
		return tr, nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return Translation{}, fmt.Errorf("encode the Gemini request: %w", err)
	}
	tr.Body = encoded
	return tr, nil
}

// renamed is the names that had to change, upstream name to the caller's, or
// nil when none did.
func renamed(names map[string]string) map[string]string {
	var out map[string]string
	for up, own := range names {
		if up != own {
			if out == nil {
				out = map[string]string{}
			}
			out[up] = own
		}
	}
	return out
}

// geminiNameChars are the characters a Gemini function name may hold, and
// geminiNameStart the ones it may begin with: a letter or an underscore,
// then letters, digits, underscores, dots, colons and dashes, at most 64.
var (
	geminiNameChars = regexp.MustCompile(`[^a-zA-Z0-9_.:-]`)
	geminiNameStart = regexp.MustCompile(`^[a-zA-Z_]`)
)

// geminiToolName is a tool's name as Gemini takes it: unchanged when it
// fits, else with what does not fit replaced and, past 64, cut and hashed
// the way upstreamToolName does, so the same name always maps the same way.
func geminiToolName(name string) string {
	up := geminiNameChars.ReplaceAllString(name, "_")
	if !geminiNameStart.MatchString(up) {
		up = "_" + up
	}
	if up == name {
		return upstreamToolName(name)
	}
	// Changed: given a hash of the original, so two names that sanitise
	// alike stay two names.
	sum := sha256.Sum256([]byte(name))
	return up[:min(len(up), maxToolName-9)] + "_" + hex.EncodeToString(sum[:4])
}

// geminiTool maps one tool: a declaration, the search flag, or nothing for a
// tool not offered now.
func geminiTool(t tool, loaded map[string]bool, names map[string]string) (*geminiwire.FunctionDeclaration, bool, error) {
	switch {
	case t.Type == "" || t.Type == "custom":
		if t.DeferLoading && !loaded[t.Name] {
			return nil, false, nil
		}
		up := geminiToolName(t.Name)
		if other, taken := names[up]; taken && other != t.Name {
			return nil, false, refuse("tools %q and %q map to the same name on Gemini", other, t.Name)
		}
		names[up] = t.Name
		params := t.InputSchema
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		return &geminiwire.FunctionDeclaration{Name: up, Description: t.Description, ParametersJSONSchema: params}, false, nil
	case strings.HasPrefix(t.Type, "web_search_"):
		if len(t.AllowedDomains) > 0 || len(t.BlockedDomains) > 0 {
			return nil, false, refuse("web_search's domain filters have no equivalent in Gemini's Google Search")
		}
		return nil, true, nil
	}
	return nil, false, refuse("tool type %q has no equivalent on Gemini; "+
		"bind this client's model to a Claude model to use it", t.Type)
}

// geminiToolChoice maps tool_choice. Gemini has no switch for parallel calls.
func geminiToolChoice(c *toolChoice) *geminiwire.ToolConfig {
	if c == nil {
		return nil
	}
	fc := &geminiwire.FunctionCallingConfig{}
	switch c.Type {
	case "any":
		fc.Mode = "ANY"
	case "none":
		fc.Mode = "NONE"
	case "tool":
		fc.Mode = "ANY"
		fc.AllowedFunctionNames = []string{geminiToolName(c.Name)}
	default:
		fc.Mode = "AUTO"
	}
	return &geminiwire.ToolConfig{FunctionCallingConfig: fc}
}

// geminiContents maps the turns onto Gemini contents, in order, a run of
// turns of one role merged into one content.
func (b builder) geminiContents(messages []message) ([]geminiwire.Content, error) {
	// A result names its call by id; Gemini wants the function's name too,
	// which only the call says.
	callNames := map[string]string{}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		blocks, _ := contentBlocks(m.Content)
		for _, bl := range blocks {
			if bl.Type == "tool_use" {
				callNames[bl.ID] = bl.Name
			}
		}
	}

	var out []geminiwire.Content
	add := func(role string, parts []geminiwire.Part) {
		if len(parts) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			return
		}
		out = append(out, geminiwire.Content{Role: role, Parts: parts})
	}
	for i, m := range messages {
		blocks, err := contentBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		switch m.Role {
		case "assistant":
			add("model", b.modelParts(blocks))
		case "user", "system":
			parts, err := b.userParts(blocks, callNames)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", i, err)
			}
			add("user", parts)
		default:
			return nil, refuse("messages[%d] has role %q, which the Messages API does not have", i, m.Role)
		}
	}
	// Every model turn's first call carries a signature: its own when the
	// caller sent it back or the gateway kept it, else the one gemini-cli
	// uses for history it holds no signature for.
	for i := range out {
		if out[i].Role != "model" {
			continue
		}
		for j := range out[i].Parts {
			p := &out[i].Parts[j]
			if p.FunctionCall == nil {
				continue
			}
			if p.ThoughtSignature == "" {
				p.ThoughtSignature = syntheticSignature
			}
			break
		}
	}
	return out, nil
}

// userParts maps a user (or system) turn's blocks.
func (b builder) userParts(blocks []block, callNames map[string]string) ([]geminiwire.Part, error) {
	var parts []geminiwire.Part
	for _, bl := range blocks {
		if bl.Type == "tool_result" {
			more, err := b.functionResponse(bl, callNames)
			if err != nil {
				return nil, err
			}
			parts = append(parts, more...)
			continue
		}
		more, err := geminiParts(bl)
		if b.skip(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, more...)
	}
	return parts, nil
}

// geminiParts maps one block of a user turn.
func geminiParts(bl block) ([]geminiwire.Part, error) {
	switch bl.Type {
	case "text":
		if strings.TrimSpace(bl.Text) == "" {
			return nil, nil
		}
		return []geminiwire.Part{{Text: bl.Text}}, nil
	case "image":
		p, err := geminiMedia(bl.Source, "an image")
		if err != nil {
			return nil, err
		}
		return []geminiwire.Part{p}, nil
	case "document":
		s := bl.Source
		if s == nil {
			return nil, refuse("a document block has no source")
		}
		switch s.Type {
		case "text":
			return []geminiwire.Part{{Text: documentText(bl.Title, bl.Context, s.Data)}}, nil
		case "content":
			inner, err := contentBlocks(s.Content)
			if err != nil {
				return nil, err
			}
			var texts []string
			var media []geminiwire.Part
			for _, c := range inner {
				switch c.Type {
				case "text":
					texts = append(texts, c.Text)
				case "image":
					p, err := geminiMedia(c.Source, "an image")
					if err != nil {
						return nil, err
					}
					media = append(media, p)
				}
			}
			return append([]geminiwire.Part{{Text: documentText(bl.Title, bl.Context, strings.Join(texts, "\n"))}}, media...), nil
		}
		p, err := geminiMedia(s, "a document")
		if err != nil {
			return nil, err
		}
		return []geminiwire.Part{p}, nil
	case "search_result":
		var texts []string
		if bl.Title != "" {
			texts = append(texts, bl.Title)
		}
		if bl.Source != nil && bl.Source.URL != "" {
			texts = append(texts, bl.Source.URL)
		}
		inner, _ := contentBlocks(bl.Content)
		for _, c := range inner {
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
		return []geminiwire.Part{{Text: strings.Join(texts, "\n")}}, nil
	case "tool_reference":
		return []geminiwire.Part{{Text: "Tool " + bl.ToolName + " is now available."}}, nil
	case "thinking", "redacted_thinking", "server_tool_use", "web_search_tool_result",
		"web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result", "mcp_tool_use", "mcp_tool_result":
		// Assistant history echoed in a user turn; what it found is in the
		// text after it.
		return nil, nil
	}
	return nil, refuse("a %q content block has no equivalent on Gemini", bl.Type)
}

// geminiMedia is an image or a document by value or by address.
func geminiMedia(s *source, what string) (geminiwire.Part, error) {
	if s == nil {
		return geminiwire.Part{}, refuse("%s block has no source", what)
	}
	switch s.Type {
	case "base64":
		return geminiwire.Part{InlineData: &geminiwire.Blob{MimeType: s.MediaType, Data: s.Data}}, nil
	case "url":
		mime := s.MediaType
		if mime == "" && what == "a document" {
			mime = "application/pdf"
		}
		return geminiwire.Part{FileData: &geminiwire.FileData{MimeType: mime, FileURI: s.URL}}, nil
	}
	return geminiwire.Part{}, refuse("%s whose source is %q (an Anthropic file) cannot be sent to Gemini; "+
		"send it as base64 or a URL", what, s.Type)
}

// functionResponse maps a tool result: its text as gemini-cli words a
// result, {"output": …}, or {"error": …} for a failed call; and its
// pictures as parts after it, since a function response holds JSON only.
func (b builder) functionResponse(bl block, callNames map[string]string) ([]geminiwire.Part, error) {
	inner, err := contentBlocks(bl.Content)
	if err != nil {
		return nil, fmt.Errorf("tool_result %s: %w", bl.ToolUseID, err)
	}
	var texts []string
	var media []geminiwire.Part
	for _, c := range inner {
		switch c.Type {
		case "text":
			texts = append(texts, c.Text)
		case "tool_reference":
			texts = append(texts, "Tool "+c.ToolName+" is now available.")
		default:
			more, err := geminiParts(c)
			if b.skip(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			media = append(media, more...)
		}
	}
	key := "output"
	if bl.IsError {
		key = "error"
	}
	resp, _ := json.Marshal(map[string]string{key: strings.Join(texts, "\n")})
	name := callNames[bl.ToolUseID]
	if name == "" {
		return nil, refuse("tool_result %s answers no tool_use in the conversation", bl.ToolUseID)
	}
	fr := geminiwire.Part{FunctionResponse: &geminiwire.FunctionResponse{
		ID: geminiCallID(bl.ToolUseID), Name: geminiToolName(name), Response: resp,
	}}
	return append([]geminiwire.Part{fr}, media...), nil
}

// modelParts maps an assistant turn: text, calls, and the signatures that go
// on the calls — carried back in a thinking block, or kept by the gateway
// for a caller that keeps no thinking (reasoningstore.go).
func (b builder) modelParts(blocks []block) []geminiwire.Part {
	var parts []geminiwire.Part
	pending := ""
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			if strings.TrimSpace(bl.Text) == "" {
				continue
			}
			parts = append(parts, geminiwire.Part{Text: bl.Text})
		case "tool_use":
			args := json.RawMessage(`{}`)
			if len(bl.Input) > 0 && string(bl.Input) != "null" {
				args = bl.Input
			}
			sig := pending
			pending = ""
			if sig == "" {
				for _, r := range b.reasoning.get(b.conversation, callKey(bl.ID)) {
					sig = r.Encrypted
				}
			}
			parts = append(parts, geminiwire.Part{
				ThoughtSignature: sig,
				FunctionCall:     &geminiwire.FunctionCall{ID: geminiCallID(bl.ID), Name: geminiToolName(bl.Name), Args: args},
			})
		case "thinking":
			if sig, ok := decodeGeminiSignature(bl.Signature); ok {
				pending = sig
			}
		}
	}
	return parts
}

// geminiThinking maps thinking and effort onto a thinking budget, and says
// whether the answer carries thinking blocks.
//
// A budget, not a level: the model a request reaches is settled after the
// translation (a client's name, a family's version), and Gemini 3 takes a
// budget as well as 2.5 does while 2.5 takes no level. So:
//
//   - thinking enabled with a budget is that budget;
//   - output_config.effort low is 1024, medium 8192, and the higher ones
//     the model's own choice (-1), which for Gemini is to think as it needs;
//   - thinking disabled is left to the model: Gemini Pro cannot turn it
//     off, and a budget of 0 is refused there;
//   - absent or adaptive is the model's own choice.
//
// Thoughts are asked for when the caller will show them, as the ChatGPT
// translation asks for a summary.
func geminiThinking(t *thinking, oc *outputConfig) (*geminiwire.ThinkingConfig, bool, bool) {
	on, omitted := false, false
	var budget *int
	set := func(n int) { budget = &n }
	if t != nil {
		switch t.Type {
		case "enabled", "adaptive":
			on = true
			omitted = t.Display == "omitted"
			if t.Type == "enabled" && t.BudgetTokens > 0 {
				set(t.BudgetTokens)
			}
		}
	}
	if oc != nil {
		switch oc.Effort {
		case "low":
			set(1024)
		case "medium":
			set(8192)
		case "high", "xhigh", "max":
			set(-1)
		}
	}
	include := on && !omitted
	if budget == nil && !include {
		return nil, on, omitted
	}
	return &geminiwire.ThinkingConfig{IncludeThoughts: include, ThinkingBudget: budget}, on, omitted
}

// geminiFormat maps a json_schema output format onto a response schema.
func geminiFormat(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("output format: %w", err)
	}
	if f.Type != "json_schema" {
		return nil, refuse("output format %q has no equivalent on Gemini", f.Type)
	}
	return f.Schema, nil
}

// estimateGemini is count_tokens's estimate for a Gemini request, on
// counttokens.go's rule: the text the model reads at four bytes a token, a
// picture at a flat figure, a PDF at its decoded size over eight.
func estimateGemini(r geminiwire.Request) int {
	n, media := 0, 0
	count := func(parts []geminiwire.Part) {
		for _, p := range parts {
			n += len(p.Text)
			switch {
			case p.InlineData != nil && p.InlineData.MimeType == "application/pdf":
				media += base64.StdEncoding.DecodedLen(len(p.InlineData.Data)) / pdfBytesPerToken
			case p.InlineData != nil, p.FileData != nil:
				media += tokensPerImage
			case p.FunctionCall != nil:
				n += len(p.FunctionCall.Name) + len(p.FunctionCall.Args)
			case p.FunctionResponse != nil:
				n += len(p.FunctionResponse.Name) + len(p.FunctionResponse.Response)
			}
		}
	}
	if si := r.Request.SystemInstruction; si != nil {
		count(si.Parts)
	}
	for _, c := range r.Request.Contents {
		count(c.Parts)
	}
	for _, t := range r.Request.Tools {
		for _, d := range t.FunctionDeclarations {
			n += len(d.Name) + len(d.Description) + len(d.ParametersJSONSchema)
		}
	}
	return (n+bytesPerToken-1)/bytesPerToken + media
}
