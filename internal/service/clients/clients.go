// Package clients is what the gateway knows about each kind of client it hands
// a configuration to: the model slots that client's configuration has, which
// of the gateway's two APIs it can be pointed at, and what each slot's model
// name has to look like for the client to treat it right.
//
// A client of the gateway is a named thing the operator set up — "laptop",
// "work crush" — holding one API key; its type is one of the Types below, and
// it keeps that type and the API it was made with (Type.Initial says why).
// Each slot of a client has a model name of that client's own (crush's
// "large"), which its configuration asks for and which means something only
// for that client's key: two clients may both have a "large", bound to
// different models. A slot is bound to a real model of any provider, or
// follows a moving target (the newest Claude Opus, ChatGPT's first-ranked
// model), so moving a client from Claude to GPT is a choice on its page and
// not a round of every configuration. No model name is shared between
// clients, and there is none a request can name that is not some client's
// own — or, for a custom client in direct mode, a real model's id.
//
// The slots are the ones each client's own configuration takes, read from the
// client's source or documentation rather than guessed; each Type says where.
// Labels, roles, names and defaults live here once: the store keeps slot rows
// by these ids, the admin API serves this table to the UI, and the UI writes
// each type's files from it.
//
// A leaf: it imports nothing of the gateway's, so the store, the model service
// and the admin API can all read it.
package clients

import (
	"regexp"
	"slices"
	"strings"
)

// Dialect is the client-facing API a client is pointed at, by the id of the
// gateway's protocol that serves it (internal/api/anthropic, api/openai).
type Dialect string

const (
	// Messages is the Anthropic Messages API, /v1/messages. It reaches the
	// Claude accounts only: a Messages request for a ChatGPT model is
	// refused, naming the slot to change (internal/httpapi/gateway).
	Messages Dialect = "anthropic"
	// Responses is the OpenAI Responses API, /v1/responses. It reaches both:
	// a ChatGPT model as the client wrote it, a Claude one translated
	// (internal/api/openai), which drops the client's reasoning settings —
	// Claude answers without extended thinking on this path.
	Responses Dialect = "openai"
)

// Title is how the UI names a dialect.
func (d Dialect) Title() string {
	switch d {
	case Messages:
		return "Anthropic Messages"
	case Responses:
		return "OpenAI Responses"
	}
	return string(d)
}

// Route is the gateway route a dialect's requests arrive on, which is what
// decides the providers it can reach.
func (d Dialect) Route() string {
	if d == Responses {
		return "/v1/responses"
	}
	return "/v1/messages"
}

// Mode is how a custom client names models. A typed client is always in
// binding mode: its configuration asks for its slots and nothing else.
type Mode string

const (
	// Binding: the client asks for its own slot names, each bound to a model
	// or a moving target, as a typed client does.
	Binding Mode = "binding"
	// Direct: the client asks for real model ids, the connected accounts'
	// models less the ones switched off for it. For a tool that has its own
	// model list and no room for a name of the gateway's.
	Direct Mode = "direct"
)

// Valid reports whether m is a mode.
func (m Mode) Valid() bool { return m == Binding || m == Direct }

// Role is what a slot is for, in words the types share — crush's large and
// opencode's main are both the main model — so the UI writing a type's files
// finds the slot a field takes by what it is for rather than by its id.
type Role string

const (
	RoleMain       Role = "main"
	RoleFast       Role = "fast"
	RoleReview     Role = "review"
	RoleSubagent   Role = "subagent"
	RoleClassifier Role = "classifier"
	RoleMemories   Role = "memories"
	RoleOpus       Role = "opus"
	RoleSonnet     Role = "sonnet"
	RoleHaiku      Role = "haiku"
	RoleFable      Role = "fable"
	// RoleCustom is an operator's own slot on a custom client: no type's
	// field takes it.
	RoleCustom Role = "custom"
)

// Follow is a moving target a slot can be bound to instead of a fixed model:
// what the gateway's built-in aliases used to follow, kept as a binding
// rather than as a name any request could ask for.
//
//	claude:<family>  the newest Claude model of a family the accounts list
//	openai:first     the ChatGPT backend's first-ranked model
//	openai:light     the last-ranked model of that one's generation
//
// internal/service/models resolves them, from the providers' own lists.
type Follow string

