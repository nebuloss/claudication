package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"claudication/internal/api"
	geminiapi "claudication/internal/api/gemini"
	"claudication/internal/httpapi/httpx"
)

// geminiCall serves the Gemini API's one pattern,
// /v1beta/models/{model}:{method}. The method is the route — streamed,
// whole, or a count — and the model is folded into the body
// (geminiapi.Envelope), because the pipeline every dialect shares reads a
// request's model from its body. From there it is inference like any other.
func (s *Gateway) geminiCall(p api.Protocol) http.Handler {
	routes := map[string]http.Handler{}
	for method, route := range geminiapi.Methods {
		routes[method] = s.inference(p, route)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model, method, ok := strings.Cut(r.PathValue("call"), ":")
		next := routes[method]
		if !ok || model == "" || next == nil {
			p.WriteError(w, http.StatusNotFound, api.KindNotFound,
				"this gateway serves streamGenerateContent, generateContent and countTokens under /v1beta/models/{model}:")
			return
		}
		body, err := httpx.ReadSized(r.Body, r.ContentLength, s.cfg.Limits.MaxBodyBytes)
		if err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, "could not read the request body")
			return
		}
		if body, err = httpx.DecodeBody(r, body, s.cfg.Limits.MaxBodyBytes); err != nil {
			p.WriteError(w, http.StatusBadRequest, api.KindInvalidRequest, err.Error())
			return
		}
		folded := geminiapi.Envelope(model, method, body)
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(folded))
		r.ContentLength = int64(len(folded))
		// Opened above; what goes on is plain.
		r.Header.Del("Content-Encoding")
		next.ServeHTTP(w, r)
	})
}
