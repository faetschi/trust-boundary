//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
	sqlite "modernc.org/sqlite"
)

const (
	sqliteFsyncFaultHelperEnv    = "TBOUND_SQLITE_FSYNC_FAULT_HELPER"
	sqliteFsyncFaultCaseEnv      = "TBOUND_SQLITE_FSYNC_FAULT_CASE"
	sqliteFsyncFaultRootEnv      = "TBOUND_SQLITE_FSYNC_FAULT_ROOT"
	sqliteFsyncFaultStoreRootEnv = "TBOUND_SQLITE_FSYNC_FAULT_STORE_ROOT"
	sqliteFsyncFaultStateEnv     = "TBOUND_SQLITE_FSYNC_FAULT_STATE"
	sqliteFsyncFaultMarkerEnv    = "TBOUND_SQLITE_FSYNC_FAULT_MARKER"
	sqliteFsyncFaultWorkflow     = "sqlite-fsync-fault"
)

type sqliteFsyncFaultState struct {
	CaseName          string `json:"caseName"`
	ReservationDigest string `json:"reservationDigest"`
	ReservationParent string `json:"reservationParent"`
	Incarnation       string `json:"incarnation"`
	Epoch             uint64 `json:"epoch"`
	HostCalls         uint64 `json:"hostCalls"`
	Marker            string `json:"marker"`
	MarkerDigest      string `json:"markerDigest"`
}

func TestSQLiteOwnerKernelFsyncFailure(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("seccomp filter uses the x86_64 syscall ABI; current architecture is %s", runtime.GOARCH)
	}

	for _, testCase := range []struct{ name string }{
		{name: "owner-wal-fsync"},
		{name: "protected-root-wal-fsync"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
			statePath := filepath.Join(base, "fsync-fault-state.json")
			markerPath := filepath.Join(base, "actual-effect.marker")
			output := runSQLiteFsyncFaultChild(t, "fault", testCase.name, rootPath, ownerStoreRoot, statePath, markerPath)
			if bytes.Contains(output, []byte("SQLITE_FSYNC_FAULT_SKIPPED=")) {
				t.Skipf("isolated kernel fault injection unavailable: %s", output)
			}
			if !bytes.Contains(output, []byte("SQLITE_FSYNC_FAULT_OBSERVED=")) {
				t.Fatalf("fault phase did not observe SQLite IOERR_FSYNC:\n%s", output)
			}
			verify := runSQLiteFsyncFaultChild(t, "verify-fault", testCase.name, rootPath, ownerStoreRoot, statePath, markerPath)
			if !bytes.Contains(verify, []byte("SQLITE_FSYNC_FAULT_RECOVERED=")) {
				t.Fatalf("fresh-process verification did not report recovery:\n%s", verify)
			}
		})
	}

	t.Run("unfiltered-positive-control", func(t *testing.T) {
		rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
		markerPath := filepath.Join(base, "actual-effect.marker")
		output := runSQLiteFsyncFaultChild(t, "positive-control", "no-filter-positive-control", rootPath,
			ownerStoreRoot, filepath.Join(base, "unused-state.json"), markerPath)
		if bytes.Contains(output, []byte("SQLITE_FSYNC_FAULT_SKIPPED=")) {
			t.Skipf("unfiltered SQLite positive control unavailable: %s", output)
		}
		if !bytes.Contains(output, []byte("SQLITE_FSYNC_CONTROL_COMPLETED=")) {
			t.Fatalf("no-filter positive control did not complete normally:\n%s", output)
		}
		verify := runSQLiteFsyncFaultChild(t, "verify-control", "no-filter-positive-control", rootPath,
			ownerStoreRoot, filepath.Join(base, "unused-state.json"), markerPath)
		if !bytes.Contains(verify, []byte("SQLITE_FSYNC_CONTROL_VERIFIED=")) {
			t.Fatalf("fresh-process positive-control verification failed:\n%s", verify)
		}
	})
}

func runSQLiteFsyncFaultChild(t *testing.T, phase, caseName, rootPath, ownerStoreRoot, statePath, markerPath string) []byte {
	t.Helper()
	return runHelperChild(t, "TestSQLiteOwnerKernelFsyncFailureHelper", "SQLITE_FSYNC_", phase, caseName,
		sqliteFsyncFaultHelperEnv+"="+phase,
		sqliteFsyncFaultCaseEnv+"="+caseName,
		sqliteFsyncFaultRootEnv+"="+rootPath,
		sqliteFsyncFaultStoreRootEnv+"="+ownerStoreRoot,
		sqliteFsyncFaultStateEnv+"="+statePath,
		sqliteFsyncFaultMarkerEnv+"="+markerPath,
	)
}

