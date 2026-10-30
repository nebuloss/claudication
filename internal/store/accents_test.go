package store

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

// The next colour is the first the others do not wear, and once every one is
// worn, the one fewest wear.
func TestNextAccent(t *testing.T) {
	if got := nextAccent(nil); got != Accents[0] {
		t.Errorf("first: %q, want %q", got, Accents[0])
	}
	if got := nextAccent([]string{Accents[0], Accents[2]}); got != Accents[1] {
		t.Errorf("with the first and third worn: %q, want %q", got, Accents[1])
	}
	all := append(slices.Clone(Accents), Accents[0], Accents[1])
	if got := nextAccent(all); got != Accents[2] {
		t.Errorf("all worn, the first two twice: %q, want %q", got, Accents[2])
	}
}

// An account takes the first colour its provider's others do not wear, keeps
// it when signed in again, and the operator can change it to a palette colour
// and nothing else. A client is coloured the same way within its type.
func TestAccentsHandedOut(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	a := addAccount(t, st, "a@example.com")
	b := addAccount(t, st, "b@example.com")
	if a.Accent != Accents[0] || b.Accent != Accents[1] {
		t.Fatalf("two of one provider: %q, %q; want %q, %q", a.Accent, b.Accent, Accents[0], Accents[1])
	}
	other, err := st.UpsertAccount(ctx,
		Account{Provider: "openai", Email: "a@example.com", AccountUUID: "o", ExpiresAt: time.Now().Add(time.Hour)},
		Tokens{AccessToken: "at", RefreshToken: "rt"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Accent != Accents[0] {
		t.Errorf("another provider's first: %q, want %q", other.Accent, Accents[0])
	}

	if err := st.SetAccountAccent(ctx, a.ID, "teal"); err != nil {
		t.Fatal(err)
	}
	again := addAccount(t, st, "a@example.com")
	if again.ID != a.ID || again.Accent != "teal" {
		t.Errorf("signed in again: %+v, want its own colour kept", again.Accent)
	}
	if err := st.SetAccountAccent(ctx, a.ID, "red"); !errors.Is(err, ErrInvalidAccent) {
		t.Errorf("a colour not in the palette: %v", err)
	}

	c1, _, err := st.CreateClient(ctx, "one", "custom", KeyLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, _, err := st.CreateClient(ctx, "two", "custom", KeyLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Accent != Accents[0] || c2.Accent != Accents[1] {
		t.Errorf("two clients of one type: %q, %q", c1.Accent, c2.Accent)
	}
	if err := st.SetClientAccent(ctx, c2.ID, "sand"); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Client(ctx, c2.ID); c.Accent != "sand" {
		t.Errorf("client recoloured: %q", c.Accent)
	}
}

// The UI's palette is this one, in this order: a colour added here and not
// there has no swatch, and one there and not here is refused.
func TestAccentsMirrored(t *testing.T) {
	src, err := os.ReadFile("../../web/src/ui/brands/accents.ts")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)export const ACCENTS = \[(.*?)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("web/src/ui/brands/accents.ts has no ACCENTS list")
	}
	var ui []string
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllSubmatch(m[1], -1) {
		ui = append(ui, string(q[1]))
	}
	if !slices.Equal(ui, Accents) {
		t.Errorf("the UI lists %v, the store %v", ui, Accents)
	}
}
