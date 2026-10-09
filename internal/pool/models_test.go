package pool

import (
	"context"
	"errors"
	"testing"
)

// A request is served only by an account that has its model on, in the usual
// order among those; when none has it on, that is its own answer, distinct
// from every account being busy.
func TestAcquireHonoursModelSwitches(t *testing.T) {
	ctx := context.Background()
	p, st := fixture(t)
	first := connect(t, p, "first@example.com", fresh())
	second := connect(t, p, "second@example.com", fresh())

	if err := st.SetAccountModel(ctx, first, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}
	lease, err := p.Acquire(ctx, "anthropic", "claude-opus-5", nil)
	if err != nil || lease.Account.ID != second {
		t.Errorf("opus with the first account's off: %v, %v; want the second", lease.Account.Email, err)
	}
	// Another model, and no model at all, still go to the top of the list.
	for _, model := range []string{"claude-haiku-4-5", ""} {
		if lease, err := p.Acquire(ctx, "anthropic", model, nil); err != nil || lease.Account.ID != first {
			t.Errorf("%q: %v, %v; want the first account", model, lease.Account.Email, err)
		}
	}

	// The only account with it on already failed this request: busy, not off.
	if _, err := p.Acquire(ctx, "anthropic", "claude-opus-5", map[string]bool{second: true}); !errors.Is(err, ErrAllCoolingUp) {
		t.Errorf("retry past the last account with it on: %v, want ErrAllCoolingUp", err)
	}

	// Off everywhere: withdrawn from the gateway.
	if err := st.SetAccountModel(ctx, second, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Acquire(ctx, "anthropic", "claude-opus-5", nil); !errors.Is(err, ErrModelOff) {
		t.Errorf("off on every account: %v, want ErrModelOff", err)
	}
	// And no accounts at all is still that, not a switch.
	if _, err := p.Acquire(ctx, "openai", "claude-opus-5", nil); !errors.Is(err, ErrNoAccounts) {
		t.Errorf("no accounts for the provider: %v, want ErrNoAccounts", err)
	}
}
