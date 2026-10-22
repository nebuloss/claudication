package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/mcp"
	"claudication/internal/service/clientconfig"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	"claudication/internal/store"
)

// perChat is what every tool that changes a name tells the agent, because
// the difference is the one an agent asked to "switch model" most needs: a
// name is the client's, for every chat it has.
const perChat = "A model name serves every chat of this client, on every machine it is set up on, from the next request. " +
	"To switch only the chat you are in, use the client's own model picker instead — /model in Claude Code and Codex, " +
	"ctrl+l in crush, /models in opencode — and leave this alone."

// manageNeeded says what a manage tool needs, at the head of its description.
const manageNeeded = "Needs the client's \"Agent may manage this client\" switch, which only an administrator can turn on, " +
	"on the client's page in the gateway's admin UI; without it this refuses and says so. "

// bindingSchema is a binding's two forms, as a tool takes them.
const bindingProps = `"model": {"type": "string", "description": "A real model id from list_models, e.g. claude-opus-5-5 or gpt-5.5. Give this or follows, not both."},
    "follows": {"type": "string", "description": "A moving target from list_models that follows the newest model of a kind, e.g. claude:opus, claude:sonnet, openai:first. Give this or model, not both."}`

func (a *Agent) tools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "status", Title: "Gateway and client status", ReadOnly: true,
			Description: "This client's standing at the claudication gateway: whether the gateway is accepting requests, " +
				"this client's limits (requests per period, token budget per day) and what it has used of them, " +
				"its requests open now, and its estimated share of each provider's subscription windows (the 5-hour " +
				"and the 7-day) across the accounts the gateway pools. Use it to explain a rate limit or decide " +
				"whether to switch to a lighter model. Changes nothing.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Call:        a.status,
		},
		{
			Name: "list_models", Title: "Models this client may use", ReadOnly: true,
			Description: "What this client's model names may be bound to now: each moving target (the newest Claude " +
				"Opus, Sonnet..., ChatGPT's first-ranked) with the model it serves today, then each real model, with " +
				"its context window, output limit, effort levels and whether it takes images. Only what an account " +
				"connected to the gateway serves now is listed, so anything here is a valid model or follows for " +
				"bind_model and add_model. Changes nothing.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Call:        a.listModels,
		},
		{
			Name: "client", Title: "This client", ReadOnly: true,
			Description: "This client as the gateway knows it: its name and type (Claude Code, Codex, crush, opencode " +
				"or custom), the API it speaks, and each of its model names — the names its configuration asks for " +
				"— with what each is bound to, the real model and provider that serves it now, and why not when it " +
				"cannot be served. Also whether its agent may change it (the manage tools) and whether it takes " +
				"model names of its own beyond its type's. Changes nothing.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Call:        a.client,
		},
		{
			Name: "chats", Title: "This client's recent chats", ReadOnly: true,
			Description: "This client's chats over the last week, most recent first: each one's id, the title the " +
				"gateway shows for it, when it was last active, and its requests and models. The id is what name_chat " +
				"takes. The chat this call comes from is marked current when the client says which it is (Codex " +
				"does); otherwise the most recently active one is usually it. Changes nothing.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Call:        a.chats,
		},
		{
			Name: "name_chat", Title: "Name a chat",
			Description: "Sets the title the gateway shows for one of this client's chats — in its admin UI's chat " +
				"list and request log — as an administrator's rename does: it replaces the title the client gave " +
				"the chat itself (Claude Code, crush and opencode name their chats) or the gateway generated, and " +
				"neither renames it after. It does not rename the chat inside the client. chat_id is an id from the " +
				"chats tool; without it, the chat this call comes from, when the client says which (Codex does; " +
				"Claude Code, crush and opencode do not, so give the id).",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {"type": "string", "minLength": 1, "maxLength": 120, "description": "The chat's new title: a few words, at most 120 characters."},
    "chat_id": {"type": "string", "description": "The chat's id, from the chats tool. Optional when the client says which chat this call comes from."}
  },
  "required": ["title"],
  "additionalProperties": false
}`),
			Call: a.nameChat,
		},
		{
			Name: "bind_model", Title: "Switch a model name", Destructive: true,
			Description: manageNeeded + "Binds one of this client's model names (from the client tool) to another model " +
				"or moving target from list_models; only what list_models offers is accepted. " + perChat +
				" The client reads a model's limits from its configuration file, so after binding a name to a model " +
				"with another context window or effort levels, run update_config to catch the file up.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The client's model name to rebind, as the client tool lists it: large, opus, main..."},
    ` + bindingProps + `
  },
  "required": ["name"],
  "additionalProperties": false
}`),
			Call: a.manage("bind_model", a.bindModel),
		},
		{
			Name: "add_model", Title: "Add a model name",
			Description: manageNeeded + "Gives this client a model name of its own beside its type's, bound to a model " +
				"or moving target from list_models (the newest Claude Opus when neither is given), for its model " +
				"picker to list — only on a type that takes them (the client tool says). The client lists it once its " +
				"configuration is updated: run update_config after. " + perChat,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9.-]{0,63}$", "description": "The new name: lower-case letters, digits, dots and dashes, up to 64, not one the client has."},
    ` + bindingProps + `
  },
  "required": ["name"],
  "additionalProperties": false
}`),
			Call: a.manage("add_model", a.addModel),
		},
		{
			Name: "remove_model", Title: "Remove a model name", Destructive: true,
			Description: manageNeeded + "Takes away a model name that was added to this client (add_model, or on its " +
				"page); its type's own names stay. A chat still asking for the name is refused from its next " +
				"request. Run update_config after, so the client's picker stops listing it.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The added model name to remove."}
  },
  "required": ["name"],
  "additionalProperties": false
}`),
			Call: a.manage("remove_model", a.removeModel),
		},
		{
			Name: "update_config", Title: "Update this client's configuration",
			Description: manageNeeded + "Makes the one-line command that rewrites this client's configuration files " +
				"on a machine already set up with it: its model names, what each serves and each one's limits. The " +
				"command is the admin page's Update config: it carries no key and keeps the key on the machine, " +
				"works once, for fifteen minutes, and changes nothing on a machine never set up with this client. " +
				"Run it yourself with your shell tool on the machine to update (curl into sh on Linux and macOS, " +
				"irm into iex in PowerShell on Windows), then restart the client — or start a new session — for it " +
				"to read the files.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "platform": {"type": "string", "enum": ["unix", "windows"], "description": "unix for Linux and macOS (sh), windows for PowerShell."}
  },
  "required": ["platform"],
  "additionalProperties": false
}`),
			Call: a.manage("update_config", a.updateConfig),
		},
	}
}

// self is the client this call's key belongs to, read afresh.
func (a *Agent) self(ctx context.Context) (store.Client, error) {
	key, ok := httpx.APIKey(ctx)
	if !ok || key.ClientType == "" {
		// The gateway's own key is no client; nothing reaches here without
		// a key, but one that is no client has nothing to be told about.
		return store.Client{}, mcp.Refuse("this key belongs to no client")
	}
	c, err := a.d.Store.Client(ctx, key.ID)
	if errors.Is(err, store.ErrClientNotFound) {
		return store.Client{}, mcp.Refuse("this key belongs to no client")
	}
	return c, err
}

// ── read ────────────────────────────────────────────────────────────────────

func (a *Agent) status(ctx context.Context, _ json.RawMessage) (any, error) {
	c, err := a.self(ctx)
	if err != nil {
		return nil, err
	}
	cfg := a.d.Config
	count, period, own := c.RPMLimit, c.Period(), true
	if count == 0 {
		count, period, own = cfg.Limits.RequestsPerMinute, time.Minute, false
	}
	now := time.Now()
	reqs, err := a.d.Store.KeyRequests(ctx, c.ID, now.Add(-max(limits.BudgetWindow, period)))
	if err != nil {
		return nil, err
	}
	var inPeriod int
	var day int64
	for _, q := range reqs {
		if q.At.After(now.Add(-period)) {
			inPeriod++
		}
		if q.At.After(now.Add(-limits.BudgetWindow)) {
			day += q.Tokens
		}
	}
	lim := map[string]any{
		"requests":         count,
		"per_seconds":      int(period.Seconds()),
		"requests_default": !own,
		"token_budget":     c.TokenBudget,
		"budget_hours":     int(limits.BudgetWindow.Hours()),
	}
	if c.TokenBudget == 0 {
		lim["token_budget_note"] = "0 is no budget"
	}
	use := map[string]any{
		"requests_in_period":      inPeriod,
		"tokens_in_budget_window": day,
		"open_now":                a.d.InFlight.Open(c.ID),
		"history_recorded":        cfg.Usage.Enabled(),
	}
	out := map[string]any{
		"gateway": map[string]any{"accepting": a.d.Enabled.On()},
		"client":  map[string]any{"name": c.Name, "type": c.Type, "agent_may_manage": c.AgentManage},
		"limits":  lim,
		"usage":   use,
	}
	if share, ok := a.d.Share(ctx, c.ID); ok {
		out["subscription_share"] = share
		out["subscription_share_note"] = "Percent of each provider's pooled 5-hour and 7-day windows this client used, estimated " +
			"from its share of the gateway's traffic on each account; anything else using the same accounts is counted as " +
			"the gateway's clients."
	}
	return out, nil
}

// facts are a model's limits, read off the gateway's model list.
type facts struct {
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output_tokens,omitempty"`
	Effort        []string `json:"effort_levels,omitempty"`
	Images        *bool    `json:"images,omitempty"`
}

