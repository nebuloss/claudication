package upstream

import (
	"io"
	"testing"
)

// The request body lets go of its bytes once read, so the request — which
// lives as long as the response — does not keep them.
func TestSentBodyReleasesItsBytes(t *testing.T) {
	s := &sentBody{b: []byte("hello world")}
	got, err := io.ReadAll(s)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("read %q, %v", got, err)
	}
	if s.b != nil {
		t.Error("bytes still referenced after EOF")
	}
}
