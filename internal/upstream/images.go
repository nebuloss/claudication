package upstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"sync"

	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decode only; there is no encoder, so WebP leaves as PNG.
)

// ManyImages is the count past which the upstream stops allowing 8000 px.
//
// From the vision documentation: "If a single API request contains more than
// 20 images, a stricter per-image dimension limit applies to every image in
// that request." Every image block counts — including ones resent from earlier
// turns and ones nested inside tool_result content — which is why the walk
// below does not care where in the body it finds them.
const ManyImages = 20

// MaxEdge is that stricter limit, in pixels, on either side.
const MaxEdge = 2000

// ShrinkImages caps every image in a many-image request at [MaxEdge].
//
// The sixth exception to the pass-through rule, and the only optional one: it
// is off unless an operator switches it on, because unlike the other five it
// changes what the model is shown rather than only the shape of the envelope.
//
// The refusal it repairs, measured on a crush conversation that had served 878
// requests either side of it:
//
//	messages.70.content.1.image.source.base64.data: At least one of the image
//	dimensions exceed max allowed size for many-image requests: 2000 pixels
//
// It reads as flaky and is not. The cap is conditional on the image count, so
// a conversation that was fine becomes refused as it accumulates images, over
// an image that has been sitting in its history for hours — and every turn
// from then on carries it again.
//
// What the caller loses is bounded and usually nothing: the upstream already
// downscales to a 2576 px long edge on Claude 4.7 and later, and to 1568 px on
// everything before that. So capping at 2000 costs at most the band between
// 2000 and 2576, and on a standard-tier model costs nothing at all.
//
// Below the threshold this does not touch the body. Images are counted before
// anything is decoded, and a request the upstream would accept is returned
// byte-identical.
func ShrinkImages(body []byte, cache *ImageCache) ([]byte, int) {
	var envelope any
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Not ours to repair. Every other pass takes the same view: a body we
		// cannot read is the caller's to answer for, and the upstream says so
		// more precisely than we could.
		return body, 0
	}

	count := 0
	eachBase64Image(envelope, func(map[string]any) { count++ })
	if count <= ManyImages {
		return body, 0
	}

	shrunk := 0
	eachBase64Image(envelope, func(source map[string]any) {
		data, _ := source["data"].(string)
		media, _ := source["media_type"].(string)
		if data == "" {
			return
		}
		out, outMedia, changed := cache.fit(data, media)
		if !changed {
			return
		}
		source["data"] = out
		source["media_type"] = outMedia
		shrunk++
	})
	if shrunk == 0 {
		return body, 0
	}

	// Re-encoding reorders keys and drops insignificant whitespace, which is
	// safe here for the same reason it is safe in the other passes: the
	// upstream parses the body before it hashes anything for the cache. What
	// must not move is the blocks, not their spacing.
	out, err := json.Marshal(envelope)
	if err != nil {
		return body, 0
	}
	return out, shrunk
}

// eachBase64Image calls fn with the source object of every base64 image block,
// wherever it sits.
//
// A recursive walk rather than a path through messages[].content[], because
// the upstream counts images nested in tool_result content towards the
// threshold too, and a walk that knows only the shapes we have seen would
// quietly miss the next one.
func eachBase64Image(v any, fn func(source map[string]any)) {
	switch node := v.(type) {
	case map[string]any:
		if node["type"] == "image" {
			if source, ok := node["source"].(map[string]any); ok && source["type"] == "base64" {
				fn(source)
			}
		}
		for _, child := range node {
			eachBase64Image(child, fn)
		}
	case []any:
		for _, child := range node {
			eachBase64Image(child, fn)
		}
	}
}

// fitResult is what one source image became, or that it was left alone.
type fitResult struct {
	data    string
	media   string
	changed bool
	// bytes is what this entry costs to keep, for the eviction bound.
	bytes int
}

