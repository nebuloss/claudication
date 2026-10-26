package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claudication/internal/geminiwire"
)

// Setting an account up, as gemini-cli's setupUser does (setup.ts) before its
// first turn: ask the backend which tier the account is on and which Cloud
// project bills it, and onboard it onto its default tier when it is on none.
//
// The project is the account's id here. Every inference request names it
// (converter.ts toGenerateContentRequest), and one Google sign-in has exactly
// one, so it is what account_uuid holds — as ChatGPT's workspace id is.
//
// Only the path gemini-cli takes with no GOOGLE_CLOUD_PROJECT set is
// followed: the free tier's managed project, and the paid tiers that come
// with one (Google AI Pro and Ultra). A Workspace or Cloud account that must
// name its own project is refused at sign-in with the backend's reasons,
// rather than signed in to fail on its first turn.

// clientMetadata is what Antigravity says about itself on setup calls, as
// CLIProxyAPI sends it (internal/auth/antigravity/auth.go). With
// Antigravity's User-Agent it put the account measured on the free tier,
// "Antigravity", with a managed project (testdata/antigravity-load.json).
var clientMetadata = map[string]string{"ideType": "ANTIGRAVITY"}

// tier is GeminiUserTier, in the fields read (types.ts).
type tier struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

// ineligibleTier says why a tier is not open to the account (types.ts).
type ineligibleTier struct {
	ReasonCode    string `json:"reasonCode"`
	ReasonMessage string `json:"reasonMessage"`
	ValidationURL string `json:"validationUrl"`
}

// loadResponse is LoadCodeAssistResponse (types.ts).
type loadResponse struct {
	CurrentTier             *tier            `json:"currentTier"`
	PaidTier                *tier            `json:"paidTier"`
	AllowedTiers            []tier           `json:"allowedTiers"`
	IneligibleTiers         []ineligibleTier `json:"ineligibleTiers"`
	CloudAICompanionProject string           `json:"cloudaicompanionProject"`
}

// operation is the long-running operation onboardUser answers (types.ts).
type operation struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
	// Error is a finished operation's failure, in Google's words.
	Error    *geminiwire.Status `json:"error"`
	Response struct {
		CloudAICompanionProject struct {
			ID string `json:"id"`
		} `json:"cloudaicompanionProject"`
	} `json:"response"`
}

// onboardPoll and onboardWait pace waiting on the onboarding operation.
// gemini-cli polls every five seconds with no limit; a sign-in is a request
// the operator is waiting on, so this gives up.
const (
	onboardPoll = 5 * time.Second
	onboardWait = 90 * time.Second
)

// identity is who an account is to the backend once set up.
type identity struct {
	Project string
	// Tier is the tier's name as Google shows it — the paid tier's when
	// there is one ("Google AI Pro"), the current tier's otherwise.
	Tier string
}

// setup finds the account's project and tier, onboarding it when it has none.
func setup(ctx context.Context, client *http.Client, token string) (identity, error) {
	var load loadResponse
	err := call(ctx, client, token, http.MethodPost, ":loadCodeAssist", map[string]any{"metadata": clientMetadata}, &load)
	if err != nil {
		return identity{}, fmt.Errorf("ask Code Assist about the account: %w", err)
	}
	if load.CurrentTier != nil {
		if load.CloudAICompanionProject == "" {
			return identity{}, ineligible(load, "the account is on a tier that needs a Google Cloud project of its own, which the gateway does not set")
		}
		return identity{Project: load.CloudAICompanionProject, Tier: tierName(load.PaidTier, load.CurrentTier)}, nil
	}
	if v := validation(load); v != "" {
		return identity{}, fmt.Errorf("Google wants the account verified before it can be used: open %s, then sign in again", v)
	}

	t := defaultTier(load)
	if t.ID == "" {
		return identity{}, ineligible(load, "the account is offered no Code Assist tier")
	}
	req := map[string]any{"tierId": t.ID, "metadata": clientMetadata}
	var op operation
	if err := call(ctx, client, token, http.MethodPost, ":onboardUser", req, &op); err != nil {
		return identity{}, fmt.Errorf("onboard the account onto %s: %w", firstOf(t.Name, t.ID), err)
	}
	deadline := time.Now().Add(onboardWait)
	for !op.Done && op.Name != "" {
		if time.Now().After(deadline) {
			return identity{}, errors.New("Code Assist is still onboarding the account; sign in again in a minute")
		}
		select {
		case <-ctx.Done():
			return identity{}, ctx.Err()
		case <-time.After(onboardPoll):
		}
		if err := call(ctx, client, token, http.MethodGet, "/"+op.Name, nil, &op); err != nil {
			return identity{}, fmt.Errorf("wait for onboarding: %w", err)
		}
	}
	if op.Error != nil && op.Error.Message != "" {
		return identity{}, fmt.Errorf("Code Assist refused to onboard the account: %s", op.Error.Message)
	}
	if op.Response.CloudAICompanionProject.ID == "" {
		return identity{}, ineligible(load, "onboarding named no project for the account")
	}
	return identity{Project: op.Response.CloudAICompanionProject.ID, Tier: firstOf(t.Name, t.ID)}, nil
}

// defaultTier is the tier onboarding puts the account on: the one the backend
// marks as default. gemini-cli falls back to the legacy tier with a project
// of the user's own, which is the path refused here.
func defaultTier(load loadResponse) tier {
	for _, t := range load.AllowedTiers {
		if t.IsDefault {
			return t
		}
	}
	return tier{}
}

func tierName(paid, current *tier) string {
	for _, t := range []*tier{paid, current} {
		if t != nil && firstOf(t.Name, t.ID) != "" {
			return firstOf(t.Name, t.ID)
		}
	}
	return ""
}

// validation is the address of a verification step the backend asks for
// before the account may be onboarded (setup.ts validateLoadCodeAssistResponse).
func validation(load loadResponse) string {
	for _, t := range load.IneligibleTiers {
		if t.ReasonCode == "VALIDATION_REQUIRED" && t.ValidationURL != "" {
			return t.ValidationURL
		}
	}
	return ""
}

// ineligible is a refusal at sign-in, with the backend's own reasons when it
// gave any.
func ineligible(load loadResponse, fallback string) error {
	var reasons []string
	for _, t := range load.IneligibleTiers {
		if t.ReasonMessage != "" {
			reasons = append(reasons, t.ReasonMessage)
		}
	}
	if len(reasons) > 0 {
		return fmt.Errorf("%s: %s", fallback, strings.Join(reasons, "; "))
	}
	return errors.New(fallback)
}

// callTimeout bounds one account call: a status or setup question.
const callTimeout = 30 * time.Second

// call is one JSON call to the Code Assist API outside a relayed request: a
// method on the versioned base (":loadCodeAssist") or a resource under it
// ("/operations/…").
func call(ctx context.Context, client *http.Client, token, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+"/"+apiVersion+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%d: %s", resp.StatusCode, describe(raw))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
