//go:build linux

package workflowrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const protectedRootHelperModeEnv = "TBOUND_PROTECTED_ROOT_LIFECYCLE_MODE"
const settledCheckpointHelperModeEnv = "TBOUND_SETTLED_CHECKPOINT_READ_MODE"

// processExitDrainVerifier is a PID-only TEST DOUBLE, not drain evidence. It
// checks only that no process still carries each recorded PID and incarnation
// digest. It observes no cgroup, scope, cell, or receipt state. Its drain
// digest binds only the owner identity, so it is weaker than the production
// verifier (catalogOwnerDrainVerifier in cmd/tbound), whose digest is the
// drainproof.Prove digest of the observed scope facts. Use it only where a test
// needs owner epochs to be accepted and does not test drain strength.
type processExitDrainVerifier struct {
	calls [][]OwnerEpochIdentity
}

func (v *processExitDrainVerifier) VerifyOwnerCellsDrainedWithProof(_ context.Context, workflow string,
	owners []OwnerEpochIdentity) ([]string, error) {
	if !validID(workflow) || len(owners) == 0 {
		return nil, errors.New("empty owner drain request")
	}
	copyOfOwners := append([]OwnerEpochIdentity(nil), owners...)
	digests := make([]string, 0, len(copyOfOwners))
	for _, owner := range copyOfOwners {
		if owner.Epoch == 0 || !validOwnerIncarnation(owner.Incarnation) {
			return nil, errors.New("malformed owner incarnation in drain request")
		}
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(owner.Incarnation.PID))); err == nil {
			observed, identityErr := ownerIncarnationForPID(owner.Incarnation.PID)
			if identityErr != nil {
				return nil, fmt.Errorf("cannot verify live prior owner PID %d: %w", owner.Incarnation.PID, identityErr)
			}
			if observed.Digest == owner.Incarnation.Digest {
				return nil, fmt.Errorf("prior owner epoch %d is still live", owner.Epoch)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot inspect prior owner PID %d: %w", owner.Incarnation.PID, err)
		}
		digests = append(digests, pidOnlyTestDrainDigest(owner))
	}
	v.calls = append(v.calls, copyOfOwners)
	return digests, nil
}

// pidOnlyTestDrainDigest is the test double's drain digest. It commits to the
// owner identity only, which is exactly the weakness the doc comment records.
func pidOnlyTestDrainDigest(owner OwnerEpochIdentity) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("tbound-pid-only-test-double-drain/v1\n%d\n%s",
		owner.Epoch, owner.Incarnation.Digest)))
	return hex.EncodeToString(sum[:])
}

// proofDrainVerifier returns caller-chosen drain digests so a test can observe
// exactly what the protected root persists. It proves nothing about the host.
type proofDrainVerifier struct {
	digest    string
	short     bool
	malformed bool
}

func (v *proofDrainVerifier) VerifyOwnerCellsDrainedWithProof(_ context.Context, workflow string,
	owners []OwnerEpochIdentity) ([]string, error) {
	if !validID(workflow) || len(owners) == 0 {
		return nil, errors.New("empty owner drain request")
	}
	digests := make([]string, 0, len(owners))
	for range owners {
		digests = append(digests, v.digest)
	}
	switch {
	case v.short:
		return digests[1:], nil
	case v.malformed:
		digests[0] = "not-a-digest"
	}
	return digests, nil
}

type allowProtectedFreshWorkflow struct {
	workflow string
}

type acceptBoundTerminalEffect struct{}

func (acceptBoundTerminalEffect) VerifyTerminalEffect(_ context.Context, previous Checkpoint,
	binding EffectBinding, next Pins) error {
	if previous.Pins.EffectReservation == nil || previous.Pins.EffectReservation.Binding != binding ||
		previous.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		next.EffectReservation == nil || next.EffectReservation.Binding != binding ||
		next.EffectReservation.Disposition != EffectReservationTerminal || !validDigest(next.EffectReservation.OutcomeDigest) {
		return ErrObservation
	}
	return nil
}

type rejectTerminalCheckpointCAS struct {
	*ProtectedTrustedRoot
	err error
}

func (r *rejectTerminalCheckpointCAS) CompareAndSwapTerminalCheckpoint(context.Context, string, uint64,
	string, Checkpoint, Checkpoint, TransitionProof, OwnerStoreIdentity, OwnerIncarnation) error {
	return r.err
}

func (p allowProtectedFreshWorkflow) AuthorizeFreshWorkflow(_ context.Context, workflow string, epoch uint64,
	initialDigest string, incarnation OwnerIncarnation, deadline time.Time, rootIdentity string, store OwnerStoreIdentity) error {
	if workflow != p.workflow || epoch == 0 || !validDigest(initialDigest) || !validOwnerIncarnation(incarnation) ||
		deadline.IsZero() || deadline.Location() != time.UTC || !validDigest(rootIdentity) || store.Path == "" || !validDigest(store.Digest) {
		return ErrFreshAuthorization
	}
	return nil
}

func newProtectedRootTestDirs(t *testing.T) (string, string, string) {
	t.Helper()
	base := t.TempDir()
	rootDir := filepath.Join(base, "host-root")
	ownerStoreRoot := filepath.Join(base, "owner-stores")
	if err := os.Mkdir(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ownerStoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownerStoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(rootDir, "workflow-root.sqlite"), ownerStoreRoot, base
}

func newProtectedRootTestBoundary(t *testing.T, ownerStoreRoot, workflow string,
	drains OwnerDrainVerifier) *LinuxHostBoundary {
	t.Helper()
	boundary, err := NewLinuxHostBoundary(ownerStoreRoot, uint32(os.Geteuid()), drains, allowProtectedFreshWorkflow{workflow: workflow})
	if err != nil {
		t.Fatalf("create Linux host boundary: %v", err)
	}
	return boundary
}

func TestProtectedRootLocksNamespaceAndRejectsForgedProcessIncarnation(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-lock"
	drains := &processExitDrainVerifier{}
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, drains)
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()

	if _, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("competing root open = %v, want ErrOwnerFence", err)
	}
	if next, err := root.NextOwnerEpoch(context.Background(), workflow); err != nil || next != 1 {
		t.Fatalf("first protected epoch candidate = %d, %v; want 1", next, err)
	}
	incarnation, err := currentProcessIncarnation()
	if err != nil {
		t.Fatal(err)
	}
	incarnation.StartTimeTicks++
	incarnation.Digest = computeOwnerIncarnationDigest(incarnation)
	if err := root.AcquireOwnerEpoch(context.Background(), workflow, 0, 1, incarnation); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("forged process incarnation acquisition = %v, want ErrOwnerFence", err)
	}
	if next, err := root.NextOwnerEpoch(context.Background(), workflow); err != nil || next != 1 {
		t.Fatalf("failed forged acquisition changed the fence: next=%d err=%v", next, err)
	}
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create fresh owner database for epoch-gap check: %v", err)
	}
	defer owner.Close()
	if _, err := owner.OwnerStoreIdentity(context.Background(), workflow); err != nil {
		t.Fatalf("verify fresh owner database before epoch-gap check: %v", err)
	}
	if err := owner.AcquireOwnerEpoch(context.Background(), workflow, 0, 2); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("first owner epoch skipped from 0 to 2: %v", err)
	}
	if next, err := owner.NextOwnerEpoch(context.Background(), workflow); err != nil || next != 1 {
		t.Fatalf("rejected first-epoch gap changed candidate: next=%d err=%v", next, err)
	}
}

