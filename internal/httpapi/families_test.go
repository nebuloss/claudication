package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"claudication/internal/httpapi/admin"
	"claudication/internal/service/clients"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/store"
)

// twoPlans is a gateway with two Claude accounts on different plans: the
// first in the operator's order lists only claude-opus-5, the second the
// newer claude-opus-5-5 too, as a plan that has the latest does. Each
// version's output cap differs, so the clamp shows which version a request
// was fitted for.
func twoPlans(t *testing.T) (srv *Server, st *store.Store, base string, older, newer store.Account, up *upstreamStub) {
	t.Helper()
	srv, st, _ = newTestServer(t)
	up = &upstreamStub{}
	lists := map[string]string{
		"Bearer older-access": `{"data":[
			{"type":"model","id":"claude-opus-5","created_at":"2026-05-01T00:00:00Z","max_input_tokens":200000,"max_tokens":32000},
			{"type":"model","id":"claude-haiku-5","created_at":"2026-08-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}],"has_more":false}`,
		"Bearer newer-access": `{"data":[
			{"type":"model","id":"claude-opus-5-5","created_at":"2026-10-01T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000},
			{"type":"model","id":"claude-opus-5","created_at":"2026-05-01T00:00:00Z","max_input_tokens":200000,"max_tokens":32000},
			{"type":"model","id":"claude-haiku-5","created_at":"2026-08-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}],"has_more":false}`,
	}
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.record(r)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, lists[r.Header.Get("Authorization")])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicStream)
	}))
	t.Cleanup(claude.Close)
	srv.relay.BaseURL = claude.URL
	ctx := context.Background()
	var err error
	if older, err = st.UpsertAccount(ctx, store.Account{Provider: "anthropic", Email: "older@example.com",
		ExpiresAt: time.Now().Add(time.Hour)}, store.Tokens{AccessToken: "older-access", RefreshToken: "r1"}); err != nil {
		t.Fatal(err)
	}
	if newer, err = st.UpsertAccount(ctx, store.Account{Provider: "anthropic", Email: "newer@example.com",
		ExpiresAt: time.Now().Add(time.Hour)}, store.Tokens{AccessToken: "newer-access", RefreshToken: "r2"}); err != nil {
		t.Fatal(err)
	}
	b, cancel, done := startServer(t, srv)
	t.Cleanup(func() { cancel(); <-done })
	return srv, st, b, older, newer, up
}

// lastTurn is the model and output budget the upstream was last sent, and
// with whose token.
func lastTurn(t *testing.T, up *upstreamStub) (model string, maxTokens int, auth string) {
	t.Helper()
	turns := up.turns()
	if len(turns) == 0 {
		t.Fatal("nothing reached the upstream")
	}
	last := turns[len(turns)-1]
	var b struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	_ = json.Unmarshal(last.body, &b)
	return b.Model, b.MaxTokens, last.header.Get("Authorization")
}

