//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/protocol"
)

func TestHintReplayAcrossFreshProcesses(t *testing.T) {
	for _, provider := range []string{"static", "choice", "local", "fallback", "legacy"} {
		for _, kind := range []string{"gate", "admission", "command_failed"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				cfg := recoveryTestConfig()
				if provider == "choice" || provider == "fallback" {
					chooser := chooserFunc(goodChoice)
					if provider == "fallback" {
						chooser = func(context.Context, HintInput, map[string]string) (ChoiceAnswer, error) {
							return ChoiceAnswer{}, context.DeadlineExceeded
						}
					}
					cfg.HintProvider = ChoiceHintProvider{Model: "offline/fixture", Chooser: chooser}
				}
				if provider == "local" {
					m, err := NewCDHLocalManifest("local/fixture", "http://127.0.0.1:8080/v1", "sha256:"+strings.Repeat("a", 64), .8, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					p, err := NewCDHProvider(m, ChooserConfig{Model: m.Models[1], Enabled: true, Client: hintDoerFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"text":"1","logprobs":{"tokens":["1"],"top_logprobs":[{"0":-3,"1":-0.1}]}}]}`))}, nil
					})})
					if err != nil {
						t.Fatal(err)
					}
					cfg.HintProvider = p
				}
				path := filepath.Join(t.TempDir(), "hint.audit")
				journal := openRecoveryTestJournal(t, path)
				tool, args := "write", json.RawMessage(`{"path":"task.txt","content":"x"}`)
				if kind == "command_failed" {
					tool, args = "bash", json.RawMessage(`{"command":"false"}`)
				}
				doer := &fixtureDoer{responses: []fixtureResponse{
					{responseID: "first", callID: "first-call", tool: tool, arguments: args},
					{responseID: "next", callID: "next-call", tool: "read", arguments: json.RawMessage(`{"path":"task.txt"}`)},
				}}
				conversation, err := newWithDoer(cfg, ApprovedModel, doer, journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := conversation.AdmitPrompt("task-hints", "Fix the public task"); err != nil {
					t.Fatal(err)
				}
				executor := &denyingExecutor{deny: map[int]*protocol.AdmissionDeniedError{}}
				if kind == "admission" {
					executor.deny[0] = protectedWriteDenial("effect-denial")
				}
				if kind == "command_failed" {
					executor.outputs = []json.RawMessage{mutationOutput("bash", "g1", recoveryTestTree("tree-1"), "transition-1", 1, "effect-1", &commandSummary{ExitCode: 3, ExitObserved: true, CommandContainmentStatus: "not-established", CommandRunnerProfile: "fixture-runner"})}
				}
				wrapped, err := NewExecutor(conversation, executor)
				if err != nil {
					t.Fatal(err)
				}
				_, _, err = driveDenialTurn(t, conversation, wrapped, recoveryTestPolicy(t, kind != "gate"), true)
				if kind == "admission" {
					var denied *protocol.AdmissionDeniedError
					if !errors.As(err, &denied) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if _, err := conversation.NextTurn(context.Background()); err != nil {
					t.Fatal(err)
				}
				trace, err := journal.Trace()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, r := range trace.Records {
					if r.Event.Kind == "provider_tool_result" || r.Event.Kind == "provider_admission_denied" {
						var e resultEvidence
						if err := json.Unmarshal(r.Event.Data, &e); err != nil {
							t.Fatal(err)
						}
						if e.Hint == nil {
							t.Fatal("missing hint metadata")
						}
						expected := "0"
						if provider == "choice" || provider == "local" {
							expected = "1"
						}
						if e.Hint.CandidateID != expected || (provider == "fallback" && e.Hint.FallbackReason != "timeout") {
							t.Fatalf("%+v", e.Hint)
						}
						if !strings.Contains(string(doer.requests[1]), HintCandidates(hintReasonMustDecode(t, e.Result))[expected]) {
							t.Fatalf("request lacks chosen hint: %s", doer.requests[1])
						}
						found = true
					}
				}
				if !found {
					t.Fatal("no result evidence")
				}
				if err := journal.Close(); err != nil {
					t.Fatal(err)
				}
				if provider == "legacy" {
					path = filepath.Join(t.TempDir(), "legacy.audit")
					legacy := openRecoveryTestJournal(t, path)
					for _, r := range trace.Records {
						event := r.Event
						switch event.Kind {
						case "provider_tool_result":
							var e resultEvidence
							_ = json.Unmarshal(event.Data, &e)
							e.Hint = nil
							event.Data, _ = json.Marshal(e)
						case "provider_admission_denied":
							var e admissionDeniedEvidence
							_ = json.Unmarshal(event.Data, &e)
							e.Hint = nil
							event.Data, _ = json.Marshal(e)
						}
						if _, err := legacy.Append(event); err != nil {
							t.Fatal(err)
						}
					}
					if err := legacy.Close(); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestHintReplaySubprocess$", "-test.v")
				cmd.Env = append(os.Environ(), "TBOUND_HINT_REPLAY_PATH="+path, "TBOUND_HINT_REPLAY_KIND="+kind)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh process: %v\n%s", err, output)
				}

				if provider == "choice" && kind == "gate" {
					changedPath := filepath.Join(t.TempDir(), "changed.audit")
					changed := openRecoveryTestJournal(t, changedPath)
					for _, r := range trace.Records {
						event := r.Event
						if event.Kind == "provider_tool_result" {
							var e resultEvidence
							if err := json.Unmarshal(event.Data, &e); err != nil {
								t.Fatal(err)
							}
							e.Hint.CandidateID = "0"
							event.Data, err = json.Marshal(e)
							if err != nil {
								t.Fatal(err)
							}
						}
						if _, err := changed.Append(event); err != nil {
							t.Fatal(err)
						}
					}
					if err := changed.Close(); err != nil {
						t.Fatal(err)
					}
					check := exec.Command(os.Args[0], "-test.run=^TestHintReplaySubprocess$", "-test.v")
					check.Env = append(os.Environ(), "TBOUND_HINT_REPLAY_PATH="+changedPath, "TBOUND_HINT_REPLAY_KIND="+kind, "TBOUND_HINT_REPLAY_REJECT=1")
					if output, err := check.CombinedOutput(); err != nil {
						t.Fatalf("fresh process tamper check: %v\n%s", err, output)
					}
				}
			})
		}
	}
}

func hintReasonMustDecode(t *testing.T, raw []byte) string {
	t.Helper()
	r, err := protocol.DecodeResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	return hintReason(r)
}

func TestHintReplaySubprocess(t *testing.T) {
	path := os.Getenv("TBOUND_HINT_REPLAY_PATH")
	if path == "" {
		return
	} // This helper is exercised by the parent, not a skipped test.
	cfg := recoveryTestConfig()
	cfg.HintProvider = ChoiceHintProvider{Model: "must-not-run", Chooser: chooserFunc(func(context.Context, HintInput, map[string]string) (ChoiceAnswer, error) {
		panic("replay called chooser")
	})}
	journal := openRecoveryTestJournal(t, path)
	projection, err := ReplayTranscript(journal, RecoveryPins{Config: cfg, Model: ApprovedModel, Policy: recoveryTestPolicy(t, os.Getenv("TBOUND_HINT_REPLAY_KIND") != "gate")}, &replayVerifierFixture{})
	if os.Getenv("TBOUND_HINT_REPLAY_REJECT") == "1" {
		if !errors.Is(err, ErrTranscriptReplay) || !strings.Contains(err.Error(), "byte-match") {
			t.Fatalf("changed hint did not fail byte identity: %v", err)
		}
		return
	}
	if err != nil || projection.ResultCount != 1 || projection.ExchangeCount != 2 {
		t.Fatalf("%+v %v", projection, err)
	}
}

func TestFallbackHintJournalRejectsEndpointCredentials(t *testing.T) {
	for _, kind := range []string{"decisions", "local"} {
		for _, endpoint := range []string{"http://127.0.0.1:8080/v1?key=credential-marker", "http://user:credential-marker@127.0.0.1:8080/v1"} {
			t.Run(kind+"/"+endpoint, func(t *testing.T) {
				cfg := recoveryTestConfig()
				cfg.HintProvider = newChoiceHintProvider(ChooserConfig{Kind: kind, BaseURL: endpoint, Model: "offline/fixture"})
				journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "credentials.audit"))
				defer journal.Close()
				conversation, err := newWithDoer(cfg, ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
					{responseID: "first", callID: "first-call", tool: "write", arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)},
				}}, journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := conversation.AdmitPrompt("task-hints", "Fix the public task"); err != nil {
					t.Fatal(err)
				}
				wrapped, err := NewExecutor(conversation, &denyingExecutor{})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := driveDenialTurn(t, conversation, wrapped, recoveryTestPolicy(t, false), true); err != nil {
					t.Fatal(err)
				}
				trace, err := journal.Trace()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, record := range trace.Records {
					if record.Event.Kind != "provider_tool_result" {
						continue
					}
					var evidence resultEvidence
					if err := json.Unmarshal(record.Event.Data, &evidence); err != nil {
						t.Fatal(err)
					}
					if evidence.Hint == nil {
						t.Fatal("missing hint selection")
					}
					encoded, err := json.Marshal(evidence.Hint)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(encoded), "credential-marker") || evidence.Hint.Endpoint != "" {
						t.Fatalf("endpoint credentials reached journal: %s", encoded)
					}
					found = true
				}
				if !found {
					t.Fatal("missing journaled result")
				}
			})
		}
	}
}

func TestHintReplayRejectsMixedMetadataAcrossFreshProcesses(t *testing.T) {
	if path := os.Getenv("TBOUND_MIXED_HINT_PATH"); path != "" {
		journal := openRecoveryTestJournal(t, path)
		defer journal.Close()
		projection, err := ReplayTranscript(journal, RecoveryPins{Config: recoveryTestConfig(), Model: ApprovedModel,
			Policy: recoveryTestPolicy(t, os.Getenv("TBOUND_MIXED_HINT_KIND") == "admission")}, &replayVerifierFixture{})
		if os.Getenv("TBOUND_MIXED_HINT_REJECT") == "1" {
			if !errors.Is(err, ErrTranscriptReplay) || !strings.Contains(err.Error(), "mixed hint metadata") {
				t.Fatalf("mixed journal was not rejected: %+v %v", projection, err)
			}
		} else if err != nil || projection.ResultCount != 2 {
			t.Fatalf("uniform journal failed: %+v %v", projection, err)
		}
		return
	}
	for _, kind := range []string{"gate", "admission"} {
		t.Run(kind, func(t *testing.T) {
			journal := openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "original.audit"))
			defer journal.Close()
			conversation, err := newWithDoer(recoveryTestConfig(), ApprovedModel, &fixtureDoer{responses: []fixtureResponse{
				{responseID: "first", callID: "first-call", tool: "write", arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)},
				{responseID: "second", callID: "second-call", tool: "write", arguments: json.RawMessage(`{"path":"task.txt","content":"y"}`)},
			}}, journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := conversation.AdmitPrompt("task-hints", "Fix the public task"); err != nil {
				t.Fatal(err)
			}
			executor := &denyingExecutor{deny: map[int]*protocol.AdmissionDeniedError{}}
			if kind == "admission" {
				executor.deny[0] = protectedWriteDenial("first-effect")
				executor.deny[1] = protectedWriteDenial("second-effect")
			}
			wrapped, err := NewExecutor(conversation, executor)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				_, _, err := driveDenialTurn(t, conversation, wrapped, recoveryTestPolicy(t, kind == "admission"), true)
				var denied *protocol.AdmissionDeniedError
				if (kind == "gate" && err != nil) || (kind == "admission" && !errors.As(err, &denied)) {
					t.Fatal(err)
				}
			}
			trace, err := journal.Trace()
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"all-present", "all-legacy", "present-then-legacy", "legacy-then-present"} {
				t.Run(mode, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "variant.audit")
					variant := openRecoveryTestJournal(t, path)
					index := 0
					for _, record := range trace.Records {
						event := record.Event
						if event.Kind == "provider_tool_result" || event.Kind == "provider_admission_denied" {
							strip := mode == "all-legacy" || (mode == "present-then-legacy" && index == 1) || (mode == "legacy-then-present" && index == 0)
							if strip {
								if event.Kind == "provider_tool_result" {
									var evidence resultEvidence
									if err := json.Unmarshal(event.Data, &evidence); err != nil {
										t.Fatal(err)
									}
									evidence.Hint = nil
									event.Data, err = json.Marshal(evidence)
								} else {
									var evidence admissionDeniedEvidence
									if err := json.Unmarshal(event.Data, &evidence); err != nil {
										t.Fatal(err)
									}
									evidence.Hint = nil
									event.Data, err = json.Marshal(evidence)
								}
								if err != nil {
									t.Fatal(err)
								}
							}
							index++
						}
						if _, err := variant.Append(event); err != nil {
							t.Fatal(err)
						}
					}
					if err := variant.Close(); err != nil {
						t.Fatal(err)
					}
					reject := "0"
					if strings.Contains(mode, "then") {
						reject = "1"
					}
					cmd := exec.Command(os.Args[0], "-test.run=^TestHintReplayRejectsMixedMetadataAcrossFreshProcesses$", "-test.count=1")
					cmd.Env = append(os.Environ(), "TBOUND_MIXED_HINT_PATH="+path, "TBOUND_MIXED_HINT_KIND="+kind, "TBOUND_MIXED_HINT_REJECT="+reject)
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("fresh replay: %v\n%s", err, output)
					}
				})
			}
		})
	}
}
