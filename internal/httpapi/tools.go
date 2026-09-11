package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// tools holds the helper scripts the admin UI offers for download. `make
// tools` populates it from scripts/, which stays the one copy; the .gitkeep
// placeholder keeps this directive valid in a source-only checkout so the Go
// build never depends on that step having run.
//
// They are served because the people who most need them have no repository.
// A release installs one binary, so "run scripts/codex-model-catalog.py" is
// advice a released install cannot follow — and that particular script has to
// run against the operator's own Codex binary, so the gateway cannot do the
// job for them and hand back the result.
//
//go:embed all:tools
var tools embed.FS

// handleTool serves one embedded script as a download.
func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	// One flat directory, so anything with a separator in it is someone
	// probing rather than asking.
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		writeError(w, http.StatusNotFound, "not_found", "no such tool")
		return
	}

	body, err := fs.ReadFile(tools, path.Join("tools", name))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found",
			"no such tool; the binary may have been built without `make tools`")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// Not something a page should be allowed to render or a proxy to guess at.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
