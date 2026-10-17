//go:build linux

package workflowrecovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	protectedRootSchemaVersion = 3
	protectedRootApplicationID = 0x54425752 // "TBWR"
)

// OwnerDrainVerifier is implemented by the trusted runner/cgroup boundary.
// It must prove that every listed owner incarnation and all of its governed
// cells are drained. A PID-only check or a provider assertion is insufficient.
// It returns one drain-proof digest per owner, in input order. The protected
// root persists each digest as that owner's drain evidence, so the digest must
// commit to the observed drain facts. A verifier that cannot produce a digest
// cannot satisfy this interface, so no owner can be fenced without evidence.
type OwnerDrainVerifier interface {
	VerifyOwnerCellsDrainedWithProof(ctx context.Context, workflow string, owners []OwnerEpochIdentity) ([]string, error)
}

// FreshWorkflowPolicy is supplied by the host control plane, not by a provider,
// CLI argument, or caller-selected callback. It authorizes one original
// workflow ID for first-ledger initialization. The supplied deadline is the
// original absolute UTC deadline from the admission catalog; implementations
// must authenticate that exact value and never recompute it from a new duration.
type FreshWorkflowPolicy interface {
	AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64,
		initialDigest string, incarnation OwnerIncarnation, deadline time.Time,
		rootIdentity string, store OwnerStoreIdentity) error
}

// HostBoundary independently validates the current owner process and protected
// owner-store path, verifies drains, and authorizes fresh workflow IDs. A
// production ProtectedTrustedRoot requires a non-nil implementation.
type HostBoundary interface {
	VerifyOwnerStore(ctx context.Context, workflow string, store OwnerStoreIdentity) error
	VerifyOwnerIncarnation(ctx context.Context, workflow string, incarnation OwnerIncarnation) error
	VerifyOwnersDrainedWithEvidence(ctx context.Context, workflow string, owners []OwnerEpochIdentity) ([]string, error)
	AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64,
		initialDigest string, incarnation OwnerIncarnation, deadline time.Time,
		rootIdentity string, store OwnerStoreIdentity) error
}

// LinuxHostBoundary enforces the host-owned owner-store namespace and actual
// Linux process incarnation. The runner's ownerDrainVerifier is mandatory for
// cgroup/cell teardown evidence; this layer does not infer that process exit
// alone drained descendants.
type LinuxHostBoundary struct {
	ownerStoreRoot      string
	ownerUID            uint32
	ownerDrainVerifier  OwnerDrainVerifier
	freshWorkflowPolicy FreshWorkflowPolicy
}

func NewLinuxHostBoundary(ownerStoreRoot string, ownerUID uint32,
	ownerDrainVerifier OwnerDrainVerifier, freshWorkflowPolicy FreshWorkflowPolicy) (*LinuxHostBoundary, error) {
	clean, err := cleanSQLitePath(ownerStoreRoot)
	if err != nil || clean != ownerStoreRoot || ownerDrainVerifier == nil || freshWorkflowPolicy == nil {
		return nil, fmt.Errorf("%w: Linux host boundary requires a clean owner-store root, drain verifier, and fresh-workflow policy", ErrOwnerFence)
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect host owner-store root: %v", ErrOwnerStore, err)
	}
	uid, uidErr := sqliteFileUID(info)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || uidErr != nil || uid != ownerUID ||
		info.Mode().Perm() != 0o700 || ownerUID != uint32(syscall.Geteuid()) {
		return nil, fmt.Errorf("%w: host owner-store root must be supervisor-owned mode 0700", ErrOwnerStore)
	}
	return &LinuxHostBoundary{ownerStoreRoot: clean, ownerUID: ownerUID,
		ownerDrainVerifier: ownerDrainVerifier, freshWorkflowPolicy: freshWorkflowPolicy}, nil
}

func (b *LinuxHostBoundary) VerifyOwnerStore(_ context.Context, workflow string, store OwnerStoreIdentity) error {
	if b == nil || !validID(workflow) || !validDigest(store.Digest) || store.Path == "" ||
		!filepath.IsAbs(store.Path) || filepath.Clean(store.Path) != store.Path {
		return fmt.Errorf("%w: invalid owner-store host binding", ErrOwnerFence)
	}
	if !pathIsWithin(b.ownerStoreRoot, filepath.Dir(store.Path)) {
		return fmt.Errorf("%w: recovery database is outside the host-provisioned owner-store root", ErrOwnerStore)
	}
	_, actualDigest, err := inspectSQLitePath(store.Path, false)
	if err != nil {
		return err
	}
	if actualDigest != store.Digest {
		return fmt.Errorf("%w: owner-store path/inode differs from the held host identity", ErrOwnerStore)
	}
	return nil
}

func (b *LinuxHostBoundary) VerifyOwnerIncarnation(_ context.Context, workflow string, incarnation OwnerIncarnation) error {
	if b == nil || !validID(workflow) || !validOwnerIncarnation(incarnation) || incarnation.EffectiveUID != b.ownerUID {
		return fmt.Errorf("%w: invalid host owner process identity", ErrOwnerFence)
	}
	observed, err := ownerIncarnationForPID(incarnation.PID)
	if err != nil || !reflect.DeepEqual(observed, incarnation) {
		return fmt.Errorf("%w: host-observed owner process differs from the acquired incarnation: %v", ErrOwnerFence, err)
	}
	return nil
}

// VerifyOwnersDrainedWithEvidence verifies that every superseded owner is drained
// and returns one drain-proof digest per owner, in input order. A missing or
// malformed digest is refused, so an owner epoch cannot be fenced on evidence
// that does not cover every owner.
func (b *LinuxHostBoundary) VerifyOwnersDrainedWithEvidence(ctx context.Context, workflow string,
	owners []OwnerEpochIdentity) ([]string, error) {
	if b == nil || ctx == nil || !validID(workflow) || len(owners) == 0 {
		return nil, fmt.Errorf("%w: no verifiable owner-incarnation drain set", ErrOwnerFence)
	}
	for _, owner := range owners {
		if owner.Epoch == 0 || !validOwnerIncarnation(owner.Incarnation) {
			return nil, fmt.Errorf("%w: malformed superseded owner identity", ErrOwnerFence)
		}
	}
	digests, err := b.ownerDrainVerifier.VerifyOwnerCellsDrainedWithProof(ctx, workflow,
		append([]OwnerEpochIdentity(nil), owners...))
	if err != nil {
		return nil, fmt.Errorf("%w: runner/cgroup owner drain: %v", ErrOwnerFence, err)
	}
	if len(digests) != len(owners) {
		return nil, fmt.Errorf("%w: runner/cgroup owner drain returned %d proof digests for %d owners",
			ErrOwnerFence, len(digests), len(owners))
	}
	for i, digest := range digests {
		if !validDigest(digest) {
			return nil, fmt.Errorf("%w: runner/cgroup owner drain proof digest %d is malformed", ErrOwnerFence, i)
		}
	}
	return append([]string(nil), digests...), nil
}

func (b *LinuxHostBoundary) AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64,
	initialDigest string, incarnation OwnerIncarnation, deadline time.Time,
	rootIdentity string, store OwnerStoreIdentity) error {
	if b == nil || ctx == nil || !validID(workflow) || ownerEpoch == 0 || !validDigest(initialDigest) ||
		!validDigest(rootIdentity) || deadline.IsZero() || deadline.Location() != time.UTC {
		return ErrFreshAuthorization
	}
	if err := b.VerifyOwnerStore(ctx, workflow, store); err != nil {
		return err
	}
	if err := b.VerifyOwnerIncarnation(ctx, workflow, incarnation); err != nil {
		return err
	}
	return b.freshWorkflowPolicy.AuthorizeFreshWorkflow(ctx, workflow, ownerEpoch, initialDigest,
		incarnation, deadline, rootIdentity, store)
}

// ProtectedTrustedRoot is the host-only workflow namespace, separate from the
// SQLiteOwner checkpoint database. It holds the authenticated checkpoint head,
// a distinct owner-fence high-water mark, every acquired owner incarnation, and
// single-use fresh authorization. Its root directory is protected by the host
// TCB and held under an exclusive OS lock for this object's lifetime. The root
// identity digest is an identifier, not an authenticator; authority comes from
// this protected namespace, the OS lock, and the mandatory HostBoundary.
type ProtectedTrustedRoot struct {
	mu             sync.Mutex
	db             *sql.DB
	lockFile       *os.File
	path           string
	ownerStoreRoot string
	rootIdentity   string
	boundary       HostBoundary
	verifiedStores map[string]OwnerStoreIdentity
	clockNow       func() (time.Time, string, int64, error)
	closed         bool
	poisoned       bool

	// clockPersistFault is a test seam invoked before each pre-commit step of
	// persistProtectedClockStateLocked. It is nil in production.
	clockPersistFault func(stage string) error
}

type protectedRootState struct {
	WorkflowID                  string
	OwnerStorePath              string
	OwnerStoreIdentityDigest    string
	WorkflowDeadlineUTC         string
	ClockBootID                 string
	ClockAnchorWallUTC          string
	ClockAnchorBootTimeNS       int64
	ClockObservedUTC            string
	ClockObservedBootTimeNS     int64
	ClockExpired                int64
	ClockUncertain              int64
	CheckpointEpoch             int64
	CheckpointDigest            string
	CheckpointIncarnationDigest string
	FenceEpoch                  int64
	FenceIncarnationDigest      string
	FreshAuthorizationDigest    string
	Revision                    int64
}

