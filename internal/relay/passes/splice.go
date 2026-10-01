package passes

import (
	"encoding/json"

	"claudication/internal/request"
)

// Adding the attribution block without rebuilding the body.
//
// Every request from a client that is not Claude Code — every crush request —
// needs the block, and the way it used to be added was to decode the whole
// envelope into a map, re-encode the system array, and re-encode the envelope.
// Measured under load (2 MB bodies, 25 agents, one core): 1.16 GB allocated,
// half of everything the gateway allocated, about six times the body per
// request, and 2.7 s of CPU. It also reordered the top-level keys and
// re-compacted every value on the way, which nothing asked for.
//
// The block goes in one place, so this finds that place and copies the body
// once around it. Every other byte stays where the client put it.

// attributionBlock is the block to insert, encoded once.
var attributionBlock = func() []byte {
	b, err := json.Marshal(map[string]string{"type": "text", "text": ClaudeCodeAttribution})
	if err != nil {
		panic(err)
	}
	return b
}()

// spliceAttribution puts the attribution block first in body's system field,
// creating the field if there is none. ok is false for any shape this does not
// recognise, and the caller then takes the slow path, which decides for itself.
//
// body must already be known to be valid JSON: this finds structure, it does
// not check it.
func spliceAttribution(body []byte) (out []byte, ok bool) {
	open := request.SkipSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return nil, false
	}
	start, end, found, ok := request.TopLevelValue(body, "system")
	if !ok {
		return nil, false
	}

	if !found {
		// As the first key: `{` + `"system":[A]` + (`,` unless empty) + rest.
		rest := request.SkipSpace(body, open+1)
		if rest >= len(body) {
			return nil, false
		}
		sep := ","
		if body[rest] == '}' {
			sep = ""
		}
		return request.Join(body[:open+1], []byte(`"system":[`), attributionBlock, []byte("]"+sep), body[open+1:]), true
	}

	switch body[start] {
	case '[':
		inner := request.SkipSpace(body, start+1)
		if inner >= end {
			return nil, false
		}
		sep := ","
		if body[inner] == ']' {
			sep = ""
		}
		return request.Join(body[:start+1], attributionBlock, []byte(sep), body[start+1:]), true
	case '"':
		// A bare string becomes the second block, its text untouched.
		return request.Join(body[:start], []byte("["), attributionBlock, []byte(`,{"type":"text","text":`),
			body[start:end], []byte("}]"), body[end:]), true
	case 'n':
		return request.Join(body[:start], []byte("["), attributionBlock, []byte("]"), body[end:]), true
	}
	return nil, false
}
