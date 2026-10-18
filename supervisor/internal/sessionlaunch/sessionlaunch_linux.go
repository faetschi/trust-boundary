//go:build linux

package sessionlaunch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

const (
	linuxOPath                 = 0x200000
	maxDevelopmentRunnerLeases = 512

	provenanceSchema = sessionrepo.D06ProvenanceSchemaVersion
	// sourceBindingDomain is the digest domain of the source-binding digest. Its
	// input gained xattr_authority, so the label moved from v1 to v2.
	sourceBindingDomain     = "tbound/sessionlaunch/source-binding/v2"
	sourceBindingEventKind  = "sessionlaunch_source_binding"
	privateGitEventKind     = "sessionlaunch_private_git_ready"
	failedSnapshotEventKind = "sessionlaunch_private_git_failed"
)

var (
	ErrInvalidSource                = errors.New("invalid sealed session source")
	ErrSourceChanged                = errors.New("sealed session source changed during private snapshot preparation")
	ErrSourceEvidence               = errors.New("sealed session source does not match durable session evidence")
	ErrAuditJournalMismatch         = errors.New("sessionlaunch journal is not the store's exact verified audit journal")
	ErrPrivateParent                = errors.New("private Git admin parent lacks an exclusive supervisor-owned boundary")
	ErrProvenanceMissing            = errors.New("durable D06 provenance is incomplete")
	ErrUnsupportedProvenanceVersion = errors.New("unsupported D06 provenance version: an upgrade requires draining retained sessions first")
	ErrWorkerExposure               = errors.New("worker exposure is unavailable or not bound to a verified sealed generation")
	ErrDevelopmentSandbox           = errors.New("measured development sandbox profile is unavailable")
	ErrRecoveryActivationRequired   = errors.New("recovery preview requires a durable clean-restart reservation before activation")
)

var (
	ErrRunnerLease   = errors.New("command runner has no one-use session lease binding")
	ErrRunnerReceipt = errors.New("command runner settlement does not match its observed cell receipt")
)

// SourceProfile is retained from the trusted session-store creation profile.
// The xattr digest is not exported by the current Store API, so the composition
// root must pass the exact profile value used to create that Store; this value
// is cross-bound to every recaptured manifest and persisted in source evidence.
type SourceProfile struct {
	Limits          workspace.Limits
	XattrVisibility workspace.XattrVisibilityAttestation
}

// GitProfile is copied from the trusted immutable runtime profile. privategit
// independently hashes and verifies the pinned executable descriptor; this
// composition checks its measured digest and execution boundary against the
// admitted profile before recording readiness.
type GitProfile struct {
	ExecutableDigest  string
	ExecutionBoundary string
}

// ManagedStore retains the exact source-scan profile supplied when the
// sessionrepo.Store was created. The current Store API does not export these
// fields; keeping them at construction avoids later substituting caller strings
// for the actual policy/limit/xattr values. The session authority callbacks in
// Options must still be concrete trusted implementations at the composition
// root; this wrapper does not bless arbitrary callback behavior.
type ManagedStore struct {
	mu       sync.Mutex
	store    *sessionrepo.Store
	journal  *audit.Journal
	profile  SourceProfile
	prepared int
	closed   bool
}

func CreateStore(root *os.File, options sessionrepo.Options) (*ManagedStore, error) {
	if err := validateManagedStoreOptions(options); err != nil {
		return nil, err
	}
	store, err := sessionrepo.Create(root, options)
	if err != nil {
		return nil, err
	}
	return newManagedStore(store, options), nil
}

// OpenStore is intentionally unavailable: recovery must use the explicit
// read-only preview and clean-restart activation path below.
func OpenStore(root *os.File, options sessionrepo.Options) (*ManagedStore, error) {
	return nil, ErrRecoveryActivationRequired
}

// OpenRecoveryPreview reconstructs an existing Store using only its exact
// retained Store/effect/admission facts. It invokes no caller-supplied live or
// historical authority callbacks, performs no repository-state repair, and
// keeps mutation, source, and exposure APIs disabled until owner receipt.
func OpenRecoveryPreview(root *os.File, options sessionrepo.Options) (*ManagedStore, error) {
	if options.Journal == nil {
		return nil, errors.New("sessionlaunch recovery preview requires the retained Store journal")
	}
	store, err := sessionrepo.OpenRecoveryPreview(root, options)
	if err != nil {
		return nil, err
	}
	return newManagedStore(store, options), nil
}

// ActivateRecovery applies deferred Store-state repair only under the exact,
// latest owner-authenticated clean-restart receipt. The expected pins are
// checked both here and by the owner-backed receipt verifier; this API never
// treats an arbitrary callback returning nil as restart authority.
func (s *ManagedStore) ActivateRecovery(ctx context.Context, ledger *workflowrecovery.Ledger,
	receipt workflowrecovery.Receipt, pins workflowrecovery.Pins, options sessionrepo.Options) error {
	if s == nil || ctx == nil || ledger == nil || !receipt.IsCleanRestart() {
		return ErrRecoveryActivationRequired
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, pins); err != nil {
		return errors.Join(ErrRecoveryActivationRequired, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.store == nil || s.prepared > 1 {
		return ErrRecoveryActivationRequired
	}
	if err := verifyManagedStoreRecoveryPins(s.store, pins, options); err != nil {
		return err
	}
	if err := s.store.ActivateRecovery(ctx, ledger, receipt, pins, options); err != nil {
		return err
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, pins); err != nil {
		return errors.Join(ErrRecoveryActivationRequired, err)
	}
	return verifyManagedStoreRecoveryPins(s.store, pins, options)
}

func validateManagedStoreOptions(options sessionrepo.Options) error {
	if options.Journal == nil || options.AuthorizeOperation == nil || options.VerifyDecision == nil || options.VerifySettlement == nil {
		return errors.New("sessionlaunch store requires explicit operation, decision and settlement authorities")
	}
	return nil
}

func newManagedStore(store *sessionrepo.Store, options sessionrepo.Options) *ManagedStore {
	return &ManagedStore{store: store, journal: options.Journal,
		profile: SourceProfile{Limits: options.Limits, XattrVisibility: options.XattrVisibility}}
}

func (s *ManagedStore) Store() *sessionrepo.Store {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.store
}

func (s *ManagedStore) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.prepared != 0 {
		return errors.New("session store still has prepared D06 sessions")
	}
	s.closed = true
	return s.store.Close()
}

// AdminParent is the supervisor-owned namespace used for private Git
// administration. It retains the exact directory and lock-file descriptors.
// Worker APIs in this package never return this parent or the Repository.Root.
type AdminParent struct {
	mu           sync.Mutex
	root         *os.File
	guard        *os.File
	rootID       nodeID
	guardID      nodeID
	recoveryName string
	closed       bool
	prepared     int
	preparing    bool
}

type nodeID struct{ device, inode uint64 }

// OpenAdminParent binds an existing exact-mode-0700 supervisor directory and
// takes a nonblocking advisory lock on its persistent private guard file. The
// caller must keep this object alive through session teardown. This prevents
// cooperating launches from sharing cleanup ownership; it is not protection
// from a hostile same-UID process that ignores the lock.
func OpenAdminParent(root *os.File) (*AdminParent, error) {
	if root == nil {
		return nil, ErrPrivateParent
	}
	rootCopy, err := duplicateFile(root, "sessionlaunch-admin-parent")
	if err != nil {
		return nil, err
	}
	rootStat, err := statNode(rootCopy)
	if err != nil || rootStat.mode&syscall.S_IFMT != syscall.S_IFDIR || rootStat.uid != uint32(syscall.Geteuid()) ||
		rootStat.gid != uint32(syscall.Getegid()) || rootStat.mode&0o7777 != 0o700 {
		_ = rootCopy.Close()
		return nil, errors.Join(ErrPrivateParent, err)
	}
	fd, err := syscall.Openat(int(rootCopy.Fd()), ".tbound-sessionlaunch.lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		_ = rootCopy.Close()
		return nil, fmt.Errorf("open private sessionlaunch lock: %w", err)
	}
	guard := os.NewFile(uintptr(fd), "sessionlaunch-private-parent-lock")
	if guard == nil {
		_ = syscall.Close(fd)
		_ = rootCopy.Close()
		return nil, ErrPrivateParent
	}
	guardStat, err := statNode(guard)
	if err != nil || guardStat.mode&syscall.S_IFMT != syscall.S_IFREG || guardStat.uid != uint32(syscall.Geteuid()) ||
		guardStat.gid != uint32(syscall.Getegid()) || guardStat.mode&0o7777 != 0o600 || guardStat.links != 1 {
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, errors.Join(ErrPrivateParent, err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, fmt.Errorf("acquire exclusive private parent lock: %w", err)
	}
	if err := writeGuardIdentity(rootCopy, guard); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, err
	}
	if err := requireFreshAdminParent(rootCopy, guard, ".tbound-sessionlaunch.lock"); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, err
	}
	return &AdminParent{root: rootCopy, guard: guard,
		rootID:  nodeID{device: rootStat.device, inode: rootStat.inode},
		guardID: nodeID{device: guardStat.device, inode: guardStat.inode}}, nil
}

