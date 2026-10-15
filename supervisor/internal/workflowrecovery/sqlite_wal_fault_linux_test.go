//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

// Each case runs one durable transition in a fault helper process, then checks
// the surviving files from a second, fresh helper process. Nothing about the
// recovered state is inferred from the faulting process's memory.
//
// Fault sources are kept distinct:
//   - kernel: RLIMIT_FSIZE makes a real write(2) fail with EFBIG, which SQLite
//     reports as SQLITE_IOERR_WRITE (778). The limit is per file, so each case
//     arms it at the call boundary where only the intended file must grow.
//   - engine: PRAGMA max_page_count is the owner's page ceiling. Setting it to the
//     current size leaves no headroom, so the reservation's headroom gate refuses
//     before any write. This is not kernel ENOSPC; that needs a full filesystem,
//     which needs root.
//   - hooks: sqliteWALFaultRoot forwards every call to the real protected root
//     and only changes the RLIMIT between two real calls. It injects no errors.
const (
	sqliteWALFaultHelperEnv    = "TBOUND_SQLITE_WAL_FAULT_HELPER"
	sqliteWALFaultCaseEnv      = "TBOUND_SQLITE_WAL_FAULT_CASE"
	sqliteWALFaultRootEnv      = "TBOUND_SQLITE_WAL_FAULT_ROOT"
	sqliteWALFaultStoreRootEnv = "TBOUND_SQLITE_WAL_FAULT_STORE_ROOT"
	sqliteWALFaultStateEnv     = "TBOUND_SQLITE_WAL_FAULT_STATE"
	sqliteWALFaultWorkflow     = "sqlite-wal-fault"
)

var sqliteWALFaultCases = []string{
	"owner-commit-ioerr",
	"root-first-ioerr",
	"root-boundary-ioerr",
	"root-anchor-ioerr",
	"owner-headroom-refused",
}

type sqliteWALFaultState struct {
	Digest      string `json:"digest"`
	Incarnation string `json:"incarnation"`
	Epoch       uint64 `json:"epoch"`
	HostCalls   uint64 `json:"hostCalls"`
	Tokens      uint64 `json:"tokens"`
}

type sqliteWALFaultRoot struct {
	*ProtectedTrustedRoot
	afterDeadline func()
	afterFence    func()
	beforeAnchor  func()
}

func (r *sqliteWALFaultRoot) VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error {
	err := r.ProtectedTrustedRoot.VerifyWorkflowDeadline(ctx, workflow, deadline)
	if err == nil && r.afterDeadline != nil {
		r.afterDeadline()
	}
	return err
}

func (r *sqliteWALFaultRoot) VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error {
	err := r.ProtectedTrustedRoot.VerifyFence(ctx, workflow, ownerEpoch)
	if err == nil && r.afterFence != nil {
		r.afterFence()
	}
	return err
}

func (r *sqliteWALFaultRoot) CompareAndSwapCheckpoint(ctx context.Context, workflow string, expectedOwnerEpoch uint64,
	expectedDigest string, nextOwnerEpoch uint64, nextDigest string, incarnation OwnerIncarnation) error {
	if r.beforeAnchor != nil {
		r.beforeAnchor()
	}
	return r.ProtectedTrustedRoot.CompareAndSwapCheckpoint(ctx, workflow, expectedOwnerEpoch, expectedDigest,
		nextOwnerEpoch, nextDigest, incarnation)
}

