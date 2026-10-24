package providerbridge

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLocalChooserUsesOnlyCandidateIDsAndLogprobConfidence(t *testing.T) {
	in := HintInput{ReasonCode: "policy_rule_deny", Tool: "write", Task: "Fix the task", Arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)}
	modelDigest := "sha256:" + strings.Repeat("a", 64)
	cases := []struct{ name, raw, want string }{
		{"high confidence", `{"choices":[{"text":"1","logprobs":{"tokens":["1"],"top_logprobs":[{"0":-3,"1":-0.1}]}}]}`, ""},
		{"low confidence", `{"choices":[{"text":"1","logprobs":{"tokens":["1"],"top_logprobs":[{"0":-0.6,"1":-0.7}]}}]}`, "low_confidence"},
		{"no logprobs", `{"choices":[{"text":"1"}]}`, "confidence_missing"},
		{"truncated logprobs", `{"choices":[{"text":"1","logprobs":{"tokens":["1"],"top_logprobs":[{"1":-0.1}]}}]}`, "confidence_missing"},
		{"wrong token", `{"choices":[{"text":"1","logprobs":{"tokens":[" 1"],"top_logprobs":[{"0":-3,"1":-0.1}]}}]}`, "confidence_missing"},
		{"free text", `{"choices":[{"text":"Use a different tool"}]}`, "invalid_candidate"},
		{"extra whitespace", `{"choices":[{"text":"1\n"}]}`, "invalid_candidate"},
		{"malformed", `{`, "chooser_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			cfg := ChooserConfig{Kind: "local", Enabled: true, BaseURL: "http://127.0.0.1:8080/v1", Model: "local/fixture", ModelDigest: modelDigest, Client: hintDoerFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "http://127.0.0.1:8080/v1/completions" || r.Header.Get("Authorization") != "" {
					t.Fatal(r)
				}
				var request localCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Temperature != 0 || request.MaxTokens != 2 || request.Logprobs != 2 || request.Grammar != `root ::= "0" | "1"` || !strings.Contains(request.Prompt, in.ReasonCode) || !strings.Contains(request.Prompt, HintCandidates(in.ReasonCode)["0"]) {
					t.Fatalf("%+v", request)
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("missing local request deadline")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.raw))}, nil
			})}
			got := newChoiceHintProvider(cfg).Hint(context.Background(), in)
			if calls != 1 || got.FallbackReason != tc.want || got.Transport != "local" || got.ModelDigest != modelDigest {
				t.Fatalf("%+v calls=%d", got, calls)
			}
			if tc.want == "" {
				if got.CandidateID != "1" || math.Abs(got.Confidence-1/(1+math.Exp(-2.9))) > 1e-12 {
					t.Fatal(got)
				}
			} else if got.CandidateID != "0" {
				t.Fatal(got)
			}
		})
	}
	for _, endpoint := range []string{"https://public.example/v1", "http://8.8.8.8/v1", "http://192.168.1.3@public.example/v1", "file:///v1", "http://127.0.0.1/v1?redirect=1"} {
		calls := 0
		cfg := ChooserConfig{Kind: "local", Enabled: true, BaseURL: endpoint, Model: "local/fixture", ModelDigest: modelDigest, Client: hintDoerFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })}
		if got := newChoiceHintProvider(cfg).Hint(context.Background(), in); calls != 0 || got.FallbackReason != "chooser_error" {
			t.Fatalf("%s %+v calls=%d", endpoint, got, calls)
		}
	}
	for _, endpoint := range []string{"http://localhost:8080/v1", "http://10.0.0.2:8080/v1", "https://192.168.1.4/v1", "http://[::1]:8080/v1"} {
		if !validLocalEndpoint(endpoint) {
			t.Fatal(endpoint)
		}
	}
}

func TestCDHLocalManifestPinsRouteAndModelFile(t *testing.T) {
	fileDigest := "sha256:" + strings.Repeat("a", 64)
	m, err := NewCDHLocalManifest("local/open-weight-fixture", "http://127.0.0.1:8080/v1", fileDigest, .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := m.Digest()
	p, err := NewCDHProvider(m, ChooserConfig{Model: m.Models[1], BaseURL: "https://wrong.invalid", ModelDigest: "wrong"})
	if err != nil || p.Transport != "local" || p.Endpoint != m.Transports[1].Endpoint || p.ModelDigest != fileDigest || p.ManifestDigest != first || p.Chooser != nil {
		t.Fatalf("%+v %v", p, err)
	}
	m.Transports[1].Endpoint = "http://192.168.1.3:8080/v1"
	second, err := m.Digest()
	if err != nil || first == second {
		t.Fatal("endpoint not pinned")
	}
	m.Transports[1].ModelDigest = "sha256:" + strings.Repeat("b", 64)
	third, err := m.Digest()
	if err != nil || second == third {
		t.Fatal("model file digest not pinned")
	}
	m.Transports[1].ModelDigest = ""
	if _, err := m.Digest(); err == nil {
		t.Fatal("missing model file digest accepted")
	}
}
