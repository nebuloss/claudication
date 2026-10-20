// Package titles names chats: reading the name a client asked a model for as
// it goes past, and — where the client's "Name chats" switch is on — asking for
// one where the client never does. See the long comment below for what was
// measured before either was built, and why the request it makes is the
// caller's own with one message appended.
package titles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"claudication/internal/api"
	"claudication/internal/pool"
	"claudication/internal/relay"
	"claudication/internal/request"
	"claudication/internal/store"
)

// Naming a chat.
//
// Claude Code never asks a model to name its own conversations, so unlike
// opencode and crush there is no title on the wire to read. The gateway asks
// for one itself, the way Claude Code's own /btw asks a side question: one
// turn, over the conversation that is already in flight, written back nowhere
// the client can see.
//
// This is the only thing in the gateway that originates upstream traffic on
// the operator's subscription rather than relaying someone else's, which is
// why each client has a switch for it, why it is made under that client's own
// key and held to its limits — skipped, not refused, when the client is at
// one — and why every failure below ends in "no title" rather than a retry.
// It goes to whichever provider served the chat, Anthropic or ChatGPT, in
// that provider's dialect.
//
// The cost, measured before it was built (2026-09-17, Haiku, an 11k-token
// conversation):
//
//	main conversation, warming        cache_create=11406  cache_read=    0
//	side call, its own system prompt   cache_create= 7886  cache_read=    0
//	side call, caller's prompt+tools   cache_create=    0  cache_read=11406
//
// So the request is the caller's own, with one message appended. Swapping in a
// "you are a title generator" system prompt — the obvious way to ask — misses
// the cache completely and writes a second entry on top of it, turning a few
// hundred billed tokens into a full re-send of the conversation.
//
// Two consequences follow from that measurement and are not negotiable:
// the call must go to the account that served the chat, because the cache is
// per-account; and it is made on the chat's own model, because the cache is
// per-model too and a cheaper model would miss it entirely — a Haiku call that
// re-sends everything costs more than a Sonnet call that reads it back.

// Reading a name the client asked for itself.
//
// opencode and crush both name their own conversations by asking a model, and
// that request comes through this gateway carrying the same session id as the
// chat it belongs to. Its answer is the title the client will display —
// verbatim, not an approximation — so for those clients the name is already
// going past and only has to be noticed.
//
// Measured on the wire (2026-09-17). opencode sends it as a separate turn with
// no tools and a system prompt that says what it is:
//
//	model   claude-haiku-4-5-20251001     tools: 0
//	system  "You are a title generator. You output ONLY a thread title."
//	user    Generate a title for this conversation: "say ok"
//
// crush does the same with "You will generate a short title based on the first
// message a user begins a conversation with."
//
// Matching on a prompt string is brittle and will drift as those clients
// change. It fails safe in both directions: a missed match costs nothing but a
// name, and generation still covers the chat if it is switched on.
//
// It also stops generation wasting a request. For opencode the title call is
// the *first* request of a session — measured: 2,618 bytes before the 42,284
// byte main turn — so without this the gateway would ask for a title of a
// conversation whose only content is a request for a title.

// crush, read from its source at 76cc5c5 (FSL: facts only, nothing copied):
//
//   - It names a session with the first message of it, by
//     sessionAgent.GenerateTitle (internal/agent/agent.go:1833), on its small
//     model, then its large one if the small one fails or runs out of room
//     (agent.go:1883).
//   - The call is a streamed agent turn with no tools: system prompt
//     templates/title.md ("You will generate a short title based on the
//     first message a user begins a conversation with.") plus "\n /no_think",
//     and one user message, "Generate a concise title for the following
//     content:\n\n<the message>\n <think>\n\n</think>" (agent.go:1867).
//   - Max output 40 tokens, or the model's default for a model that reasons
//     (agent.go:1894) — not a field to match on.
//   - It carries the session's x-session-id and x-session-affinity
//     (sessionHeaders, agent.go:1583), as every call of the session does,
//     over whichever provider type is configured: so it is the chat's own,
//     on Messages and on Responses alike.
//
// So a crush chat is never named by the gateway (NamesItsChats on the type, in
// internal/service/clients): its own title is
// read off that answer and wins over anything the gateway wrote, and the
// title request is recorded as part of the chat's cost but not as one of its
// turns (store.UsageEvent.Naming).

