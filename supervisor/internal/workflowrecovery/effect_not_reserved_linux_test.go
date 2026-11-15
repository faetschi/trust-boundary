//go:build linux

package workflowrecovery

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ReserveEffect refusal that wrote no checkpoint is typed ErrEffectNotReserved: the workflow deadline
// passed between the capture and the effect, and the durable checkpoint keeps no reservation. While
// another effect is still open the same refusal stays untyped.
func TestReserveEffectDeadlineRefusalIsTypedNotReserved(t *testing.T) {
	ctx := context.Background()
	workflow := "effect-not-reserved-deadline"
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	_, root := createProtectedRoot(t, rootPath, ownerStoreRoot, workflow)
	t.Cleanup(func() { _ = root.Close() })
	wall := time.Now().Round(0).UTC()
	bootTimeNS := int64(60_000_000_000)
	root.clockNow = func() (time.Time, string, int64, error) {
		return wall, "00000000-0000-4000-8000-000000000051", bootTimeNS, nil
	}
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	limits := testLimits()
	limits.Deadline = wall.Add(time.Hour)
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-not-reserved", 1, 1)
	if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	checkpoint, _, _, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-not-reserved")); err != nil {
		t.Fatal(err)
	}
	settleInitialTranscript(t, ledger)
	open, err := ledger.ReserveEffect(ctx, testEffectBinding("operation-open"), nil)
	if err != nil {
		t.Fatalf("reserve the first effect before the deadline: %v", err)
	}

	bootTimeNS += int64(time.Hour + time.Second)
	if _, err := ledger.ReserveEffect(ctx, testEffectBinding("operation-beside-open"), nil); !errors.Is(err, ErrDeadline) ||
		errors.Is(err, ErrEffectNotReserved) {
		t.Fatalf("ReserveEffect after the deadline with an effect open = %v, want ErrDeadline untyped", err)
	}
	reserved, _ := ledger.Snapshot()
	terminal := clonePins(reserved.Pins)
	terminal.EffectReservation.Disposition = EffectReservationTerminal
	terminal.EffectReservation.OutcomeDigest = testDigest("not-reserved Store outcome")
	terminal.StoreJournalCount++
	terminal.StoreJournalHead = testDigest("not-reserved Store journal head")
	terminal.StoreTipGenerationID = "g1-not-reserved"
	terminal.StoreTipTreeDigest = testDigest("not-reserved Store tip tree")
	terminal.AdmissionJournalCount++
	terminal.AdmissionJournalHead = testDigest("not-reserved admission head")
	if _, err := ledger.RecordTerminalEffectOutcome(ctx, open, acceptBoundTerminalEffect{}, terminal); err != nil {
		t.Fatalf("settle the first effect after the deadline: %v", err)
	}
	_, beforeDigest := ledger.Snapshot()

	_, err = ledger.ReserveEffect(ctx, testEffectBinding("operation-after-deadline"), nil)
	if !errors.Is(err, ErrDeadline) || !errors.Is(err, ErrEffectNotReserved) {
		t.Fatalf("ReserveEffect after the deadline = %v, want ErrDeadline typed ErrEffectNotReserved", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("the typed refusal is not one log line: %q", err)
	}
	// After the deadline the owner refuses to load; the protected root holds the durable head.
	after, afterDigest := ledger.Snapshot()
	state, exists, loadErr := loadProtectedRootState(ctx, root.db, workflow)
	if loadErr != nil || !exists || afterDigest != beforeDigest || state.CheckpointDigest != beforeDigest ||
		after.Pins.EffectReservation.Binding.OperationID != "operation-open" {
		t.Fatalf("the refused reservation changed the checkpoint: digest %s -> %s (durable %s) err=%v",
			beforeDigest, afterDigest, state.CheckpointDigest, loadErr)
	}
}

// F26 unit 90 at the ledger: three tool turns charge 6 of 7 host calls, the fourth provider turn takes the
// 7th and its tool call is captured, and ReserveEffect refuses the effect. The refusal is typed.
func TestReserveEffectCapRefusalAfterCaptureIsTypedNotReserved(t *testing.T) {
	spent, err := runG1HostCallSequence(t, 7)
	if !errors.Is(err, ErrExhausted) || !errors.Is(err, ErrEffectNotReserved) || spent != 7 ||
		!strings.Contains(err.Error(), "effect of turn 4") {
		t.Fatalf("limit 7: spent %d host calls, err %v; want the effect of turn 4 refused as not reserved", spent, err)
	}
}