type protectedOwnerEpoch struct {
	WorkflowID          string
	Epoch               int64
	IncarnationJSON     []byte
	IncarnationDigest   string
	Drained             int64
	DrainEvidenceDigest string
}

type protectedRootQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CreateFreshProtectedRoot explicitly bootstraps the host-only root namespace.
// Its directory must be separate from ownerStoreRoot and inaccessible to worker
// cells. The first workflow still requires the host FreshWorkflowPolicy.
func CreateFreshProtectedRoot(path, ownerStoreRoot string, boundary HostBoundary) (*ProtectedTrustedRoot, error) {
	cleanPath, err := cleanSQLitePath(path)
	if err != nil {
		return nil, err
	}
	cleanStoreRoot, err := cleanSQLitePath(ownerStoreRoot)
	if err != nil {
		return nil, err
	}
	if boundary == nil || pathsOverlap(filepath.Dir(cleanPath), cleanStoreRoot) {
		return nil, fmt.Errorf("%w: protected root requires a host boundary and a namespace separate from owner stores", ErrOwnerFence)
	}
	if err := validateSQLiteParent(cleanPath); err != nil {
		return nil, err
	}
	if err := validateProtectedOwnerStoreRoot(cleanStoreRoot); err != nil {
		return nil, err
	}
	if err := createSQLiteFile(cleanPath); err != nil {
		return nil, err
	}
	lockFile, err := acquireProtectedRootLock(cleanPath, true)
	if err != nil {
		return nil, err
	}
	rootIdentity, err := protectedRootIdentity(cleanPath, cleanStoreRoot)
	if err != nil {
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	db, err := openOwnerDatabase(cleanPath, sqliteProtectedRootMaxPageCount)
	if err != nil {
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	root := &ProtectedTrustedRoot{db: db, lockFile: lockFile, path: cleanPath,
		ownerStoreRoot: cleanStoreRoot, rootIdentity: rootIdentity, boundary: boundary,
		verifiedStores: make(map[string]OwnerStoreIdentity), clockNow: readProtectedHostClock}
	if err := root.createNamespace(context.Background()); err != nil {
		_ = db.Close()
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	return root, nil
}

// OpenExistingProtectedRoot opens only the host-provisioned root namespace.
// It never creates a namespace, fresh authorization, or replacement root.
func OpenExistingProtectedRoot(path, ownerStoreRoot string, boundary HostBoundary) (*ProtectedTrustedRoot, error) {
	cleanPath, err := cleanSQLitePath(path)
	if err != nil {
		return nil, err
	}
	cleanStoreRoot, err := cleanSQLitePath(ownerStoreRoot)
	if err != nil {
		return nil, err
	}
	if boundary == nil || pathsOverlap(filepath.Dir(cleanPath), cleanStoreRoot) {
		return nil, fmt.Errorf("%w: protected root requires a host boundary and a namespace separate from owner stores", ErrOwnerFence)
	}
	if err := validateSQLiteParent(cleanPath); err != nil {
		return nil, err
	}
	if err := validateProtectedOwnerStoreRoot(cleanStoreRoot); err != nil {
		return nil, err
	}
	_, _, err = inspectSQLitePath(cleanPath, false)
	if err != nil {
		return nil, err
	}
	lockFile, err := acquireProtectedRootLock(cleanPath, false)
	if err != nil {
		return nil, err
	}
	rootIdentity, err := protectedRootIdentity(cleanPath, cleanStoreRoot)
	if err != nil {
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	if err := precheckSQLiteFileGeometry(cleanPath, sqliteProtectedRootMaxPageCount); err != nil {
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	db, err := openOwnerDatabase(cleanPath, sqliteProtectedRootMaxPageCount)
	if err != nil {
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	root := &ProtectedTrustedRoot{db: db, lockFile: lockFile, path: cleanPath,
		ownerStoreRoot: cleanStoreRoot, rootIdentity: rootIdentity, boundary: boundary,
		verifiedStores: make(map[string]OwnerStoreIdentity), clockNow: readProtectedHostClock}
	if err := root.verifyNamespace(context.Background()); err != nil {
		_ = db.Close()
		_ = releaseProtectedRootLock(lockFile)
		return nil, err
	}
	return root, nil
}

func (r *ProtectedTrustedRoot) TrustedRootIdentityDigest() string {
	if r == nil {
		return ""
	}
	return r.rootIdentity
}

// VerifyWorkflowDeadline checks the stored immutable original deadline against
// the protected root's UTC and CLOCK_BOOTTIME anchor. A clock rollback or boot
// identity change is persisted as a sticky pause rather than renewed time.
func (r *ProtectedTrustedRoot) VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error {
	if ctx == nil || !validID(workflow) || deadline.IsZero() || r == nil {
		return ErrClockUncertain
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if !exists || state.WorkflowDeadlineUTC == "" {
		return fmt.Errorf("%w: original deadline has no protected clock anchor", ErrClockUncertain)
	}
	if !sameUTCInstant(state.WorkflowDeadlineUTC, deadline) {
		return fmt.Errorf("%w: caller deadline differs from the immutable host-root deadline", ErrRollback)
	}
	return r.verifyWorkflowClockLocked(workflow, state)
}

func (r *ProtectedTrustedRoot) VerifyHostBoundary(ctx context.Context, workflow string,
	store OwnerStoreIdentity, incarnation OwnerIncarnation) error {
	if r == nil || ctx == nil || !validID(workflow) {
		return ErrOwnerFence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	if err := r.boundary.VerifyOwnerStore(ctx, workflow, store); err != nil {
		return fmt.Errorf("%w: protected owner-store path: %v", ErrOwnerFence, err)
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if exists && (state.OwnerStorePath != store.Path || state.OwnerStoreIdentityDigest != store.Digest) {
		return fmt.Errorf("%w: supplied owner database differs from the identity retained by the protected host root", ErrOwnerStore)
	}
	if exists && state.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, state); err != nil {
			return err
		}
	}
	if prior, ok := r.verifiedStores[workflow]; ok && prior != store {
		return fmt.Errorf("%w: owner database identity changed during protected-root ownership", ErrOwnerStore)
	}
	if err := r.boundary.VerifyOwnerIncarnation(ctx, workflow, incarnation); err != nil {
		return fmt.Errorf("%w: actual owner process incarnation: %v", ErrOwnerFence, err)
	}
	r.verifiedStores[workflow] = store
	return nil
}

func (r *ProtectedTrustedRoot) VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || r == nil {
		return ErrOwnerFence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if !exists || uint64(state.FenceEpoch) != ownerEpoch {
		return ErrOwnerFence
	}
	if state.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, state); err != nil {
			return err
		}
	}
	return nil
}

// VerifyTerminalEffectOwner checks the live acquired process, protected owner
// store, exact fenced checkpoint head, and immutable original deadline. A
// sticky expiry or clock-uncertainty pause is tolerated only for this
// terminal-accounting capability.
func (r *ProtectedTrustedRoot) VerifyTerminalEffectOwner(ctx context.Context, workflow string,
	store OwnerStoreIdentity, incarnation OwnerIncarnation, ownerEpoch uint64,
	checkpointDigest string, deadline time.Time) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || !validDigest(checkpointDigest) ||
		!validOwnerIncarnation(incarnation) || deadline.IsZero() || r == nil {
		return ErrOwnerFence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.verifyTerminalEffectOwnerLocked(ctx, workflow, store, incarnation,
		ownerEpoch, checkpointDigest, deadline)
	return err
}

func (r *ProtectedTrustedRoot) verifyTerminalEffectOwnerLocked(ctx context.Context, workflow string,
	store OwnerStoreIdentity, incarnation OwnerIncarnation, ownerEpoch uint64,
	checkpointDigest string, deadline time.Time) (protectedRootState, error) {
	if err := r.checkLocked(); err != nil {
		return protectedRootState{}, err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return protectedRootState{}, err
	}
	if err := r.boundary.VerifyOwnerIncarnation(ctx, workflow, incarnation); err != nil {
		return protectedRootState{}, fmt.Errorf("%w: terminal accounting owner incarnation: %v", ErrOwnerFence, err)
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return protectedRootState{}, err
	}
	if !exists || r.verifyBoundStoreLocked(ctx, workflow, state) != nil ||
		state.OwnerStorePath != store.Path || state.OwnerStoreIdentityDigest != store.Digest ||
		uint64(state.FenceEpoch) != ownerEpoch || state.FenceIncarnationDigest != incarnation.Digest ||
		uint64(state.CheckpointEpoch) != ownerEpoch || state.CheckpointDigest != checkpointDigest ||
		state.CheckpointIncarnationDigest != incarnation.Digest {
		return protectedRootState{}, ErrOwnerFence
	}
	if state.WorkflowDeadlineUTC == "" {
		return protectedRootState{}, ErrClockUncertain
	}
	if !sameUTCInstant(state.WorkflowDeadlineUTC, deadline) {
		return protectedRootState{}, fmt.Errorf("%w: terminal accounting deadline differs from original root deadline", ErrRollback)
	}
	clockErr := r.verifyWorkflowClockLocked(workflow, state)
	if clockErr != nil && !errors.Is(clockErr, ErrDeadline) && !errors.Is(clockErr, ErrClockUncertain) {
		return protectedRootState{}, clockErr
	}
	state, exists, err = loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return protectedRootState{}, err
	}
	if !exists || r.verifyBoundStoreLocked(ctx, workflow, state) != nil ||
		state.OwnerStorePath != store.Path || state.OwnerStoreIdentityDigest != store.Digest ||
		uint64(state.FenceEpoch) != ownerEpoch || state.FenceIncarnationDigest != incarnation.Digest ||
		uint64(state.CheckpointEpoch) != ownerEpoch || state.CheckpointDigest != checkpointDigest ||
		state.CheckpointIncarnationDigest != incarnation.Digest || !sameUTCInstant(state.WorkflowDeadlineUTC, deadline) {
		return protectedRootState{}, ErrOwnerFence
	}
	if errors.Is(clockErr, ErrDeadline) && state.ClockExpired != 1 ||
		errors.Is(clockErr, ErrClockUncertain) && state.ClockUncertain != 1 {
		return protectedRootState{}, fmt.Errorf("%w: terminal accounting clock pause was not durably retained", ErrRollback)
	}
	return state, nil
}

// NextOwnerEpoch returns an advisory candidate from the durable protected-root
// fence. Acquisition still has to succeed under this root's exclusive OS lock,
// which binds the candidate to the host-verified incarnation and records the
// drain of every superseded owner.
func (r *ProtectedTrustedRoot) NextOwnerEpoch(ctx context.Context, workflow string) (uint64, error) {
	if ctx == nil || !validID(workflow) || r == nil {
		return 0, ErrOwnerFence
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return 0, err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return 0, err
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 1, nil
	}
	if state.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, state); err != nil {
			return 0, err
		}
	}
	if state.FenceEpoch <= 0 || state.FenceEpoch >= int64(maxSQLiteInteger) {
		return 0, fmt.Errorf("%w: protected owner epoch is exhausted", ErrOwnerFence)
	}
	return uint64(state.FenceEpoch) + 1, nil
}

func (r *ProtectedTrustedRoot) AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64,
	incarnation OwnerIncarnation) error {
	if ctx == nil || !validID(workflow) || nextEpoch == 0 || nextEpoch <= previousEpoch || !validOwnerIncarnation(incarnation) || r == nil {
		return fmt.Errorf("%w: invalid protected-root epoch acquisition", ErrOwnerFence)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	if err := r.boundary.VerifyOwnerIncarnation(ctx, workflow, incarnation); err != nil {
		return fmt.Errorf("%w: owner-incarnation acquisition: %v", ErrOwnerFence, err)
	}
	store, ok := r.verifiedStores[workflow]
	if !ok {
		return fmt.Errorf("%w: owner database identity was not verified by this protected root", ErrOwnerStore)
	}
	if err := r.boundary.VerifyOwnerStore(ctx, workflow, store); err != nil {
		return fmt.Errorf("%w: revalidate owner database identity: %v", ErrOwnerStore, err)
	}
	existingState, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if exists && existingState.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, existingState); err != nil {
			return err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin protected-root epoch acquisition: %v", ErrOwnerStore, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	state, exists, err := loadProtectedRootState(ctx, tx, workflow)
	if err != nil {
		return err
	}
	if !exists {
		if previousEpoch != 0 {
			return ErrMissing
		}
		if nextEpoch != 1 {
			return fmt.Errorf("%w: first owner epoch must begin at one", ErrOwnerFence)
		}
		state = protectedRootState{WorkflowID: workflow, OwnerStorePath: store.Path,
			OwnerStoreIdentityDigest: store.Digest, FenceEpoch: int64(nextEpoch),
			FenceIncarnationDigest: incarnation.Digest, Revision: 1}
		if err := insertProtectedRootState(ctx, tx, state); err != nil {
			return err
		}
		if err := insertOwnerEpoch(ctx, tx, workflow, nextEpoch, incarnation, false, ""); err != nil {
			return err
		}
	} else {
		if state.OwnerStorePath != store.Path || state.OwnerStoreIdentityDigest != store.Digest {
			return fmt.Errorf("%w: owner database identity differs from the protected-root binding", ErrOwnerStore)
		}
		if state.CheckpointEpoch < 0 || uint64(state.CheckpointEpoch) != previousEpoch {
			return fmt.Errorf("%w: protected-root accepted checkpoint epoch changed", ErrCAS)
		}
		if uint64(state.FenceEpoch) == nextEpoch && state.FenceIncarnationDigest == incarnation.Digest {
			return nil
		}
		if state.FreshAuthorizationDigest != "" && state.CheckpointEpoch == 0 {
			return fmt.Errorf("%w: fresh authorization was already consumed for this original workflow ID", ErrFreshAuthorization)
		}
		if state.FenceEpoch < 0 || nextEpoch != uint64(state.FenceEpoch)+1 {
			return fmt.Errorf("%w: new owner epoch must advance the host fence high-water mark", ErrOwnerFence)
		}
		firstDrainedEpoch := uint64(state.CheckpointEpoch)
		if firstDrainedEpoch == 0 {
			firstDrainedEpoch = 1
		}
		oldOwners, err := loadOwnerEpochs(ctx, tx, workflow, firstDrainedEpoch, uint64(state.FenceEpoch))
		if err != nil {
			return err
		}
		if uint64(len(oldOwners)) != uint64(state.FenceEpoch)-firstDrainedEpoch+1 {
			return fmt.Errorf("%w: protected root owner-epoch history has a gap", ErrRollback)
		}
		if len(oldOwners) > 0 {
			drainDigests, err := r.boundary.VerifyOwnersDrainedWithEvidence(ctx, workflow, ownerEpochIdentities(oldOwners))
			if err != nil {
				return fmt.Errorf("%w: one or more superseded owner/cells were not drained: %v", ErrOwnerFence, err)
			}
			if len(drainDigests) != len(oldOwners) {
				return fmt.Errorf("%w: drain evidence does not cover every superseded owner", ErrOwnerFence)
			}
			for i, old := range oldOwners {
				if err := markOwnerEpochDrained(ctx, tx, r.rootIdentity, old, drainDigests[i]); err != nil {
					return err
				}
			}
		}
		if err := insertOwnerEpoch(ctx, tx, workflow, nextEpoch, incarnation, false, ""); err != nil {
			return err
		}
		state.FenceEpoch = int64(nextEpoch)
		state.FenceIncarnationDigest = incarnation.Digest
		state.Revision++
		if err := updateProtectedRootState(ctx, tx, state); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: commit protected-root owner epoch: %v", ErrOwnerStore, err)
	}
	committed = true
	if err := syncSQLiteDirectory(r.path); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: sync protected-root directory after epoch change: %v", ErrOwnerStore, err)
	}
	return nil
}

func (r *ProtectedTrustedRoot) VerifyOwnersDrained(ctx context.Context, workflow string, afterEpoch, beforeEpoch uint64) error {
	if ctx == nil || !validID(workflow) || afterEpoch == 0 || beforeEpoch <= afterEpoch || r == nil {
		return fmt.Errorf("%w: invalid protected-root drain query", ErrOwnerFence)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if !exists || uint64(state.FenceEpoch) < beforeEpoch {
		return ErrOwnerFence
	}
	if state.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, state); err != nil {
			return err
		}
	}
	owners, err := loadOwnerEpochs(ctx, r.db, workflow, afterEpoch, beforeEpoch-1)
	if err != nil {
		return err
	}
	if uint64(len(owners)) != beforeEpoch-afterEpoch {
		return fmt.Errorf("%w: drain proof does not cover every acquired epoch in the superseded range", ErrOwnerFence)
	}
	for _, owner := range owners {
		if owner.Drained != 1 || !validDigest(owner.DrainEvidenceDigest) {
			return fmt.Errorf("%w: owner epoch %d has no host drain evidence", ErrOwnerFence, owner.Epoch)
		}
	}
	return nil
}

