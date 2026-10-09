package titles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudication/internal/pool"
	"claudication/internal/provider/anthropic"
	"claudication/internal/relay"
	"claudication/internal/store"
)

// fakeStore is the titler's database, in memory. Every method takes the lock
// because the titler calls it from its own goroutine.
type fakeStore struct {
	mu       sync.Mutex
	settings map[string]string
	keys     []store.APIKey
	spent    int64
	spentAt  time.Time
	accounts []store.Account
	titles   map[string]string
	models   map[string]string
	created  int

	settingsErr, setSettingErr, createErr, titleErr, setTitleErr, accountsErr, listKeysErr error

	// titled is signalled after every SetChatTitle, stored or not.
	titled chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: map[string]string{},
		titles:   map[string]string{},
		models:   map[string]string{},
		accounts: []store.Account{{ID: "a1", Email: "one@example.com"}, {ID: "a2", Email: "two@example.com"}},
		titled:   make(chan struct{}, 8),
	}
}

func (s *fakeStore) Settings(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settingsErr != nil {
		return nil, s.settingsErr
	}
	out := map[string]string{}
	for k, v := range s.settings {
		out[k] = v
	}
	return out, nil
}

func (s *fakeStore) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setSettingErr != nil {
		return s.setSettingErr
	}
	s.settings[key] = value
	return nil
}

func (s *fakeStore) CreateKey(_ context.Context, name string, limits store.KeyLimits) (store.APIKey, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return store.APIKey{}, "", s.createErr
	}
	s.created++
	k := store.APIKey{ID: "key-" + string(rune('0'+s.created)), Name: name, KeyLimits: limits}
	s.keys = append(s.keys, k)
	return k, "secret", nil
}

func (s *fakeStore) ListKeys(context.Context) ([]store.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listKeysErr != nil {
		return nil, s.listKeysErr
	}
	return append([]store.APIKey(nil), s.keys...), nil
}

func (s *fakeStore) KeySpend(_ context.Context, _ string, since time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spentAt = since
	return s.spent, nil
}

func (s *fakeStore) ListAccounts(context.Context) ([]store.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountsErr != nil {
		return nil, s.accountsErr
	}
	return append([]store.Account(nil), s.accounts...), nil
}

func (s *fakeStore) ChatTitle(_ context.Context, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.titleErr != nil {
		return "", false, s.titleErr
	}
	title, ok := s.titles[id]
	return title, ok, nil
}

func (s *fakeStore) SetChatTitle(_ context.Context, id, title, model string) error {
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		s.titled <- struct{}{}
	}()
	if s.setTitleErr != nil {
		return s.setTitleErr
	}
	if _, exists := s.titles[id]; !exists {
		s.titles[id] = title
		s.models[id] = model
	}
	return nil
}

func (s *fakeStore) title(id string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.titles[id]
	return t, s.models[id], ok
}

// fakePool hands out the first account not excluded, in order, unless it is
// cooling — which is what lets a test show the titler never falls over to a
// second account.
type fakePool struct {
	mu       sync.Mutex
	accounts []store.Account
	cooling  map[string]bool
	excluded []map[string]bool
}

func (p *fakePool) Acquire(_ context.Context, _, _ string, exclude map[string]bool) (pool.Lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := map[string]bool{}
	for k, v := range exclude {
		cp[k] = v
	}
	p.excluded = append(p.excluded, cp)
	for _, a := range p.accounts {
		if !exclude[a.ID] && !p.cooling[a.ID] {
			return pool.Lease{Account: a, AccessToken: "tok-" + a.ID}, nil
		}
	}
	return pool.Lease{}, pool.ErrAllCoolingUp
}

func (p *fakePool) ReportFailure(string, pool.FailureKind, string) {}
func (p *fakePool) ReportSuccess(string)                           {}
func (p *fakePool) Refresh(context.Context, string) error          { return nil }

// fakeUpstream is the Messages endpoint, recording what the titler sent.
type fakeUpstream struct {
	mu     sync.Mutex
	hits   int
	bodies [][]byte
	auth   []string
	paths  []string
	status int
	answer string
	// arrived, when set, is signalled as each request arrives; release, when
	// set, holds the answer until the test lets it go.
	arrived chan struct{}
	release chan struct{}
}