// The follow targets the types' slots start on.
const (
	FollowOpus   Follow = "claude:opus"
	FollowSonnet Follow = "claude:sonnet"
	FollowHaiku  Follow = "claude:haiku"
	FollowFable  Follow = "claude:fable"
	FollowFirst  Follow = "openai:first"
	FollowLight  Follow = "openai:light"
)

var familyName = regexp.MustCompile(`^[a-z]+$`)

// ClaudeFollow is the target following a Claude family.
func ClaudeFollow(family string) Follow { return Follow("claude:" + family) }

// Family is the Claude family a claude: target follows, or "".
func (f Follow) Family() string {
	if fam, ok := strings.CutPrefix(string(f), "claude:"); ok && familyName.MatchString(fam) {
		return fam
	}
	return ""
}

// Valid reports whether f is a target the gateway can follow.
func (f Follow) Valid() bool {
	return f.Family() != "" || f == FollowFirst || f == FollowLight
}

// ProviderTitle is how a screen, or a refusal, names a provider's accounts.
func ProviderTitle(provider string) string {
	switch provider {
	case "anthropic":
		return "Claude"
	case "openai":
		return "ChatGPT"
	}
	return provider
}

// Provider is whose models f follows: anthropic or openai.
func (f Follow) Provider() string {
	if f == FollowFirst || f == FollowLight {
		return "openai"
	}
	return "anthropic"
}

// Title is how the UI names a target.
func (f Follow) Title() string {
	switch {
	case f == FollowFirst:
		return "ChatGPT's first-ranked model"
	case f == FollowLight:
		return "ChatGPT's light model"
	case f.Family() != "":
		fam := f.Family()
		return "Newest Claude " + strings.ToUpper(fam[:1]) + fam[1:]
	}
	return string(f)
}

// Slot is one model field of a client's configuration.
type Slot struct {
	// ID is the slot's key in the store: large, small, main, review... It
	// never changes; the name the client asks for does.
	ID    string `json:"id"`
	Label string `json:"label"`
	Role  Role   `json:"role"`
	// Field is where the client's configuration takes it, as the client
	// spells it.
	Field string `json:"field"`
	// Name is the model name a new slot is given, before any mark its
	// dialect needs (Type.Name): short, read in the client's own model
	// picker, and never a real model's id.
	Name string `json:"name"`
	// Follows is the moving target a new slot is bound to, so a client set
	// up today goes on serving the newest model of its kind.
	Follows Follow `json:"follows"`
	// Hint says what the client uses the slot for.
	Hint string `json:"hint"`
}

// Type is one kind of client.
type Type struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Dialects are the APIs it can be pointed at, its default first.
	Dialects []Dialect `json:"dialects"`
	// Slots are its configuration's model fields; for a custom client, the
	// slot it is created with, and the rest are the operator's.
	Slots []Slot `json:"slots"`
	// Custom is the type with no recipe: no files to write, slots of the
	// operator's own naming, and a direct mode.
	Custom bool `json:"custom"`
	// Marks are, per dialect, what every slot name must contain for the
	// client to handle the model right; see the type's own comment.
	Marks map[Dialect]string `json:"marks,omitempty"`
	// Why says why the default dialect is the default, for the UI.
	Why string `json:"why,omitempty"`
	// Sessions are the request headers the type names its chat in, most
	// specific first: what the gateway groups its requests into chats by.
	// None for a custom client, whose requests are read the way each API
	// reads an unknown client's (internal/api, ConversationID).
	Sessions []string `json:"-"`
	// NamesItsChats is a type that asks a model to name each of its sessions
	// itself. The gateway reads that name off the answer, files the request
	// as part of the chat's cost but not one of its turns, and never asks for
	// a name of its own: the client's is the one it shows, and asking too
	// would spend a call on a name about to be replaced.
	//
	// Such a type has no "Name chats" switch. The switch is there because
	// the gateway's titler spends a request on the operator's subscription;
	// reading a name the client paid for anyway spends nothing, so there is
	// nothing to switch off, and its chats are always named.
	NamesItsChats bool `json:"names_its_chats"`
}