func (r *ProtectedTrustedRoot) VerifyCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64,
	digest, incarnationDigest string) error {
	if ctx == nil || !validID(workflow) || r == nil ||
		(ownerEpoch == 0 && (digest != "" || incarnationDigest != "")) ||
		(ownerEpoch > 0 && (!validDigest(digest) || !validDigest(incarnationDigest))) {
		return fmt.Errorf("%w: invalid checkpoint-head query", ErrRollback)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if ownerEpoch == 0 && digest == "" && incarnationDigest == "" {
		if exists && state.CheckpointEpoch != 0 {
			return fmt.Errorf("%w: trusted root already has an accepted checkpoint", ErrRollback)
		}
		return nil
	}
	if !exists || uint64(state.CheckpointEpoch) != ownerEpoch || state.CheckpointDigest != digest ||
		state.CheckpointIncarnationDigest != incarnationDigest {
		return fmt.Errorf("%w: local checkpoint head differs from protected host root", ErrRollback)
	}
	if state.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, state); err != nil {
			return err
		}
	}
	return nil
}

// verifySettledCheckpoint authenticates a previously settled checkpoint head
// without evaluating the workflow clock or checking a live owner incarnation.
// It is intentionally private and read-only: callers must already have a
// durable terminal marker and independent runner-drain proof, and this method
// grants no workflow continuation authority.
func (r *ProtectedTrustedRoot) verifySettledCheckpoint(ctx context.Context, workflow string,
	store OwnerStoreIdentity, checkpoint Checkpoint, digest string) (bool, bool, []OwnerEpochIdentity, error) {
	if ctx == nil || ctx.Err() != nil || !validID(workflow) || r == nil ||
		store.Path == "" || !filepath.IsAbs(store.Path) || filepath.Clean(store.Path) != store.Path ||
		!validDigest(store.Digest) || !validDigest(digest) || checkpoint.Pins.WorkflowID != workflow ||
		checkpoint.Pins.OwnerEpoch == 0 || !validDigest(checkpoint.Pins.OwnerIncarnationDigest) ||
		checkpoint.Pins.RootIdentityDigest != r.rootIdentity ||
		checkpoint.ComputedCheckpointDigest != digest {
		return false, false, nil, fmt.Errorf("%w: invalid settled checkpoint/root/store identity binding", ErrOwnerFence)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return false, false, nil, err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return false, false, nil, err
	}
	if err := r.boundary.VerifyOwnerStore(ctx, workflow, store); err != nil {
		return false, false, nil, fmt.Errorf("%w: settled owner-store identity: %v", ErrOwnerStore, err)
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return false, false, nil, err
	}
	if !exists || validateProtectedClockState(state) != nil || state.WorkflowID != workflow ||
		state.OwnerStorePath != store.Path || state.OwnerStoreIdentityDigest != store.Digest ||
		uint64(state.CheckpointEpoch) != checkpoint.Pins.OwnerEpoch || state.CheckpointDigest != digest ||
		state.CheckpointIncarnationDigest != checkpoint.Pins.OwnerIncarnationDigest ||
		uint64(state.FenceEpoch) != checkpoint.Pins.OwnerEpoch ||
		state.FenceIncarnationDigest != checkpoint.Pins.OwnerIncarnationDigest ||
		!sameUTCInstant(state.WorkflowDeadlineUTC, checkpoint.Limits.Deadline) {
		return false, false, nil, fmt.Errorf("%w: settled owner checkpoint differs from the protected root head", ErrRollback)
	}
	owners, err := loadOwnerEpochs(ctx, r.db, workflow, 1, maxSQLiteInteger)
	if err != nil {
		return false, false, nil, err
	}
	if uint64(len(owners)) != checkpoint.Pins.OwnerEpoch {
		return false, false, nil, fmt.Errorf("%w: protected owner epoch history differs from settled checkpoint", ErrRollback)
	}
	for index, owner := range owners {
		if owner.Epoch != int64(index+1) {
			return false, false, nil, fmt.Errorf("%w: protected owner epoch history is not contiguous", ErrRollback)
		}
	}
	identities := ownerEpochIdentities(owners)
	if len(identities) == 0 || identities[len(identities)-1].Incarnation.Digest != checkpoint.Pins.OwnerIncarnationDigest {
		return false, false, nil, fmt.Errorf("%w: settled checkpoint incarnation differs from protected owner epoch history", ErrRollback)
	}
	return state.ClockExpired != 0, state.ClockUncertain != 0, identities, nil
}