func TestSQLiteOwnerRealWALWriteFault(t *testing.T) {
	for _, caseName := range sqliteWALFaultCases {
		t.Run(caseName, func(t *testing.T) {
			rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
			statePath := filepath.Join(base, "fault-state.json")
			fault := runSQLiteWALFaultChild(t, "fault", caseName, rootPath, ownerStoreRoot, statePath)
			if bytes.Contains(fault, []byte("SQLITE_WAL_FAULT_SKIPPED=")) {
				t.Skipf("helper reported unsupported fault setup: %s", fault)
			}
			if !bytes.Contains(fault, []byte("SQLITE_WAL_FAULT_OBSERVED=")) {
				t.Fatalf("fault phase did not report an observed fault:\n%s", fault)
			}
			verify := runSQLiteWALFaultChild(t, "verify", caseName, rootPath, ownerStoreRoot, statePath)
			if !bytes.Contains(verify, []byte("SQLITE_WAL_FAULT_RECOVERED=")) {
				t.Fatalf("fresh-process verification did not report recovery:\n%s", verify)
			}
		})
	}
}

func runSQLiteWALFaultChild(t *testing.T, phase, caseName, rootPath, ownerStoreRoot, statePath string) []byte {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestSQLiteOwnerRealWALWriteFaultHelper$")
	command.Env = append(os.Environ(),
		sqliteWALFaultHelperEnv+"="+phase,
		sqliteWALFaultCaseEnv+"="+caseName,
		sqliteWALFaultRootEnv+"="+rootPath,
		sqliteWALFaultStoreRootEnv+"="+ownerStoreRoot,
		sqliteWALFaultStateEnv+"="+statePath,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s helper for %s failed: %v\n%s", phase, caseName, err, output)
	}
	for _, line := range bytes.Split(output, []byte("\n")) {
		if bytes.Contains(line, []byte("SQLITE_WAL_FAULT_")) {
			t.Logf("%s: %s", phase, bytes.TrimSpace(line))
		}
	}
	return output
}

func TestSQLiteOwnerRealWALWriteFaultHelper(t *testing.T) {
	phase := os.Getenv(sqliteWALFaultHelperEnv)
	if phase == "" {
		return
	}
	caseName := os.Getenv(sqliteWALFaultCaseEnv)
	rootPath := os.Getenv(sqliteWALFaultRootEnv)
	ownerStoreRoot := os.Getenv(sqliteWALFaultStoreRootEnv)
	statePath := os.Getenv(sqliteWALFaultStateEnv)
	// Ignoring SIGXFSZ turns the RLIMIT_FSIZE overrun into an ordinary EFBIG
	// write error for SQLite instead of terminating the helper process.
	signal.Ignore(syscall.SIGXFSZ)
	switch phase {
	case "fault":
		runSQLiteWALFaultPhase(t, caseName, rootPath, ownerStoreRoot, statePath)
	case "verify":
		verifySQLiteWALFaultRecovery(t, caseName, rootPath, ownerStoreRoot, statePath)
	default:
		t.Fatalf("unknown SQLite WAL fault helper phase %q", phase)
	}
}