// ClaudeCode: Claude Code's model environment, from its model configuration
// documentation (code.claude.com/docs/en/model-config, read 2026-10-20) and
// the committed configs/clients/claude-code.sh.
//
//	ANTHROPIC_MODEL                 the session's model
//	ANTHROPIC_SMALL_FAST_MODEL      titles, summaries, the quick calls on the side
//	ANTHROPIC_DEFAULT_OPUS_MODEL    what /model opus and an "opus" sub-agent run
//	ANTHROPIC_DEFAULT_SONNET_MODEL  ...sonnet
//	ANTHROPIC_DEFAULT_HAIKU_MODEL   ...haiku
//	ANTHROPIC_DEFAULT_FABLE_MODEL   ...fable
//	CLAUDE_CODE_AUTO_MODE_MODEL     the auto-mode classifier's model
//
// A Claude Code client may name its slots and nothing else (see
// internal/service/models, Route), so every model it can ask for of its own
// accord has a slot: the session, the side calls, a sub-agent's tier (a
// sub-agent definition names a tier, which these resolve), and the
// auto-mode classifier, which otherwise asks for a model of its own choosing.
// CLAUDE_CODE_SUBAGENT_MODEL is left out on purpose: set, it overrides the
// tier every sub-agent definition names, which the tier slots already let
// the operator move one at a time. A model typed into /model by hand is
// refused, naming the slots.
//
// Messages only: Claude Code speaks nothing else. Its names are claude-large,
// claude-small, claude-opus and so on rather than the bare large, small,
// opus the other types have, for two reasons in Claude Code itself: its
// /v1/models reading keeps only ids containing "claude" (gateway.handleModels),
// and opus, sonnet, haiku and default are its own model aliases — the
// ANTHROPIC_DEFAULT_*_MODEL variables are what those resolve to, and its
// documentation lists values it ignores as aliases (haiku, default, inherit,
// opusplan for ANTHROPIC_DEFAULT_MODEL), so a bare "haiku" there is not a
// name it would send. The modelPicker behavesAs setting in the generated file tells it which real
// model each name acts as, without which it caps an unknown name at a 200k
// window.
var ClaudeCode = Type{
	ID: "claude-code", Label: "Claude Code",
	// Measured 2026-09-17 (internal/api, conversation.go): the header on every
	// request, the same id in metadata.user_id, which the Messages API reads
	// when the header is missing.
	//
	// It names its own sessions. Read from the 2.1.304 binary
	// (~/.local/share/claude/versions/2.1.304, byte offsets into it) and
	// captured the same day (2026-10-20) against a stub Messages server:
	//
	//	what      one tool-free request on the small-fast slot
	//	          (ANTHROPIC_SMALL_FAST_MODEL), querySource
	//	          generate_session_title, @254164669
	//	system    billing header, "You are Claude Code, ...", then a prompt
	//	          opening "You are naming a coding session so the user can
	//	          pick it out of a long list of sessions." @254160947
	//	user      "<session>\n<the first prompt>\n</session>\n\nWrite the
	//	          title in ..."; the whole prompt unless a flag caps it at
	//	          2,000 characters (@254164974), and none for a prompt
	//	          under 10 (@254165191); the template @254164458
	//	body      "tools": [], stream true, max_tokens 32000, no thinking,
	//	          output_config.format a json_schema of one string "title"
	//	answer    text that is JSON, {"title": "..."}, parsed and trimmed
	//	          (@254164874); stored as the session's ai-title
	//	session   the session's own X-Claude-Code-Session-Id and
	//	          metadata.user_id, so it is filed under the right chat
	//
	// Sent once per session, alongside the first prompt that is not a slash
	// command, and only while the session has no title. Not on a resumed
	// session that already has messages, and not under claude -p: the latch is
	// set closed at start for either, and for CLAUDE_CODE_DISABLE_TERMINAL_TITLE
	// (@273693876, @254160088). Compaction has no call to it. The interactive
	// REPL sent it in the capture, before the first turn; a --continue of
	// that session and a claude -p session sent none. So a headless Claude
	// Code chat is not named — the price of never spending a request on it.
	// Its User-Agent says it was headless, which is how the chat list tells
	// it from one still to be named (Surface, surface.go).
	Sessions:      []string{"X-Claude-Code-Session-Id"},
	NamesItsChats: true,
	Dialects:      []Dialect{Messages},
	Marks:         map[Dialect]string{Messages: "claude"},
	Slots: []Slot{
		{ID: "main", Label: "Main", Role: RoleMain, Field: "ANTHROPIC_MODEL", Name: "large",
			Follows: FollowOpus, Hint: "The session's model, and the sub-agents that inherit it."},
		{ID: "fast", Label: "Small and fast", Role: RoleFast, Field: "ANTHROPIC_SMALL_FAST_MODEL",
			Name: "small", Follows: FollowHaiku,
			Hint: "Titles, summaries and the quick calls Claude Code makes on the side."},
		{ID: "opus", Label: "Opus tier", Role: RoleOpus, Field: "ANTHROPIC_DEFAULT_OPUS_MODEL",
			Name: "opus", Follows: FollowOpus, Hint: "What /model opus and a sub-agent asking for opus run."},
		{ID: "sonnet", Label: "Sonnet tier", Role: RoleSonnet, Field: "ANTHROPIC_DEFAULT_SONNET_MODEL",
			Name: "sonnet", Follows: FollowSonnet,
			Hint: "What /model sonnet and a sub-agent asking for sonnet run."},
		{ID: "haiku", Label: "Haiku tier", Role: RoleHaiku, Field: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			Name: "haiku", Follows: FollowHaiku,
			Hint: "What /model haiku and a sub-agent asking for haiku run."},
		{ID: "fable", Label: "Fable tier", Role: RoleFable, Field: "ANTHROPIC_DEFAULT_FABLE_MODEL",
			Name: "fable", Follows: FollowFable, Hint: "What /model fable runs."},
		{ID: "classifier", Label: "Auto mode", Role: RoleClassifier, Field: "CLAUDE_CODE_AUTO_MODE_MODEL",
			Name: "auto", Follows: FollowSonnet,
			Hint: "The classifier auto mode asks before it runs a tool without asking you."},
	},
}

