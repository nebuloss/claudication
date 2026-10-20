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
// Plan maps by role, and a client's names are its own table.
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

func existing(typ Type, d Dialect) []Existing {
	var out []Existing
	for _, s := range typ.Slots {
		out = append(out, Existing{ID: s.ID, Role: s.Role, Name: typ.Name(d, s.ID, nil)})
	}
	return out
}

func TestPlanCustomToCrush(t *testing.T) {
	have := []Existing{
		{ID: "model", Role: RoleMain, Name: "model"},
		{ID: "extra", Role: RoleCustom, Name: "extra"},
	}
	ch := Plan(have, Crush, Messages)
	if ch.Dialect != Messages {
		t.Errorf("dialect %s", ch.Dialect)
	}
	if !reflect.DeepEqual(ch.Keep, []Kept{{From: "model", To: "large", Name: "model"}}) {
		t.Errorf("keep %+v", ch.Keep)
	}
	if !reflect.DeepEqual(ch.Add, []Added{{Slot: "small", Name: "small", Follows: FollowHaiku}}) {
		t.Errorf("add %+v", ch.Add)
	}
	if len(ch.Remove) != 1 || ch.Remove[0].Name != "extra" {
		t.Errorf("remove %+v: the custom slot crush has no place for must go", ch.Remove)
	}
}

func TestPlanCrushToOpencode(t *testing.T) {
	have := existing(Crush, Messages)
	ch := Plan(have, Opencode, Responses)
	want := []Kept{{From: "large", To: "main", Name: "large"}, {From: "small", To: "small", Name: "small"}}
	if !reflect.DeepEqual(ch.Keep, want) || len(ch.Add) != 0 || len(ch.Remove) != 0 {
		t.Errorf("change %+v", ch)
	}
	// On Messages opencode wants "claude" in its names: both are renamed,
	// keeping their bindings, which travel with the slot row.
	ch = Plan(have, Opencode, Messages)
	if ch.Keep[0].Renamed != "claude-large" || ch.Keep[1].Renamed != "claude-small" {
		t.Errorf("keep %+v", ch.Keep)
	}
}

func TestPlanCodexToClaudeCode(t *testing.T) {
	have := existing(Codex, Responses)
	ch := Plan(have, ClaudeCode, Responses)
	// Claude Code speaks Messages only, so the dialect resets.
	if ch.Dialect != Messages {
		t.Errorf("dialect %s, want the type's default", ch.Dialect)
	}
	if len(ch.Keep) != 1 || ch.Keep[0].From != "main" || ch.Keep[0].To != "main" ||
		ch.Keep[0].Renamed != "claude-large" {
		t.Errorf("keep %+v", ch.Keep)
	}
	var added []string
	for _, a := range ch.Add {
		added = append(added, a.Name)
	}
	if !reflect.DeepEqual(added, []string{"claude-small", "claude-opus", "claude-sonnet", "claude-haiku",
		"claude-fable", "claude-auto"}) {
		t.Errorf("added %v", added)
	}
	var removed []string
	for _, r := range ch.Remove {
		removed = append(removed, r.Name)
	}
	if !reflect.DeepEqual(removed, []string{"review", "agents", "small"}) {
		t.Errorf("removed %v, want the names Claude Code has no slot for freed", removed)
	}
}

// A dialect change alone renames only what no longer fits; the same type and
// dialect is no change at all.
func TestPlanDialectOnly(t *testing.T) {
	have := existing(Crush, Messages)
	ch := Plan(have, Crush, Responses)
	if ch.Keep[0].Renamed != "gpt-5-large" || ch.Keep[1].Renamed != "gpt-5-small" || ch.Empty() {
		t.Errorf("keep %+v", ch.Keep)
	}
	if !Plan(have, Crush, Messages).Empty() {
		t.Error("the same type and dialect is no change")
	}
}

// An added slot never takes a name a kept one holds.
func TestPlanAvoidsKeptNames(t *testing.T) {
	have := []Existing{{ID: "model", Role: RoleMain, Name: "small"}}
	ch := Plan(have, Crush, Messages)
	if ch.Keep[0].Name != "small" || ch.Add[0].Name != "small-2" {
		t.Errorf("change %+v", ch)
	}
}

func TestPlanToCustomKeepsEverything(t *testing.T) {
	ch := Plan(existing(Crush, Messages), Custom, Messages)
	if len(ch.Keep) != 2 || len(ch.Add) != 0 || len(ch.Remove) != 0 {
		t.Errorf("change %+v", ch)
	}
	ch = Plan(nil, Custom, Messages)
	if len(ch.Add) != 1 || ch.Add[0].Slot != "large" || ch.Add[0].Follows != FollowOpus {
		t.Errorf("a custom client with no slot is given its one: %+v", ch)
	}
}
