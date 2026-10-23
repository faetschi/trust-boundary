package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

const hintCandidateSet = "tbound-hint-candidates/v1"
const maxHintTimeout = 2 * time.Second

// HintInput contains only host-approved public context, never system prompts or policy internals.
// Arguments are canonical Pi arguments; the host must approve their disclosure before enabling a remote chooser.
type HintInput struct {
	ReasonCode string          `json:"reason_code"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Task       string          `json:"task"`
}

type HintSelection struct {
	Transport      string  `json:"transport,omitempty"`
	Endpoint       string  `json:"endpoint,omitempty"`
	ModelDigest    string  `json:"model_digest,omitempty"`
	ManifestDigest string  `json:"manifest_digest,omitempty"`
	CandidateSet   string  `json:"candidate_set"`
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	CandidateID    string  `json:"candidate_id"`
	Confidence     float64 `json:"confidence"`
	LatencyNS      int64   `json:"latency_ns"`
	FallbackReason string  `json:"fallback_reason"`
}

type HintProvider interface {
	Hint(context.Context, HintInput) HintSelection
}

type StaticHintProvider struct{}

func (StaticHintProvider) Hint(_ context.Context, in HintInput) HintSelection {
	id := ""
	if permittedAlternativeHints[in.ReasonCode] != "" {
		id = "0"
	}
	return HintSelection{CandidateSet: hintCandidateSet, Provider: "static", CandidateID: id}
}

// The v1 sentences and IDs are immutable replay data. Add a new version rather than editing these.
var alternativeHints = map[string]string{
	"scope_protected_path":     "Leave protected files unchanged and continue within the task's output scope.",
	"scope_path_not_covered":   "Keep changes within the task's permitted output scope.",
	"policy_rule_deny":         "Choose another available tool for the task's files.",
	"policy_default_deny":      "Continue with an available tool that the public policy permits.",
	"unknown_tool":             "Choose read, write, edit or bash for the next call.",
	"invalid_arguments":        "Check the tool schema and submit one call with corrected argument names and types.",
	"argument_digest_mismatch": "Submit a fresh tool call with a new call id and stable arguments.",
	"correlation_required":     "Submit a fresh provider tool call with a new call id.",
	"invalid_policy":           "Stop work and report that the task is blocked until the policy is available.",
	"command_failed":           "Inspect the command inputs before trying a corrected command inside the workspace.",
}

// HintCandidates returns a fresh per-reason choice set; callers cannot mutate replay tables.
func HintCandidates(reason string) map[string]string {
	if permittedAlternativeHints[reason] == "" {
		return nil
	}
	return map[string]string{"0": permittedAlternativeHints[reason], "1": alternativeHints[reason]}
}

type ChoiceAnswer struct {
	MissingConfidence bool               `json:"-"`
	Type              string             `json:"type"`
	Choice            string             `json:"choice"`
	Confidence        float64            `json:"confidence"`
	Probabilities     map[string]float64 `json:"probabilities"`
}

type HintChooser interface {
	Choose(context.Context, HintInput, map[string]string) (ChoiceAnswer, error)
}

type ChoiceHintProvider struct {
	Transport      string
	Endpoint       string
	ModelDigest    string
	ManifestDigest string
	Model          string
	Chooser        HintChooser
	// Zero uses 0.8. Values outside (0,1] fail closed.
	MinConfidence float64
	Timeout       time.Duration
	// Now permits deterministic offline latency measurements. Nil uses time.Now.
	Now func() time.Time
}

func (p ChoiceHintProvider) Hint(ctx context.Context, in HintInput) HintSelection {
	out := StaticHintProvider{}.Hint(ctx, in)
	out.Provider, out.Model, out.ManifestDigest = "choice", p.Model, p.ManifestDigest
	out.Transport, out.Endpoint, out.ModelDigest = p.Transport, p.Endpoint, p.ModelDigest
	fallback := func(reason string) HintSelection { out.FallbackReason = reason; return out }
	candidates := HintCandidates(in.ReasonCode)
	if len(candidates) == 0 {
		return fallback("unknown_reason")
	}
	threshold := p.MinConfidence
	if threshold == 0 {
		threshold = 0.8
	}
	if p.Chooser == nil || p.Model == "" {
		return fallback("disabled")
	}
	if math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
		return fallback("invalid_config")
	}
	timeout := p.Timeout
	if timeout <= 0 || timeout > maxHintTimeout {
		timeout = maxHintTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	now := p.Now
	if now == nil {
		now = time.Now
	}
	start := now()
	answer, err := p.Chooser.Choose(ctx, in, candidates)
	out.LatencyNS = max(int64(0), now().Sub(start).Nanoseconds())
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fallback("timeout")
	}
	if err != nil || ctx.Err() != nil {
		return fallback("chooser_error")
	}
	if answer.MissingConfidence {
		return fallback("confidence_missing")
	}
	if math.IsNaN(answer.Confidence) || math.IsInf(answer.Confidence, 0) || answer.Confidence < 0 || answer.Confidence > 1 {
		return fallback("invalid_response")
	}
	out.Confidence = answer.Confidence
	if answer.Type != "choice" || candidates[answer.Choice] == "" {
		return fallback("invalid_candidate")
	}
	if answer.Confidence < threshold {
		return fallback("low_confidence")
	}
	out.CandidateID = answer.Choice
	return out
}

func selectHint(ctx context.Context, provider HintProvider, in HintInput) HintSelection {
	if provider == nil {
		provider = StaticHintProvider{}
	}
	selection := provider.Hint(ctx, in)
	result := protocol.Result{Verdict: "DENY", ReasonCode: in.ReasonCode}
	if _, err := journaledResultText(nil, result, &selection); err != nil {
		selection = StaticHintProvider{}.Hint(ctx, in)
		selection.FallbackReason = "invalid_selection"
	}
	return selection
}

// Missing metadata is the round-1 static format. No chooser is ever called during replay.
func journaledResultText(encoded []byte, result protocol.Result, selection *HintSelection) (string, error) {
	if selection == nil {
		return modelResultText(encoded, result), nil
	}
	if (selection.Endpoint != "" && !((selection.Transport == "local" && validLocalEndpoint(selection.Endpoint)) ||
		(selection.Transport == "decisions" && validDecisionsEndpoint(selection.Endpoint)))) ||
		(selection.ManifestDigest != "" && !validModelFileDigest(selection.ManifestDigest)) ||
		(selection.Provider == "static" && (selection.Transport != "" || selection.Endpoint != "" || selection.ModelDigest != "")) ||
		(selection.FallbackReason == "" && selection.Transport == "local" && (!validLocalEndpoint(selection.Endpoint) || !validModelFileDigest(selection.ModelDigest))) ||
		(selection.FallbackReason == "" && selection.Transport == "decisions" && (!validDecisionsEndpoint(selection.Endpoint) || selection.ModelDigest != "")) ||
		(selection.Transport != "" && selection.Transport != "local" && selection.Transport != "decisions") {
		return "", errors.New("invalid journaled hint transport")
	}
	if selection.CandidateSet != hintCandidateSet ||
		(selection.Provider != "static" && selection.Provider != "choice") ||
		selection.LatencyNS < 0 || math.IsNaN(selection.Confidence) || math.IsInf(selection.Confidence, 0) ||
		selection.Confidence < 0 || selection.Confidence > 1 ||
		(selection.Provider == "static" && (selection.Model != "" || selection.CandidateID == "1")) ||
		(selection.Provider == "choice" && selection.FallbackReason == "" && selection.Model == "") ||
		(selection.FallbackReason != "" && selection.CandidateID == "1") {
		return "", errors.New("invalid journaled hint selection")
	}
	reason := hintReason(result)
	hint := HintCandidates(reason)[selection.CandidateID]
	if hint == "" && (selection.CandidateID != "" || permittedAlternativeHints[reason] != "") {
		return "", errors.New("journaled hint candidate is not in the frozen reason set")
	}
	text := string(encoded)
	if hint != "" {
		text += "\nhint: " + hint
	}
	return text, nil
}
