package models

import (
	"reflect"
	"testing"

	"claudication/internal/service/clients"
)

// The ChatGPT backend's list as measured on 2026-10-19 (its ranking, no
// dates) groups into its lines, each newest version first by version, the
// lines in the order of their newest's rank; a hidden or unversioned slug is
// no line's.
func TestChatGPTFamilies(t *testing.T) {
	rank := func(id string, p int) versioned {
		return versioned{id: id, version: versionOf(id), priority: p, ranked: true, seen: p}
	}
	got := familiesOf("openai", []versioned{
		rank("gpt-6.1-sol", 1), rank("gpt-6-astra", 2), rank("gpt-6-sol", 4), rank("gpt-reserve", 4),
		rank("gpt-6-luna", 5), rank("gpt-5.6-sol", 6), rank("gpt-5.6-terra", 8), rank("gpt-5.6-luna", 9),
		rank("codex-auto-review", 43),
	})
	want := []family{
		{clients.FollowSol, []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-5.6-sol"}},
		{"openai:astra", []string{"gpt-6-astra"}},
		{clients.FollowLuna, []string{"gpt-6-luna", "gpt-5.6-luna"}},
		{"openai:terra", []string{"gpt-5.6-terra"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("families = %+v\nwant %+v", got, want)
	}
}

// Claude's lines are ordered by release, the newest family first, and a
// dated id's date is no version.
func TestClaudeFamilies(t *testing.T) {
	got := familiesOf("anthropic", []versioned{
		{id: "claude-sonnet-4-5-20250929", at: "2025-09-29T00:00:00Z", version: versionOf("claude-sonnet-4-5-20250929")},
		{id: "claude-opus-5", at: "2026-05-01T00:00:00Z", version: versionOf("claude-opus-5"), seen: 1},
		{id: "claude-opus-5-5", at: "2026-10-01T00:00:00Z", version: versionOf("claude-opus-5-5"), seen: 2},
		{id: "claude-sonnet-5", at: "2026-06-01T00:00:00Z", version: versionOf("claude-sonnet-5"), seen: 3},
	})
	want := []family{
		{clients.FollowOpus, []string{"claude-opus-5-5", "claude-opus-5"}},
		{clients.FollowSonnet, []string{"claude-sonnet-5", "claude-sonnet-4-5-20250929"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("families = %+v\nwant %+v", got, want)
	}
	if v := versionOf("claude-sonnet-4-5-20250929"); !reflect.DeepEqual(v, []int{4, 5}) {
		t.Errorf("versionOf a dated id = %v", v)
	}
	if FamilyOf("openai", "gpt-reserve") != "" || FamilyOf("anthropic", "claude-opus") != "" ||
		FamilyOf("openai", "gpt-6.1-sol") != clients.FollowSol {
		t.Error("FamilyOf")
	}
}
