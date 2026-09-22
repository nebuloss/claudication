package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// imageFitSetting is the settings row holding the switch.
const imageFitSetting = "passthrough.fit-oversized-images"

// imageFit is the switch for capping oversized images in a many-image request.
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
type imageFit struct {
	server *Server
	// Atomic rather than mutex-guarded: the relay reads this on every request
	// it forwards, so it is one of the few flags here that is genuinely on a
	// hot path. A mutex would be a shared lock taken by every concurrent
	// stream to answer a question whose value is one bit.
	on atomic.Bool
}

func newImageFit(s *Server) *imageFit { return &imageFit{server: s} }

// load reads the switch at startup. A failure is worth reporting and not worth
// refusing to start over: the default is off, which is the pass-through rule.
func (f *imageFit) load(ctx context.Context) error {
	stored, err := f.server.store.Settings(ctx)
	if err != nil {
		return err
	}
	f.on.Store(stored[imageFitSetting] == "true")
	return nil
}

func (f *imageFit) enabled() bool { return f.on.Load() }

// set changes the switch. The store is written first, so a switch that took
// effect but did not survive a restart is not a state this can reach.
func (f *imageFit) set(ctx context.Context, on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	if err := f.server.store.SetSetting(ctx, imageFitSetting, value); err != nil {
		return err
	}
	f.on.Store(on)
	return nil
}

// handleSetImageFit flips it.
func (s *Server) handleSetImageFit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", `expected {"enabled": true|false}`)
		return
	}
	if err := s.images.set(r.Context(), *body.Enabled); err != nil {
		s.log.Error("could not store the image-fit switch", "err", err)
		writeError(w, http.StatusInternalServerError, "api_error", "could not store the setting")
		return
	}
	s.log.Warn("image fitting switched", "enabled", *body.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"fit_images": s.images.enabled()})
}
