package capture

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"time"
	"unicode/utf8"
)

// HAR writes exchanges as an HTTP Archive (HAR 1.2): the format browsers'
// developer tools, Charles, Fiddler and Insomnia all open. Each exchange is
// two or more entries — the client's leg, then each upstream attempt — tied
// together by the _claudication field every entry carries, which also holds
// what HAR has no place for: the leg, the dialect, the account, whether a
// body was cut, and when each part of a streamed answer arrived.
func HAR(xs []*Exchange, creator, version string) ([]byte, error) {
	entries := []harEntry{}
	for _, x := range xs {
		entries = append(entries, harLeg(x, &x.Client, "client", 0))
		for i, l := range x.Upstream {
			entries = append(entries, harLeg(x, l, "upstream", i+1))
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	doc := map[string]any{"log": map[string]any{
		"version": "1.2",
		"creator": map[string]string{"name": creator, "version": version},
		"entries": entries,
	}}
	return json.MarshalIndent(doc, "", "  ")
}

type harEntry struct {
	Started  string         `json:"startedDateTime"`
	Time     float64        `json:"time"`
	Request  map[string]any `json:"request"`
	Response map[string]any `json:"response"`
	Cache    struct{}       `json:"cache"`
	Timings  map[string]any `json:"timings"`
	Own      map[string]any `json:"_claudication"`

	at time.Time
}

func harLeg(x *Exchange, l *Leg, leg string, attempt int) harEntry {
	req := &l.Request
	reqText, reqEnc := text(req.Body.Bytes())
	post := map[string]any{"mimeType": mimeOf(req.Header), "text": reqText}
	if reqEnc != "" {
		post["_encoding"] = reqEnc
	}
	request := map[string]any{
		"method":      req.Method,
		"url":         req.URL,
		"httpVersion": orHTTP11(req.Proto),
		"cookies":     []any{},
		"headers":     headers(req.Header),
		"queryString": query(req.URL),
		"postData":    post,
		"headersSize": -1,
		"bodySize":    req.Body.Size(),
	}

	response := map[string]any{
		"status": 0, "statusText": "", "httpVersion": "", "cookies": []any{}, "headers": []any{},
		"content":     map[string]any{"size": 0, "mimeType": ""},
		"redirectURL": "", "headersSize": -1, "bodySize": -1,
	}
	wait, receive := -1.0, -1.0
	var chunks []Chunk
	if r := l.Response; r != nil {
		body := r.Body.Bytes()
		t, enc := text(body)
		content := map[string]any{"size": r.Body.Size(), "mimeType": mimeOf(r.Header), "text": t}
		if enc != "" {
			content["encoding"] = enc
		}
		response = map[string]any{
			"status": r.Status, "statusText": http.StatusText(r.Status), "httpVersion": orHTTP11(r.Proto),
			"cookies": []any{}, "headers": headers(r.Header), "content": content,
			"redirectURL": "", "headersSize": -1, "bodySize": r.Body.Size(),
		}
		chunks = r.Body.Chunks()
		// The chunks' times are from the exchange's start; a leg's own
		// timings are from its own.
		offset := float64(l.Started.Sub(x.At).Milliseconds())
		if len(chunks) > 0 {
			wait = max(0, float64(chunks[0].AtMS)-offset)
			receive = max(0, float64(chunks[len(chunks)-1].AtMS-chunks[0].AtMS))
		}
	}
	total := 0.0
	if !l.Ended.IsZero() {
		total = float64(l.Ended.Sub(l.Started).Milliseconds())
	}
	own := map[string]any{
		"exchange": x.ID, "leg": leg, "dialect": l.Dialect, "route": x.Route, "client": x.KeyName,
	}
	if attempt > 0 {
		own["attempt"] = attempt
		own["account"] = l.Account
	}
	if l.Error != "" {
		own["error"] = l.Error
	}
	if req.Body.Truncated() {
		own["request_truncated"] = true
	}
	if l.Response != nil && l.Response.Body.Truncated() {
		own["response_truncated"] = true
	}
	if len(chunks) > 1 {
		own["chunks"] = chunks
	}
	return harEntry{
		Started:  l.Started.UTC().Format(time.RFC3339Nano),
		at:       l.Started,
		Time:     total,
		Request:  request,
		Response: response,
		Timings:  map[string]any{"send": 0, "wait": wait, "receive": receive},
		Own:      own,
	}
}

func text(b []byte) (string, string) {
	if utf8.Valid(b) {
		return string(b), ""
	}
	return base64.StdEncoding.EncodeToString(b), "base64"
}

func mimeOf(h http.Header) string {
	ct := h.Get("Content-Type")
	if ct == "" {
		return "application/octet-stream"
	}
	if mt, _, err := mime.ParseMediaType(ct); err == nil {
		return mt
	}
	return ct
}

func orHTTP11(p string) string {
	if p == "" {
		return "HTTP/1.1"
	}
	return p
}

func headers(h http.Header) []map[string]string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := []map[string]string{}
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, map[string]string{"name": k, "value": v})
		}
	}
	return out
}

func query(raw string) []map[string]string {
	out := []map[string]string{}
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	for k, vs := range u.Query() {
		for _, v := range vs {
			out = append(out, map[string]string{"name": k, "value": v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
	return out
}
