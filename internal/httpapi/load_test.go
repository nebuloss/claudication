package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"claudication/internal/relay/passes"
	"claudication/internal/store"
)

// A load test, run by hand: CLAUDICATION_LOAD=1 go test -run TestLoad -v ./internal/httpapi
//
// Not part of make check — it takes a couple of minutes and its numbers are a
// property of the machine. It answers one question: does the gateway itself
// hold up when many agents use it at once, independently of what the upstream
// does? So the upstream here is a stub that streams like the real one and is
// never the bottleneck, and everything measured is what the gateway adds.
//
// What it puts in front of the gateway, each chosen to match production:
//   - a database already holding a month of history, because the admin
//     reports scan it and share the same four connections as the relay;
//   - request bodies the size of a coding agent's turn, since every one is
//     held in memory and passes through the rewrite passes;
//   - the admin UI polling every report once a second, far harder than its
//     real 10–30 s, to find out whether reports can starve the relay.
const (
	loadFirstToken = 300 * time.Millisecond // stub: delay before the first delta
	loadDeltas     = 20                     // stub: deltas per answer
	loadDeltaGap   = 50 * time.Millisecond  // stub: gap between deltas
	loadHistory    = 80_000                 // usage rows seeded before the run
	loadStep       = 8 * time.Second        // how long each concurrency level runs
)

// loadBodyBytes is the request body size; see TestLoad.
var loadBodyBytes int

// loadStream, when set by CLAUDICATION_LOAD_STREAM, is how long each answer
// streams. Real ones run for minutes, and anything a request holds for its
// stream's lifetime only shows up in memory when streams are that long.
var loadStream time.Duration