func (u *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.hits++
	u.bodies = append(u.bodies, body)
	u.auth = append(u.auth, r.Header.Get("Authorization"))
	u.paths = append(u.paths, r.URL.RequestURI())
	status, answer := u.status, u.answer
	u.mu.Unlock()
	if u.arrived != nil {
		u.arrived <- struct{}{}
	}
	if u.release != nil {
		<-u.release
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, answer)
}

func (u *fakeUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

const goodAnswer = `{"content":[{"type":"text","text":"\"Fix the SSE parser.\""}],
	"usage":{"input_tokens":5,"output_tokens":4,"cache_read_input_tokens":11406,"cache_creation_input_tokens":2}}`

// chatBody is a request as a coding agent sends it: tools bound, streaming.
const chatBody = `{"model":"claude-sonnet-5","stream":true,"max_tokens":8192,
	"system":[{"type":"text","text":"You are Claude Code."}],
	"tools":[{"name":"Read","input_schema":{"type":"object"}}],
	"messages":[{"role":"user","content":"fix the parser"}]}`

type harness struct {
	st       *fakeStore
	pool     *fakePool
	up       *fakeUpstream
	titler   *Titler
	recorded chan recorded
}

type recorded struct {
	ev     store.UsageEvent
	budget int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		st:       newFakeStore(),
		up:       &fakeUpstream{answer: goodAnswer},
		recorded: make(chan recorded, 8),
	}
	h.pool = &fakePool{accounts: h.st.accounts, cooling: map[string]bool{}}
	srv := httptest.NewServer(h.up)
	t.Cleanup(srv.Close)
	h.titler = New(Deps{
		Store: h.st,
		Relay: &relay.Relay{Wire: anthropic.Provider{}, Pool: h.pool, Client: srv.Client(), BaseURL: srv.URL},
		Record: func(e store.UsageEvent, budget int64) {
			h.recorded <- recorded{e, budget}
		},
	})
	return h
}

func (h *harness) ev() Request {
	return Request{Conversation: "chat-1", Model: "claude-sonnet-5", AccountID: "a2", Body: []byte(chatBody)}
}

func (h *harness) waitRecorded(t *testing.T) recorded {
	t.Helper()
	select {
	case r := <-h.recorded:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the title request was never recorded")
	}
	return recorded{}
}

