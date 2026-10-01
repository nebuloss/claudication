package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/relay/passes"
	"claudication/internal/request"
)

// hopByHop headers belong to a single connection and must not be forwarded.
var hopByHop = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// Relay forwards a request to the provider on behalf of a pooled account.
//
// Its whole job is to be uninteresting: the request body
// goes out byte for byte, every header the client sent is preserved, the
// response status, headers and body come back untouched, and errors are
// forwarded verbatim. It never parses what it does not need to, which is what
// keeps it working with capabilities that do not exist yet.
type Relay struct {
	Pool AccountPool
	// Wire is the provider that answers: where requests go, how they are
	// authenticated, how answers are read. See internal/provider.
	Wire provider.Wire
	// Passes rewrite the body before it goes upstream — the stated
	// exceptions to relaying it untouched. passes.Default is the gateway's
	// set; nil runs none, which is strict pass-through and what a test that
	// builds a Relay from the fields it needs gets.
	//
	// A slice and not built on demand: the titler copies a Relay to pin it to
	// one account, and the copy shares the pipeline as it is.
	Passes passes.Pipeline
	// StallTimeout is how long a streaming answer may go without content
	// before the attempt is abandoned and the request sent again — see
	// stall.go. Zero turns that off and relays every byte the moment it
	// arrives, as before.
	StallTimeout time.Duration
	Client       *http.Client
	Log          *slog.Logger
	BaseURL      string
}

// logger is the relay's log, or one that discards. Log is optional — the
// tests build a Relay from the two fields they need — and a warning about a
// malformed body should not be the thing that takes the process down.
func (r *Relay) logger() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// nopMeter reads nothing, for a body we cannot interpret.
type nopMeter struct{}

func (nopMeter) Feed([]byte) {}
func (nopMeter) Done()       {}

// AccountPool is what the relay needs from the pool.
//
// An interface rather than the concrete type so the retry and refusal paths —
// the part the contract is strictest about — can be exercised
// without a database and a live provider behind them. *pool.Pool satisfies it.
type AccountPool interface {
	Acquire(ctx context.Context, provider string, exclude map[string]bool) (pool.Lease, error)
	ReportFailure(id string, kind pool.FailureKind, detail string)
	ReportSuccess(id string)
	Refresh(ctx context.Context, id string) error
}

// Result describes what happened, for logging and stats.
type Result struct {
	Status       int
	AccountID    string
	AccountEmail string
	Attempts     int
	BytesOut     int64
	Usage        provider.Usage
	// StreamError is set when the upstream reported an error mid-stream, after
	// a 200. Without this a failed stream is indistinguishable from a
	// successful one and gets recorded as a success.
	StreamError string
	// UpstreamError is what the upstream said when it refused, verbatim and
	// truncated. Distinct from Err, which is this gateway's own failure to get
	// an answer at all.
	UpstreamError string
	// Opaque marks a body we relayed but could not read: the upstream applied
	// a content coding despite being asked for identity. Usage is unknown and,
	// more to the point, a mid-stream error would have been invisible — so the
	// attempt is not credited as a success either way.
	Opaque bool
	// Stalls counts attempts abandoned because the stream never produced
	// content (or errored before it did) and were sent again. See stall.go.
	Stalls int
	// Rewrites names the passes that changed the body on its way upstream,
	// in the order they ran. Empty for a body that went up as it arrived.
	Rewrites []string
	// FirstContent is how long the caller waited, from the start of Do, for
	// the first event carrying content — the time to first token, stalled
	// attempts included. Zero when no content arrived or the response was not
	// a stream.
	FirstContent time.Duration
	Err          error

	// firstContentAt is when the scanner saw that event.
	firstContentAt time.Time
}

// maxAttempts bounds how many accounts one request is tried on. It is not a
// retry budget in the usual sense: the caller has its own and it is much
// larger. Claude Code retries up to ten times by default (fifteen if
// CLAUDE_CODE_MAX_RETRIES is raised, three hundred under its retry watchdog),
// backing off 500 ms doubling to 32 s with jitter, honouring retry-after and
// treating x-should-retry: false as final.
//
// So this only decides how many accounts to fail over to before answering.
// Raising it would spend more of the pool on one caller that is going to
// retry anyway.
const maxAttempts = 3

// maxRefusalBytes bounds the error body held while retrying. An error envelope
// is a few hundred bytes; this is slack, not a budget.
const maxRefusalBytes = 1 << 20

// refusal is an upstream response we set aside in order to try another
// account. If there is no other account, it is what the client gets: the
// upstream already said why, in the words the client knows how to read.
type refusal struct {
	status int
	header http.Header
	body   []byte
}