func TestProtectedRootLockPathReplacementFailsClosed(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-lock-replacement"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	lockPath := rootPath + ".lock"
	retainedLockPath := lockPath + ".retained"
	if err := os.Rename(lockPath, retainedLockPath); err != nil {
		_ = root.Close()
		t.Fatalf("rename held root lock path: %v", err)
	}
	replacement, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create replacement lock path: %v", err)
	}
	if err := replacement.Close(); err != nil {
		_ = root.Close()
		t.Fatalf("close replacement lock path: %v", err)
	}
	if _, err := root.NextOwnerEpoch(context.Background(), workflow); !errors.Is(err, ErrOwnerFence) {
		_ = root.Close()
		t.Fatalf("protected root accepted a replaced lock path: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close protected root after lock replacement: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove replacement lock path: %v", err)
	}
	if err := os.Symlink(retainedLockPath, lockPath); err != nil {
		t.Fatalf("replace root lock with symlink fixture: %v", err)
	}
	if _, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary); err == nil {
		t.Fatal("existing protected root followed a symlinked lock path")
	}
}

func TestProtectedRootRejectsOverlappingNamespaces(t *testing.T) {
	base := filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "protected-root-overlap"))
	for _, test := range []struct {
		left, right string
		want        bool
	}{
		{left: filepath.Join(base, "root"), right: filepath.Join(base, "store"), want: false},
		{left: filepath.Join(base, "root"), right: filepath.Join(base, "root", "nested"), want: true},
		{left: filepath.Join(base, "root", "nested"), right: filepath.Join(base, "root"), want: true},
		{left: filepath.Join(base, "root"), right: filepath.Join(base, "root"), want: true},
		{left: filepath.Join(base, "store"), right: filepath.Join(base, "store-copy"), want: false},
	} {
		if got := pathsOverlap(test.left, test.right); got != test.want {
			t.Errorf("pathsOverlap(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
		}
	}
}

func TestProtectedRootPersistsHeadAndRequiresDrainOfAbandonedFence(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-restart"
	ownerPath := filepath.Join(ownerStoreRoot, "workflow.sqlite")
	bootstrapBoundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	bootstrap, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, bootstrapBoundary)
	if err != nil {
		t.Fatalf("bootstrap protected root: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrapped protected root: %v", err)
	}

	// Process one initializes the original workflow and exits with epoch 1
	// durably accepted in the root and SQLite owner.
	runProtectedRootHelper(t, rootPath, ownerStoreRoot, ownerPath, workflow, "fresh")

	// A candidate read by one process is not a reservation. Remember epoch 2,
	// close its root lock, and let a later process acquire that epoch first.
	selectorBoundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	selectorRoot, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, selectorBoundary)
	if err != nil {
		t.Fatalf("open selector protected root: %v", err)
	}
	selectorOwner, err := OpenExistingSQLiteOwner(ownerPath, selectorRoot)
	if err != nil {
		_ = selectorRoot.Close()
		t.Fatalf("open selector owner: %v", err)
	}
	selectedCheckpoint, _, exists, err := selectorOwner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		_ = selectorOwner.Close()
		_ = selectorRoot.Close()
		t.Fatalf("load selector checkpoint: exists=%v err=%v", exists, err)
	}
	staleCandidate, err := selectorOwner.NextOwnerEpoch(context.Background(), workflow)
	if err != nil || staleCandidate != 2 {
		_ = selectorOwner.Close()
		_ = selectorRoot.Close()
		t.Fatalf("select stale test candidate: candidate=%d err=%v; want 2", staleCandidate, err)
	}
	if err := selectorOwner.Close(); err != nil {
		t.Fatalf("close selector owner: %v", err)
	}
	if err := selectorRoot.Close(); err != nil {
		t.Fatalf("release selector root lock: %v", err)
	}

	// Process two acquires epoch 2 but crashes before it publishes a checkpoint.
	runProtectedRootHelper(t, rootPath, ownerStoreRoot, ownerPath, workflow, "prepare")

	// A later process must take epoch 3 from the durable fence high-water. It
	// cannot reuse epoch 2 even though the accepted checkpoint still names 1.
	drains := &processExitDrainVerifier{}
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, drains)
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("open persistent protected root after child exits: %v", err)
	}
	defer root.Close()
	owner, err := OpenExistingSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("open persistent SQLite owner: %v", err)
	}
	defer owner.Close()
	checkpoint, _, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		t.Fatalf("load accepted original checkpoint: exists=%v err=%v", exists, err)
	}
	if err := owner.AcquireOwnerEpoch(context.Background(), workflow, selectedCheckpoint.Pins.OwnerEpoch, staleCandidate); !errors.Is(err, ErrOwnerFence) {
		t.Fatalf("stale advisory candidate acquired after another process fenced epoch 2: %v", err)
	}
	storeIdentity, err := owner.OwnerStoreIdentity(context.Background(), workflow)
	if err != nil || storeIdentity.Path != ownerPath || !validDigest(storeIdentity.Digest) {
		t.Fatalf("verified owner-store identity = %+v, %v", storeIdentity, err)
	}
	if next, err := owner.NextOwnerEpoch(context.Background(), workflow); err != nil || next != 3 {
		t.Fatalf("persisted owner epoch candidate = %d, %v; want 3", next, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 3, RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits,
	})
	if err != nil {
		t.Fatalf("open drained epoch 3 for recovery inspection: %v", err)
	}
	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("stale checkpoint admitted pre-effect retry = %v, want ErrRestartRequired", err)
	}
	if next, err := owner.NextOwnerEpoch(context.Background(), workflow); err != nil || next != 4 {
		t.Fatalf("post-acquisition fence candidate = %d, %v; want 4", next, err)
	}
	var drainedEpochs []uint64
	if len(drains.calls) == 1 {
		for _, owner := range drains.calls[0] {
			drainedEpochs = append(drainedEpochs, owner.Epoch)
		}
	}
	if !reflect.DeepEqual(drainedEpochs, []uint64{1, 2}) {
		t.Fatalf("drain proof did not cover both superseded owner epochs: %+v", drains.calls)
	}
	stored, err := loadOwnerEpochs(context.Background(), root.db, workflow, 1, 2)
	if err != nil || len(stored) != 2 || stored[0].Drained != 1 || stored[1].Drained != 1 ||
		!validDigest(stored[0].DrainEvidenceDigest) || !validDigest(stored[1].DrainEvidenceDigest) {
		t.Fatalf("persisted drain coverage = %+v, %v", stored, err)
	}
}

func TestProtectedRootFreshAuthorizationCannotResetReplacedOwnerStore(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-no-reset"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	ownerPath := filepath.Join(ownerStoreRoot, "workflow.sqlite")
	owner, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create original owner store: %v", err)
	}
	incarnation := owner.OwnerIncarnationDigest()
	pins := testPins(root.TrustedRootIdentityDigest(), incarnation, workflow, "session-original", 1, 1)
	if _, err := InitializeFresh(context.Background(), owner, pins, testLimits()); err != nil {
		t.Fatalf("initialize original workflow: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close original owner store: %v", err)
	}
	backupPath := ownerPath + ".original"
	if err := os.Rename(ownerPath, backupPath); err != nil {
		t.Fatalf("move original owner store in isolated test directory: %v", err)
	}
	replacement, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create replacement owner store: %v", err)
	}
	defer replacement.Close()
	replacementPins := testPins(root.TrustedRootIdentityDigest(), replacement.OwnerIncarnationDigest(),
		workflow, "session-reset", 1, 1)
	if _, err := InitializeFresh(context.Background(), replacement, replacementPins, testLimits()); !errors.Is(err, ErrOwnerFence) && !errors.Is(err, ErrFreshAuthorization) {
		t.Fatalf("replacement owner store reset existing workflow = %v, want fail closed", err)
	}
	if reflect.DeepEqual(pins, replacementPins) {
		t.Fatal("replacement workflow fixture unexpectedly reused the original pins")
	}
}