// OpenAdminParentForRecovery acquires the retained private-admin owner lock
// without creating or repairing any entry. The exact repository name must come
// from an independently authenticated host checkpoint; this function never
// discovers a retained repository by scanning the parent.
func OpenAdminParentForRecovery(root *os.File, repositoryName string) (*AdminParent, error) {
	if root == nil || !validPrivateGitName(repositoryName) {
		return nil, ErrPrivateParent
	}
	rootCopy, err := duplicateFile(root, "sessionlaunch-recovery-admin-parent")
	if err != nil {
		return nil, err
	}
	rootStat, err := statNode(rootCopy)
	if err != nil || rootStat.mode&syscall.S_IFMT != syscall.S_IFDIR || rootStat.uid != uint32(syscall.Geteuid()) ||
		rootStat.gid != uint32(syscall.Getegid()) || rootStat.mode&0o7777 != 0o700 {
		_ = rootCopy.Close()
		return nil, errors.Join(ErrPrivateParent, err)
	}
	fd, err := syscall.Openat(int(rootCopy.Fd()), ".tbound-sessionlaunch.lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = rootCopy.Close()
		return nil, errors.Join(ErrPrivateParent, err)
	}
	guard := os.NewFile(uintptr(fd), "sessionlaunch-recovery-private-parent-lock")
	if guard == nil {
		_ = syscall.Close(fd)
		_ = rootCopy.Close()
		return nil, ErrPrivateParent
	}
	guardStat, err := statNode(guard)
	if err != nil || guardStat.mode&syscall.S_IFMT != syscall.S_IFREG || guardStat.uid != uint32(syscall.Geteuid()) ||
		guardStat.gid != uint32(syscall.Getegid()) || guardStat.mode&0o7777 != 0o600 || guardStat.links != 1 {
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, errors.Join(ErrPrivateParent, err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, fmt.Errorf("acquire exclusive retained private parent lock: %w", err)
	}
	cleanup := func(cause error) (*AdminParent, error) {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = guard.Close()
		_ = rootCopy.Close()
		return nil, cause
	}
	if err := writeGuardIdentity(rootCopy, guard); err != nil {
		return cleanup(err)
	}
	if err := requireRecoveredAdminParent(rootCopy, guard, ".tbound-sessionlaunch.lock", repositoryName); err != nil {
		return cleanup(err)
	}
	return &AdminParent{root: rootCopy, guard: guard,
		rootID:       nodeID{device: rootStat.device, inode: rootStat.inode},
		guardID:      nodeID{device: guardStat.device, inode: guardStat.inode},
		recoveryName: repositoryName}, nil
}

func (p *AdminParent) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.prepared != 0 || p.preparing {
		return errors.New("private Git admin parent still owns prepared sessions")
	}
	p.closed = true
	var errs []error
	if p.guard != nil {
		errs = append(errs, syscall.Flock(int(p.guard.Fd()), syscall.LOCK_UN))
		errs = append(errs, p.guard.Close())
		p.guard = nil
	}
	if p.root != nil {
		errs = append(errs, p.root.Close())
		p.root = nil
	}
	return errors.Join(errs...)
}

// Provenance commits to the verified source evidence and actual private Git
// initialization record. It contains no source path and is copied defensively.
type Provenance struct {
	SchemaVersion        string                             `json:"schema_version"`
	BindingDigest        string                             `json:"binding_digest"`
	SourceGeneration     string                             `json:"source_generation"`
	SourceTreeDigest     string                             `json:"source_tree_digest"`
	PolicyDigest         string                             `json:"policy_digest"`
	MetadataPolicyDigest string                             `json:"metadata_policy_digest"`
	StoreAuditHead       string                             `json:"store_audit_head"`
	StoreAuditRecords    uint64                             `json:"store_audit_records"`
	SourceRootDevice     uint64                             `json:"source_root_device"`
	SourceRootInode      uint64                             `json:"source_root_inode"`
	XattrProfileDigest   string                             `json:"xattr_profile_digest"`
	XattrAuthority       workspace.XattrVisibilityAuthority `json:"xattr_authority"`
	Transfer             string                             `json:"transfer"`
	PrivateGit           privategit.Provenance              `json:"private_git"`
	PrivateGitDigest     string                             `json:"private_git_digest"`
	GitExecutableDigest  string                             `json:"git_executable_digest"`
	GitExecutionBoundary string                             `json:"git_execution_boundary"`
	SourceRecord         uint64                             `json:"source_record_sequence"`
	PrivateGitRecord     uint64                             `json:"private_git_record_sequence"`
}

// PreparedSession owns the private Git admin repository and persisted source
// provenance. The retained sessionrepo store remains the only worker-visible
// source of sealed read-only generations and disposable command views.
type PreparedSession struct {
	mu              sync.Mutex
	managed         *ManagedStore
	store           *sessionrepo.Store
	generation      *sessionrepo.Generation
	journal         *audit.Journal
	admin           *AdminParent
	git             privateGitHandle
	destroyGit      bool
	profile         SourceProfile
	provenance      Provenance
	basePrefix      []byte
	recoveryPending bool
	closed          bool
	closing         bool
	views           map[*WorkerView]struct{}
	sources         map[*VerifiedD09ExposureSource]struct{}
}

type privateGitHandle interface {
	Verify() error
	Snapshot() privategit.Snapshot
	Provenance() privategit.Provenance
	ReopenIdentity() (privategit.RepositoryIdentity, error)
	Close() error
}

type privateGitDestroyer interface {
	Destroy() error
}

// RecoveryGitPin is the independently authenticated D06/private-Git identity
// required to rebind a retained private Git tree. VerifyIdentity must compare
// the complete candidate identity with host-owned checkpoint state.
type RecoveryGitPin struct {
	RepositoryName           string
	ProvenanceDigest         string
	RepositoryIdentityDigest string
	VerifyIdentity           func(privategit.RepositoryIdentity) error
}

// RecoveryCandidate retains verified D06 evidence without exposing worker
// source authority. Only Activate can turn it into a PreparedSession, after the
// trusted workflow recovery composition has completed its owner, transcript,
// and cumulative-budget checks.
type RecoveryCandidate struct {
	mu         sync.Mutex
	managed    *ManagedStore
	prepared   *PreparedSession
	activating bool
	consumed   bool
}

// VerifyPreview repeats the read-only Store/D06/private-Git checks immediately
// before the workflow owner observes and reserves the monotonic recovery tail.
// It does not activate the Store or create worker/source authority.
func (c *RecoveryCandidate) VerifyPreview(pins workflowrecovery.Pins, options sessionrepo.Options) error {
	if c == nil {
		return ErrProvenanceMissing
	}
	c.mu.Lock()
	if c.consumed || c.activating || c.prepared == nil {
		c.mu.Unlock()
		return ErrProvenanceMissing
	}
	prepared := c.prepared
	c.mu.Unlock()
	prepared.mu.Lock()
	if prepared.closed || prepared.closing || !prepared.recoveryPending || prepared.store == nil || prepared.generation == nil {
		prepared.mu.Unlock()
		return ErrProvenanceMissing
	}
	store, baseline, journal, git := prepared.store, prepared.generation, prepared.journal, prepared.git
	prepared.mu.Unlock()
	if err := prepared.VerifySourceBinding(store, baseline, journal); err != nil {
		return err
	}
	if tip := store.TipGeneration(); tip == nil {
		return sessionrepo.ErrNoBaseline
	} else if err := verifyPreparedRecoveryPins(store, prepared, baseline, tip, pins, options); err != nil {
		return err
	}
	if git == nil {
		return ErrProvenanceMissing
	}
	return git.Verify()
}

// Activate requires the opaque receipt returned by the durable workflow owner.
// The caller must first have completed the read-only full-history verifier;
// the owner only issues a clean-restart receipt from its verified observation.
// This method independently rechecks the receipt, exact retained Store heads,
// D06 provenance, private Git identity, and Store tip before enabling exposure.
func (c *RecoveryCandidate) Activate(ctx context.Context, ledger *workflowrecovery.Ledger,
	receipt workflowrecovery.Receipt, pins workflowrecovery.Pins, options sessionrepo.Options) (*PreparedSession, error) {
	if c == nil || ctx == nil || ledger == nil || !receipt.IsCleanRestart() {
		return nil, ErrProvenanceMissing
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, pins); err != nil {
		return nil, errors.Join(ErrRecoveryActivationRequired, err)
	}
	c.mu.Lock()
	if c.consumed || c.activating || c.prepared == nil {
		c.mu.Unlock()
		return nil, ErrProvenanceMissing
	}
	c.activating = true
	prepared := c.prepared
	c.mu.Unlock()
	finishFailure := func(err error) (*PreparedSession, error) {
		c.mu.Lock()
		c.activating = false
		c.mu.Unlock()
		return nil, err
	}

	prepared.mu.Lock()
	if prepared.closed || prepared.closing || !prepared.recoveryPending || prepared.store == nil || prepared.generation == nil {
		prepared.mu.Unlock()
		return finishFailure(ErrProvenanceMissing)
	}
	store, baseline, journal := prepared.store, prepared.generation, prepared.journal
	prepared.mu.Unlock()
	if err := prepared.VerifySourceBinding(store, baseline, journal); err != nil {
		return finishFailure(err)
	}
	tip := store.TipGeneration()
	if tip == nil {
		return finishFailure(sessionrepo.ErrNoBaseline)
	}
	if err := verifyPreparedRecoveryPins(store, prepared, baseline, tip, pins, options); err != nil {
		return finishFailure(err)
	}
	if err := c.managed.ActivateRecovery(ctx, ledger, receipt, pins, options); err != nil {
		return finishFailure(err)
	}
	chain, err := store.Verify()
	if err != nil {
		return finishFailure(err)
	}
	if err := prepared.VerifySourceBinding(store, baseline, journal); err != nil {
		return finishFailure(err)
	}
	tipAfter := store.TipGeneration()
	if tipAfter == nil || tipAfter.ID() != tip.ID() || tipAfter.TreeDigest() != tip.TreeDigest() ||
		chain.SealedGeneration != tip.ID() || chain.SealedTreeDigest != tip.TreeDigest() {
		return finishFailure(errors.New("retained Store tip changed during recovery activation"))
	}
	if err := verifyPreparedRecoveryPins(store, prepared, baseline, tipAfter, pins, options); err != nil {
		return finishFailure(err)
	}
	prepared.mu.Lock()
	if prepared.closed || prepared.closing || !prepared.recoveryPending {
		prepared.mu.Unlock()
		return finishFailure(ErrProvenanceMissing)
	}
	prepared.recoveryPending = false
	prepared.mu.Unlock()
	c.mu.Lock()
	c.consumed, c.activating, c.prepared = true, false, nil
	c.mu.Unlock()
	return prepared, nil
}

