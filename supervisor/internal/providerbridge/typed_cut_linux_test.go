//go:build linux

package providerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/runend"
)

const typedCutCap = 4096

type typedCutRun struct {
	c       *Conversation
	journal *audit.Journal
	spend   *spendRecorder
	hooks   *retryHooks
	cfg     Config
	policy  gate.Policy
	model   string
}

func newTypedCutRun(t *testing.T, route openrouter.Route, limit uint64, bodies ...[]byte) *typedCutRun {
	t.Helper()
	cfg := recoveryTestConfig()
	cfg.Route = route
	run := &typedCutRun{cfg: cfg, spend: &spendRecorder{}, hooks: &retryHooks{}, model: ApprovedModel, policy: recoveryTestPolicy(t, true)}
	if route.IsResearch() {
		run.cfg.Spend, run.model = run.spend, route.Resolved().Model
	}
	run.hooks.apply(&run.cfg)
	run.cfg.SelectCompletionTokenLimit = func(context.Context) (uint64, error) { return limit, nil }
	run.journal = openRecoveryTestJournal(t, filepath.Join(t.TempDir(), "typed-cut.audit"))
	c, err := newWithDoer(run.cfg, run.model, &rawDoer{bodies: bodies}, run.journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AdmitPrompt("task-cut", "host task"); err != nil {
		t.Fatal(err)
	}
	run.c = c
	return run
}

func (r *typedCutRun) pins() RecoveryPins {
	return RecoveryPins{Config: r.cfg, Model: r.model, Policy: r.policy}
}

func (r *typedCutRun) driveCalls(t *testing.T, outputs ...json.RawMessage) {
	t.Helper()
	executor, err := NewExecutor(r.c, &fixtureExecutor{outputs: outputs})
	if err != nil {
		t.Fatal(err)
	}
	for index := range outputs {
		turn, err := r.c.NextTurn(context.Background())
		if err != nil || turn.ToolCall == nil {
			t.Fatalf("provider turn %d = %+v, %v", index+1, turn, err)
		}
		proposal := proposalFor(turn)
		matched, err := r.c.Correlate(context.Background(), proposal)
		if err != nil || !matched.Accepted {
			t.Fatalf("correlate turn %d = %+v, %v", index+1, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, r.policy)
		if err := r.c.RecordDecision(context.Background(), proposal, decision); err != nil {
			t.Fatal(err)
		}
		if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
			t.Fatalf("execute turn %d: %v", index+1, err)
		}
	}
}

func typedCutResearchRoute(t *testing.T) openrouter.Route {
	t.Helper()
	return spendTestRoute(t).WithReasoning(true)
}

func typedCutStream(model string, completionTokens int, deltas ...map[string]any) []byte {
	stream := sseChunks("resp-cut", model, "length", deltas...)
	usage, _ := json.Marshal(map[string]any{"id": "resp-cut", "model": model, "created": 1, "object": "chat.completion.chunk",
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 11, "completion_tokens": completionTokens, "cost": 0.000002}})
	return bytes.Replace(stream, []byte("data: [DONE]"), append([]byte("data: "+string(usage)+"\n\n"), []byte("data: [DONE]")...), 1)
}

