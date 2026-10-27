package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"tbound/supervisor/internal/panelaudit"
)

const openRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"

// provider sends one chat-completions request body and returns the raw response body.
type provider interface {
	complete(ctx context.Context, m panelaudit.Model, body []byte) ([]byte, error)
}

// errNoCharge marks a provider error that certainly cost nothing (an HTTP error status).
var errNoCharge = errors.New("provider refused the request")

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is one fresh, single-turn, tool-free request.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Usage       struct {
		Include bool `json:"include"`
	} `json:"usage"`
	Provider struct {
		RequireParameters bool   `json:"require_parameters"`
		DataCollection    string `json:"data_collection"`
	} `json:"provider"`
}

func newRequest(m panelaudit.Model, phase int, user string) chatRequest {
	r := chatRequest{Model: m.ID, Temperature: m.Temperature, MaxTokens: m.MaxTokens,
		Messages: []chatMessage{{"system", systemPrompts[phase]}, {"user", user}}}
	r.Usage.Include = true
	r.Provider.RequireParameters = true // only route to hosts that honour temperature and max_tokens
	r.Provider.DataCollection = "deny"  // cases include MultiPL-E, whose license forbids ML-training use
	return r
}

// chatResponse is the part of an OpenRouter response the panel reads.
type chatResponse struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Choices  []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		Cost             *float64 `json:"cost"`
	} `json:"usage"`
}

// openRouter is the live provider. The key lives only in this struct and the request header.
type openRouter struct {
	endpoint string
	key      string
	client   *http.Client
}

func (o *openRouter) complete(ctx context.Context, _ panelaudit.Model, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("%w: HTTP %d: %.300s", errNoCharge, resp.StatusCode, data)
	}
	return data, nil
}

// fakeProvider returns deterministic canned answers for --dry-run and tests. A case's
// "true" answer comes from its stimulus text, so raters agree mostly; each rater deviates
// on a hashed minority, which gives non-trivial agreement tables.
type fakeProvider struct{}

var (
	fakeItemID = regexp.MustCompile(`"item_id": "(item-[0-9a-f]+)"`)
	fakeItem   = regexp.MustCompile(`(?m)^    "([a-z_]+)": \{"rating"`)
)

func fakeHash(parts ...string) uint64 {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return binary.BigEndian.Uint64(sum[:8])
}

func (fakeProvider) complete(_ context.Context, m panelaudit.Model, body []byte) ([]byte, error) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) != 2 {
		return nil, fmt.Errorf("fake provider: bad request: %v", err)
	}
	user := req.Messages[1].Content
	_, stimulus, _ := strings.Cut(user, "verbatim")
	stimulus, _, _ = strings.Cut(stimulus, "\n## ")
	id := fakeItemID.FindStringSubmatch(user)
	if id == nil {
		return nil, errors.New("fake provider: no item id in prompt")
	}
	deviates := func(field string) bool { return fakeHash("rater", m.ID, stimulus, field)%12 == 0 }
	var answer map[string]any
	if strings.Contains(user, `"completion_predicate"`) {
		// Phase 1: a crude stimulus reading stands in for a model's own classification.
		threat, auth, effects, pred := "none", "allowed", []string{}, "the requested output exists and nothing else changed"
		if strings.Contains(stimulus, "127.0.33.1") || strings.Contains(stimulus, "Injected") {
			threat, auth, effects, pred = []string{"TM-A", "TM-B"}[fakeHash("class", stimulus)%2], "forbidden", []string{"EFF-NET-01"}, ""
		}
		if deviates("threat_class") {
			threat = "TM-C"
		}
		if deviates("authorization") {
			auth = "ambiguous"
		}
		if deviates("effect_ids") {
			effects = append(effects, "EFF-WS-01")
		}
		answer = map[string]any{"item_id": id[1], "threat_class": threat, "authorization": auth, "effect_ids": effects, "completion_predicate": pred}
	} else {
		checklist := map[string]map[string]string{}
		for _, it := range fakeItem.FindAllStringSubmatch(user, -1) {
			truth := "pass"
			if fakeHash("truth", stimulus, it[1])%7 == 0 {
				truth = "fail"
			}
			rating := truth
			if d := fakeHash("rater", m.ID, stimulus, it[1]) % 20; d == 0 {
				rating = "unclear"
			} else if d == 1 {
				rating = map[string]string{"pass": "fail", "fail": "pass"}[truth]
			}
			checklist[it[1]] = map[string]string{"rating": rating, "reason": "fake dry-run rating"}
		}
		answer = map[string]any{"item_id": id[1], "checklist": checklist}
	}
	content, _ := json.Marshal(answer)
	promptTokens := (len(req.Messages[0].Content) + len(user)) / 4
	completionTokens := len(content) / 4
	cost := (float64(promptTokens)*m.PromptUSDPerMTok + float64(completionTokens)*m.CompletionUSDPerMTok) / 1e6
	return json.Marshal(map[string]any{
		"id": fmt.Sprintf("fake-%x", fakeHash(m.ID, id[1])), "model": m.ID, "provider": "fake",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "cost": cost},
	})
}
