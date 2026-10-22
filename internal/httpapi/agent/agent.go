// Package agent is the gateway's MCP server: tools a client's own agent calls
// about the client it runs in — what it may ask for, what each of its names
// serves, how much it has used — and, when the operator allows it, to switch
// those names and catch its own configuration up.
//
// It is served at /mcp on the relay's listener, behind the client's own key,
// checked exactly as /v1 checks it (gateway.Guard, handed across by the
// composition root): a missing or unknown key is 401, a switched-off client
// 403, a switched-off gateway 404 — and so is the server itself while its own
// switch, Settings, is off (Setting) — and the key's rate limit counts these
// calls as it counts any other. The relay rather than the admin listener,
// because the agent runs where the client does, which reaches the relay and
// may never reach the admin address; and the client's key rather than an
// admin session, because what is offered is exactly that one client and
// nothing past it.
//
// Two tiers:
//
//	read    status, list_models, client, chats, name_chat — always
//	manage  bind_model, add_model, remove_model, update_config — only while
//	        the client's "Agent may manage this client" switch is on
//	        (agent_manage in schema.sql), off by default
//
// Never anything else: not its limits, its key, its type, its enable switch,
// not the accounts, other clients or the gateway's settings. Every manage
// call, allowed or refused, is logged at warn level as an admin change is,
// and filed in the request log under the client (audit), where the operator
// already looks at what a client did.
//
// The manage tools are listed whether or not the switch is on: a client
// caches the list, this server pushes nothing to tell it the list changed,
// and a tool that says why it is refused tells the agent who to ask, where
// one that vanished says nothing.
//
// The protocol is internal/mcp's. Each tool's validation is the admin API's
// own: a binding must be one of the client's choices (internal/service/models,
// available.go), a name is checked by the store, and the update command is
// made by the one path the admin page's is (clientconfig.MakeUpdate).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudication/internal/api"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/mcp"
	"claudication/internal/service/clientconfig"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/settings"
	"claudication/internal/store"
	"claudication/internal/version"
)

// Store is what the tools read and change of the database: one client's own
// rows, through the same store methods the admin API uses.
type Store interface {
	Client(ctx context.Context, id string) (store.Client, error)
	SetSlot(ctx context.Context, keyID, slot, name string, b store.Binding) error
	AddSlot(ctx context.Context, keyID, name string, b store.Binding) error
	RemoveSlot(ctx context.Context, keyID, slot string) error
	KeySecret(ctx context.Context, id string) (string, error)
	KeyActivity(ctx context.Context, keyID string, periodSince, daySince time.Time) (int, int64, error)
	ChatsIn(ctx context.Context, since time.Time, limit int, scope store.UsageScope) (store.ChatReport, error)
	ChatOfKey(ctx context.Context, id, keyID string) (bool, error)
	SetManualTitle(ctx context.Context, id, title string) (string, error)
}

// Models is the model list as the tools read it: the answers the admin page
// and /v1/models give, from internal/service/models.
type Models interface {
	List(ctx context.Context) ([]byte, error)
	For(ctx context.Context, key store.APIKey) ([]byte, error)
	Choices(ctx context.Context, off modelsvc.Off) []modelsvc.Choice
	Available(ctx context.Context, off modelsvc.Off) []modelsvc.Choice
	Resolve(ctx context.Context, off modelsvc.Off, b store.Binding) (modelsvc.Choice, string)
	Bindable(ctx context.Context, off modelsvc.Off, b store.Binding) (modelsvc.Choice, string)
	NewSlot(ctx context.Context, off modelsvc.Off, b store.Binding) (store.Binding, modelsvc.Choice, string)
}

// Deps is what the server is built from. Every field is required.
type Deps struct {
	Config *config.Config
	Log    *slog.Logger
	Store  Store
	Models Models
	// Install keeps the update links update_config makes.
	Install  *install.Links
	Budgets  *limits.Budgets
	InFlight *limits.InFlight
	// Enabled is the gateway switch, which status reports.
	Enabled *settings.Switch
	// On is the MCP server's own switch (Setting): off, /mcp answers 404 to
	// every client, whatever its "Agent may manage" switch says.
	On *settings.Switch
	// Record files a row in the request log: a manage call's audit.
	Record func(store.UsageEvent)
	// Guard fronts the server with the gateway's own key check
	// (gateway.Guard): the switch, the key, its client, its limits.
	Guard func(api.ErrorWriter, http.Handler) http.Handler
	// Share is a client's estimated share of the subscriptions, as its page
	// shows it (admin.ShareOf); ok false when there is no history.
	Share func(ctx context.Context, keyID string) (any, bool)
	// Conversation reads a chat id out of headers the way the client-facing
	// dialects do (api.Protocol, ConversationID), for a custom client, which
	// has no type to name its header: handed across by the composition root,
	// the one place that knows the dialects.
	Conversation func(h http.Header) string
}