func TestProtectedRootRejectsSameContentOwnerDBReplacementAfterLocalMetadataRewrite(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-db-inode-binding"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()

	ownerPath := filepath.Join(ownerStoreRoot, "workflow.sqlite")
	original, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create original owner store: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), original.OwnerIncarnationDigest(), workflow, "session-retained", 1, 1)
	if _, err := InitializeFresh(context.Background(), original, pins, testLimits()); err != nil {
		t.Fatalf("initialize retained workflow: %v", err)
	}
	if err := original.Close(); err != nil {
		t.Fatalf("close original owner: %v", err)
	}

	retainedPath := ownerPath + ".retained"
	if err := os.Rename(ownerPath, retainedPath); err != nil {
		t.Fatalf("retain original owner database: %v", err)
	}
	contents, err := os.ReadFile(retainedPath)
	if err != nil {
		t.Fatalf("read retained owner database: %v", err)
	}
	if err := os.WriteFile(ownerPath, contents, 0o600); err != nil {
		t.Fatalf("write same-content replacement owner database: %v", err)
	}
	if err := os.Chmod(ownerPath, 0o600); err != nil {
		t.Fatalf("protect replacement owner database: %v", err)
	}
	_, replacementIdentity, err := inspectSQLitePath(ownerPath, false)
	if err != nil {
		t.Fatalf("inspect replacement owner database: %v", err)
	}
	db, err := openOwnerDatabase(ownerPath, sqliteOwnerMaxPageCount)
	if err != nil {
		t.Fatalf("open replacement to rewrite its local identity metadata: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workflow_recovery_metadata
		SET storage_identity_digest=? WHERE singleton=1`, replacementIdentity); err != nil {
		_ = db.Close()
		t.Fatalf("rewrite replacement-local storage identity: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close metadata-rewritten replacement: %v", err)
	}

	// Local metadata now agrees with the replacement inode and the checkpoint
	// contents are byte-for-byte copied. Only the independently retained root
	// identity distinguishes this replacement from the original owner store.
	replacement, err := OpenExistingSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("replacement-local metadata should be self-consistent: %v", err)
	}
	defer replacement.Close()
	if _, _, _, err := replacement.LoadCheckpoint(context.Background(), workflow); !errors.Is(err, ErrOwnerFence) && !errors.Is(err, ErrOwnerStore) {
		t.Fatalf("protected root accepted same-content replacement owner database: %v", err)
	}
}

func TestProtectedRootDeadlineUsesStickyBootTimeExpiryAcrossWallRollbackAndReopen(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-deadline-sticky-expiry"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	baseWall := time.Now().Round(0).UTC()
	deadline := baseWall.Add(time.Hour).UTC()
	bootID := "00000000-0000-4000-8000-000000000001"
	bootTimeNS := int64(5_000_000_000)
	wallNow := baseWall
	root.clockNow = func() (time.Time, string, int64, error) { return wallNow, bootID, bootTimeNS, nil }
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create owner store: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-deadline", 1, 1)
	limits := testLimits()
	limits.Deadline = deadline
	if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
		_ = owner.Close()
		_ = root.Close()
		t.Fatalf("initialize original workflow: %v", err)
	}
	if got := owner.OwnerIncarnation(); got.Digest != owner.OwnerIncarnationDigest() || !validOwnerIncarnation(got) {
		t.Fatalf("owner incarnation accessor returned an invalid snapshot: %+v", got)
	}
	anchored, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists || anchored.WorkflowDeadlineUTC != deadline.Format(time.RFC3339Nano) ||
		anchored.ClockAnchorWallUTC != baseWall.Format(time.RFC3339Nano) {
		t.Fatalf("protected original deadline anchor = %+v exists=%v err=%v", anchored, exists, err)
	}

	// Let CLOCK_BOOTTIME pass the immutable deadline while wall time still
	// appears to have almost the full original duration left.
	bootTimeNS += int64(time.Hour + time.Second)
	wallNow = baseWall.Add(time.Second)
	if err := root.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrDeadline) {
		t.Fatalf("monotonic expiry = %v, want ErrDeadline", err)
	}
	state, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists || state.ClockExpired != 1 || state.ClockUncertain != 0 {
		t.Fatalf("persisted deadline status = %+v exists=%v err=%v", state, exists, err)
	}

	// A later plausible-looking rollback cannot clear the durable expiry.
	wallNow = baseWall
	bootTimeNS += int64(time.Second)
	if err := root.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrDeadline) {
		t.Fatalf("deadline after later wall rollback = %v, want sticky ErrDeadline", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("reopen protected root: %v", err)
	}
	defer reopened.Close()
	if err := reopened.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrDeadline) {
		t.Fatalf("reopened root cleared durable expiry: %v", err)
	}
}

func TestLoadSettledCheckpointReadOnlyAcceptsExpiredAndFreshBootHistory(t *testing.T) {
	for _, test := range []struct {
		name       string
		wantErr    error
		clockState func(*SQLiteOwner, string, time.Time, *time.Time, *string, *int64) error
	}{
		{
			name:    "expired",
			wantErr: ErrDeadline,
			clockState: func(owner *SQLiteOwner, workflow string, deadline time.Time,
				wall *time.Time, _ *string, bootNS *int64) error {
				*bootNS += int64(time.Hour + time.Second)
				*wall = wall.Add(time.Second)
				return owner.VerifyWorkflowDeadline(context.Background(), workflow, deadline)
			},
		},
		{
			name:    "fresh-boot-uncertain",
			wantErr: ErrClockUncertain,
			clockState: func(owner *SQLiteOwner, workflow string, deadline time.Time,
				wall *time.Time, bootID *string, bootNS *int64) error {
				*bootID = "00000000-0000-4000-8000-000000000099"
				*bootNS++
				*wall = wall.Add(time.Second)
				return owner.VerifyWorkflowDeadline(context.Background(), workflow, deadline)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
			workflow := "settled-view-" + strings.ReplaceAll(test.name, "-", "")
			boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
			root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
			if err != nil {
				t.Fatalf("create protected root: %v", err)
			}
			baseWall := time.Now().Round(0).UTC()
			wallNow := baseWall
			bootID := "00000000-0000-4000-8000-000000000091"
			bootTimeNS := int64(8_000_000_000)
			root.clockNow = func() (time.Time, string, int64, error) { return wallNow, bootID, bootTimeNS, nil }
			ownerPath := filepath.Join(ownerStoreRoot, workflow+".sqlite")
			owner, err := CreateFreshSQLiteOwner(ownerPath, root)
			if err != nil {
				_ = root.Close()
				t.Fatalf("create SQLite owner: %v", err)
			}
			deadline := baseWall.Add(time.Hour).UTC()
			pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow,
				"session-"+workflow, 1, 1)
			limits := testLimits()
			limits.Deadline = deadline
			if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
				_ = owner.Close()
				_ = root.Close()
				t.Fatalf("initialize workflow: %v", err)
			}
			if err := test.clockState(owner, workflow, deadline, &wallNow, &bootID, &bootTimeNS); !errors.Is(err, test.wantErr) {
				_ = owner.Close()
				_ = root.Close()
				t.Fatalf("clock transition = %v, want %v", err, test.wantErr)
			}
			if _, _, exists, err := owner.LoadCheckpoint(context.Background(), workflow); err == nil || exists {
				_ = owner.Close()
				_ = root.Close()
				t.Fatalf("normal checkpoint read bypassed workflow clock: exists=%v err=%v", exists, err)
			}
			before, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
			wantExpired, wantUncertain := int64(0), int64(0)
			if test.wantErr == ErrDeadline {
				wantExpired = 1
			}
			if test.wantErr == ErrClockUncertain {
				wantUncertain = 1
			}
			if err != nil || !exists || before.ClockExpired != wantExpired || before.ClockUncertain != wantUncertain {
				_ = owner.Close()
				_ = root.Close()
				t.Fatalf("sticky clock state = %+v exists=%v err=%v", before, exists, err)
			}
			if err := owner.Close(); err != nil {
				_ = root.Close()
				t.Fatalf("close SQLite owner before historical read: %v", err)
			}
			if err := root.Close(); err != nil {
				t.Fatalf("close root before fresh-process historical read: %v", err)
			}
			runSettledCheckpointReadHelper(t, rootPath, ownerStoreRoot, ownerPath, workflow,
				test.wantErr == ErrDeadline, test.wantErr == ErrClockUncertain)
			reopened, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
				newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
			if err != nil {
				t.Fatalf("reopen protected root: %v", err)
			}
			defer reopened.Close()
			after, exists, err := loadProtectedRootState(context.Background(), reopened.db, workflow)
			if err != nil || !exists || !reflect.DeepEqual(after, before) {
				t.Fatalf("fresh-process settled read mutated root: before=%+v after=%+v exists=%v err=%v",
					before, after, exists, err)
			}
			if err := reopened.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, test.wantErr) {
				t.Fatalf("settled view granted continuation after clock state %v: %v", test.wantErr, err)
			}
		})
	}
}

func TestLoadSettledCheckpointProcessHelper(t *testing.T) {
	if os.Getenv(settledCheckpointHelperModeEnv) == "" {
		return
	}
	rootPath := os.Getenv("TBOUND_SETTLED_CHECKPOINT_ROOT")
	ownerStoreRoot := os.Getenv("TBOUND_SETTLED_CHECKPOINT_STORE_ROOT")
	ownerPath := os.Getenv("TBOUND_SETTLED_CHECKPOINT_OWNER")
	workflow := os.Getenv("TBOUND_SETTLED_CHECKPOINT_WORKFLOW")
	wantExpired := os.Getenv("TBOUND_SETTLED_CHECKPOINT_EXPIRED") == "true"
	wantUncertain := os.Getenv("TBOUND_SETTLED_CHECKPOINT_UNCERTAIN") == "true"
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("open protected root in fresh process: %v", err)
	}
	defer root.Close()
	before, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists {
		t.Fatalf("read root state before historical view: exists=%v err=%v", exists, err)
	}
	clockCalls := 0
	root.clockNow = func() (time.Time, string, int64, error) {
		clockCalls++
		return time.Time{}, "", 0, errors.New("settled checkpoint read must not observe host clock")
	}
	view, err := LoadSettledCheckpoint(context.Background(), ownerPath, root, workflow)
	if err != nil || view.Digest == "" || view.Checkpoint.Pins.WorkflowID != workflow ||
		len(view.OwnerEpochs) != 1 || view.OwnerEpochs[0].Epoch != 1 ||
		view.ClockExpired != wantExpired || view.ClockUncertain != wantUncertain {
		t.Fatalf("fresh-process settled read = %+v err=%v", view, err)
	}
	after, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists || !reflect.DeepEqual(after, before) || clockCalls != 0 {
		t.Fatalf("fresh-process settled read mutated root or observed clock: before=%+v after=%+v exists=%v calls=%d err=%v",
			before, after, exists, clockCalls, err)
	}
}

func runSettledCheckpointReadHelper(t *testing.T, rootPath, ownerStoreRoot, ownerPath, workflow string,
	wantExpired, wantUncertain bool) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestLoadSettledCheckpointProcessHelper$")
	command.Env = append(os.Environ(),
		settledCheckpointHelperModeEnv+"=1",
		"TBOUND_SETTLED_CHECKPOINT_ROOT="+rootPath,
		"TBOUND_SETTLED_CHECKPOINT_STORE_ROOT="+ownerStoreRoot,
		"TBOUND_SETTLED_CHECKPOINT_OWNER="+ownerPath,
		"TBOUND_SETTLED_CHECKPOINT_WORKFLOW="+workflow,
		"TBOUND_SETTLED_CHECKPOINT_EXPIRED="+strconv.FormatBool(wantExpired),
		"TBOUND_SETTLED_CHECKPOINT_UNCERTAIN="+strconv.FormatBool(wantUncertain),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process settled checkpoint read failed: %v\n%s", err, output)
	}
	t.Logf("fresh-process settled checkpoint helper output:\n%s", output)
}

func TestLoadSettledCheckpointRejectsMismatchedRootDigestAndOwnerIdentity(t *testing.T) {
	rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
	workflow := "settled-view-integrity"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	ownerPath := filepath.Join(ownerStoreRoot, workflow+".sqlite")
	owner, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-integrity", 1, 1)
	if _, err := InitializeFresh(context.Background(), owner, pins, testLimits()); err != nil {
		_ = owner.Close()
		t.Fatalf("initialize workflow: %v", err)
	}
	checkpoint, digest, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		_ = owner.Close()
		t.Fatalf("load checkpoint: exists=%v err=%v", exists, err)
	}
	storeIdentity, err := owner.OwnerStoreIdentity(context.Background(), workflow)
	if err != nil {
		_ = owner.Close()
		t.Fatalf("read owner identity: %v", err)
	}
	if _, _, _, err := root.verifySettledCheckpoint(context.Background(), workflow, storeIdentity, checkpoint,
		testDigest("wrong checkpoint head")); !errors.Is(err, ErrOwnerFence) {
		t.Errorf("mismatched protected-root digest accepted: %v", err)
	}
	wrongStore := storeIdentity
	wrongStore.Digest = testDigest("wrong owner store identity")
	if _, _, _, err := root.verifySettledCheckpoint(context.Background(), workflow, wrongStore, checkpoint, digest); err == nil {
		t.Error("mismatched owner store identity accepted")
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner before read-only tests: %v", err)
	}

	otherRootDir := filepath.Join(base, "other-host-root")
	if err := os.Mkdir(otherRootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	otherRoot, err := CreateFreshProtectedRoot(filepath.Join(otherRootDir, "workflow-root.sqlite"), ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("create mismatched protected root: %v", err)
	}
	if _, err := LoadSettledCheckpoint(context.Background(), ownerPath, otherRoot, workflow); err == nil {
		t.Error("owner checkpoint bound to a different protected root was accepted")
	}
	if err := otherRoot.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openOwnerDatabase(ownerPath, sqliteOwnerMaxPageCount)
	if err != nil {
		t.Fatalf("open owner store to simulate tampering: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE workflow_recovery_state SET checkpoint_digest=? WHERE workflow_id=?`,
		testDigest("tampered latest checkpoint"), workflow); err != nil {
		_ = db.Close()
		t.Fatalf("tamper with SQLite current checkpoint row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tampered owner store: %v", err)
	}
	if _, err := LoadSettledCheckpoint(context.Background(), ownerPath, root, workflow); !errors.Is(err, ErrRollback) {
		t.Errorf("tampered owner checkpoint accepted or misclassified: %v", err)
	}
}

