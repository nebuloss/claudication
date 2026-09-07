package oauth

import (
	"errors"
	"testing"
	"time"
)

func TestPendingRoundTrip(t *testing.T) {
	p := NewPending(time.Minute)
	pkce := PKCE{Verifier: "v", Challenge: "c"}
	p.Start("anthropic", "state-1", pkce, RedirectManual)

	got, err := p.Peek("anthropic", "state-1")
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.PKCE.Verifier != "v" {
		t.Errorf("Verifier = %q, want %q", got.PKCE.Verifier, "v")
	}
}

// Peek must NOT consume: a failed exchange has to be retryable without
// walking the operator back through the consent screen.
func TestPeekDoesNotConsume(t *testing.T) {
	p := NewPending(time.Minute)
	p.Start("anthropic", "state-1", PKCE{Verifier: "v"}, RedirectManual)

	for i := range 3 {
		if _, err := p.Peek("anthropic", "state-1"); err != nil {
			t.Fatalf("Peek %d: %v", i, err)
		}
	}
	if p.Len() != 1 {
		t.Errorf("Len = %d, want the attempt still live", p.Len())
	}
}

// Once redeemed it is gone, so a replayed callback cannot mint a second
// credential.
func TestConsumeIsFinal(t *testing.T) {
	p := NewPending(time.Minute)
	p.Start("anthropic", "state-1", PKCE{Verifier: "v"}, RedirectManual)

	if _, err := p.Peek("anthropic", "state-1"); err != nil {
		t.Fatalf("Peek: %v", err)
	}
	p.Consume("state-1")
	if _, err := p.Peek("anthropic", "state-1"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("Peek after Consume = %v, want ErrUnknownState", err)
	}
}

func TestPendingRejectsUnknownState(t *testing.T) {
	p := NewPending(time.Minute)
	p.Start("anthropic", "state-1", PKCE{Verifier: "v"}, RedirectManual)

	if _, err := p.Peek("anthropic", "other"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("Peek(unknown) = %v, want ErrUnknownState", err)
	}
}

// A state minted for one provider must not complete a login for another.
func TestPendingRejectsProviderMismatch(t *testing.T) {
	p := NewPending(time.Minute)
	p.Start("anthropic", "state-1", PKCE{Verifier: "v"}, RedirectManual)

	if _, err := p.Peek("codex", "state-1"); !errors.Is(err, ErrStateMismatch) {
		t.Errorf("Peek(wrong provider) = %v, want ErrStateMismatch", err)
	}
}

func TestPendingExpires(t *testing.T) {
	p := NewPending(15 * time.Minute)
	base := time.Now()
	p.now = func() time.Time { return base }
	p.Start("anthropic", "state-1", PKCE{Verifier: "v"}, RedirectManual)

	p.now = func() time.Time { return base.Add(16 * time.Minute) }
	if _, err := p.Peek("anthropic", "state-1"); !errors.Is(err, ErrUnknownState) {
		t.Errorf("Peek(expired) = %v, want ErrUnknownState", err)
	}
	if n := p.Len(); n != 0 {
		t.Errorf("expired flows should be evicted, Len = %d", n)
	}
}