func TestLoad(t *testing.T) {
	if os.Getenv("CLAUDICATION_LOAD") == "" {
		t.Skip("set CLAUDICATION_LOAD=1 to run the load test")
	}
	// 300 KB by default; production's crush turns carry up to ~950k cached
	// tokens, which is megabytes, so CLAUDICATION_LOAD_BODY_KB sets it.
	loadBodyBytes = 300 << 10
	if v, err := strconv.Atoi(os.Getenv("CLAUDICATION_LOAD_BODY_KB")); err == nil && v > 0 {
		loadBodyBytes = v << 10
	}
	loadStream, _ = time.ParseDuration(os.Getenv("CLAUDICATION_LOAD_STREAM"))
	levels := []int{10, 50, 100, 250, 500}
	if v := os.Getenv("CLAUDICATION_LOAD_LEVELS"); v != "" {
		levels = nil
		for _, f := range strings.Split(v, ",") {
			n, _ := strconv.Atoi(f)
			levels = append(levels, n)
		}
	}

	// The stub upstream, and how many requests it holds at once.
	var upInFlight, upPeak atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := upInFlight.Add(1)
		defer upInFlight.Add(-1)
		for {
			p := upPeak.Load()
			if n <= p || upPeak.CompareAndSwap(p, n) {
				break
			}
		}
		// Discarded, not buffered: the stub runs in the same process, and a
		// copy of every body held here was being charged to the gateway.
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		send := func(s string) { _, _ = w.Write([]byte(s)); _ = rc.Flush() }
		send("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":90000,\"output_tokens\":1}}}\n\n")
		send("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		time.Sleep(loadFirstToken)
		gap := loadDeltaGap
		if loadStream > 0 {
			gap = loadStream / loadDeltas
		}
		for i := 0; i < loadDeltas; i++ {
			send("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lorem ipsum dolor \"}}\n\n")
			time.Sleep(gap)
		}
		send("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":60}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer up.Close()

	srv, st, _ := newTestServer(t)
	srv.relay.BaseURL = up.URL
	// The key's own limit is lifted for the run; the default is measured
	// separately below, because it is the first thing many agents meet.
	srv.cfg.Limits.RequestsPerMinute = 0
	// Every client here is 127.0.0.1, so the per-IP bucket would measure
	// itself rather than the gateway.
	srv.cfg.Limits.AnonPerMinute = 1_000_000
	srv.trimmer.Quiet = 2 * time.Second
	if err := seedAccount(t, st, srv); err != nil {
		t.Fatal(err)
	}
	_, key, err := st.CreateKey(context.Background(), "load", store.KeyLimits{})
	if err != nil {
		t.Fatal(err)
	}

	seedHistory(t, st, loadHistory)

	base, cancel, done := startServer(t, srv)
	defer func() { cancel(); <-done }()

	cookie := claim(t, base)

	// What kind of traffic: Anthropic Messages by default, Codex's Responses
	// with CLAUDICATION_LOAD_API=openai, or a many-image request with
	// CLAUDICATION_LOAD_IMAGES=N (image fitting on; cache off with
	// CLAUDICATION_LOAD_IMAGECACHE=off, so every request decodes).
	loadShapeV = anthropicShape(loadBody())
	if os.Getenv("CLAUDICATION_LOAD_API") == "openai" {
		loadShapeV = openaiShape(t)
	}
	if n, _ := strconv.Atoi(os.Getenv("CLAUDICATION_LOAD_IMAGES")); n > 0 {
		loadShapeV = imageShape(t, n)
		if err := srv.images.Set(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("CLAUDICATION_LOAD_IMAGECACHE") == "off" {
			// The same pipeline with no image cache, so every request decodes.
			srv.relay.Passes = passes.Default(passes.Options{
				Attribution: srv.cfg.Passthrough.ClaudeCodeAttribution,
				FitImages:   srv.images.On,
			})
		}
	}
	body := loadShapeV.body

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 2000, MaxConnsPerHost: 0}}

	t.Logf("%s, history %d rows, body %d KB, stub first token %s + %d deltas x %s",
		loadShapeV.name, loadHistory, len(body)>>10, loadFirstToken, loadDeltas, loadDeltaGap)

	if d, err := time.ParseDuration(os.Getenv("CLAUDICATION_LOAD_SOAK")); err == nil && d > 0 {
		agents := 20
		if len(levels) > 0 && os.Getenv("CLAUDICATION_LOAD_LEVELS") != "" {
			agents = levels[0]
		}
		runSoak(t, client, base, key, body, agents, d, cookie)
		return
	}
	t.Logf("%6s %8s %7s %9s %9s %9s %9s %10s %9s %8s %8s %8s",
		"agents", "requests", "errors", "added p50", "added p90", "added p99", "added max",
		"admin p99", "heap MB", "gorout.", "RSS peak", "RSS idle")

	for _, agents := range levels {
		r := runLevel(client, base, key, body, agents, cookie)
		t.Logf("%6d %8d %7s %9s %9s %9s %9s %10s %9d %8d %8d %8d",
			agents, r.requests, r.errorsText(),
			ms(r.added(.5)), ms(r.added(.9)), ms(r.added(.99)), ms(r.added(1)),
			ms(r.adminP99()), r.heapMB, r.goroutines, r.rssPeakMB, r.rssIdleMB)
		if agents == levels[0] {
			var parts []string
			for path, ds := range r.adminBy {
				sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
				parts = append(parts, fmt.Sprintf("%s=%s", path, ms(ds[len(ds)/2])))
			}
			sort.Strings(parts)
			t.Logf("       admin median by page: %s", strings.Join(parts, "  "))
		}
	}
	t.Logf("stub upstream peak in flight: %d", upPeak.Load())

	// And with the default per-key limit, which is what a fresh key has.
	srv.cfg.Limits.RequestsPerMinute = 600
	r := runLevel(client, base, key, body, 50, nil)
	t.Logf("default limit (600/min), 50 agents: %d requests, errors %s", r.requests, r.errorsText())
}

type levelResult struct {
	requests   int
	errors     map[string]int
	addedTTFT  []time.Duration // client-seen first token minus the stub's own delay
	admin      []time.Duration
	adminBy    map[string][]time.Duration
	heapMB     uint64
	goroutines int
	rssPeakMB  int64
	rssIdleMB  int64 // after the burst, once the trimmer has had its chance
}

// rssMB is the process's resident memory, which is what a container's limit
// counts — not the heap.
func rssMB() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize()) >> 20
}

func (r *levelResult) added(q float64) time.Duration {
	if len(r.addedTTFT) == 0 {
		return 0
	}
	return r.addedTTFT[int(q*float64(len(r.addedTTFT)-1))]
}

func (r *levelResult) adminP99() time.Duration {
	if len(r.admin) == 0 {
		return 0
	}
	return r.admin[int(.99*float64(len(r.admin)-1))]
}

