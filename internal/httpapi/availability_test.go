package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"claudication/internal/httpapi/admin"
	"claudication/internal/service/clients"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/store"
)

// accountOf is the id of the gateway's one account of a provider.
func accountOf(t *testing.T, st *store.Store, provider string) string {
	t.Helper()
	all, err := st.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.Provider == provider {
			return a.ID
		}
	}
	t.Fatalf("no %s account", provider)
	return ""
}

// choiceValues is a menu as values: model:<id> for each model,
// follow:<family>=<serving> for each family.
func choiceValues(cs []modelsvc.Choice) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Model != "" {
			out = append(out, "model:"+c.Model)
		} else {
			out = append(out, "follow:"+string(c.Follows)+"="+c.Serving)
		}
	}
	return out
}

// everyModel puts both providers on offering every model, as the gateway
// did before families: what the tests of each model's availability read.
func everyModel(t *testing.T, srv *Server) {
	t.Helper()
	for _, sw := range srv.models.Modes() {
		if err := sw.Set(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
}

// What a client may be offered: a model a connected account lists and has
// on, of either provider, whichever API the client speaks; a family only
// while some account serves it. The Claude stub lists claude-sonnet-5 and
// claude-haiku-5, so no account serves Opus.
func TestAvailability(t *testing.T) {
	srv, st, _, _, _, _ := twoUpstreams(t)
	everyModel(t, srv)
	ctx := context.Background()
	m := srv.models

	// Every model: each model once, and no family a choice.
	all := m.Choices(ctx, nil)
	seen := map[string]bool{}
	for _, c := range all {
		if c.Model == "" || seen[c.Model] {
			t.Errorf("a choice that is a family, or a model twice: %+v", c)
		}
		seen[c.Model] = true
	}
	choices := choiceValues(all)
	if !slices.Equal(choices[:2], []string{"model:claude-sonnet-5", "model:claude-haiku-5"}) {
		t.Errorf("choices = %v, want the Claude models first", choices)
	}
	for _, v := range []string{"model:claude-sonnet-5", "model:claude-haiku-5", "model:gpt-6.1-sol", "model:gpt-5.6-luna"} {
		if !slices.Contains(choices, v) {
			t.Errorf("choices %v lack %s", choices, v)
		}
	}
	// A family still resolves, in either mode: luna's newest by version, the
	// backend dating nothing.
	for f, want := range map[clients.Follow]string{clients.FollowSol: "gpt-6.1-sol", clients.FollowLuna: "gpt-6-luna",
		"openai:astra": "gpt-6-astra", clients.FollowSonnet: "claude-sonnet-5"} {
		if c, why := m.Resolve(ctx, nil, store.Binding{Follows: f}); why != "" || c.Serving != want {
			t.Errorf("%s serves %q (%q), want %s", f, c.Serving, why, want)
		}
	}
	if slices.ContainsFunc(choices, func(s string) bool { return strings.HasPrefix(s, "follow:claude:opus") }) {
		t.Errorf("an unresolved Opus was offered: %v", choices)
	}

	why := func(b store.Binding) string {
		t.Helper()
		_, w := m.Resolve(ctx, nil, b)
		return w
	}
	if w := why(store.Binding{Model: "gpt-6.1-sol"}); w != "" {
		t.Errorf("a GPT model: %q", w)
	}
	if w := why(store.Binding{Model: "claude-opus-4"}); w != "no connected account lists it" {
		t.Errorf("an unlisted model: %q", w)
	}
	if w := why(store.Binding{Follows: clients.FollowOpus}); !strings.Contains(w, "no account lists a Claude Opus model") {
		t.Errorf("an unresolved family: %q", w)
	}

	// Switched off on its only account: neither the model nor the target
	// resolving to it is offered, and a binding to it says why.
	claude := accountOf(t, st, "anthropic")
	if err := st.SetAccountModel(ctx, claude, "claude-haiku-5", false); err != nil {
		t.Fatal(err)
	}
	if w := why(store.Binding{Model: "claude-haiku-5"}); w != "it is switched off on every account that lists it" {
		t.Errorf("a model off: %q", w)
	}
	if w := why(store.Binding{Follows: clients.FollowHaiku}); w == "" {
		t.Error("a target resolving to a model off is available")
	}
	if got := choiceValues(m.Choices(ctx, nil)); slices.ContainsFunc(got, func(s string) bool {
		return strings.Contains(s, "haiku")
	}) || !slices.Contains(got, "model:claude-sonnet-5") {
		t.Errorf("choices with haiku off = %v", got)
	}

	// The account switched off: nothing of its is listed by an account that
	// can serve.
	if err := st.SetAccountDisabled(ctx, claude, true); err != nil {
		t.Fatal(err)
	}
	if w := why(store.Binding{Model: "claude-sonnet-5"}); w != "no connected account lists it" {
		t.Errorf("the account off: %q", w)
	}
	if got := choiceValues(m.Choices(ctx, nil)); slices.ContainsFunc(got, func(s string) bool {
		return strings.Contains(s, "claude")
	}) {
		t.Errorf("choices with no Claude account on = %v", got)
	}
}

// A client's page is handed its choices and each slot's availability; a slot
// may keep a binding that has become unavailable, and is renamed with it,
// but cannot be given a new one that is not available.
func TestAdminOffersOnlyAvailableBindings(t *testing.T) {
	srv, st, _, _, _, _ := twoUpstreams(t)
	everyModel(t, srv)
	abase, ck := adminAPI(t, srv)
	c, _ := newClient(t, st, "box", "crush", nil)
	get := func() admin.ClientJSON {
		t.Helper()
		status, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
		if status != http.StatusOK {
			t.Fatalf("get: %d %s", status, body)
		}
		return decode[struct {
			Client admin.ClientJSON `json:"client"`
		}](t, body).Client
	}
	got := get()
	if v := choiceValues(got.Choices); !slices.Contains(v, "model:gpt-6.1-sol") || !slices.Contains(v, "model:claude-sonnet-5") {
		t.Errorf("a crush's choices = %v, want both providers' models", v)
	}
	large, small := got.Slots[0], got.Slots[1]
	if large.Bound != clients.FollowOpus || !strings.Contains(large.Unavailable, "no account lists a Claude Opus model") ||
		large.Title != "Claude Opus" {
		t.Errorf("large, following an unresolved Opus: %+v", large)
	}
	if small.Unavailable != "" || small.Serving != "claude-haiku-5" || small.Provider != "anthropic" {
		t.Errorf("small: %+v", small)
	}

	set := func(slot, body string) (int, string) {
		t.Helper()
		status, out := call(t, abase, http.MethodPut, "/admin/clients/"+c.ID+"/slots/"+slot, body, ck)
		return status, string(out)
	}
	if status, out := set("large", `{"model":"claude-opus-4"}`); status != http.StatusBadRequest ||
		!strings.Contains(out, "no connected account lists it") {
		t.Errorf("an unlisted model: %d %s", status, out)
	}
	// Renamed, its unavailable binding sent back as it is: kept.
	if status, out := set("large", `{"name":"big","follows":"claude:opus"}`); status != http.StatusOK {
		t.Errorf("a rename keeping an unavailable binding: %d %s", status, out)
	}
	if status, out := set("large", `{"model":"claude-sonnet-5"}`); status != http.StatusOK {
		t.Errorf("an available model: %d %s", status, out)
	}
	if got := get().Slots[0]; got.Name != "big" || got.Model != "claude-sonnet-5" || got.Unavailable != "" {
		t.Errorf("large after: %+v", got)
	}
	// A GPT model is a crush's to have, on the API crush speaks.
	if status, out := set("small", `{"model":"gpt-6.1-sol"}`); status != http.StatusOK {
		t.Errorf("a GPT model on a crush: %d %s", status, out)
	}
	if got := get().Slots[1]; got.Serving != "gpt-6.1-sol" || got.Provider != "openai" || got.Unavailable != "" {
		t.Errorf("small after: %+v", got)
	}
}

// /v1/models offers a client only the names that resolve to something it
// can be served now, and a direct-mode client's list only available models.
func TestModelListsOfferOnlyAvailable(t *testing.T) {
	srv, st, base, _, _, _ := twoUpstreams(t)
	everyModel(t, srv)
	abase, ck := adminAPI(t, srv)
	ctx := context.Background()
	_, key := newClient(t, st, "box", "crush", nil)
	listed := func(key string) []string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var l struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		ids := []string{}
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if ids := listed(key); !slices.Equal(ids, []string{"small"}) {
		t.Errorf("crush's /v1/models = %v, want small alone: large follows an Opus no account lists", ids)
	}
	if err := st.SetAccountModel(ctx, accountOf(t, st, "anthropic"), "claude-haiku-5", false); err != nil {
		t.Fatal(err)
	}
	if ids := listed(key); len(ids) != 0 {
		t.Errorf("with haiku off, crush's /v1/models = %v", ids)
	}

	// Direct mode: the client's Models page lists what an account lists and
	// has on, of either provider, and nothing else.
	direct := func(name string) []string {
		t.Helper()
		c, _ := newClient(t, st, name, "custom", nil)
		if err := st.SetClientMode(ctx, c.ID, clients.Direct); err != nil {
			t.Fatal(err)
		}
		status, body := call(t, abase, http.MethodGet, "/admin/keys/"+c.ID+"/models", "", ck)
		if status != http.StatusOK {
			t.Fatalf("%s models: %d %s", name, status, body)
		}
		ids := []string{}
		for _, m := range decode[struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}](t, body).Models {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if ids := direct("tool"); !slices.Contains(ids, "gpt-6.1-sol") ||
		!slices.Contains(ids, "claude-sonnet-5") || slices.Contains(ids, "claude-haiku-5") {
		t.Errorf("a direct client's models = %v", ids)
	}

}