// replay writes a set-aside response verbatim.
func replay(w http.ResponseWriter, f *refusal, res *Result) {
	for name, values := range f.header {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(f.status)
	n, _ := w.Write(f.body)
	res.Status = f.status
	res.BytesOut = int64(n)
	res.UpstreamError = truncate(strings.TrimSpace(string(f.body)))
}

// truncate bounds error text kept for a log line or a usage row. An error
// envelope is a few hundred bytes; this is the guard for a body that is not.
func truncate(s string) string {
	if len(s) > 4096 {
		return s[:4096]
	}
	return s
}

// Do runs the request, retrying on another account when the failure is the
// account's fault rather than the caller's.
//
// Retries can only happen before anything is written to the client; once the
// first byte is relayed the response is committed, which is the price of not
// buffering.
func (r *Relay) Do(w http.ResponseWriter, req *http.Request, provider, upstreamPath string, body []byte, p request.Prologue) Result {
	base := r.BaseURL
	if base == "" {
		base = r.Wire.BaseURL()
	}

	// The rewrites, once, before the first attempt: a retry has to send the
	// same bytes. Each is a named pass — see internal/relay/passes for what
	// they are, why each exists, and the order they must run in.
	st := passes.State{Prologue: p}
	body, rewrites := r.Passes.Run(body, &st, r.logger())
	names := st.Names

	var res Result
	res.Rewrites = rewrites
	started := time.Now()
	tried := map[string]bool{}
	// Accounts whose token we have already refreshed for this request. One
	// refresh per account is a repair; a second is a loop.
	refreshed := map[string]bool{}

	// The last refusal we retried past, held so that running out of accounts
	// answers with the upstream's own words rather than ours.
	var last *refusal

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res.Attempts = attempt

		lease, err := r.Pool.Acquire(req.Context(), provider, tried)
		if err != nil {
			// Nothing left to try. If an upstream already refused this
			// request, that refusal is the answer: it names the limit and
			// carries the reset, and Claude Code decides what to do next by
			// reading it. Replacing it with our own "every connected account
			// is rate limited" throws away the only part the client can act
			// on — and hides, for instance, that a per-model weekly limit was
			// reached while every other model still works.
			if last != nil {
				replay(w, last, &res)
				return res
			}
			// Likewise a network failure: that is what went wrong, and the
			// pool having nothing left to retry on is only its consequence.
			// Answering "every account is rate limited" for an unreachable
			// upstream sent the operator looking at quotas, and dropped the
			// real error from the log line.
			if res.Err != nil && errors.Is(err, pool.ErrAllCoolingUp) {
				return res
			}
			res.Err = err
			return res
		}
		res.AccountID = lease.Account.ID
		res.AccountEmail = lease.Account.Email

		upstreamReq, err := r.build(req, base+upstreamPath, body, lease.AccessToken)
		if err != nil {
			res.Err = err
			return res
		}
		// Per attempt, so a stalled one can be torn down without touching the
		// caller's own context.
		attemptCtx, cancelAttempt := context.WithCancel(req.Context())
		upstreamReq = upstreamReq.WithContext(attemptCtx)

		resp, err := r.Client.Do(upstreamReq)
		// Answered, or failed for good: nothing will resend this body now, and
		// the request outlives the call through resp.Request.
		upstreamReq.GetBody = nil
		if err != nil {
			cancelAttempt()
			// A cancelled client is not the account's fault.
			if errors.Is(err, context.Canceled) || req.Context().Err() != nil {
				res.Err = err
				return res
			}
			r.Pool.ReportFailure(lease.Account.ID, pool.FailureNetwork, err.Error())
			tried[lease.Account.ID] = true
			res.Err = err
			continue
		}

		kind, retryable := pool.ClassifyStatus(resp.StatusCode)
		if retryable && attempt < maxAttempts {
			// Read it out rather than discarding it: nothing has reached the
			// client yet, so this response is still a usable answer if the
			// retry has nowhere to go.
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxRefusalBytes))
			resp.Body.Close()
			cancelAttempt()
			last = &refusal{
				status: resp.StatusCode,
				header: resp.Header.Clone(),
				body:   raw,
			}
			detail := strings.TrimSpace(string(raw))
			if len(detail) > 4096 {
				detail = detail[:4096]
			}

			// A 401 usually means the access token aged out rather than the
			// account being broken; refresh once and let the same account
			// serve the retry. Adding it to tried here would do the exact
			// opposite of what that sentence says — Acquire skips excluded
			// accounts — so the refresh would be spent and the account it
			// just repaired passed over. refreshed is a separate set for the
			// separate question of whether we have already tried this.
			if kind == pool.FailureAuth && !refreshed[lease.Account.ID] {
				refreshed[lease.Account.ID] = true
				if rerr := r.Pool.Refresh(req.Context(), lease.Account.ID); rerr == nil {
					continue
				}
			}
			r.Pool.ReportFailure(lease.Account.ID, kind, detail)
			tried[lease.Account.ID] = true
			continue
		}

		// Keep what the upstream said, for anything that failed. It was already
		// being read here for the pool's log line and then dropped, so a
		// request recorded as a 429 carried no hint of *which* limit, and the
		// Usage tab could show that something failed but never why. The body
		// is put back either way, so the client still gets it byte for byte.
		if resp.StatusCode >= 400 {
			res.UpstreamError = peekErrorAndRestore(resp)
		}
		if kind != "" {
			r.Pool.ReportFailure(lease.Account.ID, kind, res.UpstreamError)
		}

		// Hold the opening of a stream until it carries content, and send the
		// request again if it never does. Only while another attempt is
		// possible, once per request, and never on the retry itself.
		if r.StallTimeout > 0 && res.Stalls == 0 && attempt < maxAttempts && holdable(resp) {
			held, outcome := holdUntilContent(resp.Body, r.StallTimeout, r.Wire.Event)
			if outcome != holdProgressed {
				cancelAttempt()
				res.Stalls++
				r.logger().Warn("upstream stream produced no content; sending the request again",
					"outcome", outcome.String(),
					"after", time.Since(started).Round(time.Millisecond).String(),
					"account", lease.Account.Email,
					"stall_timeout", r.StallTimeout.String())
				continue
			}
			resp.Body = held
		}

		res.Status = resp.StatusCode
		r.relay(w, resp, &res, names)
		cancelAttempt()
		if !res.firstContentAt.IsZero() {
			res.FirstContent = res.firstContentAt.Sub(started)
		}
		if res.Status == http.StatusOK && res.StreamError == "" && !res.Opaque && kind == "" {
			r.Pool.ReportSuccess(lease.Account.ID)
		}
		return res
	}

	// Every attempt was refused and retryable. Same argument as above: answer
	// with the last thing the upstream actually said.
	if last != nil {
		replay(w, last, &res)
	}
	return res
}

