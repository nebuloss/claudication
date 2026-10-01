package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// The span functions are trusted to find a field in bytes they never
// validate, and every in-place rewrite in the relay splices at the offsets
// they return. A wrong offset there does not fail loudly: it sends the
// upstream a body that says something the client never did. So most of what
// follows compares them against encoding/json, the reader every other part of
// the gateway (and the upstream) agrees with.

func TestTopLevelValueFindsTheField(t *testing.T) {
	cases := []struct {
		name, body, key, want string
		found                 bool
	}{
		{"first", `{"system":"s","model":"m"}`, "system", `"s"`, true},
		{"last", `{"model":"m","system":[1,2]}`, "system", `[1,2]`, true},
		{"whitespace everywhere", " \n{ \t\"model\" :\r\n \"m\" , \"system\"\n:\n{\"a\":1}\n}\n", "system", `{"a":1}`, true},
		{"number", `{"max_tokens":1024,"x":1}`, "max_tokens", `1024`, true},
		{"number before close", `{"x":1,"max_tokens":-1.5e3}`, "max_tokens", `-1.5e3`, true},
		{"literal", `{"stream":true}`, "stream", `true`, true},
		{"null", `{"system":null}`, "system", `null`, true},
		{"braces and quotes in strings", `{"a":"}{][\"","system":"x\"}y","b":{"c":"]"}}`, "system", `"x\"}y"`, true},
		{"nested key of the same name", `{"a":{"system":"inner"},"system":"outer"}`, "system", `"outer"`, true},
		{"absent", `{"model":"m"}`, "system", ``, false},
		{"only nested", `{"a":{"system":1}}`, "system", ``, false},
		{"empty object", `{}`, "system", ``, false},
		{"empty object with space", " { \n } ", "system", ``, false},
		{"prefix of another key", `{"systems":1,"sys":2}`, "system", ``, false},
		{"escaped other key, shorter", `{"\n":1,"system":2}`, "system", `2`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, found, ok := TopLevelValue([]byte(c.body), c.key)
			if !ok {
				t.Fatalf("ok = false for %s", c.body)
			}
			if found != c.found {
				t.Fatalf("found = %v, want %v", found, c.found)
			}
			if found && c.body[start:end] != c.want {
				t.Errorf("value = %q, want %q", c.body[start:end], c.want)
			}
		})
	}
}

// A shape the walk cannot vouch for must say so rather than guess: the caller
// then takes the slow path that decodes for itself. Duplicated and escaped
// keys are the dangerous ones, because encoding/json would read a different
// value from the one a naive splice would change.
func TestTopLevelValueRefusesWhatItCannotVouchFor(t *testing.T) {
	for _, body := range []string{
		``,
		`   `,
		`[1,2]`,
		`"system"`,
		`{"system":1,"system":2}`,       // encoding/json keeps the last
		`{"system":1,"a":2,"system":3}`, // ...however far apart
		`{"\u0073ystem":1}`,             // the key, spelled with an escape
		`{"sys\u0074em":1,"system":2}`,  // an escaped duplicate
		`{"system":1,"\u0073ystem":2}`,  // escaped duplicate after the real one
		`{"a\"bcdef":1}`,                // escaped and long enough to be it
		`{"system" 1}`,                  // no colon
		`{"system":1`,                   // never closes
		`{"system":1 "a":2}`,            // no comma
		`{"system":"unterminated}`,      // string runs off the end
		`{"system":{"a":1}`,             // nested object never closes
		`{"a":1,}`,                      // trailing comma then nothing a key
		`{"a`,                           // key runs off the end
		`{"a":`,                         // value missing at the end
		`{1:2}`,                         // not a string key
	} {
		if _, _, _, ok := TopLevelValue([]byte(body), "system"); ok {
			t.Errorf("TopLevelValue(%q) ok = true, want false", body)
		}
	}
}

// TopLevelEntry's key offset is what RemoveTopLevel cuts from, so it must
// point at the opening quote of the key and not at whitespace before it.
func TestTopLevelEntryReportsTheKey(t *testing.T) {
	body := []byte(`{ "a":1,  "system" : "s" }`)
	keyStart, start, end, found, ok := TopLevelEntry(body, "system")
	if !ok || !found {
		t.Fatalf("ok=%v found=%v", ok, found)
	}
	if got := string(body[keyStart:end]); got != `"system" : "s"` {
		t.Errorf("entry = %q", got)
	}
	if got := string(body[start:end]); got != `"s"` {
		t.Errorf("value = %q", got)
	}
}

