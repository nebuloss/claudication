// Package settings holds the gateway's runtime switches: the on/off decisions
// an operator makes from the admin UI while the gateway runs — which client
// APIs are served, image fitting, the public docs page, chat titles.
//
// They live in the database rather than the config file because turning
// something off is a thing done at three in the morning, and should not need
// a config edit and a restart. Each was its own copy of the same twenty lines
// before this; a Switch is that once.
package settings

import (
	"context"
	"sync/atomic"
)

// Store is where switches persist. The database is one.
type Store interface {
	Settings(ctx context.Context) (map[string]string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Switch is one runtime on/off setting.
//
// Read on hot paths — the relay asks about image fitting on every request it
// forwards — so the value is an atomic, not a field behind a lock that every
// concurrent stream would take to read one bit.
type Switch struct {
	st  Store
	key string
	def bool

	on     atomic.Bool
	stored atomic.Bool // set explicitly, rather than its default
}

// NewSwitch returns a switch persisted under key, def until loaded or set.
func NewSwitch(st Store, key string, def bool) *Switch {
	s := &Switch{st: st, key: key, def: def}
	s.on.Store(def)
	return s
}

// Key is the setting's name in the store.
func (s *Switch) Key() string { return s.key }

// On reports the switch's value.
func (s *Switch) On() bool { return s.on.Load() }

// Stored reports whether an operator has set it, as opposed to it holding
// its default — which the admin UI shows, because a switch whose provenance
// is invisible is one an operator flips and then cannot find again.
func (s *Switch) Stored() bool { return s.stored.Load() }

// Apply takes the switch's value from settings already read.
func (s *Switch) Apply(stored map[string]string) {
	if v, ok := stored[s.key]; ok {
		s.on.Store(v == "true")
		s.stored.Store(true)
	}
}

// Set changes the switch. The store is written first, so a switch that took
// effect but did not survive a restart is not a state this can reach.
func (s *Switch) Set(ctx context.Context, on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	if err := s.st.SetSetting(ctx, s.key, value); err != nil {
		return err
	}
	s.on.Store(on)
	s.stored.Store(true)
	return nil
}

// Load reads every switch given from one read of the store. A failure is
// returned for the caller to report; the switches keep their defaults, which
// is the reason each default is the safe one.
func Load(ctx context.Context, st Store, switches ...*Switch) error {
	stored, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	for _, s := range switches {
		s.Apply(stored)
	}
	return nil
}
