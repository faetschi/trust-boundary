//go:build linux

package workflowrecovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// The G1 case is read, edit, Bash, read and a final text turn: five provider turns and four effects. Both
// ReserveHostCall and ReserveEffect charge Counters.HostCalls, and every tool goes through ReserveEffect, so
// the case spends g1ProviderTurns + g1ToolEffects host calls. A MaxHostCalls below that refuses the final
// provider turn after the last effect has already run.
const (
	g1ProviderTurns = 5
	g1ToolEffects   = 4
	g1HostCalls     = g1ProviderTurns + g1ToolEffects
)

// runG1HostCallSequence drives the ledger through the G1 turn pattern against maxHostCalls: each tool turn is a
// provider reservation, a captured call, an effect reservation, its terminal outcome and the durable result,
// and the last turn is a provider reservation alone. It returns the first error and the host calls spent.
func runG1HostCallSequence(t *testing.T, maxHostCalls uint64) (uint64, error) {
	t.Helper()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := fmt.Sprintf("g1-host-call-budget-%d", maxHostCalls)
	_, root := createProtectedRoot(t, rootPath, ownerStoreRoot, workflow)
	defer root.Close()
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	defer owner.Close()
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-g1-budget", 1, 1)
	limits := testLimits()
	limits.MaxHostCalls = maxHostCalls
	if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		t.Fatalf("load initial checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	ctx := context.Background()
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-g1")); err != nil {
		t.Fatalf("register transcript: %v", err)
	}
	spent := func() uint64 { c, _ := ledger.Snapshot(); return c.Counters.HostCalls }
	for turn := 1; turn <= g1ProviderTurns; turn++ {
		call := testHostCallBinding(fmt.Sprintf("g1-host-call-%d", turn), 41)
		if _, err := ledger.ReserveHostCall(ctx, call); err != nil {
			return spent(), fmt.Errorf("provider turn %d: %w", turn, err)
		}
		if turn == g1ProviderTurns {
			break
		}
		reserved, _ := ledger.Snapshot()
		capturePins := clonePins(reserved.Pins)
		hostCall := capturePins.HostCallReservation
		hostCall.Disposition = HostCallTerminal
		hostCall.OutcomeDigest = testDigest(fmt.Sprintf("g1 provider outcome %d", turn))
		hostCall.CapturedCall = &CapturedCallPin{
			WorkflowID: workflow, ConversationID: "conversation-g1", HostCallID: call.CallID,
			ToolCallID: fmt.Sprintf("g1-tool-call-%d", turn), OutcomeDigest: hostCall.OutcomeDigest,
			RecoveryGeneration: capturePins.RecoveryGeneration, OwnerEpoch: capturePins.OwnerEpoch,
			Disposition: CapturedCallAvailable,
		}
		g1AdvanceTranscript(&capturePins, fmt.Sprintf("g1 response %d", turn), DispositionAwaitingGateDecision)
		capturePins.PendingCallDisposition = PendingCallCaptured
		capturePins.ProviderDisposition = DispositionAwaitingGateDecision
		if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, capturePins); err != nil {
			t.Fatalf("turn %d capture: %v", turn, err)
		}
		captured, _ := ledger.Snapshot()
		binding := testEffectBinding(fmt.Sprintf("g1-operation-%d", turn))
		cc := captured.Pins.HostCallReservation.CapturedCall
		binding.WorkflowID, binding.ConversationID, binding.HostCallID, binding.ToolCallID = workflow, cc.ConversationID, cc.HostCallID, cc.ToolCallID
		binding.HostCallOutcomeDigest, binding.RecoveryGeneration, binding.OwnerEpoch = cc.OutcomeDigest, cc.RecoveryGeneration, cc.OwnerEpoch
		effect, err := ledger.ReserveEffect(ctx, binding, exactCapturedOperationVerifier{expected: binding})
		if err != nil {
			return spent(), fmt.Errorf("effect of turn %d: %w", turn, err)
		}
		effected, _ := ledger.Snapshot()
		terminalPins := clonePins(effected.Pins)
		terminalPins.StoreJournalCount++
		terminalPins.StoreJournalHead = testDigest(fmt.Sprintf("g1 store head %d", turn))
		terminalPins.EffectReservation.Disposition = EffectReservationTerminal
		terminalPins.EffectReservation.OutcomeDigest = testDigest(fmt.Sprintf("g1 store outcome %d", turn))
		if _, err := ledger.RecordTerminalEffectOutcome(ctx, effect, acceptBoundTerminalEffect{}, terminalPins); err != nil {
			t.Fatalf("turn %d terminal effect: %v", turn, err)
		}
		settled, _ := ledger.Snapshot()
		resultPins := clonePins(settled.Pins)
		resultPins.HostCallReservation.CapturedCall = nil
		resultPins.PendingCallDisposition = PendingCallTerminal
		resultPins.ProviderDisposition = DispositionAwaitingProviderExchange
		g1AdvanceTranscript(&resultPins, fmt.Sprintf("g1 result %d", turn), DispositionAwaitingProviderExchange)
		if _, err := ledger.AdvanceCheckpoint(ctx, allowProgress{}, resultPins); err != nil {
			t.Fatalf("turn %d result: %v", turn, err)
		}
	}
	return spent(), nil
}

func g1AdvanceTranscript(pins *Pins, label string, disposition string) {
	active := &pins.ProviderTranscripts[len(pins.ProviderTranscripts)-1]
	active.JournalCount++
	active.JournalHead = testDigest(label + " journal head")
	active.ProjectionCount++
	active.ProjectionDigest = testDigest(label + " projection")
	active.ProjectionHead = active.JournalHead
	active.Disposition = disposition
	pins.ProviderProjectionCount = totalProjectionCount(pins.ProviderTranscripts)
	pins.ProviderProjectionDigest = TranscriptPinsDigest(pins.ProviderTranscripts)
	pins.ProviderProjectionHead = active.ProjectionHead
}

// TestG1TurnPatternNeedsNineHostCalls pins the arithmetic behind workflow_max_host_calls of the G1 service
// config and corpus case: eight host calls refuse the fifth provider turn after four effects, nine complete it.
func TestG1TurnPatternNeedsNineHostCalls(t *testing.T) {
	spent, err := runG1HostCallSequence(t, g1HostCalls-1)
	if !errors.Is(err, ErrExhausted) || spent != g1HostCalls-1 {
		t.Fatalf("limit %d: spent %d host calls, err %v; want ErrExhausted after %d", g1HostCalls-1, spent, err, g1HostCalls-1)
	}
	spent, err = runG1HostCallSequence(t, g1HostCalls)
	if err != nil || spent != g1HostCalls {
		t.Fatalf("limit %d: spent %d host calls, err %v; want the full pattern", g1HostCalls, spent, err)
	}
}