// Replacing keeps every other byte where the client put it — the point of
// splicing rather than re-encoding is that key order and formatting survive.
func TestReplaceTopLevel(t *testing.T) {
	body := []byte("{\n  \"model\": \"m\",\n  \"system\": \"old\",\n  \"z\": [1]\n}")
	out, ok := ReplaceTopLevel(body, "system", []byte(`[{"type":"text","text":"new"}]`))
	if !ok {
		t.Fatal("ok = false")
	}
	want := "{\n  \"model\": \"m\",\n  \"system\": [{\"type\":\"text\",\"text\":\"new\"}],\n  \"z\": [1]\n}"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
	if string(body) != "{\n  \"model\": \"m\",\n  \"system\": \"old\",\n  \"z\": [1]\n}" {
		t.Error("the input was modified; callers still hold it")
	}

	for _, body := range []string{`{"model":"m"}`, `{"system":1,"system":2}`, `not json`} {
		if out, ok := ReplaceTopLevel([]byte(body), "system", []byte(`1`)); ok || out != nil {
			t.Errorf("ReplaceTopLevel(%q) = %q, %v; want nil, false", body, out, ok)
		}
	}
}

// Removing an entry must leave a well-formed object whichever position it
// held, which is the comma bookkeeping this tests.
func TestRemoveTopLevel(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"system":1,"a":2}`, `{"a":2}`},
		{`{"a":1,"system":2}`, `{"a":1}`},
		{`{"a":1,"system":2,"b":3}`, `{"a":1,"b":3}`},
		{`{"system":1}`, `{}`},
		{`{ "system" : 1 }`, `{  }`},
		{"{\"a\":1 ,\n\t\"system\":[\"}\"] \n}", "{\"a\":1  \n}"},
		{"{\"system\":{\"x\":\",\"} ,  \"a\":2}", `{"a":2}`},
	}
	for _, c := range cases {
		out, ok := RemoveTopLevel([]byte(c.body), "system")
		if !ok {
			t.Errorf("RemoveTopLevel(%q) ok = false", c.body)
			continue
		}
		if string(out) != c.want {
			t.Errorf("RemoveTopLevel(%q) = %q, want %q", c.body, out, c.want)
		}
		if !json.Valid(out) {
			t.Errorf("RemoveTopLevel(%q) = %q is not valid JSON", c.body, out)
		}
	}
	for _, body := range []string{`{"a":1}`, `{"system":1,"system":2}`, `[]`} {
		if out, ok := RemoveTopLevel([]byte(body), "system"); ok || out != nil {
			t.Errorf("RemoveTopLevel(%q) = %q, %v; want nil, false", body, out, ok)
		}
	}
}

// ArrayElements is how the empty-text pass walks a transcript message by
// message without decoding it, so each span must be exactly one element.
func TestArrayElements(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{`[]`, nil},
		{`[ ]`, nil},
		{`[1]`, []string{`1`}},
		{`[1,"a",null]`, []string{`1`, `"a"`, `null`}},
		{"[ {\"a\":\"]\"} ,\n [1,[2]] , \"x,y\" ]", []string{`{"a":"]"}`, `[1,[2]]`, `"x,y"`}},
		{`[true,false]`, []string{`true`, `false`}},
	}
	for _, c := range cases {
		b := []byte(c.body)
		spans, ok := ArrayElements(b, 0, len(b))
		if !ok {
			t.Errorf("ArrayElements(%q) ok = false", c.body)
			continue
		}
		var got []string
		for _, s := range spans {
			got = append(got, c.body[s[0]:s[1]])
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ArrayElements(%q) = %q, want %q", c.body, got, c.want)
		}
	}

	// Inside a larger body: the bounds are the array's, not the body's.
	body := []byte(`{"messages":[{"role":"user"},{"role":"assistant"}],"x":[9]}`)
	start, end, found, ok := TopLevelValue(body, "messages")
	if !ok || !found {
		t.Fatal("messages not found")
	}
	spans, ok := ArrayElements(body, start, end)
	if !ok || len(spans) != 2 || string(body[spans[1][0]:spans[1][1]]) != `{"role":"assistant"}` {
		t.Errorf("spans = %v ok = %v", spans, ok)
	}
}

func TestArrayElementsRefusesWhatIsNotAnArray(t *testing.T) {
	for _, body := range []string{``, `{}`, `"[1]"`, `[1`, `[1 2]`, `[1,`, `["a]`, `[{"a":1]`} {
		b := []byte(body)
		if spans, ok := ArrayElements(b, 0, len(b)); ok {
			t.Errorf("ArrayElements(%q) = %v, true; want false", body, spans)
		}
	}
	// An element that runs past the stated end is not inside the array.
	b := []byte(`[{"a":1}]`)
	if _, ok := ArrayElements(b, 0, 4); ok {
		t.Error("an element past end was accepted")
	}
	if _, ok := ArrayElements(b, 3, 3); ok {
		t.Error("an empty range was accepted")
	}
}

func TestJoin(t *testing.T) {
	out := Join([]byte("ab"), nil, []byte(""), []byte("c"))
	if string(out) != "abc" || cap(out) != 3 {
		t.Errorf("Join = %q cap %d, want \"abc\" in one exact allocation", out, cap(out))
	}
	if out := Join(); len(out) != 0 {
		t.Errorf("Join() = %q", out)
	}
}

// --- property checks against encoding/json ------------------------------

// entry is one top-level key as encoding/json decodes it.
type entry struct {
	key   string
	value json.RawMessage
}

// decodeEntries returns the top-level entries in order, duplicates included,
// which map decoding would hide.
func decodeEntries(t *testing.T, body []byte) []entry {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %s", body)
	}
	var out []entry
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{tok.(string), v})
	}
	return out
}

