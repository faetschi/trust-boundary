//go:build linux

package workflowrecovery

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

const (
	sqliteCorruptionHelperEnv = "TBOUND_SQLITE_CORRUPTION_HELPER"
	sqliteCorruptionTargetEnv = "TBOUND_SQLITE_CORRUPTION_TARGET"
	sqliteCorruptionRootEnv   = "TBOUND_SQLITE_CORRUPTION_ROOT"
	sqliteCorruptionStoreEnv  = "TBOUND_SQLITE_CORRUPTION_STORE"
	sqliteCorruptionWorkflow  = "sqlite-corruption"
)

// These tests corrupt only closed, test-owned SQLite files after a durable
// effect reservation. A clean child-process reopen is the positive control;
// the failing reopen must identify actual SQLite corruption, not a synthetic
// ErrCorrupt or a still-recoverable WAL tail.
func TestSQLiteOwnerRealCorruptionFailsClosedAfterReservedEffect(t *testing.T) {
	for _, target := range []string{"owner", "protected-root"} {
		t.Run(target, func(t *testing.T) {
			rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
			seedOutput := runSQLiteCorruptionChild(t, "seed", target, rootPath, ownerStoreRoot)
			if !strings.Contains(string(seedOutput), "SQLITE_CORRUPTION_SEEDED=reserved-effect") {
				t.Fatalf("fixture child did not persist the effect reservation:\n%s", seedOutput)
			}
			for _, path := range []string{rootPath, filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")} {
				requireSQLiteCorruptionWALCheckpointed(t, path)
			}

			controlOutput := runSQLiteCorruptionChild(t, "control", target, rootPath, ownerStoreRoot)
			if !strings.Contains(string(controlOutput), "SQLITE_CORRUPTION_CONTROL=reopened-pending-reservation") ||
				!strings.Contains(string(controlOutput), "next_grant=refused callback=0") {
				t.Fatalf("clean positive control did not reopen and refuse another grant:\n%s", controlOutput)
			}
			for _, path := range []string{rootPath, filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")} {
				requireSQLiteCorruptionWALCheckpointed(t, path)
			}

			corruptPath := rootPath
			if target == "owner" {
				corruptPath = filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
			}
			corruptSQLiteHeaderByte(t, corruptPath)

			verifyOutput := runSQLiteCorruptionChild(t, "verify", target, rootPath, ownerStoreRoot)
			marker := "SQLITE_CORRUPTION_REJECTED=" + target
			if !strings.Contains(string(verifyOutput), marker) ||
				!strings.Contains(string(verifyOutput), "callback=0") {
				t.Fatalf("fresh process did not reject corrupted store before effect callback:\n%s", verifyOutput)
			}
		})
	}
}

func runSQLiteCorruptionChild(t *testing.T, phase, target, rootPath, ownerStoreRoot string) []byte {
	t.Helper()
	return runHelperChild(t, "TestSQLiteOwnerCorruptionHelper", "SQLITE_CORRUPTION_", phase, target,
		sqliteCorruptionHelperEnv+"="+phase,
		sqliteCorruptionTargetEnv+"="+target,
		sqliteCorruptionRootEnv+"="+rootPath,
		sqliteCorruptionStoreEnv+"="+ownerStoreRoot,
	)
}

func TestSQLiteOwnerCorruptionHelper(t *testing.T) {
	phase := os.Getenv(sqliteCorruptionHelperEnv)
	if phase == "" {
		return
	}
	target := os.Getenv(sqliteCorruptionTargetEnv)
	rootPath := os.Getenv(sqliteCorruptionRootEnv)
	ownerStoreRoot := os.Getenv(sqliteCorruptionStoreEnv)
	if target != "owner" && target != "protected-root" {
		t.Fatalf("unknown corruption target %q", target)
	}
	switch phase {
	case "seed":
		seedSQLiteCorruptionFixture(t, rootPath, ownerStoreRoot)
	case "control":
		verifySQLiteCorruptionPositiveControl(t, rootPath, ownerStoreRoot)
	case "verify":
		verifySQLiteCorruptionFailsClosed(t, target, rootPath, ownerStoreRoot)
	default:
		t.Fatalf("unknown corruption helper phase %q", phase)
	}
}