// factsByID reads each model's facts off the gateway's model list; a list
// that cannot be read leaves them out, and the choices still list.
func (a *Agent) factsByID(ctx context.Context) map[string]facts {
	out := map[string]facts{}
	body, err := a.d.Models.List(ctx)
	if err != nil {
		return out
	}
	var l struct {
		Data []clientconfig.Model `json:"data"`
	}
	if json.Unmarshal(body, &l) != nil {
		return out
	}
	for _, m := range l.Data {
		f := facts{Effort: clientconfig.Efforts(&m)}
		if m.MaxInputTokens != nil {
			f.ContextWindow = *m.MaxInputTokens
		}
		if m.MaxTokens != nil {
			f.MaxOutput = *m.MaxTokens
		}
		if m.Capabilities.ImageInput != nil {
			f.Images = m.Capabilities.ImageInput.Supported
		}
		out[store.ModelKey(m.ID)] = f
	}
	return out
}

func (a *Agent) listModels(ctx context.Context, _ json.RawMessage) (any, error) {
	c, err := a.self(ctx)
	if err != nil {
		return nil, err
	}
	known := a.factsByID(ctx)
	type entry struct {
		Model    string         `json:"model,omitempty"`
		Follows  clients.Follow `json:"follows,omitempty"`
		Title    string         `json:"title"`
		Serving  string         `json:"serving"`
		Provider string         `json:"provider"`
		facts
	}
	var targets, models []entry
	choices := a.d.Models.Choices(ctx)
	if c.Mode == clients.Direct {
		choices = a.d.Models.Available(ctx)
	}
	for _, ch := range choices {
		e := entry{Model: ch.Model, Follows: ch.Follows, Title: ch.Title, Serving: ch.Serving,
			Provider: clients.ProviderTitle(ch.Provider), facts: known[store.ModelKey(ch.Serving)]}
		if ch.Follows != "" {
			targets = append(targets, e)
		} else {
			models = append(models, e)
		}
	}
	out := map[string]any{"follow_targets": orEmpty(targets), "models": orEmpty(models)}
	if c.Mode == clients.Direct {
		out["note"] = "This client names real models: it asks for any of these ids by itself, and has no names to bind."
	} else {
		out["note"] = "Bind a name with bind_model to a model (its id) or a follow target (its follows). " + perChat
	}
	return out, nil
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func (a *Agent) client(ctx context.Context, _ json.RawMessage) (any, error) {
	c, err := a.self(ctx)
	if err != nil {
		return nil, err
	}
	t := c.Spec()
	type name struct {
		Name        string         `json:"name"`
		Slot        string         `json:"slot"`
		Label       string         `json:"label"`
		Hint        string         `json:"hint,omitempty"`
		Field       string         `json:"field,omitempty"`
		Model       string         `json:"bound_model,omitempty"`
		Follows     clients.Follow `json:"bound_follows,omitempty"`
		Title       string         `json:"title"`
		Serving     string         `json:"serving"`
		Provider    string         `json:"provider,omitempty"`
		Unavailable string         `json:"unavailable,omitempty"`
		Removable   bool           `json:"removable"`
	}
	names := []name{}
	for _, sl := range c.Slots {
		spec, _ := t.SlotFor(sl.Slot)
		label := spec.Label
		if _, typed := t.Slot(sl.Slot); !typed {
			label = "added name"
		}
		b, why := a.d.Models.Resolve(ctx, store.Binding{Model: sl.Model, Follows: sl.Follows})
		names = append(names, name{Name: sl.Name, Slot: sl.Slot, Label: label, Hint: spec.Hint, Field: spec.Field,
			Model: sl.Model, Follows: sl.Follows, Title: b.Title, Serving: b.Serving,
			Provider: clients.ProviderTitle(b.Provider), Unavailable: why, Removable: t.Removable(sl.Slot)})
	}
	apiTitle := "Anthropic Messages and OpenAI Responses"
	if t.API != "" {
		apiTitle = t.API.Title()
	}
	out := map[string]any{
		"id": c.ID, "name": c.Name, "type": t.ID, "type_label": t.Label, "api": apiTitle,
		"mode":              c.Mode,
		"agent_may_manage":  c.AgentManage,
		"takes_added_names": t.ExtraSlots != "",
		"configured":        c.ConfiguredAt != nil,
		"model_names":       names,
		"picker":            perChat,
	}
	if t.ExtraHint != "" {
		out["added_names_hint"] = t.ExtraHint
	}
	if c.Mode == clients.Direct {
		out["note"] = "This client is in direct mode: it asks for real model ids (list_models), and its names are kept but unused."
	}
	return out, nil
}

// chatWindow is how far back the chats tool reads.
const chatWindow = 7 * 24 * time.Hour

func (a *Agent) chats(ctx context.Context, _ json.RawMessage) (any, error) {
	c, err := a.self(ctx)
	if err != nil {
		return nil, err
	}
	if !a.d.Config.Usage.Enabled() {
		return nil, mcp.Refuse("this gateway keeps no request history, so it has no chats to list")
	}
	rep, err := a.d.Store.ChatsIn(ctx, time.Now().Add(-chatWindow), store.MaxChatsPage, store.UsageScope{KeyID: c.ID})
	if err != nil {
		return nil, err
	}
	// Busiest first is the admin list's order; the one an agent is looking
	// for is the one it is in, which is the latest.
	sort.SliceStable(rep.Chats, func(i, j int) bool { return rep.Chats[i].Last.After(rep.Chats[j].Last) })
	type chat struct {
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Named    string   `json:"named_by,omitempty"`
		Last     string   `json:"last_active"`
		Requests int64    `json:"requests"`
		Models   []string `json:"models"`
		Current  bool     `json:"current,omitempty"`
	}
	current := conversation(ctx)
	out := []chat{}
	for i, ch := range rep.Chats {
		if i == 20 {
			break
		}
		e := chat{ID: ch.ID, Title: ch.Title, Last: ch.Last.UTC().Format(time.RFC3339), Requests: ch.Requests,
			Models: ch.Models, Current: ch.ID == current}
		if ch.TitleManual {
			e.Named = "by hand (an administrator, or name_chat)"
		}
		out = append(out, e)
	}
	return map[string]any{"chats": out, "current": current}, nil
}

func (a *Agent) nameChat(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		Title  string `json:"title"`
		ChatID string `json:"chat_id"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	c, err := a.self(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, &mcp.ParamsError{Message: "a title cannot be empty"}
	}
	id := strings.TrimSpace(in.ChatID)
	if id == "" {
		if id = conversation(ctx); id == "" {
			return nil, mcp.Refuse("this client does not say which chat a call comes from: give chat_id, from the chats tool")
		}
	}
	if !a.d.Config.Usage.Enabled() {
		return nil, mcp.Refuse("this gateway keeps no request history, so it cannot tell whose chat that is")
	}
	// Only a chat this client took part in: an id is a client's own, and one
	// of another client's is answered as one that does not exist.
	ok, err := a.d.Store.ChatOfKey(ctx, id, c.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, mcp.Refuse("no chat " + id + " of this client's; the chats tool lists them")
	}
	stored, err := a.d.Store.SetManualTitle(ctx, id, title)
	if err != nil {
		return nil, err
	}
	a.d.Log.Info("chat named by its client's agent", "client", c.ID, "chat", id, "ip", callOf(ctx).ip)
	return map[string]any{"chat_id": id, "title": stored}, nil
}

// ── manage ──────────────────────────────────────────────────────────────────

// manageCall is a manage tool's own work: what it did, for the answer, and
// the name and model the audit row shows.
type manageCall func(ctx context.Context, c store.Client, args json.RawMessage) (out any, name, model string, err error)

// manage is a manage tool: refused unless the client's switch is on, and
// every call, refused or not, logged and filed in the request log.
func (a *Agent) manage(tool string, run manageCall) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		c, err := a.self(ctx)
		if err != nil {
			return nil, err
		}
		var out any
		var name, model string
		if !c.AgentManage {
			err = mcp.Refuse("this client's agent may not change it: an administrator can turn on \"Agent may manage " +
				"this client\" on its page in the gateway's admin UI. The read tools work without it.")
		} else {
			out, name, model, err = run(ctx, c, args)
		}
		a.audit(ctx, c, tool, name, model, err)
		return out, err
	}
}

// audit files a manage call in the request log, under the client: the place
// the operator already reads what a client did, filtered by client, with the
// tool as its path. A row of the kind a refusal is (Rejected), because it
// reached no upstream and spent nothing: every chart and total steps over
// it, and only the log shows it. Refused calls are filed too, with why —
// an agent trying what it may not is worth seeing.
func (a *Agent) audit(ctx context.Context, c store.Client, tool, name, model string, err error) {
	info := callOf(ctx)
	status, why := 200, ""
	var te *mcp.ToolError
	var pe *mcp.ParamsError
	switch {
	case err == nil:
	case errors.As(err, &te) && !c.AgentManage:
		status, why = 403, te.Message
	case errors.As(err, &te):
		status, why = 400, te.Message
	case errors.As(err, &pe):
		status, why = 400, pe.Message
	default:
		status, why = 500, "the gateway could not do it"
	}
	a.d.Log.Warn("client changed by its agent", "tool", tool, "client", c.ID, "name", c.Name,
		"model_name", name, "model", model, "status", status, "refused", why, "ip", info.ip,
		"request_id", httpx.RequestID(ctx))
	a.d.Record(store.UsageEvent{
		At: time.Now(), KeyID: c.ID, Path: Path + "/" + tool, Status: status,
		VirtualModel: name, Model: model, ConversationID: conversation(ctx),
		Client: info.client, IP: info.ip, Rejected: true, Error: why,
	})
}

// binding reads a model or a follow target, one of the two.
func binding(model, follows string) (store.Binding, error) {
	b := store.Binding{Model: strings.TrimSpace(model), Follows: clients.Follow(strings.TrimSpace(follows))}
	switch {
	case b.Model != "" && b.Follows != "":
		return b, &mcp.ParamsError{Message: "give model or follows, not both"}
	case b.Follows != "" && !b.Follows.Valid():
		return b, mcp.Refuse("no follow target " + string(b.Follows) + "; list_models lists them")
	}
	return b, nil
}

// available refuses a binding the client cannot be offered now, as the admin
// API does: the same rule (internal/service/models, available.go), the same
// words.
func (a *Agent) available(ctx context.Context, b store.Binding) (string, error) {
	choice, why := a.d.Models.Resolve(ctx, b)
	if why != "" {
		return "", mcp.Refuse(choice.Title + " is not available to this client: " + why + "; list_models lists what is")
	}
	return choice.Serving, nil
}

// slotNamed is the slot of a client's that has a name.
func slotNamed(c store.Client, name string) (store.ClientSlot, bool) {
	key := store.ModelKey(strings.TrimSpace(name))
	for _, sl := range c.Slots {
		if sl.Name == key {
			return sl, true
		}
	}
	return store.ClientSlot{}, false
}

func names(c store.Client) string {
	var out []string
	for _, sl := range c.Slots {
		out = append(out, sl.Name)
	}
	return strings.Join(out, ", ")
}

func (a *Agent) bindModel(ctx context.Context, c store.Client, args json.RawMessage) (any, string, string, error) {
	var in struct {
		Name    string `json:"name"`
		Model   string `json:"model"`
		Follows string `json:"follows"`
	}
	if err := decode(args, &in); err != nil {
		return nil, "", "", err
	}
	if c.Mode == clients.Direct {
		return nil, in.Name, "", mcp.Refuse("this client asks for real model ids (direct mode) and has no names to bind")
	}
	sl, ok := slotNamed(c, in.Name)
	if !ok {
		return nil, in.Name, "", mcp.Refuse("this client has no model named " + in.Name + "; its names are " + names(c))
	}
	b, err := binding(in.Model, in.Follows)
	if err != nil {
		return nil, sl.Name, "", err
	}
	if b.Model == "" && b.Follows == "" {
		return nil, sl.Name, "", &mcp.ParamsError{Message: "give model or follows: what the name is to serve"}
	}
	serving, err := a.available(ctx, b)
	if err != nil {
		return nil, sl.Name, b.Model + string(b.Follows), err
	}
	if err := a.d.Store.SetSlot(ctx, c.ID, sl.Slot, "", b); err != nil {
		return nil, sl.Name, serving, refusal(err)
	}
	return map[string]any{
		"name": sl.Name, "bound_model": b.Model, "bound_follows": b.Follows, "serving": serving,
		"note": "Every chat of this client asking for " + sl.Name + " is served by " + serving +
			" from its next request. If its context window or effort levels differ from the last model's, run update_config.",
	}, sl.Name, serving, nil
}

func (a *Agent) addModel(ctx context.Context, c store.Client, args json.RawMessage) (any, string, string, error) {
	var in struct {
		Name    string `json:"name"`
		Model   string `json:"model"`
		Follows string `json:"follows"`
	}
	if err := decode(args, &in); err != nil {
		return nil, "", "", err
	}
	name := strings.TrimSpace(in.Name)
	b, err := binding(in.Model, in.Follows)
	if err != nil {
		return nil, name, "", err
	}
	if b.Model == "" && b.Follows == "" {
		// What every type's main name starts on, as the admin page's does.
		b.Follows = clients.FollowOpus
	}
	serving, err := a.available(ctx, b)
	if err != nil {
		return nil, name, b.Model + string(b.Follows), err
	}
	if err := a.d.Store.AddSlot(ctx, c.ID, name, b); err != nil {
		return nil, name, serving, refusal(err)
	}
	return map[string]any{
		"name": store.ModelKey(name), "serving": serving,
		"note": "Added. The client's picker lists it once its configuration is updated: run update_config.",
	}, name, serving, nil
}

func (a *Agent) removeModel(ctx context.Context, c store.Client, args json.RawMessage) (any, string, string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(args, &in); err != nil {
		return nil, "", "", err
	}
	sl, ok := slotNamed(c, in.Name)
	if !ok {
		return nil, in.Name, "", mcp.Refuse("this client has no model named " + in.Name + "; its names are " + names(c))
	}
	if err := a.d.Store.RemoveSlot(ctx, c.ID, sl.Slot); err != nil {
		return nil, sl.Name, "", refusal(err)
	}
	return map[string]any{
		"removed": sl.Name,
		"note":    "Removed. Run update_config so the client's picker stops listing it.",
	}, sl.Name, "", nil
}

func (a *Agent) updateConfig(ctx context.Context, c store.Client, args json.RawMessage) (any, string, string, error) {
	var in struct {
		Platform string `json:"platform"`
	}
	if err := decode(args, &in); err != nil {
		return nil, "", "", err
	}
	if in.Platform != "unix" && in.Platform != "windows" {
		return nil, "", "", &mcp.ParamsError{Message: "platform is unix or windows"}
	}
	if !clientconfig.Writes(c.Type) {
		return nil, "", "", mcp.Refuse("a custom client has no configuration files of the gateway's to update")
	}
	if c.ConfiguredAt == nil {
		return nil, "", "", mcp.Refuse("this client is not set up yet: its install command, on its page, sets it up")
	}
	base := callOf(ctx).base
	if base == "" {
		return nil, "", "", mcp.Refuse("the gateway does not know its own address: an administrator sets public-url")
	}
	// As the admin page does: a list that cannot be read gives files with
	// each name at the defaults rather than no update.
	list, err := a.d.Models.For(ctx, c.APIKey)
	if err != nil {
		a.d.Log.Warn("could not read a client's model list; writing its files without the models' facts", "id", c.ID, "err", err)
	}
	token, expires, err := clientconfig.MakeUpdate(ctx, a.d.Store, a.d.Install, c, clientconfig.For(c, list), base)
	if err != nil {
		return nil, "", "", err
	}
	windows := in.Platform == "windows"
	how := "Run it in a terminal (sh) on the machine to update."
	if windows {
		how = "Run it in PowerShell on the machine to update."
	}
	return map[string]any{
		"command":    install.Command(base, token, windows),
		"expires_at": expires.UTC().Format(time.RFC3339),
		"note": how + " It works once, until expires_at, carries no key and keeps the one on the machine. " +
			"Restart the client, or start a new session, for it to read the new files.",
	}, "", "", nil
}