// Agent serves the MCP server.
type Agent struct {
	d   Deps
	srv *mcp.Server

	// shares are the subscription shares status last read, by client: the
	// estimate reads every account's windows, and an agent polling status
	// must not make the gateway do that on each call.
	sharesMu sync.Mutex
	shares   map[string]cachedShare
}

// shareTTL is how long a client's share is answered from what was read: the
// windows it is estimated from are polled every few minutes at best.
const shareTTL = 30 * time.Second

type cachedShare struct {
	at    time.Time
	share any
	ok    bool
}

// share is a client's subscription share, read at most once per shareTTL.
func (a *Agent) share(ctx context.Context, keyID string) (any, bool) {
	a.sharesMu.Lock()
	defer a.sharesMu.Unlock()
	now := time.Now()
	if c, hit := a.shares[keyID]; hit && now.Sub(c.at) < shareTTL {
		return c.share, c.ok
	}
	for k, c := range a.shares {
		if now.Sub(c.at) >= shareTTL {
			delete(a.shares, k)
		}
	}
	share, ok := a.d.Share(ctx, keyID)
	a.shares[keyID] = cachedShare{at: now, share: share, ok: ok}
	return share, ok
}

// New builds the server and its tools.
func New(d Deps) *Agent {
	a := &Agent{d: d, shares: map[string]cachedShare{}}
	a.srv = &mcp.Server{
		Name: "claudication", Title: "Claudication gateway", Version: version.Version,
		Instructions: instructions,
		Tools:        a.tools(),
		Failed: func(ctx context.Context, tool string, err error) {
			key, _ := httpx.APIKey(ctx)
			d.Log.Error("mcp tool failed", "tool", tool, "client", key.ID, "err", err,
				"request_id", httpx.RequestID(ctx))
		},
	}
	return a
}

// Path is where the server is on the relay.
const Path = clientconfig.MCPPath

// Setting is the settings row holding the MCP server's switch (Deps.On).
const Setting = "mcp.enabled"

// Routes registers the server on the relay's mux.
func (a *Agent) Routes(mux *http.ServeMux) {
	mux.Handle(Path, a.d.Guard(a, a.switchedOn(http.HandlerFunc(a.serve))))
}

