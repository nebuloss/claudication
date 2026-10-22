package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"claudication/internal/store"
)

// The model catalogue: every model of every provider, from models.dev by
// default (config models-catalog), and the source of what the gateway says
// about a model — its name, release date, context window and output limit.
//
// The accounts say which of those models are served. The gateway's model list
// is the catalogue matched against what the connected accounts list, less
// what each account's switches turn off — so it reads the same whichever
// provider an account is on, rather than in each provider's own words.
//
// Two edges, both because a provider and the catalogue are not updated
// together:
//
//   - A model an account lists and the catalogue does not know yet is still
//     listed, as the account describes it, after the catalogue's: a model
//     released this morning is servable this morning.
//   - With no account readable there is nothing to match, and the gateway
//     lists the catalogue's Anthropic models as they stand. Anthropic's
//     /v1/models answers 401 "x-api-key header is required" to a caller with
//     no account token, so without this a gateway with no account yet had no
//     model list, nothing for a client's names to serve and no configs to
//     hand out.
//
// Measured 2026-10-15: models.dev's api.json is 5.4 MB, every provider, with
// 17 Anthropic entries — some twice, under an alias and a dated id
// (claude-haiku-4-5 and claude-haiku-4-5-20251001), which are one model here.

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

// catalogCache is the catalogue as last read, and the read in flight.
type catalogCache struct {
	mu sync.Mutex
	// models is each provider's models by the provider's models.dev id —
	// "anthropic" and "openai", the gateway's own provider ids — newest release first.
	models map[string][]catalogModel
	at     time.Time     // last read finished, successful or not
	done   chan struct{} // closed when the read in flight finishes; nil when none is
}

// catalogModels is one provider's models in the catalogue, newest release
// first, and nil when it is off, has never been read, or lists none.
//
// A stale catalogue is answered as it stands while a fresh read runs. Only
// when there is nothing yet does a caller wait for the read, and no longer
// than its own context allows.
func (s *Catalog) catalogModels(ctx context.Context, provider string) []catalogModel {
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
	read, done := c.models != nil, c.done
	models := c.models[provider]
	c.mu.Unlock()
	if read || done == nil {
		return models
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models[provider]
}

// readCatalog reads the catalogue once and keeps what it found. A failed
// read keeps what an earlier one found.
func (s *Catalog) readCatalog(url string, done chan struct{}) {
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
	}
	c.at, c.done = time.Now(), nil
	close(done)
}

// fetchCatalog fetches the catalogue.
func (s *Catalog) fetchCatalog(ctx context.Context, url string) (map[string][]catalogModel, error) {
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
	return parseCatalog(io.LimitReader(resp.Body, 32<<20))
}

// parseCatalog reads the models.dev shape — providers by id, each with its
// models by id — into each provider's models, newest release first, then by
// id.
func parseCatalog(r io.Reader) (map[string][]catalogModel, error) {
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
	out := make(map[string][]catalogModel, len(doc))
	for provider, p := range doc {
		var models []catalogModel
		for id, m := range p.Models {
			if m.ID == "" {
				m.ID = id
			}
			created := ""
			if t, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
				created = t.UTC().Format(time.RFC3339)
			}
			models = append(models, catalogModel{
				ID: m.ID, DisplayName: shownName(m.Name), CreatedAt: created,
				MaxInput: m.Limit.Context, MaxOutput: m.Limit.Output,
			})
		}
		sort.Slice(models, func(i, j int) bool {
			if models[i].CreatedAt != models[j].CreatedAt {
				return models[i].CreatedAt > models[j].CreatedAt
			}
			return models[i].ID < models[j].ID
		})
		out[provider] = models
	}
	if len(out["anthropic"]) == 0 {
		return nil, fmt.Errorf("catalogue lists no Anthropic models")
	}
	return out, nil
}

// shownName is a catalogue name as the gateway shows it. The catalogue names
// a dated snapshot "Claude Opus 4.5 (latest)" — the latest snapshot of 4.5 —
// and that name replaces the upstream's own, so every menu and config said
// "latest" beside Opus 5.5, which reads as the newest model when it is three
// releases old. The suffix says nothing the id and the date do not.
func shownName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(strings.ToLower(name), "(latest)"); i >= 0 && strings.TrimSpace(name[i+len("(latest)"):]) == "" {
		return strings.TrimSpace(name[:i])
	}
	return name
}

// catalogList is a provider's catalogue as a model list, unmatched — what is
// listed while no account can be asked.
func catalogList(models []catalogModel) []byte {
	seen := map[string]bool{}
	data := []map[string]any{}
	for _, m := range models {
		if k := store.ModelKey(m.ID); !seen[k] {
			seen[k] = true
			data = append(data, describe(map[string]any{"type": "model", "id": m.ID}, m))
		}
	}
	return modelList(data)
}

// describe puts the catalogue's facts on a model list entry. The catalogue's
// name and release date are the ones shown; a limit it does not know leaves
// the entry's own.
func describe(e map[string]any, m catalogModel) map[string]any {
	if m.DisplayName != "" {
		e["display_name"] = m.DisplayName
	}
	if m.CreatedAt != "" {
		e["created_at"] = m.CreatedAt
	}
	if m.MaxInput > 0 {
		e["max_input_tokens"] = m.MaxInput
	}
	if m.MaxOutput > 0 {
		e["max_tokens"] = m.MaxOutput
	}
	return e
}

// modelList is entries as a whole model list, its paging fields true of it.
func modelList(data []map[string]any) []byte {
	list := map[string]any{"data": data, "has_more": false}
	if len(data) > 0 {
		list["first_id"] = data[0]["id"]
		list["last_id"] = data[len(data)-1]["id"]
	}
	out, _ := json.Marshal(list)
	return out
}

// matchCatalog is the catalogue's models of a provider that an account list
// names, in the catalogue's order and with its facts, under the id the
// account uses — the one its upstream answers to. Models the list names and
// the catalogue does not know follow, as the list has them. With no
// catalogue the list is returned as it was.
func (s *Catalog) matchCatalog(ctx context.Context, provider string, body []byte) []byte {
	models := s.catalogModels(ctx, provider)
	if models == nil {
		return body
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil {
		return body
	}
	listed := map[string]map[string]any{}
	for _, e := range list.Data {
		if id, _ := e["id"].(string); id != "" {
			if k := store.ModelKey(id); listed[k] == nil {
				listed[k] = e
			}
		}
	}
	data := make([]map[string]any, 0, len(listed))
	used := map[string]bool{}
	for _, m := range models {
		k := store.ModelKey(m.ID)
		if e := listed[k]; e != nil && !used[k] {
			used[k] = true
			data = append(data, describe(e, m))
		}
	}
	for _, e := range list.Data {
		id, _ := e["id"].(string)
		if k := store.ModelKey(id); id != "" && !used[k] {
			used[k] = true
			data = append(data, e)
		}
	}
	return modelList(data)
}
