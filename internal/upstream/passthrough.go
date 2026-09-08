package upstream

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
)

// AnthropicBaseURL is where the relay forwards to.
const AnthropicBaseURL = "https://api.anthropic.com"

// oauthBeta must reach the upstream on every subscription-authenticated
// request. The gateway contract is explicit that stripping it fails those
// requests with a 401, so it is merged into whatever the client sent rather
// than replacing it.
const oauthBeta = "oauth-2025-04-20"

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

// Relay forwards a request to Anthropic on behalf of a pooled account.
//
// Its whole job is to be uninteresting: the request body
// goes out byte for byte, every header the client sent is preserved, the
// response status, headers and body come back untouched, and errors are
// forwarded verbatim. It never parses what it does not need to, which is what
// keeps it working with capabilities that do not exist yet.
type Relay struct {
	Pool accountPool
	// Attribution prepends Claude Code's system block when the caller did not.
	// Off means non-Claude-Code clients reach haiku and nothing above it.
	Attribution bool
	Client      *http.Client
	Log         *slog.Logger
	BaseURL     string
}

// bodyTee reads a copy of the relayed bytes to recover the usage figures.
// Implementations must never gate the write and never alter it.
type bodyTee interface {
	feed(chunk []byte)
	done()
}

// nopTee reads nothing, for a body we cannot interpret.
type nopTee struct{}

func (nopTee) feed([]byte) {}
func (nopTee) done()       {}

// accountPool is what the relay needs from the pool.
//
// An interface rather than the concrete type so the retry and refusal paths —
// the part the contract is strictest about — can be exercised
// without a database and a live provider behind them. *pool.Pool satisfies it.
type accountPool interface {
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
	Usage        Usage
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
	Err    error
}

type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

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
func (r *Relay) Do(w http.ResponseWriter, req *http.Request, provider, upstreamPath string, body []byte, p Prologue) Result {
	base := r.BaseURL
	if base == "" {
		base = AnthropicBaseURL
	}

	// Once, before the first attempt: a retry has to send the same bytes, and
	// a body that already leads with an accepted block comes back untouched.
	if r.Attribution {
		body = EnsureAttribution(body, p)
	}

	var res Result
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

		resp, err := r.Client.Do(upstreamReq)
		if err != nil {
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

		res.Status = resp.StatusCode
		r.relay(w, resp, &res)
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
	out, err := http.NewRequestWithContext(req.Context(), req.Method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

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

	out.Header.Set("Authorization", "Bearer "+token)

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
	// the bug ssescan.go exists to prevent, walking back in through a
	// different door.
	//
	// Content coding is a per-hop negotiation, so answering a client that
	// offered gzip with identity is correct, just less efficient. Paying that
	// on the upstream link is the cheaper half of the trade: the bytes are
	// model output, while the bulk of a Claude Code turn is the request going
	// the other way, which is unaffected.
	out.Header.Set("Accept-Encoding", "identity")

	// Merge rather than replace: the client's beta values are its own
	// capabilities and the contract forbids allowlisting them.
	betas := out.Header.Get("anthropic-beta")
	if !hasBeta(betas, oauthBeta) {
		if betas == "" {
			betas = oauthBeta
		} else {
			betas = oauthBeta + "," + betas
		}
		out.Header.Set("anthropic-beta", betas)
	}
	if out.Header.Get("anthropic-version") == "" {
		out.Header.Set("anthropic-version", "2023-06-01")
	}
	return out, nil
}

func hasBeta(header, want string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

// relay copies the upstream response to the client verbatim.
func (r *Relay) relay(w http.ResponseWriter, resp *http.Response, res *Result) {
	defer resp.Body.Close()

	for name, values := range resp.Header {
		if hopByHop[strings.ToLower(name)] {
			continue
		}
		w.Header()[name] = append([]string(nil), values...)
	}
	w.WriteHeader(resp.StatusCode)

	// Flush after every chunk. Claude Code runs a byte-level watchdog and
	// aborts a stream that goes quiet for 300 seconds; during long thinking
	// pauses the upstream's SSE pings are the only traffic, so anything that
	// batches them kills the request.
	rc := http.NewResponseController(w)

	// Which reader can recover the usage depends on the shape of the response,
	// not on what the caller asked for: a request with "stream": true that
	// fails before the stream starts comes back as plain JSON.
	var tee bodyTee
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); {
	case enc != "" && enc != "identity":
		// build asks for identity, and a server may not apply a coding the
		// client did not accept, so reaching here means the upstream broke
		// that rule. Relay the bytes — that part still works — but read
		// nothing out of them and say so, rather than recording a compressed
		// stream as zero tokens and no errors.
		r.Log.Warn("upstream compressed a response that asked for identity; usage and stream errors are unreadable",
			"content_encoding", enc)
		res.Opaque = true
		tee = nopTee{}
	case strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"):
		tee = newSSEScanner(&res.Usage, &res.StreamError)
	default:
		tee = newJSONUsage(&res.Usage)
	}
	defer tee.done()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return // client went away
			}
			_ = rc.Flush()
			res.BytesOut += int64(n)
			// The tee runs after the client write and never gates it, so
			// stats can never stall or alter the stream.
			tee.feed(buf[:n])
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				res.Err = readErr
			}
			return
		}
	}
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
func Timeout(streaming bool) time.Duration {
	if streaming {
		// Long enough for a slow model on a long generation; the client's own
		// watchdog is the real guard.
		return 30 * time.Minute
	}
	return 10 * time.Minute
}