// build copies the client's request onto the upstream, changing only what has
// to change: the credential, and the OAuth capability the upstream requires.
func (r *Relay) build(req *http.Request, url string, body []byte, token string) (*http.Request, error) {
	out, err := http.NewRequestWithContext(req.Context(), req.Method, url, &sentBody{b: body})
	if err != nil {
		return nil, err
	}
	// Set by hand because the reader is not one NewRequest recognises. GetBody
	// lets the transport resend the body if a connection dies before the
	// request is answered; Do clears it once the response is in, since it
	// would otherwise keep the body alive for the whole stream.
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return &sentBody{b: body}, nil }

	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if hopByHop[lower] {
			continue
		}
		switch lower {
		// The client's credential authenticates it to us, not us to Anthropic.
		case "authorization", "x-api-key",
			// Set by the transport from the body we are sending.
			"content-length", "host",
			// Content coding is negotiated per hop, and this hop must see
			// plaintext — see the Accept-Encoding note below.
			"accept-encoding":
			continue
		}
		out.Header[name] = append([]string(nil), values...)
	}

	// Ask for an uncompressed body, whatever the client asked us for.
	//
	// This is the one header the relay overrides rather than forwards, and it
	// has to be. Go's transport only decompresses transparently when it set
	// Accept-Encoding itself; a client that sends its own — curl --compressed,
	// python-requests, most SDKs — makes the response arrive still gzipped, and
	// then everything downstream of the body reads compressed bytes. Usage
	// records zero tokens, and, worse, the SSE scanner cannot see an
	// `event: error` in the stream, so a failed stream is reported as a
	// success and the account that failed it is credited. That is precisely
	// the bug the provider's meter exists to prevent, walking back in through a
	// different door.
	//
	// Content coding is a per-hop negotiation, so answering a client that
	// offered gzip with identity is correct, just less efficient. Paying that
	// on the upstream link is the cheaper half of the trade: the bytes are
	// model output, while the bulk of a Claude Code turn is the request going
	// the other way, which is unaffected.
	out.Header.Set("Accept-Encoding", "identity")

	// The account's credential, and whatever else the provider requires of
	// an authenticated call.
	r.Wire.Authorize(out, token)
	return out, nil
}

// sentBody is a request body that lets go of its bytes once they have been
// read. A bytes.Reader keeps its slice for as long as it exists, and the
// request it belongs to exists until the response is closed — so every body
// stayed in memory for the length of its stream, minutes for megabytes.
type sentBody struct{ b []byte }

func (s *sentBody) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		s.b = nil
		return 0, io.EOF
	}
	n := copy(p, s.b)
	s.b = s.b[n:]
	if len(s.b) == 0 {
		s.b = nil
	}
	return n, nil
}

