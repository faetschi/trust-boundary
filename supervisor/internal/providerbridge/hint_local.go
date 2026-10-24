package providerbridge

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type localChooser struct{ cfg ChooserConfig }

type localCompletionRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Temperature float64  `json:"temperature"`
	MaxTokens   int      `json:"max_tokens"`
	Logprobs    int      `json:"logprobs"`
	Stop        []string `json:"stop"`
	Grammar     string   `json:"grammar"`
}

type localCompletionResponse struct {
	Choices []struct {
		Text     string `json:"text"`
		Logprobs *struct {
			Tokens      []string             `json:"tokens"`
			TopLogprobs []map[string]float64 `json:"top_logprobs"`
		} `json:"logprobs"`
	} `json:"choices"`
}

func validLocalEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func validModelFileDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil
}

// The local endpoint must be loopback or a literal private LAN address. No DNS discovery or fallback route exists.
func (c localChooser) Choose(ctx context.Context, in HintInput, candidates map[string]string) (ChoiceAnswer, error) {
	cfg := c.cfg
	if !cfg.Enabled || cfg.Client == nil || cfg.Model == "" || !validModelFileDigest(cfg.ModelDigest) || !validLocalEndpoint(cfg.BaseURL) {
		return ChoiceAnswer{}, errors.New("invalid or disabled local chooser")
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, strconv.Quote(id))
	}
	sort.Strings(ids)
	state, err := json.Marshal(struct {
		Input      HintInput         `json:"input"`
		Candidates map[string]string `json:"candidates"`
	}{in, candidates})
	if err != nil || len(state) > MaxPromptBytes+protocolArgumentBudget {
		return ChoiceAnswer{}, errors.New("local chooser input exceeds bound")
	}
	body, err := json.Marshal(localCompletionRequest{Model: cfg.Model, Prompt: "Choose the most useful permitted alternative; output exactly one candidate id.\n" + string(state), Temperature: 0, MaxTokens: 2, Logprobs: len(candidates), Stop: []string{"\n"}, Grammar: "root ::= " + strings.Join(ids, " | ")})
	if err != nil {
		return ChoiceAnswer{}, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.BaseURL, "/")+"/completions", bytes.NewReader(body))
	if err != nil {
		return ChoiceAnswer{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := cfg.Client.Do(request)
	if err != nil {
		return ChoiceAnswer{}, err
	}
	if response == nil || response.Body == nil {
		return ChoiceAnswer{}, errors.New("empty local completion")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ChoiceAnswer{}, errors.New("local completion status rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return ChoiceAnswer{}, errors.New("local completion exceeds bound")
	}
	var decoded localCompletionResponse
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.Choices) != 1 {
		return ChoiceAnswer{}, errors.New("invalid local completion JSON")
	}
	choice := decoded.Choices[0]
	answer := ChoiceAnswer{Type: "choice", Choice: choice.Text, MissingConfidence: true}
	if candidates[choice.Text] == "" {
		answer.MissingConfidence = false
		return answer, nil
	}
	// A complete candidate-token distribution is required; truncated top-k is not evidence of confidence.
	if choice.Logprobs == nil || len(choice.Logprobs.Tokens) != 1 || choice.Logprobs.Tokens[0] != choice.Text || len(choice.Logprobs.TopLogprobs) != 1 {
		return answer, nil
	}
	logs := choice.Logprobs.TopLogprobs[0]
	if len(logs) != len(candidates) {
		return answer, nil
	}
	highest := math.Inf(-1)
	for id := range candidates {
		value, ok := logs[id]
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value > 0 {
			return answer, nil
		}
		highest = math.Max(highest, value)
	}
	sum := 0.0
	for id := range candidates {
		sum += math.Exp(logs[id] - highest)
	}
	answer.Confidence = math.Exp(logs[choice.Text]-highest) / sum
	answer.MissingConfidence = false
	return answer, nil
}