func TestTerminalEffectOutcomeCanOnlySettleExactReservationAfterStickyExpiry(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-late-effect-terminal"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	baseWall := time.Now().Round(0).UTC()
	deadline := baseWall.Add(time.Hour).UTC()
	bootID := "00000000-0000-4000-8000-000000000021"
	bootTimeNS := int64(20_000_000_000)
	wallNow := baseWall
	root.clockNow = func() (time.Time, string, int64, error) { return wallNow, bootID, bootTimeNS, nil }
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create owner store: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-late-effect", 1, 1)
	limits := testLimits()
	limits.Deadline = deadline
	if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
		_ = owner.Close()
		_ = root.Close()
		t.Fatalf("initialize workflow: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		t.Fatalf("load initial checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: checkpoint.Pins, Limits: limits,
	})
	if err != nil {
		t.Fatalf("open initial ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-late")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	settleInitialTranscript(t, ledger)
	reservation, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-late"), nil)
	if err != nil {
		t.Fatalf("reserve exact effect before deadline: %v", err)
	}
	before, beforeDigest := ledger.Snapshot()
	if beforeDigest != reservation.CheckpointDigest() || reservation.Counters().HostCalls != 1 {
		t.Fatalf("effect receipt is not the exact reserved checkpoint: digest=%s counters=%+v", beforeDigest, reservation.Counters())
	}

	terminalPins := clonePins(before.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("verified late Store outcome")
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("late Store primary journal head")
	terminalPins.StoreTipGenerationID = "g1-late-effect"
	terminalPins.StoreTipTreeDigest = testDigest("late Store tip tree")
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("late delta admission head")
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, terminalPins); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("generic progress settled an effect reservation: %v", err)
	}

	forgedPins := clonePins(terminalPins)
	forgedPins.OwnerEpoch++
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), reservation,
		acceptBoundTerminalEffect{}, forgedPins); !errors.Is(err, ErrRollback) {
		t.Fatalf("terminal effect changed an unrelated owner pin: %v", err)
	}
	candidate, proof := terminalEffectCandidateForTest(before, beforeDigest, terminalPins)
	counterCandidate := candidate
	counterCandidate.Counters.HostCalls++
	counterCandidate.ComputedCheckpointDigest = computeCheckpointDigest(counterCandidate)
	counterProof := proof
	counterProof.targetCounts = counterCandidate.Counters
	if err := root.CompareAndSwapTerminalCheckpoint(context.Background(), workflow, 1, beforeDigest,
		before, counterCandidate, counterProof, OwnerStoreIdentity{Path: owner.path, Digest: owner.storageIdentityDigest}, owner.OwnerIncarnation()); err == nil {
		t.Fatal("terminal root CAS accepted a fabricated host-call counter charge")
	}
	deadlineCandidate := candidate
	deadlineCandidate.Limits.Deadline = deadline.Add(time.Hour)
	deadlineCandidate.ComputedCheckpointDigest = computeCheckpointDigest(deadlineCandidate)
	if err := root.CompareAndSwapTerminalCheckpoint(context.Background(), workflow, 1, beforeDigest,
		before, deadlineCandidate, proof, OwnerStoreIdentity{Path: owner.path, Digest: owner.storageIdentityDigest}, owner.OwnerIncarnation()); !errors.Is(err, ErrRollback) {
		t.Fatalf("terminal root CAS accepted a changed original deadline: %v", err)
	}
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), Receipt{},
		acceptBoundTerminalEffect{}, terminalPins); !errors.Is(err, ErrObservation) {
		t.Fatalf("forged empty terminal receipt accepted: %v", err)
	}
	other := reservation
	other.effectBinding = testEffectBinding("other-operation")
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), other,
		acceptBoundTerminalEffect{}, terminalPins); !errors.Is(err, ErrObservation) {
		t.Fatalf("different reservation receipt accepted: %v", err)
	}

	// The original wall clock still appears before the deadline, but the
	// protected monotonic clock crosses it and stores a sticky expiry.
	wallNow = baseWall.Add(time.Second)
	bootTimeNS += int64(time.Hour + time.Second)
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), reservation,
		acceptBoundTerminalEffect{}, terminalPins); err != nil {
		t.Fatalf("account already-consumed known Store outcome after expiry: %v", err)
	}
	after, afterDigest := ledger.Snapshot()
	if afterDigest == beforeDigest || after.Counters != before.Counters || after.Limits != before.Limits ||
		after.Pins.EffectReservation == nil || after.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
		after.Pins.EffectReservation.OutcomeDigest != terminalPins.EffectReservation.OutcomeDigest ||
		after.Pins.StoreJournalHead != terminalPins.StoreJournalHead || after.Pins.AdmissionJournalHead != terminalPins.AdmissionJournalHead {
		t.Fatalf("late terminal accounting changed authority or failed to retain exact evidence: before=%+v after=%+v", before.Counters, after.Counters)
	}
	state, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists || state.ClockExpired != 1 || state.ClockUncertain != 0 {
		t.Fatalf("terminal accounting cleared or failed to persist expiry: %+v exists=%v err=%v", state, exists, err)
	}
	if err := root.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrDeadline) {
		t.Fatalf("terminal accounting reopened expired workflow authority: %v", err)
	}
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), reservation,
		acceptBoundTerminalEffect{}, terminalPins); !errors.Is(err, ErrObservation) {
		t.Fatalf("duplicate terminal accounting accepted: %v", err)
	}
	if _, err := ledger.ReserveEffect(context.Background(), testEffectBinding("new-after-expiry"), nil); !errors.Is(err, ErrDeadline) {
		t.Fatalf("expiry allowed new effect authority: %v", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
		newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
	if err != nil {
		t.Fatalf("reopen protected root after terminal accounting: %v", err)
	}
	defer reopened.Close()
	if err := reopened.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrDeadline) {
		t.Fatalf("reopened root lost sticky expiry after terminal accounting: %v", err)
	}
}

