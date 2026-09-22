package httpapi

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"claudication/internal/store"
)

// exportLimit bounds one download.
//
// Generous, because the point of a download is to take away more than a screen
// holds, and the whole retention window is a reasonable ask. Bounded anyway:
// this streams straight to the client, so the memory cost is one page at a
// time, but an unbounded export is still a way to make the gateway read its
// entire history because somebody left a tab open.
const exportLimit = 100_000

// exportPage is how many rows are read per round trip. Large enough that a
// hundred thousand rows is two hundred queries rather than two thousand.
const exportPage = 500

// handleExportRequests streams the request log as CSV, narrowed the same way
// the screen is.
//
// The same filter as the table it is downloaded from, read from the same query
// string: what arrives in the file is what was on the screen, which is the
// only version of this that does not need explaining. And it is the whole
// filtered set rather than the rows that happen to be loaded — the table holds
// fifty at a time and the filter spans everything kept.
func (s *Server) handleExportRequests(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Usage.Enabled() {
		writeError(w, http.StatusNotFound, "not_found", "request history is not being recorded")
		return
	}

	filter := requestFilterFrom(r.URL.Query())

	// Named for the moment it was taken, because a directory of files called
	// requests.csv is a directory of one useful file.
	name := fmt.Sprintf("requests-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// Written as it is read, so a large export does not sit in memory waiting
	// to be complete before any of it is sent.
	w.WriteHeader(http.StatusOK)

	out := csv.NewWriter(w)
	defer out.Flush()

	_ = out.Write([]string{
		"at", "status", "forwarded", "outcome", "model", "chat", "client",
		"key", "account", "ip", "path", "streaming",
		"input_tokens", "output_tokens", "cache_tokens", "duration_ms", "error",
	})

	var cursor store.UsageCursor
	for written := 0; written < exportLimit; {
		events, next, err := s.store.RecentUsage(r.Context(), exportPage, cursor, filter)
		if err != nil {
			// The header and some rows are already sent, so there is no status
			// left to change: stop, flush what there is, and say why in the
			// log. A short file beats a truncated one that claims success, and
			// the row count is the tell.
			s.log.Error("export requests", "err", err, "rows", written)
			return
		}
		for _, e := range events {
			_ = out.Write(exportRow(e))
			written++
		}
		if next.ID == 0 || len(events) == 0 {
			return
		}
		cursor = next
		// Flushed per page rather than at the end, so a slow reader gets rows
		// while the rest are still being read.
		out.Flush()
		if err := out.Error(); err != nil {
			return
		}
	}
}

func exportRow(e store.UsageEvent) []string {
	forwarded := "yes"
	if e.Rejected {
		forwarded = "no"
	}
	return []string{
		e.At.UTC().Format(time.RFC3339),
		strconv.Itoa(e.Status),
		forwarded,
		e.ErrorCode,
		e.Model,
		e.ConversationID,
		e.Client,
		e.KeyName,
		e.AccountEmail,
		e.IP,
		e.Path,
		strconv.FormatBool(e.Streaming),
		strconv.Itoa(e.InputTokens),
		strconv.Itoa(e.OutputTokens),
		strconv.Itoa(e.CacheReadTokens + e.CacheWriteTokens),
		strconv.FormatInt(e.Duration.Milliseconds(), 10),
		e.Error,
	}
}
