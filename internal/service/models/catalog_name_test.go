package models

import "testing"

// The catalogue's "(latest)" on a dated snapshot read as the newest model
// beside three newer releases; it is dropped, and nothing else is touched.
func TestShownNameDropsLatest(t *testing.T) {
	for in, want := range map[string]string{
		"Claude Opus 4.5 (latest)":   "Claude Opus 4.5",
		"Claude Haiku 4.5 (Latest) ": "Claude Haiku 4.5",
		"Claude Opus 5.5":            "Claude Opus 5.5",
		"GPT (latest) mini":          "GPT (latest) mini",
		"":                           "",
	} {
		if got := shownName(in); got != want {
			t.Errorf("shownName(%q) = %q, want %q", in, got, want)
		}
	}
}
