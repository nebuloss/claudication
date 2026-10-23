package passes

import (
	"bytes"
	"encoding/json"
	"strconv"

	"claudication/internal/request"
)

// The output budget: the second half of the seventh exception.
//
// A client sizes a request's output budget for the model it believes it is
// talking to. One naming its own slot believes in the model that slot served
// when it was configured — or, for crush, when the slot was picked: crush
// saves each pick to its data file with that moment's max_tokens and sends it
// on every request after (crush internal/config, load.go:838-842 and
// store.go:528-547 at 76cc5c5), whatever the file the gateway wrote says
// later. Rebinding the slot to a model with a smaller cap then turns every
// request into a refusal, until someone finds the stale number in a file the
// gateway never wrote.
//
// The upstream refuses a budget over the model's cap, and only the gateway
// knows the cap changed: the name means a different model now because the
// operator said so on the gateway, not because the client did anything. So,
// on a request that named a slot — and only then, beside SetModel, from the
// same decision — the budget is lowered to the cap of the model the slot
// serves now. A direct-mode client named the real model itself, so its
// budget is its own and an upstream refusal is the right answer to it.
//
// What the upstreams say about a budget over the cap:
//
//	Anthropic  max_tokens over the model's cap is a 400
//	           invalid_request_error, "max_tokens: N > M, which is the
//	           maximum allowed number of output tokens for <model>"; and
//	           thinking.budget_tokens must be below max_tokens, else 400
//	           too. [documented] — Anthropic's Messages API reference and
//	           extended-thinking guide; not measured through this gateway,
//	           which had no Claude account to spend on it when this was
//	           written.
//	ChatGPT    not measured. max_output_tokens: 256 is answered 200
//	           (internal/provider/openai, testdata/max-output.json), and 1
//	           is refused as below 16; what it does over a cap was not
//	           tried. Fitted on the same reasoning, which costs nothing when
//	           the backend would have accepted it: the model cannot write
//	           past its cap either way.
//
// The cap is the one the gateway's model list gives the model — the
// account's own list, with the public catalogue's limit.output over it
// (internal/service/models, catalog.go). A model whose cap is not known is
// sent as it came; so is a budget at or under the cap, a budget that is no
// plain integer, and a body that names none, which a Messages body always
// does and a Responses one need not.
//
// Only lowered, never raised, and the thinking budget only so far as it has
// to be to fit below the new max_tokens: both are the client's choice up to
// the point where the upstream would refuse them.

// FitMaxTokens fits an Anthropic Messages body's max_tokens to limit, and an
// extended-thinking budget_tokens below it. ok is false when nothing needed
// to change, and the body is then returned as it was.
func FitMaxTokens(body []byte, limit int) (out []byte, ok bool) {
	out, ok = fitTopLevel(body, "max_tokens", limit)
	if !ok {
		return body, false
	}
	// budget_tokens must stay below max_tokens. The thinking object is a
	// few dozen bytes, so it is rebuilt rather than spliced into; its key
	// order changes, which nothing reads.
	start, end, found, walked := request.TopLevelValue(out, "thinking")
	if !walked || !found {
		return out, true
	}
	var thinking map[string]json.RawMessage
	if json.Unmarshal(out[start:end], &thinking) != nil {
		return out, true
	}
	budget, isInt := plainInt(thinking["budget_tokens"])
	if !isInt || budget < limit {
		return out, true
	}
	thinking["budget_tokens"] = json.RawMessage(strconv.Itoa(limit - 1))
	rebuilt, err := json.Marshal(thinking)
	if err != nil {
		return out, true
	}
	return request.Join(out[:start], rebuilt, out[end:]), true
}

// FitMaxOutputTokens fits an OpenAI Responses body's max_output_tokens to
// limit. ok is false when nothing needed to change.
func FitMaxOutputTokens(body []byte, limit int) (out []byte, ok bool) {
	out, ok = fitTopLevel(body, "max_output_tokens", limit)
	if !ok {
		return body, false
	}
	return out, true
}

// FitGeminiMaxOutputTokens fits a Code Assist body's maxOutputTokens, which
// sits in request.generationConfig rather than at the top, to limit. The
// body is the gateway's own translation, so it is decoded and encoded again
// rather than spliced. ok is false when nothing needed to change. Gemini is
// not measured over a cap either; fitted on ChatGPT's reasoning.
func FitGeminiMaxOutputTokens(body []byte, limit int) (out []byte, ok bool) {
	if limit <= 0 {
		return body, false
	}
	var env, req, gc map[string]json.RawMessage
	if json.Unmarshal(body, &env) != nil || json.Unmarshal(env["request"], &req) != nil ||
		json.Unmarshal(req["generationConfig"], &gc) != nil {
		return body, false
	}
	n, isInt := plainInt(gc["maxOutputTokens"])
	if !isInt || n <= limit {
		return body, false
	}
	gc["maxOutputTokens"] = json.RawMessage(strconv.Itoa(limit))
	var err error
	if req["generationConfig"], err = json.Marshal(gc); err != nil {
		return body, false
	}
	if env["request"], err = json.Marshal(req); err != nil {
		return body, false
	}
	if out, err = json.Marshal(env); err != nil {
		return body, false
	}
	return out, true
}

// fitTopLevel lowers a top-level integer field to limit when it is above it.
func fitTopLevel(body []byte, key string, limit int) ([]byte, bool) {
	if limit <= 0 {
		return nil, false
	}
	start, end, found, walked := request.TopLevelValue(body, key)
	if !walked || !found {
		return nil, false
	}
	n, isInt := plainInt(body[start:end])
	if !isInt || n <= limit {
		return nil, false
	}
	return request.Join(body[:start], []byte(strconv.Itoa(limit)), body[end:]), true
}

// plainInt reads a JSON value that is a plain decimal integer. Anything else —
// a float, an exponent, a string, null — is not one the gateway touches.
func plainInt(raw []byte) (int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 18 {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil
}