func TestSQLiteOwnerKernelFsyncFailureHelper(t *testing.T) {
	phase := os.Getenv(sqliteFsyncFaultHelperEnv)
	if phase == "" {
		return
	}
	if runtime.GOARCH != "amd64" {
		t.Skipf("seccomp filter uses the x86_64 syscall ABI; current architecture is %s", runtime.GOARCH)
	}
	caseName := os.Getenv(sqliteFsyncFaultCaseEnv)
	rootPath := os.Getenv(sqliteFsyncFaultRootEnv)
	ownerStoreRoot := os.Getenv(sqliteFsyncFaultStoreRootEnv)
	statePath := os.Getenv(sqliteFsyncFaultStateEnv)
	markerPath := os.Getenv(sqliteFsyncFaultMarkerEnv)
	switch phase {
	case "fault":
		runSQLiteFsyncFaultPhase(t, caseName, rootPath, ownerStoreRoot, statePath, markerPath, true)
	case "positive-control":
		runSQLiteFsyncFaultPhase(t, caseName, rootPath, ownerStoreRoot, statePath, markerPath, false)
	case "verify-fault":
		verifySQLiteFsyncFaultRecovery(t, caseName, rootPath, ownerStoreRoot, statePath, markerPath)
	case "verify-control":
		verifySQLiteFsyncControl(t, rootPath, ownerStoreRoot, markerPath)
	default:
		t.Fatalf("unknown SQLite fsync fault helper phase %q", phase)
	}
}