// titleMarkers identify a client asking a model to name a conversation. Lower
// case; the system text is folded before comparison.
var titleMarkers = []string{
	"you are a title generator", // opencode
	"generate a short title",    // crush
	"generate a concise title",  // crush's user prompt
	"you output only a thread title",
}

// maxCapturedTitleAnswer bounds what is held while watching for a title. A
// title is a line; anything past this is not one, and a cap keeps a
// misbehaving upstream from streaming into memory.
const maxCapturedTitleAnswer = 64 << 10

// IsClientTitleRequest reports whether this request is a client naming its own
// conversation.
//
// Two conditions, both required. No tools, because an agent turn always binds
// them and a title call never does — that alone excludes nearly everything.
// And a marker in the system text, which is what distinguishes a title request
// from any other tool-less turn.
//
// Either dialect's body: a Messages one's system field, or a Responses one's
// instructions and its system and developer input messages, which is where a
// client on Responses puts the same prompt (fantasy's Responses model, which
// crush's openai provider type uses, sends a system message as one of the
// input).
func IsClientTitleRequest(body []byte) bool {
	var envelope struct {
		Tools        []json.RawMessage `json:"tools"`
		System       json.RawMessage   `json:"system"`
		Instructions string            `json:"instructions"`
		Input        json.RawMessage   `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	if len(envelope.Tools) > 0 {
		return false
	}
	text := strings.ToLower(systemText(envelope.System) + envelope.Instructions + inputSystemText(envelope.Input))
	if text == "" {
		return false
	}
	for _, marker := range titleMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// inputSystemText is the text of a Responses input's system and developer
// messages, whose content is a string or a list of parts.
func inputSystemText(raw json.RawMessage) string {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var out strings.Builder
	for _, it := range items {
		if it.Role == "system" || it.Role == "developer" {
			out.WriteString(systemText(it.Content))
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// systemText flattens a system field that may be a plain string or an array of
// blocks, which is the difference between what different clients send.
func systemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var out strings.Builder
	for _, b := range blocks {
		out.WriteString(b.Text)
		out.WriteByte('\n')
	}
	return out.String()
}

// titleFromRelayedAnswer reads the assistant's text out of whatever the
// upstream sent, streamed or not.
//
// Its own small reader rather than the provider's scanner (internal/provider/anthropic): that one
// is on the hot path for every relayed byte and is written to allocate nothing,
// and teaching it to accumulate text would spend that everywhere to serve a
// handful of tiny requests.
func titleFromRelayedAnswer(body []byte) string {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return titleFromAnswer(trimmed)
	}

	var text strings.Builder
	for _, line := range bytes.Split(body, []byte("\n")) {
		data, found := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data: "))
		if !found {
			continue
		}
		// Anthropic's text_delta is an object; a Responses stream — ChatGPT's,
		// for a client on that API — sends response.output_text.delta with
		// the text as a plain string.
		var event struct {
			Type  string          `json:"type"`
			Delta json.RawMessage `json:"delta"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		switch event.Type {
		case "content_block_delta":
			var d struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Delta, &d) == nil && d.Type == "text_delta" {
				text.WriteString(d.Text)
			}
		case "response.output_text.delta":
			var d string
			if json.Unmarshal(event.Delta, &d) == nil {
				text.WriteString(d)
			}
		}
	}
	return cleanTitle(text.String())
}

// CaptureSink tees a relayed answer into a buffer on its way to the client.
//
// It never gates or alters the write: the client's bytes go out first and the
// copy is incidental, so a chat name can never be the reason a response was
// slow or truncated.
type CaptureSink struct {
	api.Sink
	Seen bytes.Buffer
}