// gen builds random valid JSON, rich in the things a byte walk gets wrong:
// strings holding braces, brackets, quotes, backslashes and commas, nested
// containers, and whitespace between every token.
type gen struct{ r *rand.Rand }

var awkward = []string{"", "plain", `{`, `}`, `[`, `]`, `"`, `\`, `\"`, `,`, `:`, `"}`, `\\"`, "é ✓", "\n\t", `{"a":"b"}`, `<&>`}

func (g gen) ws() string {
	return []string{"", "", "", " ", "\n", "\t ", "\r\n  "}[g.r.Intn(7)]
}

func (g gen) str() string {
	b, _ := json.Marshal(awkward[g.r.Intn(len(awkward))] + awkward[g.r.Intn(len(awkward))])
	return string(b)
}

func (g gen) value(depth int) string {
	n := 7
	if depth >= 3 {
		n = 5
	}
	switch g.r.Intn(n) {
	case 0:
		return g.str()
	case 1:
		return []string{"0", "-1", "3.25", "1e9", "-0.5E-3"}[g.r.Intn(5)]
	case 2:
		return []string{"true", "false", "null"}[g.r.Intn(3)]
	case 3:
		return g.str()
	case 4:
		return "12"
	case 5:
		var parts []string
		for i := g.r.Intn(4); i > 0; i-- {
			parts = append(parts, g.ws()+g.value(depth+1)+g.ws())
		}
		return "[" + strings.Join(parts, ",") + g.ws() + "]"
	default:
		var parts []string
		for i := g.r.Intn(4); i > 0; i-- {
			parts = append(parts, g.ws()+g.key()+g.ws()+":"+g.ws()+g.value(depth+1)+g.ws())
		}
		return "{" + strings.Join(parts, ",") + g.ws() + "}"
	}
}

// key draws from names that collide with "system" in every way that matters:
// itself, prefixes and extensions of it, and escaped spellings.
func (g gen) key() string {
	keys := []string{`"system"`, `"system"`, `"messages"`, `"model"`, `"sys"`, `"systemx"`, `"a"`, `""`,
		`"\u0073ystem"`, `"syst\"m"`, `"\n"`}
	return keys[g.r.Intn(len(keys))]
}

func (g gen) object() []byte {
	var parts []string
	for i := g.r.Intn(5); i > 0; i-- {
		parts = append(parts, g.ws()+g.key()+g.ws()+":"+g.ws()+g.value(0)+g.ws())
	}
	return []byte(g.ws() + "{" + strings.Join(parts, ",") + g.ws() + "}" + g.ws())
}

