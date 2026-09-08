package upstream

import (
	"encoding/json"
	"testing"
)

// contentOf reads each message's content blocks back, whatever shape it is in.
func contentOf(t *testing.T, body []byte) [][]string {
	t.Helper()
	var envelope struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("body does not parse: %v", err)
	}
	var out [][]string
	for _, m := range envelope.Messages {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			out = append(out, nil) // a bare string
			continue
		}
		var texts []string
		for _, b := range blocks {
			texts = append(texts, b.Type+":"+b.Text)
		}
		out = append(out, texts)
	}
	return out
}

// One empty block is a 400 even with a good one beside it — measured — so the
// good one has to survive on its own.
func TestDropEmptyMessageTextRemovesTheEmptyBlock(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":""},
		{"type":"text","text":"say OK"}
	]}]}`)

	got := contentOf(t, DropEmptyMessageText(body))
	if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "text:say OK" {
		t.Errorf("content = %v, want just the non-empty block", got)
	}
}

func TestDropEmptyMessageTextKeepsBlocksThatAreNotEmptyText(t *testing.T) {
	// An empty tool_result is accepted upstream, so it is not ours to drop,
	// and neither is a shape we do not recognise.
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":""},
		{"type":"text","text":""},
		{"type":"something_new","payload":{"a":1}}
	]}]}`)

	out := DropEmptyMessageText(body)
	got := contentOf(t, out)
	if len(got[0]) != 2 {
		t.Fatalf("content = %v, want the tool_result and the unknown block kept", got)
	}
	if string(out) == string(body) {
		t.Error("the empty text block was not dropped")
	}
}

// Emptying the array trades one refusal for another, and inventing filler is
// not the gateway's decision.
func TestDropEmptyMessageTextLeavesAMessageThatIsAllEmpty(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`)
	if out := DropEmptyMessageText(body); string(out) != string(body) {
		t.Errorf("body changed: %s", out)
	}
}

func TestDropEmptyMessageTextLeavesABareStringContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":""}]}`)
	if out := DropEmptyMessageText(body); string(out) != string(body) {
		t.Errorf("body changed: %s", out)
	}
}

func TestDropEmptyMessageTextReturnsTheSameBytesWhenItChangesNothing(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out := DropEmptyMessageText(body)
	if &out[0] != &body[0] {
		t.Error("body was copied when nothing needed dropping")
	}
}

func TestDropEmptyMessageTextOnlyTouchesTheMessageThatNeedsIt(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"first"}]},
		{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"second"}]}
	]}`)

	got := contentOf(t, DropEmptyMessageText(body))
	if len(got) != 2 {
		t.Fatalf("messages = %v", got)
	}
	if len(got[0]) != 1 || got[0][0] != "text:first" {
		t.Errorf("the untouched message changed: %v", got[0])
	}
	if len(got[1]) != 1 || got[1][0] != "text:second" {
		t.Errorf("second message = %v, want the empty block gone", got[1])
	}
}