func TestTerminalEffectUncertainRootCASLeavesOwnerPoisonedAndClockPauseSticky(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-terminal-cas-uncertain"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	baseWall := time.Now().Round(0).UTC()
	deadline := baseWall.Add(time.Hour).UTC()
	bootID := "00000000-0000-4000-8000-000000000022"
	bootTimeNS := int64(30_000_000_000)
	clockError := error(nil)
	root.clockNow = func() (time.Time, string, int64, error) {
		if clockError != nil {
			return time.Time{}, "", 0, clockError
		}
		return baseWall, bootID, bootTimeNS, nil
	}
	rootWithAmbiguousCAS := &rejectTerminalCheckpointCAS{
		ProtectedTrustedRoot: root, err: errors.New("injected terminal root CAS ambiguity"),
	}
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), rootWithAmbiguousCAS)
	if err != nil {
		_ = root.Close()
		t.Fatalf("create owner store: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-terminal-cas", 1, 1)
	limits := testLimits()
	limits.Deadline = deadline
	if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
		_ = owner.Close()
		_ = root.Close()
		t.Fatalf("initialize workflow: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(context.Background(), workflow)
	if err != nil || !exists {
		t.Fatalf("load initial checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(context.Background(), owner, OpenOptions{
		WorkflowID: workflow, OwnerEpoch: 1, RetainedPins: checkpoint.Pins, Limits: limits,
	})
	if err != nil {
		t.Fatalf("open initial ledger: %v", err)
	}
	if _, err := ledger.RegisterInitialTranscript(context.Background(), allowProgress{}, emptyTranscript("conversation-cas")); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	settleInitialTranscript(t, ledger)
	reservation, err := ledger.ReserveEffect(context.Background(), testEffectBinding("operation-cas"), nil)
	if err != nil {
		t.Fatalf("reserve effect: %v", err)
	}
	before, beforeDigest := ledger.Snapshot()
	terminalPins := clonePins(before.Pins)
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("verified terminal result before uncertain CAS")
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal primary journal")
	terminalPins.StoreTipGenerationID = "g1-terminal-cas"
	terminalPins.StoreTipTreeDigest = testDigest("terminal Store tree")
	terminalPins.AdmissionJournalCount++
	terminalPins.AdmissionJournalHead = testDigest("terminal admission journal")

	clockError = errors.New("injected protected clock uncertainty")
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), reservation,
		acceptBoundTerminalEffect{}, terminalPins); !errors.Is(err, ErrCAS) {
		t.Fatalf("ambiguous terminal root CAS = %v, want ErrCAS", err)
	}
	after, afterDigest := ledger.Snapshot()
	if afterDigest != beforeDigest || after.Counters != before.Counters || after.Limits != before.Limits ||
		after.Pins.EffectReservation == nil || after.Pins.EffectReservation.Disposition != EffectReservationReserved {
		t.Fatalf("ambiguous root CAS was reported as success in the in-memory ledger: before=%+v after=%+v", before, after)
	}
	if _, _, _, err := owner.LoadCheckpoint(context.Background(), workflow); !errors.Is(err, ErrCAS) {
		t.Fatalf("owner remained usable after an ambiguous local/root commit: %v", err)
	}
	state, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
	if err != nil || !exists || state.ClockUncertain != 1 || state.ClockExpired != 0 {
		t.Fatalf("uncertainty pause was not retained across ambiguous terminal CAS: %+v exists=%v err=%v", state, exists, err)
	}
	if err := root.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrClockUncertain) {
		t.Fatalf("ambiguous terminal CAS cleared sticky clock uncertainty: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedRootDeadlineClockRollbackAndBootChangePersistPause(t *testing.T) {
	for _, scenario := range []string{"wall-rollback", "boot-change"} {
		t.Run(scenario, func(t *testing.T) {
			rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
			workflow := "protected-root-clock-pause-" + scenario
			boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
			root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
			if err != nil {
				t.Fatalf("create protected root: %v", err)
			}
			baseWall := time.Now().Round(0).UTC()
			deadline := baseWall.Add(time.Hour).UTC()
			bootID := "00000000-0000-4000-8000-000000000002"
			bootTimeNS := int64(9_000_000_000)
			wallNow := baseWall
			root.clockNow = func() (time.Time, string, int64, error) { return wallNow, bootID, bootTimeNS, nil }
			owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
			if err != nil {
				_ = root.Close()
				t.Fatalf("create owner store: %v", err)
			}
			pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-clock-pause", 1, 1)
			limits := testLimits()
			limits.Deadline = deadline
			if _, err := InitializeFresh(context.Background(), owner, pins, limits); err != nil {
				_ = owner.Close()
				_ = root.Close()
				t.Fatalf("initialize original workflow: %v", err)
			}

			bootTimeNS += int64(time.Second)
			if scenario == "wall-rollback" {
				wallNow = baseWall.Add(-time.Second)
			} else {
				wallNow = baseWall.Add(time.Second)
				bootID = "00000000-0000-4000-8000-000000000003"
			}
			if err := root.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrClockUncertain) {
				t.Fatalf("uncertain clock observation = %v, want ErrClockUncertain", err)
			}
			state, exists, err := loadProtectedRootState(context.Background(), root.db, workflow)
			if err != nil || !exists || state.ClockUncertain != 1 {
				t.Fatalf("persisted clock pause = %+v exists=%v err=%v", state, exists, err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := root.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot,
				newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{}))
			if err != nil {
				t.Fatalf("reopen protected root: %v", err)
			}
			defer reopened.Close()
			if err := reopened.VerifyWorkflowDeadline(context.Background(), workflow, deadline); !errors.Is(err, ErrClockUncertain) {
				t.Fatalf("reopened root cleared sticky clock pause: %v", err)
			}
		})
	}
}

