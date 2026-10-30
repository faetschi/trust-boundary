//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

const (
	sqliteTerminalFaultHelperEnv    = "TBOUND_SQLITE_TERMINAL_FAULT_HELPER"
	sqliteTerminalFaultCaseEnv      = "TBOUND_SQLITE_TERMINAL_FAULT_CASE"
	sqliteTerminalFaultRootEnv      = "TBOUND_SQLITE_TERMINAL_FAULT_ROOT"
	sqliteTerminalFaultStoreRootEnv = "TBOUND_SQLITE_TERMINAL_FAULT_STORE_ROOT"
	sqliteTerminalFaultStateEnv     = "TBOUND_SQLITE_TERMINAL_FAULT_STATE"
	sqliteTerminalFaultMarkerEnv    = "TBOUND_SQLITE_TERMINAL_FAULT_MARKER"
	sqliteTerminalFaultWorkflow     = "sqlite-terminal-fault"
)

var sqliteTerminalFaultCases = []string{
	"owner-commit-ioerr",
	"owner-commit-full",
	"root-anchor-ioerr",
}

type sqliteTerminalFaultState struct {
	ReservationDigest string `json:"reservationDigest"`
	ReservationParent string `json:"reservationParent"`
	Incarnation       string `json:"incarnation"`
	Epoch             uint64 `json:"epoch"`
	HostCalls         uint64 `json:"hostCalls"`
	Marker            string `json:"marker"`
	MarkerDigest      string `json:"markerDigest"`
}

// The wrapper forwards both real protected-root checks. The first terminal
// owner verification is made by RecordTerminalEffectOutcome; the second is
// made by SQLiteOwner after it has opened the actual owner transaction and
// immediately before it writes that transaction. Arming at the second return
// therefore leaves the fault between real verification and the owner write.
type sqliteTerminalFaultRoot struct {
	*ProtectedTrustedRoot
	terminalOwnerChecks int
	afterOwnerCheck     func()
	beforeTerminalCAS   func()
}

func (r *sqliteTerminalFaultRoot) VerifyTerminalEffectOwner(ctx context.Context, workflow string,
	store OwnerStoreIdentity, incarnation OwnerIncarnation, ownerEpoch uint64,
	checkpointDigest string, deadline time.Time) error {
	if err := r.ProtectedTrustedRoot.VerifyTerminalEffectOwner(ctx, workflow, store,
		incarnation, ownerEpoch, checkpointDigest, deadline); err != nil {
		return err
	}
	r.terminalOwnerChecks++
	if r.terminalOwnerChecks == 2 && r.afterOwnerCheck != nil {
		r.afterOwnerCheck()
	}
	return nil
}

func (r *sqliteTerminalFaultRoot) CompareAndSwapTerminalCheckpoint(ctx context.Context, workflow string,
	ownerEpoch uint64, expectedDigest string, previous, next Checkpoint, proof TransitionProof,
	store OwnerStoreIdentity, incarnation OwnerIncarnation) error {
	if r.beforeTerminalCAS != nil {
		r.beforeTerminalCAS()
	}
	return r.ProtectedTrustedRoot.CompareAndSwapTerminalCheckpoint(ctx, workflow, ownerEpoch,
		expectedDigest, previous, next, proof, store, incarnation)
}

type sqliteTerminalMarkerVerifier struct {
	markerPath string
	marker     []byte
	checks     int
}

func (v *sqliteTerminalMarkerVerifier) VerifyTerminalEffect(_ context.Context, previous Checkpoint,
	binding EffectBinding, next Pins) error {
	v.checks++
	if previous.Pins.EffectReservation == nil ||
		previous.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		previous.Pins.EffectReservation.Binding != binding ||
		next.EffectReservation == nil || next.EffectReservation.Disposition != EffectReservationTerminal ||
		next.EffectReservation.Binding != binding ||
		next.EffectReservation.OutcomeDigest != testDigest(string(v.marker)) {
		return ErrObservation
	}
	info, err := os.Lstat(v.markerPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: actual effect marker is absent or not a regular file: %v", ErrObservation, err)
	}
	contents, err := os.ReadFile(v.markerPath)
	if err != nil || !bytes.Equal(contents, v.marker) {
		return fmt.Errorf("%w: actual effect marker differs from terminal evidence: %v", ErrObservation, err)
	}
	return nil
}

