package upstream

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// pngOf is a base64 PNG of the given size, with something in it so a rescale
// has pixels to average rather than one flat colour.
func pngOf(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// bodyWith builds a request carrying n images, the first of them big.
func bodyWith(t *testing.T, n int, bigW, bigH int) []byte {
	t.Helper()
	content := []any{map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "base64", "media_type": "image/png", "data": pngOf(t, bigW, bigH),
		},
	}}
	small := pngOf(t, 8, 8)
	for i := 1; i < n; i++ {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": "image/png", "data": small,
			},
		})
	}
	out, err := json.Marshal(map[string]any{
		"model":    "claude-opus-5",
		"messages": []any{map[string]any{"role": "user", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dimensionsIn reports the size of every image in a body, in order.
func dimensionsIn(t *testing.T, body []byte) []image.Config {
	t.Helper()
	var envelope any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var out []image.Config
	eachBase64Image(envelope, func(source map[string]any) {
		raw, err := base64.StdEncoding.DecodeString(source["data"].(string))
		if err != nil {
			t.Fatal(err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, cfg)
	})
	return out
}

// Below the threshold the upstream allows 8000 px, so there is nothing to
// repair — and the body must come back exactly as it arrived.
func TestShrinkImagesLeavesAFewImagesAlone(t *testing.T) {
	body := bodyWith(t, ManyImages, 4000, 3000)
	out, n := ShrinkImages(body, NewImageCache(0))
	if n != 0 {
		t.Errorf("shrank %d images in a request of %d, want none", n, ManyImages)
	}
	if !bytes.Equal(out, body) {
		t.Error("the body was rewritten when nothing needed changing")
	}
}

// Past it, the oversized one is capped and the rest are left as they are.
func TestShrinkImagesCapsTheLongEdge(t *testing.T) {
	body := bodyWith(t, ManyImages+1, 4000, 3000)
	out, n := ShrinkImages(body, NewImageCache(0))
	if n != 1 {
		t.Fatalf("shrank %d images, want the one that was too big", n)
	}

	dims := dimensionsIn(t, out)
	if len(dims) != ManyImages+1 {
		t.Fatalf("%d images in the rewritten body, want %d", len(dims), ManyImages+1)
	}
	// The long edge lands exactly on the cap, and the aspect ratio holds:
	// 4000x3000 is 4:3, so 2000 wide is 1500 tall.
	if dims[0].Width != MaxEdge || dims[0].Height != 1500 {
		t.Errorf("first image is %dx%d, want %dx1500", dims[0].Width, dims[0].Height, MaxEdge)
	}
	for i, d := range dims[1:] {
		if d.Width != 8 || d.Height != 8 {
			t.Errorf("image %d is %dx%d, want the 8x8 it arrived as", i+1, d.Width, d.Height)
		}
	}
}

// A portrait image is capped on its own long edge, not on its width.
func TestShrinkImagesCapsTheLongEdgeWhicheverItIs(t *testing.T) {
	body := bodyWith(t, ManyImages+1, 1500, 3000)
	out, _ := ShrinkImages(body, NewImageCache(0))
	dims := dimensionsIn(t, out)
	if dims[0].Height != MaxEdge || dims[0].Width != 1000 {
		t.Errorf("portrait image is %dx%d, want 1000x%d", dims[0].Width, dims[0].Height, MaxEdge)
	}
}

// The transform has to be identical every time or it changes the cached prefix
// on every turn and misses the prompt cache it sits inside.
func TestShrinkImagesIsDeterministic(t *testing.T) {
	body := bodyWith(t, ManyImages+1, 4000, 3000)
	first, _ := ShrinkImages(body, NewImageCache(0))
	// A fresh cache, so this is the encoder being deterministic rather than
	// the cache handing back the same string.
	second, _ := ShrinkImages(body, NewImageCache(0))
	if !bytes.Equal(first, second) {
		t.Error("two runs produced different bytes; every turn would miss the prompt cache")
	}
}

// Images nested in a tool_result count towards the threshold, and get capped
// like any other. The vision docs say so explicitly, and a computer-use client
// returning screenshots is exactly how a request gets past twenty.
func TestShrinkImagesReachesIntoToolResults(t *testing.T) {
	big := pngOf(t, 3000, 3000)
	small := pngOf(t, 8, 8)
	results := []any{}
	for i := 0; i < ManyImages; i++ {
		results = append(results, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type": "base64", "media_type": "image/png", "data": small,
			},
		})
	}
	results = append(results, map[string]any{
		"type": "image",
		"source": map[string]any{
			"type": "base64", "media_type": "image/png", "data": big,
		},
	})
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "t1", "content": results,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, n := ShrinkImages(body, NewImageCache(0))
	if n != 1 {
		t.Fatalf("shrank %d images inside a tool_result, want 1", n)
	}
	dims := dimensionsIn(t, out)
	if dims[len(dims)-1].Width != MaxEdge {
		t.Errorf("the nested image is %d wide, want %d", dims[len(dims)-1].Width, MaxEdge)
	}
}

// A body we cannot read is the caller's to answer for; the upstream says what
// is wrong with it more precisely than we could.
func TestShrinkImagesLeavesUnreadableBodiesAlone(t *testing.T) {
	body := []byte(`{"messages": [`)
	out, n := ShrinkImages(body, NewImageCache(0))
	if n != 0 || !bytes.Equal(out, body) {
		t.Error("a malformed body was altered")
	}
}

// An image we cannot decode may well be one the upstream accepts. Refusing to
// relay it here would turn a maybe into a certainty.
func TestShrinkImagesLeavesUndecodableImagesAlone(t *testing.T) {
	content := []any{}
	for i := 0; i < ManyImages+1; i++ {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "image/png",
				"data":       base64.StdEncoding.EncodeToString([]byte("not an image at all")),
			},
		})
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, n := ShrinkImages(body, NewImageCache(0))
	if n != 0 || !bytes.Equal(out, body) {
		t.Errorf("shrank %d undecodable images; the body should be untouched", n)
	}
}

// The cache is what stops a long conversation re-encoding its whole history on
// every turn, so it has to actually answer the second time.
func TestImageCacheAnswersTwice(t *testing.T) {
	cache := NewImageCache(0)
	big := pngOf(t, 3000, 3000)

	first, media, changed := cache.fit(big, "image/png")
	if !changed || media != "image/png" {
		t.Fatalf("first fit: changed=%v media=%q", changed, media)
	}
	second, _, _ := cache.fit(big, "image/png")
	if first != second {
		t.Error("the cache returned different bytes for the same image")
	}
	if len(cache.entries) != 1 {
		t.Errorf("%d entries after two fits of one image, want 1", len(cache.entries))
	}

	// An image that needs nothing is remembered as such, so its header is not
	// re-read on every turn of a conversation that carries it.
	small := pngOf(t, 8, 8)
	if _, _, changed := cache.fit(small, "image/png"); changed {
		t.Error("an 8x8 image was rewritten")
	}
	if len(cache.entries) != 2 {
		t.Errorf("%d entries, want the unchanged image remembered too", len(cache.entries))
	}
}

// JPEG stays JPEG; anything else comes back as PNG, because those are the two
// Go can write.
func TestShrinkOneKeepsJPEGAndPNGifiesTheRest(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"image/jpeg", "image/jpeg"},
		{"image/png", "image/png"},
		{"image/webp", "image/png"},
		{"image/gif", "image/png"},
	} {
		// The source is a PNG whatever it claims to be: the decoder sniffs the
		// bytes, and what is under test is which encoder the media type picks.
		got := shrinkOne(pngOf(t, 2600, 2600), tc.in)
		if !got.changed {
			t.Errorf("%s: not changed", tc.in)
			continue
		}
		if got.media != tc.want {
			t.Errorf("%s came back as %s, want %s", tc.in, got.media, tc.want)
		}
	}
}

// The count is of every image block, so the threshold is reached by a
// conversation's accumulated history and not only by one crowded turn.
func TestEachBase64ImageFindsThemAnywhere(t *testing.T) {
	var envelope any
	body := fmt.Sprintf(`{"messages":[
	  {"role":"user","content":[{"type":"image","source":{"type":"base64","data":"%s"}}]},
	  {"role":"user","content":[{"type":"tool_result","content":[
	     {"type":"image","source":{"type":"base64","data":"%s"}}]}]},
	  {"role":"user","content":[{"type":"image","source":{"type":"url","url":"http://x/y.png"}}]}
	]}`, "a", "b")
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	var found []string
	eachBase64Image(envelope, func(s map[string]any) {
		found = append(found, s["data"].(string))
	})
	if len(found) != 2 {
		t.Fatalf("found %d base64 images (%s), want the two that are base64",
			len(found), strings.Join(found, ","))
	}
}