func TestProtectedRootLifecycleProcessHelper(t *testing.T) {
	mode := os.Getenv(protectedRootHelperModeEnv)
	if mode == "" {
		return
	}
	rootPath := os.Getenv("TBOUND_PROTECTED_ROOT_LIFECYCLE_ROOT")
	ownerStoreRoot := os.Getenv("TBOUND_PROTECTED_ROOT_LIFECYCLE_STORE_ROOT")
	ownerPath := os.Getenv("TBOUND_PROTECTED_ROOT_LIFECYCLE_OWNER")
	workflow := os.Getenv("TBOUND_PROTECTED_ROOT_LIFECYCLE_WORKFLOW")
	drains := &processExitDrainVerifier{}
	boundary, err := NewLinuxHostBoundary(ownerStoreRoot, uint32(os.Geteuid()), drains,
		allowProtectedFreshWorkflow{workflow: workflow})
	if err != nil {
		t.Fatalf("create helper host boundary: %v", err)
	}
	root, err := OpenExistingProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("open helper protected root: %v", err)
	}
	defer root.Close()
	ctx := context.Background()
	switch mode {
	case "fresh":
		owner, err := CreateFreshSQLiteOwner(ownerPath, root)
		if err != nil {
			t.Fatalf("create helper owner store: %v", err)
		}
		defer owner.Close()
		pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(),
			workflow, "session-original", 1, 1)
		if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
			t.Fatalf("initialize helper workflow: %v", err)
		}
	case "prepare":
		owner, err := OpenExistingSQLiteOwner(ownerPath, root)
		if err != nil {
			t.Fatalf("open helper owner store: %v", err)
		}
		defer owner.Close()
		checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, workflow)
		if err != nil || !exists {
			t.Fatalf("load helper checkpoint: exists=%v err=%v", exists, err)
		}
		next, err := owner.NextOwnerEpoch(ctx, workflow)
		if err != nil || next != 2 {
			t.Fatalf("select prepared owner epoch: next=%d err=%v", next, err)
		}
		if err := owner.AcquireOwnerEpoch(ctx, workflow, checkpoint.Pins.OwnerEpoch, next); err != nil {
			t.Fatalf("persist abandoned owner-fence preparation: %v", err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runProtectedRootHelper(t *testing.T, rootPath, ownerStoreRoot, ownerPath, workflow, mode string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestProtectedRootLifecycleProcessHelper$")
	command.Env = append(os.Environ(),
		protectedRootHelperModeEnv+"="+mode,
		"TBOUND_PROTECTED_ROOT_LIFECYCLE_ROOT="+rootPath,
		"TBOUND_PROTECTED_ROOT_LIFECYCLE_STORE_ROOT="+ownerStoreRoot,
		"TBOUND_PROTECTED_ROOT_LIFECYCLE_OWNER="+ownerPath,
		"TBOUND_PROTECTED_ROOT_LIFECYCLE_WORKFLOW="+workflow,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("protected-root %s child process failed: %v\n%s", mode, err, output)
	}
}

// acquireEpochTwoWithDrainProof makes epoch 1 the accepted checkpoint, then
// acquires epoch 2 with a drain verifier that returns the given digests. It
// returns the persisted row for epoch 1 and the acquisition error.
func acquireEpochTwoWithDrainProof(t *testing.T, workflow string, verifier *proofDrainVerifier) (string, protectedOwnerEpoch, error) {
	t.Helper()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	ownerPath := filepath.Join(ownerStoreRoot, "workflow.sqlite")
	ctx := context.Background()
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, verifier)
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	owner, err := CreateFreshSQLiteOwner(ownerPath, root)
	if err != nil {
		t.Fatalf("create owner store: %v", err)
	}
	defer owner.Close()
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow, "session-original", 1, 1)
	if _, err := InitializeFresh(ctx, owner, pins, testLimits()); err != nil {
		t.Fatalf("initialize epoch 1: %v", err)
	}
	next, err := owner.NextOwnerEpoch(ctx, workflow)
	if err != nil || next != 2 {
		t.Fatalf("next owner epoch = %d, %v; want 2", next, err)
	}
	acquireErr := owner.AcquireOwnerEpoch(ctx, workflow, 1, next)
	stored, err := loadOwnerEpochs(ctx, root.db, workflow, 1, 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("load persisted epoch 1: %+v, %v", stored, err)
	}
	return root.rootIdentity, stored[0], acquireErr
}

// The persisted drain evidence must commit to the verifier's proof digest. Two
// different observations must persist different evidence, and the persisted
// value must be exactly the pure binding of the observed digest.
func TestOwnerDrainEvidencePersistsVerifierProofDigest(t *testing.T) {
	const workflow = "protected-root-drain-evidence"
	observationA := strings.Repeat("a", 64)
	observationB := strings.Repeat("b", 64)
	rootA, storedA, err := acquireEpochTwoWithDrainProof(t, workflow, &proofDrainVerifier{digest: observationA})
	if err != nil {
		t.Fatalf("drain with observation A was refused: %v", err)
	}
	rootB, storedB, err := acquireEpochTwoWithDrainProof(t, workflow, &proofDrainVerifier{digest: observationB})
	if err != nil {
		t.Fatalf("drain with observation B was refused: %v", err)
	}
	if storedA.DrainEvidenceDigest == storedB.DrainEvidenceDigest {
		t.Fatal("different drain observations persisted the same drain evidence digest")
	}
	if storedA.Drained != 1 || storedB.Drained != 1 {
		t.Fatalf("epoch 1 was not marked drained: a=%+v b=%+v", storedA, storedB)
	}
	wantA := ownerDrainEvidenceDigest(rootA, workflow, 1, storedA.IncarnationDigest, observationA)
	wantB := ownerDrainEvidenceDigest(rootB, workflow, 1, storedB.IncarnationDigest, observationB)
	if storedA.DrainEvidenceDigest != wantA || storedB.DrainEvidenceDigest != wantB {
		t.Fatalf("persisted evidence does not bind its observation: a=%s want %s; b=%s want %s",
			storedA.DrainEvidenceDigest, wantA, storedB.DrainEvidenceDigest, wantB)
	}
}

