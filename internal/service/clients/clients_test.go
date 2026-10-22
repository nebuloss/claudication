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
		family, provider, id string
		valid                bool
	}{
		FollowOpus:      {"opus", "anthropic", "claude-opus", true},
		"claude:mythos": {"mythos", "anthropic", "claude-mythos", true},
		FollowSol:       {"sol", "openai", "gpt-sol", true},
		"openai:astra":  {"astra", "openai", "gpt-astra", true},
		"claude:":       {"", "anthropic", "", false},
		"claude:Opus 5": {"", "anthropic", "", false},
		"openai:":       {"", "openai", "", false},
		"other:x":       {"", "anthropic", "", false},
	} {
		if f.Family() != want.family || f.Provider() != want.provider || f.Valid() != want.valid ||
			f.ModelID() != want.id {
			t.Errorf("%s: family %q provider %q id %q valid %v", f, f.Family(), f.Provider(), f.ModelID(), f.Valid())
		}
		if want.valid && FollowOf(want.id) != f {
			t.Errorf("FollowOf(%q) = %q, want %q", want.id, FollowOf(want.id), f)
		}
	}
	// A real model's id names no family: it carries its version.
	for _, id := range []string{"claude-opus-5-5", "gpt-6.1-sol", "gpt-6-luna", "o3", ""} {
		if f := FollowOf(id); f != "" {
			t.Errorf("FollowOf(%q) = %q, want none", id, f)
		}
	}
	if FollowOpus.Title() != "Claude Opus" || FollowSol.Title() != "GPT Sol" || ClaudeFollow("haiku") != FollowHaiku ||
		FamilyFollow("openai", "luna") != FollowLuna {
		t.Errorf("titles: %q %q", FollowOpus.Title(), FollowSol.Title())
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

// An operator's own slot is a capability of a type, not of custom alone:
// every typed client's files can declare a name no setting asks for, and its
// picker lists it, so each takes them in the picker's role. A type's own
// slots stay, but for a custom client, all of whose slots are the operator's.
func TestExtraSlots(t *testing.T) {
	for _, c := range []struct {
		typ  Type
		role Role
	}{{Crush, RolePicker}, {Custom, RoleCustom}, {ClaudeCode, RolePicker}, {Codex, RolePicker}, {Opencode, RolePicker}} {
		s, ok := c.typ.SlotFor("s-0a1b2c")
		if ok != (c.role != "") || (ok && (s.Role != c.role || s.Name != "s-0a1b2c" || !s.Follows.Valid())) {
			t.Errorf("%s.SlotFor of an operator's slot = %+v, %v", c.typ.ID, s, ok)
		}
		if c.typ.Removable("s-0a1b2c") != (c.role != "") {
			t.Errorf("%s: an operator's slot removable = %v", c.typ.ID, !(c.role != ""))
		}
	}
	// Every typed client's own slots are settings its files always write.
	for _, typ := range []Type{ClaudeCode, Codex, Opencode, Crush} {
		for _, s := range typ.Slots {
			if typ.Removable(s.ID) {
				t.Errorf("%s's own %s is removable", typ.ID, s.ID)
			}
			if got, _ := typ.SlotFor(s.ID); got.Role != s.Role {
				t.Errorf("%s's %s reads as %+v", typ.ID, s.ID, got)
			}
		}
		if typ.ExtraHint == "" {
			t.Errorf("%s takes names of the operator's own and says nothing of what they are for", typ.ID)
		}
	}
	if !Custom.Removable("large") {
		t.Error("a custom client's first slot is not removable")
	}
	if s, _ := Crush.SlotFor("small"); s.Role != RoleFast {
		t.Errorf("crush's small reads as %+v", s)
	}
}
