package httpapi

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"claudication/internal/relay/passes"
	"claudication/internal/store"
)

// manyImageTurn is a Messages turn on the large slot carrying one more image
// than the upstream allows 8000 px for, each 2400 px wide: refused unless
// fitted. The image's base64 comes back to tell whether it went up as sent.
func manyImageTurn(t *testing.T) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2400, 4))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	var content []string
	for i := 0; i <= passes.ManyImages; i++ {
		content = append(content, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+data+`"}}`)
	}
	content = append(content, `{"type":"text","text":"what changed?"}`)
	return `{"model":"large","max_tokens":100,"stream":true,"messages":[{"role":"user","content":[` +
		strings.Join(content, ",") + `]}]}`, data
}

// Fitting images is a client's own switch: off, a many-image request reaches
// Claude with its images as sent; switched on from the client's page, the
// same request from that client reaches it fitted, and another client's
// requests are untouched. The switch is reported with the client and refuses a
// body that does not say which way to flip it.
func TestImageFittingIsPerClient(t *testing.T) {
	srv, st, base, _, claude, _ := twoUpstreams(t)
	abase, ck := adminAPI(t, srv)
	bind := map[string]store.Binding{"large": {Model: "claude-sonnet-5"}}
	c, key := newClient(t, st, "shots", "custom", bind)
	_, other := newClient(t, st, "diagrams", "custom", bind)
	turn, data := manyImageTurn(t)

	sentAsIs := func(key string) bool {
		t.Helper()
		resp, out := send(t, base+"/v1/messages", key, "mytool/1.0", turn, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, out)
		}
		turns := claude.turns()
		return bytes.Contains(turns[len(turns)-1].body, []byte(data))
	}

	if !sentAsIs(key) {
		t.Error("a client with fitting off had its images re-encoded")
	}
	status, body := call(t, abase, http.MethodGet, "/admin/clients/"+c.ID, "", ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"fit_images":false`)) {
		t.Errorf("client: %d %s, want fitting reported off", status, body)
	}

	status, body = call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/fit-images", `{"enabled":true}`, ck)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"fit_images":true`)) {
		t.Fatalf("fit-images: %d %s", status, body)
	}
	if sentAsIs(key) {
		t.Error("a client with fitting on had its images sent as they were")
	}
	if !sentAsIs(other) {
		t.Error("another client's images were re-encoded by this one's switch")
	}

	for _, bad := range []string{`{}`, `{"enabled":`, `{"enabled":"yes"}`} {
		if status, _ := call(t, abase, http.MethodPost, "/admin/clients/"+c.ID+"/fit-images", bad, ck); status != http.StatusBadRequest {
			t.Errorf("fit-images %s: %d, want 400", bad, status)
		}
	}
	if status, _ := call(t, abase, http.MethodPost, "/admin/clients/nope/fit-images", `{"enabled":true}`, ck); status != http.StatusNotFound {
		t.Errorf("fit-images on no client: %d, want 404", status)
	}
}