// Every superseded owner needs its own well-formed proof digest. A verifier
// that omits one, or returns a malformed one, must refuse the fence.
func TestOwnerDrainProofDigestsMustCoverEverySupersededOwner(t *testing.T) {
	for name, verifier := range map[string]*proofDrainVerifier{
		"missing digest":   {digest: strings.Repeat("a", 64), short: true},
		"malformed digest": {digest: "not-a-digest", malformed: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := acquireEpochTwoWithDrainProof(t, "protected-root-drain-coverage", verifier)
			if !errors.Is(err, ErrOwnerFence) {
				t.Fatalf("epoch 2 acquisition with %s = %v, want ErrOwnerFence", name, err)
			}
		})
	}
}

// The binding is a pure function of the root, workflow, epoch, incarnation and
// proof digest. Identical inputs give identical evidence, and any changed input
// changes it.
func TestOwnerDrainEvidenceDigestBindsEveryInput(t *testing.T) {
	incarnation := strings.Repeat("e", 64)
	proof := strings.Repeat("a", 64)
	base := ownerDrainEvidenceDigest("root", "wf", 1, incarnation, proof)
	if !validDigest(base) {
		t.Fatalf("drain evidence digest %q is not a SHA-256 digest", base)
	}
	if got := ownerDrainEvidenceDigest("root", "wf", 1, incarnation, proof); got != base {
		t.Fatalf("identical inputs gave different evidence: %s != %s", got, base)
	}
	for name, got := range map[string]string{
		"root":        ownerDrainEvidenceDigest("root-other", "wf", 1, incarnation, proof),
		"workflow":    ownerDrainEvidenceDigest("root", "wf-other", 1, incarnation, proof),
		"epoch":       ownerDrainEvidenceDigest("root", "wf", 2, incarnation, proof),
		"incarnation": ownerDrainEvidenceDigest("root", "wf", 1, strings.Repeat("f", 64), proof),
		"proof":       ownerDrainEvidenceDigest("root", "wf", 1, incarnation, strings.Repeat("b", 64)),
	} {
		if got == base {
			t.Fatalf("changing %s did not change the drain evidence digest", name)
		}
	}
}

func TestProtectedRootRejectsDifferentOwnerStoreRoot(t *testing.T) {
	rootPath, ownerStoreRoot, base := newProtectedRootTestDirs(t)
	workflow := "protected-root-store-binding"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close protected root: %v", err)
	}
	otherStoreRoot := filepath.Join(base, "different-owner-stores")
	if err := os.Mkdir(otherStoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(otherStoreRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	otherBoundary := newProtectedRootTestBoundary(t, otherStoreRoot, workflow, &processExitDrainVerifier{})
	opened, err := OpenExistingProtectedRoot(rootPath, otherStoreRoot, otherBoundary)
	if err == nil {
		_ = opened.Close()
		t.Fatal("protected root opened under a different owner-store namespace")
	}
	if !errors.Is(err, ErrRollback) && !strings.Contains(err.Error(), "identity") {
		t.Fatalf("different owner-store root error = %v, want an identity mismatch", err)
	}
}

func TestReserveHostCallAfterSuccessfulToolResultAllowsOnlyNextExchange(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-next-host-call-after-result"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	defer owner.Close()
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow,
		"session-next-host-call", 1, 1)
	limits := testLimits()
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
		t.Fatalf("open fresh ledger: %v", err)
	}

	priorBinding, _ := prepareCapturedCallForEffect(t, ledger,
		"operation-first-success", "provider-tool-call-first-success")
	effectReceipt, err := ledger.ReserveEffect(context.Background(), priorBinding,
		exactCapturedOperationVerifier{expected: priorBinding})
	if err != nil {
		t.Fatalf("reserve the exact captured effect: %v", err)
	}
	reserved, _ := ledger.Snapshot()
	terminalPins := clonePins(reserved.Pins)
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal Store outcome journal head")
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("verified terminal Store outcome")
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), effectReceipt,
		acceptBoundTerminalEffect{}, terminalPins); err != nil {
		t.Fatalf("settle the exact Store effect through protected-root terminal accounting: %v", err)
	}

	settled, _ := ledger.Snapshot()
	resultPins := clonePins(settled.Pins)
	resultPins.HostCallReservation.CapturedCall = nil
	resultPins.PendingCallDisposition = PendingCallTerminal
	resultPins.ProviderDisposition = DispositionAwaitingProviderExchange
	active := &resultPins.ProviderTranscripts[len(resultPins.ProviderTranscripts)-1]
	active.JournalCount++
	active.JournalHead = testDigest("terminal provider result journal head")
	active.ProjectionCount++
	active.ProjectionDigest = testDigest("terminal provider result projection")
	active.ProjectionHead = active.JournalHead
	active.Disposition = DispositionAwaitingProviderExchange
	resultPins.ProviderProjectionCount = totalProjectionCount(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionDigest = TranscriptPinsDigest(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionHead = active.ProjectionHead
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, resultPins); err != nil {
		t.Fatalf("checkpoint durable provider result after Store settlement: %v", err)
	}
	before, beforeDigest := ledger.Snapshot()
	if before.Pins.ProviderDisposition != DispositionAwaitingProviderExchange ||
		before.Pins.PendingCallDisposition != PendingCallTerminal || before.Pins.HostCallReservation == nil ||
		before.Pins.HostCallReservation.Disposition != HostCallTerminal || before.Pins.HostCallReservation.CapturedCall != nil ||
		before.Pins.EffectReservation == nil || before.Pins.EffectReservation.Disposition != EffectReservationTerminal {
		t.Fatalf("fixture is not the exact post-result state: pins=%+v", before.Pins)
	}

	if _, err := ledger.ReservePreEffectRetry(context.Background()); !errors.Is(err, ErrPendingUnknown) {
		t.Fatalf("retry reused the next-provider-exchange exception: %v, want ErrPendingUnknown", err)
	}
	unchanged, unchangedDigest := ledger.Snapshot()
	if unchangedDigest != beforeDigest || unchanged.Counters != before.Counters {
		t.Fatalf("refused retry changed the post-result checkpoint: before=%+v after=%+v", before.Counters, unchanged.Counters)
	}
	if _, err := ledger.ReserveHostCall(context.Background(), before.Pins.HostCallReservation.Binding); !errors.Is(err, ErrObservation) {
		t.Fatalf("reused prior provider-call identity after tool result: %v, want ErrObservation", err)
	}
	unchanged, unchangedDigest = ledger.Snapshot()
	if unchangedDigest != beforeDigest || unchanged.Counters != before.Counters {
		t.Fatalf("duplicate prior request changed the post-result checkpoint: before=%+v after=%+v", before.Counters, unchanged.Counters)
	}

	nextBinding := testHostCallBinding("host-call-after-successful-result", 73)
	receipt, err := ledger.ReserveHostCall(context.Background(), nextBinding)
	if err != nil {
		t.Fatalf("reserve the next exact provider request after terminal result: %v", err)
	}
	after, digest := ledger.Snapshot()
	if got, ok := receipt.HostCallBinding(); !ok || got != nextBinding || receipt.Counters() != after.Counters ||
		after.Counters.HostCalls != before.Counters.HostCalls+1 || after.Counters.Tokens != before.Counters.Tokens+73 ||
		after.Pins.HostCallReservation == nil || after.Pins.HostCallReservation.Binding != nextBinding ||
		after.Pins.HostCallReservation.Disposition != HostCallReserved || after.Pins.HostCallReservation.CapturedCall != nil ||
		after.Pins.PendingCallDisposition != PendingCallTerminal ||
		after.Pins.ProviderDisposition != DispositionAwaitingProviderExchange ||
		after.Pins.EffectReservation == nil || after.Pins.EffectReservation.Disposition != EffectReservationTerminal ||
		digest == beforeDigest {
		t.Fatalf("next provider request was not charged exactly once before dispatch: before=%+v after=%+v pins=%+v",
			before.Counters, after.Counters, after.Pins)
	}

	responsePins := clonePins(after.Pins)
	responsePins.PendingCallDisposition = PendingCallCaptured
	responsePins.ProviderDisposition = DispositionAwaitingGateDecision
	active = &responsePins.ProviderTranscripts[len(responsePins.ProviderTranscripts)-1]
	active.JournalCount++
	active.JournalHead = testDigest("second provider response journal head")
	active.ProjectionCount++
	active.ProjectionDigest = testDigest("second provider response projection")
	active.ProjectionHead = active.JournalHead
	active.Disposition = DispositionAwaitingGateDecision
	responsePins.ProviderProjectionCount = totalProjectionCount(responsePins.ProviderTranscripts)
	responsePins.ProviderProjectionDigest = TranscriptPinsDigest(responsePins.ProviderTranscripts)
	responsePins.ProviderProjectionHead = active.ProjectionHead
	responsePins.HostCallReservation.Disposition = HostCallTerminal
	responsePins.HostCallReservation.OutcomeDigest = testDigest("second provider exchange outcome")
	responsePins.HostCallReservation.CapturedCall = &CapturedCallPin{
		WorkflowID: workflow, ConversationID: active.ConversationID, HostCallID: nextBinding.CallID,
		ToolCallID: "provider-tool-call-second-success", OutcomeDigest: responsePins.HostCallReservation.OutcomeDigest,
		RecoveryGeneration: responsePins.RecoveryGeneration, OwnerEpoch: responsePins.OwnerEpoch,
		Disposition: CapturedCallAvailable,
	}
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, responsePins); err != nil {
		t.Fatalf("capture a distinct tool call from the exact next provider exchange: %v", err)
	}
	captured, _ := ledger.Snapshot()
	if captured.Pins.PendingCallDisposition != PendingCallCaptured ||
		captured.Pins.ProviderDisposition != DispositionAwaitingGateDecision ||
		captured.Pins.HostCallReservation == nil || captured.Pins.HostCallReservation.Binding != nextBinding ||
		captured.Pins.HostCallReservation.CapturedCall == nil ||
		captured.Pins.HostCallReservation.CapturedCall.ToolCallID != "provider-tool-call-second-success" {
		t.Fatalf("next exchange did not retain its exact distinct captured call: pins=%+v", captured.Pins)
	}
}

