package clientconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The client files are JSON a person reads — crush.json, opencode.jsonc, the
// Codex catalog — and the committed copy is the order they read it in. Go's
// maps forget that order, so the rewrites go through this small ordered tree
// instead: an object keeps its keys as they came, a key set again stays where
// it was and a new one goes last, and the output is what JSON.stringify(v,
// null, 2) writes. That last part matters: the committed crush.json is a new
// client's file byte for byte (TestCommittedCrushIsANewClients), which holds
// only if the writer formats exactly as the file was written.

// object is a JSON object with its keys in order.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

// get is a key's value, or nil.
func (o *object) get(k string) any {
	if o == nil {
		return nil
	}
	return o.vals[k]
}

// has reports whether the key is there.
func (o *object) has(k string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[k]
	return ok
}

// obj is a key's value when it is an object.
func (o *object) obj(k string) *object {
	v, _ := o.get(k).(*object)
	return v
}

// set puts a value under a key: in place when the key is there, last when not.
func (o *object) set(k string, v any) *object {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
	return o
}

// del takes a key out.
func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// clone is a shallow copy: the keys and their values, not what they hold.
func (o *object) clone() *object {
	c := newObject()
	if o == nil {
		return c
	}
	for _, k := range o.keys {
		c.set(k, o.vals[k])
	}
	return c
}

// parseJSON reads JSON into the ordered tree: *object, []any, string,
// json.Number, bool or nil.
func parseJSON(text string) (any, error) {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	v, err := parseValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err == nil {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}

func parseValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObject()
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := parseValue(d)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			list := []any{}
			for d.More() {
				v, err := parseValue(d)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// parseObject is parseJSON for a text that must be an object.
func parseObject(text string) (*object, error) {
	v, err := parseJSON(text)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*object)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return o, nil
}

// indented is JSON.stringify(v, null, 2).
func indented(v any) string {
	var b bytes.Buffer
	writeJSON(&b, v, "  ", "")
	return b.String()
}

// compact is JSON.stringify(v).
func compact(v any) string {
	var b bytes.Buffer
	writeJSON(&b, v, "", "")
	return b.String()
}

func writeJSON(b *bytes.Buffer, v any, step, at string) {
	inner := at + step
	nl := func(indent string) {
		if step != "" {
			b.WriteByte('\n')
			b.WriteString(indent)
		}
	}
	sep := ":"
	if step != "" {
		sep = ": "
	}
	switch v := v.(type) {
	case *object:
		if v == nil || len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(inner)
			writeString(b, k)
			b.WriteString(sep)
			writeJSON(b, v.vals[k], step, inner)
		}
		nl(at)
		b.WriteByte('}')
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			nl(inner)
			writeJSON(b, x, step, inner)
		}
		nl(at)
		b.WriteByte(']')
	case []string:
		list := make([]any, len(v))
		for i, s := range v {
			list[i] = s
		}
		writeJSON(b, list, step, at)
	case string:
		writeString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case int:
		b.WriteString(strconv.Itoa(v))
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case nil:
		b.WriteString("null")
	default:
		// Nothing the writers build is anything else; a value that is would
		// otherwise vanish from a client's file without a word.
		panic(fmt.Sprintf("clientconfig: cannot write %T as JSON", v))
	}
}

// writeString quotes a string as JSON.stringify does: only the quote, the
// backslash and control characters escaped, everything else as it is.
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == utf8.RuneError {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// stripComments is JSONC to JSON: comments out, strings — and the // in their
// URLs — left alone.
func stripComments(body string) string {
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(body) && body[j] != '"' {
				if body[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			end := min(j+1, len(body))
			out.WriteString(body[i:end])
			i = j
		case c == '/' && i+1 < len(body) && body[i+1] == '/':
			for i < len(body) && body[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