func typedCutRecordedExchanges(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "typed-cut", "production-run-db8e5693e944db8fb0161e13-provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var responses [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record struct {
			Event audit.Event `json:"event"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Event.Kind != "provider_exchange" && record.Event.Kind != "provider_exchange_unknown" {
			continue
		}
		var exchange struct {
			Response []byte `json:"response"`
		}
		if err := json.Unmarshal(record.Event.Data, &exchange); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, exchange.Response)
	}
	return responses
}

func typedCutLastRecord(t *testing.T, journal *audit.Journal) audit.Record {
	t.Helper()
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) == 0 {
		t.Fatalf("journal trace: %v", err)
	}
	return trace.Records[len(trace.Records)-1]
}

func TestRealCutStreamEndsTheRunTypedAndKeepsItsEarlierWork(t *testing.T) {
	responses := typedCutRecordedExchanges(t)
	cut, err := os.ReadFile(filepath.Join("testdata", "typed-cut", "cut-response.sse"))
	if err != nil || len(responses) != 3 || !bytes.Equal(responses[2], cut) {
		t.Fatalf("recorded exchanges: %d, cut stream matches %v, %v", len(responses), len(responses) == 3 && bytes.Equal(responses[2], cut), err)
	}
	run := newTypedCutRun(t, typedCutResearchRoute(t), typedCutCap, responses...)
	run.driveCalls(t,
		readOutputFor("docs/sgf-parsing.instructions.md", "g0", recoveryTestTree("tree-0"), "instructions"),
		mutationOutput("bash", "g1", recoveryTestTree("tree-1"), "transition-000001", 1, "effect-1", &commandSummary{
			ExitCode: 0, ExitObserved: true, CommandContainmentStatus: "not-established", CommandRunnerProfile: "fixture-runner"}))
	turn, err := run.c.NextTurn(context.Background())
	if err != nil || turn.ToolCall != nil || turn.FinishReason != "stop" || turn.AssistantText != "" {
		t.Fatalf("the cut turn = %+v, %v; want a final stop turn with no content", turn, err)
	}
	if record := typedCutLastRecord(t, run.journal); record.Event.Kind != "provider_exchange" || !bytes.Contains(record.Event.Data, []byte(`"ceiling_typed_end":true`)) {
		t.Fatalf("the cut was journaled as %q", record.Event.Kind)
	}
	if len(run.hooks.completions) != 3 || !strings.Contains(string(run.spend.usage), `"completion_tokens":4096`) ||
		run.spend.sequence()[len(run.spend.sequence())-1] != "settle:unit-1" {
		t.Fatalf("host calls settled %d, spend %v, usage %s", len(run.hooks.completions), run.spend.sequence(), run.spend.usage)
	}
	if _, err := run.c.NextTurn(context.Background()); !errors.Is(err, ErrWrongState) {
		t.Fatalf("a turn after the final cut turn: %v", err)
	}
	projection, err := ReplayTranscript(run.journal, run.pins(), &replayVerifierFixture{})
	if err != nil || projection.ProviderSequence != 3 || projection.ExchangeCount != 3 {
		t.Fatalf("replay of the typed cut: %+v, %v", projection, err)
	}
}

func TestCutReplyWithACompleteToolCallRunsAndReleasesNothing(t *testing.T) {
	model := spendTestModel
	call := map[string]any{"index": 0, "id": "call-cut", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"touch x"}`}}
	body := typedCutStream(model, typedCutCap, map[string]any{"role": "assistant", "content": "partial text"},
		map[string]any{"reasoning_content": "thinking"}, map[string]any{"tool_calls": []any{call}})
	run := newTypedCutRun(t, typedCutResearchRoute(t), typedCutCap, body)
	turn, err := run.c.NextTurn(context.Background())
	if err != nil || turn.ToolCall != nil || turn.AssistantText != "" || turn.FinishReason != "stop" {
		t.Fatalf("the cut turn = %+v, %v; want nothing released", turn, err)
	}
	var evidence exchangeEvidence
	record := typedCutLastRecord(t, run.journal)
	if err := json.Unmarshal(record.Event.Data, &evidence); err != nil || record.Event.Kind != "provider_exchange" ||
		evidence.ToolCall != nil || evidence.RefusedToolCall != nil || !bytes.Contains(evidence.Response, []byte("partial text")) {
		t.Fatalf("journaled exchange %q: tool call %+v, refused %+v, err %v", record.Event.Kind, evidence.ToolCall, evidence.RefusedToolCall, err)
	}
	proposal := proposalFor(Turn{ToolCall: &ToolCallProjection{ID: "call-cut", Name: "bash", Arguments: json.RawMessage(`{"command":"touch x"}`)}})
	if matched, err := run.c.Correlate(context.Background(), proposal); err == nil && matched.Accepted {
		t.Fatal("the call of a cut reply was captured")
	}
	last := run.c.history[len(run.c.history)-1]
	if last.Content == nil || *last.Content != "" || last.Reasoning != "" || len(last.ToolCalls) != 0 {
		t.Fatalf("history kept the cut reply: %+v", last)
	}
}

