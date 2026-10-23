//go:build linux

package providerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

func TestHintReplayAcrossFreshProcesses(t *testing.T) {
	for _, provider := range []string{"static", "choice", "fallback", "legacy"} {
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
						if provider == "choice" {
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
	if err != nil || projection.ResultCount != 1 || projection.ExchangeCount != 2 {
		t.Fatalf("%+v %v", projection, err)
	}
}
