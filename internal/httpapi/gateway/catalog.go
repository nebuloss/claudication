package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"claudication/internal/store"
)

// The public model catalogue: models.dev by default (config models-catalog).
//
// Anthropic's /v1/models needs an account's token — asked with none it
// answers 401 "x-api-key header is required" — so a gateway whose accounts
// cannot be read had no model list at all, and with it no aliases, no client
// configs and an empty docs page. models.dev publishes the same models with
// no key: measured 2026-10-15, 17 Anthropic entries, each with an id, a name,
// a release date and its context and output limits. That is what a model
// list is used for here, so it stands in until an account answers.
//
// It never decides what is served. A model the catalogue knows and no
// account lists is not offered once an account's list can be read; the
// catalogue only fills in what that list leaves out — a context window, a
// release date — for an entry it has.

// catalogTTL is how long a catalogue read is kept. It changes when a model is
// released, every few months.
const catalogTTL = 6 * time.Hour

// catalogRetry is how long a failed read stands before the next is tried, so
// an unreachable catalogue costs one timeout per period and not one per
// model list.
const catalogRetry = 10 * time.Minute

// catalogWait bounds a read. It runs on its own, not on the request that
// found the catalogue stale: /v1/models answers inside the client's 3-second
// budget, and the catalogue is megabytes.
const catalogWait = 30 * time.Second

// catalogModel is one catalogue entry, in what a model list needs.
type catalogModel struct {
	ID          string
	DisplayName string
	CreatedAt   string // RFC 3339, from the release date
	MaxInput    int
	MaxOutput   int
}

type catalog struct {
	mu     sync.Mutex
	models []catalogModel // newest release first
	byKey  map[string]catalogModel
	at     time.Time     // last read finished, successful or not
	done   chan struct{} // closed when the read in flight finishes; nil when none is
}

// catalogModels is the catalogue's Anthropic models, newest release first,
// and nil when it is off or nothing has been read.
//
// A stale catalogue is answered as it stands while a fresh read runs. Only
// when there is nothing yet does a caller wait for the read, and no longer
// than its own context allows.
func (s *Gateway) catalogModels(ctx context.Context) []catalogModel {
	url := s.cfg.ModelsCatalog
	if url == "" || url == "off" {
		return nil
	}
	c := &s.catalog
	c.mu.Lock()
	due := catalogTTL
	if c.models == nil {
		due = catalogRetry
	}
	if c.done == nil && (c.at.IsZero() || time.Since(c.at) >= due) {
		c.done = make(chan struct{})
		go s.readCatalog(url, c.done)
	}
	models, done := c.models, c.done
	c.mu.Unlock()
	if models != nil || done == nil {
		return models
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models
}

// readCatalog reads the catalogue once and keeps what it found. A failed
// read keeps what an earlier one found.
func (s *Gateway) readCatalog(url string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), catalogWait)
	defer cancel()
	models, err := s.fetchCatalog(ctx, url)
	if err != nil {
		s.log.Warn("model catalogue unreadable", "url", url, "err", err)
	}
	c := &s.catalog
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.models = models
		c.byKey = make(map[string]catalogModel, len(models))
		for _, m := range models {
			if k := store.ModelKey(m.ID); c.byKey[k].ID == "" {
				c.byKey[k] = m
			}
		}
	}
	c.at, c.done = time.Now(), nil
	close(done)
}

// catalogEntry is the catalogue's entry for a model, by the dated-suffix-free
// key, so claude-haiku-4-5-20251001 finds claude-haiku-4-5.
func (s *Gateway) catalogEntry(ctx context.Context, id string) (catalogModel, bool) {
	if s.catalogModels(ctx) == nil {
		return catalogModel{}, false
	}
	s.catalog.mu.Lock()
	defer s.catalog.mu.Unlock()
	m, ok := s.catalog.byKey[store.ModelKey(id)]
	return m, ok
}

// fetchCatalog fetches the catalogue and keeps its Anthropic models.
func (s *Gateway) fetchCatalog(ctx context.Context, url string) ([]catalogModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalogue returned %d", resp.StatusCode)
	}
	// The whole catalogue, every provider, was 5.4 MB when measured.
	return parseCatalog(io.LimitReader(resp.Body, 32<<20))
}

// parseCatalog reads the models.dev shape — providers by id, each with its
// models by id — and returns Anthropic's, newest release first, then by id.
func parseCatalog(r io.Reader) ([]catalogModel, error) {
	var doc map[string]struct {
		Models map[string]struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			ReleaseDate string `json:"release_date"`
			Limit       struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalogue: %w", err)
	}
	var out []catalogModel
	for id, m := range doc["anthropic"].Models {
		if m.ID == "" {
			m.ID = id
		}
		created := ""
		if t, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
			created = t.UTC().Format(time.RFC3339)
		}
		out = append(out, catalogModel{
			ID: m.ID, DisplayName: m.Name, CreatedAt: created,
			MaxInput: m.Limit.Context, MaxOutput: m.Limit.Output,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalogue lists no Anthropic models")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// catalogList is the catalogue as an Anthropic model list, one entry per
// model: models.dev lists claude-haiku-4-5 and claude-haiku-4-5-20251001
// both, which are one model to a client choosing from a menu.
func catalogList(models []catalogModel) []byte {
	type entry struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at,omitempty"`
		MaxInput    int    `json:"max_input_tokens,omitempty"`
		MaxOutput   int    `json:"max_tokens,omitempty"`
	}
	seen := map[string]bool{}
	data := []entry{}
	for _, m := range models {
		if k := store.ModelKey(m.ID); !seen[k] {
			seen[k] = true
			data = append(data, entry{"model", m.ID, m.DisplayName, m.CreatedAt, m.MaxInput, m.MaxOutput})
		}
	}
	list := map[string]any{"data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"] = data[0].ID
		list["last_id"] = data[len(data)-1].ID
	}
	out, _ := json.Marshal(list)
	return out
}

// withCatalog fills in, on each entry of an account's model list, the fields
// it leaves out and the catalogue has: its context window, its output limit
// and its release date. A field the account sent is never replaced. The
// list is returned as it was when nothing is added.
func (s *Gateway) withCatalog(ctx context.Context, body []byte) []byte {
	if s.catalogModels(ctx) == nil {
		return body
	}
	var list map[string]json.RawMessage
	if json.Unmarshal(body, &list) != nil {
		return body
	}
	var data []map[string]json.RawMessage
	if json.Unmarshal(list["data"], &data) != nil {
		return body
	}
	changed := false
	for _, m := range data {
		var id string
		if json.Unmarshal(m["id"], &id) != nil {
			continue
		}
		c, ok := s.catalogEntry(ctx, id)
		if !ok {
			continue
		}
		fill := func(field string, v any, empty bool) {
			if _, has := m[field]; has || empty {
				return
			}
			if raw, err := json.Marshal(v); err == nil {
				m[field] = raw
				changed = true
			}
		}
		fill("max_input_tokens", c.MaxInput, c.MaxInput == 0)
		fill("max_tokens", c.MaxOutput, c.MaxOutput == 0)
		fill("created_at", c.CreatedAt, c.CreatedAt == "")
	}
	if !changed {
		return body
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return body
	}
	list["data"] = raw
	out, err := json.Marshal(list)
	if err != nil {
		return body
	}
	return out
}
