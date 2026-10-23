package models

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"claudication/internal/config"
)

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

// Measured 2026-10-23: Antigravity's ids carry a level and some are aliases,
// so its entries are matched by name. Each gets the catalogue's facts and
// keeps its own name; an alias is described as the model it is; a name the
// catalogue has only as a preview finds it; an entry it has no name for
// falls back to its id; one it knows neither way is listed as it came. An
// entry with no catalog_name is matched by id as before, and the field
// never reaches a client.
func TestMatchCatalogByName(t *testing.T) {
	s := &Catalog{cfg: &config.Config{ModelsCatalog: "http://catalogue.invalid"}}
	s.catalog.models = map[string][]catalogModel{"google": {
		{ID: "gemini-4-argon", DisplayName: "Gemini 4 Argon", CreatedAt: "2026-10-01T00:00:00Z", MaxInput: 2000000},
		{ID: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash Lite", CreatedAt: "2026-06-01T00:00:00Z", MaxInput: 1000000},
		{ID: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash Preview", CreatedAt: "2025-12-01T00:00:00Z"},
		{ID: "gemini-3.1-flash-image", DisplayName: "Nano Banana 2", CreatedAt: "2025-11-01T00:00:00Z"},
		{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", CreatedAt: "2025-06-01T00:00:00Z", MaxInput: 1},
	}}
	s.catalog.at = time.Now()
	body := `{"data":[
	  {"id":"gemini-4-argon-high","display_name":"Gemini 4 Argon (High)","catalog_name":"Gemini 4 Argon"},
	  {"id":"gemini-4-argon-low","display_name":"Gemini 4 Argon (Low)","catalog_name":"Gemini 4 Argon"},
	  {"id":"gemini-2.5-flash","display_name":"Gemini 3.5 Flash Lite","catalog_name":"Gemini 3.5 Flash Lite"},
	  {"id":"gemini-3-flash","display_name":"Gemini 3 Flash","catalog_name":"Gemini 3 Flash"},
	  {"id":"gemini-3.1-flash-image","display_name":"Gemini 3.1 Flash Image","catalog_name":"Gemini 3.1 Flash Image"},
	  {"id":"gpt-oss-120b-medium","display_name":"GPT-OSS 120B (Medium)","catalog_name":"GPT-OSS 120B"}]}`
	var l struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(s.matchCatalog(context.Background(), "google", []byte(body)), &l); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Data {
		if _, leaked := e["catalog_name"]; leaked {
			t.Errorf("%v kept catalog_name", e["id"])
		}
		created, _ := e["created_at"].(string)
		got = append(got, e["id"].(string)+"="+e["display_name"].(string)+"@"+strings.TrimSuffix(created, "T00:00:00Z"))
	}
	want := []string{
		"gemini-4-argon-high=Gemini 4 Argon (High)@2026-10-01",
		"gemini-4-argon-low=Gemini 4 Argon (Low)@2026-10-01",
		"gemini-2.5-flash=Gemini 3.5 Flash Lite@2026-06-01",
		"gemini-3-flash=Gemini 3 Flash@2025-12-01",
		"gemini-3.1-flash-image=Gemini 3.1 Flash Image@2025-11-01",
	}
	if len(l.Data) != len(want)+1 || strings.Join(got[:len(want)], " ") != strings.Join(want, " ") {
		t.Errorf("matched\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if l.Data[len(want)]["id"] != "gpt-oss-120b-medium" || l.Data[len(want)]["created_at"] != nil {
		t.Errorf("unknown model = %v", l.Data[len(want)])
	}
}