func (c *CaptureSink) Write(p []byte) (int, error) {
	n, err := c.Sink.Write(p)
	if n > 0 && c.Seen.Len() < maxCapturedTitleAnswer {
		c.Seen.Write(p[:n])
	}
	return n, err
}

// titleMaxTokens is the whole output budget. A title is a handful of words; a
// model that wants more than this has misunderstood the task and its answer is
// not wanted.
const titleMaxTokens = 48

// titlePrompt is appended as a trailing user message.
//
// A trailing message and not a system prompt, because the system prompt is the
// cached prefix and replacing it is what the measurement above shows costs
// everything. It therefore has to argue against a system prompt that says the
// model is a coding agent, which is why it is this emphatic.
const titlePrompt = `<system-reminder>
Ignore the task above. Do not use any tool. Do not answer the conversation.

Reply with ONLY a short title naming what this conversation is about, for
someone scanning a list of conversations later. If it opened with setup, such
as writing AGENTS.md or CLAUDE.md, name the work that came after it instead.
At most 6 words. No quotes, no punctuation at the end, no preamble, no
explanation. Just the title.
</system-reminder>`

// Naming a chat from its opening exchange.
//
// The first title is written from the first request, which holds only what the
// user asked — and crush's own title, which the gateway captures, is the same:
// crush names a session from its first message alone. On production
// (2026-10-13) that was routinely the wrong thing. Every crush chat titled some
// variant of "Generate AGENTS.md" in the week before had opened with crush's
// init prompt — all twelve title requests were 655 input tokens — and those
// were the long chats, 75 to 757 requests over several days.
//
// What the user asked is half of it. The agent's answer says what the work
// turned out to be: for an init, which project the AGENTS.md describes. So a
// chat is renamed once, when the user types their second message — the first
// request whose body holds the agent's whole reply to the first — from the
// opening exchange: the first message and the agent's text in reply, with
// tool calls and their results left out (see openingExchange). After that it
// is not renamed on the gateway's initiative again; an operator can ask.
//
// That rename is a small request of its own rather than the chat's request
// with one message appended. The cached path re-reads the whole conversation,
// which by the second turn of an init is every file the agent read; on
// production (2026-10-13) a rename of a long chat on that path read 161,554
// cached tokens for a 15-token title.

// retitleBackoff spaces out attempts at one chat. A rename that fails — the
// account is cooling, the model answered without a title — is still due on
// the next request, and without this every request would try again.
const retitleBackoff = 10 * time.Minute

// Store is what the titler keeps and reads. The database is one.
type Store interface {
	ListAccounts(ctx context.Context) ([]store.Account, error)
	TitleRecord(ctx context.Context, id string) (store.TitleRecord, bool, error)
	ConversationRequests(ctx context.Context, id string) (int, error)
	SetClientTitle(ctx context.Context, id, title, model string) error
	RetitleChat(ctx context.Context, id, title, model string, basis store.TitleRecord) error
}

// Upstream is one provider as the titler reaches it: its relay, the path a
// title request goes to on it, and whether it speaks Responses (ChatGPT's
// backend) rather than Messages, which decides the body the request is.
type Upstream struct {
	Relay     *relay.Relay
	Path      string
	Responses bool
}

// Deps is what the titler is given.
type Deps struct {
	Store Store
	// Upstreams are the providers by id. A title request goes to the one
	// that served the chat, on the account that served it.
	Upstreams map[string]Upstream
	// Record files the request a title costs, like any other.
	Record func(e store.UsageEvent, budget int64)
	Log    *slog.Logger
}

// Titler mints chat titles.
type Titler struct {
	deps Deps

	mu sync.Mutex
	// inFlight stops two turns of the same new chat each asking for a title.
	// The store's first-writer-wins insert makes a duplicate harmless, but it
	// would still be two billed requests for one name.
	inFlight map[string]bool
	// renamed is when each chat last had a rename attempted, for
	// retitleBackoff. Entries older than that are swept on insert, so it holds
	// only the chats being renamed right now.
	renamed map[string]time.Time
}

