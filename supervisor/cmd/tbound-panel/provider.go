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
)

const openRouterEndpoint = "https://openrouter.ai/api/v1/chat/completions"

// provider sends one chat-completions request body and returns the raw response body.
type provider interface {
	complete(ctx context.Context, m modelConfig, body []byte) ([]byte, error)
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
		RequireParameters bool `json:"require_parameters"`
	} `json:"provider"`
}

func newRequest(m modelConfig, user string) chatRequest {
	r := chatRequest{Model: m.ID, Temperature: m.Temperature, MaxTokens: m.MaxTokens,
		Messages: []chatMessage{{"system", renderedSystemPrompt}, {"user", user}}}
	r.Usage.Include = true
	r.Provider.RequireParameters = true // only route to hosts that honour temperature and max_tokens
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

func (o *openRouter) complete(ctx context.Context, _ modelConfig, body []byte) ([]byte, error) {
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

// fakeProvider returns deterministic canned ratings for --dry-run and tests. A case's
// "true" rating per item comes from its stimulus text, so raters agree mostly; each rater
// deviates on a hashed minority of items, which gives a non-trivial kappa table.
type fakeProvider struct{}

var (
	fakeItemID = regexp.MustCompile(`"item_id": "(item-[0-9a-f]+)"`)
	fakeItem   = regexp.MustCompile(`(?m)^    "([a-z_]+)": \{"rating"`)
)

func fakeHash(parts ...string) uint64 {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return binary.BigEndian.Uint64(sum[:8])
}

func (fakeProvider) complete(_ context.Context, m modelConfig, body []byte) ([]byte, error) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) != 2 {
		return nil, fmt.Errorf("fake provider: bad request: %v", err)
	}
	user := req.Messages[1].Content
	stimulus, _, _ := strings.Cut(user, "## Source and adaptation")
	_, stimulus, _ = strings.Cut(stimulus, "verbatim")
	id := fakeItemID.FindStringSubmatch(user)
	if id == nil {
		return nil, errors.New("fake provider: no item id in prompt")
	}
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
	threat := "none"
	auth := "allowed"
	if strings.Contains(user, "Claimed population: adversarial") {
		threat, auth = []string{"TM-A", "TM-B"}[fakeHash("class", stimulus)%2], "forbidden"
	} else if strings.Contains(user, "Claimed population and proposed classification: ambiguous") {
		auth = "ambiguous"
	}
	content, _ := json.Marshal(map[string]any{"item_id": id[1], "checklist": checklist,
		"independent": map[string]any{"threat_class": threat, "authorization": auth, "effect_ids": []string{}}})
	promptTokens := (len(renderedSystemPrompt) + len(user)) / 4
	completionTokens := len(content) / 4
	cost := (float64(promptTokens)*m.PromptUSDPerMTok + float64(completionTokens)*m.CompletionUSDPerMTok) / 1e6
	return json.Marshal(map[string]any{
		"id": fmt.Sprintf("fake-%x", fakeHash(m.ID, id[1])), "model": m.ID, "provider": "fake",
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "cost": cost},
	})
}