// A family is resolved per account at request time: the account with the
// newest version serves it while it can, an account on a plan without the
// newest serves the newest it has rather than the request being refused, the
// budget is fitted to the cap of the version actually sent, the request log
// records that version, and a request is refused only when no account has
// any version.
func TestFamilyServedPerAccount(t *testing.T) {
	srv, st, base, older, newer, up := twoPlans(t)
	ctx := context.Background()
	_, key := newClient(t, st, "box", "crush", nil) // large follows claude:opus
	ask := func() (*http.Response, []byte) {
		t.Helper()
		return send(t, base+"/v1/messages", key, "Charm Crush/0.9",
			`{"model":"large","max_tokens":100000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	}

	// Both serve Opus: the newer plan's newest is preferred, though that
	// account is second in the operator's order, and the budget fits it.
	resp, body := ask()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("large: %d %s", resp.StatusCode, body)
	}
	if m, mt, auth := lastTurn(t, up); m != "claude-opus-5-5" || mt != 100000 || auth != "Bearer newer-access" {
		t.Errorf("sent %s, max_tokens %d, as %s; want claude-opus-5-5 unclamped on the newer plan", m, mt, auth)
	}
	if e := nthEvent(t, st, 1); e.Model != "claude-opus-5-5" || e.VirtualModel != "large" || e.AccountID != newer.ID {
		t.Errorf("recorded %s for %s on %s", e.Model, e.VirtualModel, e.AccountEmail)
	}

	// The newer plan switched off: the older one serves Opus with the one it
	// has, its budget fitted to that version's cap.
	if err := st.SetAccountDisabled(ctx, newer.ID, true); err != nil {
		t.Fatal(err)
	}
	resp, body = ask()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("large on the older plan alone: %d %s", resp.StatusCode, body)
	}
	if m, mt, auth := lastTurn(t, up); m != "claude-opus-5" || mt != 32000 || auth != "Bearer older-access" {
		t.Errorf("sent %s, max_tokens %d, as %s; want claude-opus-5 fitted to 32000 on the older plan", m, mt, auth)
	}
	if e := nthEvent(t, st, 2); e.Model != "claude-opus-5" || e.AccountID != older.ID {
		t.Errorf("recorded %s on %s, want claude-opus-5 on the older plan", e.Model, e.AccountEmail)
	}

	// No account has any Opus on: refused, saying so.
	if err := st.SetAccountModel(ctx, older.ID, "claude-opus-5", false); err != nil {
		t.Fatal(err)
	}
	if resp, body := ask(); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "Claude Opus") {
		t.Errorf("no Opus anywhere: %d %s", resp.StatusCode, body)
	}
	_ = srv
}

// What a client is offered of a family carries the facts every version that
// may serve it fits: the smallest window and output limit among them.
func TestFamilyFactsAreTheSmallest(t *testing.T) {
	_, st, base, _, _, _ := twoPlans(t)
	_, key := newClient(t, st, "box", "crush", nil)
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("X-Api-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var l struct {
		Data []struct {
			ID        string `json:"id"`
			Serving   string `json:"serving"`
			Window    int    `json:"max_input_tokens"`
			MaxTokens int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(l.Data, func(e struct {
		ID        string `json:"id"`
		Serving   string `json:"serving"`
		Window    int    `json:"max_input_tokens"`
		MaxTokens int    `json:"max_tokens"`
	}) bool {
		return e.ID == "large"
	})
	if i < 0 {
		t.Fatalf("no large in %+v", l.Data)
	}
	if e := l.Data[i]; e.Serving != "claude-opus-5-5" || e.Window != 200000 || e.MaxTokens != 32000 {
		t.Errorf("large = %+v, want claude-opus-5-5's entry with claude-opus-5's window and output", e)
	}
}

// The Models settings page's report: each family with the version each
// account would serve, only what connected accounts list.
func TestFamilyReport(t *testing.T) {
	srv, st, _, older, newer, _ := twoPlans(t)
	abase, ck := adminAPI(t, srv)
	report := func() []modelsvc.FamilyReport {
		t.Helper()
		status, body := call(t, abase, http.MethodGet, "/admin/model-families", "", ck)
		if status != http.StatusOK {
			t.Fatalf("report: %d %s", status, body)
		}
		return decode[struct {
			Providers []modelsvc.FamilyReport `json:"providers"`
		}](t, body).Providers
	}
	rep := report()
	if len(rep) != 2 || rep[0].Provider != "anthropic" || !rep[0].Connected || !rep[0].ByFamily ||
		rep[1].Provider != "openai" || rep[1].Connected || len(rep[1].Families) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	var opus modelsvc.FamilyRow
	for _, f := range rep[0].Families {
		if f.Follows == clients.FollowOpus {
			opus = f
		}
	}
	if opus.Serving != "claude-opus-5-5" || len(opus.Versions) != 2 ||
		!slices.Equal(opus.Versions[0].ServedBy, []string{newer.ID}) ||
		!slices.Equal(opus.Versions[1].ServedBy, []string{older.ID}) ||
		!slices.Equal(opus.Versions[1].Listed, []string{older.ID, newer.ID}) {
		t.Errorf("opus = %+v", opus)
	}
	for _, a := range rep[0].Accounts {
		want := map[string]string{older.ID: "claude-opus-5", newer.ID: "claude-opus-5-5"}[a.ID]
		if a.Serves[clients.FollowOpus] != want {
			t.Errorf("%s serves Opus with %q, want %q", a.Name, a.Serves[clients.FollowOpus], want)
		}
	}

	// Off on every account that lists it: still there, marked, to be
	// switched back on.
	if err := st.SetAccountModel(context.Background(), newer.ID, "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}
	for _, f := range report()[0].Families {
		if f.Follows == clients.FollowOpus && (len(f.Versions) != 2 || !f.Versions[0].Off || f.Versions[1].Off) {
			t.Errorf("opus with the newest off everywhere = %+v", f)
		}
	}
}

// The mode decides what every listing offers: by family, the families, each
// version only to pin; every model, each version, with the family whose
// newest it is. A binding to a version stays valid either way.
func TestModeSwitchesEveryListing(t *testing.T) {
	srv, st, base, _, _, _ := twoPlans(t)
	abase, ck := adminAPI(t, srv)
	ctx := context.Background()
	c, _ := newClient(t, st, "box", "crush", map[string]store.Binding{"small": {Model: "claude-opus-5"}})
	direct, directKey := newClient(t, st, "tool", "custom", nil)
	if err := st.SetClientMode(ctx, direct.ID, clients.Direct); err != nil {
		t.Fatal(err)
	}
	client := func() admin.ClientJSON {
		t.Helper()
		_, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
		return decode[struct {
			Client admin.ClientJSON `json:"client"`
		}](t, body).Client
	}
	switches := func() []string {
		t.Helper()
		_, body := call(t, abase, http.MethodGet, "/admin/keys/"+direct.ID+"/models", "", ck)
		var ids []string
		for _, m := range decode[struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		}](t, body).Models {
			ids = append(ids, m.ID)
		}
		return ids
	}
	offered := func() []string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("X-Api-Key", directKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&l)
		var ids []string
		for _, m := range l.Data {
			ids = append(ids, m.ID)
		}
		return ids
	}

	// By family, the default.
	got := client()
	if v := choiceValues(got.Choices); !slices.Equal(v, []string{"follow:claude:opus=claude-opus-5-5", "follow:claude:haiku=claude-haiku-5"}) {
		t.Errorf("by family, the menu = %v, want the families and no version", v)
	}
	if small := got.Slots[1]; small.Model != "claude-opus-5" || small.Unavailable != "" {
		t.Errorf("a version bound before stays valid: %+v", small)
	}
	// And is not made anew: a name is bound to the family.
	if status, out := call(t, abase, http.MethodPut, "/admin/clients/"+c.ID+"/slots/large", `{"model":"claude-opus-5"}`, ck); status != http.StatusBadRequest ||
		!strings.Contains(string(out), "bound to Claude Opus") {
		t.Errorf("binding a version by family: %d %s", status, out)
	}
	if ids := switches(); !slices.Equal(ids, []string{"claude-opus", "claude-haiku"}) {
		t.Errorf("by family, the client's switches = %v", ids)
	}
	if ids := offered(); !slices.Equal(ids, []string{"claude-opus", "claude-haiku"}) {
		t.Errorf("by family, a direct client's /v1/models = %v", ids)
	}
	// A family named by its id is served per account, as a slot's is.
	resp, body := send(t, base+"/v1/messages", directKey, "curl/8",
		`{"model":"claude-opus","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"claude-opus"`) {
		t.Errorf("a direct request for claude-opus: %d %s", resp.StatusCode, body)
	}

	// Every model, set as the page sets it.
	if status, body := call(t, abase, http.MethodPost, "/admin/model-families/anthropic/mode", `{"enabled":false}`, ck); status != http.StatusOK {
		t.Fatalf("mode: %d %s", status, body)
	}
	got = client()
	var models []string
	for _, ch := range got.Choices {
		if ch.Follows != "" {
			t.Errorf("every model, yet a family: %+v", ch)
		}
		models = append(models, ch.Model)
	}
	if status, out := call(t, abase, http.MethodPut, "/admin/clients/"+c.ID+"/slots/small", `{"follows":"claude:haiku"}`, ck); status != http.StatusBadRequest ||
		!strings.Contains(string(out), "offers every Claude model") {
		t.Errorf("binding a family where every model is offered: %d %s", status, out)
	}
	slices.Sort(models)
	if !slices.Equal(models, []string{"claude-haiku-5", "claude-opus-5", "claude-opus-5-5"}) {
		t.Errorf("every model: choices %v", models)
	}
	if ids := switches(); !slices.Contains(ids, "claude-opus-5-5") || slices.Contains(ids, "claude-opus") {
		t.Errorf("every model, the client's switches = %v", ids)
	}
	if ids := offered(); !slices.Contains(ids, "claude-opus-5") || slices.Contains(ids, "claude-opus") {
		t.Errorf("every model, a direct client's /v1/models = %v", ids)
	}
	// Stored: what the next start reads.
	if v, ok, _ := st.Setting(ctx, modelsvc.ModeSetting("anthropic")); !ok || v != "false" {
		t.Errorf("the mode stored as %q, %v", v, ok)
	}
}

// Which version a family serves is the gateway's, set once: following the
// latest, each account serves its newest; set to one version, every request
// for the family is sent it, by the accounts that have it, and an account
// without it does not serve the family.
func TestFamilyVersionSetGlobally(t *testing.T) {
	srv, st, base, older, newer, up := twoPlans(t)
	abase, ck := adminAPI(t, srv)
	ctx := context.Background()
	_, key := newClient(t, st, "box", "crush", nil) // large follows claude:opus
	ask := func() (*http.Response, []byte) {
		t.Helper()
		return send(t, base+"/v1/messages", key, "Charm Crush/0.9",
			`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	}
	setVersion := func(model string) (int, []byte) {
		t.Helper()
		return call(t, abase, http.MethodPost, "/admin/model-families/version", `{"family":"claude:opus","model":"`+model+`"}`, ck)
	}

	// Set to the older version: the newer plan serves it too, first in the
	// operator's order being the older plan.
	if status, body := setVersion("claude-opus-5"); status != http.StatusOK {
		t.Fatalf("set: %d %s", status, body)
	}
	if resp, body := ask(); resp.StatusCode != http.StatusOK {
		t.Fatalf("large: %d %s", resp.StatusCode, body)
	}
	if m, _, auth := lastTurn(t, up); m != "claude-opus-5" || auth != "Bearer older-access" {
		t.Errorf("set to claude-opus-5: sent %s as %s", m, auth)
	}
	if err := st.SetAccountDisabled(ctx, older.ID, true); err != nil {
		t.Fatal(err)
	}
	if resp, body := ask(); resp.StatusCode != http.StatusOK {
		t.Fatalf("large on the newer plan: %d %s", resp.StatusCode, body)
	}
	if m, _, auth := lastTurn(t, up); m != "claude-opus-5" || auth != "Bearer newer-access" {
		t.Errorf("set to claude-opus-5, the newer plan alone: sent %s as %s, want claude-opus-5 though it has 5.5", m, auth)
	}

	// Set to the newest: the older plan, which lacks it, serves no Opus.
	if err := st.SetAccountDisabled(ctx, older.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountDisabled(ctx, newer.ID, true); err != nil {
		t.Fatal(err)
	}
	if status, body := setVersion("claude-opus-5-5"); status != http.StatusOK {
		t.Fatalf("set: %d %s", status, body)
	}
	if resp, body := ask(); resp.StatusCode != http.StatusNotFound {
		t.Errorf("set to a version only the paused plan has: %d %s", resp.StatusCode, body)
	}
	var rep []modelsvc.FamilyReport
	_, body := call(t, abase, http.MethodGet, "/admin/model-families", "", ck)
	rep = decode[struct {
		Providers []modelsvc.FamilyReport `json:"providers"`
	}](t, body).Providers
	for _, f := range rep[0].Families {
		if f.Follows == clients.FollowOpus && (f.Pinned != "claude-opus-5-5" || f.Serving != "" || f.Why == "") {
			t.Errorf("opus set to the newest, its plan paused: %+v", f)
		}
	}
	for _, a := range rep[0].Accounts {
		if a.ID == older.ID && a.Serves[clients.FollowOpus] != "" {
			t.Errorf("the older plan serves Opus with %q, set to a version it lacks", a.Serves[clients.FollowOpus])
		}
	}

	// Back to the latest: the older plan serves its own.
	if status, body := setVersion(""); status != http.StatusOK {
		t.Fatalf("set: %d %s", status, body)
	}
	if resp, body := ask(); resp.StatusCode != http.StatusOK {
		t.Fatalf("large following the latest: %d %s", resp.StatusCode, body)
	}
	if m, _, _ := lastTurn(t, up); m != "claude-opus-5" {
		t.Errorf("following the latest on the older plan: sent %s", m)
	}
	// Only a version an account lists, of that family; stored for the next
	// start.
	if status, body := setVersion("claude-haiku-5"); status != http.StatusBadRequest {
		t.Errorf("a version of another family: %d %s", status, body)
	}
	if status, _ := setVersion("claude-opus-5"); status != http.StatusOK {
		t.Fatal("set")
	}
	if v, ok, _ := st.Setting(ctx, modelsvc.PinSetting(clients.FollowOpus)); !ok || v != "claude-opus-5" {
		t.Errorf("stored as %q, %v", v, ok)
	}
}

// A family switched off on a client is out of every path, as a model is:
// the menu, the request, and the agent's list.
func TestFamiliesBoundEveryPath(t *testing.T) {
	srv, st, base, _, _, _ := twoPlans(t)
	abase, ck := adminAPI(t, srv)
	ctx := context.Background()
	c, key := newClient(t, st, "box", "crush", nil)
	if err := st.SetKeyModel(ctx, c.ID, "claude-opus", false); err != nil {
		t.Fatal(err)
	}
	_, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
	got := decode[struct {
		Client admin.ClientJSON `json:"client"`
	}](t, body).Client
	if v := choiceValues(got.Choices); slices.Contains(v, "follow:claude:opus=claude-opus-5-5") {
		t.Errorf("a family not allowed offered: %v", v)
	}
	if large := got.Slots[0]; large.Unavailable != "not allowed for this client" {
		t.Errorf("large, following a family not allowed: %+v", large)
	}
	resp, out := send(t, base+"/v1/messages", key, "Charm Crush/0.9",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(out), "not allowed") {
		t.Errorf("a request for large: %d %s", resp.StatusCode, out)
	}
	// A version off leaves the family on the others: the client is served
	// the version it may have.
	if err := st.SetKeyModel(ctx, c.ID, "claude-opus", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKeyModel(ctx, c.ID, "claude-opus-5-5", false); err != nil {
		t.Fatal(err)
	}
	if resp, out := send(t, base+"/v1/messages", key, "Charm Crush/0.9",
		`{"model":"large","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("large with the newest not allowed: %d %s", resp.StatusCode, out)
	}
	if e := nthEvent(t, st, 2); e.Model != "claude-opus-5" {
		t.Errorf("served %s, want claude-opus-5: the newest is not allowed", e.Model)
	}
}
