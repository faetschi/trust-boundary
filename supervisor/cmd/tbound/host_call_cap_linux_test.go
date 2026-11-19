//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

func capRead(index int) providerbridge.DevelopmentProviderTurn {
	return providerbridge.DevelopmentProviderTurn{ResponseID: fmt.Sprintf("cap-response-%d", index),
		CallID: fmt.Sprintf("cap-tool-call-%d", index), Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)}
}

var capRetryable = providerbridge.DevelopmentProviderTurn{FailStatus: 503,
	FailBody: `{"error":{"type":"server_error","message":"Upstream request failed: Endpoint is unavailable."}}`}

// hostCallCapWorkflow is a vertical workflow with the F26 compliant pilot cap of 7 host calls (11 since 2026-11-14) over turns. It
// runs reads read turns through the real Store effect and returns the fixture positioned before the next
// provider turn.
func hostCallCapWorkflow(t *testing.T, turns []providerbridge.DevelopmentProviderTurn, reads int) *workflowVerticalFixture {
	t.Helper()
	return limitedWorkflow(t, turns, reads, 7, 7*1024)
}

// limitedWorkflow is hostCallCapWorkflow with the given host-call and token allowances.
func limitedWorkflow(t *testing.T, turns []providerbridge.DevelopmentProviderTurn, reads int, maxHostCalls, maxTokens uint64) *workflowVerticalFixture {
	t.Helper()
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, maxHostCalls: maxHostCalls, maxTokens: maxTokens})
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, turns)
	})
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= reads; index++ {
		turn, err := f.conversation.NextTurn(f.ctx)
		if err != nil || turn.ToolCall == nil {
			t.Fatalf("read turn %d: turn=%+v err=%v", index, turn, err)
		}
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
			ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: turn.ToolCall.Arguments}
		matched, err := f.conversation.Correlate(f.ctx, proposal)
		if err != nil || !matched.Accepted {
			t.Fatalf("correlate read turn %d: %+v %v", index, matched, err)
		}
		decision := gate.Evaluate(proposal, matched, f.policy)
		if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
			t.Fatal(err)
		}
		if _, err := executor.Execute(f.ctx, proposal, decision); err != nil {
			t.Fatalf("execute read turn %d: %v", index, err)
		}
	}
	return f
}

// requireTypedCapEnd checks the typed budget end: the last provider call went out with tools disabled, the
// tool call the offline provider still returned was refused and never captured, and the turn ended as the
// final reply. The checkpoint has every host call settled and nothing pending, the receipt lineage shows the
// tools-disabled call and the refused call, and the governed verifier accepts the journal.
func requireTypedCapEnd(t *testing.T, f *workflowVerticalFixture, turn providerbridge.Turn, err error, hostCalls uint64, reads int) {
	t.Helper()
	if err != nil || turn.ToolCall != nil || turn.FinishReason != "stop" {
		t.Fatalf("last provider turn = %+v, %v; want the refused call to end the turn", turn, err)
	}
	checkpoint, _ := f.ledger.Snapshot()
	call := checkpoint.Pins.HostCallReservation
	if checkpoint.Counters.HostCalls != hostCalls || call == nil || call.Disposition != workflowrecovery.HostCallTerminal ||
		call.CapturedCall != nil || checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal {
		t.Fatalf("checkpoint after the typed end: pins=%+v counters=%+v", checkpoint.Pins, checkpoint.Counters)
	}
	trace, traceErr := f.providerJournal.Trace()
	if traceErr != nil {
		t.Fatal(traceErr)
	}
	var receipt productionPiReceipt
	if err := applyProviderLineage(&receipt, trace, f.config.Route); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(receipt)
	if len(receipt.ToolSequence) != reads || !strings.Contains(string(encoded), `"tools_disabled_calls":1`) ||
		!strings.Contains(string(encoded), `"refused_tool_calls":1`) {
		t.Fatalf("receipt lineage %s, want %d tools, one tools-disabled call and one refused call", encoded, reads)
	}
	if _, err := f.authority.resolveFromTrace(trace, "no-such-decision"); err == nil || !strings.Contains(err.Error(), "maps to 0 provider captures") {
		t.Fatalf("governed verifier did not walk the typed-end journal: %v", err)
	}
}

// F26 unit 90 and F29 units 63 and 90: three read turns charge 6 of 7 host calls and the fourth provider call
// takes the 7th. It is sent with tools disabled; the offline provider returns a tool call anyway, which is
// refused, so the run ends with a complete final turn instead of UNKNOWN.
func TestHostCallCapToolCallOnTheLastHostCallEndsTyped(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRead(1), capRead(2), capRead(3), capRead(4)}, 3)
	turn, err := f.conversation.NextTurn(f.ctx)
	requireTypedCapEnd(t, f, turn, err, 7, 3)
}

// A retried exchange charges a host call without an effect, so the provider calls move to even host-call
// numbers. The third read is then the 6th host call with 2 left: the effect would take the 7th and the call
// that returns its result could not be reserved, so it goes out with tools disabled and ends typed too.
func TestHostCallCapToolCallAfterARetryEndsTyped(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRetryable, capRead(1), capRead(2), capRead(3)}, 2)
	if checkpoint, _ := f.ledger.Snapshot(); checkpoint.Counters.HostCalls != 5 {
		t.Fatalf("a retry and two read turns charged %d host calls, want 5", checkpoint.Counters.HostCalls)
	}
	turn, err := f.conversation.NextTurn(f.ctx)
	requireTypedCapEnd(t, f, turn, err, 6, 2)
}

// The three-tool compliant pattern of F26 (8 of 22 done units): the fourth provider call takes the last
// host call and answers with text, which needs no effect, so the task still ends normally.
func TestHostCallCapTextOnTheLastHostCallEndsTheTask(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRead(1), capRead(2), capRead(3),
		{ResponseID: "cap-response-4", Text: "done"}}, 3)
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.AssistantText != "done" || turn.ToolCall != nil {
		t.Fatalf("final text turn: %+v %v", turn, err)
	}
	checkpoint, _ := f.ledger.Snapshot()
	if checkpoint.Counters.HostCalls != 7 || checkpoint.Pins.HostCallReservation == nil ||
		checkpoint.Pins.HostCallReservation.Disposition != workflowrecovery.HostCallTerminal {
		t.Fatalf("checkpoint after the final text turn: pins=%+v counters=%+v", checkpoint.Pins, checkpoint.Counters)
	}
}
