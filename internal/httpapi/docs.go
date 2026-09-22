package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"claudication/internal/config"
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

// docsURL is where the public page can be reached.
//
// Its own address when one is configured, and the relay's otherwise — because
// that is where the relay serves it. Empty when neither is known, which means
// no link rather than a broken one.
func docsURL(c config.Config) string {
	if c.DocsListen != "" {
		return c.DocsURL
	}
	return c.PublicURL
}

// docsSetting is the settings row holding the switch.
const docsSetting = "docs.enabled"

// docsSwitch decides whether the public page is served.
//
// Runtime state rather than configuration, for the reason the API switches
// are: taking the page down is what an operator does when something is wrong,
// and the answer to that cannot be "edit a file and restart". The listener
// stays where it is and answers 404.
//
// Off by default, and this is the one place in the gateway where that default
// is about posture rather than cost. The page is served at the root of the
// relay, which today answers 404 to everything that is not an API call — so
// turning it on changes that listener from silent-unless-you-have-a-key to
// self-describing. It grants no access and carries no secret, but it is not a
// thing that should start happening because someone upgraded.
type docsSwitch struct {
	server *Server

	mu sync.Mutex
	on bool
}

func newDocsSwitch(s *Server) *docsSwitch { return &docsSwitch{server: s} }

func (d *docsSwitch) load(ctx context.Context) error {
	stored, err := d.server.store.Settings(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.on = stored[docsSetting] == "true"
	return nil
}

// enabled reports whether to serve it.
func (d *docsSwitch) enabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.on
}

func (d *docsSwitch) change(ctx context.Context, on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	if err := d.server.store.SetSetting(ctx, docsSetting, value); err != nil {
		return err
	}
	d.mu.Lock()
	d.on = on
	d.mu.Unlock()
	return nil
}

// handleSetDocs flips it.
func (s *Server) handleSetDocs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", `expected {"enabled": true|false}`)
		return
	}
	if err := s.docs.change(r.Context(), *body.Enabled); err != nil {
		s.log.Error("could not store the docs switch", "err", err)
		writeError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("public docs page switched", "enabled", *body.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"docs_enabled": s.docs.enabled()})
}

// handleDocsInfo is everything the public setup page needs, and nothing else.
//
// Deliberately not /admin/overview or /admin/config, which is what the in-app
// Setup screen read. Those carry the listen addresses, the state directory,
// the trusted proxies and every limit — an inventory of the deployment. This
// answers four questions instead: what address to point a client at, which
// APIs are being served, whether any account is connected, and which models
// exist. Nothing here is a secret, and nothing here can be changed.
func (s *Server) handleDocsInfo(w http.ResponseWriter, r *http.Request) {
	if !s.docs.enabled() {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint: "+r.URL.Path)
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
		"surfaces":   s.surfaces.state(),
		"ready":      ready,
	}

	// Best effort, and last: a page that cannot list models is still a page
	// that tells you how to configure a client.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if body := s.docsModels.get(ctx, func(c context.Context) ([]byte, error) {
		return s.fetchModels(c, "")
	}); body != nil {
		var parsed any
		if err := json.Unmarshal(body, &parsed); err == nil {
			out["models"] = parsed
		}
	}

	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, out)
}
