package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tbound/supervisor/internal/broker"
)

// ChooserConfig is host-only. No environment/key-file loader or default network client is installed.
// BaseURL is the API root (for example the approved host's /api/alpha); Model is an exact configured ID.
// A supplied client must honor request contexts and refuse redirects; local clients must disable proxies.
type ChooserConfig struct {
	Kind          string
	ModelDigest   string
	Enabled       bool
	BaseURL       string
	Model         string
	BearerToken   string
	Client        broker.HTTPDoer
	Timeout       time.Duration
	MinConfidence float64
}

type DecisionQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type DecisionsRequest struct {
	Model     string                      `json:"model"`
	State     HintInput                   `json:"state"`
	Questions map[string]DecisionQuestion `json:"questions"`
}

type DecisionsResponse struct {
	Answers map[string]ChoiceAnswer `json:"answers"`
	Model   string                  `json:"model"`
}

type decisionsChooser struct{ cfg ChooserConfig }

func newChoiceHintProvider(cfg ChooserConfig) ChoiceHintProvider {
	kind := cfg.Kind
	if kind == "" {
		kind = "decisions"
	}
	p := ChoiceHintProvider{Model: cfg.Model, Timeout: cfg.Timeout, MinConfidence: cfg.MinConfidence, Transport: kind, Endpoint: cfg.BaseURL, ModelDigest: cfg.ModelDigest}
	if cfg.Enabled {
		switch cfg.Kind {
		case "", "decisions":
			p.Chooser = decisionsChooser{cfg}
		case "local":
			p.Chooser = localChooser{cfg}
		}
	}
	return p
}

func (c decisionsChooser) Choose(ctx context.Context, in HintInput, candidates map[string]string) (ChoiceAnswer, error) {
	cfg := c.cfg
	if !cfg.Enabled || cfg.Client == nil || cfg.Model == "" {
		return ChoiceAnswer{}, errors.New("decision client disabled")
	}
	if !validDecisionsEndpoint(cfg.BaseURL) {
		return ChoiceAnswer{}, errors.New("invalid decisions base URL")
	}
	body, err := json.Marshal(DecisionsRequest{Model: cfg.Model, State: in, Questions: map[string]DecisionQuestion{
		"hint": {Type: "choice", Instructions: "Choose the most useful permitted alternative for this public denial; never change policy.", Criteria: candidates},
	}})
	if err != nil || len(body) > MaxPromptBytes+protocolArgumentBudget {
		return ChoiceAnswer{}, errors.New("decision input exceeds bound")
	}
	request, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.BaseURL, "/")+"/decisions", bytes.NewReader(body))
	if err != nil {
		return ChoiceAnswer{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
	}
	response, err := cfg.Client.Do(request)
	if err != nil {
		return ChoiceAnswer{}, err
	}
	if response == nil || response.Body == nil {
		return ChoiceAnswer{}, errors.New("empty decision response")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ChoiceAnswer{}, errors.New("decision HTTP status rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return ChoiceAnswer{}, errors.New("decision response exceeds bound")
	}
	var decoded DecisionsResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ChoiceAnswer{}, errors.New("invalid decision JSON")
	}
	answer, ok := decoded.Answers["hint"]
	if !ok {
		return ChoiceAnswer{}, errors.New("missing hint answer")
	}
	return answer, nil
}

const protocolArgumentBudget = 1 << 20

func validDecisionsEndpoint(endpoint string) bool {
	base, err := url.Parse(endpoint)
	return err == nil && base.Scheme == "https" && base.Host != "" && base.User == nil && base.RawQuery == "" && base.Fragment == ""
}
