package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claudication/internal/anthropicwire"
	"claudication/internal/provider"
)

// probePath is the Messages endpoint as the official client calls it.
const probePath = "/v1/messages?beta=true"

// ProbeResult is what a credential test learned; every provider's is the
// same shape, because the accounts screen renders it.
type ProbeResult = provider.ProbeResult

// Probe sends the smallest useful request through an OAuth credential
// to prove it actually works end to end.
//
// The system prompt carries Claude Code's identity line because a subscription
// OAuth token is provisioned for that client and inference is refused without
// it. This is the gateway speaking as itself, not forging a client's
// attribution: no billing header or conversation fingerprint is synthesised,
// which is exactly the line the proxy paths must not cross either.
//
// model empty is the account's newest Haiku, read from its own model list,
// so a test neither spends the operator's Opus allowance nor names a model
// that has since been retired.
func Probe(ctx context.Context, client *http.Client, accessToken, model string) ProbeResult {
	if model == "" {
		m, err := smallestModel(ctx, client, accessToken)
		if err != nil {
			return ProbeResult{Error: err.Error()}
		}
		model = m
	}

	body, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 16,
		"system": []map[string]any{
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		},
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with the single word: pong"},
		},
	})
	if err != nil {
		return ProbeResult{Error: fmt.Sprintf("encode probe request: %v", err)}
	}

	req, err := newAccountRequest(ctx, http.MethodPost, probePath, bytes.NewReader(body), accessToken)
	if err != nil {
		return ProbeResult{Error: fmt.Sprintf("build probe request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", apiVersion)

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{
			LatencyMS: time.Since(started).Milliseconds(),
			Error:     fmt.Sprintf("request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out := ProbeResult{
		Status:    resp.StatusCode,
		LatencyMS: time.Since(started).Milliseconds(),
		RequestID: resp.Header.Get("request-id"),
	}
	if readErr != nil {
		out.Error = fmt.Sprintf("read response: %v", readErr)
		return out
	}

	if resp.StatusCode != http.StatusOK {
		// Surface the upstream's own message verbatim. It is the only thing
		// that distinguishes an expired token from a plan restriction from a
		// model the account cannot reach.
		out.Error = clip(strings.TrimSpace(string(raw)), 2000)
		return out
	}

	var msg anthropicwire.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		out.Error = fmt.Sprintf("decode response: %v", err)
		return out
	}

	var reply strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			reply.WriteString(c.Text)
		}
	}

	out.OK = true
	out.Model = msg.Model
	out.StopReason = msg.StopReason
	out.InputTokens = msg.Usage.InputTokens
	out.OutputTokens = msg.Usage.OutputTokens
	out.Reply = strings.TrimSpace(reply.String())
	return out
}

// smallestModel is the account's newest Haiku — the list is newest first —
// or its first model when it lists no Haiku.
func smallestModel(ctx context.Context, client *http.Client, accessToken string) (string, error) {
	method, path, _ := Provider{}.ModelsRequest("", provider.Credential{})
	req, err := newAccountRequest(ctx, method, path, nil, accessToken)
	if err != nil {
		return "", fmt.Errorf("build the model list request: %w", err)
	}
	req.Header.Set("anthropic-version", apiVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("read the model list: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read the model list: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the model list returned %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(raw)), 2000))
	}
	var l struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &l) != nil || len(l.Data) == 0 {
		return "", fmt.Errorf("the account lists no models")
	}
	for _, m := range l.Data {
		if strings.HasPrefix(m.ID, "claude-haiku-") {
			return m.ID, nil
		}
	}
	return l.Data[0].ID, nil
}
