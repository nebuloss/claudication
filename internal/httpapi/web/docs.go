package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"claudication/internal/httpapi/httpx"
)

// modelsTTL is how long the public page's model list may be stale.
//
// The list changes when Anthropic ships a model, so minutes of staleness cost
// nothing and the alternative costs a great deal: /admin/models acquires an
// account from the pool and calls the upstream on every hit, and this page is
// unauthenticated. Uncached, anyone who can open it could spend the
// subscription in a loop. Ten minutes turns a thousand page loads into one
// upstream call.
const modelsTTL = 10 * time.Minute

// modelCache holds the last answer and when it was taken.
type modelCache struct {
	mu   sync.Mutex
	body []byte
	at   time.Time
	// inFlight collapses a burst: the first caller fetches and the rest wait
	// on the same result rather than each starting their own.
	inFlight *sync.WaitGroup
}

// models returns the upstream list, from cache when it is fresh enough.
//
// A failure is answered with whatever is held, however old. The page is
// instructions, not an API — a stale list beside a copyable config is better
// than an error where the list should be, and better than a page that cannot
// render because the upstream is having a bad minute.
func (c *modelCache) get(ctx context.Context, fetch func(context.Context) ([]byte, error)) []byte {
	c.mu.Lock()
	if c.body != nil && time.Since(c.at) < modelsTTL {
		body := c.body
		c.mu.Unlock()
		return body
	}
	if wg := c.inFlight; wg != nil {
		c.mu.Unlock()
		wg.Wait()
		c.mu.Lock()
		body := c.body
		c.mu.Unlock()
		return body
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inFlight = wg
	c.mu.Unlock()

	body, err := fetch(ctx)

	c.mu.Lock()
	if err == nil {
		c.body, c.at = body, time.Now()
	} else if c.body != nil {
		// Keep serving the stale copy, but let the next caller try again
		// rather than sitting on a failure for the whole TTL.
		c.at = time.Now().Add(-modelsTTL).Add(time.Minute)
	}
	out := c.body
	c.inFlight = nil
	c.mu.Unlock()
	wg.Done()
	return out
}

// welcomePage is what a browser gets at the root when the docs page is off.
//
// A JSON error is the right answer to a client that asked for an endpoint and
// the wrong one to a person who typed the address: {"error":{"type":
// "not_found"}} tells them nothing they can act on and looks like a fault.
//
// It deliberately says nothing about this gateway. The switch being off means
// this address does not describe what is behind it, and a welcome page naming
// the product, its version or whether it has accounts connected would undo
// exactly that. So: what the address is for, what you would need, and who to
// ask — none of which is a fact about this deployment.
//
// Self-contained on purpose. It is served when the built UI may not even be
// embedded, so it links no stylesheet and fetches nothing.
const welcomePage = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>API endpoint</title>
<style>
  :root { color-scheme: light dark; --bg:#faf9f7; --fg:#1c1b19; --dim:#4a4643; --line:#a8a29b; }
  @media (prefers-color-scheme: dark) { :root { --bg:#14100f; --fg:#e8e2de; --dim:#a9a29d; --line:#4a4643; } }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
         background:var(--bg); color:var(--fg);
         font:16px/1.65 ui-sans-serif,system-ui,"Segoe UI",sans-serif; }
  main { max-width:32rem; padding:2rem 1.5rem; }
  h1 { margin:0 0 .75rem; font-size:1.25rem; font-weight:600; }
  p { margin:0 0 .75rem; color:var(--dim); }
  hr { border:0; border-top:1px solid var(--line); margin:1.5rem 0; }
  code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:.9em; }
</style>
<main>
  <h1>This is an API endpoint</h1>
  <p>There is no website here. The address serves an API, and a request to it
     needs a key.</p>
  <hr>
  <p>If you were given this address to configure a tool, you will need an API
     key from whoever runs it — the address alone is not enough.</p>
  <p>Health checks: <code>/health</code>.</p>
</main>
`

// ServeWelcome answers a browser at the root with something readable.
func ServeWelcome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// No-store rather than no-cache: which page the root serves is a switch an
	// operator flips, and a cached copy of this one would outlive the flip.
	w.Header().Set("Cache-Control", "no-store")
	// Still a 404. Nothing is published at this address, and saying 200 would
	// tell a monitor that something is.
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(welcomePage))
}

// HandleDocsInfo is everything the public docs page needs, and nothing else.
//
// Deliberately not /admin/overview or /admin/config, which is what the in-app
// Setup screen read. Those carry the listen addresses, the state directory,
// the trusted proxies and every limit — an inventory of the deployment. This
// answers four questions instead: what address to point a client at, which
// APIs are being served, whether any account is connected, and which models
// exist. Nothing here is a secret, and nothing here can be changed.
func (s *Site) HandleDocsInfo(w http.ResponseWriter, r *http.Request) {
	if !s.docs.On() {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such endpoint: "+r.URL.Path)
		return
	}
	// The relay's address, not this page's: the snippets have to point at
	// where requests go, and the docs listener is somewhere else entirely.
	// Empty when the listeners are split and nobody said what the outside
	// name is. The page says so rather than guessing: a snippet pointing at
	// the wrong host is worse than one that admits it does not know.
	public := s.cfg.PublicURL

	ready := false
	if accounts, err := s.store.ListAccounts(r.Context()); err == nil {
		for _, a := range accounts {
			if !a.Disabled() {
				ready = true
				break
			}
		}
	}

	out := map[string]any{
		"public_url": public,
		"surfaces":   s.surfaces.State(),
		"ready":      ready,
	}

	// Best effort, and last: a page that cannot list models is still a page
	// that tells you how to configure a client.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if body := s.docsModels.get(ctx, func(c context.Context) ([]byte, error) {
		// Every model, as the admin list asks: the default page is 20, and the
		// client configs on this page are written from what comes back.
		return s.fetchModels(c, "limit=1000")
	}); body != nil {
		var parsed any
		if err := json.Unmarshal(body, &parsed); err == nil {
			out["models"] = parsed
		}
	}

	w.Header().Set("Cache-Control", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, out)
}