func (r *ProtectedTrustedRoot) AuthorizeFreshWorkflow(ctx context.Context, workflow string, ownerEpoch uint64,
	initialDigest string, incarnation OwnerIncarnation, deadline time.Time) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || !validDigest(initialDigest) ||
		!validOwnerIncarnation(incarnation) || deadline.IsZero() || deadline.Location() != time.UTC || r == nil {
		return ErrFreshAuthorization
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	store, ok := r.verifiedStores[workflow]
	if !ok {
		return fmt.Errorf("%w: fresh authorization lacks a host-verified owner database identity", ErrOwnerStore)
	}
	state, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if !exists || r.verifyBoundStoreLocked(ctx, workflow, state) != nil ||
		uint64(state.FenceEpoch) != ownerEpoch || state.FenceIncarnationDigest != incarnation.Digest ||
		state.CheckpointEpoch != 0 || state.CheckpointDigest != "" || state.FreshAuthorizationDigest != "" ||
		state.WorkflowDeadlineUTC != "" || state.ClockBootID != "" {
		return fmt.Errorf("%w: fresh authorization is not single-use for the root-bound original workflow", ErrFreshAuthorization)
	}
	if err := r.boundary.AuthorizeFreshWorkflow(ctx, workflow, ownerEpoch, initialDigest,
		incarnation, deadline, r.rootIdentity, store); err != nil {
		return fmt.Errorf("%w: host catalog rejected or failed to bind new original workflow ID: %v", ErrFreshAuthorization, err)
	}
	anchorWall, bootID, bootTimeNS, err := r.clockNow()
	if err != nil || !validBootID(bootID) || bootTimeNS < 0 {
		return fmt.Errorf("%w: read host clock/boot anchor: %v", ErrClockUncertain, err)
	}
	anchorWall = anchorWall.Round(0).UTC()
	if !anchorWall.Before(deadline) {
		return ErrDeadline
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin single-use fresh authorization: %v", ErrOwnerStore, err)
	}
	defer tx.Rollback()
	state, exists, err = loadProtectedRootState(ctx, tx, workflow)
	if err != nil {
		return err
	}
	if !exists || r.verifyBoundStoreLocked(ctx, workflow, state) != nil {
		return fmt.Errorf("%w: fresh authorization does not name this root-bound owner database", ErrOwnerStore)
	}
	if uint64(state.FenceEpoch) != ownerEpoch || state.FenceIncarnationDigest != incarnation.Digest ||
		state.CheckpointEpoch != 0 || state.CheckpointDigest != "" || state.FreshAuthorizationDigest != "" ||
		state.WorkflowDeadlineUTC != "" || state.ClockBootID != "" {
		return fmt.Errorf("%w: fresh authorization is not single-use for a new original workflow ID", ErrFreshAuthorization)
	}
	state.FreshAuthorizationDigest = initialDigest
	state.WorkflowDeadlineUTC = deadline.Format(time.RFC3339Nano)
	state.ClockBootID = bootID
	state.ClockAnchorWallUTC = anchorWall.Format(time.RFC3339Nano)
	state.ClockAnchorBootTimeNS = bootTimeNS
	state.ClockObservedUTC = anchorWall.Format(time.RFC3339Nano)
	state.ClockObservedBootTimeNS = bootTimeNS
	state.Revision++
	if err := updateProtectedRootState(ctx, tx, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: commit single-use fresh authorization: %v", ErrOwnerStore, err)
	}
	if err := syncSQLiteDirectory(r.path); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: sync protected root after fresh authorization: %v", ErrOwnerStore, err)
	}
	return nil
}

