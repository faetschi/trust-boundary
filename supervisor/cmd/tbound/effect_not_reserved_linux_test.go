//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/workflowrecovery"
)

// effectRefusedAfterCapture runs F26 unit 90 with the host-call guard of #147 off, which stands for every
// ReserveEffect refusal after a capture (the workflow deadline, a fence or admission refusal): three reads
// charge 6 of 7 host calls, the fourth provider call takes the 7th, its read is captured and allowed, and
// ReserveEffect refuses the effect. The supervisor loop then fails with the executor error; Pi asks the
// bridge for the next turn, which the closed conversation refuses. It returns the fixture and the governed
// loops' joined error.
func effectRefusedAfterCapture(t *testing.T) (*workflowVerticalFixture, error) {
	t.Helper()
	turns := []providerbridge.DevelopmentProviderTurn{capRead(1), capRead(2), capRead(3), capRead(4)}
	f := prepareWorkflowVertical(t, workflowVerticalOptions{launchProof: true, maxHostCalls: 7, maxTokens: 7 * 1024})
	f.startConversation(t, func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		config.HostCallsLeft = nil
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, turns)
	})
	for index := 0; index < 3; index++ {
		executeOneRead(t, f)
	}
	turn, err := f.conversation.NextTurn(f.ctx)
	if err != nil || turn.ToolCall == nil || turn.ToolCall.ID != "cap-tool-call-4" {
		t.Fatalf("fourth provider turn: turn=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
		Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := f.conversation.Correlate(f.ctx, proposal)
	if err != nil || !matched.Accepted {
		t.Fatalf("correlate the fourth read: %+v %v", matched, err)
	}
	decision := gate.Evaluate(proposal, matched, f.policy)
	if err := f.conversation.RecordDecision(f.ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	output, execErr := executor.Execute(f.ctx, proposal, decision)
	if execErr == nil || len(output) != 0 || !errors.Is(execErr, workflowrecovery.ErrExhausted) {
		t.Fatalf("the refused effect released a result or was not refused by the cap: %s %v", output, execErr)
	}
	// As piruntime.Supervisor.Serve and providerbridge.Serve wrap their loop errors.
	serveErr := fmt.Errorf("executor failed; result withheld: %w", execErr)
	_, nextErr := f.conversation.NextTurn(f.ctx)
	bridgeErr := fmt.Errorf("provider turn failed without a journaled disposition; conversation closed: %w", nextErr)
	return f, governedChannelsError(serveErr, bridgeErr)
}

// The remaining case of #147 end to end: a captured call whose effect ReserveEffect refused leaves the
// request unknown with its call still held. The effect never ran. Abandon refuses while the owners are
// held; cleanup then drains the owner scope and releases the managed-root run, abandon settles the request
// on the typed record of the call, and a later request is admitted.
func TestEffectRefusedAfterCaptureFreesCleanupAndAbandonSettles(t *testing.T) {
	const reason = "effect refused after capture; operator recovery"
	f, channelsErr := effectRefusedAfterCapture(t)

	checkpoint, _ := f.ledger.Snapshot()
	call := checkpoint.Pins.HostCallReservation
	if checkpoint.Counters.HostCalls != 7 || call == nil || call.CapturedCall == nil ||
		call.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable || call.CapturedCall.ToolCallID != "cap-tool-call-4" ||
		checkpoint.Pins.EffectReservation == nil || checkpoint.Pins.EffectReservation.Binding.ToolCallID != "cap-tool-call-3" ||
		checkpoint.Pins.EffectReservation.Disposition != workflowrecovery.EffectReservationTerminal {
		t.Fatalf("checkpoint after the refused effect: pins=%+v counters=%+v", checkpoint.Pins, checkpoint.Counters)
	}
	storeTrace, err := f.storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	// The third read is the last authorized operation; after its effect the Store holds only the denial.
	authorized, last := 0, -1
	for index, record := range storeTrace.Records {
		if record.Event.Kind == governedOperationAuthorizationKind {
			authorized, last = authorized+1, index
		}
	}
	var after []string
	for _, record := range storeTrace.Records[last+1:] {
		after = append(after, record.Event.Kind)
	}
	if authorized != 3 || strings.Join(after, ",") != "effect_intent,effect_outcome,operation_denied" {
		t.Fatalf("the refused effect reached the Store: %d authorizations, records after the last: %v", authorized, after)
	}

	h := newUnknownRequestHarness(t, f)
	if err := h.abandon(reason); !errors.Is(err, ErrPiServiceUnreconciled) ||
		!strings.Contains(err.Error(), "managed-root run") || !strings.Contains(err.Error(), "owner scope") {
		t.Fatalf("abandon before cleanup was not refused for the unreleased run and the undrained scope: %v", err)
	}
	if !h.cleanup(t, settledWorkerLifecycle(g1SettledWorker, true, channelsErr), newProductionCommandRunner(nil)) {
		t.Fatalf("cleanup stayed quarantined after an effect that was never reserved: %v", channelsErr)
	}
	abandoned := h.abandoned(t, reason)
	if abandoned.Reconciliation.ProviderSettlement != providerSettlementTerminal ||
		abandoned.Reconciliation.WorkflowID != workflowVerticalID {
		t.Fatalf("the abandon record does not settle the workflow: %+v", abandoned.Reconciliation)
	}
	if err := h.requests.admit(t, 2, true); err != nil {
		t.Fatalf("a later request stayed blocked after the abandon: %v", err)
	}
}

const notReservedToolCall = "call-read-2"

func notReservedResult(mutate func(map[string]any)) audit.Event {
	return notStartedResult(func(fields map[string]any) {
		delete(fields, "command_not_started")
		fields["tool_call_id"] = notReservedToolCall
		fields["effect_not_reserved"] = true
		if mutate != nil {
			mutate(fields)
		}
	})
}

// On the abandon path a call the checkpoint still holds (captured, its gate decision awaited) counts as
// disposed when the authenticated checkpoint alone shows its effect was never reserved: the captured call is
// still unconsumed, and no effect reservation names it or is open. However the provider journal ends, that
// is accepted. Anything else stays refused: an effect reserved for the call, an open earlier effect, or pins
// that do not hold exactly that one call.
func TestReconcileOwnerAcceptsOnlyAnEffectThatWasNeverReserved(t *testing.T) {
	heldCall := func(f *ownerFixture) {
		pins := &f.view.Checkpoint.Pins
		pins.ProviderDisposition = workflowrecovery.DispositionAwaitingGateDecision
		pins.PendingCallDisposition = workflowrecovery.PendingCallCaptured
		pins.ProviderTranscripts = []workflowrecovery.TranscriptPin{{Ordinal: 1, ConversationID: "conversation-1",
			Disposition: workflowrecovery.DispositionAwaitingGateDecision}}
		pins.HostCallReservation = &workflowrecovery.HostCallReservationPin{
			Binding:     workflowrecovery.HostCallBinding{CallID: g1HostCallID, RequestDigest: g1RequestDigest, TokenUpperBound: g1TokenUpperBound},
			Disposition: workflowrecovery.HostCallTerminal, OutcomeDigest: strings.Repeat("7", 64),
			CapturedCall: &workflowrecovery.CapturedCallPin{WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1",
				HostCallID: g1HostCallID, ToolCallID: notReservedToolCall, OutcomeDigest: strings.Repeat("7", 64),
				RecoveryGeneration: 1, OwnerEpoch: 1, Disposition: workflowrecovery.CapturedCallAvailable}}
		// The previous call's effect, settled.
		pins.EffectReservation = &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationTerminal,
			OutcomeDigest: strings.Repeat("6", 64),
			Binding: workflowrecovery.EffectBinding{OperationID: "sessionrepo-effect-1", SpecDigest: strings.Repeat("8", 64),
				WorkflowID: reconcileTestWorkflow, ConversationID: "conversation-1", HostCallID: "host-provider-call-previous",
				ToolCallID: "call-read-1", HostCallOutcomeDigest: strings.Repeat("5", 64), RecoveryGeneration: 1, OwnerEpoch: 1}}
		f.view.Checkpoint.Counters.HostCalls, f.view.Checkpoint.Counters.Tokens = 3, 2*g1TokenUpperBound
	}
	journal := func(events ...audit.Event) func(*ownerFixture) {
		return func(f *ownerFixture) {
			trace := reconcileTestTrace(t, events...)
			f.providerTrace = func(conversationID string) (audit.Trace, error) {
				if conversationID != "conversation-1" {
					return audit.Trace{}, errors.New("no such conversation")
				}
				return trace, nil
			}
		}
	}
	decision := audit.Event{Kind: "provider_gate_decision", ID: "conversation-1:decision:1", Data: []byte(`{}`)}
	field := func(name string, value any) func(map[string]any) {
		return func(fields map[string]any) {
			if value == nil {
				delete(fields, name)
			} else {
				fields[name] = value
			}
		}
	}
	pinned := func(mutate func(*workflowrecovery.Pins)) func(*ownerFixture) {
		return func(f *ownerFixture) { mutate(&f.view.Checkpoint.Pins) }
	}
	ownEffect := func(disposition workflowrecovery.EffectReservationDisposition) func(*workflowrecovery.Pins) {
		return func(pins *workflowrecovery.Pins) {
			captured := pins.HostCallReservation.CapturedCall
			pins.EffectReservation.Disposition = disposition
			pins.EffectReservation.Binding.ToolCallID = captured.ToolCallID
			pins.EffectReservation.Binding.HostCallID = captured.HostCallID
		}
	}
	typed := journal(decision, notReservedResult(nil))
	for name, tc := range map[string]struct {
		mutate     []func(*ownerFixture)
		reconciled bool
	}{
		"effect never reserved": {[]func(*ownerFixture){heldCall, typed}, true},
		"first call, no effect before": {[]func(*ownerFixture){heldCall, typed,
			pinned(func(pins *workflowrecovery.Pins) { pins.EffectReservation = nil })}, true},
		// The journal does not decide: the checkpoint alone proves the effect never ran, so every way the
		// call's journal can end is accepted, including journals written before the record was typed.
		"journal before the record was typed": {[]func(*ownerFixture){heldCall,
			journal(decision, notReservedResult(field("effect_not_reserved", nil)))}, true},
		"journal ends at the decision": {[]func(*ownerFixture){heldCall, journal(decision)}, true},
		"journal ends at the capture": {[]func(*ownerFixture){heldCall,
			journal(audit.Event{Kind: "provider_exchange", ID: "conversation-1:exchange:1", Data: []byte(`{}`)})}, true},
		"no provider journal reader": {[]func(*ownerFixture){heldCall}, true},
		"effect reserved for the call": {[]func(*ownerFixture){heldCall, typed, pinned(ownEffect(workflowrecovery.EffectReservationReserved)),
			pinned(func(pins *workflowrecovery.Pins) {
				pins.HostCallReservation.CapturedCall.Disposition = workflowrecovery.CapturedCallConsumed
			})}, false},
		"effect reserved for the call, call unconsumed": {[]func(*ownerFixture){heldCall, typed,
			pinned(ownEffect(workflowrecovery.EffectReservationReserved))}, false},
		"terminal effect of the call": {[]func(*ownerFixture){heldCall, typed, pinned(ownEffect(workflowrecovery.EffectReservationTerminal))}, false},
		"retired effect of the call":  {[]func(*ownerFixture){heldCall, typed, pinned(ownEffect(workflowrecovery.EffectReservationRetired))}, false},
		"effect of the call's host call": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.EffectReservation.Binding.HostCallID = g1HostCallID
		})}, false},
		"previous effect still reserved": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.EffectReservation.Disposition = workflowrecovery.EffectReservationReserved
		})}, false},
		"previous effect unknown": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.EffectReservation.Disposition = workflowrecovery.EffectReservationUnknown
		})}, false},
		"captured call consumed": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.CapturedCall.Disposition = workflowrecovery.CapturedCallConsumed
		})}, false},
		"captured call of another conversation": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.CapturedCall.ConversationID = "conversation-2"
		})}, false},
		"captured call of another host call": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.CapturedCall.HostCallID = "host-provider-call-other"
		})}, false},
		"host call still reserved": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.HostCallReservation.Disposition = workflowrecovery.HostCallReserved
		})}, false},
		"pending call in flight": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.PendingCallDisposition = workflowrecovery.PendingCallInFlight
		})}, false},
		"provider exchange awaited": {[]func(*ownerFixture){heldCall, typed, pinned(func(pins *workflowrecovery.Pins) {
			pins.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
			pins.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionAwaitingProviderExchange
		})}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnerFixture(t, func(f *ownerFixture) {
				for _, mutate := range tc.mutate {
					mutate(f)
				}
			})
			_, err := f.reconcile(t)
			if tc.reconciled && err != nil {
				t.Fatalf("a call whose effect was never reserved was refused: %v", err)
			}
			if !tc.reconciled && !errors.Is(err, ErrPiServiceUnreconciled) {
				t.Fatalf("an open call was reconciled: %v", err)
			}
		})
	}
}
