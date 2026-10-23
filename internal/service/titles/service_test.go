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
	"claudication/internal/provider"
	"claudication/internal/provider/anthropic"
	chatgpt "claudication/internal/provider/openai"
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
	bases    map[string]store.TitleRecord
	requests map[string]int
	created  int

	settingsErr, setSettingErr, createErr, titleErr, setTitleErr, accountsErr, keyErr error

	// titled is signalled after every SetChatTitle or RetitleChat, stored or not.
	titled chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: map[string]string{},
		titles:   map[string]string{},
		models:   map[string]string{},
		bases:    map[string]store.TitleRecord{},
		requests: map[string]int{},
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

func (s *fakeStore) CreateUnkeptKey(_ context.Context, name string, limits store.KeyLimits) (store.APIKey, string, error) {
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

func (s *fakeStore) Key(_ context.Context, id string) (store.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keyErr != nil {
		return store.APIKey{}, s.keyErr
	}
	for _, k := range s.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return store.APIKey{}, store.ErrKeyNotFound
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

func (s *fakeStore) TitleRecord(_ context.Context, id string) (store.TitleRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.titleErr != nil {
		return store.TitleRecord{}, false, s.titleErr
	}
	title, ok := s.titles[id]
	if !ok {
		return store.TitleRecord{}, false, nil
	}
	r := s.bases[id]
	r.Title = title
	return r, true, nil
}

func (s *fakeStore) ConversationRequests(_ context.Context, id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[id], nil
}

func (s *fakeStore) RetitleChat(_ context.Context, id, title, model string, basis store.TitleRecord) error {
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		s.titled <- struct{}{}
	}()
	if s.setTitleErr != nil {
		return s.setTitleErr
	}
	s.titles[id] = title
	s.models[id] = model
	s.bases[id] = basis
	return nil
}

// SetClientTitle is the store's: it replaces a title the gateway wrote, and
// nothing else.
func (s *fakeStore) SetClientTitle(_ context.Context, id, title, model string) error {
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		s.titled <- struct{}{}
	}()
	if s.setTitleErr != nil {
		return s.setTitleErr
	}
	if _, exists := s.titles[id]; !exists || s.bases[id].Requests > 0 {
		s.titles[id] = title
		s.models[id] = model
		s.bases[id] = store.TitleRecord{}
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

func (p *fakePool) ReportFailure(string, pool.FailureKind, string)                              {}
func (p *fakePool) ReportFailureUntil(string, pool.FailureKind, string, time.Time)              {}
func (p *fakePool) ReportModelFailureUntil(string, string, pool.FailureKind, string, time.Time) {}
func (p *fakePool) NoteQuota(string, provider.Quota)                                            {}
func (p *fakePool) ReportSuccess(string)                                                        {}
func (p *fakePool) Refresh(context.Context, string) error                                       { return nil }

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
	if strings.HasPrefix(answer, "event:") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
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
		Upstreams: map[string]Upstream{
			"anthropic": {Relay: &relay.Relay{Wire: anthropic.Provider{}, Pool: h.pool, Client: srv.Client(),
				BaseURL: srv.URL}, Path: "/v1/messages?beta=true"},
			"openai": {Relay: &relay.Relay{Wire: chatgpt.Provider{}, Pool: h.pool, Client: srv.Client(),
				BaseURL: srv.URL}, Path: "/responses", Responses: true},
		},
		Record: func(e store.UsageEvent, budget int64) {
			h.recorded <- recorded{e, budget}
		},
	})
	return h
}

func (h *harness) ev() Request {
	return Request{Conversation: "chat-1", Model: "claude-sonnet-5", AccountID: "a2", Provider: "anthropic",
		KeyID: "k1", KeyName: "laptop", Budget: 5000, Body: []byte(chatBody)}
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

	h.titler.Consider(h.ev())
	r := h.waitRecorded(t)
	// The call is recorded as soon as it returns, before its answer is read
	// and stored, so the title has to be waited for on its own.
	select {
	case <-h.st.titled:
	case <-time.After(5 * time.Second):
		t.Fatal("the title was never stored")
	}

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

	// Attributed to the client's key, so it is that client's spend, against
	// its budget, and to the chat it names; never stamped with a tool name.
	e := r.ev
	if e.KeyID != "k1" || e.KeyName != "laptop" {
		t.Errorf("recorded under key %q (%q), want the client's", e.KeyID, e.KeyName)
	}
	if e.AccountID != "a2" || e.AccountEmail != "two@example.com" || e.Path != store.InternalPath ||
		e.ConversationID != "chat-1" || e.Model != "claude-sonnet-5" || e.Client != "" || e.Status != 200 {
		t.Errorf("recorded event: %+v", e)
	}
	if e.InputTokens != 5 || e.OutputTokens != 4 || e.CacheReadTokens != 11406 || e.CacheWriteTokens != 2 {
		t.Errorf("usage not carried: %+v", e)
	}
	if r.budget != 5000 {
		t.Errorf("budget = %d, want the client's", r.budget)
	}
}

