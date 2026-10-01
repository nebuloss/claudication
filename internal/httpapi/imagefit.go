package httpapi

import (
	"encoding/json"
	"net/http"
)

// imageFitSetting is the settings row holding the switch for capping
// oversized images in a many-image request.
//
// Runtime state rather than configuration, for the reason the API switches and
// the chat-title switches are: this one decides whether the relay may change
// what the model is shown, and the answer to "stop doing that" cannot be "edit
// a file and restart".
//
// Off by default, unlike every other pass the relay makes. The other five
// exceptions repair an envelope the upstream would refuse over its shape; this
// one re-encodes the caller's own image, and the caller is the only one who
// knows whether the detail it loses mattered.
const imageFitSetting = "passthrough.fit-oversized-images"

// handleSetImageFit flips it.
func (s *Server) handleSetImageFit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", `expected {"enabled": true|false}`)
		return
	}
	if err := s.images.Set(r.Context(), *body.Enabled); err != nil {
		s.log.Error("could not store the image-fit switch", "err", err)
		writeError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("image fitting switched", "enabled", *body.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"fit_images": s.images.On()})
}