func TestReplayAcceptsARecordedTypedCutAndRefusesAForgedOne(t *testing.T) {
	model := spendTestModel
	body := typedCutStream(model, typedCutCap, map[string]any{"role": "assistant", "reasoning_content": "still thinking"})
	run := newTypedCutRun(t, typedCutResearchRoute(t), typedCutCap, body)
	if _, err := run.c.NextTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayTranscript(run.journal, run.pins(), &replayVerifierFixture{}); err != nil {
		t.Fatalf("the recorded typed cut: %v", err)
	}
	trace, err := run.journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	for _, forgery := range []struct {
		name     string
		evidence func(*exchangeEvidence)
		raw      func([]byte) []byte
	}{
		{"finish length below the cap", func(e *exchangeEvidence) {
			e.Response = typedCutStream(model, typedCutCap-1, map[string]any{"role": "assistant", "reasoning_content": "still thinking"})
		}, nil},
		{"edited stream that is no cut", func(e *exchangeEvidence) {
			e.Response = bytes.Replace(e.Response, []byte(`"finish_reason":"length"`), []byte(`"finish_reason":"stop"`), 1)
		}, nil},
		{"typed mark removed from a cut", nil, func(data []byte) []byte {
			return bytes.Replace(data, []byte(`,"ceiling_typed_end":true`), nil, 1)
		}},
	} {
		events := make([]audit.Event, len(trace.Records))
		for i, record := range trace.Records {
			events[i] = record.Event
			if record.Event.Kind != "provider_exchange" {
				continue
			}
			data := record.Event.Data
			if forgery.evidence != nil {
				var evidence exchangeEvidence
				if err := json.Unmarshal(data, &evidence); err != nil {
					t.Fatal(err)
				}
				forgery.evidence(&evidence)
				if data, err = json.Marshal(evidence); err != nil {
					t.Fatal(err)
				}
			}
			if forgery.raw != nil {
				data = forgery.raw(data)
			}
			events[i].Data = data
		}
		if _, err := ReplayTranscript(rechainRecoveryEvents(t, events), run.pins(), &replayVerifierFixture{}); !errors.Is(err, ErrTranscriptReplay) {
			t.Errorf("%s: replay = %v, want ErrTranscriptReplay", forgery.name, err)
		}
	}
}

func TestSiblingCeilingsStayCeilings(t *testing.T) {
	model := spendTestModel
	research := typedCutResearchRoute(t)
	thinking := map[string]any{"role": "assistant", "reasoning_content": "t"}
	manyEvents := make([]map[string]any, openrouter.MaxReasoningSSEEvents+1)
	for i := range manyEvents {
		manyEvents[i] = map[string]any{"reasoning_content": "t"}
	}
	for _, tc := range []struct {
		name  string
		route openrouter.Route
		limit uint64
		body  []byte
	}{
		{"SSE event count", research, typedCutCap, typedCutStream(model, typedCutCap, manyEvents...)},
		{"length below the cap", research, typedCutCap, typedCutStream(model, typedCutCap-1, thinking)},
		{"length at a different cap", research, 2 * typedCutCap, typedCutStream(model, typedCutCap, thinking)},
		{"length without usage", research, typedCutCap, sseChunks("resp-cut", model, "length", thinking)},
		{"engineering route", openrouter.Route{}, 512, typedCutStream(openrouter.ApprovedModel, 512, thinking)},
	} {
		run := newTypedCutRun(t, tc.route, tc.limit, tc.body)
		_, err := run.c.NextTurn(context.Background())
		var journaled journaledFailure
		if !errors.Is(err, runend.ErrCeiling) || !errors.As(err, &journaled) {
			t.Errorf("%s: %v, want a journaled ceiling", tc.name, err)
			continue
		}
		if record := typedCutLastRecord(t, run.journal); record.Event.Kind != "provider_exchange_unknown" {
			t.Errorf("%s: journaled as %q", tc.name, record.Event.Kind)
		}
	}
}
