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

// protectedClockFixture is an in-process root and owner with one reserved
// effect ready for its terminal outcome. The clock is pinned so verification
// neither expires nor pauses the workflow, which keeps every root clock write
// deterministic.
type protectedClockFixture struct {
	workflow     string
	root         *ProtectedTrustedRoot
	owner        *SQLiteOwner
	ledger       *Ledger
	reservation  Receipt
	beforeDigest string
	terminalPins Pins
}

func newProtectedClockFixture(t *testing.T, workflow string) *protectedClockFixture {
	t.Helper()
	ctx := context.Background()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	baseWall := time.Now().Round(0).UTC()
	deadline := baseWall.Add(time.Hour).UTC()
	bootID := "00000000-0000-4000-8000-000000000031"
	bootTimeNS := int64(40_000_000_000)
	root.clockNow = func() (time.Time, string, int64, error) { return baseWall, bootID, bootTimeNS, nil }
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-clock-precommit", 1, 1)
	limits := testLimits()
	limits.Deadline = deadline
	if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize workflow through protected root: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !exists {
		t.Fatalf("load fresh checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-clock-precommit")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	settleInitialTranscript(t, ledger)
	reservation, err := ledger.ReserveEffect(ctx, testEffectBinding("operation-clock-precommit"), nil)
	if err != nil {
		t.Fatalf("reserve effect: %v", err)
	}
	reserved, reservedDigest := ledger.Snapshot()
	if reservedDigest != reservation.CheckpointDigest() || reserved.Pins.EffectReservation == nil ||
		reserved.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("reservation is not the exact reserved head: digest=%s pins=%+v", reservedDigest, reserved.Pins.EffectReservation)
	}
	terminalPins := clonePins(reserved.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("clock pre-commit Store outcome")
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("clock pre-commit Store journal head")
	terminalPins.StoreTipGenerationID = "g1-clock-precommit"
	terminalPins.StoreTipTreeDigest = testDigest("clock pre-commit Store tip tree")
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("clock pre-commit admission head")
	return &protectedClockFixture{workflow: workflow, root: root, owner: owner, ledger: ledger,
		reservation: reservation, beforeDigest: reservedDigest, terminalPins: terminalPins}
}

// A pre-commit failure in the root clock high-water write is rolled back and
// leaves the durable head unchanged. It must refuse only the attempted terminal
// commit: the root stays usable, and the same terminal commit succeeds once the
// fault clears.
func TestProtectedClockPreCommitFaultRefusesOnlyTheAttemptedTerminal(t *testing.T) {
	for _, stage := range []string{"begin", "load", "update"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			fx := newProtectedClockFixture(t, "protected-clock-precommit-"+stage)
			armed := true
			fx.root.clockPersistFault = func(got string) error {
				if armed && got == stage {
					armed = false
					return errors.New("injected " + stage + " fault")
				}
				return nil
			}
			receipt, err := fx.ledger.RecordTerminalEffectOutcome(ctx, fx.reservation, acceptBoundTerminalEffect{}, fx.terminalPins)
			if armed {
				t.Fatalf("%s: terminal commit never reached the pre-commit fault", stage)
			}
			if err == nil || !strings.Contains(err.Error(), "injected "+stage+" fault") {
				t.Fatalf("%s: terminal commit err=%v; want the injected pre-commit failure", stage, err)
			}
			if receipt.WorkflowID() != "" || receipt.CheckpointDigest() != "" {
				t.Fatalf("%s: failed terminal commit returned a success receipt: workflow=%q digest=%q",
					stage, receipt.WorkflowID(), receipt.CheckpointDigest())
			}
			if fx.root.poisoned || fx.owner.poisoned {
				t.Fatalf("%s: rolled-back pre-commit fault poisoned owner=%t root=%t; want neither poisoned",
					stage, fx.owner.poisoned, fx.root.poisoned)
			}
			if after, afterDigest := fx.ledger.Snapshot(); afterDigest != fx.beforeDigest ||
				after.Pins.EffectReservation == nil || after.Pins.EffectReservation.Disposition != EffectReservationReserved {
				t.Fatalf("%s: failed terminal commit changed the in-memory ledger: before=%s after=%s pins=%+v",
					stage, fx.beforeDigest, afterDigest, after.Pins.EffectReservation)
			}
			stored, storedDigest, exists, err := fx.owner.LoadCheckpoint(ctx, fx.workflow)
			if err != nil || !exists || storedDigest != fx.beforeDigest ||
				stored.Pins.EffectReservation == nil || stored.Pins.EffectReservation.Disposition != EffectReservationReserved {
				t.Fatalf("%s: durable head is not the unchanged reservation: exists=%v digest=%s err=%v pins=%+v",
					stage, exists, storedDigest, err, stored.Pins.EffectReservation)
			}
			if err := fx.root.VerifyFence(ctx, fx.workflow, 1); err != nil {
				t.Fatalf("%s: root refused an ordinary fence check after a rolled-back pre-commit fault: %v", stage, err)
			}
			receipt, err = fx.ledger.RecordTerminalEffectOutcome(ctx, fx.reservation, acceptBoundTerminalEffect{}, fx.terminalPins)
			if err != nil {
				t.Fatalf("%s: same terminal commit failed after the fault cleared: %v", stage, err)
			}
			if receipt.WorkflowID() != fx.workflow || receipt.CheckpointDigest() == "" {
				t.Fatalf("%s: terminal commit after the fault cleared returned no success receipt: workflow=%q digest=%q",
					stage, receipt.WorkflowID(), receipt.CheckpointDigest())
			}
			if after, _ := fx.ledger.Snapshot(); after.Pins.EffectReservation == nil ||
				after.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
				after.Pins.EffectReservation.OutcomeDigest != fx.terminalPins.EffectReservation.OutcomeDigest {
				t.Fatalf("%s: terminal commit did not settle the reservation: pins=%+v", stage, after.Pins.EffectReservation)
			}
			if fx.root.poisoned || fx.owner.poisoned {
				t.Fatalf("%s: successful retry left owner=%t root=%t poisoned", stage, fx.owner.poisoned, fx.root.poisoned)
			}
		})
	}
}