func (r *ProtectedTrustedRoot) CompareAndSwapCheckpoint(ctx context.Context, workflow string, expectedOwnerEpoch uint64,
	expectedDigest string, nextOwnerEpoch uint64, nextDigest string, incarnation OwnerIncarnation) error {
	if ctx == nil || !validID(workflow) || nextOwnerEpoch == 0 || !validDigest(nextDigest) ||
		!validOwnerIncarnation(incarnation) || r == nil ||
		(expectedOwnerEpoch == 0 && expectedDigest != "") ||
		(expectedOwnerEpoch > 0 && !validDigest(expectedDigest)) {
		return fmt.Errorf("%w: invalid protected-root checkpoint CAS inputs", ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	if err := r.boundary.VerifyOwnerIncarnation(ctx, workflow, incarnation); err != nil {
		return fmt.Errorf("%w: root CAS owner incarnation: %v", ErrOwnerFence, err)
	}
	currentState, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil {
		return err
	}
	if !exists {
		return ErrMissing
	}
	if currentState.WorkflowDeadlineUTC != "" {
		if err := r.verifyWorkflowClockLocked(workflow, currentState); err != nil {
			return err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin protected-root checkpoint CAS: %v", ErrOwnerStore, err)
	}
	defer tx.Rollback()
	state, exists, err := loadProtectedRootState(ctx, tx, workflow)
	if err != nil {
		return err
	}
	if !exists || r.verifyBoundStoreLocked(ctx, workflow, state) != nil ||
		uint64(state.FenceEpoch) != nextOwnerEpoch || state.FenceIncarnationDigest != incarnation.Digest {
		return ErrOwnerFence
	}
	if expectedOwnerEpoch == 0 {
		if state.CheckpointEpoch != 0 || state.CheckpointDigest != "" || state.FreshAuthorizationDigest != nextDigest {
			return fmt.Errorf("%w: fresh root authorization is absent, stale, or already consumed", ErrFreshAuthorization)
		}
	} else if uint64(state.CheckpointEpoch) != expectedOwnerEpoch || state.CheckpointDigest != expectedDigest {
		return ErrCAS
	} else if nextOwnerEpoch > expectedOwnerEpoch {
		owners, err := loadOwnerEpochs(ctx, tx, workflow, expectedOwnerEpoch, nextOwnerEpoch-1)
		if err != nil {
			return err
		}
		if uint64(len(owners)) != nextOwnerEpoch-expectedOwnerEpoch {
			return fmt.Errorf("%w: root CAS skipped an unaccounted owner epoch", ErrOwnerFence)
		}
		for _, owner := range owners {
			if owner.Drained != 1 || !validDigest(owner.DrainEvidenceDigest) {
				return fmt.Errorf("%w: owner epoch %d has not been drained", ErrOwnerFence, owner.Epoch)
			}
		}
	}
	state.CheckpointEpoch = int64(nextOwnerEpoch)
	state.CheckpointDigest = nextDigest
	state.CheckpointIncarnationDigest = incarnation.Digest
	state.FreshAuthorizationDigest = ""
	state.Revision++
	if err := updateProtectedRootState(ctx, tx, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: commit protected-root checkpoint CAS: %v", ErrOwnerStore, err)
	}
	if err := syncSQLiteDirectory(r.path); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: sync protected-root directory after checkpoint CAS: %v", ErrOwnerStore, err)
	}
	return nil
}

// CompareAndSwapTerminalCheckpoint advances only the authenticated checkpoint
// head for an exact already-consumed effect reservation. The owner epoch,
// immutable limits, counters, and sticky clock pause are preserved.
func (r *ProtectedTrustedRoot) CompareAndSwapTerminalCheckpoint(ctx context.Context, workflow string,
	ownerEpoch uint64, expectedDigest string, previous, next Checkpoint, proof TransitionProof,
	store OwnerStoreIdentity, incarnation OwnerIncarnation) error {
	if ctx == nil || !validID(workflow) || ownerEpoch == 0 || !validDigest(expectedDigest) ||
		!validOwnerIncarnation(incarnation) || r == nil || previous.Pins.WorkflowID != workflow ||
		next.Pins.WorkflowID != workflow || previous.Pins.OwnerEpoch != ownerEpoch || next.Pins.OwnerEpoch != ownerEpoch ||
		previous.Pins.OwnerIncarnationDigest != incarnation.Digest || previous.Pins.RootIdentityDigest != r.rootIdentity ||
		previous.ComputedCheckpointDigest != expectedDigest || computeCheckpointDigest(previous) != expectedDigest ||
		!validDigest(next.ComputedCheckpointDigest) || computeCheckpointDigest(next) != next.ComputedCheckpointDigest {
		return fmt.Errorf("%w: invalid terminal checkpoint CAS inputs", ErrInvalid)
	}
	if err := validateOwnerTransition(previous, expectedDigest, true, next, expectedDigest, proof); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkLocked(); err != nil {
		return err
	}
	state, err := r.verifyTerminalEffectOwnerLocked(ctx, workflow, store, incarnation,
		ownerEpoch, expectedDigest, previous.Limits.Deadline)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin protected terminal checkpoint CAS: %v", ErrOwnerStore, err)
	}
	defer tx.Rollback()
	current, exists, err := loadProtectedRootState(ctx, tx, workflow)
	if err != nil {
		return err
	}
	if !exists || current.Revision != state.Revision ||
		r.verifyBoundStoreLocked(ctx, workflow, current) != nil ||
		current.OwnerStorePath != store.Path || current.OwnerStoreIdentityDigest != store.Digest ||
		uint64(current.FenceEpoch) != ownerEpoch || current.FenceIncarnationDigest != incarnation.Digest ||
		uint64(current.CheckpointEpoch) != ownerEpoch || current.CheckpointDigest != expectedDigest ||
		current.CheckpointIncarnationDigest != incarnation.Digest ||
		current.WorkflowDeadlineUTC != state.WorkflowDeadlineUTC ||
		current.ClockBootID != state.ClockBootID || current.ClockAnchorWallUTC != state.ClockAnchorWallUTC ||
		current.ClockAnchorBootTimeNS != state.ClockAnchorBootTimeNS ||
		current.ClockExpired != state.ClockExpired || current.ClockUncertain != state.ClockUncertain {
		return ErrCAS
	}
	if current.Revision >= int64(maxSQLiteInteger) {
		return fmt.Errorf("%w: protected root revision exhausted", ErrOwnerStore)
	}
	current.CheckpointEpoch = int64(ownerEpoch)
	current.CheckpointDigest = next.ComputedCheckpointDigest
	current.CheckpointIncarnationDigest = incarnation.Digest
	current.Revision++
	if err := updateProtectedRootState(ctx, tx, current); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: commit protected terminal checkpoint CAS: %v", ErrOwnerStore, err)
	}
	if err := syncSQLiteDirectory(r.path); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: sync protected root after terminal checkpoint CAS: %v", ErrOwnerStore, err)
	}
	readback, exists, err := loadProtectedRootState(ctx, r.db, workflow)
	if err != nil || !exists || uint64(readback.CheckpointEpoch) != ownerEpoch ||
		readback.CheckpointDigest != next.ComputedCheckpointDigest ||
		readback.CheckpointIncarnationDigest != incarnation.Digest ||
		uint64(readback.FenceEpoch) != ownerEpoch || readback.FenceIncarnationDigest != incarnation.Digest ||
		readback.WorkflowDeadlineUTC != state.WorkflowDeadlineUTC || readback.ClockBootID != state.ClockBootID ||
		readback.ClockAnchorWallUTC != state.ClockAnchorWallUTC || readback.ClockAnchorBootTimeNS != state.ClockAnchorBootTimeNS ||
		readback.ClockExpired != state.ClockExpired || readback.ClockUncertain != state.ClockUncertain {
		r.poisoned = true
		return fmt.Errorf("%w: protected root did not confirm terminal checkpoint head: %v", ErrCAS, err)
	}
	return nil
}

func (r *ProtectedTrustedRoot) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	if r.db != nil {
		errs = append(errs, r.db.Close())
	}
	if r.lockFile != nil {
		errs = append(errs, releaseProtectedRootLock(r.lockFile))
	}
	return errors.Join(errs...)
}

func (r *ProtectedTrustedRoot) checkLocked() error {
	if r == nil || r.db == nil || r.boundary == nil {
		return ErrOwnerFence
	}
	if r.closed {
		return fmt.Errorf("%w: protected root is closed", ErrOwnerStore)
	}
	if r.poisoned {
		return fmt.Errorf("%w: protected root is poisoned after an ambiguous commit", ErrCAS)
	}
	return nil
}

func readProtectedHostClock() (time.Time, string, int64, error) {
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return time.Time{}, "", 0, fmt.Errorf("read kernel boot ID: %w", err)
	}
	bootID := strings.ToLower(strings.TrimSpace(string(bootIDBytes)))
	if !validBootID(bootID) {
		return time.Time{}, "", 0, fmt.Errorf("kernel boot ID has invalid format")
	}
	var bootTime unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &bootTime); err != nil {
		return time.Time{}, "", 0, fmt.Errorf("read CLOCK_BOOTTIME: %w", err)
	}
	if bootTime.Sec < 0 || bootTime.Nsec < 0 || bootTime.Nsec >= 1_000_000_000 ||
		bootTime.Sec > (int64(1<<63-1)-bootTime.Nsec)/1_000_000_000 {
		return time.Time{}, "", 0, fmt.Errorf("CLOCK_BOOTTIME is outside the supported range")
	}
	return time.Now().Round(0).UTC(), bootID,
		bootTime.Sec*1_000_000_000 + bootTime.Nsec, nil
}

func validBootID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	if len(compact) != 32 {
		return false
	}
	for _, char := range compact {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func sameUTCInstant(encoded string, value time.Time) bool {
	parsed, err := parseProtectedUTC(encoded)
	if err != nil || value.IsZero() {
		return false
	}
	return parsed.Equal(value.Round(0).UTC())
}

func parseProtectedUTC(encoded string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, encoded)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != encoded {
		return time.Time{}, fmt.Errorf("noncanonical protected UTC timestamp")
	}
	return parsed, nil
}

func validateProtectedClockState(state protectedRootState) error {
	if state.ClockExpired < 0 || state.ClockExpired > 1 || state.ClockUncertain < 0 || state.ClockUncertain > 1 {
		return fmt.Errorf("invalid sticky clock status")
	}
	if state.WorkflowDeadlineUTC == "" {
		if state.ClockBootID != "" || state.ClockAnchorWallUTC != "" || state.ClockAnchorBootTimeNS != 0 ||
			state.ClockObservedUTC != "" || state.ClockObservedBootTimeNS != 0 || state.ClockExpired != 0 || state.ClockUncertain != 0 {
			return fmt.Errorf("partial protected clock anchor")
		}
		return nil
	}
	deadline, deadlineErr := parseProtectedUTC(state.WorkflowDeadlineUTC)
	anchor, anchorErr := parseProtectedUTC(state.ClockAnchorWallUTC)
	observed, observedErr := parseProtectedUTC(state.ClockObservedUTC)
	if deadlineErr != nil || anchorErr != nil || observedErr != nil || !validBootID(state.ClockBootID) ||
		state.ClockAnchorBootTimeNS < 0 || state.ClockObservedBootTimeNS < state.ClockAnchorBootTimeNS ||
		observed.Before(anchor) || !anchor.Before(deadline) {
		return fmt.Errorf("invalid protected deadline clock anchor")
	}
	return nil
}

