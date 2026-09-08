package upstream

import "bytes"

// A JSON string may hold `\uD800`-`\uDFFF` escapes, which are halves of a
// surrogate pair. Alone, they encode nothing, and the upstream refuses the
// whole request:
//
//	{"type":"error","error":{"type":"invalid_request_error","message":
//	 "The request body is not valid JSON: no low surrogate in string: line 1 …"}}
//
// Measured: a lone high or low surrogate is a 400 in a user message, in a
// system block and in a tool_result alike; a well-formed pair is a 200.
//
// This is easy to produce by accident and hard to see. Cutting a string to a
// character or byte budget splits any emoji sitting on the boundary, and a
// tool that truncates its own output — which is most of them — will do it
// eventually. Claude Code runs its own sanitiser over the entire assembled
// body as the last thing before sending, which is the clearest possible
// statement that this is worth handling rather than relaying into a refusal.
//
// The replacement is U+FFFD, what every decoder substitutes anyway: Go's own
// json package does it silently on any body this gateway re-encodes for
// another reason, so doing it here only makes the behaviour uniform and
// countable.
// A raw literal, so this is the six ASCII characters of the JSON escape and
// not the character itself — the point is that it is the same width as the
// escape it overwrites.
const surrogateReplacement = `\ufffd`

// FixLoneSurrogates repairs unpaired surrogate escapes and reports how many it
// replaced.
//
// It works on the escape sequences rather than on decoded values, so nothing
// else in the body is touched: the replacement escape is the same six bytes
// as the one it overwrites, and
// every other byte is left where it was. A body with no surrogate escape at
// all is returned as the caller's own bytes.
func FixLoneSurrogates(body []byte) ([]byte, int) {
	// Every surrogate escape starts `\ud`, in either case. Bodies carrying
	// other escapes — control characters in tool output, mostly — stop here.
	if !bytes.Contains(body, []byte(`\ud`)) && !bytes.Contains(body, []byte(`\uD`)) {
		return body, 0
	}

	out := body
	fixed := 0
	copied := false

	for i := 0; i < len(out); {
		if out[i] != '\\' {
			i++
			continue
		}
		// Only an odd-length run of backslashes leaves the last one escaping
		// what follows: in `\\ud800` the backslash is literal text.
		j := i
		for j < len(out) && out[j] == '\\' {
			j++
		}
		if (j-i)%2 == 0 {
			i = j
			continue
		}

		cp, ok := escapeAt(out, j)
		if !ok {
			i = j + 1
			continue
		}
		switch {
		case cp >= 0xD800 && cp <= 0xDBFF:
			// A high surrogate is only well-formed when a low one follows.
			//
			// j+6, not j+5: this escape ends at j+4, so the next one's
			// backslash is at j+5 and its `u` — which is what escapeAt wants —
			// is one further on. Off by one here reads every well-formed pair
			// as two lone halves and replaces both, quietly turning every
			// escaped emoji into two replacement characters. It still produces
			// valid JSON, so the upstream accepts it and nothing complains.
			if low, ok := escapeAt(out, j+6); ok && low >= 0xDC00 && low <= 0xDFFF {
				i = j + 11
				continue
			}
		case cp >= 0xDC00 && cp <= 0xDFFF:
			// A paired low surrogate was consumed above, so reaching one here
			// means it stands alone.
		default:
			i = j + 5
			continue
		}

		if !copied {
			out = append([]byte(nil), out...)
			copied = true
		}
		copy(out[j-1:j+5], surrogateReplacement)
		fixed++
		i = j + 5
	}
	return out, fixed
}

// escapeAt decodes the `\uXXXX` escape whose `u` sits at i, if one does.
func escapeAt(b []byte, i int) (rune, bool) {
	if i >= len(b) || b[i] != 'u' || i+4 >= len(b) {
		return 0, false
	}
	var cp rune
	for _, c := range b[i+1 : i+5] {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		cp = cp<<4 | d
	}
	return cp, true
}
