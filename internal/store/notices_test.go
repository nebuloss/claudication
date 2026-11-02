package store

import (
	"context"
	"path/filepath"
	"testing"
)

// A mark lasts while its notice does: marking read twice keeps the first
// time, unread drops it, and a notice no longer current loses its mark so the
// same problem coming back is news.
func TestNoticeMarks(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "notices.db"), testSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.MarkNotices(ctx, []string{"a", "b", "c"}, true); err != nil {
		t.Fatal(err)
	}
	first, err := st.NoticesRead(ctx)
	if err != nil || len(first) != 3 {
		t.Fatalf("marks = %v, %v; want three", first, err)
	}
	if err := st.MarkNotices(ctx, []string{"a"}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNotices(ctx, []string{"b"}, false); err != nil {
		t.Fatal(err)
	}
	if err := st.ForgetNoticesRead(ctx, map[string]bool{"a": true, "b": true}); err != nil {
		t.Fatal(err)
	}
	got, err := st.NoticesRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got["a"].Equal(first["a"]) {
		t.Errorf("marks = %v; want only a, read when first marked (%v)", got, first["a"])
	}
}
