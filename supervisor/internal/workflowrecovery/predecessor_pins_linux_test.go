//go:build linux

package workflowrecovery

import (
	"context"
	"errors"
	"testing"
)

func withPredecessorPins(p *Pins) {
	p.ImportedPrefixDigest = testDigest("imported prefix")
	p.PredecessorTurnReceiptDigest = testDigest("predecessor receipt")
}

var predecessorPinChanges = map[string]struct {
	start  func(*Pins)
	change func(*Pins)
}{
	"imported prefix changed": {withPredecessorPins, func(p *Pins) { p.ImportedPrefixDigest = testDigest("other prefix") }},
	"imported prefix cleared": {withPredecessorPins, func(p *Pins) { p.ImportedPrefixDigest = "" }},
	"imported prefix added":   {func(*Pins) {}, func(p *Pins) { p.ImportedPrefixDigest = testDigest("imported prefix") }},
	"receipt changed":         {withPredecessorPins, func(p *Pins) { p.PredecessorTurnReceiptDigest = testDigest("other receipt") }},
	"receipt cleared":         {withPredecessorPins, func(p *Pins) { p.PredecessorTurnReceiptDigest = "" }},
	"receipt added":           {func(*Pins) {}, func(p *Pins) { p.PredecessorTurnReceiptDigest = testDigest("predecessor receipt") }},
}

func settledTranscriptPins(t *testing.T, ledger *Ledger) Pins {
	t.Helper()
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	checkpoint, _ := ledger.Snapshot()
	pins := clonePins(checkpoint.Pins)
	pins.ProviderTranscripts[0].JournalCount = 1
	pins.ProviderTranscripts[0].JournalHead = testDigest("settled provider transcript")
	pins.ProviderTranscripts[0].Disposition = DispositionTerminal
	pins.ProviderDisposition = DispositionTerminal
	pins.ProviderProjectionDigest = digestTranscriptPins(pins.ProviderTranscripts)
	return pins
}

func TestPredecessorPinsAreImmutableAcrossCheckpoints(t *testing.T) {
	for name, tc := range predecessorPinChanges {
		t.Run(name, func(t *testing.T) {
			_, _, ledger, _, _ := newWorkflowHarnessWithPins(t, "workflow-predecessor", testLimits(), tc.start)
			pins := settledTranscriptPins(t, ledger)
			changed := clonePins(pins)
			tc.change(&changed)
			if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, changed); !errors.Is(err, ErrRollback) {
				t.Fatalf("checkpoint changed a predecessor pin: err=%v, want ErrRollback", err)
			}
			if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pins); err != nil {
				t.Fatalf("unchanged pins refused: %v", err)
			}
		})
	}
}

// reserveRestart runs a clean restart from a settled first transcript to a new recovery generation
// with change applied to the next pins.
func reserveRestart(t *testing.T, start, change func(*Pins)) (Checkpoint, error) {
	t.Helper()
	const workflow = "workflow-predecessor-restart"
	owner, root, ledger, _, _ := newWorkflowHarnessWithPins(t, workflow, testLimits(), start)
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-1")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	stale, staleDigest := ledger.Snapshot()
	root.setFence(workflow, 2, 2)
	replacement, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 2, RetainedPins: stale.Pins, Limits: stale.Limits,
	})
	if err != nil {
		t.Fatalf("open replacement owner: %v", err)
	}
	observed := clonePins(stale.Pins)
	observed.ProviderTranscripts[0].JournalCount = 2
	observed.ProviderTranscripts[0].JournalHead = testDigest("provider-journal-tail")
	observed.ProviderTranscripts[0].Disposition = DispositionTerminal
	observed.ProviderDisposition = DispositionTerminal
	observed.ProviderProjectionDigest = digestTranscriptPins(observed.ProviderTranscripts)
	observation, err := replacement.ObserveRecovery(context.Background(), allowRecovery{}, observed)
	if err != nil {
		t.Fatalf("observe recovery: %v", err)
	}
	next := clonePins(observed)
	next.SessionID = "session-recovered"
	next.RecoveryGeneration = 2
	next.OwnerEpoch = 2
	next.PreviousRecoveryLineageDigest = observed.RecoveryLineageDigest
	transcript := emptyTranscript("conversation-2")
	transcript.Ordinal = 2
	next.ProviderTranscripts = append(next.ProviderTranscripts, transcript)
	next.ProviderProjectionDigest = digestTranscriptPins(next.ProviderTranscripts)
	next.ProviderDisposition = DispositionAwaitingHostPrompt
	change(&next)
	next.RecoveryLineageDigest = ""
	next.RecoveryLineageDigest = ComputeRecoveryLineageDigest(staleDigest, observed, next)
	if _, err := replacement.ReserveCleanRestart(context.Background(), observation, next); err != nil {
		return Checkpoint{}, err
	}
	checkpoint, _ := replacement.Snapshot()
	return checkpoint, nil
}

func TestPredecessorPinsAreImmutableAcrossRecoveryGenerations(t *testing.T) {
	checkpoint, err := reserveRestart(t, withPredecessorPins, func(*Pins) {})
	if err != nil {
		t.Fatalf("restart keeping the predecessor pins: %v", err)
	}
	if checkpoint.Pins.RecoveryGeneration != 2 || checkpoint.Pins.ImportedPrefixDigest != testDigest("imported prefix") ||
		checkpoint.Pins.PredecessorTurnReceiptDigest != testDigest("predecessor receipt") {
		t.Fatalf("restart did not carry the predecessor pins: %+v", checkpoint.Pins)
	}
	for name, tc := range predecessorPinChanges {
		t.Run(name, func(t *testing.T) {
			if _, err := reserveRestart(t, tc.start, tc.change); !errors.Is(err, ErrRollback) {
				t.Fatalf("restart changed a predecessor pin: err=%v, want ErrRollback", err)
			}
		})
	}
}

func TestPredecessorPinsMustBeBareDigests(t *testing.T) {
	valid := testPins(testDigest("root"), testDigest("incarnation"), "workflow", "session", 1, 1)
	withPredecessorPins(&valid)
	if err := validatePins(valid); err != nil {
		t.Fatalf("valid predecessor pins refused: %v", err)
	}
	for name, change := range map[string]func(*Pins){
		"prefixed imported digest": func(p *Pins) { p.ImportedPrefixDigest = "sha256:" + p.ImportedPrefixDigest },
		"short receipt digest":     func(p *Pins) { p.PredecessorTurnReceiptDigest = p.PredecessorTurnReceiptDigest[:63] },
	} {
		pins := valid
		change(&pins)
		if err := validatePins(pins); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err=%v, want ErrInvalid", name, err)
		}
	}
}