func verifyPreparedRecoveryPins(store *sessionrepo.Store, prepared *PreparedSession,
	baseline, tip *sessionrepo.Generation, pins workflowrecovery.Pins, options sessionrepo.Options) error {
	if store == nil || prepared == nil || baseline == nil || tip == nil ||
		pins.WorkflowID == "" || pins.SessionID == "" || pins.RecoveryGeneration == 0 || pins.OwnerEpoch == 0 {
		return ErrProvenanceMissing
	}
	if err := verifyManagedStoreRecoveryPins(store, pins, options); err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	provenance := prepared.Provenance()
	provenanceDigest, err := canonicalDigest(provenance)
	if err != nil || workflowRecoveryDigest(pins.D06ProvenanceDigest) != workflowRecoveryDigest(provenanceDigest) ||
		provenance.SourceGeneration != baseline.ID() || provenance.SourceTreeDigest != baseline.TreeDigest() ||
		!bundleContainsGeneration(bundle, baseline) || pins.StoreBaselineGenerationID != baseline.ID() ||
		pins.StoreBaselineTreeDigest != workflowTreeDigest(baseline.TreeDigest()) ||
		pins.StoreTipGenerationID != tip.ID() || pins.StoreTipTreeDigest != workflowTreeDigest(tip.TreeDigest()) {
		return errors.Join(ErrProvenanceMissing, err)
	}
	identity, err := prepared.git.ReopenIdentity()
	if err != nil || workflowRecoveryDigest(pins.PrivateGitIdentityDigest) != workflowRecoveryDigest(identity.Digest()) {
		return errors.Join(ErrProvenanceMissing, err)
	}
	return nil
}

func verifyManagedStoreRecoveryPins(store *sessionrepo.Store, pins workflowrecovery.Pins, options sessionrepo.Options) error {
	if store == nil || options.Journal == nil || options.PolicyDigest == "" || options.MetadataPolicyDigest == "" {
		return ErrProvenanceMissing
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	storeTrace, err := options.Journal.Trace()
	if err != nil {
		return err
	}
	storeHead, storeCount := traceHead(storeTrace)
	admissionTrace, err := audit.Verify(bytes.NewReader(bundle.AdmissionJournal))
	if err != nil {
		return err
	}
	admissionHead, admissionCount := traceHead(admissionTrace)
	if bundle.PolicyDigest != options.PolicyDigest || bundle.MetadataPolicyDigest != options.MetadataPolicyDigest ||
		storeCount != pins.StoreJournalCount || workflowRecoveryDigest(storeHead) != workflowRecoveryDigest(pins.StoreJournalHead) ||
		admissionCount != pins.AdmissionJournalCount || workflowRecoveryDigest(admissionHead) != workflowRecoveryDigest(pins.AdmissionJournalHead) ||
		bundle.Baseline.ID != pins.StoreBaselineGenerationID ||
		workflowTreeDigest(bundle.Baseline.TreeDigest) != pins.StoreBaselineTreeDigest ||
		len(bundle.Generations) == 0 || bundle.Generations[len(bundle.Generations)-1].ID != pins.StoreTipGenerationID ||
		workflowTreeDigest(bundle.Generations[len(bundle.Generations)-1].TreeDigest) != pins.StoreTipTreeDigest {
		return ErrSourceEvidence
	}
	return nil
}

func traceHead(trace audit.Trace) (string, uint64) {
	if len(trace.Records) == 0 {
		return "", 0
	}
	last := trace.Records[len(trace.Records)-1]
	return last.Hash, last.Sequence
}

func workflowRecoveryDigest(value string) string {
	return strings.TrimPrefix(value, "sha256:")
}

func workflowTreeDigest(value string) string {
	return strings.TrimPrefix(value, delta.TreeDigestProfile)
}

// Close discards a not-yet-activated candidate. The verification-only
// privateGit.Close path closes descriptors and does not clean retained data.
func (c *RecoveryCandidate) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.activating {
		c.mu.Unlock()
		return errors.New("recovery candidate activation is in progress")
	}
	prepared := c.prepared
	c.prepared, c.consumed = nil, true
	c.mu.Unlock()
	if prepared != nil {
		return prepared.Close()
	}
	return nil
}

// WorkerView wraps a sessionrepo read-only exposure. Descriptor returns only a
// CLOEXEC O_PATH descriptor for that selected generation—not the Store root,
// generations directory, private Git parent, or Git admin directory.
type WorkerView struct {
	mu       sync.Mutex
	owner    *PreparedSession
	view     *sessionrepo.ExposedView
	source   *os.File
	evidence sessionrepo.GenerationEvidence
	closed   bool
}

type commandLease struct {
	leaseID string
	running bool
}

// DevelopmentCommandRunner is a concrete, measured adapter from the sealed
// sessionrepo command-view contract to the existing namespace/landlock/seccomp
// development sandbox. It always reports its non-claim-bearing evidence class;
// it is not a D09/D10 production profile or a cgroup delegation proof.
type DevelopmentCommandRunner struct {
	mu       sync.Mutex
	report   sandbox.Report
	limits   sandbox.Limits
	leases   map[string]commandLease
	receipts map[string]sessionrepo.CommandSettlement
}

func NewDevelopmentCommandRunner(limits sandbox.Limits) (*DevelopmentCommandRunner, error) {
	if limits.MemoryBytes < 0 || limits.PidsMax < 0 || limits.CPUWeight < 0 {
		return nil, ErrDevelopmentSandbox
	}
	report, err := sandbox.Probe()
	if err != nil {
		return nil, fmt.Errorf("probe development command sandbox: %w", err)
	}
	if err := requireDevelopmentSandbox(report); err != nil {
		return nil, err
	}
	if (limits.MemoryBytes != 0 || limits.PidsMax != 0 || limits.CPUWeight != 0) && !report.CgroupDelegated {
		return nil, fmt.Errorf("%w: requested resource limits require observed delegated cgroup-v2", ErrDevelopmentSandbox)
	}
	return &DevelopmentCommandRunner{report: report, limits: limits,
		leases: make(map[string]commandLease), receipts: make(map[string]sessionrepo.CommandSettlement)}, nil
}

func requireDevelopmentSandbox(report sandbox.Report) error {
	var missing []string
	if report.LandlockABI < 1 {
		missing = append(missing, "landlock-abi1")
	}
	if !report.SeccompBPF {
		missing = append(missing, "seccomp-bpf")
	}
	if !report.UserNamespaces {
		missing = append(missing, "user-namespace")
	}
	if !report.MountNamespace {
		missing = append(missing, "mount-namespace")
	}
	if !report.PidNamespace {
		missing = append(missing, "pid-namespace")
	}
	if !report.NetworkNamespace {
		missing = append(missing, "network-namespace")
	}
	if !report.Loopback {
		missing = append(missing, "loopback-interface")
	}
	if !report.NoNewPrivs {
		missing = append(missing, "no-new-privs")
	}
	if !report.CapabilitiesEmpty {
		missing = append(missing, "empty-capabilities")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrDevelopmentSandbox, strings.Join(missing, ","))
	}
	return nil
}

func (r *DevelopmentCommandRunner) Profile() string {
	if r == nil {
		return "dev-wsl-non-claim-bearing:unavailable"
	}
	return r.report.EvidenceClass()
}

// RegisterLease is called only from the concrete host operation authorizer
// after it has matched an ALLOW decision to providerbridge audit evidence and
// the exact current generation. The view ID is generated by sessionrepo before
// the runner receives the view; a model/caller cannot set it.
func (r *DevelopmentCommandRunner) RegisterLease(viewID, leaseID string) error {
	if r == nil || !validViewIdentity(viewID) || !validDomainIdentity(leaseID) {
		return ErrRunnerLease
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.leases[viewID]; exists || len(r.leases) >= maxDevelopmentRunnerLeases {
		return ErrRunnerLease
	}
	r.leases[viewID] = commandLease{leaseID: leaseID}
	return nil
}

func (r *DevelopmentCommandRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if r == nil || ctx == nil || view == nil || spec.Executable != "/bin/bash" || len(spec.Args) != 2 ||
		spec.Args[0] != "-lc" || strings.TrimSpace(spec.Args[1]) == "" {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("development runner accepts only the registered Bash argv shape")
	}
	viewID := view.ID()
	r.mu.Lock()
	lease, exists := r.leases[viewID]
	if !exists || lease.running {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrRunnerLease
	}
	lease.running = true
	r.leases[viewID] = lease
	r.mu.Unlock()

	root, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	result, settlement, launchErr := sandbox.Launch(ctx, sandbox.Request{
		Root: root, Argv: append([]string{spec.Executable}, spec.Args...),
		Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "LANG=C"},
		Dir: spec.WorkingDirectory, Limits: r.limits,
	})
	closeErr := root.Close()
	if launchErr != nil || closeErr != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(launchErr, closeErr)
	}
	commandResult := sessionrepo.CommandResult{ExitCode: result.ExitCode, ExitObserved: true,
		Stdout: append([]byte(nil), result.Stdout...), Stderr: append([]byte(nil), result.Stderr...)}
	commandSettlement := sessionrepo.CommandSettlement{
		LeaseID: lease.leaseID, ViewID: viewID,
		ProcessScopeEmpty: settlement.ProcessScopeEmpty, WritersStopped: settlement.WritersStopped,
		MountDetached: settlement.MountDetached, ExitObserved: settlement.ExitObserved,
		ExitCode: settlement.ExitCode, EvidenceClass: settlement.EvidenceClass,
	}
	if !commandSettlement.ExitObserved || !commandSettlement.ProcessScopeEmpty || !commandSettlement.WritersStopped || !commandSettlement.MountDetached {
		return sessionrepo.CommandResult{}, commandSettlement, sandbox.ErrNotSettled
	}
	r.mu.Lock()
	if _, duplicate := r.receipts[viewID]; duplicate {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrRunnerReceipt
	}
	r.receipts[viewID] = commandSettlement
	r.mu.Unlock()
	return commandResult, commandSettlement, nil
}

