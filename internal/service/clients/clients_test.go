package clients

import (
	"reflect"
	"strings"
	"testing"
)

// A slot's name is its own plain one, for every type: no client needs a
// provider's word in it (see each Type for the setting that replaces one).
func TestNameIsPlain(t *testing.T) {
	cases := []struct {
		typ  Type
		slot string
		want string
	}{
		{Crush, "large", "large"},
		{ClaudeCode, "main", "large"},
		{ClaudeCode, "opus", "opus"},
		{ClaudeCode, "classifier", "auto"},
		{Opencode, "main", "large"},
		{Opencode, "small", "small"},
		{Codex, "subagent", "agents"},
		{Custom, "model", "model"},
		{Custom, "my-tool", "my-tool"},
	}
	for _, c := range cases {
		if got := c.typ.Name(c.slot, nil); got != c.want {
			t.Errorf("%s.Name(%s) = %q, want %q", c.typ.ID, c.slot, got, c.want)
		}
		if !ValidName(c.want) {
			t.Errorf("%q is not a valid name", c.want)
		}
	}
	taken := map[string]bool{"model": true, "model-2": true}
	if got := Custom.Name("model", func(n string) bool { return taken[n] }); got != "model-3" {
		t.Errorf("a taken name was not numbered: %q", got)
	}
}

// Every typed client speaks one API of its own and a custom one both, every
// slot follows a target and is named, and the slot ids, roles and names are
// unique within a type: the files find a slot by role, and a client's names
// are its own table.
func TestTypesAreWellFormed(t *testing.T) {
	native := map[string]Dialect{"claude-code": Messages, "crush": Messages, "opencode": Messages,
		"codex": Responses, "custom": ""}
	for _, typ := range All {
		if want, ok := native[typ.ID]; !ok || typ.API != want || (typ.API == "") != typ.Custom {
			t.Errorf("%s speaks %q", typ.ID, typ.API)
		}
		if len(typ.Slots) == 0 {
			t.Errorf("%s has no slot", typ.ID)
		}
		ids, roles, names := map[string]bool{}, map[Role]bool{}, map[string]bool{}
		for _, s := range typ.Slots {
			if ids[s.ID] || roles[s.Role] || names[s.Name] {
				t.Errorf("%s repeats slot %s, role %s or name %s", typ.ID, s.ID, s.Role, s.Name)
			}
			ids[s.ID], roles[s.Role], names[s.Name] = true, true, true
			if !s.Follows.Valid() || s.Label == "" || !ValidName(s.Name) {
				t.Errorf("%s.%s has no target, label or valid name", typ.ID, s.ID)
			}
			if strings.Contains(s.Name, "claude") || strings.Contains(s.Name, "gpt") {
				t.Errorf("%s.%s's name %q names a provider; a slot may serve either", typ.ID, s.ID, s.Name)
			}
		}
		if got, ok := Find(typ.ID); !ok || got.ID != typ.ID {
			t.Errorf("Find(%s) failed", typ.ID)
		}
	}
}

func TestFollow(t *testing.T) {
	for f, want := range map[Follow]struct {
		family, provider string
		valid            bool
	}{
		FollowOpus:      {"opus", "anthropic", true},
		"claude:mythos": {"mythos", "anthropic", true},
		FollowFirst:     {"", "openai", true},
		FollowLight:     {"", "openai", true},
		"claude:":       {"", "anthropic", false},
		"claude:Opus 5": {"", "anthropic", false},
		"openai:other":  {"", "anthropic", false},
	} {
		if f.Family() != want.family || f.Provider() != want.provider || f.Valid() != want.valid {
			t.Errorf("%s: family %q provider %q valid %v", f, f.Family(), f.Provider(), f.Valid())
		}
	}
	if FollowOpus.Title() != "Newest Claude Opus" || ClaudeFollow("haiku") != FollowHaiku {
		t.Errorf("titles: %q", FollowOpus.Title())
	}
}

// A new client starts with every slot of its type under its plain name, each
// following its default; a custom one with its one slot.
func TestInitial(t *testing.T) {
	var names []string
	for _, s := range ClaudeCode.Initial() {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"large", "small", "opus", "sonnet", "haiku", "fable", "auto"}) {
		t.Errorf("Claude Code's names %v", names)
	}
	if slots := Crush.Initial(); slots[0].Name != "large" || slots[1].Follows != FollowHaiku {
		t.Errorf("crush %+v", slots)
	}
	if slots := Opencode.Initial(); slots[0].Name != "large" || slots[1].Name != "small" {
		t.Errorf("opencode %+v", slots)
	}
	if slots := Custom.Initial(); !reflect.DeepEqual(slots,
		[]Initial{{Slot: "large", Name: "large", Follows: FollowOpus}}) {
		t.Errorf("custom %+v", slots)
	}
}