// verifyWorkflowClockLocked advances and persists the protected wall/boot-time
// high-water before granting any owner operation. The owner OS lock is held by
// this root for its lifetime; the state revision check also fails closed if a
// second writer or replaced database appears.
func (r *ProtectedTrustedRoot) verifyWorkflowClockLocked(workflow string, state protectedRootState) error {
	if state.ClockUncertain != 0 {
		return ErrClockUncertain
	}
	if state.ClockExpired != 0 {
		return ErrDeadline
	}
	deadline, deadlineErr := parseProtectedUTC(state.WorkflowDeadlineUTC)
	anchor, anchorErr := parseProtectedUTC(state.ClockAnchorWallUTC)
	observed, observedErr := parseProtectedUTC(state.ClockObservedUTC)
	if deadlineErr != nil || anchorErr != nil || observedErr != nil || !validBootID(state.ClockBootID) || r.clockNow == nil {
		return r.persistClockPauseLocked(workflow, state)
	}
	wallNow, bootID, bootNowNS, err := r.clockNow()
	if err != nil || !validBootID(bootID) || bootNowNS < 0 || bootID != state.ClockBootID ||
		bootNowNS < state.ClockObservedBootTimeNS || wallNow.IsZero() {
		return r.persistClockPauseLocked(workflow, state)
	}
	wallNow = wallNow.Round(0).UTC()
	if wallNow.Before(observed) || bootNowNS < state.ClockAnchorBootTimeNS {
		return r.persistClockPauseLocked(workflow, state)
	}
	elapsedNS := bootNowNS - state.ClockAnchorBootTimeNS
	monotonicUTC := anchor.Add(time.Duration(elapsedNS))
	effectiveUTC := wallNow
	if observed.After(effectiveUTC) {
		effectiveUTC = observed
	}
	if monotonicUTC.After(effectiveUTC) {
		effectiveUTC = monotonicUTC
	}
	if effectiveUTC.IsZero() {
		return r.persistClockPauseLocked(workflow, state)
	}
	next := state
	next.ClockObservedUTC = wallNow.Format(time.RFC3339Nano)
	next.ClockObservedBootTimeNS = bootNowNS
	if !effectiveUTC.Before(deadline) {
		next.ClockExpired = 1
	}
	if err := r.persistProtectedClockStateLocked(workflow, state, next); err != nil {
		return err
	}
	if next.ClockExpired != 0 {
		return ErrDeadline
	}
	return nil
}

func (r *ProtectedTrustedRoot) persistClockPauseLocked(workflow string, state protectedRootState) error {
	next := state
	next.ClockUncertain = 1
	if err := r.persistProtectedClockStateLocked(workflow, state, next); err != nil {
		return err
	}
	return ErrClockUncertain
}

func (r *ProtectedTrustedRoot) persistProtectedClockStateLocked(workflow string, previous, next protectedRootState) error {
	if previous.Revision >= int64(maxSQLiteInteger) {
		r.poisoned = true
		return fmt.Errorf("%w: protected root clock revision exhausted", ErrOwnerStore)
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Failures before the commit leave the durable head unchanged: the deferred
	// rollback undoes any uncommitted write, and the caller refuses only this
	// attempt. They do not poison the root. A rollback that cannot be confirmed
	// may leave the single pooled connection inside the write, so it poisons.
	if err := r.injectClockPersistFaultLocked("begin"); err != nil {
		return fmt.Errorf("%w: begin protected clock high-water update: %v", ErrOwnerStore, err)
	}
	tx, err := r.db.BeginTx(persistCtx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin protected clock high-water update: %v", ErrOwnerStore, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			r.poisoned = true
		}
	}()
	if err := r.injectClockPersistFaultLocked("load"); err != nil {
		return fmt.Errorf("%w: read protected clock high-water: %v", ErrOwnerStore, err)
	}
	current, exists, err := loadProtectedRootState(persistCtx, tx, workflow)
	if err != nil {
		if errors.Is(err, ErrRollback) {
			// The caller validated this row moments earlier, so an invalid row now
			// means another writer changed it during this operation.
			r.poisoned = true
		}
		return err
	}
	if !exists || current.Revision != previous.Revision || current.WorkflowDeadlineUTC != previous.WorkflowDeadlineUTC ||
		current.ClockBootID != previous.ClockBootID || current.ClockAnchorWallUTC != previous.ClockAnchorWallUTC ||
		current.ClockAnchorBootTimeNS != previous.ClockAnchorBootTimeNS {
		r.poisoned = true
		return fmt.Errorf("%w: protected clock anchor/revision changed during verification", ErrRollback)
	}
	next.Revision = current.Revision + 1
	if err := r.injectClockPersistFaultLocked("update"); err != nil {
		return fmt.Errorf("%w: update protected clock high-water: %v", ErrOwnerStore, err)
	}
	if err := updateProtectedRootState(persistCtx, tx, next); err != nil {
		if errors.Is(err, ErrCAS) {
			// The row matched no workflow after it was read in this transaction.
			r.poisoned = true
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: commit protected clock high-water update: %v", ErrOwnerStore, err)
	}
	committed = true
	if err := syncSQLiteDirectory(r.path); err != nil {
		r.poisoned = true
		return fmt.Errorf("%w: sync protected root clock high-water: %v", ErrOwnerStore, err)
	}
	return nil
}

// injectClockPersistFaultLocked runs the test seam for one pre-commit step. It
// returns nil in production because clockPersistFault is never set there.
func (r *ProtectedTrustedRoot) injectClockPersistFaultLocked(stage string) error {
	if r.clockPersistFault == nil {
		return nil
	}
	return r.clockPersistFault(stage)
}

func (r *ProtectedTrustedRoot) verifyRootPathLocked() error {
	_, _, err := inspectSQLitePath(r.path, false)
	if err != nil {
		return err
	}
	identity, err := protectedRootIdentity(r.path, r.ownerStoreRoot)
	if err != nil {
		return err
	}
	if identity != r.rootIdentity {
		return fmt.Errorf("%w: host root namespace identity changed", ErrOwnerFence)
	}
	return nil
}

func (r *ProtectedTrustedRoot) verifyBoundStoreLocked(ctx context.Context, workflow string, state protectedRootState) error {
	store, ok := r.verifiedStores[workflow]
	if !ok || store.Path != state.OwnerStorePath || store.Digest != state.OwnerStoreIdentityDigest {
		return fmt.Errorf("%w: owner database does not match the protected-root workflow binding", ErrOwnerStore)
	}
	if err := r.boundary.VerifyOwnerStore(ctx, workflow, store); err != nil {
		return fmt.Errorf("%w: owner database host verification: %v", ErrOwnerStore, err)
	}
	return nil
}

func (r *ProtectedTrustedRoot) createNamespace(ctx context.Context) error {
	// Enabling WAL writes page 1, which fixes the page size, so geometry is
	// verified before it.
	if err := verifySQLitePageGeometry(ctx, r.db, sqliteProtectedRootMaxPageCount); err != nil {
		return err
	}
	var mode string
	if err := r.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("%w: configure protected root WAL mode: mode=%q error=%v", ErrOwnerStore, mode, err)
	}
	if err := r.verifyRootPragmas(ctx); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id=%d", protectedRootApplicationID)); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", protectedRootSchemaVersion)); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE workflow_root_metadata(
			singleton INTEGER PRIMARY KEY CHECK(singleton=1),
			schema_version INTEGER NOT NULL CHECK(schema_version=3),
			root_identity_digest TEXT NOT NULL CHECK(length(root_identity_digest)=64),
			owner_store_root TEXT NOT NULL
		) STRICT`,
		`CREATE TABLE workflow_root_state(
			workflow_id TEXT PRIMARY KEY NOT NULL,
			owner_store_path TEXT NOT NULL,
			owner_store_identity_digest TEXT NOT NULL CHECK(length(owner_store_identity_digest)=64),
			workflow_deadline_utc TEXT NOT NULL,
			clock_boot_id TEXT NOT NULL,
			clock_anchor_wall_utc TEXT NOT NULL,
			clock_anchor_boot_time_ns INTEGER NOT NULL CHECK(clock_anchor_boot_time_ns>=0),
			clock_observed_utc TEXT NOT NULL,
			clock_observed_boot_time_ns INTEGER NOT NULL CHECK(clock_observed_boot_time_ns>=0),
			clock_expired INTEGER NOT NULL CHECK(clock_expired IN (0,1)),
			clock_uncertain INTEGER NOT NULL CHECK(clock_uncertain IN (0,1)),
			checkpoint_epoch INTEGER NOT NULL CHECK(checkpoint_epoch>=0),
			checkpoint_digest TEXT NOT NULL CHECK(checkpoint_digest='' OR length(checkpoint_digest)=64),
			checkpoint_incarnation_digest TEXT NOT NULL CHECK(checkpoint_incarnation_digest='' OR length(checkpoint_incarnation_digest)=64),
			fence_epoch INTEGER NOT NULL CHECK(fence_epoch>0),
			fence_incarnation_digest TEXT NOT NULL CHECK(length(fence_incarnation_digest)=64),
			fresh_authorization_digest TEXT NOT NULL CHECK(fresh_authorization_digest='' OR length(fresh_authorization_digest)=64),
			revision INTEGER NOT NULL CHECK(revision>0)
		) STRICT`,
		`CREATE TABLE workflow_root_owner_epochs(
			workflow_id TEXT NOT NULL,
			epoch INTEGER NOT NULL CHECK(epoch>0),
			incarnation_json BLOB NOT NULL CHECK(length(incarnation_json) BETWEEN 1 AND 4096),
			incarnation_digest TEXT NOT NULL CHECK(length(incarnation_digest)=64),
			drained INTEGER NOT NULL CHECK(drained IN (0,1)),
			drain_evidence_digest TEXT NOT NULL CHECK(drain_evidence_digest='' OR length(drain_evidence_digest)=64),
			PRIMARY KEY(workflow_id,epoch),
			FOREIGN KEY(workflow_id) REFERENCES workflow_root_state(workflow_id) ON DELETE RESTRICT
		) STRICT`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%w: create protected host-root schema: %v", ErrOwnerStore, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_root_metadata(singleton,schema_version,root_identity_digest,owner_store_root)
		VALUES(1,?,?,?)`, protectedRootSchemaVersion, r.rootIdentity, r.ownerStoreRoot); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: bind protected host-root identity: %v", ErrOwnerStore, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit protected host-root schema: %v", ErrOwnerStore, err)
	}
	if err := syncSQLiteDirectory(r.path); err != nil {
		return fmt.Errorf("%w: sync protected-root directory: %v", ErrOwnerStore, err)
	}
	return r.verifyNamespace(ctx)
}