func (r *levelResult) errorsText() string {
	if len(r.errors) == 0 {
		return "0"
	}
	var parts []string
	for k, v := range r.errors {
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func runLevel(client *http.Client, base, key string, body []byte, agents int, cookie *http.Cookie) levelResult {
	var mu sync.Mutex
	res := levelResult{errors: map[string]int{}, adminBy: map[string][]time.Duration{}}
	deadline := time.Now().Add(loadStep)
	var wg sync.WaitGroup

	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				ttft, errKind := oneRequest(client, base, key, body)
				mu.Lock()
				res.requests++
				if errKind != "" {
					res.errors[errKind]++
				} else {
					res.addedTTFT = append(res.addedTTFT, ttft-loadFirstToken)
				}
				mu.Unlock()
			}
		}()
	}

	// The admin UI, polling every report at once, once a second.
	stop := make(chan struct{})
	var adminWG sync.WaitGroup
	if cookie != nil {
		for _, path := range []string{"/admin/overview", "/admin/usage", "/admin/requests", "/admin/chats", "/admin/requests/facets"} {
			adminWG.Add(1)
			go func() {
				defer adminWG.Done()
				tick := time.NewTicker(time.Second)
				defer tick.Stop()
				for {
					select {
					case <-stop:
						return
					case <-tick.C:
					}
					req, _ := http.NewRequest(http.MethodGet, base+path, nil)
					req.AddCookie(cookie)
					start := time.Now()
					resp, err := client.Do(req)
					if err == nil {
						_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
						resp.Body.Close()
					}
					took := time.Since(start)
					mu.Lock()
					res.admin = append(res.admin, took)
					res.adminBy[path] = append(res.adminBy[path], took)
					mu.Unlock()
				}
			}()
		}
	}

	// Peak heap and goroutines across the level.
	var peakHeap uint64
	peakG := 0
	sampleDone := make(chan struct{})
	go func() {
		defer close(sampleDone)
		var m runtime.MemStats
		for time.Now().Before(deadline) {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peakHeap {
				peakHeap = m.HeapInuse
			}
			if g := runtime.NumGoroutine(); g > peakG {
				peakG = g
			}
			if r := rssMB(); r > res.rssPeakMB {
				res.rssPeakMB = r
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()

	wg.Wait()
	close(stop)
	adminWG.Wait()
	<-sampleDone

	sort.Slice(res.addedTTFT, func(i, j int) bool { return res.addedTTFT[i] < res.addedTTFT[j] })
	sort.Slice(res.admin, func(i, j int) bool { return res.admin[i] < res.admin[j] })
	res.heapMB = peakHeap >> 20
	res.goroutines = peakG

	// Quiet, long enough for the trimmer's quiet period and a tick after it.
	time.Sleep(loadIdle)
	res.rssIdleMB = rssMB()
	return res
}

// loadIdle is how long each level waits after its burst before reading what
// the process still holds. The test shortens the trimmer's quiet period to 2 s
// so this does not dominate the run.
const loadIdle = 9 * time.Second

// loadShape is one kind of traffic: where it goes, and how its stream says
// "first token" and "done".
type loadShape struct {
	name        string
	path        string
	body        []byte
	first, stop string
}

var loadShapeV loadShape

func anthropicShape(body []byte) loadShape {
	return loadShape{"anthropic messages", "/v1/messages", body,
		"event: content_block_delta", "event: message_stop"}
}

// openaiShape is Codex's own captured request, its history grown to the body
// size — every Codex turn resends instructions, tools and the whole input.
func openaiShape(t *testing.T) loadShape {
	t.Helper()
	raw, err := os.ReadFile("../api/openai/testdata/codex-responses-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	input, _ := req["input"].([]any)
	turn := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 400)
	for size := len(raw); size < loadBodyBytes; size += 2 * len(turn) {
		input = append(input,
			map[string]any{"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": turn}}},
			map[string]any{"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": turn}}})
	}
	req["input"] = input
	body, _ := json.Marshal(req)
	return loadShape{"openai responses (codex)", "/v1/responses", body,
		"event: response.output_text.delta", "event: response.completed"}
}

