package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claudication/internal/geminiwire"
	"claudication/internal/provider"
)

// probeTimeout bounds a credential test: one short turn.
const probeTimeout = 60 * time.Second

// Probe sends the smallest useful turn through an account, to prove it serves
// end to end: streamed, as gemini-cli sends every turn, through the same
// envelope and the same Stamp a relayed turn goes through.
//
// model empty is the account's newest Flash model, from its own quota, so a
// test neither names a model the plan lacks nor spends a Pro request.
func Probe(ctx context.Context, client *http.Client, cred provider.Credential, model string) provider.ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	started := time.Now()
	if model == "" {
		m, err := probeModel(ctx, client, cred)
		if err != nil {
			return provider.ProbeResult{LatencyMS: time.Since(started).Milliseconds(), Error: err.Error()}
		}
		model = m
	}
	body, err := json.Marshal(geminiwire.Request{
		Model: model,
		Request: geminiwire.GenerateRequest{
			Contents: []geminiwire.Content{{Role: "user", Parts: []geminiwire.Part{{Text: "Reply with the single word: pong"}}}},
		},
	})
	if err != nil {
		return provider.ProbeResult{Error: fmt.Sprintf("encode probe request: %v", err)}
	}
	body = Provider{}.Stamp(body, cred)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+StreamPath, bytes.NewReader(body))
	if err != nil {
		return provider.ProbeResult{Error: fmt.Sprintf("build probe request: %v", err)}
	}
	Provider{}.Authorize(req, cred)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return provider.ProbeResult{LatencyMS: time.Since(started).Milliseconds(), Error: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	out := provider.ProbeResult{
		Status:    resp.StatusCode,
		LatencyMS: time.Since(started).Milliseconds(),
		Model:     model,
	}
	if readErr != nil {
		out.Error = fmt.Sprintf("read response: %v", readErr)
		return out
	}
	if resp.StatusCode != http.StatusOK {
		out.Error = describe(raw)
		return out
	}

	var (
		reply     strings.Builder
		usage     provider.Usage
		streamErr string
		finish    string
	)
	m := newMeter(resp.Header.Get("Content-Type"), provider.Reading{Usage: &usage, StreamError: &streamErr})
	m.Feed(raw)
	m.Done()
	for _, line := range strings.Split(string(raw), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var ev geminiwire.Response
		if json.Unmarshal([]byte(strings.TrimSpace(payload)), &ev) != nil || ev.Response == nil {
			continue
		}
		out.RequestID = firstOf(out.RequestID, ev.TraceID)
		if ev.Response.ModelVersion != "" {
			out.Model = ev.Response.ModelVersion
		}
		for _, c := range ev.Response.Candidates {
			if c.FinishReason != "" {
				finish = c.FinishReason
			}
			if c.Content == nil {
				continue
			}
			for _, p := range c.Content.Parts {
				if !p.Thought {
					reply.WriteString(p.Text)
				}
			}
		}
	}
	out.InputTokens = usage.InputTokens + usage.CacheReadTokens
	out.OutputTokens = usage.OutputTokens
	out.Reply = strings.TrimSpace(reply.String())
	switch {
	case streamErr != "":
		out.Error = streamErr
	case finish == "":
		out.Error = "the stream ended without a finish reason"
	default:
		out.OK = true
		out.StopReason = finish
	}
	return out
}

// probeModel is the model a test uses when none was named: the account's
// newest Flash, or its first model when it has no Flash.
func probeModel(ctx context.Context, client *http.Client, cred provider.Credential) (string, error) {
	var q quotaResponse
	if err := call(ctx, client, cred.Token, http.MethodPost, ":retrieveUserQuota", map[string]string{"project": cred.AccountID}, &q); err != nil {
		return "", fmt.Errorf("read the account's models: %w", err)
	}
	ids := q.models()
	if len(ids) == 0 {
		return "", fmt.Errorf("the account has quota for no model")
	}
	for _, id := range ids {
		if r := rank(id); r.gemini && r.tier == flashTier {
			return id, nil
		}
	}
	return ids[0], nil
}