// What the opening exchange is cut to. The user's message is the ask; the
// agent's text is cut from the front, because the end of its reply is the
// summary of what it did, which is what a name is made of.
const (
	openingUser  = 2 << 10
	openingAgent = 6 << 10
)

// message is one turn of a conversation, as either dialect sends it.
type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// conversationMessages are a request's turns: a Messages body's messages, or
// a Responses body's input, whose message items carry a role and whose
// function calls and their outputs carry none and are left out.
func conversationMessages(body []byte) ([]message, bool) {
	var envelope struct {
		Messages []message `json:"messages"`
		Input    []message `json:"input"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false
	}
	if len(envelope.Messages) > 0 {
		return envelope.Messages, true
	}
	return envelope.Input, true
}

// isText is a content part that is text: Messages' text, Responses' input
// and output text.
func isText(kind string) bool {
	return kind == "text" || kind == "input_text" || kind == "output_text"
}

// openingExchange is the user's first message and the agent's text in reply,
// up to the user's next message: what the chat set out to do and what the
// agent made of it. Tool calls and their results are left out — they are the
// bulk of an agent's turn and say little a name needs — and so are a client's
// injected reminders. "" when the user has typed nothing yet.
func openingExchange(body []byte) string {
	msgs, ok := conversationMessages(body)
	if !ok {
		return ""
	}
	var user string
	var agent []string
	for _, m := range msgs {
		switch m.Role {
		case "user":
			typed := typedTextOf(m.Content)
			if typed == "" {
				continue
			}
			if user != "" {
				// The user spoke again: the opening exchange is over.
				return openingText(user, agent)
			}
			user = typed
		case "assistant":
			if user == "" {
				continue
			}
			for _, b := range contentBlocks(m.Content) {
				if isText(b.Type) && strings.TrimSpace(b.Text) != "" {
					agent = append(agent, strings.TrimSpace(b.Text))
				}
			}
		}
	}
	if user == "" {
		return ""
	}
	return openingText(user, agent)
}

func openingText(user string, agent []string) string {
	out := "<user>\n" + clip(user, openingUser) + "\n</user>"
	if reply := strings.Join(agent, "\n\n"); reply != "" {
		out += "\n<agent>\n" + clipFront(reply, openingAgent) + "\n</agent>"
	}
	return out
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentBlocks reads a message's content, which a client may send as a plain
// string or as an array of blocks.
func contentBlocks(raw json.RawMessage) []contentBlock {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return []contentBlock{{Type: "text", Text: plain}}
	}
	var blocks []contentBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

// typedTextOf is what the user typed in one user-role message: "" for one that
// carries tool results back, or nothing but injected reminders.
func typedTextOf(raw json.RawMessage) string {
	var text strings.Builder
	for _, b := range contentBlocks(raw) {
		switch b.Type {
		case "tool_result":
			// A client may tuck a note beside the results it sends back; that
			// is still the agent's loop, not the user speaking.
			return ""
		case "text", "input_text", "output_text":
			if t := typedText(b.Text); t != "" {
				text.WriteString(t)
				text.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(text.String())
}

// clip cuts s to n bytes on a character boundary, keeping the start.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// clipFront cuts s to n bytes on a character boundary, keeping the end.
func clipFront(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…" + s[cut:]
}

// openingSystem is the rename request's instruction. A system prompt of its
// own is right here, unlike on the first-title path: there is no cached prefix
// to preserve, because nothing of the chat's own request is sent.
const openingSystem = `You name conversations between a user and a coding agent. You are shown how
one began: the user's first message and the agent's reply. Reply with ONLY a
short title naming what the conversation is about, for someone scanning a list
of conversations later. Name the subject, not the setup step: if the exchange
is writing AGENTS.md or CLAUDE.md, name the project it describes. At most 6
words. No quotes, no punctuation at the end, no preamble. Just the title.`

// openingBody is the rename request, in the dialect of the provider that
// served the chat. On ChatGPT's backend it streams, as every request Codex
// sends there does, and is not stored, which the backend requires of a
// client signed in with a subscription (Codex sends store:false).
func openingBody(responses bool, model, opening string) ([]byte, error) {
	if responses {
		return json.Marshal(map[string]any{
			"model":        model,
			"instructions": openingSystem,
			"input":        []map[string]string{{"role": "user", "content": opening}},
			"stream":       true,
			"store":        false,
		})
	}
	return json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": titleMaxTokens,
		"system":     []map[string]string{{"type": "text", "text": openingSystem}},
		"messages":   []map[string]string{{"role": "user", "content": opening}},
	})
}

// New returns a titler.
func New(d Deps) *Titler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Record == nil {
		d.Record = func(store.UsageEvent, int64) {}
	}
	return &Titler{deps: d, inFlight: map[string]bool{}, renamed: map[string]time.Time{}}
}

// claim reserves a conversation so only one turn asks for its title.
func (t *Titler) claim(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inFlight[id] {
		return false
	}
	t.inFlight[id] = true
	return true
}

func (t *Titler) release(id string) {
	t.mu.Lock()
	delete(t.inFlight, id)
	t.mu.Unlock()
}

// mayRename reports whether a rename of this chat may be attempted now, and
// if so records the attempt.
func (t *Titler) mayRename(id string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.renamed[id]; ok && now.Sub(last) < retitleBackoff {
		return false
	}
	for k, at := range t.renamed {
		if now.Sub(at) >= retitleBackoff {
			delete(t.renamed, k)
		}
	}
	t.renamed[id] = now
	return true
}

// renameDue reports whether a chat is due its one rename from the opening
// exchange: the user has typed a second message, so the agent's reply to the
// first is complete in the body, and the title was written before that. A
// title the operator typed is never due, and nor is one the client gave its
// own chat (Requests 0: the gateway did not write it): that is the name the
// client shows, and renaming it here would give one chat two names. See
// "Naming a chat from its opening exchange".
func renameDue(current store.TitleRecord, turns int) bool {
	return !current.Manual && current.Requests > 0 && current.Turns < 2 && turns >= 2
}

// Consider names a chat if it has no name yet, or renames one that has
// outgrown its name.
//
// Called after a relayed request has already been answered, and it returns
// immediately: the client's turn is finished and must never wait on this. Every
// path out of here that is not a title is silent by design — titling is a
// convenience, and an operator whose log filled with warnings because a model
// declined to name something would rightly turn it off.
func (t *Titler) Consider(ev Request) {
	if _, ok := t.deps.Upstreams[ev.Provider]; !ok || ev.Conversation == "" || ev.AccountID == "" || ev.Model == "" || ev.KeyID == "" {
		return
	}
	if len(ev.Body) == 0 {
		return
	}
	// Never title a title request. For opencode this is the first request of a
	// session, so without this the gateway would spend a call asking what to
	// call a conversation whose only content is a request for a name — and the
	// answer to the real question is already coming back on this very request,
	// which is what CaptureTitle picks up.
	if IsClientTitleRequest(ev.Body) {
		return
	}
	// Nor a chat whose client names it itself: its own title request is on
	// its way through, and CaptureTitle reads the answer for nothing. Asking
	// as well spends a call on a name that would be replaced the moment the
	// client's arrived (SetClientTitle).
	if ev.SelfNaming {
		return
	}
	if !t.claim(ev.Conversation) {
		return
	}
	go func() {
		defer t.release(ev.Conversation)
		// Not the request's context: that one is cancelled the moment the
		// client's turn ends, which is exactly when this starts.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		t.run(ctx, ev)
	}()
}

// CaptureTitle stores a name the client asked a model for, off the answer the
// gateway just relayed.
//
// Nothing is spent here: the request was the client's, the answer was going to
// it anyway, and this only reads the copy. Silent on every failure for the same
// reason Consider is — a name is a convenience, and the model declining to give
// one is not an event worth a log line.
func (t *Titler) CaptureTitle(conversation, model string, answer []byte) {
	if conversation == "" || len(answer) == 0 {
		return
	}
	title := titleFromRelayedAnswer(answer)
	if title == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := t.deps.Store.SetClientTitle(ctx, conversation, title, model); err != nil {
			t.deps.Log.Warn("could not store a captured chat title", "err", err)
			return
		}
		t.deps.Log.Debug("read a chat name from the client's own title request",
			"chat", conversation, "title", title)
	}()
}

// Request is what naming a chat needs, copied out of the request that
// prompted it because that request is over by the time this runs.
type Request struct {
	Conversation string
	// SelfNaming is a chat whose client's type names its chats itself
	// (clients.Type.NamesItsChats).
	SelfNaming bool
	// KeyID and KeyName are the client's: a title is asked for under the key
	// of the client whose chat it names, so it is that client's spend, held
	// to that client's limits. Budget is that key's, for the recorder.
	KeyID, KeyName string
	Budget         int64
	// Allow takes the title request's turn against the client's limits —
	// its rate and its budget — and says whether it may go: false when the
	// client is at a limit, and then nothing is sent, because a title must
	// never be why the client's own next request is refused.
	Allow func() bool
	// Model is the real model the chat's latest turn was served by, on
	// AccountID of Provider.
	Model     string
	AccountID string
	Provider  string
	// Body is the request as it went upstream, which for a translating dialect
	// is not the one that arrived. It has to be that one: the cached prefix is
	// what the upstream saw.
	Body []byte
}

func (t *Titler) run(ctx context.Context, ev Request) {
	log := t.deps.Log
	up := t.deps.Upstreams[ev.Provider]

	current, found, err := t.deps.Store.TitleRecord(ctx, ev.Conversation)
	if err != nil {
		return
	}
	requests, err := t.deps.Store.ConversationRequests(ctx, ev.Conversation)
	if err != nil {
		return
	}
	turns := userTurns(ev.Body)
	basis := store.TitleRecord{Requests: requests, Turns: turns}

	var body []byte
	pinTo := ""
	if found {
		if !renameDue(current, turns) || !t.mayRename(ev.Conversation, time.Now()) {
			return
		}
		opening := openingExchange(ev.Body)
		if opening == "" {
			return
		}
		// A request of its own: nothing of the chat's cached prefix is sent,
		// so no account of the provider is cheaper than another.
		if body, err = openingBody(up.Responses, ev.Model, opening); err != nil {
			return
		}
	} else {
		var ok bool
		if body, ok = titleBody(up.Responses, ev.Body); !ok {
			return
		}
		// Pinned to the account that served the chat. The prompt cache is
		// per-account, so any other account is a guaranteed miss and a title
		// is not worth paying full price for: if this account is cooling, the
		// pool refuses and the chat simply stays unnamed.
		pinTo = ev.AccountID
	}

	title, err := t.ask(ctx, ev, up, pinTo, body, basis)
	if err != nil {
		log.Debug("could not name a chat", "chat", ev.Conversation, "err", err)
		return
	}
	log.Debug("named a chat", "chat", ev.Conversation, "title", title,
		"was", current.Title, "requests", requests, "turns", turns)
}

// errAtLimit and errNoTitle are why ask came back empty-handed.
var (
	errAtLimit = errors.New("the client is at one of its limits")
	errNoTitle = errors.New("the model answered without a title")
)

// ask relays one title request under the client's key, stores the answer and
// files what it cost. pinTo, when set, is the only account it may use.
func (t *Titler) ask(ctx context.Context, ev Request, up Upstream, pinTo string, body []byte,
	basis store.TitleRecord) (string, error) {
	if ev.Allow != nil && !ev.Allow() {
		return "", errAtLimit
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, up.Path, http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if up.Responses {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	// The conversation, for a provider that keeps one on an account
	// (ChatGPT's: internal/pool, affinity.go) — the same account it served.
	ctx = pool.WithConversation(ctx, ev.Conversation)
	req = req.WithContext(ctx)

	relayed := *up.Relay
	if pinTo != "" {
		accounts, err := t.deps.Store.ListAccounts(ctx)
		if err != nil {
			return "", err
		}
		relayed.Pool = pinnedPool{AccountPool: relayed.Pool, only: pinTo, all: accounts}
	}

	var answer bytes.Buffer
	sink := &captureWriter{body: &answer, header: http.Header{}}

	started := time.Now()
	res := relayed.Do(sink, req, up.Path, body, request.Peek(body))
	// Recorded whatever came of it, once an account was used: a call that was
	// refused, or that answered with no usable title, spent as much as one
	// that named the chat — and it is the client's spend, against the
	// client's budget. No account means the pool refused before anything was
	// sent, and nothing was spent.
	if res.AccountID != "" {
		t.deps.Record(titleUsage(started, ev, res), ev.Budget)
	}
	if res.Err != nil {
		return "", res.Err
	}
	if res.Status != http.StatusOK {
		return "", fmt.Errorf("the upstream answered %d", res.Status)
	}

	title := titleFromRelayedAnswer(answer.Bytes())
	if title == "" {
		return "", errNoTitle
	}
	if err := t.deps.Store.RetitleChat(ctx, ev.Conversation, title, ev.Model, basis); err != nil {
		t.deps.Log.Warn("could not store a chat title", "err", err)
		return "", err
	}
	return title, nil
}

// titleUsage is the row a title request files: like any other request's,
// under the client's key and against the chat it names, so the cost of
// naming a conversation is part of what that conversation — and that client —
// cost rather than an unexplained line item.
//
// Error is filled the way a client request's is, so the recorder classifies
// it into ErrorCode the same way: this gateway's own failure first, then a
// stream that died after its 200, then what the upstream said refusing.
func titleUsage(started time.Time, ev Request, res relay.Result) store.UsageEvent {
	e := store.UsageEvent{
		At: started, KeyID: ev.KeyID, KeyName: ev.KeyName,
		AccountID: res.AccountID, AccountEmail: res.AccountEmail, Provider: res.Provider,
		Model: ev.Model, Path: store.InternalPath,
		// No Client: the column is the tool the User-Agent named, and this
		// request was the gateway's, made for the client. The key says whose.
		ConversationID:   ev.Conversation,
		Status:           res.Status,
		InputTokens:      res.Usage.InputTokens,
		OutputTokens:     res.Usage.OutputTokens,
		CacheReadTokens:  res.Usage.CacheReadTokens,
		CacheWriteTokens: res.Usage.CacheCreationTokens,
		ReasoningTokens:  res.Usage.ReasoningTokens,
		Duration:         time.Since(started),
	}
	switch {
	case res.Err != nil:
		e.Error = res.Err.Error()
	case res.StreamError != "":
		e.Error = res.StreamError
	case res.UpstreamError != "":
		e.Error = res.UpstreamError
	case res.Status != http.StatusOK:
		e.Error = fmt.Sprintf("the upstream answered %d", res.Status)
	}
	return e
}

// userTurns counts the messages the user typed, as opposed to the user-role
// messages that carry tool results back.
func userTurns(body []byte) int {
	msgs, ok := conversationMessages(body)
	if !ok {
		return 0
	}
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && typedTextOf(m.Content) != "" {
			n++
		}
	}
	return n
}

// typedText is text minus a block that is wholly a client's <system-reminder>,
// which Claude Code puts at the front of a user message to carry CLAUDE.md and
// the like. Those are not the user talking.
func typedText(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<system-reminder>") && strings.HasSuffix(s, "</system-reminder>") {
		return ""
	}
	return s
}

// titleBody turns the relayed request into the one that asks for a title.
//
// Everything is preserved verbatim — system, tools, messages and the
// cache_control markers on them — because that is the cached prefix and the
// measurement above is what happens when it is not. Only the tail changes.
//
// A Responses body (ChatGPT's backend) is changed the same way: the prompt
// is one more input message after the conversation's, and instructions,
// tools and prompt_cache_key stay, which is what the backend's cache reads.
// It keeps streaming, as everything sent to that backend does, and it is
// given no output cap: its reasoning models spend output on reasoning first,
// and a cap that leaves none for the answer names nothing.
func titleBody(responses bool, body []byte) ([]byte, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false
	}
	field := "messages"
	if responses {
		field = "input"
	}
	raw, ok := envelope[field]
	if !ok {
		return nil, false
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil || len(messages) == 0 {
		return nil, false
	}

	ask, err := json.Marshal(map[string]string{"role": "user", "content": titlePrompt})
	if err != nil {
		return nil, false
	}
	messages = append(messages, ask)
	encoded, err := json.Marshal(messages)
	if err != nil {
		return nil, false
	}
	envelope[field] = encoded
	// A forced tool_choice would make a tool call the only legal reply, and
	// there is no answer in that.
	delete(envelope, "tool_choice")
	if !responses {
		envelope["max_tokens"] = json.RawMessage(strconv.Itoa(titleMaxTokens))
		// Streaming would mean parsing a stream to read one line. Thinking
		// would spend the whole output budget before reaching the answer.
		delete(envelope, "stream")
		delete(envelope, "thinking")
	}
	// Left in place deliberately: tools are part of the cached prefix, and
	// dropping them truncates it before the messages, which is most of what
	// there is to read back. A model that calls one anyway produces no text and
	// is handled as "no title".
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, false
	}
	return out, true
}

// titleFromAnswer reads the title out of a non-streaming answer: Anthropic's
// content blocks, or a Responses answer's output messages.
func titleFromAnswer(body []byte) string {
	type part struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var answer struct {
		Content []part `json:"content"`
		Output  []struct {
			Content []part `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ""
	}
	parts := answer.Content
	for _, o := range answer.Output {
		parts = append(parts, o.Content...)
	}
	var text string
	for _, p := range parts {
		if isText(p.Type) {
			text += p.Text
		}
	}
	return cleanTitle(text)
}