// The whole feature, end to end: a chat with no name is named on the account
// that served it, the request is the caller's own with one message appended,
// and what it cost is filed under the gateway's own key against that chat.
func TestConsiderNamesAChatOnItsOwnAccount(t *testing.T) {
	h := newHarness(t)
	if err := h.titler.Set(context.Background(), SettingGenerate, true); err != nil {
		t.Fatal(err)
	}

	h.titler.Consider(h.ev())
	r := h.waitRecorded(t)

	title, model, ok := h.st.title("chat-1")
	if !ok || title != "Fix the SSE parser" || model != "claude-sonnet-5" {
		t.Errorf("stored title %q (model %q, found %v)", title, model, ok)
	}

	// Pinned: the cache is per-account, so the call must go to a2 even though
	// the pool would otherwise hand out a1 first.
	h.up.mu.Lock()
	auth, path, body := h.up.auth[0], h.up.paths[0], h.up.bodies[0]
	h.up.mu.Unlock()
	if auth != "Bearer tok-a2" {
		t.Errorf("sent with %q, want the chat's own account a2", auth)
	}
	if path != "/v1/messages?beta=true" {
		t.Errorf("sent to %s", path)
	}
	var sent struct {
		Stream   *bool             `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("sent body is not JSON: %v", err)
	}
	if sent.Stream != nil || len(sent.Messages) != 2 || len(sent.Tools) != 1 ||
		!strings.Contains(string(sent.Messages[1]), "short title") {
		t.Errorf("sent body is not the caller's plus a title request: %s", body)
	}

	// Attributed to the internal key, so its spend is visible and cappable,
	// and to the chat it names; never stamped with a client name.
	e := r.ev
	if !h.titler.IsOwnKey(e.KeyID) || e.KeyName != internalKeyName {
		t.Errorf("recorded under key %q (%q), want the gateway's own", e.KeyID, e.KeyName)
	}
	if e.AccountID != "a2" || e.AccountEmail != "two@example.com" || e.Path != store.InternalPath ||
		e.ConversationID != "chat-1" || e.Model != "claude-sonnet-5" || e.Client != "" || e.Status != 200 {
		t.Errorf("recorded event: %+v", e)
	}
	if e.InputTokens != 5 || e.OutputTokens != 4 || e.CacheReadTokens != 11406 || e.CacheWriteTokens != 2 {
		t.Errorf("usage not carried: %+v", e)
	}
	if r.budget != 0 {
		t.Errorf("budget = %d", r.budget)
	}
	if h.st.settings[titleKeySetting] != e.KeyID {
		t.Errorf("the key id was not remembered: %q", h.st.settings[titleKeySetting])
	}
}

// Two turns of the same new chat arrive close together. A second request for
// the same name would be harmless to the store and still billed twice.
func TestConsiderAsksOncePerChatInFlight(t *testing.T) {
	h := newHarness(t)
	h.up.arrived = make(chan struct{}, 2)
	h.up.release = make(chan struct{})
	_ = h.titler.Set(context.Background(), SettingGenerate, true)

	h.titler.Consider(h.ev())
	<-h.up.arrived
	h.titler.Consider(h.ev())
	close(h.up.release)
	h.waitRecorded(t)

	if n := h.up.count(); n != 1 {
		t.Errorf("upstream asked %d times for one chat", n)
	}
}

// Everything Consider refuses up front must spend nothing and start nothing:
// it runs on the request path and the client's turn is already over.
func TestConsiderDeclinesWithoutStartingAnything(t *testing.T) {
	titleRequest := `{"system":"You are a title generator. You output ONLY a thread title.",
		"messages":[{"role":"user","content":"Generate a title for this conversation"}]}`
	for name, c := range map[string]struct {
		on bool
		ev Request
	}{
		"switched off":      {false, Request{Conversation: "c", Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
		"no conversation":   {true, Request{Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
		"no account":        {true, Request{Conversation: "c", Model: "m", Body: []byte(chatBody)}},
		"no model":          {true, Request{Conversation: "c", AccountID: "a2", Body: []byte(chatBody)}},
		"no body":           {true, Request{Conversation: "c", Model: "m", AccountID: "a2"}},
		"a title request":   {true, Request{Conversation: "c", Model: "m", AccountID: "a2", Body: []byte(titleRequest)}},
		"already in flight": {true, Request{Conversation: "busy", Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
	} {
		h := newHarness(t)
		_ = h.titler.Set(context.Background(), SettingGenerate, c.on)
		h.titler.claim("busy")
		h.titler.Consider(c.ev)
		// Nothing claimed means no goroutine was started for it.
		if c.ev.Conversation != "" && c.ev.Conversation != "busy" && !h.titler.claim(c.ev.Conversation) {
			t.Errorf("%s: a title request was started", name)
		}
		if n := h.up.count(); n != 0 {
			t.Errorf("%s: upstream asked %d times", name, n)
		}
	}
}

// run's own refusals: each must end in "no title" without reaching the
// upstream, because every one of them is a request that would be wasted.
func TestRunSkipsWhatItShouldNotSpendOn(t *testing.T) {
	boom := errors.New("boom")
	for name, setup := range map[string]func(h *harness, ev *Request){
		"already named": func(h *harness, _ *Request) { h.st.titles["chat-1"] = "Existing" },
		"title lookup failed": func(h *harness, _ *Request) {
			h.st.titleErr = boom
		},
		"key could not be issued": func(h *harness, _ *Request) { h.st.createErr = boom },
		"key id could not be saved": func(h *harness, _ *Request) {
			h.st.setSettingErr = boom
		},
		"accounts unreadable": func(h *harness, _ *Request) { h.st.accountsErr = boom },
		"body without messages": func(_ *harness, ev *Request) {
			ev.Body = []byte(`{"model":"m"}`)
		},
		// The operator's cap on what titling may cost, using the control
		// every other key has.
		"daily budget spent": func(h *harness, _ *Request) {
			h.st.keys = []store.APIKey{{ID: "own", Name: internalKeyName, KeyLimits: store.KeyLimits{TokenBudget: 1000}}}
			h.st.settings[titleKeySetting] = "own"
			h.st.spent = 1000
		},
	} {
		h := newHarness(t)
		ev := h.ev()
		setup(h, &ev)
		if err := h.titler.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.titler.run(context.Background(), ev)
		if n := h.up.count(); n != 0 {
			t.Errorf("%s: upstream asked %d times", name, n)
		}
		if len(h.recorded) != 0 {
			t.Errorf("%s: a request was recorded", name)
		}
		if name != "already named" {
			if _, _, ok := h.st.title("chat-1"); ok {
				t.Errorf("%s: a title was stored", name)
			}
		}
	}
}

// Under budget the title goes ahead, and the spend is measured over a
// rolling day — the same window every other key's budget uses.
func TestRunProceedsUnderBudget(t *testing.T) {
	h := newHarness(t)
	h.st.keys = []store.APIKey{{ID: "own", Name: internalKeyName, KeyLimits: store.KeyLimits{TokenBudget: 1000}}}
	h.st.settings[titleKeySetting] = "own"
	h.st.spent = 999
	_ = h.titler.Load(context.Background())

	before := time.Now()
	h.titler.run(context.Background(), h.ev())
	r := h.waitRecorded(t)
	if r.ev.KeyID != "own" || h.st.created != 0 {
		t.Errorf("recorded under %q after creating %d keys, want the existing one", r.ev.KeyID, h.st.created)
	}
	if d := before.Sub(h.st.spentAt); d < 24*time.Hour-time.Minute || d > 24*time.Hour+time.Minute {
		t.Errorf("spend measured over %s, want a day", d)
	}
}

// A refusal or an answer that is not a title must leave the chat unnamed and
// file nothing: no name was got, and no retry is made.
func TestRunStoresNothingWithoutATitle(t *testing.T) {
	for name, setup := range map[string]func(h *harness){
		"upstream refused": func(h *harness) {
			h.up.status = http.StatusBadRequest
			h.up.answer = `{"type":"error","error":{"type":"invalid_request_error"}}`
		},
		"model answered the conversation": func(h *harness) {
			h.up.answer = `{"content":[{"type":"text","text":"` + strings.Repeat("I will read the parser. ", 10) + `"}]}`
		},
		// Falling back to another account would buy a title at full price,
		// since the cache lives on the account that is cooling.
		"chat's account is cooling": func(h *harness) { h.pool.cooling["a2"] = true },
	} {
		h := newHarness(t)
		setup(h)
		h.titler.run(context.Background(), h.ev())
		if _, _, ok := h.st.title("chat-1"); ok {
			t.Errorf("%s: a title was stored", name)
		}
		if len(h.recorded) != 0 {
			t.Errorf("%s: a request was recorded", name)
		}
	}

	h := newHarness(t)
	h.pool.cooling["a2"] = true
	h.titler.run(context.Background(), h.ev())
	if h.up.count() != 0 {
		t.Error("a cooling account's title went to another account")
	}
	for _, ex := range h.pool.excluded {
		if !ex["a1"] {
			t.Errorf("the pool was offered a1: exclude = %v", ex)
		}
	}
}

// A title that could not be stored is not recorded either: nothing would show
// what it bought.
func TestRunDoesNotRecordAnUnstoredTitle(t *testing.T) {
	h := newHarness(t)
	h.st.setTitleErr = errors.New("disk full")
	h.titler.run(context.Background(), h.ev())
	if h.up.count() != 1 {
		t.Fatalf("upstream asked %d times", h.up.count())
	}
	if len(h.recorded) != 0 {
		t.Error("an unstored title was recorded")
	}
}

// The internal key is issued once and reused. A new key per title — or per
// restart — would scatter the gateway's spend across rows and orphan it.
func TestTheInternalKeyIsIssuedOnceAndFoundAgain(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	tt := New(Deps{Store: st})

	id, name, err := tt.key(ctx)
	if err != nil || name != internalKeyName {
		t.Fatalf("first key: %q %q %v", id, name, err)
	}
	if again, _, _ := tt.key(ctx); again != id || st.created != 1 {
		t.Errorf("second call gave %q after %d creations, want %q reused", again, st.created, id)
	}

	// After a restart only the id is known; the name is read back rather
	// than a second key being issued.
	restarted := New(Deps{Store: st})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !restarted.IsOwnKey(id) || restarted.IsOwnKey("someone-else") || restarted.IsOwnKey("") {
		t.Error("IsOwnKey does not recognise exactly the stored key")
	}
	got, gotName, err := restarted.key(ctx)
	if err != nil || got != id || gotName != internalKeyName || st.created != 1 {
		t.Errorf("after restart: %q %q %v, %d keys created", got, gotName, err, st.created)
	}

	// The stored key was deleted, or lost with a restored database. Usage
	// must not be recorded against a key that does not exist.
	st.keys = nil
	gone := New(Deps{Store: st})
	_ = gone.Load(ctx)
	fresh, _, err := gone.key(ctx)
	if err != nil || fresh == id || st.created != 2 || st.settings[titleKeySetting] != fresh {
		t.Errorf("replacement key %q (err %v, %d created, stored %q)", fresh, err, st.created, st.settings[titleKeySetting])
	}

	// An unreadable keys list is treated as the key not being found.
	st.listKeysErr = errors.New("locked")
	unreadable := New(Deps{Store: st})
	_ = unreadable.Load(ctx)
	if _, _, err := unreadable.key(ctx); err != nil || st.created != 3 {
		t.Errorf("with an unreadable list: err %v, %d created", err, st.created)
	}
}

// Both switches start off — titling spends the operator's subscription — and
// each persists before it takes effect, so a restart cannot silently undo it.
func TestSwitches(t *testing.T) {
	ctx := context.Background()
	st := newFakeStore()
	tt := New(Deps{Store: st})
	if tt.On() || tt.Capturing() {
		t.Fatal("a new titler starts switched on")
	}

	if err := tt.Set(ctx, SettingGenerate, true); err != nil || !tt.On() || st.settings[SettingGenerate] != "true" {
		t.Errorf("generate: on=%v stored=%q err=%v", tt.On(), st.settings[SettingGenerate], err)
	}
	if err := tt.Set(ctx, SettingCapture, true); err != nil || !tt.Capturing() || st.settings[SettingCapture] != "true" {
		t.Errorf("capture: on=%v stored=%q err=%v", tt.Capturing(), st.settings[SettingCapture], err)
	}
	if err := tt.Set(ctx, "chat.titles.other", true); !errors.Is(err, errUnknownSwitch) {
		t.Errorf("unknown switch: %v", err)
	}

	st.setSettingErr = errors.New("read-only")
	if err := tt.Set(ctx, SettingGenerate, false); err == nil || !tt.On() {
		t.Error("a switch changed without being stored")
	}

	// What was stored is what a restart reads.
	st.setSettingErr = nil
	restarted := New(Deps{Store: st})
	if err := restarted.Load(ctx); err != nil || !restarted.On() || !restarted.Capturing() {
		t.Errorf("after restart: on=%v capturing=%v err=%v", restarted.On(), restarted.Capturing(), err)
	}

	// A failed read is reported and leaves the safe state.
	st.settingsErr = errors.New("no database")
	broken := New(Deps{Store: st})
	if err := broken.Load(ctx); err == nil || broken.On() || broken.Capturing() {
		t.Errorf("failed load: err=%v on=%v", err, broken.On())
	}
}

// Recognising a client's own title request is what lets the gateway read the
// name instead of paying for one. It must not match an agent turn, or an
// agent's answer would be stored as a chat name.
func TestIsClientTitleRequest(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       bool
	}{
		{"opencode", `{"system":"You are a title generator. You output ONLY a thread title.","messages":[]}`, true},
		{"crush, as blocks", `{"system":[{"type":"text","text":"You will generate a short title based on the first message"}]}`, true},
		{"agent turn with the same words", `{"system":"You are a title generator.","tools":[{"name":"Read"}]}`, false},
		{"ordinary tool-less turn", `{"system":"You are a helpful assistant."}`, false},
		{"no system", `{"messages":[{"role":"user","content":"Generate a short title"}]}`, false},
		{"system of an unknown shape", `{"system":42}`, false},
		{"not json", `garbage`, false},
	} {
		if got := IsClientTitleRequest([]byte(c.body)); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The name a client asked for comes back streamed or not, and has to be read
// out of either; deltas that are not text (thinking, tool input) are not part
// of it.
func TestTitleFromRelayedAnswer(t *testing.T) {
	stream := "event: message_start\r\ndata: {\"type\":\"message_start\"}\r\n\r\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\\\"Fix the \"}}\n" +
		"data: not json\n" +
		": keep-alive\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"parser.\\\"\"}}\n" +
		"data: {\"type\":\"message_stop\"}\n"
	if got := titleFromRelayedAnswer([]byte(stream)); got != "Fix the parser" {
		t.Errorf("streamed: %q", got)
	}
	if got := titleFromRelayedAnswer([]byte("\n  {\"content\":[{\"type\":\"text\",\"text\":\"Plain answer\"}]}")); got != "Plain answer" {
		t.Errorf("plain: %q", got)
	}
	if got := titleFromRelayedAnswer([]byte("event: ping\ndata: {}\n")); got != "" {
		t.Errorf("no text: %q", got)
	}
}

// Capture stores what the client was shown, verbatim; a failure is silent and
// stores nothing.
func TestCaptureTitle(t *testing.T) {
	st := newFakeStore()
	tt := New(Deps{Store: st})

	tt.CaptureTitle("", "m", []byte(`{"content":[{"type":"text","text":"x"}]}`))
	tt.CaptureTitle("c", "m", nil)
	tt.CaptureTitle("c", "m", []byte(`{"content":[]}`))
	select {
	case <-st.titled:
		t.Fatal("a title was stored from nothing")
	default:
	}

	tt.CaptureTitle("chat-9", "claude-haiku-4-5", []byte(`{"content":[{"type":"text","text":"Say ok"}]}`))
	select {
	case <-st.titled:
	case <-time.After(5 * time.Second):
		t.Fatal("captured title never stored")
	}
	if title, model, ok := st.title("chat-9"); !ok || title != "Say ok" || model != "claude-haiku-4-5" {
		t.Errorf("stored %q / %q / %v", title, model, ok)
	}

	st.setTitleErr = errors.New("disk full")
	tt.CaptureTitle("chat-10", "m", []byte(`{"content":[{"type":"text","text":"Lost"}]}`))
	select {
	case <-st.titled:
	case <-time.After(5 * time.Second):
		t.Fatal("store never asked")
	}
	if _, _, ok := st.title("chat-10"); ok {
		t.Error("a failed store left a title")
	}
}

// fakeSink is the client's side of a relayed answer.
type fakeSink struct{ *httptest.ResponseRecorder }

func (s fakeSink) Unwrap() http.ResponseWriter { return s.ResponseRecorder }
func (s fakeSink) Close(error)                 {}

// The client's bytes go out whole whatever the copy does: a chat name must
// never be why an answer was truncated. The copy itself stops growing once it
// is past anything a title could be.
func TestCaptureSinkNeverGatesTheClient(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := &CaptureSink{Sink: fakeSink{rec}}

	chunk := bytes.Repeat([]byte("a"), 40<<10)
	for i := 0; i < 4; i++ {
		if n, err := sink.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("write %d: %d, %v", i, n, err)
		}
	}
	if rec.Body.Len() != 4*len(chunk) {
		t.Errorf("client got %d bytes, want %d", rec.Body.Len(), 4*len(chunk))
	}
	if got := sink.Seen.Len(); got != 2*len(chunk) {
		t.Errorf("copy holds %d bytes, want it to stop once past the cap", got)
	}
}

// The titler's own answer is bounded too: an upstream that streams far more
// than a few words must not grow memory unopposed, but the relay is still
// told every byte was taken so it does not fail the request.
func TestCaptureWriterIsBounded(t *testing.T) {
	var buf bytes.Buffer
	w := &captureWriter{body: &buf, header: http.Header{}}
	w.Header().Set("X", "y")
	w.WriteHeader(http.StatusAccepted)
	if w.status != http.StatusAccepted || w.header.Get("X") != "y" {
		t.Error("status or header not kept")
	}
	big := bytes.Repeat([]byte("b"), 1<<20)
	_, _ = w.Write(big)
	_, _ = w.Write([]byte("c"))
	if n, err := w.Write([]byte("more")); n != 4 || err != nil {
		t.Errorf("write past the cap: %d, %v", n, err)
	}
	if buf.Len() != 1<<20+1 {
		t.Errorf("held %d bytes", buf.Len())
	}
}
