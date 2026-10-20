package handover

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Pending counts the connections a server has accepted whose first request
// has not reached the handler yet.
//
// It exists because http.Server.Shutdown drops them. A connection accepted
// before Shutdown whose request finishes arriving after it is closed unread:
// the server checks for shutdown between reading a request and serving it
// (net/http, conn.serve). With the sockets still open in the worker taking
// over, that connection was never refused — it was accepted here and then
// answered with an EOF. Measured: one connection per handover under a tight
// request loop, every time, before Settle.
//
// So a draining server stops accepting, waits for Settle — every accepted
// connection has had its request handed to a handler, or gone — and only then
// calls Shutdown, which waits for the handlers.
type Pending struct {
	n     atomic.Int64
	mu    sync.Mutex
	conns map[net.Conn]*pendingConn
}

type pendingConn struct{ done atomic.Bool }

type pendingKey struct{}

// Track wraps srv's handler and connection hooks to count. Call it before
// srv serves anything; it keeps any ConnState or ConnContext srv already had.
func Track(srv *http.Server) *Pending {
	p := &Pending{conns: map[net.Conn]*pendingConn{}}
	prevCtx, prevState, next := srv.ConnContext, srv.ConnState, srv.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	// Called for every accepted connection before its state is New, so the
	// count is never behind what Serve has accepted.
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if prevCtx != nil {
			ctx = prevCtx(ctx, c)
		}
		pc := &pendingConn{}
		p.n.Add(1)
		p.mu.Lock()
		p.conns[c] = pc
		p.mu.Unlock()
		return context.WithValue(ctx, pendingKey{}, pc)
	}
	srv.ConnState = func(c net.Conn, st http.ConnState) {
		if st == http.StateClosed || st == http.StateHijacked {
			p.mu.Lock()
			pc := p.conns[c]
			delete(p.conns, c)
			p.mu.Unlock()
			if pc != nil {
				p.settle(pc)
			}
		}
		if prevState != nil {
			prevState(c, st)
		}
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pc, ok := r.Context().Value(pendingKey{}).(*pendingConn); ok {
			p.settle(pc)
		}
		next.ServeHTTP(w, r)
	})
	return p
}

func (p *Pending) settle(pc *pendingConn) {
	if pc.done.CompareAndSwap(false, true) {
		p.n.Add(-1)
	}
}

// Settle waits until no accepted connection is still short of a handler, or
// ctx is done. A client that connects and sends nothing holds it for as long
// as ctx allows, which is why the caller bounds it.
func (p *Pending) Settle(ctx context.Context) {
	for p.n.Load() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}