func runSQLiteWALFaultPhase(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath string) {
	t.Helper()
	ctx := context.Background()
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	ownerWAL := ownerPath + "-wal"
	rootWAL := rootPath + "-wal"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, sqliteWALFaultWorkflow, &processExitDrainVerifier{})
	realRoot, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create real protected root: %v", err)
	}
	armed := &sqliteWALFaultRoot{ProtectedTrustedRoot: realRoot}
	owner, err := CreateFreshSQLiteOwner(ownerPath, armed)
	if err != nil {
		t.Fatalf("create real SQLite owner: %v", err)
	}
	pins := testPins(realRoot.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(),
		sqliteWALFaultWorkflow, "session-wal-fault", 1, 1)
	// The exact-workflow fresh authorizer in this package is a test-only host
	// fixture. This test covers durable SQLite/root behavior, not signed
	// production catalog or profile authorization.
	if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
		t.Fatalf("initialize test workflow through protected root: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, sqliteWALFaultWorkflow)
	if err != nil || !exists {
		t.Fatalf("load fresh test checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{
		WorkflowID: sqliteWALFaultWorkflow, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open test ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-wal-fault")); err != nil {
		t.Fatalf("register test provider transcript: %v", err)
	}
	prior, priorDigest := ledger.Snapshot()
	if prior.Pins.EffectReservation != nil || prior.Counters.HostCalls != 0 {
		t.Fatalf("unexpected pre-fault effect authority state: counters=%+v effect=%+v", prior.Counters, prior.Pins.EffectReservation)
	}
	if err := realRoot.VerifyCheckpoint(ctx, sqliteWALFaultWorkflow, prior.Pins.OwnerEpoch, priorDigest,
		prior.Pins.OwnerIncarnationDigest); err != nil {
		t.Fatalf("verify protected-root predecessor before fault: %v", err)
	}

	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		skipSQLiteWALFault(t, fmt.Sprintf("cannot read RLIMIT_FSIZE: %v", err))
	}
	setLimit := func(limit uint64) {
		capped := syscall.Rlimit{Cur: limit, Max: original.Max}
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &capped); err != nil {
			if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) {
				skipSQLiteWALFault(t, fmt.Sprintf("environment cannot set RLIMIT_FSIZE in the isolated helper: %v", err))
			}
			t.Fatalf("set helper RLIMIT_FSIZE=%d: %v", limit, err)
		}
	}

	armedAt := false
	switch caseName {
	case "owner-commit-ioerr":
		// All root verification writes for this transition have finished here.
		// The owner WAL is capped at its current size, so its first new frame fails.
		armed.afterFence = func() {
			armedAt = true
			setLimit(sqliteWALFaultFileSize(t, ownerWAL))
		}
	case "root-first-ioerr":
		// The root clock high-water commit is the first file growth in the transition.
		armedAt = true
		setLimit(0)
	case "root-boundary-ioerr":
		// The first root clock write has committed; the second one must fail.
		armed.afterDeadline = func() {
			armedAt = true
			setLimit(sqliteWALFaultFileSize(t, rootWAL))
		}
	case "root-anchor-ioerr":
		// The owner head is durably committed before the root anchor is attempted.
		armed.beforeAnchor = func() {
			armedAt = true
			setLimit(sqliteWALFaultFileSize(t, rootWAL))
		}
	case "owner-headroom-refused":
		// The ceiling leaves no headroom for the reservation's terminal accounting,
		// so the gate refuses before any owner statement runs.
		armedAt = true
		limitOwnerPageCount(t, owner)
	default:
		t.Fatalf("unknown SQLite WAL fault case %q", caseName)
	}
	receipt, faultErr := ledger.ReserveEffect(ctx, testEffectBinding("effect-wal-fault"), nil)
	// Restore before any further verification so later reads are not capped.
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("restore helper RLIMIT_FSIZE: %v", err)
	}
	if !armedAt {
		t.Fatalf("%s: fault point was never reached; the transition did not perform the intended write", caseName)
	}
	if faultErr == nil {
		t.Fatalf("%s: reservation succeeded; the real fault did not occur: receipt=%q", caseName, receipt.CheckpointDigest())
	}
	if receipt.CheckpointDigest() != "" || receipt.WorkflowID() != "" {
		t.Fatalf("%s: failed reservation returned a receipt: workflow=%q digest=%q", caseName, receipt.WorkflowID(), receipt.CheckpointDigest())
	}
	if _, ok := receipt.EffectBinding(); ok {
		t.Fatalf("%s: failed reservation returned an effect binding: %+v", caseName, receipt)
	}
	if caseName == "owner-headroom-refused" {
		if !errors.Is(faultErr, ErrExhausted) || errors.Is(faultErr, ErrCAS) {
			t.Fatalf("%s: refusal = %v, want typed ErrExhausted without ErrCAS", caseName, faultErr)
		}
	} else if !strings.Contains(faultErr.Error(), "disk I/O error") && !strings.Contains(faultErr.Error(), "database or disk is full") {
		t.Fatalf("%s: failure is not a SQLite I/O or full condition: %v", caseName, faultErr)
	}
	if after, afterDigest := ledger.Snapshot(); afterDigest != priorDigest || !reflect.DeepEqual(after, prior) {
		t.Fatalf("%s: failed reservation changed the in-memory ledger: before=%+v after=%+v", caseName, prior, after)
	}

	switch caseName {
	case "owner-commit-ioerr":
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrCAS, ErrOwnerStore)
		requireSQLiteWALFaultCode(t, caseName, faultErr, 778) // SQLITE_IOERR_WRITE from EFBIG.
		if !owner.poisoned || realRoot.poisoned {
			t.Fatalf("%s: owner poisoned=%t root poisoned=%t; want owner-only poisoning after an ambiguous commit",
				caseName, owner.poisoned, realRoot.poisoned)
		}
	case "owner-headroom-refused":
		var refusedBySQLite *sqlite.Error
		if errors.As(faultErr, &refusedBySQLite) {
			t.Fatalf("%s: headroom refusal reached SQLite before any write: %v", caseName, faultErr)
		}
		if owner.poisoned || realRoot.poisoned {
			t.Fatalf("%s: owner poisoned=%t root poisoned=%t; want neither poisoned by a refusal before any write",
				caseName, owner.poisoned, realRoot.poisoned)
		}
	case "root-first-ioerr":
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrOwnerStore)
		if !realRoot.poisoned || owner.poisoned {
			t.Fatalf("%s: root poisoned=%t owner poisoned=%t; want root-only poisoning before any owner write",
				caseName, realRoot.poisoned, owner.poisoned)
		}
	case "root-boundary-ioerr":
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrCAS, ErrOwnerFence)
		if !realRoot.poisoned || owner.poisoned {
			t.Fatalf("%s: root poisoned=%t owner poisoned=%t; want root-only poisoning before the owner CAS",
				caseName, realRoot.poisoned, owner.poisoned)
		}
	case "root-anchor-ioerr":
		requireSQLiteWALFaultChain(t, caseName, faultErr, ErrCAS)
		if !realRoot.poisoned || !owner.poisoned {
			t.Fatalf("%s: root poisoned=%t owner poisoned=%t; want both poisoned after an unanchored commit",
				caseName, realRoot.poisoned, owner.poisoned)
		}
	}

	// Fail closed in-process: no successful load may return a head other than
	// the predecessor, and a poisoned or root-poisoned owner must not load at all.
	stored, storedDigest, storedExists, loadErr := owner.LoadCheckpoint(ctx, sqliteWALFaultWorkflow)
	if loadErr == nil && (!storedExists || storedDigest != priorDigest) {
		t.Fatalf("%s: faulted process returned a non-predecessor head: exists=%v digest=%s", caseName, storedExists, storedDigest)
	}
	if (owner.poisoned || realRoot.poisoned) && loadErr == nil {
		t.Fatalf("%s: poisoned store still loaded a head: %+v", caseName, stored.Counters)
	}

	state := sqliteWALFaultState{Digest: priorDigest, Incarnation: prior.Pins.OwnerIncarnationDigest,
		Epoch: prior.Pins.OwnerEpoch, HostCalls: prior.Counters.HostCalls, Tokens: prior.Counters.Tokens}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode predecessor state: %v", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatalf("write predecessor state: %v", err)
	}
	var sqliteErr *sqlite.Error
	code := 0
	if errors.As(faultErr, &sqliteErr) {
		code = sqliteErr.Code()
	}
	t.Logf("SQLITE_WAL_FAULT_ERROR=%q", faultErr.Error())
	t.Logf("SQLITE_WAL_FAULT_OBSERVED=%s code=%d primary=%d owner_poisoned=%t root_poisoned=%t",
		caseName, code, code&0xff, owner.poisoned, realRoot.poisoned)
}