func TestSQLiteOwnerTerminalEffectRealPersistenceFault(t *testing.T) {
	for _, caseName := range sqliteTerminalFaultCases {
		t.Run(caseName, func(t *testing.T) {
			rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
			statePath := filepath.Join(base, "terminal-fault-state.json")
			markerPath := filepath.Join(base, "actual-effect.marker")
			fault := runSQLiteTerminalFaultChild(t, "fault", caseName, rootPath, ownerStoreRoot, statePath, markerPath)
			if bytes.Contains(fault, []byte("SQLITE_TERMINAL_FAULT_SKIPPED=")) {
				t.Skipf("helper reported unsupported fault setup: %s", fault)
			}
			if !bytes.Contains(fault, []byte("SQLITE_TERMINAL_FAULT_OBSERVED=")) {
				t.Fatalf("fault phase did not report an observed fault:\n%s", fault)
			}
			verify := runSQLiteTerminalFaultChild(t, "verify", caseName, rootPath, ownerStoreRoot, statePath, markerPath)
			if !bytes.Contains(verify, []byte("SQLITE_TERMINAL_FAULT_RECOVERED=")) {
				t.Fatalf("fresh-process verification did not report recovery:\n%s", verify)
			}
		})
	}
}

func runSQLiteTerminalFaultChild(t *testing.T, phase, caseName, rootPath, ownerStoreRoot, statePath, markerPath string) []byte {
	t.Helper()
	return runHelperChild(t, "TestSQLiteOwnerTerminalEffectRealPersistenceFaultHelper", "SQLITE_TERMINAL_FAULT_", phase, caseName,
		sqliteTerminalFaultHelperEnv+"="+phase,
		sqliteTerminalFaultCaseEnv+"="+caseName,
		sqliteTerminalFaultRootEnv+"="+rootPath,
		sqliteTerminalFaultStoreRootEnv+"="+ownerStoreRoot,
		sqliteTerminalFaultStateEnv+"="+statePath,
		sqliteTerminalFaultMarkerEnv+"="+markerPath,
	)
}

func TestSQLiteOwnerTerminalEffectRealPersistenceFaultHelper(t *testing.T) {
	phase := os.Getenv(sqliteTerminalFaultHelperEnv)
	if phase == "" {
		return
	}
	// Make an over-limit regular-file write return EFBIG to SQLite rather than
	// delivering SIGXFSZ to the isolated helper process.
	signal.Ignore(syscall.SIGXFSZ)
	caseName := os.Getenv(sqliteTerminalFaultCaseEnv)
	rootPath := os.Getenv(sqliteTerminalFaultRootEnv)
	ownerStoreRoot := os.Getenv(sqliteTerminalFaultStoreRootEnv)
	statePath := os.Getenv(sqliteTerminalFaultStateEnv)
	markerPath := os.Getenv(sqliteTerminalFaultMarkerEnv)
	switch phase {
	case "fault":
		runSQLiteTerminalFaultPhase(t, caseName, rootPath, ownerStoreRoot, statePath, markerPath)
	case "verify":
		verifySQLiteTerminalFaultRecovery(t, caseName, rootPath, ownerStoreRoot, statePath, markerPath)
	default:
		t.Fatalf("unknown SQLite terminal fault helper phase %q", phase)
	}
}