func (r *ProtectedTrustedRoot) verifyNamespace(ctx context.Context) error {
	if err := r.verifyRootPathLocked(); err != nil {
		return err
	}
	if err := verifySQLitePageGeometry(ctx, r.db, sqliteProtectedRootMaxPageCount); err != nil {
		return err
	}
	if err := r.verifyRootPragmas(ctx); err != nil {
		return err
	}
	var appID, version int64
	if err := r.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if appID != protectedRootApplicationID || version != protectedRootSchemaVersion {
		return fmt.Errorf("%w: protected host-root schema identity differs", ErrRollback)
	}
	var schemaVersion int64
	var rootIdentity, storeRoot string
	if err := r.db.QueryRowContext(ctx, `SELECT schema_version,root_identity_digest,owner_store_root
		FROM workflow_root_metadata WHERE singleton=1`).Scan(&schemaVersion, &rootIdentity, &storeRoot); err != nil {
		return fmt.Errorf("%w: read protected root metadata: %v", ErrOwnerStore, err)
	}
	if schemaVersion != protectedRootSchemaVersion || rootIdentity != r.rootIdentity || storeRoot != r.ownerStoreRoot {
		return fmt.Errorf("%w: protected host-root metadata differs from host configuration", ErrRollback)
	}
	var integrity string
	if err := r.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("%w: protected root SQLite quick_check: result=%q error=%v", ErrRollback, integrity, err)
	}
	return nil
}

func (r *ProtectedTrustedRoot) verifyRootPragmas(ctx context.Context) error {
	var engineVersion string
	if err := r.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&engineVersion); err != nil {
		return fmt.Errorf("%w: read protected-root SQLite version: %v", ErrOwnerStore, err)
	}
	if err := requireWALResetFix(engineVersion); err != nil {
		return err
	}
	var mode string
	if err := r.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("%w: protected root is not in WAL mode: %q %v", ErrOwnerStore, mode, err)
	}
	var syncMode, fk, timeout, autocheckpoint int64
	if err := r.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&autocheckpoint); err != nil {
		return err
	}
	if syncMode != 2 || fk != 1 || timeout != 5000 || autocheckpoint != 1000 {
		return fmt.Errorf("%w: protected-root PRAGMA configuration differs", ErrOwnerStore)
	}
	return nil
}

func loadProtectedRootState(ctx context.Context, queryer protectedRootQueryer, workflow string) (protectedRootState, bool, error) {
	var state protectedRootState
	err := queryer.QueryRowContext(ctx, `SELECT workflow_id,owner_store_path,owner_store_identity_digest,
		workflow_deadline_utc,clock_boot_id,clock_anchor_wall_utc,clock_anchor_boot_time_ns,
		clock_observed_utc,clock_observed_boot_time_ns,clock_expired,clock_uncertain,
		checkpoint_epoch,checkpoint_digest,checkpoint_incarnation_digest,
		fence_epoch,fence_incarnation_digest,fresh_authorization_digest,revision FROM workflow_root_state WHERE workflow_id=?`, workflow).Scan(
		&state.WorkflowID, &state.OwnerStorePath, &state.OwnerStoreIdentityDigest,
		&state.WorkflowDeadlineUTC, &state.ClockBootID, &state.ClockAnchorWallUTC, &state.ClockAnchorBootTimeNS,
		&state.ClockObservedUTC, &state.ClockObservedBootTimeNS, &state.ClockExpired, &state.ClockUncertain,
		&state.CheckpointEpoch, &state.CheckpointDigest, &state.CheckpointIncarnationDigest,
		&state.FenceEpoch, &state.FenceIncarnationDigest, &state.FreshAuthorizationDigest, &state.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return protectedRootState{}, false, nil
	}
	if err != nil {
		return protectedRootState{}, false, fmt.Errorf("%w: read protected host-root workflow state: %v", ErrOwnerStore, err)
	}
	if state.WorkflowID != workflow || state.OwnerStorePath == "" || !filepath.IsAbs(state.OwnerStorePath) ||
		filepath.Clean(state.OwnerStorePath) != state.OwnerStorePath || !validDigest(state.OwnerStoreIdentityDigest) ||
		validateProtectedClockState(state) != nil ||
		state.CheckpointEpoch < 0 || state.FenceEpoch <= 0 || state.Revision <= 0 ||
		state.CheckpointEpoch > state.FenceEpoch ||
		(state.CheckpointEpoch == 0 && (state.CheckpointDigest != "" || state.CheckpointIncarnationDigest != "")) ||
		(state.CheckpointEpoch > 0 && (!validDigest(state.CheckpointDigest) || !validDigest(state.CheckpointIncarnationDigest))) ||
		!validDigest(state.FenceIncarnationDigest) ||
		(state.FreshAuthorizationDigest != "" && !validDigest(state.FreshAuthorizationDigest)) {
		return protectedRootState{}, false, fmt.Errorf("%w: protected root contains invalid workflow authority state", ErrRollback)
	}
	return state, true, nil
}

func insertProtectedRootState(ctx context.Context, tx *sql.Tx, state protectedRootState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_root_state(
		workflow_id,owner_store_path,owner_store_identity_digest,
		workflow_deadline_utc,clock_boot_id,clock_anchor_wall_utc,clock_anchor_boot_time_ns,clock_observed_utc,clock_observed_boot_time_ns,clock_expired,clock_uncertain,
		checkpoint_epoch,checkpoint_digest,checkpoint_incarnation_digest,
		fence_epoch,fence_incarnation_digest,fresh_authorization_digest,revision
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, state.WorkflowID, state.OwnerStorePath, state.OwnerStoreIdentityDigest,
		state.WorkflowDeadlineUTC, state.ClockBootID, state.ClockAnchorWallUTC, state.ClockAnchorBootTimeNS,
		state.ClockObservedUTC, state.ClockObservedBootTimeNS, state.ClockExpired, state.ClockUncertain,
		state.CheckpointEpoch, state.CheckpointDigest,
		state.CheckpointIncarnationDigest, state.FenceEpoch, state.FenceIncarnationDigest,
		state.FreshAuthorizationDigest, state.Revision)
	if err != nil {
		return fmt.Errorf("%w: insert protected root workflow: %v", ErrOwnerStore, err)
	}
	return nil
}

