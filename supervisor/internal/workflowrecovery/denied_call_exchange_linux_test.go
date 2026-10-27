//go:build linux

package workflowrecovery

import (
	"context"
	"path/filepath"
	"testing"
)

// deniedCallAwaitingProvider is the checkpoint a released DENY leaves: the denied call is retired with
// its retirement digest and the same conversation awaits the model's answer to the DENY result.
func deniedCallAwaitingProvider(captured Checkpoint) Pins {
	pins := clonePins(captured.Pins)
	pins.ProviderTranscripts[0].JournalCount += 2
	pins.ProviderTranscripts[0].JournalHead = testDigest("deny decision and result journal head")
	pins.ProviderTranscripts[0].ProjectionCount += 2
	pins.ProviderTranscripts[0].ProjectionDigest = testDigest("deny decision and result projection")
	pins.ProviderTranscripts[0].ProjectionHead = pins.ProviderTranscripts[0].JournalHead
	pins.ProviderTranscripts[0].Disposition = DispositionAwaitingProviderExchange
	pins.ProviderProjectionCount += 2
	pins.ProviderProjectionDigest = TranscriptPinsDigest(pins.ProviderTranscripts)
	pins.ProviderProjectionHead = pins.ProviderTranscripts[0].ProjectionHead
	pins.ProviderDisposition = DispositionAwaitingProviderExchange
	pins.PendingCallDisposition = PendingCallRetired
	pins.HostCallReservation.CapturedCall.Disposition = CapturedCallRetired
	pins.HostCallReservation.CapturedCall.RetirementDigest = testDigest("verified DENY decision and result")
	return pins
}

func TestDeniedCallRetiresWhileItsConversationAwaitsTheProvider(t *testing.T) {
	ctx := context.Background()
	_, _, ledger, _, _ := newWorkflowHarness(t, "workflow-denied-exchange", testLimits())
	binding, captured := prepareCapturedCallForEffect(t, ledger, "operation-denied", "provider-tool-call-denied")
	denied := deniedCallAwaitingProvider(captured)

	for name, mutate := range map[string]func(*Pins){
		"no retirement digest":          func(p *Pins) { p.HostCallReservation.CapturedCall.RetirementDigest = "" },
		"pending call terminal":         func(p *Pins) { p.PendingCallDisposition = PendingCallTerminal },
		"call still available":          func(p *Pins) { p.HostCallReservation.CapturedCall.Disposition = CapturedCallAvailable },
		"other conversation":            func(p *Pins) { p.HostCallReservation.CapturedCall.ConversationID = "conversation-other" },
		"transcript awaits gate":        func(p *Pins) { p.ProviderTranscripts[0].Disposition = DispositionAwaitingGateDecision },
		"other owner epoch":             func(p *Pins) { p.HostCallReservation.CapturedCall.OwnerEpoch++ },
		"other recovery generation":     func(p *Pins) { p.HostCallReservation.CapturedCall.RecoveryGeneration++ },
		"provider awaits gate decision": func(p *Pins) { p.ProviderDisposition = DispositionAwaitingGateDecision },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := clonePins(denied)
			mutate(&wrong)
			wrong.ProviderProjectionDigest = TranscriptPinsDigest(wrong.ProviderTranscripts)
			if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, wrong); err == nil {
				t.Fatal("accepted a malformed denied-call retirement")
			}
		})
	}
	if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, denied); err != nil {
		t.Fatalf("retire the denied call while its conversation awaits the provider: %v", err)
	}
	retired, retiredDigest := ledger.Snapshot()
	if _, err := ledger.ReserveEffect(ctx, binding, exactCapturedOperationVerifier{expected: binding}); err == nil {
		t.Fatal("retired denied call was reserved for an effect")
	}
	if after, digest := ledger.Snapshot(); digest != retiredDigest || after.Counters != retired.Counters {
		t.Fatal("refused effect reservation changed the checkpoint")
	}
	if _, err := ledger.ReserveHostCall(ctx, testHostCallBinding("host-call-captured", 41)); err == nil {
		t.Fatal("the denied call's host-call identity was charged again")
	}

	next := testHostCallBinding("host-call-after-deny", 41)
	if _, err := ledger.ReserveHostCall(ctx, next); err != nil {
		t.Fatalf("charge the provider exchange that answers the DENY result: %v", err)
	}
	reserved, _ := ledger.Snapshot()
	if reserved.Counters.HostCalls != retired.Counters.HostCalls+1 || reserved.Counters.Tokens != retired.Counters.Tokens+41 ||
		reserved.Pins.HostCallReservation.CapturedCall != nil {
		t.Fatalf("next provider exchange was not charged exactly once: %+v", reserved.Counters)
	}
	second := clonePins(reserved.Pins)
	second.ProviderTranscripts[0].JournalCount++
	second.ProviderTranscripts[0].JournalHead = testDigest("second captured call journal head")
	second.ProviderTranscripts[0].ProjectionCount++
	second.ProviderTranscripts[0].ProjectionDigest = testDigest("second captured call projection")
	second.ProviderTranscripts[0].ProjectionHead = second.ProviderTranscripts[0].JournalHead
	second.ProviderTranscripts[0].Disposition = DispositionAwaitingGateDecision
	second.ProviderProjectionCount++
	second.ProviderProjectionDigest = TranscriptPinsDigest(second.ProviderTranscripts)
	second.ProviderProjectionHead = second.ProviderTranscripts[0].ProjectionHead
	second.ProviderDisposition = DispositionAwaitingGateDecision
	second.PendingCallDisposition = PendingCallCaptured
	second.HostCallReservation.Disposition = HostCallTerminal
	second.HostCallReservation.OutcomeDigest = testDigest("second provider response")
	second.HostCallReservation.CapturedCall = &CapturedCallPin{
		WorkflowID: second.WorkflowID, ConversationID: "conversation-captured",
		HostCallID: next.CallID, ToolCallID: "provider-tool-call-after-deny",
		OutcomeDigest: second.HostCallReservation.OutcomeDigest, RecoveryGeneration: second.RecoveryGeneration,
		OwnerEpoch: second.OwnerEpoch, Disposition: CapturedCallAvailable,
	}
	if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, second); err != nil {
		t.Fatalf("capture a different call after the DENY: %v", err)
	}
}