func TestSpansAgreeWithEncodingJSON(t *testing.T) {
	g := gen{rand.New(rand.NewSource(1))}
	const key = "system"
	refused, found := 0, 0
	for i := 0; i < 5000; i++ {
		body := g.object()
		if !json.Valid(body) {
			t.Fatalf("generator produced invalid JSON: %s", body)
		}
		entries := decodeEntries(t, body)
		var matches []entry
		for _, e := range entries {
			if e.key == key {
				matches = append(matches, e)
			}
		}

		keyStart, start, end, gotFound, ok := TopLevelEntry(body, key)
		label := fmt.Sprintf("case %d: %s", i, body)

		if len(matches) > 1 && ok && gotFound {
			t.Fatalf("%s\nduplicated key reported as found once", label)
		}
		if !ok {
			refused++
			// Refusal is only allowed for a reason the doc names: a duplicate
			// or an escaped key that might be this one.
			if len(matches) <= 1 && !strings.Contains(string(body), `\`) {
				t.Fatalf("%s\nrefused a body with no duplicate and no escape", label)
			}
			continue
		}
		if gotFound != (len(matches) == 1) {
			t.Fatalf("%s\nfound = %v, encoding/json sees %d", label, gotFound, len(matches))
		}
		if !gotFound {
			if _, ok := RemoveTopLevel(body, key); ok {
				t.Fatalf("%s\nremoved an absent key", label)
			}
			continue
		}
		found++
		if !bytes.Equal(body[start:end], matches[0].value) {
			t.Fatalf("%s\nspan %q, encoding/json %q", label, body[start:end], matches[0].value)
		}
		if !bytes.HasPrefix(body[keyStart:], []byte(`"`+key+`"`)) {
			t.Fatalf("%s\nkey offset %d does not start the key", label, keyStart)
		}

		// Replacing gives what encoding/json would see with that one value
		// changed, and nothing else moved.
		replaced, ok := ReplaceTopLevel(body, key, []byte(`{"swapped":[1]}`))
		if !ok || !json.Valid(replaced) {
			t.Fatalf("%s\nreplace = %q, %v", label, replaced, ok)
		}
		after := decodeEntries(t, replaced)
		if len(after) != len(entries) {
			t.Fatalf("%s\nreplace changed the entry count", label)
		}
		for j := range entries {
			want := entries[j].value
			if entries[j].key == key {
				want = json.RawMessage(`{"swapped":[1]}`)
			}
			if after[j].key != entries[j].key || !bytes.Equal(after[j].value, want) {
				t.Fatalf("%s\nreplace: entry %d = %s:%s", label, j, after[j].key, after[j].value)
			}
		}

		// Removing gives exactly the other entries, in order, and valid JSON.
		removed, ok := RemoveTopLevel(body, key)
		if !ok || !json.Valid(removed) {
			t.Fatalf("%s\nremove = %q, %v", label, removed, ok)
		}
		rest := decodeEntries(t, removed)
		var want []entry
		for _, e := range entries {
			if e.key != key {
				want = append(want, e)
			}
		}
		if len(rest) != len(want) {
			t.Fatalf("%s\nremove left %d entries, want %d: %s", label, len(rest), len(want), removed)
		}
		for j := range want {
			if rest[j].key != want[j].key || !bytes.Equal(rest[j].value, want[j].value) {
				t.Fatalf("%s\nremove: entry %d = %s:%s", label, j, rest[j].key, rest[j].value)
			}
		}
	}
	// Guard against a generator that stops exercising the interesting paths.
	if found < 500 || refused < 200 {
		t.Errorf("found %d, refused %d: the generator is not covering both paths", found, refused)
	}
}

// Every array the generator builds, wherever it sits, must split into the
// same elements encoding/json sees.
func TestArrayElementsAgreesWithEncodingJSON(t *testing.T) {
	g := gen{rand.New(rand.NewSource(2))}
	checked := 0
	for i := 0; i < 3000; i++ {
		v := g.ws() + "[" + g.ws()
		var parts []string
		for n := g.r.Intn(5); n > 0; n-- {
			parts = append(parts, g.value(0)+g.ws())
		}
		v += strings.Join(parts, ","+g.ws()) + "]"
		body := []byte(`{"messages":` + v + `}`)
		var want []json.RawMessage
		if err := json.Unmarshal([]byte(v), &want); err != nil {
			t.Fatalf("generator produced invalid JSON: %s", v)
		}
		start, end, found, ok := TopLevelValue(body, "messages")
		if !ok || !found {
			t.Fatalf("messages not found in %s", body)
		}
		spans, ok := ArrayElements(body, start, end)
		if !ok || len(spans) != len(want) {
			t.Fatalf("%s: %d spans ok=%v, encoding/json sees %d", body, len(spans), ok, len(want))
		}
		for j, s := range spans {
			if !bytes.Equal(body[s[0]:s[1]], want[j]) {
				t.Fatalf("%s: element %d = %q, want %q", body, j, body[s[0]:s[1]], want[j])
			}
		}
		checked += len(spans)
	}
	if checked < 1000 {
		t.Errorf("only %d elements checked", checked)
	}
}