// ImageCache remembers what each oversized image was rewritten to.
//
// Needed for two separate reasons. A long conversation resends every image on
// every turn, so without it a 25-image chat decodes and re-encodes 25 images
// per request for the life of the conversation. And the answer has to be
// identical each time: a transform that varied would change the prefix on
// every turn and miss the very prompt cache it sits inside.
//
// Keyed on the source bytes rather than on any position in the request, so an
// image that moves as the conversation grows is still the same entry.
type ImageCache struct {
	mu      sync.Mutex
	entries map[string]fitResult
	// order is insertion order, for eviction. Oldest first, which is the right
	// end to drop from: a conversation resends its whole history, so the
	// images still in play are the ones most recently asked for.
	order []string
	held  int
	max   int
}

// DefaultImageCacheBytes bounds what the cache keeps. Generous because the
// alternative is re-encoding on every turn, and small next to what a gateway
// holding request bodies in memory already spends.
const DefaultImageCacheBytes = 64 << 20

// NewImageCache returns a cache bounded to max bytes of rewritten images.
func NewImageCache(max int) *ImageCache {
	if max <= 0 {
		max = DefaultImageCacheBytes
	}
	return &ImageCache{entries: map[string]fitResult{}, max: max}
}

// fit returns the rewritten image, or reports that none was needed.
//
// An image that needs no change is remembered as such: the common case in a
// many-image request is that most images are already small, and re-reading
// every header on every turn is the cost this exists to avoid.
func (c *ImageCache) fit(data, media string) (string, string, bool) {
	// No cache is a working configuration, just a slower one: the relay may be
	// assembled without one, and the answer must not depend on that.
	if c == nil {
		r := shrinkOne(data, media)
		return r.data, r.media, r.changed
	}

	key := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))

	c.mu.Lock()
	if hit, ok := c.entries[key]; ok {
		c.mu.Unlock()
		return hit.data, hit.media, hit.changed
	}
	c.mu.Unlock()

	result := shrinkOne(data, media)

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.entries[key] = result
		c.order = append(c.order, key)
		c.held += result.bytes
		for c.held > c.max && len(c.order) > 1 {
			oldest := c.order[0]
			c.order = c.order[1:]
			c.held -= c.entries[oldest].bytes
			delete(c.entries, oldest)
		}
	}
	return result.data, result.media, result.changed
}

// shrinkOne does the work for one image. Any failure leaves it alone: an image
// we cannot decode is one the upstream may well accept, and refusing to relay
// it here would turn a maybe into a certainty.
func shrinkOne(data, media string) fitResult {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fitResult{}
	}

	// The header alone answers the only question that matters most of the
	// time. DecodeConfig reads dimensions without decoding pixels, so an
	// already-small image costs a few bytes of parsing rather than a full
	// decode.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fitResult{}
	}
	if cfg.Width <= MaxEdge && cfg.Height <= MaxEdge {
		return fitResult{}
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fitResult{}
	}
	dst := scaleToFit(src, MaxEdge)

	// JPEG stays JPEG; everything else becomes PNG, because Go encodes only
	// those two and a GIF or WebP re-encoded losslessly is the safer default
	// for the screenshots this mostly sees. Animation is lost, which costs
	// nothing: the upstream reads only the first frame either way.
	var buf bytes.Buffer
	outMedia := "image/png"
	if media == "image/jpeg" {
		// Fixed quality, because the bytes have to come out the same every
		// time for the prompt cache to hit.
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return fitResult{}
		}
		outMedia = "image/jpeg"
	} else if err := png.Encode(&buf, dst); err != nil {
		return fitResult{}
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return fitResult{data: encoded, media: outMedia, changed: true, bytes: len(encoded)}
}

// scaleToFit puts the long edge exactly on max and keeps the aspect ratio.
func scaleToFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	switch {
	case w >= h:
		nw = max
		nh = int(math.Round(float64(h) * float64(max) / float64(w)))
	default:
		nh = max
		nw = int(math.Round(float64(w) * float64(max) / float64(h)))
	}
	nw, nh = maxInt(nw, 1), maxInt(nh, 1)

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// CatmullRom rather than the cheap sampler: this runs once per image per
	// conversation, and what is usually being shrunk is a screenshot with text
	// in it, where a nearest-neighbour pass is the difference between legible
	// and not.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Referenced so the GIF decoder registers; image.Decode finds formats by the
// side effect of importing them, and a bare blank import beside two named ones
// reads like an oversight.
var _ = gif.Decode
