package providerbridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

type chooserFunc func(context.Context, HintInput, map[string]string) (ChoiceAnswer, error)

func (f chooserFunc) Choose(ctx context.Context, in HintInput, candidates map[string]string) (ChoiceAnswer, error) {
	return f(ctx, in, candidates)
}
func goodChoice(_ context.Context, _ HintInput, _ map[string]string) (ChoiceAnswer, error) {
	return ChoiceAnswer{Type: "choice", Choice: "1", Confidence: 0.95}, nil
}

func TestFrozenHintCandidates(t *testing.T) {
	raw, err := json.Marshal(frozenHintCandidates())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "e28ad5dac7c75e7cfb23a0fd22ff946218f5dffe8c56754724e31f01611f1d9e" {
		t.Fatalf("v1 candidate bytes changed: %s; retain v1 and add a new version", got)
	}

	if len(alternativeHints) != len(permittedAlternativeHints) {
		t.Fatal("reason coverage drift")
	}
	for reason, static := range permittedAlternativeHints {
		candidates := HintCandidates(reason)
		if len(candidates) != 2 || candidates["0"] != static {
			t.Fatalf("%s: %#v", reason, candidates)
		}
		for id, sentence := range candidates {
			if len(sentence) == 0 || len(sentence) > 128 || strings.ContainsAny(sentence, "\n\r/\\") {
				t.Fatalf("%s/%s invalid: %q", reason, id, sentence)
			}
		}
		candidates["0"] = "changed"
		if HintCandidates(reason)["0"] != static {
			t.Fatal("caller mutated frozen table")
		}
	}
}

func TestChoiceHintFallbacks(t *testing.T) {
	cases := []struct {
		name, want string
		answer     ChoiceAnswer
		err        error
	}{
		{name: "success", answer: ChoiceAnswer{Type: "choice", Choice: "1", Confidence: .9}},
		{name: "default chosen", answer: ChoiceAnswer{Type: "choice", Choice: "0", Confidence: .9}},
		{name: "low", want: "low_confidence", answer: ChoiceAnswer{Type: "choice", Choice: "1", Confidence: .79}},
		{name: "unknown", want: "invalid_candidate", answer: ChoiceAnswer{Type: "choice", Choice: "evil", Confidence: .9}},
		{name: "wrong type", want: "invalid_candidate", answer: ChoiceAnswer{Type: "score", Choice: "1", Confidence: .9}},
		{name: "nan", want: "invalid_response", answer: ChoiceAnswer{Type: "choice", Choice: "1", Confidence: math.NaN()}},
		{name: "range", want: "invalid_response", answer: ChoiceAnswer{Type: "choice", Choice: "1", Confidence: 1.1}},
		{name: "error", want: "chooser_error", err: errors.New("private remote failure")},
		{name: "timeout", want: "timeout", err: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ChoiceHintProvider{Model: "fixture/model", Chooser: chooserFunc(func(context.Context, HintInput, map[string]string) (ChoiceAnswer, error) { return tc.answer, tc.err })}
			out := p.Hint(context.Background(), HintInput{ReasonCode: "policy_rule_deny"})
			if out.FallbackReason != tc.want || (tc.want != "" && out.CandidateID != "0") {
				t.Fatalf("%+v", out)
			}
			if _, err := json.Marshal(out); err != nil {
				t.Fatal(err)
			}
		})
	}
	disabled := NewChoiceHintProvider(ChooserConfig{Model: "fixture/model"})
	if out := disabled.Hint(context.Background(), HintInput{ReasonCode: "policy_rule_deny"}); out.FallbackReason != "disabled" {
		t.Fatal(out)
	}
	p := ChoiceHintProvider{Model: "fixture/model", Chooser: chooserFunc(goodChoice), MinConfidence: 2}
	if out := p.Hint(context.Background(), HintInput{ReasonCode: "policy_rule_deny"}); out.FallbackReason != "invalid_config" {
		t.Fatal(out)
	}
	p.MinConfidence = 0
	if out := p.Hint(context.Background(), HintInput{ReasonCode: "unknown"}); out.FallbackReason != "unknown_reason" || out.CandidateID != "" {
		t.Fatal(out)
	}
	p.Timeout = time.Millisecond
	p.Chooser = chooserFunc(func(ctx context.Context, _ HintInput, _ map[string]string) (ChoiceAnswer, error) {
		<-ctx.Done()
		return goodChoice(ctx, HintInput{}, nil)
	})
	if out := p.Hint(context.Background(), HintInput{ReasonCode: "policy_rule_deny"}); out.FallbackReason != "timeout" {
		t.Fatal(out)
	}
}

type hintDoerFunc func(*http.Request) (*http.Response, error)

