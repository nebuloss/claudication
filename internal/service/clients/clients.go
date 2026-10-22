// Package clients is what the gateway knows about each kind of client it hands
// a configuration to: the model slots that client's configuration has, which
// of the gateway's two APIs it speaks, and what its configuration has to say
// for the client to treat a name of the gateway's as the model behind it.
//
// A client of the gateway is a named thing the operator set up — "laptop",
// "work crush" — holding one API key; its type is one of the Types below, and
// it keeps that type for good (Type.Initial says why). The API it speaks is
// its type's (Type.API), not a choice made per client: both APIs reach both
// providers, so which one a client speaks decides nothing about what it can
// be served, and each type is pointed at the one it speaks natively.
// Each slot of a client has a model name of that client's own (crush's
// "large"), which its configuration asks for and which means something only
// for that client's key: two clients may both have a "large", bound to
// different models. A slot is bound to a real model of any provider, or
// follows a family (Claude Opus, GPT Sol: the newest version the serving
// account has), so moving a client from Claude to GPT is a choice on its page and
// not a round of every configuration. No model name is shared between
// clients, and there is none a request can name that is not some client's
// own — or, for a custom client in direct mode, a real model's id.
//
// A slot's name is plain — large, small, review — and names no provider,
// because a slot can be bound to a model of either. Some clients do switch
// features on a model's id; none of them needs the name for it, because each
// such switch has a setting that says the same thing, and the files the
// gateway writes set it. Each Type below records what its client reads off
// an id, where, and the setting that replaces it, measured against the
// client itself. Names used to carry "claude" for Claude Code and opencode;
// neither needs it (see their Types), and clients made then keep the names
// they were given.
//
// The slots are the ones each client's own configuration takes, read from the
// client's source or documentation rather than guessed; each Type says where.
// Labels, roles, names and defaults live here once: the store keeps slot rows
// by these ids, the admin API serves this table to the UI, and the gateway writes
// each type's files from it.
//
// A leaf: it imports nothing of the gateway's, so the store, the model service
// and the admin API can all read it.
package clients

import (
	"regexp"
	"strings"
)

// Dialect is a client-facing API, by the id of the gateway's protocol that
// serves it (internal/api/anthropic, api/openai). Both reach both providers:
// a model of the provider that speaks the client's API as the client wrote
// the request, one of the other translated.
type Dialect string

const (
	// Messages is the Anthropic Messages API, /v1/messages.
	Messages Dialect = "anthropic"
	// Responses is the OpenAI Responses API, /v1/responses.
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
// opencode's main are both the main model — so the writer of a type's files
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
	// RolePicker is an operator's own slot on a typed client: a name its
	// files declare beside the type's own, which its model picker then lists
	// — Claude Code's /model, Codex's /model and -m, opencode's model list,
	// crush's ctrl+l. No setting asks for it of the client's own accord; it
	// is there to be picked by hand for a chat, and declared with what the
	// model it serves needs, as the type's own names are.
	RolePicker Role = "picker"
)

// Follow is a model family a slot can be bound to instead of one fixed
// version: what the gateway's built-in aliases used to follow, kept as a
// binding rather than as a name any request could ask for.
//
//	claude:<family>  a Claude line: opus, sonnet, haiku, fable
//	openai:<line>    a ChatGPT line: sol, astra, luna, terra
//
// A family is served by the newest of its versions that the serving account
// lists and has on, resolved per account at request time
// (internal/service/models), so an account on a plan without the newest Opus
// still serves Opus, with the newest one it has.
//
// ChatGPT's lines are the slug's last word (gpt-6.1-sol is sol), because that
// is what the backend itself treats as one model across releases. Its own
// descriptions, 2026-10-19 (internal/provider/openai/testdata/models.json):
// gpt-6.1-sol "Latest workhorse model", gpt-6-sol "Previous generation
// workhorse model", gpt-5.6-sol "Older generation workhorse model";
// gpt-6-luna "Fast and affordable", gpt-5.6-luna "Older fast and efficient";
// gpt-6-astra "Frontier intelligence"; gpt-5.6-terra "Older balanced". A plan
// without gpt-6.1-sol falls back to the previous workhorse, which is the
// substitute that means something. Grouping by generation (GPT-6.1, GPT-6,
// GPT-5.6), as the model tree folds a list, would make one choice of three
// unlike models; the ranking targets openai:first and openai:light, which
// slots followed until schema 040, said nothing about what to fall back to
// on an account whose ranking differs.
type Follow string

