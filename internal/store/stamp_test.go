package store

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A window that starts on a whole second must count the events inside that
// second. With RFC3339Nano, "…:00.5Z" sorted before "…:00Z" as text, so an
// event half a second into the window was left out of it.
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

// Migration 020 brings rows written before stamp to the fixed width, so old
// and new rows compare with each other.
func TestMigrationFixesOldTimes(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.RecordUsage(ctx, UsageEvent{At: time.Now(), KeyID: "k"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"2026-10-01T10:00:00Z":           "2026-10-01T10:00:00.000000000Z",
		"2026-10-01T10:00:00.5Z":         "2026-10-01T10:00:00.500000000Z",
		"2026-10-01T10:00:00.123456789Z": "2026-10-01T10:00:00.123456789Z",
	}
	sql, err := migrationFS.ReadFile("migrations/020_fixed_width_times.sql")
	if err != nil {
		t.Fatal(err)
	}
	for old, want := range cases {
		if _, err := st.db.ExecContext(ctx, `UPDATE usage_events SET at = ?`, old); err != nil {
			t.Fatal(err)
		}
		// Twice: the migration must leave a converted value alone.
		for i := 0; i < 2; i++ {
			if _, err := st.db.ExecContext(ctx, string(sql)); err != nil {
				t.Fatal(err)
			}
		}
		var got string
		if err := st.db.QueryRowContext(ctx, `SELECT at FROM usage_events`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s → %s, want %s", old, got, want)
		}
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
