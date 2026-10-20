package admin

import (
	"context"
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/settings"
)

// handleConfig reports every configuration value and where it came from.
//
// Read-only, and it stays that way. The config file is never written back —
// that is the rule this package's config loader exists to enforce, because an
// agent that re-serialises a YAML file destroys every comment the operator
// wrote in it. What this screen can do instead is answer the question that
// actually costs time: not "what is this set to" but "why is it that, and
// which of the three places do I change".
func (s *Admin) handleConfig(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"path":     s.cfg.Path,
		"settings": s.cfg.Settings(),
		// Reported alongside the file-and-environment settings rather than on
		// a screen of their own: they are configuration an operator reasons
		// about together with the rest, and separating them is how a switch
		// gets flipped and then lost.
		"surfaces": s.surfaces.State(),
		// Beside the surfaces for the same reason, and because it is the one
		// switch that decides whether the gateway spends the operator's
		// subscription on its own behalf. That belongs where they are already
		// looking at what this gateway is allowed to do.
		"chat_titles":         s.titles.On(),
		"chat_titles_capture": s.titles.Capturing(),
		"fit_images":          s.images.On(),
	})
}

// handleAdminModels lists the models the connected accounts can actually serve.
//
// The same upstream list /v1/models proxies, but reachable with an admin
// session instead of an API key, because the Setup screen needs it and the
// browser has a cookie rather than a key. Reusing the same list keeps one answer
// to "which models exist" rather than a second, drifting copy of it in the UI.
func (s *Admin) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	body, err := s.models.List(ctx)
	if err != nil {
		s.log.Warn("could not read the upstream model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream,
			"could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// handleSetSurface turns one client-facing API on or off.
//
// It takes effect on the next request — there is no restart and nothing to
// drain — and it is allowed to leave every surface off. That state stops the
// gateway serving anything to anyone, which is the point of having the switch
// at all, so it is not second-guessed here.
func (s *Admin) handleSetSurface(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.protocols.Find(id)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, httpx.KindNotFound, "no such API surface")
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpx.DecodeJSONLimit(w, r, &body, 1<<10, switchUsage) {
		return
	}
	if body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, "enabled is required")
		return
	}

	if err := s.surfaces.Set(r.Context(), id, *body.Enabled); err != nil {
		s.log.Error("could not store the API surface switch", "surface", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the setting")
		return
	}
	s.log.Warn("API surface switched", "surface", id, "title", p.Title(), "enabled", *body.Enabled)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"surfaces": s.surfaces.State()})
}

// switchUsage is what an on/off endpoint says to a body of the wrong shape.
const switchUsage = `expected {"enabled": true|false}`

// switchHandler serves one runtime switch: it stores the new state, logs it,
// and answers it back under field, which is the name the config screen reads
// it by.
//
// One handler for every switch that is only on or off, because each used to
// be its own copy of this — decode, store, log, answer — and copies are where
// one of them stops logging, or answers under a different name.
func (s *Admin) switchHandler(sw *settings.Switch, what, field string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.DecodeJSONLimit(w, r, &body, 1<<10, switchUsage) {
			return
		}
		if body.Enabled == nil {
			httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, switchUsage)
			return
		}
		if err := sw.Set(r.Context(), *body.Enabled); err != nil {
			s.log.Error("could not store the "+what+" switch", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, httpx.KindInternal, "could not store the setting")
			return
		}
		s.log.Warn(what+" switched", "enabled", *body.Enabled)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{field: sw.On()})
	}
}
