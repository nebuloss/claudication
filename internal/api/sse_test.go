package api

import (
	"bytes"
	"testing"
)

func TestFramesAreReadWholeAsTheyArrive(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("event: a\ndata: {\"x\":")
	if _, ok := NextFrame(&buf); ok {
		t.Fatal("a frame read before its blank line")
	}
	buf.WriteString("1}\n\n: ping\r\n\r\nevent: b\r\ndata: one\r\ndata: two\r\n\r\nrest")
	var got []string
	for {
		f, ok := NextFrame(&buf)
		if !ok {
			break
		}
		ev, data := FrameFields(f)
		got = append(got, ev+"="+string(data))
	}
	want := []string{`a={"x":1}`, "=", "b=one\ntwo"}
	if len(got) != len(want) {
		t.Fatalf("frames = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}
	if buf.String() != "rest" {
		t.Errorf("left %q", buf.String())
	}
}
