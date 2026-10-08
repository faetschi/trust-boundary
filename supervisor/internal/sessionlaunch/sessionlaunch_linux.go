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
	"tbound/supervisor/internal/workspace"
)

const (
	linuxOPath                 = 0x200000
	maxDevelopmentRunnerLeases = 512

	provenanceSchema        = "tbound-sessionlaunch-provenance/v1"
	sourceBindingEventKind  = "sessionlaunch_source_binding"
	privateGitEventKind     = "sessionlaunch_private_git_ready"
	failedSnapshotEventKind = "sessionlaunch_private_git_failed"
)

var (
	ErrInvalidSource        = errors.New("invalid sealed session source")
	ErrSourceChanged        = errors.New("sealed session source changed during private snapshot preparation")
	ErrSourceEvidence       = errors.New("sealed session source does not match durable session evidence")
	ErrAuditJournalMismatch = errors.New("sessionlaunch journal is not the store's exact verified audit journal")
	ErrPrivateParent        = errors.New("private Git admin parent lacks an exclusive supervisor-owned boundary")
	ErrProvenanceMissing    = errors.New("durable D06 provenance is incomplete")
	ErrWorkerExposure       = errors.New("worker exposure is unavailable or not bound to a verified sealed generation")
	ErrDevelopmentSandbox   = errors.New("measured development sandbox profile is unavailable")
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
	if options.Journal == nil || options.AuthorizeOperation == nil || options.VerifyDecision == nil || options.VerifySettlement == nil {
		return nil, errors.New("sessionlaunch store requires explicit operation, decision and settlement authorities")
	}
	store, err := sessionrepo.Create(root, options)
	if err != nil {
		return nil, err
	}
	return &ManagedStore{store: store, journal: options.Journal,
		profile: SourceProfile{Limits: options.Limits, XattrVisibility: options.XattrVisibility}}, nil
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
	mu        sync.Mutex
	root      *os.File
	guard     *os.File
	rootID    nodeID
	guardID   nodeID
	closed    bool
	prepared  int
	preparing bool
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
	SchemaVersion        string                `json:"schema_version"`
	BindingDigest        string                `json:"binding_digest"`
	SourceGeneration     string                `json:"source_generation"`
	SourceTreeDigest     string                `json:"source_tree_digest"`
	PolicyDigest         string                `json:"policy_digest"`
	MetadataPolicyDigest string                `json:"metadata_policy_digest"`
	StoreAuditHead       string                `json:"store_audit_head"`
	StoreAuditRecords    uint64                `json:"store_audit_records"`
	SourceRootDevice     uint64                `json:"source_root_device"`
	SourceRootInode      uint64                `json:"source_root_inode"`
	XattrProfileDigest   string                `json:"xattr_profile_digest"`
	Transfer             string                `json:"transfer"`
	PrivateGit           privategit.Provenance `json:"private_git"`
	PrivateGitDigest     string                `json:"private_git_digest"`
	GitExecutableDigest  string                `json:"git_executable_digest"`
	GitExecutionBoundary string                `json:"git_execution_boundary"`
	SourceRecord         uint64                `json:"source_record_sequence"`
	PrivateGitRecord     uint64                `json:"private_git_record_sequence"`
}

