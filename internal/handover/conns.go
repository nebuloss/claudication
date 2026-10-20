package handover

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Conns follows a server's connections, for the two ways of letting go of
// them without a client seeing an error.
//
// A stop calls Shutdown, which drops a connection accepted before it whose
// request finishes arriving after it: the server checks for shutdown between
// reading a request and serving it (net/http, conn.serve). Settle waits for
// every accepted connection to have handed its first request to a handler
// first. Measured: one EOF per handover under a tight request loop, every
// time, before it.
//
// A handover must not close an idle keep-alive connection at all. The client
// owns that connection and may be writing a request on it at the instant it
// closes — a POST it cannot safely resend — and Shutdown, and
// SetKeepAlivesEnabled(false), close every idle one at once. Drain instead
// leaves them open, answers the next request on each with Connection: close
// so the client opens its next one to the worker taking over, and waits for
// every connection to be closed — by that, by the client, or by the server's
// idle timeout — before the caller shuts down what is left.
type Conns struct {
	pending  atomic.Int64
	open     atomic.Int64
	draining atomic.Bool
	mu       sync.Mutex
	conns    map[net.Conn]*trackedConn
}

type trackedConn struct{ served atomic.Bool }

type connKey struct{}

// Track wraps srv's handler and connection hooks to count. Call it before
// srv serves anything; it keeps any ConnState or ConnContext srv already had.
func Track(srv *http.Server) *Conns {
	c := &Conns{conns: map[net.Conn]*trackedConn{}}
	prevCtx, prevState, next := srv.ConnContext, srv.ConnState, srv.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	// Called for every accepted connection before its state is New, so the
	// counts are never behind what Serve has accepted.
	srv.ConnContext = func(ctx context.Context, nc net.Conn) context.Context {
		if prevCtx != nil {
			ctx = prevCtx(ctx, nc)
		}
		tc := &trackedConn{}
		c.pending.Add(1)
		c.open.Add(1)
		c.mu.Lock()
		c.conns[nc] = tc
		c.mu.Unlock()
		return context.WithValue(ctx, connKey{}, tc)
	}
	srv.ConnState = func(nc net.Conn, st http.ConnState) {
		if st == http.StateClosed || st == http.StateHijacked {
			c.mu.Lock()
			tc := c.conns[nc]
			delete(c.conns, nc)
			c.mu.Unlock()
			if tc != nil {
				c.serve(tc)
				c.open.Add(-1)
			}
		}
		if prevState != nil {
			prevState(nc, st)
		}
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tc, ok := r.Context().Value(connKey{}).(*trackedConn); ok {
			c.serve(tc)
		}
		if c.draining.Load() {
			// The server closes the connection after this response, and the
			// client, told so, opens its next one elsewhere.
			w.Header().Set("Connection", "close")
		}
		next.ServeHTTP(w, r)
	})
	return c
}

func (c *Conns) serve(tc *trackedConn) {
	if tc.served.CompareAndSwap(false, true) {
		c.pending.Add(-1)
	}
}

// Settle waits until no accepted connection is still short of a handler, or
// ctx is done. A client that connects and sends nothing holds it for as long
// as ctx allows, which is why the caller bounds it.
func (c *Conns) Settle(ctx context.Context) {
	wait(ctx, func() bool { return c.pending.Load() == 0 })
}

// Drain answers every request from now on with Connection: close and waits
// until every connection the server has open is closed, or ctx is done. Call
// it with the listeners closed and their accept loops returned, so the count
// cannot grow.
func (c *Conns) Drain(ctx context.Context) {
	c.draining.Store(true)
	wait(ctx, func() bool { return c.open.Load() == 0 })
}

// Open is how many connections the server has open now.
func (c *Conns) Open() int64 { return c.open.Load() }

func wait(ctx context.Context, done func() bool) {
	for !done() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}
