package admin

import (
	"encoding/json"
	"net/http"

	"claudication/internal/httpapi/httpx"
)

// handleSetImageFit flips it.
func (s *Admin) handleSetImageFit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", `expected {"enabled": true|false}`)
		return
	}
	if err := s.images.Set(r.Context(), *body.Enabled); err != nil {
		s.log.Error("could not store the image-fit switch", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("image fitting switched", "enabled", *body.Enabled)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"fit_images": s.images.On()})
}

// handleSetDocs flips it.
func (s *Admin) handleSetDocs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", `expected {"enabled": true|false}`)
		return
	}
	if err := s.docs.Set(r.Context(), *body.Enabled); err != nil {
		s.log.Error("could not store the docs switch", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("public docs page switched", "enabled", *body.Enabled)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"docs_enabled": s.docs.On()})
}