func verifySQLiteWALFaultRecovery(t *testing.T, caseName, rootPath, ownerStoreRoot, statePath string) {
	t.Helper()
	ctx := context.Background()
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read predecessor state: %v", err)
	}
	var state sqliteWALFaultState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatalf("decode predecessor state: %v", err)
	}
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, sqliteWALFaultWorkflow, &processExitDrainVerifier{})
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("reopen protected root in a fresh process: %v", err)
	}
	if err := root.VerifyCheckpoint(ctx, sqliteWALFaultWorkflow, state.Epoch, state.Digest, state.Incarnation); err != nil {
		t.Fatalf("fresh process: protected root no longer holds the exact predecessor head: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("reopen SQLite owner in a fresh process: %v", err)
	}
	var quickCheck string
	if err := owner.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("fresh process quick_check=%q err=%v", quickCheck, err)
	}
	stored, storedDigest, storedExists, loadErr := owner.LoadCheckpoint(ctx, sqliteWALFaultWorkflow)
	if caseName == "root-anchor-ioerr" {
		// The owner head was committed but never anchored by the root. A fresh
		// process must reject it, and the rejected head must be the durable one.
		if !errors.Is(loadErr, ErrRollback) {
			t.Fatalf("fresh process accepted or misclassified an unanchored owner head: exists=%v digest=%s err=%v",
				storedExists, storedDigest, loadErr)
		}
		persisted, persistedDigest, persistedExists, _, rawErr := loadLedger(ctx, owner.db, sqliteWALFaultWorkflow)
		if rawErr != nil || !persistedExists || persistedDigest == state.Digest || persisted.ComputedCheckpointDigest != persistedDigest ||
			persisted.Counters.HostCalls != state.HostCalls+1 || persisted.Pins.EffectReservation == nil {
			t.Fatalf("rejected head is not the durable committed reservation: digest=%s exists=%v err=%v counters=%+v",
				persistedDigest, persistedExists, rawErr, persisted.Counters)
		}
		t.Log("SQLITE_WAL_FAULT_RECOVERED=unanchored-head-rejected")
		return
	}
	if loadErr != nil || !storedExists || storedDigest != state.Digest ||
		stored.Counters.HostCalls != state.HostCalls || stored.Counters.Tokens != state.Tokens || stored.Pins.EffectReservation != nil {
		t.Fatalf("fresh process did not recover the exact predecessor: exists=%v digest=%s err=%v counters=%+v effect=%+v",
			storedExists, storedDigest, loadErr, stored.Counters, stored.Pins.EffectReservation)
	}
	t.Log("SQLITE_WAL_FAULT_RECOVERED=predecessor")
}

