// Package titles names chats: reading the name a client asked a model for as
// it goes past, and — when switched on — asking for one where the client never
// does. See the long comment below for what was measured before either was
// built, and why the request it makes is the caller's own with one message
// appended.
package titles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"claudication/internal/api"
	"claudication/internal/pool"
	"claudication/internal/relay"
	"claudication/internal/request"
	"claudication/internal/service/settings"
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
// why it is off until switched on, why it is attributed to a key of its own,
// and why every failure below ends in "no title" rather than a retry.
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
func IsClientTitleRequest(body []byte) bool {
	var envelope struct {
		Tools  []json.RawMessage `json:"tools"`
		System json.RawMessage   `json:"system"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	if len(envelope.Tools) > 0 {
		return false
	}
	text := strings.ToLower(systemText(envelope.System))
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
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			text.WriteString(event.Delta.Text)
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

// SettingGenerate is the settings row holding the switch.
const SettingGenerate = "chat.titles.enabled"

// SettingCapture is the switch for reading names clients generate themselves.
// Separate from generation because they are different bargains: this one costs
// nothing and only notices an answer already going past, while generation
// spends the operator's subscription.
const SettingCapture = "chat.titles.capture"

// titleKeySetting remembers which API key the gateway issued itself.
const titleKeySetting = "chat.titles.key"

// internalKeyName is what that key is called in the keys list.
//
// It is a real key rather than a bypass so that what the gateway spends on its
// own behalf is visible where every other kind of spend is visible, and can be
// capped with the same token budget. An operator who thinks titling is not
// worth it can give it a budget of a few thousand tokens a day and watch it
// stop, without the gateway needing a second mechanism for that.
//
// The admin UI offers no Delete for it, because deleting it was once presented
// as the way to turn titling off and it never was: the next request needing a
// name issues a fresh key under a new id, and the usage attributed to the old
// one is orphaned. The controls that work are the switch and the budget.
const internalKeyName = "gateway (internal)"

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
someone scanning a list of conversations later. At most 6 words. No quotes, no
punctuation at the end, no preamble, no explanation. Just the title.
</system-reminder>`

// Store is what the titler keeps and reads. The database is one.
type Store interface {
	settings.Store
	CreateKey(ctx context.Context, name string, limits store.KeyLimits) (store.APIKey, string, error)
	ListKeys(ctx context.Context) ([]store.APIKey, error)
	KeySpend(ctx context.Context, keyID string, since time.Time) (int64, error)
	ListAccounts(ctx context.Context) ([]store.Account, error)
	ChatTitle(ctx context.Context, id string) (string, bool, error)
	SetChatTitle(ctx context.Context, id, title, model string) error
}

// Deps is what the titler is given.
type Deps struct {
	Store Store
	// Relay is copied and pinned to one account for each title request.
	Relay *relay.Relay
	// Record files the request a title costs, like any other.
	Record func(e store.UsageEvent, budget int64)
	Log    *slog.Logger
}

// Titler mints chat titles.
type Titler struct {
	deps Deps

	generate *settings.Switch
	capture  *settings.Switch

	mu      sync.Mutex
	keyID   string
	keyName string
	// inFlight stops two turns of the same new chat each asking for a title.
	// The store's first-writer-wins insert makes a duplicate harmless, but it
	// would still be two billed requests for one name.
	inFlight map[string]bool
}

// New returns a titler with both switches off until Load reads them.
func New(d Deps) *Titler {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Titler{
		deps:     d,
		generate: settings.NewSwitch(d.Store, SettingGenerate, false),
		capture:  settings.NewSwitch(d.Store, SettingCapture, false),
		inFlight: map[string]bool{},
	}
}

// Load reads the switches and the gateway's own key at startup. A failure is
// worth reporting and not worth refusing to start over: off is a safe state.
func (t *Titler) Load(ctx context.Context) error {
	stored, err := t.deps.Store.Settings(ctx)
	if err != nil {
		return err
	}
	t.generate.Apply(stored)
	t.capture.Apply(stored)
	t.mu.Lock()
	t.keyID = stored[titleKeySetting]
	t.mu.Unlock()
	return nil
}

// On reports whether the gateway asks for titles itself.
func (t *Titler) On() bool { return t.generate.On() }

// Capturing reports whether names clients ask for are read as they go past.
func (t *Titler) Capturing() bool { return t.capture.On() }

// IsOwnKey reports whether an id is the key the gateway issued itself, so the
// keys screen can leave the Delete off that row.
func (t *Titler) IsOwnKey(id string) bool {
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.keyID == id
}

// errUnknownSwitch is Set's answer for a key that is neither switch.
var errUnknownSwitch = errors.New("no such title switch")

// Set changes one of the two switches, by its settings key.
func (t *Titler) Set(ctx context.Context, key string, on bool) error {
	switch key {
	case SettingGenerate:
		return t.generate.Set(ctx, on)
	case SettingCapture:
		return t.capture.Set(ctx, on)
	}
	return errUnknownSwitch
}

// key returns the gateway's own API key, creating it the first time.
//
// The secret is thrown away deliberately: nothing needs to authenticate with
// it, because internal calls do not go in through the front door. What the key
// is for is identity and a budget — a row in the keys list that usage can be
// attributed to and a ceiling an operator can set.
func (t *Titler) key(ctx context.Context) (id, name string, err error) {
	t.mu.Lock()
	id, name = t.keyID, t.keyName
	t.mu.Unlock()
	if id != "" && name != "" {
		return id, name, nil
	}

	if id != "" {
		// Known id, name not yet read back: look it up rather than create a
		// second key on every restart.
		if k, ok := t.lookupKey(ctx, id); ok {
			t.mu.Lock()
			t.keyName = k.Name
			t.mu.Unlock()
			return id, k.Name, nil
		}
		// The id is stored but the key is gone — deleted through the API, or
		// lost with a restored database. Issue a fresh one rather than
		// recording usage against a key that does not exist.
	}

	key, _, err := t.deps.Store.CreateKey(ctx, internalKeyName, store.KeyLimits{})
	if err != nil {
		return "", "", err
	}
	if err := t.deps.Store.SetSetting(ctx, titleKeySetting, key.ID); err != nil {
		return "", "", err
	}
	t.mu.Lock()
	t.keyID, t.keyName = key.ID, key.Name
	t.mu.Unlock()
	return key.ID, key.Name, nil
}

// lookupKey finds one key by id. The list is short and this runs once per
// title, off the request path, so walking it costs nothing worth a new query.
func (t *Titler) lookupKey(ctx context.Context, id string) (store.APIKey, bool) {
	keys, err := t.deps.Store.ListKeys(ctx)
	if err != nil {
		return store.APIKey{}, false
	}
	for _, k := range keys {
		if k.ID == id {
			return k, true
		}
	}
	return store.APIKey{}, false
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

// Consider names a chat if it has no name yet.
//
// Called after a relayed request has already been answered, and it returns
// immediately: the client's turn is finished and must never wait on this. Every
// path out of here that is not a title is silent by design — titling is a
// convenience, and an operator whose log filled with warnings because a model
// declined to name something would rightly turn it off.
func (t *Titler) Consider(ev Request) {
	if !t.On() || ev.Conversation == "" || ev.AccountID == "" || ev.Model == "" {
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
		if err := t.deps.Store.SetChatTitle(ctx, conversation, title, model); err != nil {
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
	Model        string
	AccountID    string
	// Body is the request as it went upstream, which for a translating dialect
	// is not the one that arrived. It has to be that one: the cached prefix is
	// what the upstream saw.
	Body []byte
}

func (t *Titler) run(ctx context.Context, ev Request) {
	log := t.deps.Log

	if _, found, err := t.deps.Store.ChatTitle(ctx, ev.Conversation); err != nil || found {
		return
	}

	keyID, keyName, err := t.key(ctx)
	if err != nil {
		log.Warn("could not issue the gateway's own key", "err", err)
		return
	}
	// The same ceiling any other key gets, checked the same way. A budget set
	// on the internal key is how an operator caps what titling may cost, using
	// the control they already know rather than a second mechanism.
	if k, ok := t.lookupKey(ctx, keyID); ok && k.TokenBudget > 0 {
		spent, err := t.deps.Store.KeySpend(ctx, keyID, time.Now().Add(-24*time.Hour))
		if err == nil && spent >= k.TokenBudget {
			return
		}
	}

	body, ok := titleBody(ev.Body)
	if !ok {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", http.NoBody)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")

	// A relay of its own, pinned to the account that served the chat. The
	// prompt cache is per-account, so any other account is a guaranteed miss
	// and a title is not worth paying full price for: if this account is
	// cooling, the pool refuses and the chat simply stays unnamed.
	pinned := *t.deps.Relay
	accounts, err := t.deps.Store.ListAccounts(ctx)
	if err != nil {
		return
	}
	pinned.Pool = pinnedPool{AccountPool: pinned.Pool, only: ev.AccountID, all: accounts}

	var answer bytes.Buffer
	sink := &captureWriter{body: &answer, header: http.Header{}}

	started := time.Now()
	res := pinned.Do(sink, req, "anthropic", "/v1/messages?beta=true", body, request.Peek(body))
	if res.Err != nil || res.Status != http.StatusOK {
		return
	}

	title := titleFromAnswer(answer.Bytes())
	if title == "" {
		return
	}
	if err := t.deps.Store.SetChatTitle(ctx, ev.Conversation, title, ev.Model); err != nil {
		log.Warn("could not store a chat title", "err", err)
		return
	}

	// Recorded like any other request, under the gateway's own key and against
	// the chat it names, so the cost of naming a conversation is part of what
	// that conversation cost rather than an unexplained line item.
	t.deps.Record(store.UsageEvent{
		At: started, KeyID: keyID, KeyName: keyName,
		AccountID: res.AccountID, AccountEmail: res.AccountEmail,
		Model: ev.Model, Path: store.InternalPath,
		// No Client: this row is the gateway's own, and the column means "who
		// was having this conversation". Stamping it here put the gateway's
		// name on a crush chat, because the rollup picked a client with
		// MAX(client) and a lowercase g sorts above Charm-Crush's C. The key
		// name already says whose request it was.
		ConversationID:   ev.Conversation,
		Status:           res.Status,
		InputTokens:      res.Usage.InputTokens,
		OutputTokens:     res.Usage.OutputTokens,
		CacheReadTokens:  res.Usage.CacheReadTokens,
		CacheWriteTokens: res.Usage.CacheCreationTokens,
		Duration:         time.Since(started),
	}, 0)

	log.Debug("named a chat", "chat", ev.Conversation, "title", title,
		"cache_read", res.Usage.CacheReadTokens, "input", res.Usage.InputTokens)
}

// titleBody turns the relayed request into the one that asks for a title.
//
// Everything is preserved verbatim — system, tools, messages and the
// cache_control markers on them — because that is the cached prefix and the
// measurement above is what happens when it is not. Only the tail changes.
func titleBody(body []byte) ([]byte, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false
	}
	raw, ok := envelope["messages"]
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
	envelope["messages"] = encoded
	envelope["max_tokens"] = json.RawMessage(strconv.Itoa(titleMaxTokens))

	// Streaming would mean parsing a stream to read one line. Thinking would
	// spend the whole output budget before reaching the answer. A forced
	// tool_choice would make a tool call the only legal reply, and there is no
	// answer in that.
	delete(envelope, "stream")
	delete(envelope, "thinking")
	delete(envelope, "tool_choice")
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

// titleFromAnswer reads the title out of a non-streaming Anthropic answer.
func titleFromAnswer(body []byte) string {
	var answer struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return ""
	}
	var text string
	for _, block := range answer.Content {
		if block.Type == "text" {
			text += block.Text
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
