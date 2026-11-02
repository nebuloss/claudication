package relay

import (
	"context"
	"io"
	"net/http"
)

// Tap is told what each attempt sent and what came back, for an operator
// recording a client's content (internal/service/capture). It watches and
// never changes: the body it is handed is the one sent, and the answer it
// wraps reads the same bytes in the same order.
type Tap interface {
	// Sent is an attempt's request as it goes upstream, after the passes,
	// the version rebind and the wire's stamp: the bytes on the wire.
	Sent(account string, req *http.Request, body []byte)
	// Failed is why the attempt got no answer.
	Failed(err error)
	// Answered is the attempt's answer, returning the body to read in its
	// place.
	Answered(status int, header http.Header, body io.ReadCloser) io.ReadCloser
}

type tapKey struct{}

// WithTap hands the relay a request's Tap.
func WithTap(ctx context.Context, t Tap) context.Context {
	return context.WithValue(ctx, tapKey{}, t)
}

// tapOf is the request's Tap, or one that does nothing.
func tapOf(ctx context.Context) Tap {
	if t, ok := ctx.Value(tapKey{}).(Tap); ok && t != nil {
		return t
	}
	return noTap{}
}

type noTap struct{}

func (noTap) Sent(string, *http.Request, []byte)                           {}
func (noTap) Failed(error)                                                 {}
func (noTap) Answered(_ int, _ http.Header, b io.ReadCloser) io.ReadCloser { return b }