// The families the types' slots start on, and ChatGPT's two that 040 moved
// the ranking targets onto.
const (
	FollowOpus   Follow = "claude:opus"
	FollowSonnet Follow = "claude:sonnet"
	FollowHaiku  Follow = "claude:haiku"
	FollowFable  Follow = "claude:fable"
	FollowSol    Follow = "openai:sol"
	FollowLuna   Follow = "openai:luna"
)

var familyName = regexp.MustCompile(`^[a-z]+$`)

// familyPrefixes are each provider's prefix in a Follow, and in the model id
// a direct-mode client names the family by (Follow.ModelID).
var familyPrefixes = []struct{ provider, follow, id string }{
	{"anthropic", "claude:", "claude-"},
	{"openai", "openai:", "gpt-"},
}

// ClaudeFollow is the family of a Claude line.
func ClaudeFollow(family string) Follow { return Follow("claude:" + family) }

// FamilyFollow is a provider's family, "" for a provider that has none.
func FamilyFollow(provider, family string) Follow {
	for _, p := range familyPrefixes {
		if p.provider == provider {
			return Follow(p.follow + family)
		}
	}
	return ""
}

// Family is the line f follows, or "".
func (f Follow) Family() string {
	for _, p := range familyPrefixes {
		if fam, ok := strings.CutPrefix(string(f), p.follow); ok && familyName.MatchString(fam) {
			return fam
		}
	}
	return ""
}

// Valid reports whether f is a family the gateway can follow.
func (f Follow) Valid() bool { return f.Family() != "" }

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
	for _, p := range familyPrefixes {
		if strings.HasPrefix(string(f), p.follow) {
			return p.provider
		}
	}
	return "anthropic"
}

// ModelID is the model id a direct-mode client names the family by:
// claude-opus, gpt-sol. No real model is named so — a real one carries its
// version — so the id cannot mean two things; it is also the key a client's
// switch over the family is stored under (store.APIKey.ModelsOff).
func (f Follow) ModelID() string {
	fam := f.Family()
	for _, p := range familyPrefixes {
		if fam != "" && strings.HasPrefix(string(f), p.follow) {
			return p.id + fam
		}
	}
	return ""
}

// FollowOf is the family a direct-mode model id names (claude-opus is
// claude:opus), or "" for an id that names a real model, or nothing.
func FollowOf(id string) Follow {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range familyPrefixes {
		if fam, ok := strings.CutPrefix(id, p.id); ok && familyName.MatchString(fam) {
			return Follow(p.follow + fam)
		}
	}
	return ""
}

// Title is how the UI names a family: Claude Opus, GPT Sol.
func (f Follow) Title() string {
	fam := f.Family()
	if fam == "" {
		return string(f)
	}
	word := strings.ToUpper(fam[:1]) + fam[1:]
	if f.Provider() == "openai" {
		return "GPT " + word
	}
	return "Claude " + word
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
	// Name is the model name a new slot is given (Type.Name): short, plain,
	// read in the client's own model picker, and never a real model's id.
	Name string `json:"name"`
	// Follows is the family a new slot is bound to, so a client set up
	// today goes on serving the newest model of its kind.
	Follows Follow `json:"follows"`
	// Hint says what the client uses the slot for.
	Hint string `json:"hint"`
}

