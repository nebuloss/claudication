package upstream

import "encoding/json"

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
	open := skipSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return nil, false
	}
	start, end, found, ok := topLevelValue(body, "system")
	if !ok {
		return nil, false
	}

	if !found {
		// As the first key: `{` + `"system":[A]` + (`,` unless empty) + rest.
		rest := skipSpace(body, open+1)
		if rest >= len(body) {
			return nil, false
		}
		sep := ","
		if body[rest] == '}' {
			sep = ""
		}
		return join(body[:open+1], []byte(`"system":[`), attributionBlock, []byte("]"+sep), body[open+1:]), true
	}

	switch body[start] {
	case '[':
		inner := skipSpace(body, start+1)
		if inner >= end {
			return nil, false
		}
		sep := ","
		if body[inner] == ']' {
			sep = ""
		}
		return join(body[:start+1], attributionBlock, []byte(sep), body[start+1:]), true
	case '"':
		// A bare string becomes the second block, its text untouched.
		return join(body[:start], []byte("["), attributionBlock, []byte(`,{"type":"text","text":`),
			body[start:end], []byte("}]"), body[end:]), true
	case 'n':
		return join(body[:start], []byte("["), attributionBlock, []byte("]"), body[end:]), true
	}
	return nil, false
}

// join concatenates parts into one allocation.
func join(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// topLevelValue finds the value of key in the top-level object of a valid JSON
// body: its byte range, whether the key is there, and whether the walk made
// sense of the body at all. Keys containing escapes are not matched, so a body
// that spells "system" with one is reported as not understood rather than as
// lacking the key.
func topLevelValue(body []byte, key string) (start, end int, found, ok bool) {
	i := skipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, false, false
	}
	i = skipSpace(body, i+1)
	if i < len(body) && body[i] == '}' {
		return 0, 0, false, true
	}
	for i < len(body) {
		if body[i] != '"' {
			return 0, 0, false, false
		}
		kEnd, escaped := stringEnd(body, i)
		if kEnd < 0 {
			return 0, 0, false, false
		}
		name := body[i+1 : kEnd-1]
		i = skipSpace(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, false, false
		}
		vStart := skipSpace(body, i+1)
		vEnd := valueEnd(body, vStart)
		if vEnd < 0 {
			return 0, 0, false, false
		}
		if string(name) == key {
			if escaped || found {
				// Escaped, or said twice — and encoding/json keeps the last
				// of two, so splicing the first would disagree with every
				// other reader of this body. Not ours to resolve.
				return 0, 0, false, false
			}
			start, end, found = vStart, vEnd, true
		} else if escaped && len(name) >= len(key) {
			// Might be the key, spelled with an escape. Not worth decoding.
			return 0, 0, false, false
		}
		i = skipSpace(body, vEnd)
		if i >= len(body) {
			return 0, 0, false, false
		}
		switch body[i] {
		case ',':
			i = skipSpace(body, i+1)
		case '}':
			return start, end, found, true
		default:
			return 0, 0, false, false
		}
	}
	return 0, 0, false, false
}

func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// stringEnd returns the index just past the string starting at b[i] == '"',
// and whether it contained an escape; -1 if it never closes.
func stringEnd(b []byte, i int) (int, bool) {
	escaped := false
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			escaped = true
			j++
		case '"':
			return j + 1, escaped
		}
	}
	return -1, escaped
}

// valueEnd returns the index just past the value starting at b[i], or -1.
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		end, _ := stringEnd(b, i)
		return end
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end, _ := stringEnd(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		// A number, true, false or null: up to the next delimiter.
		j := i
		for j < len(b) {
			switch b[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j
			}
			j++
		}
		return j
	}
}