// PreparedSession owns the private Git admin repository and persisted source
// provenance. The retained sessionrepo store remains the only worker-visible
// source of sealed read-only generations and disposable command views.
type PreparedSession struct {
	mu         sync.Mutex
	managed    *ManagedStore
	store      *sessionrepo.Store
	generation *sessionrepo.Generation
	journal    *audit.Journal
	admin      *AdminParent
	git        *privategit.Repository
	profile    SourceProfile
	provenance Provenance
	basePrefix []byte
	closed     bool
	closing    bool
	views      map[*WorkerView]struct{}
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
		managed.profile.Limits.MaxTotalBytes < managed.profile.Limits.MaxFileBytes || !managed.profile.XattrVisibility.Complete ||
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
	if admin.closed || admin.root == nil || admin.guard == nil || admin.prepared != 0 || admin.preparing {
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
		XattrProfileDigest: profile.XattrVisibility.ProfileDigest, Transfer: "workspace-import-ordinary-byte-copy",
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
		XattrVisibility: privategit.XattrVisibilityAttestation{ProfileDigest: profile.XattrVisibility.ProfileDigest, Complete: true},
		Binding:         source,
	}, gitOptions)
	if createErr != nil {
		if errors.Is(createErr, privategit.ErrCleanupIncomplete) {
			keepAdminReservation = true
		}
		failureData, _ := json.Marshal(struct {
			SchemaVersion string `json:"schema_version"`
			BindingDigest string `json:"binding_digest"`
			ErrorClass    string `json:"error_class"`
		}{provenanceSchema, bindingDigest, safeErrorClass(createErr)})
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
		failedData, _ := json.Marshal(struct {
			SchemaVersion string `json:"schema_version"`
			BindingDigest string `json:"binding_digest"`
			ErrorClass    string `json:"error_class"`
		}{provenanceSchema, bindingDigest, safeErrorClass(err)})
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
	readyData, err := json.Marshal(struct {
		SchemaVersion string              `json:"schema_version"`
		Provenance    Provenance          `json:"provenance"`
		Snapshot      privategit.Snapshot `json:"snapshot"`
	}{provenanceSchema, provenance, created.Snapshot()})
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
		profile: profile, provenance: provenance,
		basePrefix: appendRecordBytes(appendRecordBytes(append([]byte(nil), bundle.AuditJournal...), sourceRecord), readyRecord),
		views:      make(map[*WorkerView]struct{}),
	}, nil
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
	if p.closed || p.closing || p.git == nil || p.admin == nil || p.admin.root == nil {
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
	if err := privateGit.Verify(); err != nil {
		return err
	}
	return nil
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
	p.mu.Unlock()
	var errs []error
	for _, view := range views {
		if err := view.Close(); err != nil {
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
	git, admin, managed := p.git, p.admin, p.managed
	p.mu.Unlock()
	if git != nil {
		if err := git.Destroy(); err != nil {
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

func verifyPersistedD06Records(journalBytes []byte, provenance Provenance) error {
	trace, err := audit.Verify(bytes.NewReader(journalBytes))
	if err != nil {
		return err
	}
	var sourceSeen, gitSeen bool
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case sourceBindingEventKind:
			var source sourceBindingRecord
			if unmarshalNoUnknown(record.Event.Data, &source) != nil || record.Sequence != provenance.SourceRecord ||
				source.Provenance.BindingDigest != provenance.BindingDigest || source.Provenance.SourceGeneration != provenance.SourceGeneration ||
				source.Provenance.SourceTreeDigest != provenance.SourceTreeDigest || source.Provenance.PrivateGitDigest != "" ||
				!reflect.DeepEqual(source.SourceManifest, provenance.PrivateGit.SourceManifest) {
				return ErrProvenanceMissing
			}
			sourceSeen = true
		case privateGitEventKind:
			var event struct {
				SchemaVersion string              `json:"schema_version"`
				Provenance    Provenance          `json:"provenance"`
				Snapshot      privategit.Snapshot `json:"snapshot"`
			}
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
		Limits     workspace.Limits `json:"limits"`
	}{"tbound/sessionlaunch/source-binding/v1", DigestBytes(bundle.AuditJournal), generation.ID(), generation.TreeDigest(),
		bundle.PolicyDigest, bundle.MetadataPolicyDigest, bundle.AuditHeadHash, stat.device, stat.inode,
		profile.XattrVisibility.ProfileDigest, profile.Limits}
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

func requireFreshAdminParent(root, guard *os.File, lockName string) error {
	fd, err := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open private admin directory for stale-entry check: %w", err)
	}
	directory := os.NewFile(uintptr(fd), "sessionlaunch-admin-enumeration")
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.Join(ErrPrivateParent, err, closeErr)
	}
	if len(entries) != 1 || entries[0].Name() != lockName {
		return fmt.Errorf("%w: private admin parent contains stale or unexpected entries", ErrPrivateParent)
	}
	lockFD, err := syscall.Openat(int(root.Fd()), lockName, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errors.Join(ErrPrivateParent, err)
	}
	lockFile := os.NewFile(uintptr(lockFD), "verified-sessionlaunch-lock")
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
