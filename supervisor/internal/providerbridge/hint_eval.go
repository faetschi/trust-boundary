package providerbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// HintEvalFixture contains only fake or separately captured recordings; evaluating it never opens a network client.
type HintEvalFixture struct {
	SchemaVersion string             `json:"schema_version"`
	Note          string             `json:"note"`
	Scenarios     []HintEvalScenario `json:"scenarios"`
}

type HintEvalScenario struct {
	ID         string                   `json:"id"`
	Source     string                   `json:"source"`
	Input      HintInput                `json:"input"`
	StaticNext string                   `json:"static_next"`
	Recordings map[string]HintRecording `json:"recordings"`
}

type HintRecording struct {
	Answer    ChoiceAnswer `json:"answer"`
	Failure   string       `json:"failure,omitempty"`
	LatencyMS int64        `json:"latency_ms"`
	Next      string       `json:"next"`
}

func (r HintRecording) Choose(context.Context, HintInput, map[string]string) (ChoiceAnswer, error) {
	switch r.Failure {
	case "":
		return r.Answer, nil
	case "timeout":
		return ChoiceAnswer{}, context.DeadlineExceeded
	default:
		return ChoiceAnswer{}, errors.New("recorded chooser error")
	}
}

type HintEvalReport struct {
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	Scenarios          int     `json:"scenarios"`
	FallbackRate       float64 `json:"fallback_rate"`
	CheckRejectionRate float64 `json:"check_rejection_rate"`
	MeanLatencyMS      float64 `json:"mean_latency_ms"`
	P95LatencyMS       int64   `json:"p95_latency_ms"`
	MeanHintBytes      float64 `json:"mean_hint_bytes"`
	MaxHintBytes       int     `json:"max_hint_bytes"`
	// Counts reflect the supplied next-proposal recordings, not predictions or authorization.
	AlternativeAllowed int `json:"alternative_allowed"`
	RetryDenied        int `json:"retry_denied"`
	NoFurtherAction    int `json:"no_further_action"`
}

// EvaluateHintFixture compares static with N recorded decision models on identical scenarios.
func EvaluateHintFixture(f HintEvalFixture, confidence float64, timeout time.Duration) ([]HintEvalReport, error) {
	if f.SchemaVersion != "tbound-hint-eval/v1" || len(f.Scenarios) == 0 {
		return nil, errors.New("invalid hint evaluation fixture")
	}
	models := []string{""}
	for model := range f.Scenarios[0].Recordings {
		if model == "" {
			return nil, errors.New("empty chooser model")
		}
		models = append(models, model)
	}
	sort.Strings(models[1:])
	reports := make([]HintEvalReport, 0, len(models))
	seen := map[string]bool{}
	for _, s := range f.Scenarios {
		if s.ID == "" || seen[s.ID] || s.Source == "" || len(HintCandidates(s.Input.ReasonCode)) == 0 || len(s.Recordings) != len(models)-1 {
			return nil, fmt.Errorf("invalid scenario %q", s.ID)
		}
		seen[s.ID] = true
	}
	for _, model := range models {
		report := HintEvalReport{Provider: "static", Model: model, Scenarios: len(f.Scenarios)}
		latencies := make([]int64, 0, len(f.Scenarios))
		var fallback, rejected, totalBytes, totalMS int64
		for _, scenario := range f.Scenarios {
			var provider HintProvider = StaticHintProvider{}
			next := scenario.StaticNext
			latency := int64(0)
			if model != "" {
				report.Provider = "choice"
				recording, ok := scenario.Recordings[model]
				if !ok || recording.LatencyMS < 0 || recording.LatencyMS > maxHintTimeout.Milliseconds() || (recording.Failure != "" && recording.Failure != "timeout" && recording.Failure != "error") {
					return nil, fmt.Errorf("invalid recording %s/%s", scenario.ID, model)
				}
				latency = recording.LatencyMS
				ticks := 0
				provider = ChoiceHintProvider{Model: model, Chooser: recording, MinConfidence: confidence, Timeout: timeout, Now: func() time.Time {
					ticks++
					return time.Unix(0, 0).Add(time.Duration(ticks-1) * time.Duration(latency) * time.Millisecond)
				}}
				next = recording.Next
			}
			chosen := selectHint(context.Background(), provider, scenario.Input)
			if chosen.FallbackReason != "" {
				fallback++
			}
			if chosen.FallbackReason == "invalid_candidate" || chosen.FallbackReason == "invalid_response" || chosen.FallbackReason == "invalid_selection" {
				rejected++
			}
			length := len(HintCandidates(scenario.Input.ReasonCode)[chosen.CandidateID])
			totalBytes += int64(length)
			report.MaxHintBytes = max(report.MaxHintBytes, length)
			ms := chosen.LatencyNS / int64(time.Millisecond)
			totalMS += ms
			latencies = append(latencies, ms)
			switch next {
			case "alternative_allowed":
				report.AlternativeAllowed++
			case "retry_denied":
				report.RetryDenied++
			case "no_further_action":
				report.NoFurtherAction++
			default:
				return nil, fmt.Errorf("invalid continuation %s/%s", scenario.ID, model)
			}
		}
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		count := float64(report.Scenarios)
		report.FallbackRate = float64(fallback) / count
		report.CheckRejectionRate = float64(rejected) / count
		report.MeanLatencyMS = float64(totalMS) / count
		report.P95LatencyMS = latencies[(95*len(latencies)+99)/100-1]
		report.MeanHintBytes = float64(totalBytes) / count
		reports = append(reports, report)
	}
	return reports, nil
}