func runSQLiteFsyncFaultPhase(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath, markerPath string, injectFault bool) {
	t.Helper()
	ctx := context.Background()
	workflow := sqliteFsyncFaultWorkflow
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
		workflow, "session-fsync-fault", 1, 1)
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
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-fsync-fault")); err != nil {
		t.Fatalf("register test provider transcript: %v", err)
	}
	reservation, err := ledger.ReserveEffect(ctx, testEffectBinding("terminal-effect-fsync-fault"), nil)
	if err != nil {
		t.Fatalf("durably reserve effect before actual effect: %v", err)
	}
	reserved, reservationDigest := ledger.Snapshot()
	if reservation.CheckpointDigest() != reservationDigest || reserved.Pins.EffectReservation == nil ||
		reserved.Pins.EffectReservation.Disposition != EffectReservationReserved || reserved.Counters.HostCalls != 1 {
		t.Fatalf("effect reservation receipt/checkpoint mismatch: receipt=%s checkpoint=%s pins=%+v counters=%+v",
			reservation.CheckpointDigest(), reservationDigest, reserved.Pins.EffectReservation, reserved.Counters)
	}
	anchored, anchoredDigest, anchoredExists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !anchoredExists || anchoredDigest != reservationDigest || anchored.Pins.EffectReservation == nil ||
		anchored.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("effect reservation not durable before actual effect: exists=%v digest=%s err=%v", anchoredExists, anchoredDigest, err)
	}
	if err := realRoot.VerifyCheckpoint(ctx, workflow, reserved.Pins.OwnerEpoch, reservationDigest,
		reserved.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("protected root did not anchor the reservation before actual effect: %v", err)
	}

	marker := []byte("effect executed once after durable reservation: " + reserved.Pins.EffectReservation.Binding.OperationID + "\n")
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
		t.Fatalf("write and sync actual effect marker after reservation: %v", err)
	}
	if effectCalls != 1 {
		t.Fatalf("actual effect callback ran %d times; want one", effectCalls)
	}

	terminalPins := clonePins(reserved.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest(string(marker))
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal Store journal: " + terminalPins.EffectReservation.Binding.OperationID)
	terminalPins.StoreTipGenerationID = "g1-terminal-fsync-fault"
	terminalPins.StoreTipTreeDigest = testDigest("terminal Store tree: " + terminalPins.EffectReservation.Binding.OperationID)
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("terminal admission journal: " + terminalPins.EffectReservation.Binding.OperationID)
	verifier := &sqliteTerminalMarkerVerifier{markerPath: markerPath, marker: marker}

	armedAt := false
	var setupErr error
	var targetFD int32
	filterCountBefore, filterCountAfter := -1, -1
	if !injectFault {
		filterCountBefore, _ = sqliteSeccompFilterCount()
	}
	attach := func(path string) {
		armedAt = true
		if !injectFault {
			return
		}
		fds, err := sqliteOpenFDsForPath(path)
		if err != nil {
			setupErr = err
			return
		}
		if len(fds) != 1 {
			setupErr = fmt.Errorf("expected exactly one open SQLite WAL fd for %s, found %v", path, fds)
			return
		}
		targetFD = fds[0]
		filterCountBefore, _ = sqliteSeccompFilterCount()
		setupErr = installSQLiteFsyncSeccomp(targetFD)
		if setupErr == nil {
			filterCountAfter, _ = sqliteSeccompFilterCount()
			if filterCountBefore >= 0 && filterCountAfter >= 0 && filterCountAfter != filterCountBefore+1 {
				setupErr = fmt.Errorf("TSYNC installed %d seccomp filters; expected exactly one (before=%d after=%d)",
					filterCountAfter-filterCountBefore, filterCountBefore, filterCountAfter)
			}
		}
	}
	if !injectFault {
		if caseName != "no-filter-positive-control" {
			t.Fatalf("unknown no-filter SQLite control case %q", caseName)
		}
		armed.afterOwnerCheck = func() { attach(ownerWAL) }
	} else {
		switch caseName {
		case "owner-wal-fsync":
			armed.afterOwnerCheck = func() { attach(ownerWAL) }
		case "protected-root-wal-fsync":
			armed.beforeTerminalCAS = func() { attach(rootWAL) }
		default:
			t.Fatalf("unknown SQLite fsync fault case %q", caseName)
		}
	}
	terminalReceipt, faultErr := ledger.RecordTerminalEffectOutcome(ctx, reservation, verifier, terminalPins)
	if setupErr != nil {
		if errors.Is(setupErr, syscall.EPERM) || errors.Is(setupErr, syscall.EACCES) ||
			errors.Is(setupErr, syscall.ENOSYS) || errors.Is(setupErr, syscall.EBUSY) {
			t.Logf("SQLITE_FSYNC_FAULT_SKIPPED=%v", setupErr)
			t.Skipf("kernel seccomp setup unavailable: %v", setupErr)
		}
		t.Fatalf("install exact-fd seccomp filter: %v", setupErr)
	}
	if !armedAt {
		t.Fatal("terminal persistence boundary was never reached")
	}
	if verifier.checks != 1 {
		t.Fatalf("terminal outcome verifier ran %d times; want once", verifier.checks)
	}
	if !injectFault {
		if faultErr != nil || terminalReceipt.WorkflowID() != workflow || terminalReceipt.CheckpointDigest() == "" {
			t.Fatalf("unfiltered positive control failed terminal persistence: receipt=%+v err=%v", terminalReceipt, faultErr)
		}
		if binding, ok := terminalReceipt.EffectBinding(); !ok || binding.OperationID != "terminal-effect-fsync-fault" {
			t.Fatalf("positive control returned unexpected terminal receipt: binding=%+v ok=%t", binding, ok)
		}
		filterCountAfter, _ = sqliteSeccompFilterCount()
		if filterCountBefore >= 0 && filterCountAfter != filterCountBefore {
			t.Fatalf("no-filter positive control changed the seccomp filter count: before=%d after=%d",
				filterCountBefore, filterCountAfter)
		}
		t.Logf("SQLITE_FSYNC_CONTROL_COMPLETED=filter=none filters_before=%d filters_after=%d terminal_digest=%s effect_calls=%d",
			filterCountBefore, filterCountAfter, terminalReceipt.CheckpointDigest(), effectCalls)
		return
	}
	if faultErr == nil {
		t.Fatalf("%s: terminal write succeeded despite injected EIO; receipt=%s", caseName, terminalReceipt.CheckpointDigest())
	}
	if terminalReceipt.WorkflowID() != "" || terminalReceipt.CheckpointDigest() != "" {
		t.Fatalf("failed terminal persistence returned a success receipt: workflow=%q digest=%q",
			terminalReceipt.WorkflowID(), terminalReceipt.CheckpointDigest())
	}
	if _, ok := terminalReceipt.EffectBinding(); ok {
		t.Fatalf("failed terminal persistence returned terminal outcome receipt: %+v", terminalReceipt)
	}
	if !strings.Contains(faultErr.Error(), "disk I/O error") {
		t.Fatalf("injected EIO was not surfaced as a SQLite I/O error: %v", faultErr)
	}
	if !errors.Is(faultErr, ErrCAS) {
		t.Fatalf("SQLite fsync fault lost ErrCAS classification: %v", faultErr)
	}
	if caseName == "owner-wal-fsync" {
		if !errors.Is(faultErr, ErrOwnerStore) {
			t.Fatalf("owner WAL fsync fault lost ErrOwnerStore classification: %v", faultErr)
		}
		requireSQLiteWALFaultCode(t, caseName, faultErr, 1034) // SQLITE_IOERR_FSYNC
	} else if !strings.Contains(faultErr.Error(), "disk I/O error (1034)") {
		// The trusted-root terminal-CAS wrapper intentionally reports ErrCAS
		// with formatted detail, so errors.As cannot reach the nested SQLite
		// error. Its exact extended code remains visible in the wrapped text.
		t.Fatalf("protected-root WAL fault did not surface SQLITE_IOERR_FSYNC(1034): %v", faultErr)
	}
	if caseName == "owner-wal-fsync" {
		if !owner.poisoned || realRoot.poisoned {
			t.Fatalf("owner WAL fsync fault poisoned owner=%t root=%t; want owner-only poison", owner.poisoned, realRoot.poisoned)
		}
	} else if !owner.poisoned || !realRoot.poisoned {
		t.Fatalf("protected-root WAL fsync fault poisoned owner=%t root=%t; want both poisoned after owner commit",
			owner.poisoned, realRoot.poisoned)
	}
	if after, afterDigest := ledger.Snapshot(); afterDigest != reservationDigest || !equalTerminalReservationCheckpoint(after, reserved) {
		t.Fatalf("failed terminal persistence changed in-memory reservation: before=%s after=%s", reservationDigest, afterDigest)
	}
	state := sqliteFsyncFaultState{CaseName: caseName, ReservationDigest: reservationDigest,
		ReservationParent: reserved.PreviousCheckpointDigest,
		Incarnation:       reserved.Pins.OwnerIncarnationDigest,
		Epoch:             reserved.Pins.OwnerEpoch,
		HostCalls:         reserved.Counters.HostCalls,
		Marker:            string(marker),
		MarkerDigest:      testDigest(string(marker)),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode reserved-effect state: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("write reserved-effect state: %v", err)
	}
	code := 1034 // SQLITE_IOERR_FSYNC, visible in the protected-root wrapper's formatted SQLite error.
	if caseName == "owner-wal-fsync" {
		var sqliteErr *sqlite.Error
		if !errors.As(faultErr, &sqliteErr) || sqliteErr.Code() != 1034 {
			t.Fatalf("SQLite VFS did not report SQLITE_IOERR_FSYNC(1034): %v", faultErr)
		}
		code = sqliteErr.Code()
	}
	primary := code & 0xff
	t.Logf("SQLITE_FSYNC_FAULT_OBSERVED=%s target_fd=%d code=%d primary=%d filters_before=%d filters_after=%d owner_poisoned=%t root_poisoned=%t effect_calls=%d error=%q",
		caseName, targetFD, code, primary, filterCountBefore, filterCountAfter, owner.poisoned, realRoot.poisoned, effectCalls, faultErr.Error())
}