// Codex: the model keys of Codex's config.toml, from its configuration
// reference (learn.chatgpt.com/docs/config-file/config-reference, read
// 2026-10-20).
//
//	model                           the session's model; compaction runs on it too
//	review_model                    what /review runs (defaults to the session's)
//	agents.default_subagent_model   what spawned agents run, unless the spawn names one
//	memories.extract_model,
//	memories.consolidation_model    memory extraction and consolidation, when memories are on
//
// Each also gets an entry in the model catalog the config points
// model_catalog_json at, carrying the context window of the model it serves —
// Codex sizes its compaction from the catalog and guesses low for a name it
// does not know — and naming the review slot as its auto_review_model_override,
// so the approval reviewer does not ask for a model of its own (Codex's
// codex-auto-review, which the ChatGPT list keeps hidden).
//
// Responses only: Codex speaks nothing else (internal/api/openai). No mark:
// with the catalog, Codex reads everything it needs about a name from there.
var Codex = Type{
	ID: "codex", Label: "Codex CLI",
	// session_id and conversation_id on 0.154.0 (measured, internal/api/openai,
	// ConversationID); session-id and thread-id on Codex main
	// (codex-api/src/requests/headers.rs, read 2026-10-19). No naming request
	// in the 0.154.0 capture: the gateway titler names its chats.
	Sessions: []string{"Session_id", "Conversation_id", "Session-Id", "Thread-Id"},
	Dialects: []Dialect{Responses},
	Slots: []Slot{
		{ID: "main", Label: "Main", Role: RoleMain, Field: "model", Name: "large",
			Follows: FollowOpus, Hint: "The session's model, compaction included."},
		{ID: "review", Label: "Review", Role: RoleReview, Field: "review_model", Name: "review",
			Follows: FollowOpus, Hint: "What /review and the approval reviewer run."},
		{ID: "subagent", Label: "Sub-agents", Role: RoleSubagent, Field: "agents.default_subagent_model",
			Name: "agents", Follows: FollowOpus, Hint: "What agents Codex spawns run, unless the spawn names a model."},
		{ID: "memories", Label: "Memories", Role: RoleMemories,
			Field: "memories.extract_model, memories.consolidation_model", Name: "small",
			Follows: FollowHaiku, Hint: "Memory extraction and consolidation, when memories are on."},
	},
}

