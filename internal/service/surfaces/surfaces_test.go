package surfaces

import (
	"context"
	"errors"
	"testing"

	"claudication/internal/api"
	anthropicapi "claudication/internal/api/anthropic"
	"claudication/internal/api/openai"
	"claudication/internal/config"
)

// memStore is the two calls a switch makes, in a map.
type memStore struct {
	rows map[string]string
	fail error
}

func (m *memStore) Settings(context.Context) (map[string]string, error) {
	if m.fail != nil {
		return nil, m.fail
	}
	return m.rows, nil
}

func (m *memStore) SetSetting(_ context.Context, key, value string) error {
	if m.fail != nil {
		return m.fail
	}
	m.rows[key] = value
	return nil
}

func registry() api.Registry {
	return api.Registry{anthropicapi.New(), openai.New("claude-sonnet-5", 1024)}
}

// Every surface serves until someone says otherwise, and the report says so
// in registration order, as the default rather than as a stored choice.
func TestSurfacesServeByDefault(t *testing.T) {
	s := New(registry(), &memStore{rows: map[string]string{}})
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := s.State()
	if len(state) != 2 || state[0].ID != anthropicapi.ID || state[1].ID != openai.ID {
		t.Fatalf("state = %+v, want both surfaces in registration order", state)
	}
	for _, sf := range state {
		if !sf.Enabled || sf.Origin != config.FromDefault || len(sf.Routes) == 0 {
			t.Errorf("%s = %+v, want serving, by default, with its routes", sf.ID, sf)
		}
	}
	if s.Enabled("no-such-surface") {
		t.Error("a surface the registry does not hold is served")
	}
}

// A switch is stored and survives a reload, which is what lets an operator
// turn an API off without a restart that keeps it off only until the next.
func TestSurfaceSwitchIsStoredAndReloaded(t *testing.T) {
	st := &memStore{rows: map[string]string{}}
	s := New(registry(), st)
	if err := s.Set(context.Background(), openai.ID, false); err != nil {
		t.Fatal(err)
	}
	if s.Enabled(openai.ID) || !s.Enabled(anthropicapi.ID) {
		t.Error("switching one surface off did not take, or took the other with it")
	}
	if _, ok := st.rows[Key(openai.ID)]; !ok {
		t.Errorf("nothing stored under %s", Key(openai.ID))
	}

	reloaded := New(registry(), st)
	if err := reloaded.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled(openai.ID) {
		t.Error("the stored switch did not survive a reload")
	}
	for _, sf := range reloaded.State() {
		if sf.ID == openai.ID && sf.Origin != config.FromDatabase {
			t.Errorf("origin = %q, want database for a stored switch", sf.Origin)
		}
	}

	if err := s.Set(context.Background(), "no-such-surface", true); !errors.Is(err, ErrUnknown) {
		t.Errorf("Set(unknown) = %v, want ErrUnknown", err)
	}
}

// A store that cannot be read leaves every surface serving: the fallback is
// the state the gateway was in before the switches existed, not silence.
func TestUnreadableStoreLeavesSurfacesServing(t *testing.T) {
	s := New(registry(), &memStore{rows: map[string]string{}, fail: errors.New("disk gone")})
	if err := s.Load(context.Background()); err == nil {
		t.Error("an unreadable store was not reported")
	}
	if !s.Enabled(anthropicapi.ID) || !s.Enabled(openai.ID) {
		t.Error("a failed load switched a surface off")
	}
}