// switchedOn answers for the whole server while its switch is off: 404, as a
// switched-off gateway answers, in JSON-RPC's shape, for every client.
//
// 404 and not an empty tools list, or a 503. By the Streamable HTTP
// transport (MCP 2025-06-18), a 404 to initialize is an endpoint that is not
// there, which a client reports as a server that failed to connect and goes
// on without; a 404 to a request carrying a session id is a session gone,
// which a client re-initializes once and is told the same. A 503 says try
// again, and a client may, for as long as the switch stays off — the reason
// the gateway switch answers 404 too. Not measured per client: what is
// relied on is the transport's rule, which each implements. An empty tools list
// would be a healthy server with nothing on it, and since this server sends
// no list_changed, a client would keep that empty list after the switch is
// back on. Behind the key check and the gateway's switch, so a caller
// without a key is still 401 and a gateway switched off still says so.
//
// The clients' files keep their MCP entry either way (clientconfig): turning
// the server back on needs nothing reinstalled, only a client's reconnect —
// a new session, or /mcp in Claude Code.
func (a *Agent) switchedOn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.d.On != nil && !a.d.On.On() {
			mcp.WriteError(w, http.StatusNotFound,
				"this gateway's MCP server is switched off; an administrator turns it on under Settings")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteError answers a refusal of the gateway's own — no key, a switched-off
// client — in the shape an MCP client reads: a JSON-RPC error, under the
// status the key check chose, which is what the client acts on.
func (a *Agent) WriteError(w http.ResponseWriter, status int, _ api.ErrorKind, msg string) {
	mcp.WriteError(w, status, msg)
}

// call is what a tool knows of the request it is answering beyond its key.
type call struct {
	// conversation is the chat the request came from, when its client says.
	conversation string
	client       string
	surface      string
	ip           string
}

type callKey struct{}

func callOf(ctx context.Context) call {
	c, _ := ctx.Value(callKey{}).(call)
	return c
}

func (a *Agent) serve(w http.ResponseWriter, r *http.Request) {
	key, _ := httpx.APIKey(r.Context())
	c := call{
		conversation: a.conversationOf(key, r.Header),
		client:       api.ClientName(r.UserAgent()),
		ip:           httpx.ClientIP(r.Context()),
	}
	a.srv.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
}

// instructions are what a client may put before its model, so it knows what
// the tools are for before it reads each one.
const instructions = `These tools are about the client you are running in, as the claudication gateway serves it: its model names and what each serves, the models it may switch them to, its limits and usage, and its chats.

Read first: "client" shows this client's model names and what each serves now; "list_models" what any of them may be switched to; "status" the gateway, this client's limits and usage.

Changing the client (bind_model, add_model, remove_model, update_config) needs the operator to have turned on "Agent may manage this client" on the client's page; until then those tools say so.

A model name serves every chat of this client. To switch only the chat you are in, use the client's own model picker instead: /model in Claude Code and Codex, ctrl+l in crush, /models in opencode.`

// The chat a call comes from, which name_chat names when it is given none.
//
// What each client sends to an MCP server, read 2026-10-22 (Claude Code
// 2.1.308 on the wire against a logging server; the rest from source):
//
//	Claude Code  no session header: X-Claude-Code-Session-Id is set only by
//	             its Anthropic API client (2.1.308 @243665410), and an MCP
//	             request carries User-Agent, Accept-Encoding and the
//	             configured headers (@279687485); tools/call _meta holds
//	             claudecode/toolUseId alone (@279370534)
//	Codex        no header; tools/call _meta carries sessionId and threadId
//	             (codex-rs core/src/mcp_tool_call.rs:1294-1317 at
//	             rust-v0.163.0), the UUID its session_id header carries on
//	             /v1/responses, which is the chat's id here
//	crush        no header: sessionHeaders is put on model requests only
//	             (internal/agent/agent.go:1583 at 76cc5c5); no _meta
//	opencode     no header, no _meta (1.17.13, callTool @98717580)
//
// So the headers /v1 reads are read here too, for a client that starts
// sending them, then Codex's _meta; a call from any other client names its
// chat itself, found with the chats tool.

// conversation is the chat a tool call comes from, or "".
func conversation(ctx context.Context) string {
	if c := callOf(ctx).conversation; c != "" {
		return c
	}
	var meta struct {
		SessionID string `json:"sessionId"`
		ThreadID  string `json:"threadId"`
	}
	if json.Unmarshal(mcp.Meta(ctx), &meta) != nil {
		return ""
	}
	if id := api.CleanIdentifier(meta.SessionID); id != "" {
		return id
	}
	return api.CleanIdentifier(meta.ThreadID)
}

// conversationOf is the chat a request to the server says it comes from in
// its headers: the ones its client's type names its chats in
// (clients.Type, Sessions), as the gateway groups its /v1 requests; for a
// custom client, which has none, whatever the dialects read (Deps,
// Conversation), as /v1 reads an unknown client's.
func (a *Agent) conversationOf(key store.APIKey, h http.Header) string {
	t := (store.Client{APIKey: key, Type: key.ClientType}).Spec()
	if len(t.Sessions) == 0 {
		return a.d.Conversation(h)
	}
	return api.HeaderID(h, t.Sessions...)
}

// errorText is a store refusal as the agent is told it: the store's own words
// for a change it cannot make, and not found as not found.
func refusal(err error) error {
	switch {
	case errors.Is(err, store.ErrInvalidClient):
		return mcp.Refuse(err.Error())
	case errors.Is(err, store.ErrClientNotFound):
		return mcp.Refuse("this client has no model of that name")
	}
	return err
}

// decode reads a tool's arguments, refusing ones that do not fit as the
// JSON-RPC invalid-params error the specification names for them.
func decode(args json.RawMessage, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &mcp.ParamsError{Message: "the arguments do not fit this tool's schema: " + err.Error()}
	}
	return nil
}