func (s *sentBody) Close() error {
	s.b = nil
	return nil
}

// relay copies the upstream response to the client verbatim.
//
// names is the only thing that can make it not verbatim: when the request had
// tool names rewritten, the ones coming back are turned into the client's own
// again. It is nil for every other request, and then this is a byte copy.
func (r *Relay) relay(w http.ResponseWriter, resp *http.Response, res *Result, names map[string]string) {
	defer resp.Body.Close()

	for name, values := range resp.Header {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(resp.StatusCode)

	// Flush after every chunk.
	//
	// Claude Code runs a byte-level watchdog over the response and errors the
	// stream when no bytes arrive for long enough. Read out of the client:
	// 180 s talking straight to api.anthropic.com, 300 s through a custom base
	// URL — which is us — clamped to [10 s, 30 min] and overridable with
	// CLAUDE_BYTE_STREAM_IDLE_TIMEOUT_MS. A separate event-level idle timeout
	// sits at 300 s or more, and it reports stalls at 15, 30, 60 and 120 s
	// along the way.
	//
	// During a long thinking pause the upstream's SSE pings are the only
	// traffic there is, so anything that batches them kills the request. The
	// client will even synthesise its own ping if raw bytes are still arriving
	// but no decodable event has surfaced for 10 s — which only helps if the
	// bytes are actually moving, and they only move if this flushes.
	rc := http.NewResponseController(w)

	// Which reader can recover the usage depends on the shape of the response,
	// not on what the caller asked for: a request with "stream": true that
	// fails before the stream starts comes back as plain JSON.
	var tee provider.Meter
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); {
	case enc != "" && enc != "identity":
		// build asks for identity, and a server may not apply a coding the
		// client did not accept, so reaching here means the upstream broke
		// that rule. Relay the bytes — that part still works — but read
		// nothing out of them and say so, rather than recording a compressed
		// stream as zero tokens and no errors.
		r.logger().Warn("upstream compressed a response that asked for identity; usage and stream errors are unreadable",
			"content_encoding", enc)
		res.Opaque = true
		tee = nopMeter{}
	default:
		tee = r.Wire.Meter(resp.Header.Get("Content-Type"), provider.Reading{
			Usage:        &res.Usage,
			StreamError:  &res.StreamError,
			FirstContent: &res.firstContentAt,
		})
	}
	defer tee.Done()

	// Not for a body we could not read: rewriting compressed bytes would
	// corrupt them, and Opaque already says the contents are unknown.
	var restorer *passes.NameRestorer
	if len(names) > 0 && !res.Opaque {
		restorer = passes.NewNameRestorer(names)
	}

	write := func(b []byte) bool {
		if len(b) == 0 {
			return true
		}
		if _, err := w.Write(b); err != nil {
			return false // client went away
		}
		_ = rc.Flush()
		res.BytesOut += int64(len(b))
		return true
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			out := buf[:n]
			if restorer != nil {
				out = restorer.Translate(buf[:n])
			}
			if !write(out) {
				return
			}
			// The tee runs after the client write and never gates it, so
			// stats can never stall or alter the stream. It reads what the
			// upstream sent, not what we forwarded.
			tee.Feed(buf[:n])
		}
		if readErr != nil {
			if restorer != nil {
				write(restorer.Tail())
			}
			if !errors.Is(readErr, io.EOF) {
				res.Err = readErr
			}
			return
		}
	}
}

// holdable reports whether a response is a stream whose start can be held:
// a 200 Messages stream in plain text. Anything else is relayed as it comes.
func holdable(resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		return false
	}
	return strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
}

// peekErrorAndRestore reads the error body for logging and puts it back, so
// the client still receives it byte for byte. Error text is load-bearing:
// Claude Code decides whether to retry and disable a capability by matching on
// the upstream's wording, so it must arrive unmodified.
func peekErrorAndRestore(resp *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return ""
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if len(raw) > 4096 {
		return strings.TrimSpace(string(raw[:4096]))
	}
	return strings.TrimSpace(string(raw))
}

// Timeout returns a context deadline appropriate to the request.
//
// Both are outer bounds rather than the operative limit: the client gives up
// long before either. Read out of it — a 600 s SDK-level timeout, a 300 s
// per-request timeout on its non-streaming path (120 s under
// CLAUDE_CODE_REMOTE), a 30 s slow-first-byte warning, and the response
// watchdogs described in relay. So these exist to stop a forgotten request
// pinning an account forever, not to decide when a caller gives up. Shortening
// them to something that looks tidier would start cutting off requests the
// client was still waiting on.
func Timeout(streaming bool) time.Duration {
	if streaming {
		// Long enough for a slow model on a long generation; the client's own
		// watchdog is the real guard.
		return 30 * time.Minute
	}
	return 10 * time.Minute
}