func verifySQLiteFsyncControl(t *testing.T, rootPath, ownerStoreRoot, markerPath string) {
	t.Helper()
	ctx := context.Background()
	marker, err := os.ReadFile(markerPath)
	if err != nil || len(marker) == 0 {
		t.Fatalf("read control effect marker: %v", err)
	}
	workflow := sqliteFsyncFaultWorkflow
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("reopen protected root in fresh control verifier: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
	if err != nil {
		t.Fatalf("reopen SQLite owner in fresh control verifier: %v", err)
	}
	checkpoint, digest, exists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !exists || checkpoint.Pins.EffectReservation == nil ||
		checkpoint.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
		checkpoint.Pins.EffectReservation.OutcomeDigest != testDigest(string(marker)) {
		t.Fatalf("fresh control verifier did not recover terminal marker evidence: exists=%t digest=%s err=%v pins=%+v",
			exists, digest, err, checkpoint.Pins.EffectReservation)
	}
	if err := root.VerifyCheckpoint(ctx, workflow, checkpoint.Pins.OwnerEpoch, digest,
		checkpoint.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("protected root does not anchor control terminal checkpoint: %v", err)
	}
	t.Logf("SQLITE_FSYNC_CONTROL_VERIFIED=terminal-checkpoint-root-anchored digest=%s", digest)
}

func verifySQLiteFsyncFaultRecovery(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath, markerPath string) {
	t.Helper()
	ctx := context.Background()
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read exact reserved-effect state in fresh process: %v", err)
	}
	var state sqliteFsyncFaultState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode exact reserved-effect state in fresh process: %v", err)
	}
	if state.CaseName != caseName || state.ReservationDigest == "" || state.Marker == "" ||
		state.MarkerDigest != testDigest(state.Marker) || state.HostCalls != 1 {
		t.Fatalf("fresh verifier received malformed or mismatched reservation state: %+v", state)
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() {
		t.Fatalf("fresh process cannot find the single effect marker: info=%v err=%v", markerInfo, err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != state.Marker || testDigest(string(marker)) != state.MarkerDigest {
		t.Fatalf("fresh process marker differs from effect performed after reservation: err=%v", err)
	}
	workflow := sqliteFsyncFaultWorkflow
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("reopen protected root in fresh fsync-fault verifier: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
	if err != nil {
		t.Fatalf("reopen SQLite owner in fresh fsync-fault verifier: %v", err)
	}
	var quickCheck string
	if err := owner.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("fresh-process SQLite quick_check=%q err=%v", quickCheck, err)
	}

	if caseName != "owner-wal-fsync" && caseName != "protected-root-wal-fsync" {
		t.Fatalf("unknown SQLite fsync recovery case %q", caseName)
	}
	stored, digest, exists, loadErr := owner.LoadCheckpoint(ctx, workflow)
	if loadErr == nil && exists && digest == state.ReservationDigest {
		if stored.PreviousCheckpointDigest != state.ReservationParent ||
			stored.Pins.OwnerIncarnationDigest != state.Incarnation || stored.Pins.OwnerEpoch != state.Epoch ||
			stored.Counters.HostCalls != state.HostCalls || stored.Pins.EffectReservation == nil ||
			stored.Pins.EffectReservation.Disposition != EffectReservationReserved ||
			stored.Pins.EffectReservation.Binding.OperationID != "terminal-effect-fsync-fault" {
			t.Fatalf("fresh process recovered a different reservation: digest=%s pins=%+v", digest, stored.Pins.EffectReservation)
		}
		if err := root.VerifyCheckpoint(ctx, workflow, state.Epoch, state.ReservationDigest, state.Incarnation); err != nil {
			t.Fatalf("fresh protected root does not anchor the exact reservation: %v", err)
		}
		ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: stored.Pins.OwnerEpoch,
			RetainedPins: stored.Pins, Limits: stored.Limits})
		if err != nil {
			t.Fatalf("open exact unresolved reservation in fresh process: %v", err)
		}
		secondEffectCallbacks := 0
		invokeSecondEffect := func() error {
			secondEffectCallbacks++
			return os.WriteFile(markerPath, []byte("second effect must not run\n"), 0o600)
		}
		attemptSecondEffect := func() error {
			if _, err := ledger.ReserveEffect(ctx, testEffectBinding("must-not-run-second-effect"), nil); err != nil {
				return err
			}
			return invokeSecondEffect()
		}
		admissionErr := attemptSecondEffect()
		if !errors.Is(admissionErr, ErrPendingUnknown) || secondEffectCallbacks != 0 {
			t.Fatalf("next admission after unknown terminal persistence err=%v second_effect_callbacks=%d; want ErrPendingUnknown and zero callbacks",
				admissionErr, secondEffectCallbacks)
		}
		if after, err := os.ReadFile(markerPath); err != nil || string(after) != state.Marker {
			t.Fatalf("blocked retry changed the original effect marker: err=%v", err)
		}
		t.Logf("SQLITE_FSYNC_FAULT_RECOVERED=exact-reservation-pending-unknown second_effect_callbacks=%d digest=%s",
			secondEffectCallbacks, digest)
		return
	}
	if !errors.Is(loadErr, ErrRollback) || exists {
		t.Fatalf("fresh process accepted or misclassified an unanchored terminal owner head: exists=%t digest=%s err=%v",
			exists, digest, loadErr)
	}
	if err := root.VerifyCheckpoint(ctx, workflow, state.Epoch, state.ReservationDigest, state.Incarnation); err != nil {
		t.Fatalf("fresh protected root does not retain the exact reservation anchor: %v", err)
	}
	persisted, persistedDigest, persistedExists, _, rawErr := loadLedger(ctx, owner.db, workflow)
	if rawErr != nil || !persistedExists || persistedDigest == state.ReservationDigest ||
		persisted.PreviousCheckpointDigest != state.ReservationDigest ||
		persisted.ComputedCheckpointDigest != persistedDigest || persisted.Counters.HostCalls != state.HostCalls ||
		persisted.Pins.EffectReservation == nil || persisted.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
		persisted.Pins.EffectReservation.Binding.OperationID != "terminal-effect-fsync-fault" ||
		persisted.Pins.EffectReservation.OutcomeDigest != state.MarkerDigest {
		t.Fatalf("unanchored local owner head is not the exact durable terminal outcome: digest=%s exists=%t err=%v pins=%+v",
			persistedDigest, persistedExists, rawErr, persisted.Pins.EffectReservation)
	}
	secondEffectCallbacks := 0
	attemptRetryAfterRejectedHead := func() error {
		retryCheckpoint, _, retryExists, retryErr := owner.LoadCheckpoint(ctx, workflow)
		if retryErr != nil {
			return retryErr
		}
		if !retryExists {
			return ErrMissing
		}
		retryLedger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: retryCheckpoint.Pins.OwnerEpoch,
			RetainedPins: retryCheckpoint.Pins, Limits: retryCheckpoint.Limits})
		if err != nil {
			return err
		}
		if _, err := retryLedger.ReserveEffect(ctx, testEffectBinding("must-not-run-second-effect"), nil); err != nil {
			return err
		}
		secondEffectCallbacks++
		return os.WriteFile(markerPath, []byte("second effect must not run\n"), 0o600)
	}
	retryErr := attemptRetryAfterRejectedHead()
	if !errors.Is(retryErr, ErrRollback) || secondEffectCallbacks != 0 {
		t.Fatalf("retry after unanchored terminal rejection err=%v second_effect_callbacks=%d; want ErrRollback and zero callbacks",
			retryErr, secondEffectCallbacks)
	}
	t.Logf("SQLITE_FSYNC_FAULT_RECOVERED=unanchored-terminal-head-rejected second_effect_callbacks=%d reservation_digest=%s terminal_digest=%s",
		secondEffectCallbacks, state.ReservationDigest, persistedDigest)
}

