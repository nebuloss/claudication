package passes

import (
	"bytes"
	"context"
	"log/slog"
	"time"

	"claudication/internal/request"
)

// State is what passes share as they run: the prologue, which a pass that
// rewrites the system field must keep current for the passes after it, and
// what the response side needs to undo.
type State struct {
	Prologue request.Prologue
	// Names maps a tool name sent upstream back to the client's own, for the
	// relay to restore on the way back (NewNameRestorer). Nil when the
	// tool-names pass changed nothing.
	Names map[string]string
	// FitImages is the requesting client's own switch for capping images in
	// a many-image request (its "Shrink images" switch, clients in
	// schema.sql). Per request rather than per pipeline: the pipeline is one
	// for the whole gateway, and which clients send screenshots enough to
	// cross the threshold — and whether the detail lost matters to them — is
	// a fact about the client. Set by the relay from the request's context
	// (WithFitImages); the passes never read the store.
	FitImages bool
}

// fitImagesKey carries a request's FitImages through its context.
type fitImagesKey struct{}

// WithFitImages records whether the client behind a request has image
// fitting on. The gateway knows the client; the relay, which runs the passes,
// knows only the request — so the choice rides on the request's context, as
// the conversation id does for the pool.
func WithFitImages(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, fitImagesKey{}, on)
}

// FitsImages is what WithFitImages recorded, false when nothing did: a
// request from no client (the titler's own) is never re-encoded.
func FitsImages(ctx context.Context) bool {
	on, _ := ctx.Value(fitImagesKey{}).(bool)
	return on
}

// Pass is one rewrite of a request body on its way upstream.
//
// Apply returns the body unchanged — the same slice — when it has nothing to
// do, and a new one when it rewrote something. That identity is how the
// pipeline knows a pass acted without asking it to say so.
type Pass struct {
	// Name is how the pass appears in logs and in a request's rewrites.
	Name string
	// Enabled reports whether the pass runs at all on this request; nil
	// means always. A function of the request's State rather than a bool
	// because one pass is a switch of the client that sent it.
	Enabled func(st *State) bool
	Apply   func(body []byte, st *State) []byte
}

// Pipeline runs passes in order, each on the previous one's output.
type Pipeline []Pass

// Run applies every enabled pass and returns the body to send and the names
// of the passes that changed it.
//
// Each pass is timed and its effect logged at debug level — whether it acted,
// the body size before and after, how long it took — so a request that
// misbehaves can be narrowed to the one rewrite responsible by turning on
// debug logging, rather than by bisecting the relay. The names of the passes
// that acted come back for the relay's own log line, so which rewrites a
// request took is visible without debug logging at all.
func (pl Pipeline) Run(body []byte, st *State, log *slog.Logger) ([]byte, []string) {
	var applied []string
	for _, p := range pl {
		if p.Enabled != nil && !p.Enabled(st) {
			continue
		}
		start := time.Now()
		out := p.Apply(body, st)
		changed := !sameSlice(out, body)
		if log != nil && log.Enabled(context.Background(), slog.LevelDebug) {
			log.Debug("request pass",
				"pass", p.Name,
				"changed", changed,
				"bytes_in", len(body),
				"bytes_out", len(out),
				"took_us", time.Since(start).Microseconds())
		}
		if changed {
			applied = append(applied, p.Name)
		}
		body = out
	}
	return body, applied
}

// sameSlice reports whether two slices are the same bytes in memory — which
// is what a pass that changed nothing returns.
func sameSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return len(a) == 0 || &a[0] == &b[0]
}

// Options is what the default pipeline needs from the gateway's configuration.
type Options struct {
	// Attribution adds Claude Code's identity block to requests without it.
	Attribution bool
	// Images remembers fitted images across requests; nil means no caching.
	Images *ImageCache
	Log    *slog.Logger
}

// Default is the gateway's pipeline, in the order the passes must run.
//
// The order is load-bearing. Surrogates first, because it repairs bytes the
// upstream would refuse outright and the passes after it read those bytes.
// Attribution before the system-text passes, because it reads the prologue
// peeked from the original body and they rewrite the system field. Tool names
// before images, and images last: it touches messages alone and is the one
// pass that changes what the model is shown rather than the envelope's shape,
// which is why it runs only for a client whose operator switched it on.
func Default(o Options) Pipeline {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return Pipeline{
		surrogates(log),
		{
			Name:    "attribution",
			Enabled: func(*State) bool { return o.Attribution },
			Apply: func(body []byte, st *State) []byte {
				return EnsureAttribution(body, st.Prologue)
			},
		},
		{
			Name: "system-text",
			Apply: func(body []byte, st *State) []byte {
				if !mayNeedSystemNormalising(body) {
					return body
				}
				// Splices rather than rebuilds, on the prologue's word that the
				// body parses. The rewrite keeps the body valid, so that word
				// stays good for the passes after this one.
				return normaliseSystem(body, st.Prologue.Valid)
			},
		},
		{
			Name: "empty-text",
			Apply: func(body []byte, st *State) []byte {
				if !bytes.Contains(body, []byte(`"text":""`)) {
					return body
				}
				return dropEmptyMessageText(body, st.Prologue.Valid)
			},
		},
		{
			Name: "tool-names",
			Apply: func(body []byte, st *State) []byte {
				out, names := RewriteRefusedToolNames(body)
				st.Names = names
				return out
			},
		},
		{
			Name:    "images",
			Enabled: func(st *State) bool { return st.FitImages },
			Apply: func(body []byte, st *State) []byte {
				fitted, n := ShrinkImages(body, o.Images)
				if n == 0 {
					return body
				}
				log.Info("capped oversized images for a many-image request",
					"images", n, "max_edge", MaxEdge)
				return fitted
			},
		},
	}
}

// OpenAI is the pipeline for the ChatGPT Codex backend: lone surrogates
// repaired, and nothing else.
//
// The other five of Default's passes repair an Anthropic Messages envelope —
// Claude Code's identity block, the system array, empty text blocks, tool
// names Anthropic refuses, its image rules — and on a Responses body they
// would either do nothing or put Anthropic's fields into a request that has
// none. Surrogates are the one refusal measured on both: a lone \ud800 in the
// text is a 400 here too ({"detail":"Bad Request"}, 2026-10-19,
// internal/provider/openai/testdata/surrogate.json), and the repair is a
// byte-level one that does not care whose envelope it is in.
//
// Not stripped, though crush strips it as refused: max_output_tokens.
// Measured the same day, a request carrying max_output_tokens: 256 was
// answered 200 (testdata/max-output.json), so there is nothing to repair.
// The model is set by SetModel as on every wire, when a request names one of
// its client's model names; that is not a pass.
func OpenAI(o Options) Pipeline {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return Pipeline{surrogates(log)}
}

// surrogates is FixLoneSurrogates as a pass, the same on every pipeline.
func surrogates(log *slog.Logger) Pass {
	return Pass{
		Name: "surrogates",
		Apply: func(body []byte, st *State) []byte {
			fixed, n := FixLoneSurrogates(body)
			if n == 0 {
				return body
			}
			// The prologue was read from the bytes just replaced and holds
			// the system array by reference; attribution rebuilds from it,
			// which would put the broken escape straight back.
			st.Prologue = request.Peek(fixed)
			log.Warn("repaired unpaired surrogate escapes in the request body; "+
				"something upstream of this gateway is cutting text mid-character",
				"replaced", n)
			return fixed
		},
	}
}
