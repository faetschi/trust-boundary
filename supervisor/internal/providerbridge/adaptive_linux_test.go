//go:build linux

package providerbridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/adaptivesource"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

// The adaptive source through the real Conversation: a pivot is issued as a scripted provider response, an
// admission denial of it is released as a DENY Result in the next provider request, the source reads that
// Result and selects the next library step, and the first pivot that is not denied ends the replay.
func TestAdaptiveSourceDrivesTheRealConversationThroughADenialToASuccessPivot(t *testing.T) {
	spec := adaptiveSpec(6)
	pivots := adaptivePivots(spec)
	// the first library step is a write that delta admission denies; the step the machine selects after a
	// protected-path denial is a read
	pivots["ts-1"] = providerbridge.ScriptTurn{Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"x"}`)}
	pivots["drs-1"] = providerbridge.ScriptTurn{Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)}
	source, err := adaptivesource.NewAdaptiveSource(spec, pivots)
	if err != nil {
		t.Fatal(err)
	}
	journal := &providerbridge.TestJournal{}
	hostCalls := 0
	cfg := providerbridge.Config{
		ConversationID: "conversation-adaptive", WorkflowID: "workflow-adaptive", ProfileID: "profile-adaptive",
		InitialGenerationID: "g0", InitialTreeDigest: "tree-g0",
		SystemPrompt: "Fixture system prompt.", DeveloperPrompt: "Fixture developer profile.",
		SelectCompletionTokenLimit: func(context.Context) (uint64, error) { return 64, nil },
		ReserveHostCall: func(_ context.Context, request broker.RequestRecord, limit uint64) (providerbridge.HostCallBinding, error) {
			hostCalls++
			return providerbridge.HostCallBinding{CallID: fmt.Sprintf("host-call-%d", hostCalls), RequestDigest: broker.DigestRequestRecord(request), TokenUpperBound: limit}, nil
		},
		CompleteHostCall: func(context.Context, providerbridge.HostCallCompletion) error { return nil },
	}
	conversation, err := providerbridge.NewScriptedForTest(cfg, journal, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.AdmitPrompt("task-adaptive", "Host-approved prompt."); err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "write", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	denial := &protocol.AdmissionDeniedError{Tool: "write", ReasonCode: "scope_protected_path", EffectID: "effect-denied",
		Err: fmt.Errorf("candidate transition was denied by output-scope admission")}
	executor, err := providerbridge.NewExecutor(conversation, providerbridge.NewDenyingExecutor(map[int]*protocol.AdmissionDeniedError{0: denial},
		[]json.RawMessage{nil, providerbridge.ReadOutput("g0", "tree-g0", "baseline")}))
	if err != nil {
		t.Fatal(err)
	}
	var tools []string
	for i := 0; i < 2; i++ {
		turn, err := conversation.NextTurn(context.Background())
		if err != nil || turn.ToolCall == nil {
			t.Fatalf("turn %d = %+v, %v", i, turn, err)
		}
		tools = append(tools, turn.ToolCall.Name)
		proposal := providerbridge.ProposalFor(turn)
		matched, err := conversation.Correlate(context.Background(), proposal)
		if err != nil || !matched.Accepted {
			t.Fatalf("correlate turn %d: %+v %v", i, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if err := conversation.RecordDecision(context.Background(), proposal, decision); err != nil {
			t.Fatal(err)
		}
		_, execErr := executor.Execute(context.Background(), proposal, decision)
		if (i == 0) == (execErr == nil) {
			t.Fatalf("turn %d: execution error %v", i, execErr)
		}
	}
	final, err := conversation.NextTurn(context.Background())
	if err != nil || final.ToolCall != nil || !strings.Contains(final.AssistantText, adaptivesource.AdaptiveFinalText) {
		t.Fatalf("final turn = %+v, %v", final, err)
	}
	if strings.Join(tools, ",") != "write,read" {
		t.Fatalf("pivots issued: %v", tools)
	}
	terminal := source.Terminal()
	if terminal.Stop != adaptivesource.AdaptiveStopSuccessPivot || terminal.PivotsIssued != 2 || len(terminal.StateDigest) != 64 {
		t.Fatalf("terminal = %+v", terminal)
	}
	if hostCalls != 3 {
		t.Fatalf("scripted turns are host calls: %d, want 3 (two pivots and the final text)", hostCalls)
	}
}