// cleanTitle takes the first line and strips what a model adds despite being
// asked not to: surrounding quotes, a trailing full stop, a "Title:" preamble.
func cleanTitle(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if line, _, found := strings.Cut(text, "\n"); found {
		text = strings.TrimSpace(line)
	}
	if rest, found := strings.CutPrefix(text, "Title:"); found {
		text = strings.TrimSpace(rest)
	}
	text = strings.Trim(text, `"'“”`)
	text = strings.TrimRight(text, ".!,;: ")
	// A model that ignored the instruction and answered the conversation
	// instead produces a sentence, not a title. Better no name than a
	// misleading one.
	if len(text) > 96 {
		return ""
	}
	return strings.TrimSpace(text)
}

// pinnedPool restricts a relay to one account.
//
// Expressed as an exclusion because that is what the pool takes, and it takes
// an exclusion because its job is failover — which is exactly what must not
// happen here. Falling back to another account would produce a title at full
// price, so the refusal is the right answer.
type pinnedPool struct {
	relay.AccountPool
	only string
	all  []store.Account
}

func (p pinnedPool) Acquire(ctx context.Context, provider, model string, exclude map[string]bool) (pool.Lease, error) {
	only := make(map[string]bool, len(p.all))
	for k, v := range exclude {
		only[k] = v
	}
	for _, a := range p.all {
		if a.ID != p.only {
			only[a.ID] = true
		}
	}
	return p.AccountPool.Acquire(ctx, provider, model, only)
}

// captureWriter is an http.ResponseWriter that keeps the answer instead of
// sending it anywhere. The relay writes to a client; this call has none.
type captureWriter struct {
	body   *bytes.Buffer
	header http.Header
	status int
}

func (c *captureWriter) Header() http.Header { return c.header }
func (c *captureWriter) WriteHeader(s int)   { c.status = s }

func (c *captureWriter) Write(p []byte) (int, error) {
	// Bounded: this is an answer of a few words, and a body that is not one is
	// not an answer this wants. Without a cap a misbehaving upstream could
	// stream into memory unopposed.
	if c.body.Len() > 1<<20 {
		return len(p), nil
	}
	return c.body.Write(p)
}
