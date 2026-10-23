// Package geminiwire is the shape of what Google's Code Assist backend takes
// and answers: the envelope it wraps a Gemini request in, the request and its
// parts, the streamed and whole answers, usage, and the error envelope with
// its google.rpc details.
//
// Shapes only, with no behaviour, so it sits at the bottom of the layering and
// anything may use it: the provider package that meters and classifies
// answers for the relay, and the two dialects that translate into it and back
// out of it, which are below the provider and so could not share its types.
// anthropicwire is the same arrangement for Anthropic's answers, and exists
// for the same reason: copies of a wire format are where one reader learns a
// field the other never hears of.
//
// The fields are gemini-cli's, read at a713410 (2026-10-22):
// packages/core/src/code_assist/converter.ts for the envelope, and
// @google/genai's types for what goes inside it. They are the subset a
// translation writes or a reader reads; the backend takes more.
package geminiwire

import "encoding/json"

// Request is the Code Assist envelope around one Gemini request, as
// gemini-cli's toGenerateContentRequest builds it. Project is the account's
// Cloud project, which the backend bills and which is per account, so it is
// left out by whoever builds the body and stamped in by the provider once an
// account has been leased for the attempt.
type Request struct {
	Model        string          `json:"model"`
	Project      string          `json:"project,omitempty"`
	UserPromptID string          `json:"user_prompt_id,omitempty"`
	Request      GenerateRequest `json:"request"`
}

// GenerateRequest is the Gemini request proper, in Vertex's field names.
type GenerateRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	SafetySettings    []SafetySetting   `json:"safetySettings,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	// SessionID groups a conversation's turns; gemini-cli sends its own
	// session's. The backend's use of it is not documented.
	SessionID string `json:"session_id,omitempty"`
}

// Content is one turn: a role, "user" or "model", and its parts. The system
// instruction is a Content with no role.
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part is one piece of a turn. Exactly one of Text, InlineData, FileData,
// FunctionCall and FunctionResponse is set, and Thought marks a Text as the
// model's reasoning rather than its answer.
type Part struct {
	Text    string `json:"text,omitempty"`
	Thought bool   `json:"thought,omitempty"`
	// ThoughtSignature is the model's opaque record of its reasoning, on the
	// part that follows it. Gemini 3 refuses a turn whose function calls come
	// back without the signatures they were sent with, so a translation has
	// to carry it through the client and back.
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	InlineData       *Blob             `json:"inlineData,omitempty"`
	FileData         *FileData         `json:"fileData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// Blob is inline bytes, base64 in JSON.
type Blob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// FileData is content by reference.
type FileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

// FunctionCall is the model asking for a tool. ID is set by newer models and
// echoed on the matching FunctionResponse; older ones match by name and order.
type FunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// FunctionResponse is a tool's result. Response must be a JSON object.
type FunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// Tool is a set of declarations, or one of the backend's own tools.
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	GoogleSearch         *struct{}             `json:"googleSearch,omitempty"`
}

// FunctionDeclaration declares one tool. ParametersJSONSchema takes a JSON
// Schema as it is; the older parameters field takes only the OpenAPI subset
// Gemini's own Schema type has, and refuses what it does not know.
type FunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

// ToolConfig says whether and which tools the model may call.
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// FunctionCallingConfig mode is AUTO, ANY, NONE or VALIDATED; with ANY,
// AllowedFunctionNames narrows which.
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// SafetySetting is one harm category's threshold.
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GenerationConfig is the sampling and output settings.
type GenerationConfig struct {
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"topP,omitempty"`
	TopK             *float64        `json:"topK,omitempty"`
	MaxOutputTokens  int             `json:"maxOutputTokens,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseJsonSchema,omitempty"`
	ThinkingConfig   *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ThinkingConfig asks for reasoning. Gemini 2.5 takes a token budget (-1 is
// the model's own choice, 0 off where the model allows it); Gemini 3 takes a
// level, LOW or HIGH, and is refused a budget and a level together.
type ThinkingConfig struct {
	IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
}

// Response is the Code Assist envelope around one answer, or around one
// event of a streamed one: each `data:` line of the stream is a whole
// Response. TraceID is the backend's id for the turn.
type Response struct {
	Response *GenerateResponse `json:"response,omitempty"`
	TraceID  string            `json:"traceId,omitempty"`
}

// GenerateResponse is the Gemini answer proper.
type GenerateResponse struct {
	Candidates     []Candidate     `json:"candidates,omitempty"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
	ModelVersion   string          `json:"modelVersion,omitempty"`
	ResponseID     string          `json:"responseId,omitempty"`
}

// Candidate is one answer. The gateway asks for one, and reads the first.
type Candidate struct {
	Content      *Content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	Index        int      `json:"index,omitempty"`
}

// The finish reasons a translation tells apart. The rest — SAFETY,
// RECITATION, PROHIBITED_CONTENT and the like — are the model declining, and
// are read as such.
const (
	FinishStop                  = "STOP"
	FinishMaxTokens             = "MAX_TOKENS"
	FinishMalformedFunctionCall = "MALFORMED_FUNCTION_CALL"
)

// PromptFeedback is set when the prompt itself was blocked, and then there
// are no candidates.
type PromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

// UsageMetadata is what a turn cost. Every streamed event may carry it; the
// last one's is the turn's. PromptTokenCount includes the cached part, and
// ThoughtsTokenCount is outside CandidatesTokenCount — both unlike
// Anthropic's figures, and normalised by whoever reads them.
type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
}

// ErrorEnvelope is a Google API error answer: google.rpc.Status in JSON.
type ErrorEnvelope struct {
	Error Status `json:"error"`
}

// Status is the error itself. Code is the HTTP status, Status its canonical
// name (RESOURCE_EXHAUSTED, PERMISSION_DENIED, …), and Details the typed
// google.rpc messages that say more, each told apart by its "@type".
type Status struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Status  string            `json:"status"`
	Details []json.RawMessage `json:"details,omitempty"`
}

// The google.rpc detail types read, by their "@type".
const (
	TypeErrorInfo    = "type.googleapis.com/google.rpc.ErrorInfo"
	TypeRetryInfo    = "type.googleapis.com/google.rpc.RetryInfo"
	TypeQuotaFailure = "type.googleapis.com/google.rpc.QuotaFailure"
	TypeHelp         = "type.googleapis.com/google.rpc.Help"
)

// Detail is any one google.rpc detail, in the fields of the four types read.
// Which apply is said by Type.
type Detail struct {
	Type string `json:"@type"`
	// ErrorInfo.
	Reason   string            `json:"reason,omitempty"`
	Domain   string            `json:"domain,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	// RetryInfo: a duration, "3.5s" or "120ms".
	RetryDelay string `json:"retryDelay,omitempty"`
	// QuotaFailure.
	Violations []struct {
		Subject     string `json:"subject,omitempty"`
		Description string `json:"description,omitempty"`
		QuotaID     string `json:"quotaId,omitempty"`
	} `json:"violations,omitempty"`
	// Help.
	Links []struct {
		Description string `json:"description,omitempty"`
		URL         string `json:"url,omitempty"`
	} `json:"links,omitempty"`
}
