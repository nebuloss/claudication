package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"claudication/internal/httpapi/httpx"
	"claudication/internal/service/clients"
	modelsvc "claudication/internal/service/models"
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
		"gateway_enabled": s.serving.On(),
		"mcp_enabled":     s.mcp.On(),
		// Whether the gateway checks models itself (internal/service/availability).
		"model_checks_enabled": s.modelChecks.On(),
	})
}

// handleModelFamilies is each provider's families: its mode, every family
// with its versions and the accounts listing each, and which version of each
// family every account would serve — the Models settings page, and an
// account's own page, which reads its row.
func (s *Admin) handleModelFamilies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rep, err := s.models.Report(ctx)
	if err != nil {
		httpx.Internal(w, s.log, "read the model families", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": rep})
}

// handleFamilyVersion sets which version a family serves, for the whole
// gateway: one the connected accounts list, or "" to follow the latest each
// account has. Answered with the report, as the page shows it after.
func (s *Admin) handleFamilyVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Family clients.Follow `json:"family"`
		Model  *string        `json:"model"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if body.Model == nil || !body.Family.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest,
			`expected {"family": "claude:opus", "model": "<a version>" or "" for the latest}`)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch err := s.models.SetPin(ctx, body.Family, *body.Model); {
	case errors.Is(err, modelsvc.ErrNotAVersion):
		httpx.WriteError(w, http.StatusBadRequest, httpx.KindInvalidRequest, err.Error())
		return
	case err != nil:
		httpx.Internal(w, s.log, "store the family's version", err)
		return
	}
	s.log.Warn("family version set", "family", body.Family, "model", *body.Model, "ip", httpx.ClientIP(r.Context()))
	s.handleModelFamilies(w, r)
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

	// The lists the gateway last read; ?fresh=1 reads every upstream now,
	// which is where an upstream that cannot be reached is said.
	list := s.models.List
	if r.URL.Query().Get("fresh") == "1" {
		list = s.models.Fresh
	}
	body, err := list(ctx)
	if err != nil {
		s.log.Warn("could not read the upstream model list", "err", err)
		httpx.WriteError(w, http.StatusBadGateway, httpx.KindUpstream,
			"could not reach the upstream model list")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
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
