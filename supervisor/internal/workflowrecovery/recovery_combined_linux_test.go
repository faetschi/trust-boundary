//go:build linux

package workflowrecovery

import (
	"context"
	"errors"
	"testing"
)

// These ledger tests cover the owner-side rules for one atomic clean restart plus
// pre-effect retry. The production prepare/activate path is exercised across
// fresh processes by cmd/tbound's governed recovery tests.

type combinedRetirementFixture struct {
	path         string
	root         *testTrustedRoot
	owner        *SQLiteOwner
	ledger       *Ledger
	observation  VerifiedRecoveryObservation
	observed     Pins
	staged       Checkpoint
	stagedDigest string
}

// newCombinedRetirementFixture drives one provider call to its captured state
// under epoch 1, then reopens the workflow under a drained epoch 2 and verifies
// the externally retired observation that an unleased-call recovery produces.
func newCombinedRetirementFixture(t *testing.T, workflow string, limits Limits, captured bool) combinedRetirementFixture {
	t.Helper()
	ctx := context.Background()
	owner, root, first, _, _ := newWorkflowHarness(t, workflow, limits)
	if captured {
		prepareCapturedCallForEffect(t, first, "combined-operation", "combined-tool-call")
	} else if _, err := first.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-idle")); err != nil {
		t.Fatalf("register idle transcript: %v", err)
	}
	stale, staleDigest := first.Snapshot()
	root.setFence(workflow, 2, 2)
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 2,
		RetainedPins: stale.Pins, Limits: stale.Limits})
	if err != nil {
		t.Fatalf("open drained replacement epoch: %v", err)
	}
	observed := clonePins(stale.Pins)
	observed.ProviderTranscripts[0].Disposition = DispositionRetired
	observed.ProviderProjectionDigest = TranscriptPinsDigest(observed.ProviderTranscripts)
	observed.ProviderDisposition = DispositionRetired
	if captured {
		observed.PendingCallDisposition = PendingCallRetired
		observed.HostCallReservation.CapturedCall.Disposition = CapturedCallRetired
		observed.HostCallReservation.CapturedCall.RetirementDigest = testDigest("unleased retirement binding")
	}
	observation, err := ledger.ObserveRecovery(ctx, allowRecovery{}, observed)
	if err != nil {
		t.Fatalf("verify externally retired observation: %v", err)
	}
	return combinedRetirementFixture{path: owner.path, root: root, owner: owner, ledger: ledger,
		observation: observation, observed: observed, staged: stale, stagedDigest: staleDigest}
}

// combinedRestartPins is the next lineage the host binds to the replacement
// session. It keeps every retained predecessor commitment exactly as observed.
func combinedRestartPins(f combinedRetirementFixture) Pins {
	next := clonePins(f.observed)
	next.SessionID = "session-combined-restart"
	next.RecoveryGeneration = f.observed.RecoveryGeneration + 1
	next.OwnerEpoch = f.ledger.fencedEpoch
	next.PreviousRecoveryLineageDigest = f.observed.RecoveryLineageDigest
	restarted := emptyTranscript("conversation-combined-restart")
	restarted.Ordinal = uint64(len(next.ProviderTranscripts) + 1)
	next.ProviderTranscripts = append(next.ProviderTranscripts, restarted)
	next.ProviderProjectionDigest = TranscriptPinsDigest(next.ProviderTranscripts)
	next.ProviderProjectionCount = totalProjectionCount(next.ProviderTranscripts)
	next.ProviderProjectionHead = ""
	next.ProviderDisposition = DispositionAwaitingHostPrompt
	next.PendingCallDisposition = PendingCallNone
	next.RecoveryLineageDigest = ""
	next.RecoveryLineageDigest = ComputeRecoveryLineageDigest(f.stagedDigest, f.observed, next)
	return next
}

