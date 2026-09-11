package httpapi

import (
	"context"
	"net/http"
	"sync"

	"claudication/internal/api"
	"claudication/internal/config"
	"claudication/internal/store"
)

// surfaceKey is the settings row holding one surface's switch.
func surfaceKey(id string) string { return "api." + id + ".enabled" }

// surfaces decides, per request, whether a client-facing API is being served.
//
// Runtime state rather than configuration, and deliberately so. Turning an API
// off is what an operator does when something is wrong — a key is loose, a
// client is looping, an account is being spent — and the answer to that cannot
// be "edit a file, restart, and drop every stream in flight". So the switches
// live in the database, take effect on the next request, and survive a
// restart.
//
// Both may be off at once. A gateway serving no API at all is a strange thing
// to want for long, but it is exactly what "stop everything now" means, and a
// switch that refuses to reach that state is not a switch.
type surfaces struct {
	registry api.Registry
	store    *store.Store

	mu sync.RWMutex
	// on holds only what has been set explicitly. A surface with no row takes
	// defaultEnabled, which is how an upgrade that adds a surface behaves the
	// same on a fresh install and an existing one.
	on map[string]bool
}

// defaultEnabled is what a surface does before anyone has said otherwise.
//
// On, for both. The alternative — a new surface arriving switched off — makes
// an upgrade silently not serve something the release notes say it serves, and
// every API here is behind an API key already, so serving one more shape of
// request to an authenticated caller widens nothing.
const defaultEnabled = true

func newSurfaces(registry api.Registry, st *store.Store) *surfaces {
	return &surfaces{registry: registry, store: st, on: map[string]bool{}}
}

// load reads the stored switches once at startup. A failure here is worth
// reporting but not worth refusing to start over: the defaults are serving
// state, not an empty one.
func (s *surfaces) load(ctx context.Context) error {
	stored, err := s.store.Settings(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.registry {
		if v, ok := stored[surfaceKey(p.ID())]; ok {
			s.on[p.ID()] = v == "true"
		}
	}
	return nil
}

// enabled reports whether a surface is being served.
func (s *surfaces) enabled(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.on[id]; ok {
		return v
	}
	return defaultEnabled
}

// set changes a switch and records it. The store is written first: a switch
// that took effect but did not survive the next restart is the worse failure.
func (s *surfaces) set(ctx context.Context, id string, on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	if err := s.store.SetSetting(ctx, surfaceKey(id), value); err != nil {
		return err
	}
	s.mu.Lock()
	s.on[id] = on
	s.mu.Unlock()
	return nil
}

// surfaceState is one switch as the admin UI sees it.
type surfaceState struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Routes  []string `json:"routes"`
	Enabled bool   `json:"enabled"`
	// Origin is where the effective value came from, in the same vocabulary
	// the config screen uses, so the two read as one story.
	Origin config.Origin `json:"origin"`
}

// state reports every surface in registration order.
func (s *surfaces) state() []surfaceState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]surfaceState, 0, len(s.registry))
	for _, p := range s.registry {
		enabled, origin := defaultEnabled, config.FromDefault
		if v, ok := s.on[p.ID()]; ok {
			enabled, origin = v, config.FromDatabase
		}
		out = append(out, surfaceState{
			ID:      p.ID(),
			Title:   p.Title(),
			Routes:  p.Routes(),
			Enabled: enabled,
			Origin:  origin,
		})
	}
	return out
}

// surface gates a route on its API being switched on.
//
// It answers 404 rather than 503 on purpose. 503 says "try again shortly" and
// a client obediently will, for as long as the surface stays off; 404 says the
// gateway does not serve this, which is the truth and which stops the client
// rather than making it spin. The message names the surface and where to turn
// it back on, because the operator reading it in a client's log is usually not
// the one who flipped the switch.
//
// The check is per request and reads a cached value, so a switch takes effect
// on the next request with no restart and no lock on the hot path beyond a
// read lock over a two-entry map.
func (s *Server) surface(p api.Protocol, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.surfaces.enabled(p.ID()) {
			p.WriteError(w, http.StatusNotFound, "not_found",
				"the "+p.Title()+" is turned off on this gateway; "+
					"an administrator can switch it back on under Settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}
