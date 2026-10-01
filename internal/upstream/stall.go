package upstream

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// Holding the start of a stream, and retrying one that never starts.
//
// The relay's rule is that bytes go to the client as they arrive, and this does
// not change a byte. It changes when the first few are sent, for one reason
// measured on this gateway's own traffic (2026-10-01, one account, up to five
// requests in flight):
//
//	request   bytes   output tokens   duration   how it ended
//	08:05     1033    2               306 s      client gave up
//	08:33     1979    17              307 s      client gave up
//	08:58     1042    4               307 s      client gave up
//	09:13     1033    2               308 s      client gave up
//	07:56     1063    3               191 s      event: error, overloaded_error
//
// Every one opened normally — a 200, message_start — and then produced nothing,
// while the requests running beside it on the same account finished at the
// usual ~60 output tokens a second. The upstream had accepted the request and
// was not working on it. The byte counts are what the gateway itself received,
// so neither the proxy in front nor the client was holding anything back.
//
// Once message_start has been relayed, nothing can be done about that: the
// response is committed and a second attempt would put a second message_start
// in front of the client. So for a streaming 200 the opening events —
// message_start, content_block_start, ping — are held until the first event
// that carries content. That one releases everything held, in order and
// unchanged, and from there it is the same byte copy as always. If none comes
// within the stall timeout, or the upstream sends an error first, the attempt is
// abandoned and the request is sent again; the client has received nothing, so
// it cannot tell.
//
// What it costs: the client sees its first byte when the first token exists
// rather than a moment earlier, which no client measured treats differently.
// Claude Code warns about a slow first byte at 30 s, and its watchdogs sit at
// 300 s. A request that legitimately goes silent before its first token for
// longer than the timeout — a vast uncached prompt — is sent twice, once; the
// retry is not held, so it can take as long as it needs.
//
// The account is not cooled down for a stall. The other requests in flight on
// it were fine, so it is the one that most likely serves the retry well, and
// with one account connected it is the only one there is.
//
// # Releasing on the first token was too early
//
// The same day, with the hold in place, the stalls moved by one event (09:49 to
// 13:40, 1,369 requests, not one caught):
//
//	request   bytes   output tokens   first token   duration   how it ended
//	11:54     1253    16              21.8 s        322 s      client gave up
//	12:10     1273    13               3.3 s        303 s      client gave up
//	12:46     2048    17               8.0 s        248 s      overloaded_error
//	13:29     1276    17               7.2 s        307 s      client gave up
//	13:31     1261    16               9.9 s        310 s      client gave up
//
// Each sent exactly one delta and then nothing — the first delta is what
// released the hold, so every one of them was already the client's by the
// time it stalled. crush gives up on a stream that has yielded no part for its
// request timeout, which pings do not reset, and says the model is not
// responding.
//
// So the hold waits for the stream to be flowing, not merely started: a few
// content events (holdContentEvents), or one that settles the answer
// (content_block_stop, message_delta, message_stop — a reply short enough to
// finish in fewer). Healthy deltas arrive milliseconds apart, so the extra
// wait is nothing; a stream whose content stops for progressGap while still
// held is a stall, and is sent again like one that never started.
//
// Not a watchdog after release. Once the client has bytes a retry is no
// longer invisible, and a silent pause mid-answer is legitimate — long
// thinking sends nothing but pings.

// holdContentEvents is how many content events show a stream is flowing.
const holdContentEvents = 3

// progressGap is how long content may stop while the opening is still held.
// A variable so the tests need not wait it out.
var progressGap = 30 * time.Second

// quietEvent reports whether an SSE event name carries no content: the ones
// the upstream sends before it has produced anything.
func quietEvent(name []byte) bool {
	switch string(name) {
	case "message_start", "content_block_start", "ping":
		return true
	}
	return false
}

// settlingEvent reports whether an event finishes a block or the answer: a
// stream that has got this far is not stalled at its start.
func settlingEvent(name []byte) bool {
	switch string(name) {
	case "content_block_stop", "message_delta", "message_stop":
		return true
	}
	return false
}

// maxHeld bounds what is held while waiting. The opening events are a few
// hundred bytes; a body that reaches this without one content event is not a
// Messages stream this understands, and is let through.
const maxHeld = 64 << 10

type holdOutcome uint8

const (
	// holdProgressed: content arrived, or the body ended, or it was not a
	// stream this could read. Relay what was held and carry on.
	holdProgressed holdOutcome = iota
	// holdStalled: nothing but opening events within the timeout.
	holdStalled
	// holdErrored: the upstream sent an error event before any content.
	holdErrored
)

func (o holdOutcome) String() string {
	switch o {
	case holdStalled:
		return "stalled"
	case holdErrored:
		return "errored"
	}
	return "progressed"
}

// pumped is one read from the upstream body. buf is the pooled buffer b was
// read into, handed back once b has been consumed.
type pumped struct {
	b   []byte
	buf *[]byte
	err error
}

// pumpBuffers recycles read buffers. Without it every chunk of every stream
// was a fresh 32 KB — 170 MB allocated in a short load test, all of it
// garbage the moment the chunk had been copied on.
var pumpBuffers = sync.Pool{New: func() any {
	b := make([]byte, 32*1024)
	return &b
}}