func TestNextProviderExchangeAdmissionRejectsNearbyPendingStates(t *testing.T) {
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := "protected-root-next-host-call-nearby-states"
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	defer owner.Close()
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow,
		"session-next-host-call-nearby", 1, 1)
	limits := testLimits()
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
		t.Fatalf("open fresh ledger: %v", err)
	}

	priorBinding, _ := prepareCapturedCallForEffect(t, ledger,
		"operation-nearby-state", "provider-tool-call-nearby-state")
	effectReceipt, err := ledger.ReserveEffect(context.Background(), priorBinding,
		exactCapturedOperationVerifier{expected: priorBinding})
	if err != nil {
		t.Fatalf("reserve the exact captured effect: %v", err)
	}
	reserved, _ := ledger.Snapshot()
	terminalPins := clonePins(reserved.Pins)
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("nearby-state terminal Store journal head")
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("nearby-state terminal Store outcome")
	if _, err := ledger.RecordTerminalEffectOutcome(context.Background(), effectReceipt,
		acceptBoundTerminalEffect{}, terminalPins); err != nil {
		t.Fatalf("settle exact Store effect: %v", err)
	}
	settled, _ := ledger.Snapshot()
	resultPins := clonePins(settled.Pins)
	resultPins.HostCallReservation.CapturedCall = nil
	resultPins.PendingCallDisposition = PendingCallTerminal
	resultPins.ProviderDisposition = DispositionAwaitingProviderExchange
	active := &resultPins.ProviderTranscripts[len(resultPins.ProviderTranscripts)-1]
	active.JournalCount++
	active.JournalHead = testDigest("nearby-state provider result journal head")
	active.ProjectionCount++
	active.ProjectionDigest = testDigest("nearby-state provider result projection")
	active.ProjectionHead = active.JournalHead
	active.Disposition = DispositionAwaitingProviderExchange
	resultPins.ProviderProjectionCount = totalProjectionCount(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionDigest = TranscriptPinsDigest(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionHead = active.ProjectionHead
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, resultPins); err != nil {
		t.Fatalf("checkpoint result: %v", err)
	}
	base, _ := ledger.Snapshot()

	cases := map[string]func(*Pins){
		"pending call is not terminal": func(p *Pins) { p.PendingCallDisposition = PendingCallInFlight },
		"missing terminal effect":      func(p *Pins) { p.EffectReservation = nil },
		"unknown effect": func(p *Pins) {
			p.EffectReservation.Disposition = EffectReservationUnknown
			p.EffectReservation.OutcomeDigest = ""
		},
		"retired effect": func(p *Pins) {
			p.EffectReservation.Disposition = EffectReservationRetired
			p.EffectReservation.OutcomeDigest = ""
			p.EffectReservation.RetirementDigest = testDigest("retired effect")
		},
		"unknown prior host call": func(p *Pins) {
			p.HostCallReservation.Disposition = HostCallUnknown
			p.HostCallReservation.OutcomeDigest = ""
		},
		"retired prior host call": func(p *Pins) {
			p.HostCallReservation.Disposition = HostCallRetired
			p.HostCallReservation.OutcomeDigest = ""
			p.HostCallReservation.RetirementDigest = testDigest("retired host call")
		},
		"effect belongs to another host call": func(p *Pins) {
			p.EffectReservation.Binding.HostCallID = "different-prior-call"
		},
		"effect belongs to another conversation": func(p *Pins) {
			p.EffectReservation.Binding.ConversationID = "different-conversation"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := clonePins(base.Pins)
			mutate(&candidate)
			checkpoint := base
			checkpoint.Pins = candidate
			checkpoint.ComputedCheckpointDigest = computeCheckpointDigest(checkpoint)
			if err := validateHostCallReservationAdmission(checkpoint, time.Now()); !errors.Is(err, ErrPendingUnknown) {
				t.Fatalf("nearby state admission = %v, want ErrPendingUnknown", err)
			}
		})
	}
}

func terminalEffectCandidateForTest(previous Checkpoint, previousDigest string, nextPins Pins) (Checkpoint, TransitionProof) {
	next := previous
	next.Pins = clonePins(nextPins)
	next.PreviousCheckpointDigest = previousDigest
	next.ObservationDigest = digestTerminalEffect(previousDigest, previous.Pins, next.Pins, previous.Counters)
	next.ComputedCheckpointDigest = computeCheckpointDigest(next)
	proof := TransitionProof{
		kind: transitionTerminalEffect, reservation: reservationEffect, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), observedPins: clonePins(next.Pins), targetPins: clonePins(next.Pins),
		previousCounts: previous.Counters, targetCounts: next.Counters, proofDigest: next.ObservationDigest,
	}
	return next, proof
}
