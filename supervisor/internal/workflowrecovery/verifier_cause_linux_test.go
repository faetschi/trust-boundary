//go:build linux

package workflowrecovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"tbound/supervisor/internal/runend"
)

type ceilingProgress struct{}

func (ceilingProgress) VerifyProgress(context.Context, Checkpoint, Pins) error {
	return fmt.Errorf("store evidence bundle: %w", runend.ErrCeiling)
}

// A verifier's failure keeps its cause in the ledger's error: the run-end registry must still see a ceiling marker
// that first surfaces inside a verifier (R2 of the #209 verification), beside the ledger's own ErrObservation.
func TestLedgerVerifierFailureKeepsItsCause(t *testing.T) {
	_, _, ledger, _, _ := newWorkflowHarnessWithPins(t, "workflow-verifier-cause", testLimits(), func(*Pins) {})
	pins := settledTranscriptPins(t, ledger)
	_, err := ledger.AdvanceCheckpoint(context.Background(), ceilingProgress{}, pins)
	if !errors.Is(err, ErrObservation) || !errors.Is(err, runend.ErrCeiling) {
		t.Fatalf("progress verifier failure = %v: observation %t ceiling %t, want both", err,
			errors.Is(err, ErrObservation), errors.Is(err, runend.ErrCeiling))
	}
}