func runSQLiteTerminalFaultPhase(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath, markerPath string) {
	t.Helper()
	ctx := context.Background()
	workflow := sqliteTerminalFaultWorkflow
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	ownerWAL := ownerPath + "-wal"
	rootWAL := rootPath + "-wal"
	_, realRoot := createProtectedRoot(t, rootPath, ownerStoreRoot, workflow)
	armed := &sqliteTerminalFaultRoot{ProtectedTrustedRoot: realRoot}
	owner, err := CreateFreshSQLiteOwner(ownerPath, armed)
	if err != nil {
		t.Fatalf("create real SQLite owner: %v", err)
	}
	pins := testPins(realRoot.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(),
		workflow, "session-terminal-fault", 1, 1)
	if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
		t.Fatalf("initialize test workflow through protected root: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !exists {
		t.Fatalf("load fresh test checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open test ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-terminal-fault")); err != nil {
		t.Fatalf("register test provider transcript: %v", err)
	}
	settleInitialTranscript(t, ledger)
	binding := testEffectBinding("terminal-effect-fault")
	reservation, err := ledger.ReserveEffect(ctx, binding, nil)
	if err != nil {
		t.Fatalf("durably reserve effect before actual effect: %v", err)
	}
	reserved, reservationDigest := ledger.Snapshot()
	if reservation.CheckpointDigest() != reservationDigest || reserved.Pins.EffectReservation == nil ||
		reserved.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		reserved.Pins.EffectReservation.Binding != binding || reserved.Counters.HostCalls != 1 {
		t.Fatalf("effect reservation receipt/checkpoint mismatch: receipt=%s checkpoint=%s pins=%+v counters=%+v",
			reservation.CheckpointDigest(), reservationDigest, reserved.Pins.EffectReservation, reserved.Counters)
	}
	anchored, anchoredDigest, anchoredExists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !anchoredExists || anchoredDigest != reservationDigest ||
		anchored.Pins.EffectReservation == nil || anchored.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("effect reservation is not durable and owner-authenticated before the actual effect: exists=%v digest=%s err=%v",
			anchoredExists, anchoredDigest, err)
	}
	if err := realRoot.VerifyCheckpoint(ctx, workflow, reserved.Pins.OwnerEpoch, reservationDigest,
		reserved.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("protected root did not anchor the reservation before the actual effect: %v", err)
	}

	marker := []byte("effect executed once after durable reservation: " + binding.OperationID + "\n")
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("actual effect marker already exists before dispatch: %v", err)
	}
	effectCalls := 0
	writeActualEffect := func() error {
		effectCalls++
		file, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(marker)
		syncErr := file.Sync()
		closeErr := file.Close()
		return errors.Join(writeErr, syncErr, closeErr)
	}
	if err := writeActualEffect(); err != nil {
		t.Fatalf("write actual effect marker after durable reservation: %v", err)
	}
	if effectCalls != 1 {
		t.Fatalf("actual effect callback ran %d times, want once", effectCalls)
	}

	terminalPins := clonePins(reserved.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest(string(marker))
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal Store journal: " + binding.OperationID)
	terminalPins.StoreTipGenerationID = "g1-terminal-effect"
	terminalPins.StoreTipTreeDigest = testDigest("terminal Store tree: " + binding.OperationID)
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("terminal admission journal: " + binding.OperationID)
	verifier := &sqliteTerminalMarkerVerifier{markerPath: markerPath, marker: marker}

	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		skipSQLiteTerminalFault(t, fmt.Sprintf("cannot read RLIMIT_FSIZE: %v", err))
	}
	setLimit := func(limit uint64) {
		capped := syscall.Rlimit{Cur: limit, Max: original.Max}
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &capped); err != nil {
			if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) {
				skipSQLiteTerminalFault(t, fmt.Sprintf("environment cannot set RLIMIT_FSIZE in the isolated helper: %v", err))
			}
			t.Fatalf("set helper RLIMIT_FSIZE=%d: %v", limit, err)
		}
	}
	armedAt := false
	switch caseName {
	case "owner-commit-ioerr":
		armed.afterOwnerCheck = func() {
			armedAt = true
			setLimit(sqliteWALFaultFileSize(t, ownerWAL))
		}
	case "owner-commit-full":
		// max_page_count is a connection-local SQLite limit. Install it after
		// the effect and reservation, while no owner transaction is open; the
		// real terminal CAS still reaches its second verified owner check before
		// the bounded page budget rejects the write.
		limitOwnerPageCount(t, owner)
		armed.afterOwnerCheck = func() { armedAt = true }
	case "root-anchor-ioerr":
		armed.beforeTerminalCAS = func() {
			armedAt = true
			setLimit(sqliteWALFaultFileSize(t, rootWAL))
		}
	default:
		t.Fatalf("unknown SQLite terminal fault case %q", caseName)
	}
	terminalReceipt, faultErr := ledger.RecordTerminalEffectOutcome(ctx, reservation, verifier, terminalPins)
	// Restore the isolated helper's process limit before recovery bookkeeping.
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("restore helper RLIMIT_FSIZE: %v", err)
	}
	if !armedAt {
		t.Fatalf("%s: real terminal persistence boundary was never reached", caseName)
	}
	if verifier.checks != 1 {
		t.Fatalf("terminal outcome verifier ran %d times, want exactly once", verifier.checks)
	}
	if faultErr == nil {
		t.Fatalf("%s: terminal persistence succeeded despite the real write fault: receipt=%s",
			caseName, terminalReceipt.CheckpointDigest())
	}
	if terminalReceipt.WorkflowID() != "" || terminalReceipt.CheckpointDigest() != "" {
		t.Fatalf("failed terminal persistence returned a success receipt: workflow=%q digest=%q",
			terminalReceipt.WorkflowID(), terminalReceipt.CheckpointDigest())
	}
	if _, ok := terminalReceipt.EffectBinding(); ok {
		t.Fatalf("failed terminal persistence returned a terminal outcome receipt: %+v", terminalReceipt)
	}
	if caseName == "owner-commit-full" {
		if !strings.Contains(faultErr.Error(), "database or disk is full") {
			t.Fatalf("fault was not surfaced as SQLite FULL: %v", faultErr)
		}
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrCAS, ErrOwnerStore)
		requireSQLiteWALFaultCode(t, caseName, faultErr, 13)
		if owner.poisoned || realRoot.poisoned {
			t.Fatalf("rolled-back owner statement FULL poisoned owner=%t root=%t; want neither poisoned", owner.poisoned, realRoot.poisoned)
		}
	} else if caseName == "owner-commit-ioerr" {
		if !strings.Contains(faultErr.Error(), "disk I/O error") {
			t.Fatalf("fault was not surfaced as a real SQLite EFBIG I/O error: %v", faultErr)
		}
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrCAS, ErrOwnerStore)
		requireSQLiteWALFaultCode(t, caseName, faultErr, 778)
		if !owner.poisoned || realRoot.poisoned {
			t.Fatalf("owner commit IOERR poisoned owner=%t root=%t; want owner-only poison", owner.poisoned, realRoot.poisoned)
		}
	} else {
		if !strings.Contains(faultErr.Error(), "disk I/O error") {
			t.Fatalf("fault was not surfaced as a real SQLite EFBIG I/O error: %v", faultErr)
		}
		if !errors.Is(faultErr, ErrCAS) {
			t.Fatalf("root-anchor I/O fault lost ErrCAS: %v", faultErr)
		}
		if !owner.poisoned || !realRoot.poisoned {
			t.Fatalf("root-anchor fault poisoned owner=%t root=%t; want both poisoned after owner commit", owner.poisoned, realRoot.poisoned)
		}
	}
	if after, afterDigest := ledger.Snapshot(); afterDigest != reservationDigest ||
		!equalTerminalReservationCheckpoint(after, reserved) {
		t.Fatalf("failed terminal persistence changed the in-memory ledger: before=%s after=%s", reservationDigest, afterDigest)
	}

	state := sqliteTerminalFaultState{ReservationDigest: reservationDigest,
		ReservationParent: reserved.PreviousCheckpointDigest,
		Incarnation:       reserved.Pins.OwnerIncarnationDigest,
		Epoch:             reserved.Pins.OwnerEpoch,
		HostCalls:         reserved.Counters.HostCalls,
		Marker:            string(marker),
		MarkerDigest:      testDigest(string(marker)),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode reserved state: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("write reserved state: %v", err)
	}
	code := 0
	var sqliteErr *sqlite.Error
	if errors.As(faultErr, &sqliteErr) {
		code = sqliteErr.Code()
	}
	t.Logf("SQLITE_TERMINAL_FAULT_OBSERVED=%s code=%d owner_poisoned=%t root_poisoned=%t effect_calls=%d",
		caseName, code, owner.poisoned, realRoot.poisoned, effectCalls)
}