func TestCleanRestartWithPreEffectRetryChargesBothAllowancesInOneReceipt(t *testing.T) {
	limits := testLimits()
	f := newCombinedRetirementFixture(t, "workflow-combined-positive", limits, true)
	before, beforeDigest := f.ledger.Snapshot()
	if before.Counters.PreEffectRetries != 0 || before.Counters.CleanRestarts != 0 {
		t.Fatalf("fixture began with recovery charges: %+v", before.Counters)
	}
	next := combinedRestartPins(f)
	receipt, err := f.ledger.ReserveCleanRestartWithPreEffectRetry(context.Background(), f.observation, next)
	if err != nil {
		t.Fatalf("reserve combined clean restart and retry: %v", err)
	}
	after, afterDigest := f.ledger.Snapshot()
	if afterDigest != receipt.CheckpointDigest() || afterDigest == beforeDigest ||
		after.Counters.CleanRestarts != before.Counters.CleanRestarts+1 ||
		after.Counters.PreEffectRetries != before.Counters.PreEffectRetries+1 ||
		after.Counters.HostCalls != before.Counters.HostCalls || after.Counters.Tokens != before.Counters.Tokens {
		t.Fatalf("combined reservation did not charge exactly one restart and one retry: before=%+v after=%+v receipt=%+v",
			before.Counters, after.Counters, receipt.Counters())
	}
	if receipt.Counters() != after.Counters || receipt.Remaining().PreEffectRetries != limits.MaxPreEffectRetries-1 ||
		receipt.Remaining().CleanRestarts != limits.MaxCleanRestarts-1 {
		t.Fatalf("receipt is not bound to the final checkpoint allowances: counters=%+v remaining=%+v",
			receipt.Counters(), receipt.Remaining())
	}
	if err := f.ledger.VerifyCleanRestartReceipt(context.Background(), receipt, next); err != nil {
		t.Fatalf("final combined receipt did not verify against the head: %v", err)
	}
	// The combined charge consumed one of the two workflow-wide retries, so exactly
	// one standalone retry remains before exhaustion.
	if _, err := f.ledger.ReservePreEffectRetry(context.Background()); err != nil {
		t.Fatalf("second workflow-wide retry after the combined charge: %v", err)
	}
	if _, err := f.ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrExhausted) {
		t.Fatalf("third total pre-effect retry = %v, want ErrExhausted", err)
	}
	latest, latestDigest := f.ledger.Snapshot()

	// Replaying the durable journal must validate the combined transition again.
	if err := f.owner.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingSQLiteOwner(f.path, f.root)
	if err != nil {
		t.Fatalf("reopen SQLite owner and replay combined transition: %v", err)
	}
	defer reopened.Close()
	replayed, replayedDigest, exists, err := reopened.LoadCheckpoint(context.Background(), receipt.WorkflowID())
	if err != nil || !exists || replayedDigest != latestDigest || replayed.Counters != latest.Counters {
		t.Fatalf("replayed checkpoint differs from the latest durable head: digest=%s exists=%v err=%v counters=%+v",
			replayedDigest, exists, err, replayed.Counters)
	}
}

func TestCleanRestartWithPreEffectRetryRefusesIdleRetirementWithoutCharging(t *testing.T) {
	f := newCombinedRetirementFixture(t, "workflow-combined-idle", testLimits(), false)
	before, beforeDigest := f.ledger.Snapshot()
	_, err := f.ledger.ReserveCleanRestartWithPreEffectRetry(context.Background(), f.observation,
		combinedRestartPins(f))
	if !errors.Is(err, ErrObservation) {
		t.Fatalf("retry charged for an idle lineage retirement: err=%v", err)
	}
	after, afterDigest := f.ledger.Snapshot()
	if afterDigest != beforeDigest || after.Counters != before.Counters {
		t.Fatalf("refused idle retry changed the checkpoint: before=%+v after=%+v", before.Counters, after.Counters)
	}
}

func TestCleanRestartWithPreEffectRetryExhaustionChargesNothing(t *testing.T) {
	limits := testLimits()
	limits.MaxPreEffectRetries = 0
	f := newCombinedRetirementFixture(t, "workflow-combined-exhausted", limits, true)
	before, beforeDigest := f.ledger.Snapshot()
	_, err := f.ledger.ReserveCleanRestartWithPreEffectRetry(context.Background(), f.observation,
		combinedRestartPins(f))
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("combined reservation with no retry allowance = %v, want ErrExhausted", err)
	}
	after, afterDigest := f.ledger.Snapshot()
	if afterDigest != beforeDigest || after.Counters != before.Counters {
		t.Fatalf("refused combined reservation consumed a restart: before=%+v after=%+v", before.Counters, after.Counters)
	}
}
