package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudication/internal/pool"
	"claudication/internal/provider"
	"claudication/internal/provider/anthropic"
	"claudication/internal/request"
	"claudication/internal/store"
)

// versionPool hands out two accounts in turn, each serving its own version of
// a family, as pool.Acquire does for a request marked WithVersions.
type versionPool struct{}

func (versionPool) Acquire(_ context.Context, _, _ string, exclude map[string]bool) (pool.Lease, error) {
	switch {
	case !exclude["newer"]:
		return pool.Lease{Account: store.Account{ID: "newer"}, AccessToken: "t1", Model: "claude-opus-5-5"}, nil
	case !exclude["older"]:
		return pool.Lease{Account: store.Account{ID: "older"}, AccessToken: "t2", Model: "claude-opus-5"}, nil
	}
	return pool.Lease{}, pool.ErrAllCoolingUp
}
func (versionPool) ReportFailure(string, pool.FailureKind, string)                              {}
func (versionPool) ReportFailureUntil(string, pool.FailureKind, string, time.Time)              {}
func (versionPool) ReportModelFailureUntil(string, string, pool.FailureKind, string, time.Time) {}
func (versionPool) NoteQuota(string, provider.Quota)                                            {}
func (versionPool) ReportSuccess(string)                                                        {}
func (versionPool) Refresh(context.Context, string) error                                       { return nil }

// Each attempt sends the version its account serves, rebuilt from the passes'
// body rather than the previous attempt's, and the result says which version
// answered: what the request log records.
func TestEachAttemptSendsItsAccountsVersion(t *testing.T) {
	var sent []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.Header().Set("Content-Type", "application/json")
		if len(sent) == 1 {
			// Overloaded: retried on the next account.
			w.WriteHeader(529)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	defer upstream.Close()

	r := &Relay{Wire: anthropic.Provider{}, Pool: versionPool{}, Client: upstream.Client(), BaseURL: upstream.URL}
	var rebound []string
	ctx := WithRebind(context.Background(), func(body []byte, model string) []byte {
		rebound = append(rebound, string(body))
		return []byte(`{"model":"` + model + `"}`)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).WithContext(ctx)
	res := r.Do(httptest.NewRecorder(), req, "/v1/messages", []byte(`{"model":"opus"}`), request.Prologue{Model: "opus"})

	if res.Status != http.StatusOK || res.Model != "claude-opus-5" || res.AccountID != "older" {
		t.Fatalf("result: status %d, model %q, account %q; want 200 from the older plan's claude-opus-5",
			res.Status, res.Model, res.AccountID)
	}
	want := []string{`{"model":"claude-opus-5-5"}`, `{"model":"claude-opus-5"}`}
	if len(sent) != 2 || sent[0] != want[0] || sent[1] != want[1] {
		t.Errorf("sent %q, want %q", sent, want)
	}
	for i, b := range rebound {
		if b != `{"model":"opus"}` {
			t.Errorf("attempt %d was rebound from %s, want the passes' body", i+1, b)
		}
	}
}
