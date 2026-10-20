package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"claudication/internal/service/uptime"
)

// The state the uptime record takes from the gateway: degraded with no
// account able to serve, up with one, paused with the switch off whatever the
// accounts.
func TestUptimeStateFollowsTheSwitchAndThePool(t *testing.T) {
	srv, st, _ := newTestServer(t)
	ctx := context.Background()
	if got := srv.uptimeState(ctx); got != uptime.Degraded {
		t.Errorf("no accounts: %s, want degraded", got)
	}
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	if got := srv.uptimeState(ctx); got != uptime.Up {
		t.Errorf("an account: %s, want up", got)
	}
	if err := srv.serving.Set(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := srv.uptimeState(ctx); got != uptime.Paused {
		t.Errorf("switched off: %s, want paused", got)
	}
}

// /status.json is on the relay-only listener, unauthenticated, cacheable for
// a minute, and carries exactly the report's keys: nothing a listener adds on
// the way out, and nothing the report did not mean to publish.
func TestStatusEndpointPublishesOnlyTheReport(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if err := srv.uptime.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes(role{gateway: true}))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/status.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"days", "generated", "hours", "incidents", "state", "uptime"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if string(body["state"]) != `"degraded"` {
		t.Errorf("state = %s, want degraded with no account", body["state"])
	}
}
