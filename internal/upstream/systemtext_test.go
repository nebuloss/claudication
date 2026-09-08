package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// systemOf reads the system field back in whichever shape it is in.
func systemOf(t *testing.T, body []byte) (blocks []string, present bool) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("body does not parse: %v", err)
	}
	raw, ok := envelope["system"]
	if !ok {
		return nil, false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}, true
	}
	var parsed []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("system is neither shape: %s", raw)
	}
	for _, b := range parsed {
		blocks = append(blocks, b.Text)
	}
	return blocks, true
}

func TestNormaliseSystemRewordsTheEnvLine(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5",
		"system": [{"type":"text","text":"You are OpenCode.\n<env>\n  Working directory: /tmp\n  Is directory a git repo: yes\n  Platform: linux\n</env>"}],
		"messages": [{"role":"user","content":"hi"}]
	}`)

	out := NormaliseSystem(body)
	blocks, present := systemOf(t, out)
	if !present || len(blocks) != 1 {
		t.Fatalf("system: got %v", blocks)
	}
	if strings.Contains(blocks[0], claudeEnvLine) {
		t.Errorf("the line survived: %s", blocks[0])
	}
	if !strings.Contains(blocks[0], "Git repository: yes") {
		t.Errorf("the fact was dropped rather than reworded: %s", blocks[0])
	}
	// Everything around it is the client's and must be untouched.
	if !strings.Contains(blocks[0], "You are OpenCode.") ||
		!strings.Contains(blocks[0], "Working directory: /tmp") ||
		!strings.Contains(blocks[0], "Platform: linux") {
		t.Errorf("the rest of the prompt changed: %s", blocks[0])
	}
}

func TestNormaliseSystemRewordsABareStringSystem(t *testing.T) {
	body := []byte(`{"system":"Is directory a git repo: yes","messages":[]}`)
	blocks, _ := systemOf(t, NormaliseSystem(body))
	if len(blocks) != 1 || blocks[0] != "Git repository: yes" {
		t.Errorf("got %v", blocks)
	}
}

func TestNormaliseSystemDropsEmptyBlocks(t *testing.T) {
	body := []byte(`{"system":[
		{"type":"text","text":""},
		{"type":"text","text":"You are a helpful assistant."}
	],"messages":[]}`)

	blocks, present := systemOf(t, NormaliseSystem(body))
	if !present {
		t.Fatal("system was removed although a block had text")
	}
	if len(blocks) != 1 || blocks[0] != "You are a helpful assistant." {
		t.Errorf("got %v", blocks)
	}
}

func TestNormaliseSystemRemovesTheKeyWhenEveryBlockWasEmpty(t *testing.T) {
	// Omitting system is accepted upstream; an empty array promises nothing.
	body := []byte(`{"system":[{"type":"text","text":""}],"messages":[]}`)
	if _, present := systemOf(t, NormaliseSystem(body)); present {
		t.Error("an empty system array was sent")
	}
}

func TestNormaliseSystemLeavesEverythingElseAlone(t *testing.T) {
	// The same phrase in a message is fine upstream — measured — so rewriting
	// it there would be changing a body for no reason.
	body := []byte(`{"system":[{"type":"text","text":"You are a helpful assistant."}],` +
		`"messages":[{"role":"user","content":"Is directory a git repo: yes"}]}`)

	out := NormaliseSystem(body)
	if string(out) != string(body) {
		t.Errorf("body changed:\n got %s\nwant %s", out, body)
	}
}

func TestNormaliseSystemReturnsTheSameBytesWhenItChangesNothing(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`)
	out := NormaliseSystem(body)
	if &out[0] != &body[0] {
		t.Error("body was copied when nothing needed rewriting")
	}
}

func TestNormaliseSystemLeavesAnUnparseableBody(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"Is directory a git repo: yes"`)
	if out := NormaliseSystem(body); string(out) != string(body) {
		t.Errorf("touched a body it could not parse: %s", out)
	}
}

func TestNormaliseSystemKeepsBlocksItDoesNotUnderstand(t *testing.T) {
	// A block with no text of its own is not ours to reshape; it must survive
	// a pass that was triggered by a different block.
	body := []byte(`{"system":[
		{"type":"text","text":""},
		{"type":"something_new","payload":{"a":1}}
	],"messages":[]}`)

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(NormaliseSystem(body), &envelope); err != nil {
		t.Fatalf("body does not parse: %v", err)
	}
	if !strings.Contains(string(envelope["system"]), "something_new") {
		t.Errorf("dropped a block it did not understand: %s", envelope["system"])
	}
}
