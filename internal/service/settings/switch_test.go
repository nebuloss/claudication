package settings

import (
	"context"
	"errors"
	"testing"
)

type memStore struct {
	values  map[string]string
	readErr error
	setErr  error
	reads   int
}

func (m *memStore) Settings(context.Context) (map[string]string, error) {
	m.reads++
	if m.readErr != nil {
		return nil, m.readErr
	}
	out := map[string]string{}
	for k, v := range m.values {
		out[k] = v
	}
	return out, nil
}

func (m *memStore) SetSetting(_ context.Context, key, value string) error {
	if m.setErr != nil {
		return m.setErr
	}
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[key] = value
	return nil
}

func TestASwitchHoldsItsDefaultUntilLoaded(t *testing.T) {
	for _, def := range []bool{true, false} {
		s := NewSwitch(&memStore{}, "k", def)
		if s.On() != def || s.Stored() {
			t.Errorf("default %v: On=%v Stored=%v", def, s.On(), s.Stored())
		}
		if s.Key() != "k" {
			t.Errorf("Key = %q", s.Key())
		}
	}
}

// Load reads the store once for any number of switches, and a stored value
// overrides the default either way — including "false" over a true default.
func TestLoadAppliesStoredValuesInOneRead(t *testing.T) {
	st := &memStore{values: map[string]string{"a": "false", "b": "true"}}
	a, b, c := NewSwitch(st, "a", true), NewSwitch(st, "b", false), NewSwitch(st, "c", true)
	if err := Load(context.Background(), st, a, b, c); err != nil {
		t.Fatal(err)
	}
	if st.reads != 1 {
		t.Errorf("reads = %d, want 1", st.reads)
	}
	if a.On() || !a.Stored() {
		t.Errorf("a: On=%v Stored=%v, want false/true", a.On(), a.Stored())
	}
	if !b.On() || !b.Stored() {
		t.Errorf("b: On=%v Stored=%v, want true/true", b.On(), b.Stored())
	}
	if !c.On() || c.Stored() {
		t.Errorf("c: On=%v Stored=%v, want its default, unstored", c.On(), c.Stored())
	}
}

// A store that cannot be read leaves every switch at its default.
func TestLoadFailureKeepsDefaults(t *testing.T) {
	st := &memStore{readErr: errors.New("closed")}
	s := NewSwitch(st, "k", true)
	if err := Load(context.Background(), st, s); err == nil {
		t.Fatal("error swallowed")
	}
	if !s.On() || s.Stored() {
		t.Errorf("On=%v Stored=%v after a failed load", s.On(), s.Stored())
	}
}

// Set persists first: a failed write changes nothing, a good one survives a
// reload.
func TestSetPersistsBeforeTakingEffect(t *testing.T) {
	st := &memStore{setErr: errors.New("read-only")}
	s := NewSwitch(st, "k", false)
	if err := s.Set(context.Background(), true); err == nil {
		t.Fatal("error swallowed")
	}
	if s.On() || s.Stored() {
		t.Error("a switch that failed to persist took effect anyway")
	}

	st.setErr = nil
	if err := s.Set(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !s.On() || !s.Stored() || st.values["k"] != "true" {
		t.Errorf("On=%v Stored=%v stored=%q", s.On(), s.Stored(), st.values["k"])
	}
	reloaded := NewSwitch(st, "k", false)
	if err := Load(context.Background(), st, reloaded); err != nil || !reloaded.On() {
		t.Errorf("did not survive a reload: %v %v", reloaded.On(), err)
	}
	if err := s.Set(context.Background(), false); err != nil || s.On() || st.values["k"] != "false" {
		t.Errorf("turning off: On=%v stored=%q err=%v", s.On(), st.values["k"], err)
	}
}
