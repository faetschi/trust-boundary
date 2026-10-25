//go:build linux

package productprovider

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

type CheckResult struct {
	ModelMatch   bool   `json:"model_identity_match"`
	UsagePresent bool   `json:"usage_present"`
	Calls        int    `json:"calls"`
	MaxTokens    uint64 `json:"max_tokens"`
}

func (s *Store) Check(ctx context.Context, id string, maxTokens uint64) (CheckResult, error) {
	p, ok := Lookup(id)
	if !ok || maxTokens < p.MinCompletionTokens || maxTokens > p.MaxCompletionTokens {
		return CheckResult{}, errors.New("invalid profile or completion-token ceiling")
	}
	c, err := s.credentials(id)
	if err != nil {
		return CheckResult{}, err
	}
	d, err := c.NewCheckHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		return CheckResult{}, errors.New("provider transport configuration failed")
	}
	return checkWithDoer(ctx, p, maxTokens, d)
}

// checkWithDoer is the offline transport seam. The profile and outbound request
// still come only from the compiled table. No response text or raw transport
// error is ever returned, even when the provider echoes credentials.
func checkWithDoer(ctx context.Context, p Profile, maxTokens uint64, doer broker.HTTPDoer) (CheckResult, error) {
	compiled, ok := Lookup(p.ID)
	if !ok || maxTokens < compiled.MinCompletionTokens || maxTokens > compiled.MaxCompletionTokens {
		return CheckResult{}, errors.New("invalid profile or completion-token ceiling")
	}
	p = compiled
	prompt := "Reply with OK only. Do not call tools."
	b, err := broker.New(broker.Profile{
		ID: p.ID, Model: p.Model, Route: openrouter.EngineeringRoute(),
		Messages:     []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: "product-provider-check/tool/v1",
		ResponseIDIssuer: "product-provider-check/response/v1", Generation: "check",
	}, doer)
	if err != nil {
		return CheckResult{}, errors.New("provider transport configuration failed")
	}
	captured, exchangeErr := b.ExchangeWithCompletionLimit(ctx, maxTokens, nil)
	records := b.Records()
	result := CheckResult{Calls: len(records), MaxTokens: maxTokens}
	if len(records) != 1 {
		return result, errors.New("provider check did not dispatch exactly one request")
	}
	r := records[0]
	if r.HasResponse() {
		switch {
		case r.StatusCode() >= 300 && r.StatusCode() <= 399:
			return result, errors.New("provider redirect refused; verify the qualified endpoint with the administrator")
		case r.StatusCode() == http.StatusUnauthorized || r.StatusCode() == http.StatusForbidden:
			return result, errors.New("provider authentication failed; key may be expired or revoked: rotate the credential")
		case r.StatusCode() == http.StatusTooManyRequests || r.StatusCode() == http.StatusPaymentRequired:
			return result, errors.New("provider quota or rate limit exhausted (HTTP 429/402); check account credit and retry later explicitly")
		case r.StatusCode() != http.StatusOK:
			return result, errors.New("provider HTTP failure; check account and provider availability")
		}
		types := r.ContentTypes()
		if len(types) != 1 {
			return result, errors.New("unsupported provider protocol; expected Chat Completions text/event-stream")
		}
		media, _, err := mime.ParseMediaType(types[0])
		if err != nil || media != "text/event-stream" {
			return result, errors.New("unsupported provider protocol; expected Chat Completions text/event-stream")
		}
	}
	if exchangeErr != nil {
		if strings.Contains(exchangeErr.Error(), "response model differs") {
			return result, errors.New("provider response model identity mismatch; expected the qualified model, no fallback allowed")
		}
		if !r.HasResponse() || !r.ResponseComplete() {
			return result, errors.New("provider network failure; check DNS, TLS and connectivity, then retry explicitly")
		}
		return result, errors.New("unsupported or malformed provider protocol; expected bounded Chat Completions SSE")
	}
	result.ModelMatch = captured.Model() == p.Model
	var usage struct {
		Prompt     *uint64 `json:"prompt_tokens"`
		Completion *uint64 `json:"completion_tokens"`
		Total      *uint64 `json:"total_tokens"`
	}
	if json.Unmarshal(captured.Usage(), &usage) != nil || usage.Prompt == nil || usage.Completion == nil || usage.Total == nil || *usage.Completion > maxTokens || *usage.Prompt > *usage.Total || *usage.Completion != *usage.Total-*usage.Prompt {
		return result, errors.New("provider usage evidence missing or invalid; cannot verify token accounting")
	}
	result.UsagePresent = true
	return result, nil
}