func sqliteOpenFDsForPath(path string) ([]int32, error) {
	var expected unix.Stat_t
	if err := unix.Stat(path, &expected); err != nil {
		return nil, fmt.Errorf("stat target SQLite WAL %s: %w", path, err)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, fmt.Errorf("read /proc/self/fd: %w", err)
	}
	var matches []int32
	for _, entry := range entries {
		fd, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		fdPath := filepath.Join("/proc/self/fd", entry.Name())
		link, err := os.Readlink(fdPath)
		if err != nil || filepath.Clean(link) != filepath.Clean(path) {
			continue
		}
		var actual unix.Stat_t
		if err := unix.Fstat(int(fd), &actual); err != nil || actual.Dev != expected.Dev ||
			actual.Ino != expected.Ino || actual.Mode&unix.S_IFMT != expected.Mode&unix.S_IFMT {
			continue
		}
		matches = append(matches, int32(fd))
	}
	return matches, nil
}

func sqliteSeccompFilterCount() (int, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Seccomp_filters:") {
			count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Seccomp_filters:")))
			if err != nil {
				return -1, fmt.Errorf("parse Seccomp_filters status field: %w", err)
			}
			return count, nil
		}
	}
	return -1, fmt.Errorf("Seccomp_filters status field is unavailable")
}

func installSQLiteFsyncSeccomp(targetFD int32) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs in isolated helper: %w", err)
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: unix.AUDIT_ARCH_X86_64},
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x80000000}, // SECCOMP_RET_KILL_PROCESS on an unexpected syscall ABI.
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: uint32(unix.SYS_FSYNC)},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 5, K: uint32(unix.SYS_FDATASYNC)},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 3, K: uint32(targetFD)},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 20},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: 0},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(syscall.EIO)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	result, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	runtime.KeepAlive(filter)
	runtime.KeepAlive(&program)
	if errno != 0 {
		return fmt.Errorf("install TSYNC seccomp fd filter: %w", errno)
	}
	if result != 0 {
		return fmt.Errorf("TSYNC could not synchronize thread %d: %w", result, syscall.EBUSY)
	}
	return nil
}