func (c pumped) release() {
	if c.buf != nil {
		pumpBuffers.Put(c.buf)
	}
}

// pump reads a body on its own goroutine, so that waiting for the next chunk
// can have a deadline — a plain Read cannot be given one. Once the hold is
// over it is an io.Reader like any other, so the relay downstream of it is
// unchanged.
type pump struct {
	ch   chan pumped
	stop chan struct{}
	once sync.Once
	cur  pumped
	err  error
}

func startPump(body io.Reader) *pump {
	p := &pump{ch: make(chan pumped), stop: make(chan struct{})}
	go func() {
		for {
			buf := pumpBuffers.Get().(*[]byte)
			n, err := body.Read(*buf)
			if n > 0 {
				select {
				case p.ch <- pumped{b: (*buf)[:n], buf: buf}:
				case <-p.stop:
					pumpBuffers.Put(buf)
					return
				}
			} else {
				pumpBuffers.Put(buf)
			}
			if err != nil {
				select {
				case p.ch <- pumped{err: err}:
				case <-p.stop:
				}
				return
			}
		}
	}()
	return p
}

// next waits for the next chunk until the deadline. ok is false on timeout.
func (p *pump) next(deadline <-chan time.Time) (c pumped, ok bool) {
	select {
	case c = <-p.ch:
		return c, true
	case <-deadline:
		return pumped{}, false
	}
}

func (p *pump) Read(b []byte) (int, error) {
	for len(p.cur.b) == 0 {
		p.cur.release()
		p.cur = pumped{}
		if p.err != nil {
			return 0, p.err
		}
		c := <-p.ch
		p.cur, p.err = c, c.err
	}
	n := copy(b, p.cur.b)
	p.cur.b = p.cur.b[n:]
	return n, nil
}

// abandon lets the reading goroutine go. The body must also be closed, or a
// Read already in progress stays blocked on the network.
func (p *pump) abandon() { p.once.Do(func() { close(p.stop) }) }

// heldBody is what the relay reads after a hold: the held bytes, then the rest
// of the stream through the pump.
type heldBody struct {
	io.Reader
	body io.Closer
	pump *pump
}

func (h *heldBody) Close() error {
	err := h.body.Close()
	h.pump.abandon()
	return err
}

// holdUntilContent reads the start of a stream until it carries content, ends,
// errors, or the timeout passes. On holdProgressed the returned body replays
// everything read so far and then the rest; on the other outcomes the body has
// been closed and the caller should abandon the attempt.
func holdUntilContent(body io.ReadCloser, timeout time.Duration) (io.ReadCloser, holdOutcome) {
	p := startPump(body)
	deadline := time.Now().Add(timeout)

	var held []byte
	var sc heldScan
	finish := func(o holdOutcome) (io.ReadCloser, holdOutcome) {
		if o != holdProgressed {
			_ = body.Close()
			p.abandon()
			return nil, o
		}
		return &heldBody{Reader: io.MultiReader(bytes.NewReader(held), p), body: body, pump: p}, o
	}

	for {
		// The overall limit, or — once content has started — the gap it may
		// leave, whichever comes first.
		until := deadline
		if sc.content > 0 {
			if gap := sc.lastContent.Add(progressGap); gap.Before(until) {
				until = gap
			}
		}
		timer := time.NewTimer(time.Until(until))
		c, ok := p.next(timer.C)
		timer.Stop()
		if !ok {
			return finish(holdStalled)
		}
		if len(c.b) > 0 {
			held = append(held, c.b...)
			c.release() // copied into held; the buffer can go round again
			o, decided := classifyHeld(held, &sc, time.Now())
			if decided {
				return finish(o)
			}
			if len(held) >= maxHeld {
				return finish(holdProgressed)
			}
		}
		if c.err != nil {
			// The stream ended before any content. Relay it as it is: an empty
			// answer is still an answer, and the error, if any, is the
			// relay's to report.
			p.err = c.err
			return finish(holdProgressed)
		}
	}
}

// heldScan is how far classifyHeld has read, and what it has seen.
type heldScan struct {
	scanned     int
	content     int       // content events seen
	lastContent time.Time // when the latest one arrived
}

// classifyHeld reads the complete lines of held not yet scanned and decides,
// if it can: an error before the stream is flowing, or enough content — or a
// settling event — to show that it is.
func classifyHeld(held []byte, sc *heldScan, now time.Time) (holdOutcome, bool) {
	for {
		i := bytes.IndexByte(held[sc.scanned:], '\n')
		if i < 0 {
			return holdProgressed, false
		}
		line := bytes.TrimSuffix(held[sc.scanned:sc.scanned+i], []byte("\r"))
		sc.scanned += i + 1
		if !bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		name := bytes.TrimSpace(line[len("event:"):])
		switch {
		case quietEvent(name):
			continue
		case bytes.Equal(name, []byte("error")):
			return holdErrored, true
		case settlingEvent(name):
			return holdProgressed, true
		default:
			sc.content++
			sc.lastContent = now
			if sc.content >= holdContentEvents {
				return holdProgressed, true
			}
		}
	}
}