// VerifySettlement consumes the runner's one-use receipt. The booleans passed
// by sessionrepo are compared against the actual sandbox.Launch result retained
// by this runner; caller-created settlement structs do not establish trust.
func (r *DevelopmentCommandRunner) VerifySettlement(candidate sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if r == nil {
		return ErrRunnerReceipt
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actual, exists := r.receipts[viewID]
	if !exists || actual.LeaseID != leaseID || actual.ViewID != viewID || !reflect.DeepEqual(actual, candidate) ||
		!actual.ExitObserved || !actual.ProcessScopeEmpty || !actual.WritersStopped || !actual.MountDetached ||
		actual.EvidenceClass != r.report.EvidenceClass() || !strings.HasPrefix(actual.EvidenceClass, "dev-wsl-non-claim-bearing:") {
		return ErrRunnerReceipt
	}
	delete(r.receipts, viewID)
	delete(r.leases, viewID)
	return nil
}

// Prepare creates and persists the D06 binding before any worker exposure. It
// verifies the actual retained generation descriptor/stat, the Store's
// independently verified chain/bundle, exact on-disk audit bytes, scan policy,
// and tree manifest. privategit.Create then verifies the same descriptor before,
// during, and after the filtered ordinary byte copy. A second synced event
// records private Git's returned manifest/admin/executable provenance before
// this function yields a PreparedSession.
func Prepare(ctx context.Context, managed *ManagedStore, generation *sessionrepo.Generation,
	admin *AdminParent, git privategit.PinnedGit, gitProfile GitProfile,
) (*PreparedSession, error) {
	if ctx == nil || managed == nil || managed.store == nil || generation == nil || managed.journal == nil || admin == nil ||
		managed.profile.Limits.MaxObjects <= 0 || managed.profile.Limits.MaxDepth <= 0 || managed.profile.Limits.MaxFileBytes <= 0 ||
		managed.profile.Limits.MaxTotalBytes < managed.profile.Limits.MaxFileBytes || managed.profile.XattrVisibility.Authority != workspace.XattrAuthorityBoundedNoHiddenTrusted ||
		!validDigest(managed.profile.XattrVisibility.ProfileDigest) || !validDigest(gitProfile.ExecutableDigest) ||
		!validDomainIdentity(gitProfile.ExecutionBoundary) {
		return nil, ErrInvalidSource
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	if managed.closed {
		return nil, sessionrepo.ErrClosed
	}
	store, journal, profile := managed.store, managed.journal, managed.profile
	admin.mu.Lock()
	if admin.closed || admin.root == nil || admin.guard == nil || admin.recoveryName != "" || admin.prepared != 0 || admin.preparing {
		admin.mu.Unlock()
		return nil, ErrPrivateParent
	}
	adminStat, err := statNode(admin.root)
	guardStat, guardErr := statNode(admin.guard)
	if err != nil || guardErr != nil || admin.rootID != (nodeID{device: adminStat.device, inode: adminStat.inode}) ||
		admin.guardID != (nodeID{device: guardStat.device, inode: guardStat.inode}) {
		admin.mu.Unlock()
		return nil, errors.Join(ErrPrivateParent, err, guardErr)
	}
	admin.preparing = true
	admin.prepared++
	admin.mu.Unlock()
	keepAdminReservation := false
	defer func() {
		admin.mu.Lock()
		admin.preparing = false
		if !keepAdminReservation {
			admin.prepared--
		}
		admin.mu.Unlock()
	}()
	verified, err := store.Verify()
	if err != nil {
		return nil, fmt.Errorf("verify session store before D06 source binding: %w", err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return nil, fmt.Errorf("obtain retained store evidence before D06 source binding: %w", err)
	}
	if err := validateCurrentGeneration(bundle, verified, generation); err != nil {
		return nil, err
	}
	if err := validateJournalSnapshot(journal, bundle); err != nil {
		return nil, err
	}
	for _, effect := range mustAuditTrace(bundle.AuditJournal).Effects {
		if effect.Unresolved {
			return nil, fmt.Errorf("D06 source preparation refuses unresolved session effect %q", effect.ID)
		}
	}
	sourcePath, err := generation.ReadOnlyMountSource()
	if err != nil {
		return nil, fmt.Errorf("open retained sealed generation source descriptor: %w", err)
	}
	sourceRoot, err := openReadableDirAt(sourcePath)
	_ = sourcePath.Close()
	if err != nil {
		return nil, fmt.Errorf("open readable relative descriptor for sealed generation: %w", err)
	}
	closeSource := true
	defer func() {
		if closeSource {
			_ = sourceRoot.Close()
		}
	}()
	sourceStat, err := statNode(sourceRoot)
	if err != nil || sourceStat.mode&syscall.S_IFMT != syscall.S_IFDIR {
		return nil, errors.Join(ErrSourceEvidence, err)
	}
	if err := verifyGenerationDescriptor(store, generation, sourceRoot, sourceStat, bundle, profile); err != nil {
		return nil, err
	}
	bindingDigest, err := bindingDigest(bundle, generation, sourceStat, profile)
	if err != nil {
		return nil, err
	}
	provenance := Provenance{
		SchemaVersion: provenanceSchema, BindingDigest: bindingDigest,
		SourceGeneration: generation.ID(), SourceTreeDigest: generation.TreeDigest(),
		PolicyDigest: bundle.PolicyDigest, MetadataPolicyDigest: bundle.MetadataPolicyDigest,
		StoreAuditHead: bundle.AuditHeadHash, StoreAuditRecords: bundle.AuditRecordCount,
		SourceRootDevice: sourceStat.device, SourceRootInode: sourceStat.inode,
		XattrProfileDigest: profile.XattrVisibility.ProfileDigest, XattrAuthority: profile.XattrVisibility.Authority, Transfer: "workspace-import-ordinary-byte-copy",
		GitExecutableDigest: gitProfile.ExecutableDigest, GitExecutionBoundary: gitProfile.ExecutionBoundary,
		SourceRecord: bundle.AuditRecordCount + 1,
	}
	sourceEventData, err := json.Marshal(sourceBindingRecord{Provenance: provenance, SourceManifest: generation.Manifest()})
	if err != nil {
		return nil, err
	}
	sourceEventID := "sessionlaunch-source-" + strings.TrimPrefix(bindingDigest, "sha256:")
	sourceRecord, err := journal.Append(audit.Event{Kind: sourceBindingEventKind, ID: sourceEventID, Data: sourceEventData})
	if err != nil {
		return nil, fmt.Errorf("persist verified D06 source binding before copy: %w", err)
	}
	source := &verifiedSourceBinding{
		store: store, generation: generation, root: sourceRoot, identity: nodeID{device: sourceStat.device, inode: sourceStat.inode},
		profile: profile, bindingDigest: bindingDigest, baseBundle: bundle, sourceEvent: sourceRecord,
	}
	gitOptions := privategit.Options{Git: git, InitialBranch: "main", CleanupBoundary: cleanupBoundary(admin)}
	created, createErr := privategit.Create(ctx, admin.root, privategit.SourceDescriptor{
		Root: sourceRoot, Generation: generation.ID(), MetadataPolicyDigest: bundle.MetadataPolicyDigest,
		BindingDigest: bindingDigest, Limits: privateGitLimits(profile.Limits), Quiescent: true,
		XattrVisibility: privategit.XattrVisibilityAttestation{ProfileDigest: profile.XattrVisibility.ProfileDigest,
			Authority: privategit.XattrVisibilityAuthority(profile.XattrVisibility.Authority)},
		Binding: source,
	}, gitOptions)
	if createErr != nil {
		if errors.Is(createErr, privategit.ErrCleanupIncomplete) {
			keepAdminReservation = true
		}
		failureData, _ := json.Marshal(failedSnapshotRecord{provenanceSchema, bindingDigest, safeErrorClass(createErr)})
		_, auditErr := journal.Append(audit.Event{Kind: failedSnapshotEventKind, ID: sourceEventID + "-failed", Data: failureData})
		return nil, errors.Join(fmt.Errorf("create fresh private Git snapshot: %w", createErr), auditErr)
	}
	destroyCreated := func(cause error) error {
		cleanupErr := created.Destroy()
		if cleanupErr != nil {
			keepAdminReservation = true
		}
		return errors.Join(cause, cleanupErr)
	}
	provenance.PrivateGit = created.Provenance()
	provenance.PrivateGitDigest = provenance.PrivateGit.Digest()
	if provenance.PrivateGit.GitExecutableDigest != gitProfile.ExecutableDigest ||
		provenance.PrivateGit.GitExecutionBoundary != gitProfile.ExecutionBoundary {
		return nil, destroyCreated(errors.New("pinned Git executable or execution boundary differs from admitted profile"))
	}
	if err := created.Verify(); err != nil {
		failedData, _ := json.Marshal(failedSnapshotRecord{provenanceSchema, bindingDigest, safeErrorClass(err)})
		_, auditErr := journal.Append(audit.Event{Kind: failedSnapshotEventKind, ID: sourceEventID + "-verify-failed", Data: failedData})
		return nil, errors.Join(destroyCreated(fmt.Errorf("verify fresh private Git admin tree: %w", err)), auditErr)
	}
	traceBeforeReady, err := journal.Trace()
	if err != nil {
		return nil, errors.Join(err, created.Destroy())
	}
	if uint64(len(traceBeforeReady.Records)) != provenance.SourceRecord {
		return nil, errors.Join(errors.New("audit journal changed during private Git preparation"), created.Destroy())
	}
	provenance.PrivateGitRecord = uint64(len(traceBeforeReady.Records)) + 1
	readyData, err := json.Marshal(privateGitReadyRecord{provenanceSchema, provenance, created.Snapshot()})
	if err != nil {
		return nil, destroyCreated(err)
	}
	readyID := "sessionlaunch-privategit-" + strings.TrimPrefix(provenance.PrivateGitDigest, "sha256:")
	readyRecord, err := journal.Append(audit.Event{Kind: privateGitEventKind, ID: readyID, Data: readyData})
	if err != nil {
		return nil, destroyCreated(fmt.Errorf("persist private Git provenance before worker exposure: %w", err))
	}
	if sourceRecord.Sequence != provenance.SourceRecord || readyRecord.Sequence != provenance.PrivateGitRecord {
		return nil, destroyCreated(errors.New("D06 provenance journal sequence changed while appending"))
	}
	if err := verifyProvenancePrefix(store, bundle, sourceRecord, sourceEventData, readyRecord, readyData); err != nil {
		return nil, destroyCreated(err)
	}
	keepAdminReservation = true
	managed.prepared++
	return &PreparedSession{
		managed: managed, store: store, generation: generation, journal: journal, admin: admin, git: created,
		destroyGit: true,
		profile:    profile, provenance: provenance,
		basePrefix: appendRecordBytes(appendRecordBytes(append([]byte(nil), bundle.AuditJournal...), sourceRecord), readyRecord),
		views:      make(map[*WorkerView]struct{}),
		sources:    make(map[*VerifiedD09ExposureSource]struct{}),
	}, nil
}

// Rebind verifies the original D06 records and attaches the retained
// verification-only private Git handle to an already-opened Store. It does not
// create new D06 records or produce worker authority by itself. The caller must
// still complete transcript cross-linking, owner fencing, cumulative budget
// reservation, and fresh-session admission before exposing any operation.
func Rebind(managed *ManagedStore, admin *AdminParent, pin RecoveryGitPin) (*RecoveryCandidate, error) {
	if managed == nil || admin == nil || !validPrivateGitName(pin.RepositoryName) ||
		!validDigest(pin.ProvenanceDigest) || !validDigest(pin.RepositoryIdentityDigest) || pin.VerifyIdentity == nil {
		return nil, ErrProvenanceMissing
	}
	managed.mu.Lock()
	if managed.closed || managed.store == nil || managed.journal == nil {
		managed.mu.Unlock()
		return nil, sessionrepo.ErrClosed
	}
	store, journal, profile := managed.store, managed.journal, managed.profile
	managed.mu.Unlock()
	if !store.IsRecoveryPreview() {
		return nil, ErrRecoveryActivationRequired
	}

	admin.mu.Lock()
	if admin.closed || admin.root == nil || admin.guard == nil || admin.recoveryName != pin.RepositoryName ||
		admin.prepared != 0 || admin.preparing {
		admin.mu.Unlock()
		return nil, ErrPrivateParent
	}
	adminStat, err := statNode(admin.root)
	guardStat, guardErr := statNode(admin.guard)
	if err != nil || guardErr != nil || admin.rootID != (nodeID{device: adminStat.device, inode: adminStat.inode}) ||
		admin.guardID != (nodeID{device: guardStat.device, inode: guardStat.inode}) {
		admin.mu.Unlock()
		return nil, errors.Join(ErrPrivateParent, err, guardErr)
	}
	admin.preparing = true
	admin.prepared++
	admin.mu.Unlock()
	keepAdminReservation := false
	defer func() {
		admin.mu.Lock()
		admin.preparing = false
		if !keepAdminReservation {
			admin.prepared--
		}
		admin.mu.Unlock()
	}()

	if profile.Limits.MaxObjects <= 0 || profile.Limits.MaxDepth <= 0 || profile.Limits.MaxFileBytes <= 0 ||
		profile.Limits.MaxTotalBytes < profile.Limits.MaxFileBytes || profile.XattrVisibility.Authority != workspace.XattrAuthorityBoundedNoHiddenTrusted ||
		!validDigest(profile.XattrVisibility.ProfileDigest) {
		return nil, ErrProvenanceMissing
	}
	verified, err := store.Verify()
	if err != nil {
		return nil, fmt.Errorf("verify retained Store before D06 rebind: %w", err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return nil, fmt.Errorf("obtain retained Store evidence before D06 rebind: %w", err)
	}
	if !reflect.DeepEqual(verified, bundle.ValidatedChain) {
		return nil, ErrSourceEvidence
	}
	if err := validateJournalSnapshot(journal, bundle); err != nil {
		return nil, err
	}
	trace := mustAuditTrace(bundle.AuditJournal)
	for _, effect := range trace.Effects {
		if effect.Unresolved {
			return nil, fmt.Errorf("D06 rebind refuses unresolved session effect %q", effect.ID)
		}
	}
	baseline := store.BaselineGeneration()
	if baseline == nil || !bundleContainsGeneration(bundle, baseline) {
		return nil, sessionrepo.ErrNoBaseline
	}
	provenance, snapshot, err := retainedD06Evidence(trace.Records)
	if err != nil {
		return nil, err
	}
	provenanceDigest, err := canonicalDigest(provenance)
	if err != nil || provenanceDigest != pin.ProvenanceDigest ||
		provenance.PrivateGitDigest != provenance.PrivateGit.Digest() ||
		provenance.SourceGeneration != baseline.ID() || provenance.SourceTreeDigest != baseline.TreeDigest() ||
		provenance.PolicyDigest != bundle.PolicyDigest || provenance.MetadataPolicyDigest != bundle.MetadataPolicyDigest ||
		provenance.XattrProfileDigest != profile.XattrVisibility.ProfileDigest ||
		provenance.XattrAuthority != profile.XattrVisibility.Authority ||
		provenance.GitExecutableDigest != provenance.PrivateGit.GitExecutableDigest ||
		provenance.GitExecutionBoundary != provenance.PrivateGit.GitExecutionBoundary {
		return nil, errors.Join(ErrProvenanceMissing, err)
	}
	if err := verifyPersistedD06Records(bundle.AuditJournal, provenance); err != nil {
		return nil, err
	}

	admin.mu.Lock()
	if admin.closed || admin.root == nil || admin.guard == nil || admin.recoveryName != pin.RepositoryName {
		admin.mu.Unlock()
		return nil, ErrPrivateParent
	}
	parentStat, parentErr := statNode(admin.root)
	if parentErr != nil || parentStat.mode&syscall.S_IFMT != syscall.S_IFDIR {
		admin.mu.Unlock()
		return nil, errors.Join(ErrPrivateParent, parentErr)
	}
	parentIdentity := privategit.DirectoryIdentity{Device: parentStat.device, Inode: parentStat.inode}
	verifyOwner := func(parent *os.File, expected privategit.DirectoryIdentity) error {
		admin.mu.Lock()
		defer admin.mu.Unlock()
		if admin.closed || admin.root == nil || admin.guard == nil || admin.prepared != 1 ||
			admin.recoveryName != pin.RepositoryName || expected != parentIdentity {
			return ErrPrivateParent
		}
		rootStat, rootErr := statNode(admin.root)
		candidateStat, candidateErr := statNode(parent)
		guardStat, guardErr := statNode(admin.guard)
		if rootErr != nil || candidateErr != nil || guardErr != nil ||
			candidateStat.device != rootStat.device || candidateStat.inode != rootStat.inode ||
			(parentStat.device != rootStat.device || parentStat.inode != rootStat.inode) ||
			admin.rootID != (nodeID{device: rootStat.device, inode: rootStat.inode}) ||
			admin.guardID != (nodeID{device: guardStat.device, inode: guardStat.inode}) {
			return errors.Join(ErrPrivateParent, rootErr, candidateErr, guardErr)
		}
		return requireRecoveredAdminParent(admin.root, admin.guard, ".tbound-sessionlaunch.lock", pin.RepositoryName)
	}
	admin.mu.Unlock()

	git, err := privategit.Reopen(admin.root, privategit.ReopenApproval{
		Name: pin.RepositoryName, ParentIdentity: parentIdentity, Provenance: provenance.PrivateGit,
		Snapshot: snapshot, ExpectedIdentityDigest: pin.RepositoryIdentityDigest,
		TrustedVerifier: func(identity privategit.RepositoryIdentity) error {
			if identity.Digest() != pin.RepositoryIdentityDigest {
				return privategit.ErrReopenBinding
			}
			return pin.VerifyIdentity(identity)
		},
		VerifyExclusiveParent: verifyOwner,
	})
	if err != nil {
		return nil, fmt.Errorf("verify pinned retained private Git snapshot: %w", err)
	}
	if err := git.Verify(); err != nil {
		_ = git.Close()
		return nil, fmt.Errorf("revalidate retained private Git snapshot: %w", err)
	}
	if err := verifyPersistedD06Records(bundle.AuditJournal, provenance); err != nil {
		_ = git.Close()
		return nil, err
	}
	basePrefix, err := retainedD06Prefix(trace.Records, provenance)
	if err != nil {
		_ = git.Close()
		return nil, err
	}
	managed.mu.Lock()
	if managed.closed || managed.store != store || managed.journal != journal {
		managed.mu.Unlock()
		_ = git.Close()
		return nil, errors.New("managed Store changed during D06 recovery rebind")
	}
	managed.prepared++
	managed.mu.Unlock()
	keepAdminReservation = true
	prepared := &PreparedSession{
		managed: managed, store: store, generation: baseline, journal: journal, admin: admin, git: git,
		profile: profile, provenance: provenance, basePrefix: basePrefix, recoveryPending: true,
		views: make(map[*WorkerView]struct{}), sources: make(map[*VerifiedD09ExposureSource]struct{}),
	}
	return &RecoveryCandidate{managed: managed, prepared: prepared}, nil
}

type privateGitReadyRecord struct {
	SchemaVersion string              `json:"schema_version"`
	Provenance    Provenance          `json:"provenance"`
	Snapshot      privategit.Snapshot `json:"snapshot"`
}

type failedSnapshotRecord struct {
	SchemaVersion string `json:"schema_version"`
	BindingDigest string `json:"binding_digest"`
	ErrorClass    string `json:"error_class"`
}

type sourceBindingRecord struct {
	Provenance     Provenance         `json:"provenance"`
	SourceManifest delta.TreeManifest `json:"source_manifest"`
}

type verifiedSourceBinding struct {
	store         *sessionrepo.Store
	generation    *sessionrepo.Generation
	root          *os.File
	identity      nodeID
	profile       SourceProfile
	bindingDigest string
	baseBundle    sessionrepo.EvidenceBundle
	sourceEvent   audit.Record
}

func (b *verifiedSourceBinding) VerifySource(root *os.File, generation, metadataPolicyDigest, xattrProfileDigest, bindingDigest string) error {
	if b == nil || root == nil || b.store == nil || b.generation == nil || b.root == nil ||
		root.Fd() != b.root.Fd() || generation != b.generation.ID() || metadataPolicyDigest != b.baseBundle.MetadataPolicyDigest ||
		xattrProfileDigest != b.profile.XattrVisibility.ProfileDigest || bindingDigest != b.bindingDigest {
		return ErrSourceEvidence
	}
	stat, err := statNode(root)
	if err != nil || (nodeID{device: stat.device, inode: stat.inode}) != b.identity {
		return errors.Join(ErrSourceChanged, err)
	}
	chain, err := b.store.Verify()
	if err != nil {
		return fmt.Errorf("reverify durable session chain during source copy: %w", err)
	}
	bundle, err := b.store.EvidenceBundle()
	if err != nil {
		return err
	}
	if err := validateCurrentGeneration(bundle, chain, b.generation); err != nil {
		return err
	}
	if err := verifySourceEventOnlyAppend(b.baseBundle.AuditJournal, bundle.AuditJournal, b.sourceEvent); err != nil {
		return err
	}
	return verifyGenerationDescriptor(b.store, b.generation, root, stat, b.baseBundle, b.profile)
}

// ExposeReadOnly is the only worker-view creation API. It first rechecks the
// source, store chain, private Git repository and durable provenance records.
// The returned descriptor is pinned to a sealed worker generation view and is
// never the session Store root or private Git admin/root.
func (p *PreparedSession) ExposeReadOnly(generation *sessionrepo.Generation, mode sessionrepo.ViewMode) (*WorkerView, error) {
	if p == nil || generation == nil {
		return nil, ErrWorkerExposure
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.closing || p.recoveryPending || p.git == nil || p.admin == nil || p.admin.root == nil {
		return nil, ErrWorkerExposure
	}
	if err := p.git.Verify(); err != nil {
		return nil, fmt.Errorf("verify supervisor-private Git admin before exposure: %w", err)
	}
	verified, err := p.store.Verify()
	if err != nil {
		return nil, err
	}
	bundle, err := p.store.EvidenceBundle()
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(verified, bundle.ValidatedChain) || !bundleContainsGeneration(bundle, generation) || containsGitMetadata(generation.Manifest()) {
		return nil, ErrWorkerExposure
	}
	if err := validateJournalSnapshot(p.journal, bundle); err != nil {
		return nil, err
	}
	if err := verifyPersistedD06Records(bundle.AuditJournal, p.provenance); err != nil {
		return nil, err
	}
	view, err := generation.Expose(mode)
	if err != nil {
		return nil, fmt.Errorf("create sealed read-only worker exposure: %w", err)
	}
	source, err := view.ReadOnlyMountSource()
	if err != nil {
		return nil, errors.Join(err, view.Close())
	}
	stat, err := statNode(source)
	if err != nil {
		return nil, errors.Join(err, source.Close(), view.Close())
	}
	observed := view.Observed()
	if stat.mode&syscall.S_IFMT != syscall.S_IFDIR || observed.ID != generation.ID() || observed.TreeDigest != generation.TreeDigest() ||
		!reflect.DeepEqual(observed.Manifest, generation.Manifest()) {
		return nil, errors.Join(ErrWorkerExposure, source.Close(), view.Close())
	}
	viewResult := &WorkerView{owner: p, view: view, source: source, evidence: sessionrepo.GenerationEvidence{
		ID: generation.ID(), TreeDigest: generation.TreeDigest(), Manifest: generation.Manifest(),
	}}
	p.views[viewResult] = struct{}{}
	return viewResult, nil
}

func (v *WorkerView) Descriptor() (*os.File, sessionrepo.GenerationEvidence, error) {
	if v == nil {
		return nil, sessionrepo.GenerationEvidence{}, ErrWorkerExposure
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed || v.source == nil {
		return nil, sessionrepo.GenerationEvidence{}, ErrWorkerExposure
	}
	fd, err := syscall.Openat(int(v.source.Fd()), ".", linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, sessionrepo.GenerationEvidence{}, err
	}
	return os.NewFile(uintptr(fd), "sealed-worker-generation"), cloneGenerationEvidence(v.evidence), nil
}

func (v *WorkerView) Close() error {
	if v == nil {
		return nil
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	source, view := v.source, v.view
	v.mu.Unlock()
	var errs []error
	if source != nil {
		if err := source.Close(); err != nil {
			errs = append(errs, err)
		} else {
			v.mu.Lock()
			v.source = nil
			v.mu.Unlock()
		}
	}
	if view != nil {
		if err := view.Close(); err != nil {
			errs = append(errs, err)
		} else {
			v.mu.Lock()
			v.view = nil
			v.mu.Unlock()
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	v.mu.Lock()
	v.closed = true
	v.source, v.view = nil, nil
	owner := v.owner
	v.owner = nil
	v.mu.Unlock()
	if owner != nil {
		owner.mu.Lock()
		delete(owner.views, v)
		owner.mu.Unlock()
	}
	return nil
}

func (p *PreparedSession) Provenance() Provenance {
	if p == nil {
		return Provenance{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := p.provenance
	copy.PrivateGit.SourceManifest.Objects = append([]delta.ManifestObject(nil), p.provenance.PrivateGit.SourceManifest.Objects...)
	return copy
}

// RecoveryIdentity returns the canonical D06 provenance digest, the current
// verified private-Git repository identity digest, and the retained repository
// name. These values let the host checkpoint bind the exact prepared source
// before worker exposure; callers cannot derive them from the worker or a
// client-supplied path.
func (p *PreparedSession) RecoveryIdentity() (provenanceDigest, repositoryIdentityDigest, repositoryName string, err error) {
	if p == nil {
		return "", "", "", ErrProvenanceMissing
	}
	p.mu.Lock()
	if p.closed || p.closing || p.git == nil {
		p.mu.Unlock()
		return "", "", "", ErrProvenanceMissing
	}
	provenance := p.provenance
	provenance.PrivateGit.SourceManifest.Objects = append([]delta.ManifestObject(nil), p.provenance.PrivateGit.SourceManifest.Objects...)
	git := p.git
	p.mu.Unlock()
	provenanceDigest, err = canonicalDigest(provenance)
	if err != nil {
		return "", "", "", errors.Join(ErrProvenanceMissing, err)
	}
	identity, err := git.ReopenIdentity()
	if err != nil || identity.Digest() == "" || provenance.PrivateGit.RepositoryName == "" {
		return "", "", "", errors.Join(ErrProvenanceMissing, err)
	}
	return provenanceDigest, identity.Digest(), provenance.PrivateGit.RepositoryName, nil
}

// VerifySourceBinding proves this PreparedSession belongs to the exact
// sessionrepo Store, retained Generation and audit Journal supplied by the
// composition root. It re-verifies the complete chain, journal bytes, D06
// events and private Git admin identity; matching generation-label strings are
// insufficient.
func (p *PreparedSession) VerifySourceBinding(store *sessionrepo.Store, generation *sessionrepo.Generation, journal *audit.Journal) error {
	if p == nil || store == nil || generation == nil || journal == nil {
		return ErrProvenanceMissing
	}
	p.mu.Lock()
	if p.closed || p.closing || p.store != store || p.generation != generation || p.journal != journal || p.git == nil {
		p.mu.Unlock()
		return ErrProvenanceMissing
	}
	provenance := p.provenance
	privateGit := p.git
	p.mu.Unlock()
	verified, err := store.Verify()
	if err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(verified, bundle.ValidatedChain) || !bundleContainsGeneration(bundle, generation) ||
		bundle.PolicyDigest != provenance.PolicyDigest || bundle.MetadataPolicyDigest != provenance.MetadataPolicyDigest ||
		generation.ID() != provenance.SourceGeneration ||
		generation.TreeDigest() != provenance.SourceTreeDigest {
		return ErrSourceEvidence
	}
	if err := validateJournalSnapshot(journal, bundle); err != nil {
		return err
	}
	if err := verifyPersistedD06Records(bundle.AuditJournal, provenance); err != nil {
		return err
	}
	return privateGit.Verify()
}

// VerifyDurableProvenance is safe to call from sessionrepo's operation/decision
// callbacks, which are invoked while Store.mu is held. It verifies the shared
// append-only journal tail and private Git identity without re-entering Store
// verification and creating a self-deadlock.
func (p *PreparedSession) VerifyDurableProvenance() error {
	if p == nil {
		return ErrProvenanceMissing
	}
	p.mu.Lock()
	if p.closed || p.closing || p.journal == nil || p.git == nil {
		p.mu.Unlock()
		return ErrProvenanceMissing
	}
	journal, git, prefix, provenance := p.journal, p.git, append([]byte(nil), p.basePrefix...), p.provenance
	p.mu.Unlock()
	trace, err := journal.Trace()
	if err != nil {
		return err
	}
	encoded, err := marshalAuditRecords(trace.Records)
	if err != nil || !bytes.HasPrefix(encoded, prefix) {
		return errors.Join(ErrProvenanceMissing, err)
	}
	if err := verifyPersistedD06Records(encoded, provenance); err != nil {
		return err
	}
	return git.Verify()
}

// Close withdraws every worker view before destroying the supervisor-private
// Git tree. It never cleans an arbitrary caller path; privategit performs
// descriptor-relative cleanup under the retained exclusive admin parent.
func (p *PreparedSession) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed || p.closing {
		p.mu.Unlock()
		return errors.New("prepared session cleanup is already in progress")
	}
	p.closing = true
	views := make([]*WorkerView, 0, len(p.views))
	for view := range p.views {
		views = append(views, view)
	}
	sources := make([]*VerifiedD09ExposureSource, 0, len(p.sources))
	for source := range p.sources {
		sources = append(sources, source)
	}
	p.mu.Unlock()
	var errs []error
	for _, view := range views {
		if err := view.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, source := range sources {
		if err := source.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if closeErr := errors.Join(errs...); closeErr != nil {
		p.mu.Lock()
		p.closing = false
		p.mu.Unlock()
		return closeErr
	}
	p.mu.Lock()
	git, admin, managed, destroyGit := p.git, p.admin, p.managed, p.destroyGit
	p.mu.Unlock()
	if git != nil {
		var err error
		if destroyGit {
			destroyer, ok := git.(privateGitDestroyer)
			if !ok {
				p.mu.Lock()
				p.closing = false
				p.mu.Unlock()
				return errors.New("owned private Git repository has no bounded destroy operation")
			}
			err = destroyer.Destroy()
		} else {
			err = git.Close()
		}
		if err != nil {
			p.mu.Lock()
			p.closing = false
			p.mu.Unlock()
			return err
		}
	}
	p.mu.Lock()
	p.closed = true
	p.closing = false
	p.git, p.admin, p.managed = nil, nil, nil
	p.destroyGit = false
	p.mu.Unlock()
	if admin != nil {
		admin.mu.Lock()
		admin.prepared--
		admin.mu.Unlock()
	}
	if managed != nil {
		managed.mu.Lock()
		managed.prepared--
		managed.mu.Unlock()
	}
	return errors.Join(errs...)
}

func cleanupBoundary(admin *AdminParent) privategit.CleanupBoundary {
	return privategit.CleanupBoundary{ExclusiveParent: true,
		Description: fmt.Sprintf("sessionlaunch-held-flock-parent-dev=%d-ino=%d-guard-dev=%d-ino=%d",
			admin.rootID.device, admin.rootID.inode, admin.guardID.device, admin.guardID.inode)}
}

func verifyGenerationDescriptor(store *sessionrepo.Store, generation *sessionrepo.Generation, root *os.File,
	identity nodeStat, bundle sessionrepo.EvidenceBundle, profile SourceProfile) error {
	if store == nil || generation == nil || root == nil || generation.ID() == "" ||
		bundle.MetadataPolicyDigest == "" || bundle.PolicyDigest == "" {
		return ErrSourceEvidence
	}
	retained, err := generation.ReadOnlyMountSource()
	if err != nil {
		return err
	}
	retainedStat, statErr := statNode(retained)
	_ = retained.Close()
	if statErr != nil || (nodeID{device: retainedStat.device, inode: retainedStat.inode}) != (nodeID{device: identity.device, inode: identity.inode}) {
		return errors.Join(ErrSourceChanged, statErr)
	}
	manifest := generation.Manifest()
	if manifest.Generation != generation.ID() || manifest.MetadataPolicyDigest != bundle.MetadataPolicyDigest {
		return ErrSourceEvidence
	}
	if containsGitMetadata(manifest) {
		return fmt.Errorf("%w: retained session generation contains live Git administration metadata", privategit.ErrSourceContainsGit)
	}
	snapshot, err := workspace.Scan(root, workspace.Options{
		Generation: generation.ID(), MetadataPolicyDigest: bundle.MetadataPolicyDigest,
		Limits: profile.Limits, QuiescentRoot: true, XattrVisibility: profile.XattrVisibility,
		ImportPolicy: workspace.ImportPolicy{ReservedMetadata: workspace.ReservedMetadataRejectLiveGit},
	})
	if err != nil || snapshot.TreeDigest != generation.TreeDigest() || !reflect.DeepEqual(snapshot.Manifest, manifest) {
		return errors.Join(ErrSourceChanged, err)
	}
	if !bundleContainsGeneration(bundle, generation) {
		return ErrSourceEvidence
	}
	return nil
}

func validateCurrentGeneration(bundle sessionrepo.EvidenceBundle, chain delta.Result, generation *sessionrepo.Generation) error {
	if generation == nil || len(bundle.Generations) == 0 || bundle.SchemaVersion == "" ||
		!reflect.DeepEqual(chain, bundle.ValidatedChain) || chain.SealedGeneration != generation.ID() || chain.SealedTreeDigest != generation.TreeDigest() {
		return ErrSourceEvidence
	}
	last := bundle.Generations[len(bundle.Generations)-1]
	if last.ID != generation.ID() || last.TreeDigest != generation.TreeDigest() || !reflect.DeepEqual(last.Manifest, generation.Manifest()) {
		return ErrSourceEvidence
	}
	return nil
}

func bundleContainsGeneration(bundle sessionrepo.EvidenceBundle, generation *sessionrepo.Generation) bool {
	if generation == nil {
		return false
	}
	for _, item := range bundle.Generations {
		if item.ID == generation.ID() && item.TreeDigest == generation.TreeDigest() && reflect.DeepEqual(item.Manifest, generation.Manifest()) {
			return true
		}
	}
	return false
}

func validateJournalSnapshot(journal *audit.Journal, bundle sessionrepo.EvidenceBundle) error {
	if journal == nil || len(bundle.AuditJournal) == 0 {
		return ErrAuditJournalMismatch
	}
	trace, err := journal.Trace()
	if err != nil {
		return err
	}
	encoded, err := marshalAuditRecords(trace.Records)
	if err != nil {
		return err
	}
	bundleTrace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, bundle.AuditJournal) || uint64(len(trace.Records)) != bundle.AuditRecordCount ||
		!reflect.DeepEqual(trace, bundleTrace) || auditHead(trace.Records) != bundle.AuditHeadHash {
		return ErrAuditJournalMismatch
	}
	return nil
}

func verifyProvenancePrefix(store *sessionrepo.Store, base sessionrepo.EvidenceBundle, sourceRecord audit.Record, sourceData []byte, readyRecord audit.Record, readyData []byte) error {
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	expected := appendRecordBytes(appendRecordBytes(append([]byte(nil), base.AuditJournal...), sourceRecord), readyRecord)
	if !bytes.Equal(bundle.AuditJournal, expected) ||
		bundle.AuditRecordCount != base.AuditRecordCount+2 || sourceRecord.Event.Kind != sourceBindingEventKind ||
		sourceRecord.Event.Data == nil || !bytes.Equal(sourceRecord.Event.Data, sourceData) ||
		readyRecord.Event.Kind != privateGitEventKind || !bytes.Equal(readyRecord.Event.Data, readyData) {
		return ErrProvenanceMissing
	}
	return nil
}

func verifySourceEventOnlyAppend(base, current []byte, sourceRecord audit.Record) error {
	expected := appendRecordBytes(append([]byte(nil), base...), sourceRecord)
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("%w: audit journal changed during source byte copy", ErrSourceChanged)
	}
	return nil
}

// requireProvenanceVersion refuses a D06 journal record whose schema version is
// not the current provenanceSchema, before any other field is read. An upgrade
// that changes the provenance format requires draining retained sessions first:
// older records are refused as an unsupported version, never migrated, and an
// absent xattr_authority is never inferred as bounded. The version is read from
// the record envelope (private-Git-ready) and from the embedded provenance
// (both record kinds); any version present must be the current one.
func requireProvenanceVersion(data []byte) error {
	var probe struct {
		SchemaVersion string `json:"schema_version"`
		Provenance    struct {
			SchemaVersion string `json:"schema_version"`
		} `json:"provenance"`
	}
	if json.Unmarshal(data, &probe) != nil {
		return ErrProvenanceMissing
	}
	envelope, embedded := probe.SchemaVersion, probe.Provenance.SchemaVersion
	if embedded != provenanceSchema || (envelope != "" && envelope != provenanceSchema) {
		version := embedded
		if version == provenanceSchema {
			version = envelope
		}
		return errors.Join(ErrProvenanceMissing, fmt.Errorf("%w: record version %q, supported %q",
			ErrUnsupportedProvenanceVersion, version, provenanceSchema))
	}
	return nil
}

// verifyPersistedD06Records checks only current-version D06 records; an upgrade
// requires draining retained sessions first.
func verifyPersistedD06Records(journalBytes []byte, provenance Provenance) error {
	trace, err := audit.Verify(bytes.NewReader(journalBytes))
	if err != nil {
		return err
	}
	if provenance.SourceRecord == 0 || provenance.PrivateGitRecord != provenance.SourceRecord+1 ||
		provenance.StoreAuditRecords+1 != provenance.SourceRecord {
		return ErrProvenanceMissing
	}
	var sourceSeen, gitSeen bool
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case sourceBindingEventKind:
			if err := requireProvenanceVersion(record.Event.Data); err != nil {
				return err
			}
			var source sourceBindingRecord
			if unmarshalNoUnknown(record.Event.Data, &source) != nil || record.Sequence != provenance.SourceRecord ||
				source.Provenance.StoreAuditRecords != provenance.StoreAuditRecords ||
				source.Provenance.StoreAuditHead != provenance.StoreAuditHead ||
				source.Provenance.BindingDigest != provenance.BindingDigest || source.Provenance.SourceGeneration != provenance.SourceGeneration ||
				source.Provenance.SourceTreeDigest != provenance.SourceTreeDigest || source.Provenance.PrivateGitDigest != "" ||
				!reflect.DeepEqual(source.SourceManifest, provenance.PrivateGit.SourceManifest) {
				return ErrProvenanceMissing
			}
			if provenance.StoreAuditRecords > uint64(len(trace.Records)) ||
				(provenance.StoreAuditRecords == 0 && provenance.StoreAuditHead != "") ||
				(provenance.StoreAuditRecords > 0 && trace.Records[provenance.StoreAuditRecords-1].Hash != provenance.StoreAuditHead) {
				return ErrProvenanceMissing
			}
			sourceSeen = true
		case privateGitEventKind:
			if err := requireProvenanceVersion(record.Event.Data); err != nil {
				return err
			}
			var event privateGitReadyRecord
			if unmarshalNoUnknown(record.Event.Data, &event) != nil || record.Sequence != provenance.PrivateGitRecord ||
				event.SchemaVersion != provenanceSchema || !reflect.DeepEqual(event.Provenance, provenance) ||
				event.Provenance.PrivateGitDigest != provenance.PrivateGitDigest || event.Provenance.BindingDigest != provenance.BindingDigest ||
				event.Snapshot.TreeDigest != provenance.SourceTreeDigest || !reflect.DeepEqual(event.Snapshot.Manifest, provenance.PrivateGit.SourceManifest) {
				return ErrProvenanceMissing
			}
			gitSeen = true
		}
	}
	if !sourceSeen || !gitSeen {
		return ErrProvenanceMissing
	}
	return nil
}

// retainedD06Evidence reads only current-version D06 records; an upgrade requires
// draining retained sessions first.
func retainedD06Evidence(records []audit.Record) (Provenance, privategit.Snapshot, error) {
	var provenance Provenance
	var snapshot privategit.Snapshot
	var sourceCount, readyCount int
	for _, record := range records {
		switch record.Event.Kind {
		case sourceBindingEventKind:
			if err := requireProvenanceVersion(record.Event.Data); err != nil {
				return Provenance{}, privategit.Snapshot{}, err
			}
			sourceCount++
		case privateGitEventKind:
			if err := requireProvenanceVersion(record.Event.Data); err != nil {
				return Provenance{}, privategit.Snapshot{}, err
			}
			readyCount++
			var ready privateGitReadyRecord
			if unmarshalNoUnknown(record.Event.Data, &ready) != nil || ready.SchemaVersion != provenanceSchema {
				return Provenance{}, privategit.Snapshot{}, ErrProvenanceMissing
			}
			provenance, snapshot = ready.Provenance, ready.Snapshot
		}
	}
	if sourceCount != 1 || readyCount != 1 || provenance.SchemaVersion != provenanceSchema ||
		provenance.PrivateGitRecord == 0 || snapshot.TreeDigest == "" ||
		!reflect.DeepEqual(snapshot.Manifest, provenance.PrivateGit.SourceManifest) ||
		snapshot.TreeDigest != provenance.SourceTreeDigest {
		return Provenance{}, privategit.Snapshot{}, ErrProvenanceMissing
	}
	return provenance, snapshot, nil
}

func retainedD06Prefix(records []audit.Record, provenance Provenance) ([]byte, error) {
	if provenance.PrivateGitRecord == 0 || provenance.PrivateGitRecord > uint64(len(records)) {
		return nil, ErrProvenanceMissing
	}
	var prefix []byte
	for index := uint64(0); index < provenance.PrivateGitRecord; index++ {
		prefix = appendRecordBytes(prefix, records[index])
	}
	return prefix, nil
}

func bindingDigest(bundle sessionrepo.EvidenceBundle, generation *sessionrepo.Generation, stat nodeStat, profile SourceProfile) (string, error) {
	identity := struct {
		Domain     string           `json:"domain"`
		Session    string           `json:"session_evidence_digest"`
		Generation string           `json:"generation"`
		TreeDigest string           `json:"tree_digest"`
		Policy     string           `json:"policy_digest"`
		Metadata   string           `json:"metadata_policy_digest"`
		AuditHead  string           `json:"audit_head_hash"`
		Device     uint64           `json:"device"`
		Inode      uint64           `json:"inode"`
		Xattr      string           `json:"xattr_profile_digest"`
		Authority  string           `json:"xattr_authority"`
		Limits     workspace.Limits `json:"limits"`
	}{sourceBindingDomain, DigestBytes(bundle.AuditJournal), generation.ID(), generation.TreeDigest(),
		bundle.PolicyDigest, bundle.MetadataPolicyDigest, bundle.AuditHeadHash, stat.device, stat.inode,
		profile.XattrVisibility.ProfileDigest, string(profile.XattrVisibility.Authority), profile.Limits}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return DigestBytes(encoded), nil
}

func appendRecordBytes(prefix []byte, record audit.Record) []byte {
	encoded, _ := json.Marshal(record)
	prefix = append(prefix, encoded...)
	return append(prefix, '\n')
}

func marshalAuditRecords(records []audit.Record) ([]byte, error) {
	var output bytes.Buffer
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func auditHead(records []audit.Record) string {
	if len(records) == 0 {
		return ""
	}
	return records[len(records)-1].Hash
}

func mustAuditTrace(journal []byte) audit.Trace {
	trace, _ := audit.Verify(bytes.NewReader(journal))
	return trace
}

func privateGitLimits(limits workspace.Limits) privategit.Limits {
	return privategit.Limits{MaxObjects: limits.MaxObjects, MaxDepth: limits.MaxDepth, MaxFileBytes: limits.MaxFileBytes, MaxTotalBytes: limits.MaxTotalBytes}
}

type nodeStat struct {
	device uint64
	inode  uint64
	links  uint64
	uid    uint32
	gid    uint32
	mode   uint32
}

func statNode(file *os.File) (nodeStat, error) {
	if file == nil {
		return nodeStat{}, ErrInvalidSource
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return nodeStat{}, err
	}
	return nodeStat{device: uint64(stat.Dev), inode: uint64(stat.Ino), links: uint64(stat.Nlink), uid: stat.Uid, gid: stat.Gid, mode: stat.Mode}, nil
}

func duplicateFile(file *os.File, name string) (*os.File, error) {
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

func openReadableDirAt(path *os.File) (*os.File, error) {
	if path == nil {
		return nil, ErrInvalidSource
	}
	fd, err := syscall.Openat(int(path.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "verified-sealed-generation-source"), nil
}

func containsGitMetadata(manifest delta.TreeManifest) bool {
	for _, object := range manifest.Objects {
		for _, component := range strings.Split(object.Path, "/") {
			if strings.EqualFold(component, ".git") {
				return true
			}
		}
	}
	return false
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validViewIdentity(value string) bool {
	if len(value) != len("view-")+48 || !strings.HasPrefix(value, "view-") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "view-"))
	return err == nil
}

func validDomainIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func DigestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func safeErrorClass(err error) string {
	if err == nil {
		return "unknown"
	}
	return fmt.Sprintf("%T", err)
}

func cloneGenerationEvidence(e sessionrepo.GenerationEvidence) sessionrepo.GenerationEvidence {
	e.Manifest.Objects = append([]delta.ManifestObject(nil), e.Manifest.Objects...)
	return e
}

func (p *PreparedSession) HasDurableD06Provenance() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	store, generation, journal := p.store, p.generation, p.journal
	p.mu.Unlock()
	return p.VerifySourceBinding(store, generation, journal) == nil
}

// The source evidence structs are package-local audit format, not user input.
// unmarshalNoUnknown keeps any accidental schema drift fail-closed.
func unmarshalNoUnknown(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON value")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func writeGuardIdentity(root *os.File, guard *os.File) error {
	rootStat, err := statNode(root)
	if err != nil {
		return err
	}
	guardStat, err := statNode(guard)
	if err != nil {
		return err
	}
	if rootStat.mode&syscall.S_IFMT != syscall.S_IFDIR || guardStat.mode&syscall.S_IFMT != syscall.S_IFREG || guardStat.links != 1 ||
		rootStat.uid != uint32(syscall.Geteuid()) || rootStat.gid != uint32(syscall.Getegid()) ||
		rootStat.mode&0o7777 != 0o700 || guardStat.uid != rootStat.uid || guardStat.gid != rootStat.gid || guardStat.mode&0o7777 != 0o600 {
		return ErrPrivateParent
	}
	return nil
}

func readAdminEntries(root *os.File, openMessage, name string) ([]os.DirEntry, error) {
	fd, err := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", openMessage, err)
	}
	directory := os.NewFile(uintptr(fd), name)
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(ErrPrivateParent, err, closeErr)
	}
	return entries, nil
}

func verifyAdminLock(root, guard *os.File, lockName, fileName string) error {
	lockFD, err := syscall.Openat(int(root.Fd()), lockName, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(ErrPrivateParent, err)
	}
	lockFile := os.NewFile(uintptr(lockFD), fileName)
	lockStat, statErr := statNode(lockFile)
	guardStat, guardErr := statNode(guard)
	_ = lockFile.Close()
	if statErr != nil || guardErr != nil || lockStat.mode&syscall.S_IFMT != syscall.S_IFREG ||
		lockStat.mode&0o7777 != 0o600 || lockStat.links != 1 ||
		lockStat.device != guardStat.device || lockStat.inode != guardStat.inode {
		return errors.Join(ErrPrivateParent, statErr, guardErr)
	}
	return nil
}

func requireFreshAdminParent(root, guard *os.File, lockName string) error {
	entries, err := readAdminEntries(root, "open private admin directory for stale-entry check", "sessionlaunch-admin-enumeration")
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != lockName {
		return fmt.Errorf("%w: private admin parent contains stale or unexpected entries", ErrPrivateParent)
	}
	return verifyAdminLock(root, guard, lockName, "verified-sessionlaunch-lock")
}

func requireRecoveredAdminParent(root, guard *os.File, lockName, repositoryName string) error {
	if !validPrivateGitName(repositoryName) {
		return ErrPrivateParent
	}
	entries, err := readAdminEntries(root, "open retained private admin directory for exact-entry check", "sessionlaunch-recovery-admin-enumeration")
	if err != nil {
		return err
	}
	if len(entries) != 2 {
		return fmt.Errorf("%w: retained private admin parent has unexpected entry count", ErrPrivateParent)
	}
	seenLock, seenRepository := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case lockName:
			seenLock = true
		case repositoryName:
			seenRepository = true
		default:
			return fmt.Errorf("%w: retained private admin parent contains an unexpected entry", ErrPrivateParent)
		}
	}
	if !seenLock || !seenRepository {
		return fmt.Errorf("%w: retained private admin parent is missing a pinned entry", ErrPrivateParent)
	}
	return verifyAdminLock(root, guard, lockName, "verified-sessionlaunch-recovery-lock")
}

func validPrivateGitName(name string) bool {
	if len(name) != len("staging-")+32 || !strings.HasPrefix(name, "staging-") || strings.ToLower(name) != name {
		return false
	}
	suffix := strings.TrimPrefix(name, "staging-")
	decoded, err := hex.DecodeString(suffix)
	return err == nil && hex.EncodeToString(decoded) == suffix
}