func (f hintDoerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestDecisionsClientOfflineContract(t *testing.T) {
	in := HintInput{ReasonCode: "policy_rule_deny", Tool: "write", Arguments: json.RawMessage(`{"content":"x","path":"task.txt"}`), Task: "Fix the task"}
	client := hintDoerFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://offline.invalid/api/alpha/decisions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fake" {
			t.Fatalf("request: %v", r)
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("unbounded request")
		}
		var request DecisionsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "fixture/decision" || !reflect.DeepEqual(request.State, in) || request.Questions["hint"].Type != "choice" || len(request.Questions["hint"].Criteria) != 2 {
			t.Fatalf("%+v", request)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"answers":{"hint":{"type":"choice","choice":"1","confidence":0.93,"probabilities":{"0":0.07,"1":0.93}}},"model":"fixture/decision"}`))}, nil
	})
	p := NewChoiceHintProvider(ChooserConfig{Enabled: true, BaseURL: "https://offline.invalid/api/alpha", Model: "fixture/decision", BearerToken: "fake", Client: client})
	got := p.Hint(context.Background(), in)
	if got.CandidateID != "1" || got.FallbackReason != "" || got.Confidence != .93 {
		t.Fatal(got)
	}
	for _, raw := range []string{`{`, `{}`, `{"answers":{"hint":{"type":"choice","choice":"unregistered","confidence":1}}}`, strings.Repeat("x", 65537)} {
		p = NewChoiceHintProvider(ChooserConfig{Enabled: true, BaseURL: "https://offline.invalid/api/alpha", Model: "fixture/decision", Client: hintDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})})
		if out := p.Hint(context.Background(), in); out.FallbackReason == "" || out.CandidateID != "0" {
			t.Fatal(out)
		}
	}
	for _, status := range []int{301, 400, 401, 429, 500} {
		p = NewChoiceHintProvider(ChooserConfig{Enabled: true, BaseURL: "https://offline.invalid/api/alpha", Model: "fixture/decision", Client: hintDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("private error"))}, nil
		})})
		if out := p.Hint(context.Background(), in); out.FallbackReason != "chooser_error" {
			t.Fatal(out)
		}
	}
}

func TestJournaledHintRejectsInvalidSelections(t *testing.T) {
	result := protocol.Result{Verdict: "DENY", ReasonCode: "scope_protected_path"}
	selection := ChoiceHintProvider{Model: "fixture/model", Chooser: chooserFunc(goodChoice)}.Hint(context.Background(), HintInput{ReasonCode: result.ReasonCode})
	text, err := journaledResultText([]byte("RESULT"), result, &selection)
	if err != nil || text != "RESULT\nhint: "+HintCandidates(result.ReasonCode)["1"] {
		t.Fatalf("%q %v", text, err)
	}
	for _, mutate := range []func(*HintSelection){
		func(h *HintSelection) { h.CandidateID = "nope" }, func(h *HintSelection) { h.Provider = "generate" },
		func(h *HintSelection) { h.CandidateSet = "v999" }, func(h *HintSelection) { h.LatencyNS = -1 },
		func(h *HintSelection) { h.Confidence = math.NaN() }, func(h *HintSelection) { h.FallbackReason = "timeout" },
	} {
		bad := selection
		mutate(&bad)
		if _, err := journaledResultText(nil, result, &bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	legacy, _ := journaledResultText([]byte("RESULT"), result, nil)
	if legacy != modelResultText([]byte("RESULT"), result) {
		t.Fatal("legacy static changed")
	}
}

func TestCDHManifestPinsTreatment(t *testing.T) {
	m, err := NewCDHManifest("offline/open-source-fixture", .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := m.Digest()
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("%s %v", digest, err)
	}
	raw, _ := json.Marshal(m)
	var roundtrip CDHManifest
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if got, _ := roundtrip.Digest(); got != digest {
		t.Fatal("unstable digest")
	}
	p, err := NewCDHProvider(m, ChooserConfig{Model: m.Models[1], Timeout: 2 * time.Second, MinConfidence: .99})
	if err != nil || p.ManifestDigest != digest || p.Timeout != time.Second || p.MinConfidence != .8 || p.Chooser != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := NewCDHProvider(m, ChooserConfig{Model: "unregistered"}); err == nil {
		t.Fatal("unregistered model accepted")
	}
	for _, model := range []string{"", "typesafe/jev-1.14"} {
		if _, err := NewCDHManifest(model, .8, time.Second); err == nil {
			t.Fatal("unresolved manifest accepted")
		}
	}
	roundtrip.MinConfidence = .9
	if got, _ := roundtrip.Digest(); got == digest {
		t.Fatal("threshold not pinned")
	}
	roundtrip.Candidates["policy_rule_deny"]["1"] = "Changed."
	if _, err := roundtrip.Digest(); err == nil {
		t.Fatal("candidate drift accepted")
	}
}
