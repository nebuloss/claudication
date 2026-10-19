package store

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A window that starts on a whole second must count the events inside that
// second. When times were text, "…:00.5Z" sorted before "…:00Z", so an event
// half a second into the window was left out of it; integer milliseconds
// cannot, and this keeps it that way.
func TestWindowStartingOnAWholeSecond(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	since := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{
		since.Add(-500 * time.Millisecond), // before: not counted
		since,                              // on the boundary: counted
		since.Add(500 * time.Millisecond),  // inside the first second
	} {
		if err := st.RecordUsage(ctx, UsageEvent{At: at, KeyID: "k", InputTokens: 10}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.KeySpend(ctx, "k", since)
	if err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Errorf("KeySpend = %d, want 20", n)
	}
}

// An over-long title is cut on a character boundary, never inside one.
func TestChatTitleCutOnACharacter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	long := strings.Repeat("é", maxTitle) // two bytes each
	if err := st.SetChatTitle(ctx, "c", "x"+long, "m"); err != nil {
		t.Fatal(err)
	}
	title, _, _ := st.ChatTitle(ctx, "c")
	if !utf8.ValidString(title) || len(title) > maxTitle || len(title) < maxTitle-utf8.UTFMax {
		t.Errorf("title is %d bytes, valid UTF-8 %v", len(title), utf8.ValidString(title))
	}
}