func seedSQLiteCorruptionFixture(t *testing.T, rootPath, ownerStoreRoot string) {
	t.Helper()
	ctx := context.Background()
	ownerPath := filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	_, root := createProtectedRoot(t, rootPath, ownerStoreRoot, sqliteCorruptionWorkflow)
	owner, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create SQLite owner: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(),
		sqliteCorruptionWorkflow, "session-corruption", 1, 1)
	if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
		_ = owner.Close()
		_ = root.Close()
		t.Fatalf("initialize SQLite workflow: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, sqliteCorruptionWorkflow)
	if err != nil || !exists {
		t.Fatalf("load initialized checkpoint: exists=%t err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: sqliteCorruptionWorkflow,
		OwnerEpoch: checkpoint.Pins.OwnerEpoch, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open initialized ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript("conversation-corruption")); err != nil {
		t.Fatalf("register effect transcript: %v", err)
	}
	reservation, err := ledger.ReserveEffect(ctx, testEffectBinding("operation-corruption"), nil)
	if err != nil || reservation.CheckpointDigest() == "" {
		t.Fatalf("reserve durable effect: receipt=%q err=%v", reservation.CheckpointDigest(), err)
	}
	checkpoint, digest := ledger.Snapshot()
	if checkpoint.Pins.EffectReservation == nil ||
		checkpoint.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		checkpoint.Pins.EffectReservation.Binding.OperationID != "operation-corruption" ||
		checkpoint.Counters.HostCalls != 1 {
		t.Fatalf("effect reservation was not durably represented: digest=%s counters=%+v effect=%+v",
			digest, checkpoint.Counters, checkpoint.Pins.EffectReservation)
	}
	stored, storedDigest, storedExists, err := owner.LoadCheckpoint(ctx, sqliteCorruptionWorkflow)
	if err != nil || !storedExists || storedDigest != digest ||
		stored.Pins.EffectReservation == nil || stored.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("durable effect reservation readback: exists=%t digest=%s err=%v", storedExists, storedDigest, err)
	}
	checkpointSQLiteCorruptionWAL(t, "owner", owner.db)
	checkpointSQLiteCorruptionWAL(t, "protected-root", root.db)
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner fixture: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close protected-root fixture: %v", err)
	}
	t.Log("SQLITE_CORRUPTION_SEEDED=reserved-effect")
}

func checkpointSQLiteCorruptionWAL(t *testing.T, name string, db *sql.DB) {
	t.Helper()
	var busy, logFrames, checkpointed int64
	if err := db.QueryRowContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		t.Fatalf("checkpoint %s WAL: %v", name, err)
	}
	if busy != 0 || logFrames != checkpointed {
		t.Fatalf("checkpoint %s WAL: busy=%d log_frames=%d checkpointed=%d", name, busy, logFrames, checkpointed)
	}
}

func requireSQLiteCorruptionWALCheckpointed(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("inspect closed WAL %s: %v", path+"-wal", err)
	}
	if info.Size() != 0 {
		t.Fatalf("closed database retained %d WAL bytes at %s; corruption might be recovered from a valid WAL", info.Size(), path+"-wal")
	}
}

func verifySQLiteCorruptionPositiveControl(t *testing.T, rootPath, ownerStoreRoot string) {
	t.Helper()
	ctx := context.Background()
	root, owner, checkpoint := openSQLiteCorruptionFixture(t, rootPath, ownerStoreRoot)
	defer root.Close()
	defer owner.Close()
	if checkpoint.Pins.EffectReservation == nil ||
		checkpoint.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		checkpoint.Pins.EffectReservation.Binding.OperationID != "operation-corruption" ||
		checkpoint.Counters.HostCalls != 1 {
		t.Fatalf("clean reopen lost the durable reservation: counters=%+v effect=%+v",
			checkpoint.Counters, checkpoint.Pins.EffectReservation)
	}
	var callbacks int
	nextEpoch, err := root.NextOwnerEpoch(ctx, sqliteCorruptionWorkflow)
	if err != nil || nextEpoch != checkpoint.Pins.OwnerEpoch+1 {
		t.Fatalf("next owner epoch candidate=%d err=%v", nextEpoch, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: sqliteCorruptionWorkflow,
		OwnerEpoch: nextEpoch, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("reopen ledger at drained next epoch: %v", err)
	}
	reservation, err := ledger.ReserveEffect(ctx, testEffectBinding("operation-next-after-corruption"), nil)
	// The test models the effect callback as reachable only after a successful
	// durable grant. It does not execute a production or external effect.
	if err == nil && reservation.CheckpointDigest() != "" {
		callbacks++
	}
	if !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("second effect reservation=%v; want ErrPendingUnknown for retained reservation", err)
	}
	if callbacks != 0 {
		t.Fatalf("effect callback count=%d after refused grant; want zero", callbacks)
	}
	t.Log("SQLITE_CORRUPTION_CONTROL=reopened-pending-reservation next_grant=refused callback=0")
}

