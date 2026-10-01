// Package surfaces holds the switches that decide which client-facing APIs
// the gateway serves.
package surfaces

import (
	"context"
	"errors"

	"claudication/internal/api"
	"claudication/internal/config"
	"claudication/internal/service/settings"
)

// Key is the settings row holding one surface's switch.
func Key(id string) string { return "api." + id + ".enabled" }

// Surfaces decides, per request, whether a client-facing API is being served.
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
type Surfaces struct {
	registry api.Registry
	// One switch per surface, built from the registry. A surface with no row
	// takes defaultEnabled, which is how an upgrade that adds a surface
	// behaves the same on a fresh install and an existing one.
	switches map[string]*settings.Switch
	store    settings.Store
}

// defaultEnabled is what a surface does before anyone has said otherwise.
//
// On, for both. The alternative — a new surface arriving switched off — makes
// an upgrade silently not serve something the release notes say it serves, and
// every API here is behind an API key already, so serving one more shape of
// request to an authenticated caller widens nothing.
const defaultEnabled = true

func New(registry api.Registry, st settings.Store) *Surfaces {
	s := &Surfaces{registry: registry, store: st, switches: map[string]*settings.Switch{}}
	for _, p := range registry {
		s.switches[p.ID()] = settings.NewSwitch(st, Key(p.ID()), defaultEnabled)
	}
	return s
}

// Load reads the stored switches once at startup. A failure here is worth
// reporting but not worth refusing to start over: the defaults are serving
// state, not an empty one.
func (s *Surfaces) Load(ctx context.Context) error {
	all := make([]*settings.Switch, 0, len(s.switches))
	for _, sw := range s.switches {
		all = append(all, sw)
	}
	return settings.Load(ctx, s.store, all...)
}

// Enabled reports whether a surface is being served. A surface the registry
// does not know is not served.
func (s *Surfaces) Enabled(id string) bool {
	sw, ok := s.switches[id]
	return ok && sw.On()
}

// ErrUnknown is set's answer for an id the registry does not hold.
var ErrUnknown = errors.New("no such API surface")

// Set changes a switch and records it. The store is written first: a switch
// that took effect but did not survive the next restart is the worse failure.
func (s *Surfaces) Set(ctx context.Context, id string, on bool) error {
	sw, ok := s.switches[id]
	if !ok {
		return ErrUnknown
	}
	return sw.Set(ctx, on)
}

// Surface is one switch as the admin UI sees it.
type Surface struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Routes  []string `json:"routes"`
	Enabled bool     `json:"enabled"`
	// Origin is where the effective value came from, in the same vocabulary
	// the config screen uses, so the two read as one story.
	Origin config.Origin `json:"origin"`
}

// State reports every surface in registration order.
func (s *Surfaces) State() []Surface {
	out := make([]Surface, 0, len(s.registry))
	for _, p := range s.registry {
		sw := s.switches[p.ID()]
		enabled, origin := sw.On(), config.FromDefault
		if sw.Stored() {
			origin = config.FromDatabase
		}
		out = append(out, Surface{
			ID:      p.ID(),
			Title:   p.Title(),
			Routes:  p.Routes(),
			Enabled: enabled,
			Origin:  origin,
		})
	}
	return out
}