func TestAllowedOrExecutedCallCannotRetireAsDenied(t *testing.T) {
	for _, executed := range []bool{false, true} {
		name := "allowed"
		if executed {
			name = "executed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			workflow := "workflow-no-retire-" + name
			rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
			_, root := createProtectedRoot(t, rootPath, ownerStoreRoot, workflow)
			t.Cleanup(func() { _ = root.Close() })
			owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-no-retire", 1, 1)
			limits := testLimits()
			if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
				t.Fatal(err)
			}
			ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: pins, Limits: limits})
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := prepareCapturedCallForEffect(t, ledger, "operation-"+name, "tool-call-"+name)
			receipt, err := ledger.ReserveEffect(ctx, binding, exactCapturedOperationVerifier{expected: binding})
			if err != nil {
				t.Fatal(err)
			}
			if executed {
				terminal, _ := ledger.Snapshot()
				pins := clonePins(terminal.Pins)
				pins.StoreJournalCount++
				pins.StoreJournalHead = testDigest("executed Store outcome")
				pins.EffectReservation.Disposition = EffectReservationTerminal
				pins.EffectReservation.OutcomeDigest = testDigest("executed effect")
				if _, err := ledger.RecordTerminalEffectOutcome(ctx, receipt, acceptBoundTerminalEffect{}, pins); err != nil {
					t.Fatal(err)
				}
			}
			before, digest := ledger.Snapshot()
			if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, deniedCallAwaitingProvider(before)); err == nil {
				t.Fatal("consumed allowed/executed call retired as DENY")
			}
			after, afterDigest := ledger.Snapshot()
			if digest != afterDigest || before.Counters != after.Counters {
				t.Fatal("refused retirement changed checkpoint")
			}
		})
	}
}
