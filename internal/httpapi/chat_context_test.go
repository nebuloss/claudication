package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"claudication/internal/store"
)

// A long crush session, as the gateway sees it: the main model's context
// grows, the client compacts, it grows again; the small model titles it
// on the way. The list says where it stands and the detail says when it fell,
// and the two agree.
func TestChatContextOverTheAPI(t *testing.T) {
	srv, st, _ := newTestServer(t)
	base, c := adminAPI(t, srv)
	start := time.Now().Add(-time.Hour)

	for i, tr := range []struct {
		model  string
		prompt int
	}{
		{"claude-haiku-4-5", 1_200},
		{"claude-opus-5", 200_000},
		{"claude-opus-5", 580_000},
		{"claude-opus-5", 60_000}, // compacted
		{"claude-opus-5", 150_000},
	} {
		if err := st.RecordUsage(context.Background(), store.UsageEvent{
			At: start.Add(time.Duration(i) * time.Minute), ConversationID: "ses_1",
			Client: "crush", Model: tr.model, Path: "/v1/messages", Status: 200,
			InputTokens: 10, CacheReadTokens: tr.prompt - 10,
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, body := call(t, base, http.MethodGet, "/admin/chats?days=1", "", c)
	list := decode[struct {
		Report store.ChatReport `json:"report"`
	}](t, body).Report.Chats
	if len(list) != 1 {
		t.Fatalf("chats = %+v", list)
	}
	got := list[0].Context
	if got.Model != "claude-opus-5" || got.Now != 150_000 || got.Peak != 580_000 || got.Compactions != 1 {
		t.Errorf("list context = %+v", got)
	}

	status, body := call(t, base, http.MethodGet, "/admin/chats/ses_1", "", c)
	if status != http.StatusOK {
		t.Fatalf("detail: %d %s", status, body)
	}
	detail := decode[struct {
		Context store.ChatContext `json:"context"`
	}](t, body).Context
	if detail.Now != got.Now || detail.Peak != got.Peak || len(detail.CompactedAt) != 1 ||
		!detail.CompactedAt[0].Equal(start.Add(3*time.Minute)) {
		t.Errorf("detail context = %+v, want the list's figures and the fall at minute 3", detail)
	}
}