func updateProtectedRootState(ctx context.Context, tx *sql.Tx, state protectedRootState) error {
	result, err := tx.ExecContext(ctx, `UPDATE workflow_root_state SET
		owner_store_path=?,owner_store_identity_digest=?,workflow_deadline_utc=?,clock_boot_id=?,clock_anchor_wall_utc=?,
		clock_anchor_boot_time_ns=?,clock_observed_utc=?,clock_observed_boot_time_ns=?,clock_expired=?,clock_uncertain=?,
		checkpoint_epoch=?,checkpoint_digest=?,checkpoint_incarnation_digest=?,fence_epoch=?,
		fence_incarnation_digest=?,fresh_authorization_digest=?,revision=? WHERE workflow_id=?`,
		state.OwnerStorePath, state.OwnerStoreIdentityDigest, state.WorkflowDeadlineUTC, state.ClockBootID,
		state.ClockAnchorWallUTC, state.ClockAnchorBootTimeNS, state.ClockObservedUTC, state.ClockObservedBootTimeNS, state.ClockExpired, state.ClockUncertain,
		state.CheckpointEpoch, state.CheckpointDigest,
		state.CheckpointIncarnationDigest, state.FenceEpoch,
		state.FenceIncarnationDigest, state.FreshAuthorizationDigest, state.Revision, state.WorkflowID)
	if err != nil {
		return fmt.Errorf("%w: update protected root workflow: %v", ErrOwnerStore, err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: protected root state update did not change one workflow", ErrCAS)
	}
	return nil
}

func insertOwnerEpoch(ctx context.Context, tx *sql.Tx, workflow string, epoch uint64,
	incarnation OwnerIncarnation, drained bool, drainDigest string) error {
	encoded, err := json.Marshal(incarnation)
	if err != nil {
		return fmt.Errorf("%w: encode protected owner incarnation: %v", ErrOwnerStore, err)
	}
	drainedValue := 0
	if drained {
		drainedValue = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_root_owner_epochs(
		workflow_id,epoch,incarnation_json,incarnation_digest,drained,drain_evidence_digest
	) VALUES(?,?,?,?,?,?)`, workflow, int64(epoch), encoded, incarnation.Digest, drainedValue, drainDigest)
	if err != nil {
		return fmt.Errorf("%w: append protected owner epoch: %v", ErrOwnerStore, err)
	}
	return nil
}

func loadOwnerEpochs(ctx context.Context, queryer protectedRootQueryer, workflow string, first, last uint64) ([]protectedOwnerEpoch, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT workflow_id,epoch,incarnation_json,incarnation_digest,drained,drain_evidence_digest
		FROM workflow_root_owner_epochs WHERE workflow_id=? AND epoch>=? AND epoch<=? ORDER BY epoch`,
		workflow, int64(first), int64(last))
	if err != nil {
		return nil, fmt.Errorf("%w: read protected owner epoch history: %v", ErrOwnerStore, err)
	}
	defer rows.Close()
	var owners []protectedOwnerEpoch
	for rows.Next() {
		var owner protectedOwnerEpoch
		if err := rows.Scan(&owner.WorkflowID, &owner.Epoch, &owner.IncarnationJSON,
			&owner.IncarnationDigest, &owner.Drained, &owner.DrainEvidenceDigest); err != nil {
			return nil, fmt.Errorf("%w: decode protected owner epoch history: %v", ErrRollback, err)
		}
		var incarnation OwnerIncarnation
		if decodeOwnerJSON(owner.IncarnationJSON, &incarnation) != nil || !validOwnerIncarnation(incarnation) ||
			owner.WorkflowID != workflow || owner.Epoch <= 0 || owner.IncarnationDigest != incarnation.Digest ||
			(owner.Drained != 0 && owner.Drained != 1) ||
			(owner.Drained == 0 && owner.DrainEvidenceDigest != "") ||
			(owner.Drained == 1 && !validDigest(owner.DrainEvidenceDigest)) {
			return nil, fmt.Errorf("%w: malformed protected owner epoch record", ErrRollback)
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan protected owner epoch history: %v", ErrOwnerStore, err)
	}
	return owners, nil
}

func ownerEpochIdentities(owners []protectedOwnerEpoch) []OwnerEpochIdentity {
	result := make([]OwnerEpochIdentity, 0, len(owners))
	for _, owner := range owners {
		var incarnation OwnerIncarnation
		_ = json.Unmarshal(owner.IncarnationJSON, &incarnation)
		result = append(result, OwnerEpochIdentity{Epoch: uint64(owner.Epoch), Incarnation: incarnation})
	}
	return result
}

// markOwnerEpochDrained persists the drain evidence for one superseded owner. The
// evidence digest binds the protected root, the owner epoch, and the verifier's
// drain-proof digest, which commits to the observed drain facts.
func markOwnerEpochDrained(ctx context.Context, tx *sql.Tx, rootIdentity string, ownerEpoch protectedOwnerEpoch,
	proofDigest string) error {
	if ownerEpoch.Drained == 1 && validDigest(ownerEpoch.DrainEvidenceDigest) {
		return nil
	}
	if ownerEpoch.Drained != 0 || ownerEpoch.DrainEvidenceDigest != "" {
		return fmt.Errorf("%w: owner drain record is malformed before update", ErrRollback)
	}
	var incarnation OwnerIncarnation
	if !validDigest(rootIdentity) {
		return fmt.Errorf("%w: protected root identity is malformed", ErrRollback)
	}
	if !validDigest(proofDigest) {
		return fmt.Errorf("%w: owner drain proof digest is malformed", ErrOwnerFence)
	}
	if err := decodeOwnerJSON(ownerEpoch.IncarnationJSON, &incarnation); err != nil || !validOwnerIncarnation(incarnation) {
		return fmt.Errorf("%w: decode owner before drain marking: %v", ErrRollback, err)
	}
	drainDigest := ownerDrainEvidenceDigest(rootIdentity, ownerEpoch.WorkflowID, uint64(ownerEpoch.Epoch),
		incarnation.Digest, proofDigest)
	result, err := tx.ExecContext(ctx, `UPDATE workflow_root_owner_epochs SET drained=1,drain_evidence_digest=?
		WHERE workflow_id=? AND epoch=? AND incarnation_digest=? AND drained=0`,
		drainDigest, ownerEpoch.WorkflowID, ownerEpoch.Epoch, incarnation.Digest)
	if err != nil {
		return fmt.Errorf("%w: persist owner drain evidence: %v", ErrOwnerStore, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("%w: owner drain record changed during acquisition", ErrCAS)
	}
	return nil
}

// ownerDrainEvidenceDigest is the persisted drain evidence for one superseded
// owner. It is a pure binding of the protected root, workflow, epoch, owner
// incarnation digest, and the verifier's drain-proof digest. Any changed input
// changes the evidence.
func ownerDrainEvidenceDigest(rootIdentity, workflow string, epoch uint64, incarnationDigest, proofDigest string) string {
	encoded, _ := json.Marshal(struct {
		RootIdentity string `json:"root_identity_digest"`
		Workflow     string `json:"workflow_id"`
		Epoch        uint64 `json:"epoch"`
		Incarnation  string `json:"incarnation_digest"`
		ProofDigest  string `json:"drain_proof_digest"`
	}{rootIdentity, workflow, epoch, incarnationDigest, proofDigest})
	sum := sha256.Sum256(append([]byte("tbound-owner-drain-evidence/v2\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func validOwnerIncarnation(incarnation OwnerIncarnation) bool {
	return incarnation.PID > 0 && incarnation.StartTimeTicks > 0 && incarnation.PIDNamespaceInode > 0 &&
		incarnation.BootID != "" && validDigest(incarnation.CgroupIdentityDigest) &&
		incarnation.Digest == computeOwnerIncarnationDigest(incarnation)
}

func cleanProtectedRootDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%w: protected root directory must be absolute and clean", ErrOwnerStore)
	}
	return path, nil
}

func validateProtectedOwnerStoreRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect host owner-store root: %v", ErrOwnerStore, err)
	}
	uid, uidErr := sqliteFileUID(info)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || uidErr != nil ||
		uid != uint32(syscall.Geteuid()) || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: owner-store root must be supervisor-owned mode 0700", ErrOwnerStore)
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	separator := string(filepath.Separator)
	return left == right || strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}

func protectedRootIdentity(path, ownerStoreRoot string) (string, error) {
	clean, storageDigest, err := inspectSQLitePath(path, false)
	if err != nil {
		return "", err
	}
	if err := validateProtectedOwnerStoreRoot(ownerStoreRoot); err != nil {
		return "", err
	}
	identity := struct {
		Path             string `json:"path"`
		StorageDigest    string `json:"storage_digest"`
		OwnerStoreRoot   string `json:"owner_store_root"`
		OwnerStoreDevice uint64 `json:"owner_store_device"`
		OwnerStoreInode  uint64 `json:"owner_store_inode"`
		LockDevice       uint64 `json:"lock_device"`
		LockInode        uint64 `json:"lock_inode"`
	}{Path: clean, StorageDigest: storageDigest, OwnerStoreRoot: ownerStoreRoot}
	info, err := os.Lstat(ownerStoreRoot)
	if err != nil {
		return "", fmt.Errorf("%w: stat protected owner-store root: %v", ErrOwnerStore, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("%w: owner-store root inode identity is unavailable", ErrOwnerStore)
	}
	identity.OwnerStoreDevice, identity.OwnerStoreInode = uint64(stat.Dev), stat.Ino
	lockInfo, err := os.Lstat(path + ".lock")
	if err != nil {
		return "", fmt.Errorf("%w: stat protected root lock: %v", ErrOwnerStore, err)
	}
	lockUID, err := sqliteFileUID(lockInfo)
	lockStat, ok := lockInfo.Sys().(*syscall.Stat_t)
	if lockInfo.Mode()&os.ModeSymlink != 0 || !lockInfo.Mode().IsRegular() || err != nil ||
		lockUID != uint32(syscall.Geteuid()) || lockInfo.Mode().Perm() != 0o600 || !ok || lockStat == nil {
		return "", fmt.Errorf("%w: protected root lock identity is invalid", ErrOwnerStore)
	}
	identity.LockDevice, identity.LockInode = uint64(lockStat.Dev), lockStat.Ino
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("tbound-protected-workflow-root/v1\n"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func pathIsWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func acquireProtectedRootLock(rootPath string, create bool) (*os.File, error) {
	lockPath := rootPath + ".lock"
	flags := syscall.O_RDWR | syscall.O_CLOEXEC | syscall.O_NOFOLLOW
	if create {
		flags |= syscall.O_CREAT
	}
	fd, err := syscall.Open(lockPath, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open protected root lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	pathInfo, pathErr := os.Lstat(lockPath)
	uid, uidErr := sqliteFileUID(info)
	if pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) ||
		info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || uidErr != nil ||
		uid != uint32(syscall.Geteuid()) || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protected root lock must be supervisor-owned mode 0600", ErrOwnerStore)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protected root namespace is already owned: %v", ErrOwnerFence, err)
	}
	return file, nil
}

func releaseProtectedRootLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