// Type is one kind of client.
type Type struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// API is the API it speaks, the one its configuration points at: its
	// own, fixed by the type. "" for a custom client, whose key works on
	// both and whose page gives both addresses.
	API Dialect `json:"api,omitempty"`
	// Slots are its configuration's model fields; for a custom client, the
	// slot it is created with, and the rest are the operator's.
	Slots []Slot `json:"slots"`
	// Custom is the type with no recipe: no files to write, slots of the
	// operator's own naming, and a direct mode.
	Custom bool `json:"custom"`
	// ExtraSlots is the role an operator's own slot takes on this type, and
	// "" for a type that has none: one whose configuration could not declare
	// a model it does not name in a field. A custom client's are RoleCustom,
	// since it is all slots of the operator's own naming; every typed
	// client's are RolePicker, since each one's files can declare a model
	// that no setting asks for and its picker lists it (each Type says
	// where). Such a slot is added, renamed, rebound and removed on the
	// client's page; the type's own Slots stay (Removable).
	ExtraSlots Role `json:"extra_slots,omitempty"`
	// ExtraHint says what an operator's own slot is for, as Slot.Hint does.
	ExtraHint string `json:"extra_hint,omitempty"`
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
// Messages: Claude Code speaks nothing else.
//
// Its names are the plain large, small, opus, sonnet, haiku, fable and auto.
// A tier slot is named for the tier it fills — Claude Code's own word for it,
// the one /model and a sub-agent's model: field use — and not for the model
// behind it, which may be a GPT one: a sub-agent asking for opus asks the
// gateway for "opus", a name that means what the client meant by it.
// What Claude Code reads off a model id, read from the 2.1.304 binary
// (~/.local/share/claude/versions/2.1.304, byte offsets) on 2026-10-20:
//
//	/v1/models    keeps ids matching /(claude|anthropic)/i (@239613896,
//	              @254499289), but only asks with
//	              CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY set (@239610674,
//	              and the bootstrap's own gate @254493275);
//	              the file does not set it, so Claude Code never asks, and
//	              every name it uses is in the file anyway
//	family        y4 picks opus/sonnet/haiku/fable by substring (@239618383)
//	              and the canonical id by full versioned id
//	              (QS @239676434); claude-large matched neither, claude-sonnet
//	              and claude-haiku matched a family by accident
//	behavesAs     the modelPicker setting's row for a name: "the ID of a
//	              model it does know whose client-side handling — prompt
//	              profile, capability and effort defaults — applies to it"
//	              (@237153249); looked up by the name (BU @239682076)
//	telemetry     a model name without "claude" is logged as null rather
//	              than "other" (@244612190): nothing a request carries
//	aliases       opus, sonnet, haiku, fable, best, opusplan (+[1m]) (q4
//	              @237017993): a tier variable set to its own alias is sent
//	              verbatim, so /model opus asks for "opus"
//
// So the setting that makes a name behave as its model is behavesAs, which
// the generated file has always written for every name but the classifier's,
// and the name itself decides nothing. Captured the same day against a stub
// Messages server, in a network namespace with only loopback, with the
// generated configuration — claude -p once on the session model and once per
// --model opus, sonnet, haiku and fable — the requests under claude-large,
// claude-opus... and under large, opus... (or large, strong, balanced...)
// are byte for byte the same but for the model field and the system prompt's
// "You are powered by the model <name>" line: the same betas, thinking
// (adaptive, display omitted), effort, max_tokens, context_management, cache
// marks, tools and system prompt. Without behavesAs the two sets differed —
// claude-sonnet and claude-haiku drew the Sonnet and Haiku prompt profiles
// by substring, the rest none — and with behavesAs a name follows its row
// even against its family word: "opus" set to behave as Haiku drew the Haiku
// profile.
//
// The classifier's name stays out of the picker, as before: nobody picks it.
// Claude Code checks CLAUDE_CODE_AUTO_MODE_MODEL against a short list of real
// ids it refuses there (claude-sonnet-5-5, claude-opus-5-5, claude-opus-5;
// P$o @239656802) and falls back to a model of its own choosing, which this
// client may not name; "auto" is on no list, as "claude-auto" was not.
//
// An operator's own name (RolePicker) has no variable: a session switches to
// it with /model <name>, and the file gives it a modelPicker row with
// behavesAs like every other name. Read from the 2.1.308 binary
// (~/.local/share/claude/versions/2.1.308, byte offsets) on 2026-10-22, the
// 2.1.304 one having been replaced by the updater:
//
//	accepted   /model validates a name (Hbr @257365329) by the availableModels
//	           allowlist, then returns valid at once for a name that is a
//	           modelPicker row (@257365729); any other name is probed with a
//	           one-token request, querySource model_validation (@257366256),
//	           and refused as "Model '<name>' not found" on a 404
//	offered    a row is listed in the picker when its name passes pUo
//	           (@243623280), which holds for a plain name off a first-party
//	           base URL: the catalog check behind it, ZF, returns nothing
//	           unless the base URL is Anthropic's (@242188638)
//	unknown    a name with no row and no behavesAs "isn't described by this
//	           version's model catalog" (@242169658) and is run at a guessed
//	           200k window
//
// Captured the same day against a stub Messages server in a loopback-only
// network namespace, with a scratch HOME and CLAUDE_CONFIG_DIR and the
// generated --settings plus a row {"model":"deep","behavesAs":
// "claude-opus-5-5"}: claude -p "/model deep" answered "Set model to deep"
// and sent nothing; "/model nopicker", a name with no row the stub refused,
// sent the one-token probe and answered "Model 'nopicker' not found". A turn
// on --model deep sent max_tokens 128000, effort medium and the Opus prompt
// profile; with the row saying claude-haiku-5-5, the Haiku profile; with no
// row, max_tokens 32000, effort high and a warning that the model is
// unrecognized. So the row is what makes an extra name both pickable without
// a probe and handled as the model it serves.
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
	API:           Messages,
	ExtraSlots:    RolePicker,
	ExtraHint:     "In /model's list: a session switches to it with /model <name>.",
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
// Responses: Codex speaks nothing else (internal/api/openai).
//
// Nothing in Codex's handling of a model is keyed off a name the catalog
// carries: the catalog entry is looked up by its slug, and everything Codex
// decides about the model — its window and compaction, reasoning levels and
// summaries, tool shapes (apply_patch_tool_type, shell_type), parallel tool
// calls, its system prompt (base_instructions) — is a field of that entry
// (configs/clients/codex-models.json). The name is only the key, so a plain
// one behaves as fully as a real slug would; the one way a name goes wrong
// is a catalog without it, when Codex falls back to its generic metadata
// (docs/clients.md, "The model catalog").
//
// So an operator's own name (RolePicker) is one more catalog entry, written
// from the committed one with the window of the model it serves, as the
// type's names are: Codex's /model lists the catalog, and codex -m <name>
// asks for an entry by its slug. config.toml has no key for it.
var Codex = Type{
	ID: "codex", Label: "Codex CLI",
	// session_id and conversation_id on 0.154.0 (measured, internal/api/openai,
	// ConversationID); session-id and thread-id on Codex main
	// (codex-api/src/requests/headers.rs, read 2026-10-19). No naming request
	// in the 0.154.0 capture: the gateway titler names its chats.
	Sessions:   []string{"Session_id", "Conversation_id", "Session-Id", "Thread-Id"},
	API:        Responses,
	ExtraSlots: RolePicker,
	ExtraHint:  "In Codex's /model list, and for codex -m <name>.",
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
// Messages, through its built-in "anthropic" provider (@ai-sdk/anthropic),
// overridden to point at the gateway: either of opencode's providers would
// reach both of the gateway's, and this is the one with thinking for Claude.
//
// Its names are the plain large and small. What opencode reads off a model's
// id (model.api.id, the id it sends), read from the 1.17.13 binary
// (~/.opencode/bin/opencode, byte offsets into its bundled code) on
// 2026-10-20:
//
//	tool streaming  the call options start with toolStreaming false for an id
//	                without "claude" on @ai-sdk/anthropic (@97905071); the
//	                SDK reads it (@111708457) and leaves eager_input_streaming
//	                off every tool (@111672706), so a tool call's input
//	                arrives whole
//	system prompt   the Anthropic-tuned one for an id with "claude", its
//	                generic one otherwise (@96421643)
//	tool call ids   in the history, rewritten to [a-zA-Z0-9_-] for an id with
//	                "claude" (@97891337); ids this gateway hands out already
//	                are (toolu_..., and call_... from a GPT model)
//	caching         cache marks for provider "anthropic" whatever the id
//	                (@97895334)
//	top_p           dropped beside a temperature for "claude-*" ids in the SDK
//	                (@112130321); the file sends no temperature
//
// Tool streaming has a setting: a declared model's own options are merged
// over those defaults (@97937152 reads them from the config; @96424514
// merges model.options last but for the agent's and the variant's), so
// "options": {"toolStreaming": true} on each name turns it back on. Captured
// the same day against a stub, in a loopback-only network namespace,
// opencode run with the generated file: under claude-large every one of the
// 10 tools carried eager_input_streaming; under large none did, and with the
// option all 10 did again, the requests otherwise the same.
//
// The system prompt has no per-model setting — an agent's own prompt
// replaces it for every model the agent runs, which would pin opencode's
// prompt text into this file — and is the one difference left: a plain name
// gets opencode's generic prompt. It is no feature switch: the tools, thinking,
// caching and streaming are the same, and the mark sent the Claude-tuned
// prompt to a GPT model as readily as to Claude. So no mark.
//
// An operator's own name (RolePicker) is one more model declared on the
// provider, at its own model's limits and with toolStreaming on, as the two
// are: opencode's model list (/models) offers every declared model, and
// picking one runs the session on it. No key of the file names it.
var Opencode = Type{
	ID: "opencode", Label: "opencode",
	// x-session-id: ses_<id>, beside x-session-affinity, measured 2026-09-17;
	// and the first request of a session is its own naming request, "You are
	// a title generator. You output ONLY a thread title." with no tools
	// (internal/service/titles, measured the same day).
	Sessions:      []string{"X-Session-Id"},
	NamesItsChats: true,
	API:           Messages,
	ExtraSlots:    RolePicker,
	ExtraHint:     "In opencode's model list (/models), to pick for a session by hand.",
	Slots: []Slot{
		{ID: "main", Label: "Main", Role: RoleMain, Field: "model", Name: "large",
			Follows: FollowOpus, Hint: "The model doing the work, and every agent's."},
		{ID: "small", Label: "Small", Role: RoleFast, Field: "small_model", Name: "small",
			Follows: FollowHaiku, Hint: "Titles and summaries."},
	},
}

// Crush: models.large and models.small, crush's two model types
// (crush internal/config, SelectedModelTypeLarge/Small, at 76cc5c5). Read at
// 76cc5c5 for v0.97.2:
//
//	large    the coder, task and plan agents (agent/agent.go:1411-1417) and
//	         a session's summary (agent.go:1851-1884)
//	small    a session's title, and the agentic fetch tool's own agent
//	         (agentic_fetch_tool.go:150-184); nothing else
//	picker   ctrl+l lists every model of every configured provider, in a
//	         Large and a Small tab, by each model's name
//	         (ui/model/ui.go:2830)
//	picks    saved to crush's data file — ~/.local/share/crush/crush.json,
//	         %LOCALAPPDATA%\crush on Windows — as models.<type> with the
//	         model, provider, reasoning effort and max_tokens of the moment
//	         (config/store.go:528-547). That file loads after
//	         ~/.config/crush/crush.json and wins (config/load.go:944-977),
//	         so the file's models.large and models.small are a first run's
//	         defaults only, and a pick pins max_tokens (load.go:838-842)
//	/models  asked only of a custom provider with no models listed or
//	         discover_models set; the file lists them, so crush is never
//	         asked to
//
// So an operator's own slot is worth having on crush: the file declares it
// and the picker lists it, under a name saying what it serves, ready to be
// picked into either tab. Its role is RolePicker. A pick of it is saved by crush with that moment's max_tokens,
// which the gateway fits to the model the slot serves when it is rebound
// (internal/relay/passes, FitMaxTokens).
//
// Messages, through its "anthropic" provider type, the path the gateway's
// crush handling was measured on. crush itself has no rule about a model's
// id on it (the id rules in internal/agent/coordinator.go are for OpenCode's
// providers, Gemini and MiniMax). Its SDK has one: fantasy (providers/
// anthropic/anthropic.go:43-110, v0.45.1) asks for summarized thinking only
// for an id containing claude-opus-5, claude-sonnet-5, claude-fable-5,
// claude-mythos-5 or a recent claude-opus-4-N, which no slot name matches —
// nor did any mark — and those models, by fantasy's reading of Anthropic's
// thinking documentation, return empty thinking text unless a display is
// asked for, so crush would show none. The setting is the provider option thinking_display
// (provider_options.go:83), merged from the provider's provider_options
// (coordinator.go:494); captured against a stub with the installed crush
// (v0.97.2), "large" sends thinking {"type": "adaptive"} without it and
// {"type": "adaptive", "display": "summarized"} with it. The file sets it on
// the provider, so it holds for every name.
//
// Its "openai" type would not do: it asks fantasy for the Responses API
// (internal/agent/coordinator.go, buildOpenaiProvider), and fantasy
// (providers/openai, v0.45.1) grants that only to an id matching gpt-[4-9] or
// gpt-<two digits>, sending every other to /chat/completions, which this
// gateway does not serve.
var Crush = Type{
	ID: "crush", Label: "crush",
	// x-session-id and x-session-affinity, a hash of the session id, on every
	// call of a session whatever the provider type (sessionHeaders,
	// internal/agent/agent.go:1583 at 76cc5c5), its naming request included;
	// that request is sessionAgent.GenerateTitle (agent.go:1833), described in
	// internal/service/titles.
	Sessions:      []string{"X-Session-Id"},
	NamesItsChats: true,
	API:           Messages,
	ExtraSlots:    RolePicker,
	ExtraHint:     "In crush's model picker (ctrl+l), to pick for either model by hand.",
	Slots: []Slot{
		{ID: "large", Label: "Large", Role: RoleMain, Field: "models.large", Name: "large",
			Follows: FollowOpus, Hint: "The model doing the work, and the summaries. crush starts on it."},
		{ID: "small", Label: "Small", Role: RoleFast, Field: "models.small", Name: "small",
			Follows: FollowHaiku, Hint: "Session titles and web fetches. crush starts on it."},
	},
}

// Custom is a tool the gateway has no recipe for: its page shows the key, the
// two base URLs and the slot names, and the operator names its slots — or, in
// direct mode, lets it name real models. It starts with one slot, and its key
// works on both APIs: which one the tool speaks is the tool's.
var Custom = Type{
	ID: "custom", Label: "Custom",
	Custom:     true,
	ExtraSlots: RoleCustom,
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

// Slot is one of the type's slots by id.
func (t Type) Slot(id string) (Slot, bool) {
	for _, s := range t.Slots {
		if s.ID == id {
			return s, true
		}
	}
	return Slot{}, false
}

// SlotFor is a slot of this type by id, or for an operator's own slot on a
// type that has them (ExtraSlots) a slot of that id in the type's role for
// them, following the newest Opus as a type's main slot starts on: what an
// operator-named slot is.
func (t Type) SlotFor(id string) (Slot, bool) {
	if s, ok := t.Slot(id); ok {
		return s, true
	}
	if t.ExtraSlots != "" && ValidName(id) {
		return Slot{ID: id, Label: id, Role: t.ExtraSlots, Name: id, Follows: FollowOpus, Hint: t.ExtraHint}, true
	}
	return Slot{}, false
}

// Removable reports whether a client of this type can have its slot of this
// id taken away: an operator's own, on a type that has them. A type's own
// slots are fields its files always write — crush's models.large and
// models.small, Claude Code's model variables — and stay. A custom client's are all the operator's, the one
// it starts with included, since no file of the gateway's names any.
func (t Type) Removable(id string) bool {
	if t.ExtraSlots == "" {
		return false
	}
	_, typed := t.Slot(id)
	return t.Custom || !typed
}

// name is what a model name may be: lower case, digits, dots and dashes, as a
// real id is, so it sits in a config and a URL unquoted.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)

// ValidName reports whether s can be a model name, or a custom slot's id.
func ValidName(s string) bool { return name.MatchString(s) }

// Name is the model name slot is given: the slot's own name, or for an
// operator's slot its id. taken says a name is already this client's; a
// number is added until one is free. The client's own name is no part of it:
// a rename never touches its configuration.
func (t Type) Name(slot string, taken func(string) bool) string {
	base := slot
	if s, ok := t.Slot(slot); ok && s.Name != "" {
		base = s.Name
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

// Initial is one slot a new client is made with: one of its type's, under its
// default name, following the slot's default target.
type Initial struct {
	Slot    string
	Name    string
	Follows Follow
}

// Initial are the slots a new client of this type starts with. A custom
// client starts with its one slot; the rest are the operator's to add.
//
// A client keeps the type it was made with: its key was handed out for that
// client, and its slots are its type's configuration fields, so another type
// would be another set of slots — another client, with a key of its own.
func (t Type) Initial() []Initial {
	slots := t.Slots
	if t.Custom {
		slots = slots[:1]
	}
	taken := map[string]bool{}
	out := make([]Initial, 0, len(slots))
	for _, s := range slots {
		n := t.Name(s.ID, func(n string) bool { return taken[n] })
		taken[n] = true
		out = append(out, Initial{Slot: s.ID, Name: n, Follows: s.Follows})
	}
	return out
}
