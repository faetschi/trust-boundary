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

// A bash pivot that the gate allowed and that exited nonzero is released to the source as an ALLOW Result with
// the command_failed status: the replay continues with the next library step, and only the later pivot that
// ran cleanly is the success pivot.
func TestAdaptiveSourceThroughTheRealConversationContinuesPastAFailedCommand(t *testing.T) {
	spec := adaptiveSpec(6)
	pivots := adaptivePivots(spec)
	pivots["ts-2"] = providerbridge.ScriptTurn{Tool: "read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	source, err := adaptivesource.NewAdaptiveSource(spec, pivots)
	if err != nil {
		t.Fatal(err)
	}
	journal := &providerbridge.TestJournal{}
	hostCalls := 0
	cfg := providerbridge.Config{
		ConversationID: "conversation-status", WorkflowID: "workflow-status", ProfileID: "profile-status",
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
	if err := conversation.AdmitPrompt("task-status", "Host-approved prompt."); err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "bash", Effect: gate.EffectAllow}, {Tool: "read", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	failed := providerbridge.MutationOutput("bash", "g1", "tree-g1", "t1", 1, "effect-1", &providerbridge.CommandSummary{ExitCode: 3, ExitObserved: true,
		CommandContainmentStatus: "established", CommandRunnerProfile: "d09"})
	executor, err := providerbridge.NewExecutor(conversation, providerbridge.NewDenyingExecutor(nil, []json.RawMessage{failed, providerbridge.ReadOutput("g1", "tree-g1", "baseline")}))
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
		if _, err := executor.Execute(context.Background(), proposal, decision); err != nil {
			t.Fatalf("execute turn %d: %v", i, err)
		}
	}
	final, err := conversation.NextTurn(context.Background())
	if err != nil || final.ToolCall != nil || !strings.Contains(final.AssistantText, adaptivesource.AdaptiveFinalText) {
		t.Fatalf("final turn = %+v, %v", final, err)
	}
	if strings.Join(tools, ",") != "bash,read" {
		t.Fatalf("pivots issued: %v (a failed command is not a success pivot)", tools)
	}
	terminal := source.Terminal()
	if terminal.Stop != adaptivesource.AdaptiveStopSuccessPivot || terminal.PivotsIssued != 2 {
		t.Fatalf("terminal = %+v", terminal)
	}
	found := false
	for _, event := range journal.Snapshot() {
		if event.Kind != "provider_tool_result" {
			continue
		}
		var evidence struct {
			Result []byte `json:"result"`
		}
		if err := json.Unmarshal(event.Data, &evidence); err != nil {
			t.Fatal(err)
		}
		result, err := protocol.DecodeResult(evidence.Result)
		if err != nil {
			t.Fatal(err)
		}
		if result.Tool == "bash" {
			found = true
			if result.Verdict != "ALLOW" || result.ReasonCode != "policy_rule_allow" || result.Status != protocol.ResultStatusCommandFailed {
				t.Fatalf("bash result = %+v", result)
			}
		}
	}
	if !found {
		t.Fatal("no bash result was journaled")
	}
}
