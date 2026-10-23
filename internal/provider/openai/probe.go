package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claudication/internal/provider"
)

// probeTimeout bounds a credential test: one short turn.
const probeTimeout = 60 * time.Second

// Probe sends the smallest useful turn through an account, to prove it serves
// end to end: the same shape scripts/probe-chatgpt.py's baseline sent on
// 2026-10-19 and was answered 200 — streamed, store false, short
// instructions as the top-level field (the backend took that and a developer
// message alike).
//
// model empty is the backend's own first choice, read from the account's
// model list, so a test does not name a model that a plan does not have.
func Probe(ctx context.Context, client *http.Client, cred provider.Credential, model string) provider.ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	started := time.Now()
	if model == "" {
		m, err := firstModel(ctx, client, cred)
		if err != nil {
			return provider.ProbeResult{LatencyMS: time.Since(started).Milliseconds(), Error: err.Error()}
		}
		model = m
	}
	body, err := json.Marshal(map[string]any{
		"model":        model,
		"stream":       true,
		"store":        false,
		"instructions": "Reply with the single word you are asked for.",
		"input": []map[string]any{{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": "Reply with the single word: pong"}},
		}},
	})
	if err != nil {
		return provider.ProbeResult{Error: fmt.Sprintf("encode probe request: %v", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return provider.ProbeResult{Error: fmt.Sprintf("build probe request: %v", err)}
	}
	Provider{}.Authorize(req, cred)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return provider.ProbeResult{LatencyMS: time.Since(started).Milliseconds(), Error: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	out := provider.ProbeResult{
		Status:    resp.StatusCode,
		LatencyMS: time.Since(started).Milliseconds(),
		RequestID: resp.Header.Get("X-Request-Id"),
		Model:     model,
	}
	if readErr != nil {
		out.Error = fmt.Sprintf("read response: %v", readErr)
		return out
	}
	if resp.StatusCode != http.StatusOK {
		out.Error = explain(resp.StatusCode, raw)
		return out
	}

	var (
		reply     strings.Builder
		usage     provider.Usage
		streamErr string
		completed bool
	)
	sc := newSSEScanner(provider.Reading{Usage: &usage, StreamError: &streamErr})
	sc.Feed(raw)
	for _, block := range strings.Split(string(raw), "\n\n") {
		var name, data string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data += strings.TrimSpace(line[len("data:"):])
			}
		}
		switch name {
		case "response.output_text.delta":
			var d struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &d) == nil {
				reply.WriteString(d.Delta)
			}
		case evCompleted:
			completed = true
			var c struct {
				Response struct {
					Model string `json:"model"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(data), &c) == nil && c.Response.Model != "" {
				out.Model = c.Response.Model
			}
		}
	}
	out.InputTokens = usage.InputTokens + usage.CacheReadTokens
	out.OutputTokens = usage.OutputTokens
	out.Reply = strings.TrimSpace(reply.String())
	switch {
	case streamErr != "":
		out.Error = streamErr
	case !completed:
		out.Error = "the stream ended before response.completed"
	default:
		out.OK = true
		out.StopReason = "completed"
	}
	return out
}

// explain is a refusal as the operator should read it: the upstream's own
// words, with what they mean when that is not obvious.
func explain(status int, raw []byte) string {
	text := clip(strings.TrimSpace(string(raw)), 2000)
	if parseError(raw).kind() == "usage_not_included" {
		return "this ChatGPT plan does not include Codex: " + text
	}
	return text
}

// firstModel is the account's own first-ranked model.
func firstModel(ctx context.Context, client *http.Client, cred provider.Credential) (string, error) {
	method, path, _ := Provider{}.ModelsRequest("", cred)
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, nil)
	if err != nil {
		return "", err
	}
	Provider{}.Authorize(req, cred)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("read the model list: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read the model list: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the model list returned %d: %s", resp.StatusCode, explain(resp.StatusCode, raw))
	}
	list, err := Provider{}.ModelList(raw)
	if err != nil {
		return "", err
	}
	var l struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(list, &l) != nil || len(l.Data) == 0 {
		return "", fmt.Errorf("the account lists no models")
	}
	return l.Data[0].ID, nil
}