// A row that another writer changes between the caller's read and the clock
// write is a concurrent writer. Those cases must keep poisoning the root.
func TestProtectedClockConcurrentWriterStillPoisonsRoot(t *testing.T) {
	mutations := map[string]string{
		"revision-change":        `UPDATE workflow_root_state SET revision=revision+1 WHERE workflow_id=?`,
		"invalid-row-after-read": `UPDATE workflow_root_state SET checkpoint_epoch=fence_epoch+1 WHERE workflow_id=?`,
	}
	for name, statement := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			fx := newProtectedClockFixture(t, "protected-clock-concurrent-"+name)
			injected := false
			fx.root.clockPersistFault = func(stage string) error {
				if stage == "begin" && !injected {
					injected = true
					if _, err := fx.root.db.ExecContext(ctx, statement, fx.workflow); err != nil {
						t.Errorf("simulate concurrent writer: %v", err)
					}
				}
				return nil
			}
			if _, err := fx.ledger.RecordTerminalEffectOutcome(ctx, fx.reservation, acceptBoundTerminalEffect{}, fx.terminalPins); err == nil {
				t.Fatal("terminal commit succeeded across a concurrent writer")
			}
			if !injected {
				t.Fatal("concurrent writer was never injected")
			}
			if !fx.root.poisoned || fx.owner.poisoned {
				t.Fatalf("concurrent writer poisoned owner=%t root=%t; want root-only poisoning", fx.owner.poisoned, fx.root.poisoned)
			}
			if err := fx.root.VerifyFence(ctx, fx.workflow, 1); !errors.Is(err, ErrCAS) {
				t.Fatalf("poisoned root accepted an ordinary fence check: %v", err)
			}
		})
	}
}
