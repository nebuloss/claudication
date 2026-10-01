package request

// Finding and replacing top-level fields in the bytes.
//
// What every in-place rewrite is built from. A pass that changes one field —
// the attribution block in system, the reworded environment line, a message
// with an empty block — finds that field's span here and copies the body once
// around its replacement, instead of decoding the envelope into a map and
// re-encoding all of it. That rebuild cost several times the body per request
// and reordered the client's keys on the way; see the relay passes for what
// was measured.
//
// These find structure; they do not validate it. Callers splice only into a
// body already known to parse (Prologue.Valid), and treat ok == false — a
// duplicated or escaped key, or a shape the walk does not follow — as a reason
// to take a slower path that decides for itself.

// Join concatenates parts into one allocation.
func Join(parts ...[]byte) []byte {
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

// TopLevelValue finds the value of key in the top-level object of a valid JSON
// body: its byte range, whether the key is there, and whether the walk made
// sense of the body at all. Keys containing escapes are not matched, so a body
// that spells "system" with one is reported as not understood rather than as
// lacking the key.
func TopLevelValue(body []byte, key string) (start, end int, found, ok bool) {
	_, start, end, found, ok = TopLevelEntry(body, key)
	return start, end, found, ok
}

// TopLevelEntry is TopLevelValue that also reports where the entry's key
// begins, which is what removing the entry needs.
func TopLevelEntry(body []byte, key string) (keyStart, start, end int, found, ok bool) {
	i := SkipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return 0, 0, 0, false, false
	}
	i = SkipSpace(body, i+1)
	if i < len(body) && body[i] == '}' {
		return 0, 0, 0, false, true
	}
	for i < len(body) {
		if body[i] != '"' {
			return 0, 0, 0, false, false
		}
		kStart := i
		kEnd, escaped := stringEnd(body, i)
		if kEnd < 0 {
			return 0, 0, 0, false, false
		}
		name := body[i+1 : kEnd-1]
		i = SkipSpace(body, kEnd)
		if i >= len(body) || body[i] != ':' {
			return 0, 0, 0, false, false
		}
		vStart := SkipSpace(body, i+1)
		vEnd := valueEnd(body, vStart)
		if vEnd < 0 {
			return 0, 0, 0, false, false
		}
		if string(name) == key {
			if escaped || found {
				// Escaped, or said twice — and encoding/json keeps the last
				// of two, so splicing the first would disagree with every
				// other reader of this body. Not ours to resolve.
				return 0, 0, 0, false, false
			}
			keyStart, start, end, found = kStart, vStart, vEnd, true
		} else if escaped && len(name) >= len(key) {
			// Might be the key, spelled with an escape. Not worth decoding.
			return 0, 0, 0, false, false
		}
		i = SkipSpace(body, vEnd)
		if i >= len(body) {
			return 0, 0, 0, false, false
		}
		switch body[i] {
		case ',':
			i = SkipSpace(body, i+1)
		case '}':
			return keyStart, start, end, found, true
		default:
			return 0, 0, 0, false, false
		}
	}
	return 0, 0, 0, false, false
}

// ReplaceTopLevel returns body with key's value replaced, copying everything
// else as it is. ok is false when the key is absent or the body is a shape
// TopLevelEntry does not understand.
func ReplaceTopLevel(body []byte, key string, value []byte) ([]byte, bool) {
	_, start, end, found, ok := TopLevelEntry(body, key)
	if !ok || !found {
		return nil, false
	}
	return Join(body[:start], value, body[end:]), true
}

// RemoveTopLevel returns body without key's entry, taking one neighbouring
// comma with it so the object stays well formed.
func RemoveTopLevel(body []byte, key string) ([]byte, bool) {
	keyStart, _, end, found, ok := TopLevelEntry(body, key)
	if !ok || !found {
		return nil, false
	}
	// A following comma goes with the entry; failing that, the preceding one.
	if after := SkipSpace(body, end); after < len(body) && body[after] == ',' {
		return Join(body[:keyStart], body[SkipSpace(body, after+1):]), true
	}
	before := keyStart - 1
	for before >= 0 && (body[before] == ' ' || body[before] == '\t' || body[before] == '\n' || body[before] == '\r') {
		before--
	}
	if before >= 0 && body[before] == ',' {
		return Join(body[:before], body[end:]), true
	}
	return Join(body[:keyStart], body[end:]), true // the only entry
}

// ArrayElements returns the [start, end) range of each element of the array
// occupying b[start:end], or ok false if it is not one.
func ArrayElements(b []byte, start, end int) (spans [][2]int, ok bool) {
	if start >= end || b[start] != '[' {
		return nil, false
	}
	i := SkipSpace(b, start+1)
	if i < end && b[i] == ']' {
		return nil, true
	}
	for i < end {
		e := valueEnd(b, i)
		if e < 0 || e > end {
			return nil, false
		}
		spans = append(spans, [2]int{i, e})
		i = SkipSpace(b, e)
		if i >= end {
			return nil, false
		}
		switch b[i] {
		case ',':
			i = SkipSpace(b, i+1)
		case ']':
			return spans, true
		default:
			return nil, false
		}
	}
	return nil, false
}

func SkipSpace(b []byte, i int) int {
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