// Opencode: model and small_model, opencode's two model keys
// (opencode.ai/config.json), each declared on the provider with the limits of
// the model it serves — opencode takes a declared model's limits only from
// there, and an undeclared limit is 0, which turns auto-compaction off. Its
// agents (build, plan, general, explore) run the main model and its titles
// and summaries the small one unless configured otherwise, so the two cover
// every request it makes of its own accord.
//
// Both dialects work, and Responses is the default. Read from the opencode
// 1.17.13 binary (~/.opencode/bin/opencode, its bundled provider code): its
// built-in "openai" provider builds every model with sdk.responses(id) and
// sends store:false, whatever the id, so a Responses config reaches a Claude
// and a GPT model alike and moving a slot from one to the other needs no new
// file. Over Messages ("anthropic" provider, @ai-sdk/anthropic) opencode
// turns tool-call streaming off for an id that does not contain "claude", so
// Messages names carry it.
var Opencode = Type{
	ID: "opencode", Label: "opencode",
	// x-session-id: ses_<id>, beside x-session-affinity, measured 2026-09-17;
	// and the first request of a session is its own naming request, "You are
	// a title generator. You output ONLY a thread title." with no tools
	// (internal/service/titles, measured the same day).
	Sessions:      []string{"X-Session-Id"},
	NamesItsChats: true,
	Dialects:      []Dialect{Responses, Messages},
	Marks:         map[Dialect]string{Messages: "claude"},
	Why: "Responses is the default: opencode sends every model of its OpenAI provider over it, so a slot " +
		"moves between Claude and GPT with no new file. Claude answers through the gateway's translation " +
		"there, without extended thinking; pick Anthropic Messages to keep thinking on a Claude-only client.",
	Slots: []Slot{
		{ID: "main", Label: "Main", Role: RoleMain, Field: "model", Name: "large",
			Follows: FollowOpus, Hint: "The model doing the work, and every agent's."},
		{ID: "small", Label: "Small", Role: RoleFast, Field: "small_model", Name: "small",
			Follows: FollowHaiku, Hint: "Titles and summaries."},
	},
}

// Crush: models.large and models.small, crush's two model types
// (crush internal/config, SelectedModelTypeLarge/Small, at 76cc5c5): its
// coder runs the large one, its titles and summaries the small one.
//
// Messages is the default, for a reason in crush's source: its "openai"
// provider type (internal/agent/coordinator.go, buildOpenaiProvider) asks
// fantasy for the Responses API, and fantasy (providers/openai, v0.45.1)
// grants it only to a model id matching gpt-[4-9] or gpt-<two digits>
// anywhere in it, sending every other id to /chat/completions, which this
// gateway does not serve. So a crush on Responses works only while each of
// its names carries "gpt-5", and its names say so. Messages has no such rule
// and is the path the gateway's crush handling was measured on.
var Crush = Type{
	ID: "crush", Label: "crush",
	// x-session-id and x-session-affinity, a hash of the session id, on every
	// call of a session whatever the provider type (sessionHeaders,
	// internal/agent/agent.go:1583 at 76cc5c5), its naming request included;
	// that request is sessionAgent.GenerateTitle (agent.go:1833), described in
	// internal/service/titles.
	Sessions:      []string{"X-Session-Id"},
	NamesItsChats: true,
	Dialects:      []Dialect{Messages, Responses},
	Marks:         map[Dialect]string{Responses: "gpt-5"},
	Why: "Anthropic Messages is the default: it is the path crush was measured on, with thinking. " +
		"crush speaks OpenAI Responses only for a model id it takes for a GPT one, so on Responses " +
		"its names carry gpt-5 — and then a slot moves between Claude and GPT with no new file.",
	Slots: []Slot{
		{ID: "large", Label: "Large", Role: RoleMain, Field: "models.large", Name: "large",
			Follows: FollowOpus, Hint: "The model doing the work."},
		{ID: "small", Label: "Small", Role: RoleFast, Field: "models.small", Name: "small",
			Follows: FollowHaiku, Hint: "Titles and summaries."},
	},
}