func requireSQLiteWALFaultChain(t *testing.T, caseName string, err error, sentinels ...error) {
	t.Helper()
	for _, sentinel := range sentinels {
		if !errors.Is(err, sentinel) {
			t.Fatalf("%s: error chain lost %v: %v", caseName, sentinel, err)
		}
	}
}

func requireSQLiteWALFaultCode(t *testing.T, caseName string, err error, want int) {
	t.Helper()
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("%s: error chain lost the modernc SQLite cause: %T %v", caseName, err, err)
	}
	if code := sqliteErr.Code(); code != want {
		t.Fatalf("%s: SQLite code %d, want %d: %v", caseName, code, want, err)
	}
}

func limitOwnerPageCount(t *testing.T, owner *SQLiteOwner) {
	t.Helper()
	ctx := context.Background()
	var pages int64
	if err := owner.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatalf("read owner page count: %v", err)
	}
	if _, err := owner.db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", pages)); err != nil {
		t.Fatalf("limit owner page count: %v", err)
	}
}

func sqliteWALFaultFileSize(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s before arming RLIMIT_FSIZE: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
	return uint64(info.Size())
}

func skipSQLiteWALFault(t *testing.T, reason string) {
	t.Helper()
	t.Logf("SQLITE_WAL_FAULT_SKIPPED=%s", reason)
	t.Skip(reason)
}
