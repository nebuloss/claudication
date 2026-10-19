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
)

// probePath is the Messages endpoint as the official client calls it.
const probePath = "/v1/messages?beta=true"

// ProbeResult is what a credential test learned.
type ProbeResult struct {
	OK           bool          `json:"ok"`
	Model        string        `json:"model,omitempty"`
	StopReason   string        `json:"stop_reason,omitempty"`
	InputTokens  int           `json:"input_tokens,omitempty"`
	OutputTokens int           `json:"output_tokens,omitempty"`
	Reply        string        `json:"reply,omitempty"`
	LatencyMS    int64         `json:"latency_ms"`
	Status       int           `json:"status"`
	RequestID    string        `json:"request_id,omitempty"`
	Error        string        `json:"error,omitempty"`
	latency      time.Duration `json:"-"`
}

// Probe sends the smallest useful request through an OAuth credential
// to prove it actually works end to end.
//
// The system prompt carries Claude Code's identity line because a subscription
// OAuth token is provisioned for that client and inference is refused without
// it. This is the gateway speaking as itself, not forging a client's
// attribution: no billing header or conversation fingerprint is synthesised,
// which is exactly the line the proxy paths must not cross either.
func Probe(ctx context.Context, client *http.Client, accessToken, model string) ProbeResult {
	if model == "" {
		model = "claude-haiku-4-5"
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