// imageShape is a request carrying n photographs over the many-image edge
// limit, which the image fitting pass decodes, scales and re-encodes.
func imageShape(t *testing.T, n int) loadShape {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2400, 1600))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < 1600; y++ {
		for x := 0; x < 2400; x++ {
			img.Set(x, y, color.RGBA{uint8(x / 10), uint8(y / 7), uint8(rnd.Intn(40)), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buf.Bytes())
	var content []string
	for i := 0; i < n; i++ {
		content = append(content, `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"`+data+`"}}`)
	}
	content = append(content, `{"type":"text","text":"what changed between these?"}`)
	body := `{"model":"claude-opus-5","stream":true,"max_tokens":1024,` +
		`"messages":[{"role":"user","content":[` + strings.Join(content, ",") + `]}]}`
	return loadShape{fmt.Sprintf("%d images of 2400x1600 (%d KB each)", n, buf.Len()>>10), "/v1/messages",
		[]byte(body), "event: content_block_delta", "event: message_stop"}
}

// runSoak holds a steady load for d with the admin UI polling at its real
// rate, and samples what is still alive after each collection. A leak is a
// line that keeps rising; a healthy gateway is flat once warm.
func runSoak(t *testing.T, client *http.Client, base, key string, body []byte, agents int, d time.Duration, cookie *http.Cookie) {
	t.Helper()
	t.Logf("soak: %d agents for %s", agents, d)
	deadline := time.Now().Add(d)
	var requests, failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if _, errKind := oneRequest(client, base, key, body); errKind != "" {
					failures.Add(1)
				}
				requests.Add(1)
			}
		}()
	}
	stop := make(chan struct{})
	for _, path := range []string{"/admin/overview", "/admin/usage", "/admin/requests", "/admin/chats", "/admin/accounts"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(15 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				req, _ := http.NewRequest(http.MethodGet, base+path, nil)
				req.AddCookie(cookie)
				if resp, err := client.Do(req); err == nil {
					_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
					resp.Body.Close()
				}
			}
		}()
	}

	sample := func(label string) (live uint64, g int) {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("  %-8s requests=%-7d failures=%-4d live heap=%4d MB  goroutines=%5d  RSS=%4d MB",
			label, requests.Load(), failures.Load(), m.HeapAlloc>>20, runtime.NumGoroutine(), rssMB())
		return m.HeapAlloc, runtime.NumGoroutine()
	}
	var firstLive uint64
	var firstG int
	for i := 0; time.Now().Before(deadline); i++ {
		time.Sleep(30 * time.Second)
		live, g := sample(fmt.Sprintf("t+%ds", (i+1)*30))
		if i == 1 { // the first minute is warm-up
			firstLive, firstG = live, g
		}
	}
	close(stop)
	wg.Wait()
	time.Sleep(loadIdle)
	live, g := sample("idle")
	t.Logf("soak: live heap %d MB at 1 min -> %d MB idle, goroutines %d -> %d",
		firstLive>>20, live>>20, firstG, g)
}

// oneRequest runs one streamed request to the end and returns when its first
// token arrived, or what went wrong.
func oneRequest(client *http.Client, base, key string, body []byte) (time.Duration, string) {
	shape := loadShapeV
	req, _ := http.NewRequest(http.MethodPost, base+shape.path, bytes.NewReader(body))
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, "conn"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = bytes.NewBuffer(nil).ReadFrom(resp.Body)
		return 0, strconv.Itoa(resp.StatusCode)
	}
	var ttft time.Duration
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	stopped := false
	for sc.Scan() {
		line := sc.Text()
		if ttft == 0 && line == shape.first {
			ttft = time.Since(start)
		}
		if line == shape.stop {
			stopped = true
		}
	}
	if !stopped {
		return 0, "cut"
	}
	return ttft, ""
}

// loadBody is a request the size of a coding agent's turn.
func loadBody() []byte {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", loadBodyBytes/45)
	return []byte(`{"model":"claude-opus-5","stream":true,"max_tokens":4096,` +
		`"system":[{"type":"text","text":"You are a helpful coding agent."}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + text + `"}]}]}`)
}

// seedHistory fills the usage table the way a month of traffic would: spread
// over thirty days, a few hundred chats, a handful of models and keys.
func seedHistory(t *testing.T, st *store.Store, n int) {
	t.Helper()
	start := time.Now()
	now := time.Now()
	var wg sync.WaitGroup
	per := n / 4
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				k := w*per + i
				_ = st.RecordUsage(context.Background(), store.UsageEvent{
					At:             now.Add(-time.Duration(k) * 30 * 24 * time.Hour / time.Duration(n)),
					KeyID:          "k" + strconv.Itoa(k%3),
					KeyName:        "key" + strconv.Itoa(k%3),
					AccountID:      "a",
					AccountEmail:   "a@example.com",
					Model:          []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}[k%3],
					Path:           "/v1/messages",
					ConversationID: "chat-" + strconv.Itoa(k%400),
					Client:         []string{"crush", "claude-cli", "opencode"}[k%3],
					Status:         200,
					Streaming:      true,
					InputTokens:    10, OutputTokens: 300, CacheReadTokens: 90_000,
					Duration: 7 * time.Second, FirstToken: 2 * time.Second,
				})
			}
		}()
	}
	wg.Wait()
	t.Logf("seeded %d rows in %s", n, time.Since(start).Round(time.Millisecond))
}

// claim sets the admin password on the fresh gateway and returns its session.
func claim(t *testing.T, base string) *http.Cookie {
	t.Helper()
	resp := postJSON(t, base, http.MethodPost, "/admin/setup", map[string]string{"password": testPassword})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	return sessionCookieOf(t, resp)
}

func ms(d time.Duration) string { return d.Round(time.Millisecond).String() }
