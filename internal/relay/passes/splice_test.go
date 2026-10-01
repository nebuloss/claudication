package passes

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"claudication/internal/request"
)

// rebuild is the old way, kept here as the oracle: decode the envelope,
// prepend the block, re-encode. The splice must mean exactly the same.
func rebuild(t *testing.T, body string) any {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	blocks, ok := systemBlocks(envelope["system"])
	if !ok {
		t.Fatalf("oracle cannot read system in %s", body)
	}
	sys, _ := json.Marshal(append([]json.RawMessage{attributionBlock}, blocks...))
	envelope["system"] = sys
	out, _ := json.Marshal(envelope)
	var v any
	_ = json.Unmarshal(out, &v)
	return v
}

func TestSpliceMeansWhatTheRebuildMeant(t *testing.T) {
	for name, body := range map[string]string{
		"no system":      `{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		"empty object":   `{}`,
		"string":         `{"model":"m","system":"You are helpful.","messages":[]}`,
		"escaped string": `{"system":"line one\nhe said \"]}\" and left","messages":[]}`,
		"array":          `{"system":[{"type":"text","text":"a"},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}`,
		"empty array":    `{"system":[],"model":"m"}`,
		"null":           `{"system":null,"model":"m"}`,
		"pretty":         "{\n  \"model\": \"m\",\n  \"system\" : [ {\"type\":\"text\",\"text\":\"x\"} ],\n  \"stream\": true\n}",
		"system last":    `{"messages":[{"role":"user","content":"the word \"system\": [ appears here }"}],"system":"s"}`,
		"nested system":  `{"messages":[{"role":"user","content":[{"type":"text","text":"x","system":[1]}]}],"model":"m"}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, ok := spliceAttribution([]byte(body))
			if !ok {
				t.Fatalf("splice refused %s", body)
			}
			var got any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("not valid JSON: %v\n%s", err, out)
			}
			if want := rebuild(t, body); !reflect.DeepEqual(got, want) {
				t.Errorf("splice and rebuild disagree\n got %s\nbody %s", out, body)
			}
		})
	}
}

// The point of splicing: everything but the system field is the client's own
// bytes, in the client's order — not re-encoded, not re-sorted.
func TestSpliceLeavesTheRestByteForByte(t *testing.T) {
	messages := `"messages":[ {"role":"user", "content":"keep   my   spacing é"} ]`
	body := `{"model":"m",` + messages + `,"system":[{"type":"text","text":"x"}],"zeta":1}`
	out, ok := spliceAttribution([]byte(body))
	if !ok {
		t.Fatal("refused")
	}
	if !strings.HasPrefix(string(out), `{"model":"m",`+messages+`,"system":[`+string(attributionBlock)+`,{"type":"text","text":"x"}],"zeta":1}`) {
		t.Errorf("bytes outside system changed:\n%s", out)
	}
}

// Shapes it will not guess at go to the slow path, which has its own answer.
func TestSpliceRefusesWhatItCannotPlace(t *testing.T) {
	for _, body := range []string{
		`{"system":"a","system":"b"}`, // encoding/json keeps the last; not ours to pick
		// The key with its "t" written as a unicode escape. Built from parts
		// so nothing on the way to this file can collapse the escape.
		`{"sys` + `\` + `u0074em":"a"}`,
		`{"system":42}`,
		`[]`,
	} {
		if _, ok := spliceAttribution([]byte(body)); ok {
			t.Errorf("spliced %s", body)
		}
	}
}

// And through EnsureAttribution itself: one allocation the size of the body,
// not the six the rebuild cost.
func TestEnsureAttributionAllocatesOnce(t *testing.T) {
	body := []byte(`{"model":"m","system":"s","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 1<<20) + `"}]}`)
	p := request.Peek(body)
	allocated := testing.AllocsPerRun(5, func() { _ = EnsureAttribution(body, p) })
	// systemBlocks decodes the small system field; the body itself is copied
	// once. A few dozen small allocations at most; the byte count below is
	// the check that matters.
	if allocated > 50 {
		t.Errorf("%v allocations per call", allocated)
	}
	before := totalAlloc()
	_ = EnsureAttribution(body, p)
	if grew := totalAlloc() - before; grew > 2*uint64(len(body)) {
		t.Errorf("allocated %d bytes for a %d byte body", grew, len(body))
	}
}

func totalAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}