func equalTerminalReservationCheckpoint(left, right Checkpoint) bool {
	return reflect.DeepEqual(left, right) && left.Pins.EffectReservation != nil &&
		left.Pins.EffectReservation.Disposition == EffectReservationReserved
}

func verifySQLiteTerminalFaultRecovery(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath, markerPath string) {
	t.Helper()
	ctx := context.Background()
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read reserved state: %v", err)
	}
	var state sqliteTerminalFaultState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode reserved state: %v", err)
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() {
		t.Fatalf("fresh process cannot find the single actual effect marker: info=%v err=%v", markerInfo, err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != state.Marker || testDigest(string(marker)) != state.MarkerDigest {
		t.Fatalf("fresh process marker differs from the exact effect performed after reservation: err=%v", err)
	}
	workflow := sqliteTerminalFaultWorkflow
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("reopen protected root in fresh process: %v", err)
	}
	if err := root.VerifyCheckpoint(ctx, workflow, state.Epoch, state.ReservationDigest, state.Incarnation); err != nil {
		t.Fatalf("fresh protected root does not hold the exact reserved-effect head: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("reopen owner SQLite in fresh process: %v", err)
	}
	var quickCheck string
	if err := owner.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("fresh-process SQLite quick_check=%q err=%v", quickCheck, err)
	}
	stored, storedDigest, exists, loadErr := owner.LoadCheckpoint(ctx, workflow)
	if caseName == "root-anchor-ioerr" {
		if !errors.Is(loadErr, ErrRollback) {
			t.Fatalf("fresh process accepted or misclassified the unanchored terminal owner head: exists=%v digest=%s err=%v",
				exists, storedDigest, loadErr)
		}
		persisted, persistedDigest, persistedExists, _, rawErr := loadLedger(ctx, owner.db, workflow)
		if rawErr != nil || !persistedExists || persistedDigest == state.ReservationDigest ||
			persisted.ComputedCheckpointDigest != persistedDigest ||
			persisted.Counters.HostCalls != state.HostCalls || persisted.Pins.EffectReservation == nil ||
			persisted.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
			persisted.Pins.EffectReservation.OutcomeDigest != state.MarkerDigest {
			t.Fatalf("unanchored local head is not the durable terminal outcome: digest=%s exists=%v err=%v pins=%+v",
				persistedDigest, persistedExists, rawErr, persisted.Pins.EffectReservation)
		}
		if stored.Pins.EffectReservation != nil || exists {
			t.Fatalf("fresh load exposed an unanchored terminal outcome: exists=%v pins=%+v", exists, stored.Pins.EffectReservation)
		}
		t.Log("SQLITE_TERMINAL_FAULT_RECOVERED=unanchored-terminal-head-rejected")
		return
	}
	if caseName != "owner-commit-ioerr" && caseName != "owner-commit-full" {
		t.Fatalf("unknown SQLite terminal fault case %q", caseName)
	}
	if loadErr != nil || !exists || storedDigest != state.ReservationDigest ||
		stored.Counters.HostCalls != state.HostCalls || stored.Pins.EffectReservation == nil ||
		stored.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		stored.Pins.EffectReservation.Binding.OperationID != "terminal-effect-fault" {
		t.Fatalf("fresh process did not recover the exact unresolved reservation: exists=%v digest=%s err=%v pins=%+v",
			exists, storedDigest, loadErr, stored.Pins.EffectReservation)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: stored.Pins.OwnerEpoch,
		RetainedPins: stored.Pins, Limits: stored.Limits})
	if err != nil {
		t.Fatalf("open exact unresolved reservation for next admission attempt: %v", err)
	}
	secondEffectCalls := 0
	secondReceipt, admissionErr := ledger.ReserveEffect(ctx, testEffectBinding("must-not-run-second-effect"), nil)
	if admissionErr == nil {
		if secondReceipt.CheckpointDigest() == "" {
			t.Fatal("next effect admission returned a receipt without a checkpoint")
		}
		secondEffectCalls++
		if err := os.WriteFile(markerPath, []byte("second effect must not run\n"), 0o600); err != nil {
			t.Fatalf("simulate second actual effect callback: %v", err)
		}
	}
	if !errors.Is(admissionErr, ErrPendingUnknown) || secondEffectCalls != 0 {
		t.Fatalf("next admission after unknown terminal persistence err=%v second_effect_calls=%d; want ErrPendingUnknown and zero effects",
			admissionErr, secondEffectCalls)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("original actual effect marker disappeared: %v", err)
	}
	t.Log("SQLITE_TERMINAL_FAULT_RECOVERED=reserved-effect-pending-unknown-next-admission-denied")
}

func skipSQLiteTerminalFault(t *testing.T, reason string) {
	t.Helper()
	t.Logf("SQLITE_TERMINAL_FAULT_SKIPPED=%s", reason)
	t.Skip(reason)
}
