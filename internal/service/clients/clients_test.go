package clients

import (
	"reflect"
	"testing"
)

func TestNameCarriesTheMark(t *testing.T) {
	cases := []struct {
		typ  Type
		d    Dialect
		slot string
		want string
	}{
		{Crush, Messages, "large", "large"},
		{Crush, Responses, "large", "gpt-5-large"},
		{ClaudeCode, Messages, "main", "claude-large"},
		{ClaudeCode, Messages, "classifier", "claude-auto"},
		{Opencode, Responses, "small", "small"},
		{Opencode, Messages, "small", "claude-small"},
		{Codex, Responses, "subagent", "agents"},
		{Custom, Messages, "model", "model"},
		{Custom, Messages, "my-tool", "my-tool"},
	}
	for _, c := range cases {
		if got := c.typ.Name(c.d, c.slot, nil); got != c.want {
			t.Errorf("%s.Name(%s, %s) = %q, want %q", c.typ.ID, c.d, c.slot, got, c.want)
		}
		if !c.typ.Fits(c.d, c.want) || !ValidName(c.want) {
			t.Errorf("%q does not fit %s on %s", c.want, c.typ.ID, c.d)
		}
	}
	taken := map[string]bool{"model": true, "model-2": true}
	if got := Custom.Name(Messages, "model", func(n string) bool { return taken[n] }); got != "model-3" {
		t.Errorf("a taken name was not numbered: %q", got)
	}
}

// Every type's default dialect is one it speaks, every slot follows a target
// and is named, and the slot ids, roles and names are unique within a type:
// the files find a slot by role, and a client's names are its own table.
func TestTypesAreWellFormed(t *testing.T) {
	for _, typ := range All {
		if len(typ.Dialects) == 0 || len(typ.Slots) == 0 {
			t.Errorf("%s has no dialect or no slot", typ.ID)
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
			for _, d := range typ.Dialects {
				if n := typ.Name(d, s.ID, nil); !typ.Fits(d, n) {
					t.Errorf("%s.%s's name %q does not fit %s", typ.ID, s.ID, n, d)
				}
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

// A new client starts with every slot of its type under the name its dialect
// needs, each following its default; a custom one with its one slot; and a
// dialect the type cannot speak is its default.
func TestInitial(t *testing.T) {
	d, slots := ClaudeCode.Initial(Responses)
	if d != Messages {
		t.Errorf("Claude Code on Responses started on %s, want its only dialect", d)
	}
	var names []string
	for _, s := range slots {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"claude-large", "claude-small", "claude-opus", "claude-sonnet",
		"claude-haiku", "claude-fable", "claude-auto"}) {
		t.Errorf("Claude Code's names %v", names)
	}
	if _, slots := Crush.Initial(Responses); slots[0].Name != "gpt-5-large" || slots[1].Follows != FollowHaiku {
		t.Errorf("crush on Responses %+v", slots)
	}
	if d, slots := Custom.Initial(""); d != Messages || !reflect.DeepEqual(slots,
		[]Initial{{Slot: "large", Name: "large", Follows: FollowOpus}}) {
		t.Errorf("custom %s %+v", d, slots)
	}
}