// A chat a ChatGPT account served is named there, on that account, in that
// backend's dialect: the conversation's own input with the prompt appended,
// streamed, and the title read off the stream.
func TestConsiderNamesAChatGPTChat(t *testing.T) {
	h := newHarness(t)
	h.up.answer = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Fix the build\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"
	ev := h.ev()
	ev.Provider, ev.Model = "openai", "gpt-6-luna"
	ev.Body = []byte(`{"model":"gpt-6-luna","stream":true,"store":false,"instructions":"be terse",` +
		`"tools":[{"type":"function","name":"bash"}],` +
		`"input":[{"role":"user","content":[{"type":"input_text","text":"fix the build"}]}]}`)
	h.titler.Consider(ev)
	r := h.waitRecorded(t)
	select {
	case <-h.st.titled:
	case <-time.After(5 * time.Second):
		t.Fatal("the title was never stored")
	}
	if title, model, _ := h.st.title("chat-1"); title != "Fix the build" || model != "gpt-6-luna" {
		t.Errorf("stored %q (%q)", title, model)
	}
	h.up.mu.Lock()
	path, body := h.up.paths[0], h.up.bodies[0]
	h.up.mu.Unlock()
	var sent struct {
		Stream bool              `json:"stream"`
		Input  []json.RawMessage `json:"input"`
		Tools  []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || path != "/responses" || !sent.Stream ||
		len(sent.Input) != 2 || len(sent.Tools) != 1 || !strings.Contains(string(sent.Input[1]), "short title") {
		t.Errorf("sent to %s: %s", path, body)
	}
	if r.ev.KeyID != "k1" || r.ev.Provider != "openai" || r.ev.AccountID != "a2" {
		t.Errorf("recorded %+v", r.ev)
	}
}

// The client's limits decide: Allow is asked once, and a client with room
// is named, its turn taken.
func TestRunTakesItsTurnFromTheClient(t *testing.T) {
	h := newHarness(t)
	asked := 0
	ev := h.ev()
	ev.Allow = func() bool { asked++; return true }
	h.titler.run(context.Background(), ev)
	if r := h.waitRecorded(t); r.ev.KeyID != "k1" || asked != 1 {
		t.Errorf("recorded under %q after asking %d times", r.ev.KeyID, asked)
	}
}

// Two turns of the same new chat arrive close together. A second request for
// the same name would be harmless to the store and still billed twice.
func TestConsiderAsksOncePerChatInFlight(t *testing.T) {
	h := newHarness(t)
	h.up.arrived = make(chan struct{}, 2)
	h.up.release = make(chan struct{})

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
		"no key":            {true, Request{Conversation: "c", Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
		"no conversation":   {true, Request{Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
		"no account":        {true, Request{Conversation: "c", Model: "m", Body: []byte(chatBody)}},
		"no model":          {true, Request{Conversation: "c", AccountID: "a2", Body: []byte(chatBody)}},
		"no body":           {true, Request{Conversation: "c", Model: "m", AccountID: "a2"}},
		"a title request":   {true, Request{Conversation: "c", Model: "m", AccountID: "a2", Body: []byte(titleRequest)}},
		"already in flight": {true, Request{Conversation: "busy", Model: "m", AccountID: "a2", Body: []byte(chatBody)}},
		// A provider the titler has no way to.
		"an unknown provider": {true, Request{Conversation: "c", Model: "m", AccountID: "a2", Provider: "elsewhere",
			Body: []byte(chatBody)}},
	} {
		if c.ev.Provider == "" {
			c.ev.Provider = "anthropic"
		}
		if name != "no key" {
			c.ev.KeyID = "k1"
		}
		h := newHarness(t)
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
		"accounts unreadable": func(h *harness, _ *Request) { h.st.accountsErr = boom },
		"body without messages": func(_ *harness, ev *Request) {
			ev.Body = []byte(`{"model":"m"}`)
		},
		// The client at one of its limits: a title must never take what
		// its own next request needs.
		"client at a limit": func(_ *harness, ev *Request) { ev.Allow = func() bool { return false } },
	} {
		h := newHarness(t)
		ev := h.ev()
		setup(h, &ev)
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

// A refusal or an answer that is not a title must leave the chat unnamed, and
// no retry is made. Each still files the request it cost, when it cost one: a
// wasted call spends the client's budget like any other, and leaving it out
// would let through exactly the calls that bought nothing.
func TestRunStoresNothingWithoutATitle(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(h *harness)
		// status is the row that must be filed, 0 for none.
		status int
	}{
		"upstream refused": {func(h *harness) {
			h.up.status = http.StatusBadRequest
			h.up.answer = `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`
		}, http.StatusBadRequest},
		"model answered the conversation": {func(h *harness) {
			h.up.answer = `{"content":[{"type":"text","text":"` + strings.Repeat("I will read the parser. ", 10) + `"}],
				"usage":{"input_tokens":5,"output_tokens":40}}`
		}, http.StatusOK},
		// Falling back to another account would buy a title at full price,
		// since the cache lives on the account that is cooling. Nothing was
		// sent, so nothing is filed.
		"chat's account is cooling": {func(h *harness) { h.pool.cooling["a2"] = true }, 0},
	} {
		h := newHarness(t)
		tc.setup(h)
		h.titler.run(context.Background(), h.ev())
		if _, _, ok := h.st.title("chat-1"); ok {
			t.Errorf("%s: a title was stored", name)
		}
		if tc.status == 0 {
			if len(h.recorded) != 0 {
				t.Errorf("%s: a request was recorded", name)
			}
			continue
		}
		r := h.waitRecorded(t)
		if r.ev.Status != tc.status || r.ev.AccountID != "a2" || r.ev.ConversationID != "chat-1" {
			t.Errorf("%s: recorded %+v", name, r.ev)
		}
		if failed := tc.status != http.StatusOK; failed != (r.ev.Error != "") {
			t.Errorf("%s: status %d recorded with error %q", name, r.ev.Status, r.ev.Error)
		}
		if tc.status == http.StatusOK && r.ev.OutputTokens != 40 {
			t.Errorf("%s: output tokens = %d, want what the call spent", name, r.ev.OutputTokens)
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

// A title that could not be stored was still paid for, so it is still filed.
func TestRunRecordsAnUnstoredTitle(t *testing.T) {
	h := newHarness(t)
	h.st.setTitleErr = errors.New("disk full")
	h.titler.run(context.Background(), h.ev())
	if h.up.count() != 1 {
		t.Fatalf("upstream asked %d times", h.up.count())
	}
	if r := h.waitRecorded(t); r.ev.Status != http.StatusOK || r.ev.InputTokens != 5 {
		t.Errorf("recorded %+v, want the call it cost", r.ev)
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
		// crush's GenerateTitle at 76cc5c5 (internal/agent/agent.go:1867): its
		// title.md system prompt with " /no_think" after it, one user message,
		// no tools, streamed — on Messages, and on Responses, where fantasy
		// puts the system prompt among the input.
		{"crush on Messages", `{"model":"small","max_tokens":40,"stream":true,` +
			`"system":[{"type":"text","text":"You will generate a short title based on the first message a user begins a conversation with.\n\n<rules>\n</rules>\n /no_think"}],` +
			`"messages":[{"role":"user","content":[{"type":"text","text":"Generate a concise title for the following content:\n\nfix the build\n <think>\n\n</think>"}]}]}`, true},
		{"crush on Responses", `{"model":"gpt-5-small","max_output_tokens":40,"stream":true,"store":false,"input":[` +
			`{"role":"system","content":"You will generate a short title based on the first message a user begins a conversation with.\n /no_think"},` +
			`{"role":"user","content":[{"type":"input_text","text":"Generate a concise title for the following content:\n\nfix the build"}]}]}`, true},
		{"Responses, as instructions", `{"instructions":"You are a title generator. You output ONLY a thread title.","input":[]}`, true},
		// Claude Code 2.1.304's session title request, as captured on
		// 2026-10-20 (internal/service/clients, ClaudeCode): the billing
		// header and identity blocks first, the naming prompt third, an empty
		// tools array, structured output asking for {"title"}.
		{"Claude Code", claudeCodeNaming, true},
		{"Claude Code's first turn", `{"model":"claude-large","max_tokens":32000,"stream":true,` +
			`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],` +
			`"tools":[{"name":"Read","input_schema":{"type":"object"}}],` +
			`"messages":[{"role":"user","content":"explain the login bug in auth.go please"}]}`, false},
		// Its teleport title for a cloud session puts its prompt in the user
		// message, none of it in the system: not a chat of this gateway's,
		// and not taken.
		{"Claude Code's teleport title", `{"model":"claude-small","tools":[],"messages":[{"role":"user","content":` +
			`"You are coming up with a title and a git branch name for a coding session based on the provided description."}]}`, false},
		{"crush's coder turn", `{"system":[{"type":"text","text":"You are Crush"}],"tools":[{"name":"bash"}],` +
			`"messages":[{"role":"user","content":"Generate a concise title"}]}`, false},
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

// claudeCodeNaming is Claude Code 2.1.304's session title request as the stub
// recorded it, its naming prompt cut to its first and last sentences and its
// metadata device id shortened.
const claudeCodeNaming = `{"model":"claude-small","messages":[{"role":"user","content":[{"type":"text",` +
	`"text":"<session>\nexplain the login bug in auth.go please\n</session>\n\nWrite the title in the predominant language of the session."}]}],` +
	`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.304.644; cc_entrypoint=cli;"},` +
	`{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},` +
	`{"type":"text","text":"You are naming a coding session so the user can pick it out of a long list of sessions. ` +
	`Return JSON with a single \"title\" field. Capitalize the first letter of the title."}],` +
	`"tools":[],"metadata":{"user_id":"{\"device_id\":\"ea63\",\"account_uuid\":\"\",\"session_id\":\"e966da91-ed4b-4451-9a11-478849a2cd97\"}"},` +
	`"max_tokens":32000,"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object",` +
	`"properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}},"stream":true}`

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
	// A Responses stream, as ChatGPT answers a client on that API.
	responses := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Fix the \"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"build\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"
	if got := titleFromRelayedAnswer([]byte(responses)); got != "Fix the build" {
		t.Errorf("Responses: %q", got)
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

// A chat the gateway named from its first message — "Generate AGENTS.md",
// after crush's init prompt, was the production case — is renamed once the
// user types a second message: the agent's whole reply is in the body, and
// the chat is renamed from that opening exchange — a small request of its
// own, with no tools and nothing of the cached conversation — and then not
// again. A title the client gave the chat itself is not: it is the one the
// client shows.
func TestRenamedOnceFromTheOpeningExchange(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.st.titles["chat-2"] = "Generate AGENTS.md for codebase"
	own := h.ev()
	own.Conversation = "chat-2"
	own.Body = convo(crushInit, assistantUse, toolResult, followUp)
	h.titler.run(ctx, own)
	if n := h.up.count(); n != 0 {
		t.Fatalf("renamed the client's own title; asked %d times", n)
	}

	h.st.titles["chat-1"] = "Generate AGENTS.md for codebase"
	h.st.bases["chat-1"] = store.TitleRecord{Requests: 1, Turns: 1}
	summary := `{"role":"assistant","content":[{"type":"text","text":"Wrote AGENTS.md for the ZeeOS installer."}]}`

	ev := h.ev()
	ev.Body = convo(crushInit, assistantUse, toolResult, summary)
	h.titler.run(ctx, ev)
	if n := h.up.count(); n != 0 {
		t.Fatalf("renamed before the opening exchange was over; asked %d times", n)
	}

	ev.Body = convo(crushInit, assistantUse, toolResult, summary, followUp)
	h.titler.run(ctx, ev)
	if n := h.up.count(); n != 1 {
		t.Fatalf("upstream asked %d times, want one rename", n)
	}
	r, _, _ := h.st.TitleRecord(ctx, "chat-1")
	if r.Title != "Fix the SSE parser" || r.Turns != 2 || r.Manual {
		t.Errorf("after rename: %+v", r)
	}
	h.up.mu.Lock()
	sent := h.up.bodies[0]
	h.up.mu.Unlock()
	var body struct {
		Tools    []json.RawMessage `json:"tools"`
		System   []struct{ Text string }
		Messages []struct{ Content string }
	}
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 0 || len(body.System) == 0 ||
		!strings.Contains(body.System[len(body.System)-1].Text, "how\none began") ||
		len(body.Messages) != 1 ||
		!strings.Contains(body.Messages[0].Content, "Analyze this codebase") ||
		!strings.Contains(body.Messages[0].Content, "ZeeOS installer") ||
		strings.Contains(body.Messages[0].Content, "ext4") {
		t.Errorf("rename request is not the opening exchange on its own: %s", sent)
	}
	if e := h.waitRecorded(t).ev; e.Path != store.InternalPath || e.ConversationID != "chat-1" {
		t.Errorf("the rename was not filed like a title: %+v", e)
	}

	// Named from its opening once; later turns leave it alone.
	h.titler.mu.Lock()
	delete(h.titler.renamed, "chat-1")
	h.titler.mu.Unlock()
	ev.Body = convo(crushInit, assistantUse, toolResult, summary, followUp, assistantSay, followUp)
	h.titler.run(ctx, ev)
	if n := h.up.count(); n != 1 {
		t.Errorf("upstream asked %d times; renamed a second time", n)
	}
}

// A title the operator typed is never renamed automatically.
func TestATypedTitleIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.st.titles["chat-1"] = "ZeeOS installer"
	h.st.bases["chat-1"] = store.TitleRecord{Manual: true}

	ev := h.ev()
	ev.Body = convo(crushInit, assistantSay, followUp)
	h.titler.run(ctx, ev)
	if n := h.up.count(); n != 0 {
		t.Fatalf("a typed title was renamed automatically; asked %d times", n)
	}
}
