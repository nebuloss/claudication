// Package gemini is the Gemini API surface: what gemini-cli speaks when it is
// pointed at a gateway, and what any @google/genai client sends to a base URL
// of its choosing.
//
// gemini-cli (main at a713410, read 2026-10-23) has an auth type for exactly
// this, "gateway" (packages/core/src/core/contentGenerator.ts): with
// GOOGLE_GEMINI_BASE_URL set it builds a GoogleGenAI client on that address
// with GEMINI_API_KEY as the key, and @google/genai sends
//
//	POST {base}/v1beta/models/{model}:streamGenerateContent?alt=sse
//	POST {base}/v1beta/models/{model}:generateContent
//	POST {base}/v1beta/models/{model}:countTokens
//	x-goog-api-key: <the key>
//
// with the model in the path and the body a GenerateContentRequest:
// contents, systemInstruction, tools, toolConfig, generationConfig. None of
// it has been measured through the gateway yet; it is read from the source.
//
// # Where a request goes
//
// By the model it names, as every dialect's does (internal/service/models):
//
//	a Gemini model        Code Assist, the request rewrapped (native.go)
//	a Claude model        Messages, translated (tomessages.go) and its
//	                      answer said back (frommessages.go)
//	a GPT model           Messages, then on to Responses (api.Chain)
//	countTokens           answered here, estimated (count.go)
//
// The path is folded into the body before the gateway's pipeline sees it
// (Envelope), because the pipeline reads a request's model from its body as
// every other dialect carries it there.
package gemini
