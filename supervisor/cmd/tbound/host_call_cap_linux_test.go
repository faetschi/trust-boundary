//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, maxHostCalls: 7, maxTokens: 7 * 1024})
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

// requireAbandonableCapEnd checks that the refused provider turn left the checkpoint in the state abandon
// reconciles: hostCalls charged, the last one reserved and closed by an unknown exchange, no captured or
// pending call, and the previous effect terminal.
func requireAbandonableCapEnd(t *testing.T, f *workflowVerticalFixture, err error, hostCalls uint64) {
	t.Helper()
	if !errors.Is(err, providerbridge.ErrNoHostCallForToolEffect) {
		t.Fatalf("provider turn = %v, want ErrNoHostCallForToolEffect", err)
	}
	checkpoint, _ := f.ledger.Snapshot()
	call := checkpoint.Pins.HostCallReservation
	if checkpoint.Counters.HostCalls != hostCalls || call == nil || call.Disposition != workflowrecovery.HostCallReserved ||
		call.CapturedCall != nil || checkpoint.Pins.PendingCallDisposition != workflowrecovery.PendingCallTerminal ||
		checkpoint.Pins.EffectReservation == nil ||
		checkpoint.Pins.EffectReservation.Disposition != workflowrecovery.EffectReservationTerminal {
		t.Fatalf("checkpoint after the refused tool call: pins=%+v counters=%+v", checkpoint.Pins, checkpoint.Counters)
	}
	trace, traceErr := f.providerJournal.Trace()
	if traceErr != nil {
		t.Fatal(traceErr)
	}
	if !unknownExchangeClosesHostCall(checkpoint, trace, workflowVerticalID, workflowVerticalConversation) {
		t.Fatal("the provider journal does not close the reserved host call as unknown")
	}
	if err := validateSettledServiceDisposition(checkpoint.Pins, workflowVerticalConversation, true, false, false); err != nil {
		t.Fatalf("abandon would refuse the checkpoint: %v", err)
	}
}

// F26 unit 90: three read turns charge 6 of 7 host calls, the fourth provider call takes the 7th and
// returns a tool call. It used to be captured, its effect reservation was refused, and the call stayed
// pending, a state abandon cannot settle. Now the call is never captured.
func TestHostCallCapToolCallOnTheLastHostCallLeavesNoPendingCall(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRead(1), capRead(2), capRead(3), capRead(4)}, 3)
	_, err := f.conversation.NextTurn(f.ctx)
	requireAbandonableCapEnd(t, f, err, 7)
}

// A retried exchange charges a host call without an effect, so the provider calls move to even host-call
// numbers. The third read is then the 6th host call: captured, its effect would take the 7th and the
// provider call that returns its result could not be reserved, which leaves nothing reserved for abandon
// to close. Now that read is refused like the unit-90 call.
func TestHostCallCapToolCallAfterARetryLeavesNoUnreservableResultTurn(t *testing.T) {
	f := hostCallCapWorkflow(t, []providerbridge.DevelopmentProviderTurn{capRetryable, capRead(1), capRead(2), capRead(3)}, 2)
	if checkpoint, _ := f.ledger.Snapshot(); checkpoint.Counters.HostCalls != 5 {
		t.Fatalf("a retry and two read turns charged %d host calls, want 5", checkpoint.Counters.HostCalls)
	}
	_, err := f.conversation.NextTurn(f.ctx)
	requireAbandonableCapEnd(t, f, err, 6)
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
