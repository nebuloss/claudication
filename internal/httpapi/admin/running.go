package admin

import (
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
)

// handleRunning is the requests the gateway is serving now, the
// longest-running first, for the request log to show above what it has
// recorded: a request is only in the log once it ends. ?key= keeps one
// client's. now is the gateway's clock, which each row's time running is
// counted on.
func (s *Admin) handleRunning(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"running": s.running.List(r.URL.Query().Get("key")),
		"now":     time.Now().UTC(),
	})
}