// Custom is a tool the gateway has no recipe for: its page shows the key, the
// two base URLs and the slot names, and the operator names its slots — or, in
// direct mode, lets it name real models. It starts with one slot.
var Custom = Type{
	ID: "custom", Label: "Custom",
	Dialects: []Dialect{Messages, Responses},
	Custom:   true,
	Slots: []Slot{
		{ID: "large", Label: "Large", Role: RoleMain, Name: "large", Follows: FollowOpus,
			Hint: "The model the tool asks for."},
	},
}

// All are the types, in the order the UI offers them.
var All = []Type{ClaudeCode, Codex, Opencode, Crush, Custom}

// Find is a type by id.
func Find(id string) (Type, bool) {
	for _, t := range All {
		if t.ID == id {
			return t, true
		}
	}
	return Type{}, false
}

// DefaultDialect is the dialect a new client of this type is pointed at.
func (t Type) DefaultDialect() Dialect { return t.Dialects[0] }

// Speaks reports whether the type can be pointed at a dialect.
func (t Type) Speaks(d Dialect) bool { return slices.Contains(t.Dialects, d) }

// Mark is what every slot name must contain on a dialect, or "".
func (t Type) Mark(d Dialect) string { return t.Marks[d] }

// Slot is one of the type's slots by id.
func (t Type) Slot(id string) (Slot, bool) {
	for _, s := range t.Slots {
		if s.ID == id {
			return s, true
		}
	}
	return Slot{}, false
}

// SlotFor is a slot of this type by id, or for a custom client's own slot a
// slot of that id following what its first slot does: what an operator-named
// slot is.
func (t Type) SlotFor(id string) (Slot, bool) {
	if s, ok := t.Slot(id); ok {
		return s, true
	}
	if t.Custom && ValidName(id) {
		return Slot{ID: id, Label: id, Role: RoleCustom, Name: id, Follows: FollowOpus}, true
	}
	return Slot{}, false
}

// Fits reports whether a name is one the type handles right on a dialect.
func (t Type) Fits(d Dialect, name string) bool {
	m := t.Mark(d)
	return m == "" || strings.Contains(name, m)
}

// name is what a model name may be: lower case, digits, dots and dashes, as a
// real id is, so it sits in a config and a URL unquoted.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// ValidName reports whether s can be a model name, or a custom slot's id.
func ValidName(s string) bool { return name.MatchString(s) }

// Name is the model name slot is given on dialect d: the slot's own name,
// with the dialect's mark in front when it lacks it. taken says a name is
// already this client's; a number is added until one is free. The client's
// own name is no part of it: a rename never touches its configuration.
func (t Type) Name(d Dialect, slot string, taken func(string) bool) string {
	base := slot
	if s, ok := t.Slot(slot); ok && s.Name != "" {
		base = s.Name
	}
	if m := t.Mark(d); m != "" && !strings.Contains(base, m) {
		base = m + "-" + base
	}
	n := base
	for i := 2; taken != nil && taken(n); i++ {
		n = base + "-" + itoa(i)
	}
	return n
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// Initial is one slot a new client is made with: one of its type's, under the
// name its dialect needs, following the slot's default target.
type Initial struct {
	Slot    string
	Name    string
	Follows Follow
}

// Initial are the slots a new client of this type starts with on dialect d,
// the type's default when it cannot speak d. A custom client starts with its
// one slot; the rest are the operator's to add.
//
// A client keeps the type and dialect it was made with: its key was handed
// out for that client, and the names its configuration asks for carry the
// dialect's mark, so another dialect would be another set of names — another
// client, with a key of its own.
func (t Type) Initial(d Dialect) (Dialect, []Initial) {
	if !t.Speaks(d) {
		d = t.DefaultDialect()
	}
	slots := t.Slots
	if t.Custom {
		slots = slots[:1]
	}
	taken := map[string]bool{}
	out := make([]Initial, 0, len(slots))
	for _, s := range slots {
		n := t.Name(d, s.ID, func(n string) bool { return taken[n] })
		taken[n] = true
		out = append(out, Initial{Slot: s.ID, Name: n, Follows: s.Follows})
	}
	return d, out
}