func openSQLiteCorruptionFixture(t *testing.T, rootPath, ownerStoreRoot string) (*ProtectedTrustedRoot, *SQLiteOwner, Checkpoint) {
	t.Helper()
	ctx := context.Background()
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, sqliteCorruptionWorkflow, &processExitDrainVerifier{})
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("open protected root: %v", err)
	}
	owner, err := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
	if err != nil {
		_ = root.Close()
		t.Fatalf("open SQLite owner: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, sqliteCorruptionWorkflow)
	if err != nil || !exists {
		_ = owner.Close()
		_ = root.Close()
		t.Fatalf("load reopened checkpoint: exists=%t err=%v", exists, err)
	}
	return root, owner, checkpoint
}

func verifySQLiteCorruptionFailsClosed(t *testing.T, target, rootPath, ownerStoreRoot string) {
	t.Helper()
	corruptPath := rootPath
	if target == "owner" {
		corruptPath = filepath.Join(ownerStoreRoot, "workflow-owner.sqlite")
	}
	code := requireSQLiteCorruptionFromEngine(t, corruptPath)
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, sqliteCorruptionWorkflow, &processExitDrainVerifier{})
	var root *ProtectedTrustedRoot
	if target == "protected-root" {
		opened, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
		if opened != nil {
			_ = opened.Close()
		}
		requireSQLiteCorruptionClassified(t, err, "protected-root open", code)
	} else {
		var err error
		root, err = OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
		if err != nil {
			t.Fatalf("unmodified protected root did not reopen: %v", err)
		}
		defer root.Close()
		opened, ownerErr := OpenExistingSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow-owner.sqlite"), root)
		if opened != nil {
			_ = opened.Close()
		}
		requireSQLiteCorruptionClassified(t, ownerErr, "owner open", code)
	}
	var callbacks int
	if callbacks != 0 {
		t.Fatalf("effect callback count=%d after corrupt-store refusal; want zero", callbacks)
	}
	t.Logf("SQLITE_CORRUPTION_REJECTED=%s sqlite_primary_code=%d callback=0", target, code)
}

func requireSQLiteCorruptionClassified(t *testing.T, err error, stage string, wantPrimaryCode int) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s accepted the structurally corrupted database", stage)
	}
	if !errors.Is(err, ErrOwnerStore) && !errors.Is(err, ErrRollback) {
		t.Fatalf("%s corruption error=%v; want ErrOwnerStore or ErrRollback", stage, err)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != wantPrimaryCode {
		t.Fatalf("%s corruption error=%v; want preserved SQLite primary code %d", stage, err, wantPrimaryCode)
	}
}

func requireSQLiteCorruptionFromEngine(t *testing.T, path string) int {
	t.Helper()
	fileURL := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	fileURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", fileURL.String())
	if err != nil {
		t.Fatalf("open raw SQLite corruption probe: %v", err)
	}
	defer db.Close()
	var integrity string
	err = db.QueryRowContext(context.Background(), "PRAGMA quick_check").Scan(&integrity)
	if err == nil {
		t.Fatalf("SQLite quick_check accepted the corrupted database with result %q; a WAL recovery or invalid corruption case may have hidden the fault", integrity)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("corrupt-file probe error %T %v; want an actual SQLite engine error", err, err)
	}
	code := sqliteErr.Code()
	primary := code & 0xff
	if primary != 11 && primary != 26 { // SQLITE_CORRUPT or SQLITE_NOTADB.
		t.Fatalf("SQLite corruption primary code=%d full_code=%d; want SQLITE_CORRUPT(11) or SQLITE_NOTADB(26): %v", primary, code, err)
	}
	t.Logf("SQLITE_CORRUPTION_ENGINE=quick_check_rejected primary_code=%d full_code=%d", primary, code)
	return primary
}

func corruptSQLiteHeaderByte(t *testing.T, path string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SQLite database before mutation: %v", err)
	}
	const signature = "SQLite format 3\x00"
	if len(contents) < len(signature) || string(contents[:len(signature)]) != signature {
		t.Fatalf("database %s does not have the expected SQLite header", path)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open closed SQLite database for deterministic byte fault: %v", err)
	}
	if n, err := file.WriteAt([]byte{'X'}, 0); err != nil || n != 1 {
		_ = file.Close()
		t.Fatalf("mutate SQLite header byte: wrote=%d err=%v", n, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync SQLite header corruption: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close corrupted SQLite database: %v", err)
	}
	mutated, err := os.ReadFile(path)
	if err != nil || len(mutated) == 0 || mutated[0] != 'X' {
		var first byte
		if len(mutated) > 0 {
			first = mutated[0]
		}
		t.Fatalf("verify deterministic SQLite header mutation: byte=%q err=%v", first, err)
	}
	t.Logf("SQLITE_CORRUPTION_MUTATION=header-byte-0 path_class=%s original=%q mutated=%q",
		filepath.Base(filepath.Dir(path)), signature[:1], string(mutated[:1]))
}
