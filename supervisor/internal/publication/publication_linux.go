//go:build linux

package publication

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/gowebpki/jcs"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const (
	publicationSchema = "tbound-publication-recovery/v1"
	linuxOPath        = 0x200000
	resolveNoMagic    = 0x02
	resolveNoSymlinks = 0x04
	resolveBeneath    = 0x08
	sysOpenat2        = 437 // Linux generic syscall number, including amd64 and arm64.
	atRemoveDir       = 0x200
	copyChunk         = 64 << 10
	maxPlanObjects    = 1024
	maxRecordBytes    = 6 << 20
)

var (
	ErrInvalidOptions     = errors.New("invalid publication options")
	ErrLockBusy           = errors.New("publication repository is locked by another owner")
	ErrLiveRootLockBusy   = errors.New("live-root repository is locked by another publication owner")
	ErrLiveRootLockState  = errors.New("live-root repository lock identity changed")
	ErrOwnerEpoch         = errors.New("publication owner epoch is not monotonically increasing")
	ErrClosed             = errors.New("publication repository is closed")
	ErrQuarantined        = errors.New("publication repository is paused by unresolved or unknown work")
	ErrInvalidRequest     = errors.New("invalid publication request")
	ErrOriginProposal     = errors.New("publication origin is not an approved proposal call in the validated chain")
	ErrBaselineConflict   = errors.New("live publication baseline conflicts with the recorded baseline")
	ErrUnsupportedDelta   = errors.New("publication delta contains an unsupported operation or object")
	ErrTokenReplay        = errors.New("publication identity or decision was already consumed")
	ErrTokenBinding       = errors.New("publication authorization does not bind the exact validated delta")
	ErrRecoveryUnknown    = errors.New("publication recovery is ambiguous; repository remains paused")
	ErrInjectedFault      = errors.New("publication fault injected")
	ErrUntrustedStateRoot = errors.New("publication state root is not private and supervisor-owned")
	ErrUntrustedArtifact  = errors.New("publication artifact directory or object is untrusted")
	ErrUnsupportedOpenat2 = errors.New("Linux openat2 with beneath/no-link resolution is required")
)

type Status string

const (
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusUnknown   Status = "UNKNOWN"
)

type ObjectStatus string

const (
	ObjectSucceeded ObjectStatus = "SUCCEEDED"
	ObjectFailed    ObjectStatus = "FAILED"
	ObjectUnknown   ObjectStatus = "UNKNOWN"
)

// Options pins the workflow owner, durable journal, private host state root,
// and the bounded normalized filesystem metadata profile.
type Options struct {
	WorkflowID string
	OwnerEpoch uint64
	StateRoot  *os.File
	Journal    *audit.Journal
	Workspace  workspace.Options
}

type Repository struct {
	mu        sync.Mutex
	options   Options
	lockFile  *os.File
	liveLocks map[string]*liveRootLock
	closed    bool
}

// RepositoryBinding is a read-only snapshot of the repository configuration
// that owns durable publication records. The Journal pointer is included so a
// trusted lifecycle coordinator can reject a repository attached to a
// different audit log before allowing publication or recovery.
type RepositoryBinding struct {
	WorkflowID string
	OwnerEpoch uint64
	Journal    *audit.Journal
	Workspace  workspace.Options
}

type liveRootLock struct {
	rootID   fileIdentity
	parentID fileIdentity
	lockID   fileIdentity
	name     string
	parent   *os.File
	file     *os.File
}

// CommitBinding is the exact scope presented to the trusted internal-commit
// authorizer. A decision for a different chain, composed delta, baseline,
// policy, owner epoch, or originating proposal is unusable.
type CommitBinding struct {
	WorkflowID           string `json:"workflow_id"`
	OwnerEpoch           uint64 `json:"owner_epoch"`
	PublicationID        string `json:"publication_id"`
	OriginProposalID     string `json:"origin_proposal_id"`
	ChainDigest          string `json:"chain_digest"`
	DeltaDigest          string `json:"delta_digest"`
	BaselineTreeDigest   string `json:"baseline_tree_digest"`
	SealedTreeDigest     string `json:"sealed_tree_digest"`
	PolicyDigest         string `json:"policy_digest"`
	MetadataPolicyDigest string `json:"metadata_policy_digest"`
	BindingDigest        string `json:"binding_digest"`
}

// Authorization is the receipt returned by the trusted internal-commit gate.
type Authorization struct {
	DecisionID    string `json:"decision_id"`
	BindingDigest string `json:"binding_digest"`
}

type Authorizer func(CommitBinding) (Authorization, error)
type FaultInjector func(point string, objectIndex int) error

// Request contains only supervisor-owned descriptors and the complete
// approved-delta chain. VerifyTransition must authenticate each policy receipt.
// OriginProposalID must exactly match a ProposalID in an approved proposal-call
// transition; lease-only origins are unsupported and fail closed.
type Request struct {
	LiveRoot         *os.File
	SealedRoot       *os.File
	Chain            delta.ChainSpec
	Transitions      []delta.Transition
	OriginProposalID string
	PublicationID    string // fresh internal-commit effect identity
	VerifyTransition delta.DecisionVerifier
}

type ObjectResult struct {
	Path       string       `json:"path"`
	SourcePath string       `json:"source_path,omitempty"`
	Operation  string       `json:"operation"`
	Status     ObjectStatus `json:"status"`
	Durability string       `json:"durability"`
	Detail     string       `json:"detail,omitempty"`
}

type Result struct {
	TransactionID string         `json:"transaction_id"`
	PublicationID string         `json:"publication_id"`
	Status        Status         `json:"status"`
	Objects       []ObjectResult `json:"objects"`
	Binding       CommitBinding  `json:"binding"`
}

type openHow struct {
	Flags   uint64
	Mode    uint64
	Resolve uint64
}

type fileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Links  uint64 `json:"links"`
	Size   int64  `json:"size"`
}

type objectPlan struct {
	Index      int               `json:"index"`
	Path       string            `json:"path"`
	SourcePath string            `json:"source_path,omitempty"`
	Operation  string            `json:"operation"`
	Before     delta.ObjectState `json:"before"`
	After      delta.ObjectState `json:"after"`
	BeforeID   *fileIdentity     `json:"before_identity,omitempty"`
	SourceID   *fileIdentity     `json:"source_identity,omitempty"`
	StageName  string            `json:"stage_name,omitempty"`
	BackupName string            `json:"backup_name,omitempty"`
}

type prepareRecord struct {
	SchemaVersion    string        `json:"schema_version"`
	TransactionID    string        `json:"transaction_id"`
	WorkflowID       string        `json:"workflow_id"`
	OwnerEpoch       uint64        `json:"owner_epoch"`
	PublicationID    string        `json:"publication_id"`
	OriginProposalID string        `json:"origin_proposal_id"`
	SealedGeneration string        `json:"sealed_generation"`
	Binding          CommitBinding `json:"binding"`
	LiveRoot         fileIdentity  `json:"live_root"`
	ArtifactParent   fileIdentity  `json:"artifact_parent"`
	ArtifactName     string        `json:"artifact_name"`
	Plans            []objectPlan  `json:"objects"`
}

type progressRecord struct {
	TransactionID  string        `json:"transaction_id"`
	Index          int           `json:"index"`
	Step           string        `json:"step"`
	StageIdentity  *fileIdentity `json:"stage_identity,omitempty"`
	BackupIdentity *fileIdentity `json:"backup_identity,omitempty"`
	ParentIdentity *fileIdentity `json:"parent_identity,omitempty"`
	ObjectIdentity *fileIdentity `json:"object_identity,omitempty"`
	Durability     string        `json:"durability,omitempty"`
}

type tokenRecord struct {
	TransactionID string        `json:"transaction_id"`
	TokenID       string        `json:"token_id"`
	DecisionID    string        `json:"decision_id"`
	BindingDigest string        `json:"binding_digest"`
	Binding       CommitBinding `json:"binding"`
}

type terminalRecord struct {
	TransactionID string         `json:"transaction_id"`
	Status        Status         `json:"status"`
	Objects       []ObjectResult `json:"objects"`
	Reason        string         `json:"reason,omitempty"`
}

type txView struct {
	prepare    prepareRecord
	progress   map[int][]progressRecord
	artifactID *fileIdentity
	token      *tokenRecord
	terminal   *terminalRecord
}

// Acquire takes a nonblocking workflow repository flock and durably advances
// owner_epoch before admitting publication or recovery. Publish and Recover
// additionally acquire an identity-keyed live-root flock in its trusted parent;
// that descriptor and lock are retained until Repository.Close, including
// across method returns. Every participating host publisher must use these
// locks; non-cooperating writers remain an explicit host-concurrency assumption.
func Acquire(options Options) (*Repository, error) {
	if !validIdentity(options.WorkflowID) || options.OwnerEpoch == 0 || options.StateRoot == nil || options.Journal == nil {
		return nil, ErrInvalidOptions
	}
	if err := validatePrivateStateRoot(options.StateRoot); err != nil {
		return nil, err
	}
	workflowHash := sha256.Sum256([]byte(options.WorkflowID))
	lockName := "publication-" + hex.EncodeToString(workflowHash[:]) + ".lock"
	fd, err := syscall.Openat(int(options.StateRoot.Fd()), lockName,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open publication repository lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), lockName)
	closeOnError := func(err error) (*Repository, error) { _ = lock.Close(); return nil, err }
	if err := validateLockFile(lock); err != nil {
		return closeOnError(err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return closeOnError(ErrLockBusy)
		}
		return closeOnError(fmt.Errorf("flock publication repository: %w", err))
	}
	if err := options.StateRoot.Sync(); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return closeOnError(fmt.Errorf("sync publication lock directory: %w", err))
	}
	r := &Repository{options: options, lockFile: lock, liveLocks: make(map[string]*liveRootLock)}
	trace, err := options.Journal.Trace()
	if err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return closeOnError(err)
	}
	var prior uint64
	for _, record := range trace.Records {
		if record.Event.Kind != "publication_owner_epoch" {
			continue
		}
		var epoch struct {
			WorkflowID string `json:"workflow_id"`
			OwnerEpoch uint64 `json:"owner_epoch"`
		}
		if err := decodeCanonical(record.Event.Data, &epoch); err != nil {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			return closeOnError(err)
		}
		if epoch.WorkflowID == options.WorkflowID {
			if epoch.OwnerEpoch == 0 || epoch.OwnerEpoch <= prior {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				return closeOnError(ErrOwnerEpoch)
			}
			prior = epoch.OwnerEpoch
		}
	}
	if options.OwnerEpoch <= prior {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return closeOnError(fmt.Errorf("%w: requested %d, durable %d", ErrOwnerEpoch, options.OwnerEpoch, prior))
	}
	data, _ := json.Marshal(struct {
		WorkflowID string `json:"workflow_id"`
		OwnerEpoch uint64 `json:"owner_epoch"`
	}{options.WorkflowID, options.OwnerEpoch})
	if _, err := options.Journal.Append(audit.Event{Kind: "publication_owner_epoch", ID: fmt.Sprintf("%s/%d", hex.EncodeToString(workflowHash[:8]), options.OwnerEpoch), Data: data}); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return closeOnError(fmt.Errorf("persist publication owner epoch: %w", err))
	}
	return r, nil
}

// Binding returns the repository's immutable durable binding while it remains
// open. It exposes no mutable repository state and is intended for trusted
// composition/recovery checks.
func (r *Repository) Binding() (RepositoryBinding, bool) {
	if r == nil {
		return RepositoryBinding{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.options.Journal == nil {
		return RepositoryBinding{}, false
	}
	return RepositoryBinding{
		WorkflowID: r.options.WorkflowID,
		OwnerEpoch: r.options.OwnerEpoch,
		Journal:    r.options.Journal,
		Workspace:  r.options.Workspace,
	}, true
}

// Publish validates the ordered ledger and complete manifests, stages every
// replacement and retained original on the target filesystem, persists a
// bound single-use token, then applies operations with per-object checkpoints.
// Any failure after token consumption is reconciled by observation only.
func (r *Repository) Publish(ctx context.Context, request Request, authorize Authorizer, inject FaultInjector) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpenLocked(); err != nil {
		return Result{}, err
	}
	if request.LiveRoot == nil || request.SealedRoot == nil || authorize == nil || request.VerifyTransition == nil ||
		!validIdentity(request.PublicationID) || !validIdentity(request.OriginProposalID) {
		return Result{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	trace, err := r.options.Journal.Trace()
	if err != nil {
		return Result{}, err
	}
	transactions, err := readTransactions(trace, r.options.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	restores, err := readRestores(trace, r.options.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	for _, restore := range restores {
		if restore.terminal == nil || restore.terminal.Status == StatusUnknown {
			return Result{}, fmt.Errorf("%w: disposable checkpoint restore %s requires reconciliation", ErrQuarantined, restore.intent.RestoreID)
		}
	}
	usedDecisions := map[string]bool{}
	for _, tx := range transactions {
		if tx.terminal == nil {
			return Result{}, fmt.Errorf("%w: transaction %s requires recovery", ErrQuarantined, tx.prepare.TransactionID)
		}
		if tx.terminal.Status == StatusUnknown {
			return Result{}, fmt.Errorf("%w: transaction %s is UNKNOWN", ErrQuarantined, tx.prepare.TransactionID)
		}
		if tx.prepare.PublicationID == request.PublicationID {
			return Result{}, ErrTokenReplay
		}
		if tx.token != nil {
			usedDecisions[tx.token.DecisionID] = true
		}
	}
	chain, err := delta.ValidateChain(request.Chain, request.Transitions, request.VerifyTransition)
	if err != nil {
		return Result{}, fmt.Errorf("validate approved-delta chain: %w", err)
	}
	if !chainContainsOriginProposal(request.Transitions, request.OriginProposalID) {
		return Result{}, ErrOriginProposal
	}
	if len(chain.ComposedChanges) > maxPlanObjects {
		return Result{}, fmt.Errorf("%w: changed object count exceeds %d", ErrUnsupportedDelta, maxPlanObjects)
	}
	rootID, err := identifyDirectory(request.LiveRoot)
	if err != nil {
		return Result{}, err
	}
	artifactParent, err := openRootParent(request.LiveRoot)
	if err != nil {
		return Result{}, fmt.Errorf("open live-root parent for target-filesystem staging: %w", err)
	}
	defer artifactParent.Close()
	parentID, err := identifyDirectory(artifactParent)
	if err != nil {
		return Result{}, err
	}
	if rootID.Device != parentID.Device || mountID(request.LiveRoot) == 0 || mountID(request.LiveRoot) != mountID(artifactParent) || rootID.Inode == parentID.Inode {
		return Result{}, errors.New("live root and parent must be distinct directories on one verified mount")
	}
	if err := validateTrustedArtifactParent(artifactParent); err != nil {
		return Result{}, err
	}
	if err := r.acquireLiveRootLock(request.LiveRoot, artifactParent, rootID, parentID); err != nil {
		return Result{}, err
	}
	baseOptions := r.options.Workspace
	baseOptions.Generation = request.Chain.Baseline.Generation
	baseline, err := workspace.Scan(request.LiveRoot, baseOptions)
	if err != nil {
		return Result{}, fmt.Errorf("scan live publication baseline: %w", err)
	}
	if baseline.TreeDigest != request.Chain.ExpectedBaselineTreeDigest {
		return Result{}, ErrBaselineConflict
	}
	sealedOptions := r.options.Workspace
	sealedOptions.Generation = request.Chain.Sealed.Generation
	sealed, err := workspace.Scan(request.SealedRoot, sealedOptions)
	if err != nil {
		return Result{}, fmt.Errorf("scan sealed publication generation: %w", err)
	}
	if sealed.TreeDigest != request.Chain.ExpectedSealedTreeDigest || !sameManifest(sealed.Manifest, request.Chain.Sealed) {
		return Result{}, errors.New("sealed generation descriptor does not match the validated endpoint manifest")
	}
	binding, err := buildBinding(r.options, request, chain)
	if err != nil {
		return Result{}, err
	}
	plans, err := makePlans(chain)
	if err != nil {
		return Result{}, err
	}
	validatedSources, err := capturePreimages(request.LiveRoot, request.SealedRoot, plans, r.options.Workspace.Limits)
	if err != nil {
		return Result{}, fmt.Errorf("validate affected publication objects: %w", err)
	}
	defer closeValidatedSources(validatedSources)
	if err := injectFault(inject, "after-source-validation", -1); err != nil {
		return Result{}, err
	}
	txID, err := randomLabel("pub-")
	if err != nil {
		return Result{}, err
	}
	artifactName := ".tbound-publication-" + strings.TrimPrefix(txID, "pub-")
	for index := range plans {
		if strings.HasPrefix(pathBase(plans[index].Path), ".tbound-publication-") {
			return Result{}, ErrUnsupportedDelta
		}
		if plans[index].After.Exists && plans[index].After.Type == delta.ObjectRegular {
			plans[index].StageName = fmt.Sprintf("stage-%04d", index)
		}
		if plans[index].Before.Exists && plans[index].Before.Type == delta.ObjectRegular {
			plans[index].BackupName = fmt.Sprintf("original-%04d", index)
		}
	}
	prep := prepareRecord{SchemaVersion: publicationSchema, TransactionID: txID, WorkflowID: r.options.WorkflowID,
		OwnerEpoch: r.options.OwnerEpoch, PublicationID: request.PublicationID, OriginProposalID: request.OriginProposalID,
		SealedGeneration: request.Chain.Sealed.Generation, Binding: binding, LiveRoot: rootID,
		ArtifactParent: parentID, ArtifactName: artifactName, Plans: plans}
	if err := r.appendRecord("publication_prepare", txID, prep); err != nil {
		return Result{}, fmt.Errorf("persist publication recovery plan: %w", err)
	}
	pendingResult := Result{TransactionID: txID, PublicationID: request.PublicationID, Status: StatusUnknown, Binding: binding}
	if err := injectFault(inject, "after-prepare-checkpoint", -1); err != nil {
		return pendingResult, err
	}
	artifact, err := createArtifactDirectory(artifactParent, artifactName)
	if err != nil {
		return pendingResult, err
	}
	defer artifact.Close()
	if err := injectFault(inject, "after-artifact-create", -1); err != nil {
		return pendingResult, err
	}
	artifactID, err := identifyDirectory(artifact)
	if err != nil {
		return pendingResult, err
	}
	if err := artifactParent.Sync(); err != nil {
		return pendingResult, fmt.Errorf("sync artifact parent: %w", err)
	}
	if err := r.appendProgress(progressRecord{TransactionID: txID, Index: -1, Step: "artifact-ready", ObjectIdentity: &artifactID, Durability: "artifact-parent-synced"}); err != nil {
		return pendingResult, err
	}
	if err := injectFault(inject, "after-artifact-parent-sync", -1); err != nil {
		return pendingResult, err
	}
	for i := range prep.Plans {
		if err := ctx.Err(); err != nil {
			return pendingResult, r.abortPrepared(artifactParent, artifact, prep, err, inject)
		}
		plan := prep.Plans[i]
		var stageID, backupID *fileIdentity
		if plan.StageName != "" {
			id, err := stageSealedFile(ctx, validatedSources[plan.Index], artifact, plan, r.options.Workspace.Limits.MaxFileBytes, inject)
			if err != nil {
				if errors.Is(err, ErrInjectedFault) {
					return pendingResult, err
				}
				return pendingResult, r.abortPrepared(artifactParent, artifact, prep, err, inject)
			}
			stageID = &id
		}
		if plan.BackupName != "" {
			id, err := stageOriginalFile(ctx, request.LiveRoot, artifact, plan, r.options.Workspace.Limits.MaxFileBytes, inject)
			if err != nil {
				if errors.Is(err, ErrInjectedFault) {
					return pendingResult, err
				}
				return pendingResult, r.abortPrepared(artifactParent, artifact, prep, err, inject)
			}
			backupID = &id
		}
		if err := artifact.Sync(); err != nil {
			return pendingResult, r.abortPrepared(artifactParent, artifact, prep, fmt.Errorf("sync target-filesystem staged objects: %w", err), inject)
		}
		if err := r.appendProgress(progressRecord{TransactionID: txID, Index: i, Step: "staged", StageIdentity: stageID, BackupIdentity: backupID, Durability: "staged-objects-and-directory-synced"}); err != nil {
			return pendingResult, err
		}
		if err := injectFault(inject, "after-stage-checkpoint", i); err != nil {
			return pendingResult, err
		}
	}
	if err := ctx.Err(); err != nil {
		return pendingResult, r.abortPrepared(artifactParent, artifact, prep, err, inject)
	}
	authorization, err := authorize(binding)
	if err != nil {
		return pendingResult, r.abortPrepared(artifactParent, artifact, prep, fmt.Errorf("publication denied: %w", err), inject)
	}
	if !validIdentity(authorization.DecisionID) || authorization.BindingDigest != binding.BindingDigest {
		return pendingResult, r.abortPrepared(artifactParent, artifact, prep, ErrTokenBinding, inject)
	}
	if usedDecisions[authorization.DecisionID] {
		return pendingResult, r.abortPrepared(artifactParent, artifact, prep, ErrTokenReplay, inject)
	}
	tokenID, err := randomLabel("commit-")
	if err != nil {
		return pendingResult, r.abortPrepared(artifactParent, artifact, prep, err, inject)
	}
	token := tokenRecord{TransactionID: txID, TokenID: tokenID, DecisionID: authorization.DecisionID, BindingDigest: binding.BindingDigest, Binding: binding}
	if err := r.appendRecord("publication_token_consumed", txID, token); err != nil {
		return pendingResult, fmt.Errorf("persist single-use commit token: %w", err)
	}
	if err := injectFault(inject, "after-token-checkpoint", -1); err != nil {
		return pendingResult, err
	}
	if err := r.apply(ctx, request.LiveRoot, artifact, prep, inject); err != nil {
		return pendingResult, err
	}
	tx, err := readOneTransaction(r.options.Journal, r.options.WorkflowID, txID)
	if err != nil {
		return Result{TransactionID: txID, PublicationID: request.PublicationID, Status: StatusUnknown, Binding: binding}, err
	}
	return r.reconcile(request.LiveRoot, artifactParent, artifact, tx, inject)
}

// Recover reconciles a durable prepare/token record against the exact live-root
// inode. It only observes a consumed token's effects; it cannot resume apply,
// roll back, or restore a checkpoint. Pre-token staging may be discarded.
func (r *Repository) Recover(liveRoot *os.File, transactionID string, inject FaultInjector) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkOpenLocked(); err != nil {
		return Result{}, err
	}
	if liveRoot == nil || !validTransactionID(transactionID) {
		return Result{}, ErrInvalidRequest
	}
	tx, err := readOneTransaction(r.options.Journal, r.options.WorkflowID, transactionID)
	if err != nil {
		return Result{}, err
	}
	if tx.terminal != nil {
		result := resultFromTerminal(tx.prepare, *tx.terminal)
		if result.Status == StatusUnknown {
			return result, ErrRecoveryUnknown
		}
		return result, nil
	}
	rootID, err := identifyDirectory(liveRoot)
	if err != nil {
		return Result{}, err
	}
	if !sameDirectoryIdentity(rootID, tx.prepare.LiveRoot) {
		return Result{}, errors.New("recovery descriptor does not identify the recorded live root")
	}
	parent, err := openRootParent(liveRoot)
	if err != nil {
		return Result{}, err
	}
	defer parent.Close()
	parentID, err := identifyDirectory(parent)
	if err != nil || !sameDirectoryIdentity(parentID, tx.prepare.ArtifactParent) {
		return Result{}, errors.New("recovery artifact-parent identity changed")
	}
	if err := r.acquireLiveRootLock(liveRoot, parent, rootID, parentID); err != nil {
		return Result{TransactionID: transactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Binding: tx.prepare.Binding}, err
	}
	artifact, err := openArtifactDirectory(parent, tx.prepare.ArtifactName, tx.artifactID)
	if err != nil && !errors.Is(err, syscall.ENOENT) {
		return Result{}, err
	}
	if artifact != nil {
		defer artifact.Close()
	}
	if tx.token == nil {
		if artifact != nil {
			if tx.artifactID == nil {
				return Result{TransactionID: transactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Binding: tx.prepare.Binding}, ErrRecoveryUnknown
			}
			if err := removePreparedArtifacts(parent, artifact, tx.prepare, inject); err != nil {
				return Result{TransactionID: transactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Binding: tx.prepare.Binding}, err
			}
		}
		objects := make([]ObjectResult, len(tx.prepare.Plans))
		for i, plan := range tx.prepare.Plans {
			objects[i] = ObjectResult{Path: plan.Path, SourcePath: plan.SourcePath, Operation: plan.Operation, Status: ObjectFailed, Durability: "no-token-no-publication-effect"}
		}
		terminal := terminalRecord{TransactionID: transactionID, Status: StatusFailed, Objects: objects, Reason: "preparation interrupted before token consumption"}
		if err := r.appendRecord("publication_aborted", transactionID, terminal); err != nil {
			return Result{}, err
		}
		return resultFromTerminal(tx.prepare, terminal), nil
	}
	if artifact == nil || tx.artifactID == nil {
		return Result{TransactionID: transactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Binding: tx.prepare.Binding}, ErrRecoveryUnknown
	}
	return r.reconcile(liveRoot, parent, artifact, tx, inject)
}

// Close releases the repository flock. Outstanding transactions are not
// silently reconciled or rolled back.
func (r *Repository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var liveErr error
	for key, liveLock := range r.liveLocks {
		liveErr = errors.Join(liveErr, releaseLiveRootLock(liveLock))
		delete(r.liveLocks, key)
	}
	unlockErr := syscall.Flock(int(r.lockFile.Fd()), syscall.LOCK_UN)
	closeErr := r.lockFile.Close()
	return errors.Join(liveErr, unlockErr, closeErr)
}

func (r *Repository) checkOpenLocked() error {
	if r.closed {
		return ErrClosed
	}
	return nil
}

func (r *Repository) apply(ctx context.Context, root, artifact *os.File, prep prepareRecord, inject FaultInjector) error {
	tx, err := readOneTransaction(r.options.Journal, r.options.WorkflowID, prep.TransactionID)
	if err != nil {
		return err
	}
	indices := make([]int, len(prep.Plans))
	for i := range indices {
		indices[i] = i
	}
	sort.SliceStable(indices, func(i, j int) bool {
		left, right := prep.Plans[indices[i]], prep.Plans[indices[j]]
		leftDir := left.After.Exists && left.After.Type == delta.ObjectDirectory
		rightDir := right.After.Exists && right.After.Type == delta.ObjectDirectory
		if leftDir != rightDir {
			return leftDir
		}
		if leftDir {
			return pathDepth(left.Path) < pathDepth(right.Path)
		}
		return left.Index < right.Index
	})
	for _, index := range indices {
		if err := ctx.Err(); err != nil {
			return err
		}
		plan := prep.Plans[index]
		parent, base, parentID, err := openTargetParent(root, plan.Path)
		if err != nil {
			return fmt.Errorf("resolve target parent for %q: %w", plan.Path, err)
		}
		if err := r.appendProgress(progressRecord{TransactionID: prep.TransactionID, Index: index, Step: "parent-bound", ParentIdentity: &parentID, Durability: "parent-identity-recorded"}); err != nil {
			parent.Close()
			return err
		}
		if err := injectFault(inject, "after-parent-checkpoint", index); err != nil {
			parent.Close()
			return err
		}
		if plan.After.Exists && plan.After.Type == delta.ObjectDirectory {
			if err := r.createDirectory(ctx, root, parent, base, index, plan, prep.TransactionID, inject); err != nil {
				parent.Close()
				return err
			}
			parent.Close()
			continue
		}
		if plan.Before.Exists {
			if err := verifyCurrentRegularAt(parent, base, plan.Before, plan.BeforeID, r.options.Workspace.Limits.MaxFileBytes); err != nil {
				parent.Close()
				return fmt.Errorf("baseline changed before applying %q: %w", plan.Path, err)
			}
		} else {
			exists, err := pathExistsAt(parent, base)
			if err != nil {
				parent.Close()
				return err
			}
			if exists {
				parent.Close()
				return fmt.Errorf("%w: target %q appeared after baseline validation", ErrBaselineConflict, plan.Path)
			}
		}
		if err := r.appendProgress(progressRecord{TransactionID: prep.TransactionID, Index: index, Step: "object-start", ParentIdentity: &parentID, Durability: "effect-start-recorded"}); err != nil {
			parent.Close()
			return err
		}
		if err := injectFault(inject, "after-object-start-checkpoint", index); err != nil {
			parent.Close()
			return err
		}
		if err := verifyParentStill(root, plan.Path, parentID); err != nil {
			parent.Close()
			return err
		}
		if !plan.After.Exists {
			_, backupID := stagedIdentities(tx.progress[index])
			if plan.BackupName == "" || backupID == nil || !artifactMatches(artifact, plan.BackupName, plan.Before, backupID, r.options.Workspace.Limits.MaxFileBytes) {
				parent.Close()
				return fmt.Errorf("retained original for %q is missing or invalid", plan.Path)
			}
			if err := unlinkAt(int(parent.Fd()), base, 0); err != nil {
				parent.Close()
				return fmt.Errorf("unlink publication target %q: %w", plan.Path, err)
			}
			if err := injectFault(inject, "after-unlink", index); err != nil {
				parent.Close()
				return err
			}
			if err := parent.Sync(); err != nil {
				parent.Close()
				return fmt.Errorf("sync deletion parent %q: %w", plan.Path, err)
			}
			if err := injectFault(inject, "after-parent-sync", index); err != nil {
				parent.Close()
				return err
			}
			parent.Close()
			if err := r.appendProgress(progressRecord{TransactionID: prep.TransactionID, Index: index, Step: "applied", Durability: "unlink-and-parent-sync-checkpointed"}); err != nil {
				return err
			}
			if err := injectFault(inject, "after-object-checkpoint", index); err != nil {
				return err
			}
			continue
		}
		stageID, backupID := stagedIdentities(tx.progress[index])
		if plan.StageName == "" || stageID == nil || !artifactMatches(artifact, plan.StageName, plan.After, stageID, r.options.Workspace.Limits.MaxFileBytes) {
			parent.Close()
			return fmt.Errorf("staged replacement for %q is missing or invalid", plan.Path)
		}
		if plan.Before.Exists && (plan.BackupName == "" || backupID == nil || !artifactMatches(artifact, plan.BackupName, plan.Before, backupID, r.options.Workspace.Limits.MaxFileBytes)) {
			parent.Close()
			return fmt.Errorf("retained original for %q is missing or invalid", plan.Path)
		}
		if err := syscall.Renameat(int(artifact.Fd()), plan.StageName, int(parent.Fd()), base); err != nil {
			parent.Close()
			return fmt.Errorf("rename staged object into %q: %w", plan.Path, err)
		}
		if err := injectFault(inject, "after-rename", index); err != nil {
			parent.Close()
			return err
		}
		if err := parent.Sync(); err != nil {
			parent.Close()
			return fmt.Errorf("sync publication parent %q: %w", plan.Path, err)
		}
		if err := artifact.Sync(); err != nil {
			parent.Close()
			return fmt.Errorf("sync staged-source directory after rename: %w", err)
		}
		if err := injectFault(inject, "after-parent-sync", index); err != nil {
			parent.Close()
			return err
		}
		objectID, err := identifyAt(parent, base)
		parent.Close()
		if err != nil {
			return err
		}
		if !sameIdentity(*stageID, objectID) {
			return fmt.Errorf("renamed object %q does not retain the staged inode identity", plan.Path)
		}
		if err := r.appendProgress(progressRecord{TransactionID: prep.TransactionID, Index: index, Step: "applied", ObjectIdentity: &objectID, Durability: "rename-and-parent-sync-checkpointed"}); err != nil {
			return err
		}
		if err := injectFault(inject, "after-object-checkpoint", index); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) createDirectory(ctx context.Context, root, parent *os.File, base string, index int, plan objectPlan, txID string, inject FaultInjector) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	exists, err := pathExistsAt(parent, base)
	if err != nil {
		return err
	}
	if exists {
		return ErrBaselineConflict
	}
	parentID, err := identifyDirectory(parent)
	if err != nil {
		return err
	}
	if err := r.appendProgress(progressRecord{TransactionID: txID, Index: index, Step: "object-start", ParentIdentity: &parentID, Durability: "directory-create-start-recorded"}); err != nil {
		return err
	}
	if err := injectFault(inject, "after-object-start-checkpoint", index); err != nil {
		return err
	}
	if err := verifyParentStill(root, plan.Path, parentID); err != nil {
		return err
	}
	if err := syscall.Mkdirat(int(parent.Fd()), base, 0o700); err != nil {
		return fmt.Errorf("mkdir publication directory %q: %w", plan.Path, err)
	}
	if err := injectFault(inject, "after-mkdir", index); err != nil {
		return err
	}
	fd, err := syscall.Openat(int(parent.Fd()), base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), plan.Path)
	defer directory.Close()
	if err := syscall.Fchmod(fd, 0o755); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync created directory %q: %w", plan.Path, err)
	}
	if err := parent.Sync(); err != nil {
		return fmt.Errorf("sync parent of created directory %q: %w", plan.Path, err)
	}
	if err := injectFault(inject, "after-parent-sync", index); err != nil {
		return err
	}
	identity, err := identifyDirectory(directory)
	if err != nil {
		return err
	}
	if err := r.appendProgress(progressRecord{TransactionID: txID, Index: index, Step: "applied", ObjectIdentity: &identity, Durability: "directory-and-parent-synced-checkpointed"}); err != nil {
		return err
	}
	return injectFault(inject, "after-object-checkpoint", index)
}

func (r *Repository) reconcile(root, parent, artifact *os.File, tx txView, inject FaultInjector) (Result, error) {
	if tx.terminal != nil {
		return resultFromTerminal(tx.prepare, *tx.terminal), nil
	}
	results := make([]ObjectResult, len(tx.prepare.Plans))
	unknown, failed := false, false
	for i, plan := range tx.prepare.Plans {
		progress := tx.progress[i]
		var latest, applied *progressRecord
		started := false
		for j := range progress {
			p := &progress[j]
			if p.Step == "object-start" {
				started = true
			}
			if p.Step == "applied" {
				applied = p
			}
			latest = p
		}
		status, durability, detail := classifyObservedObject(root, artifact, plan, latest, applied, started, r.options.Workspace.Limits.MaxFileBytes)
		results[i] = ObjectResult{Path: plan.Path, SourcePath: plan.SourcePath, Operation: plan.Operation, Status: status, Durability: durability, Detail: detail}
		unknown = unknown || status == ObjectUnknown
		failed = failed || status == ObjectFailed
		if err := injectFault(inject, "after-checkpoint", i); err != nil {
			return Result{TransactionID: tx.prepare.TransactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Objects: results, Binding: tx.prepare.Binding}, err
		}
	}
	status := StatusSucceeded
	if unknown {
		status = StatusUnknown
	} else if failed {
		status = StatusFailed
	}
	if status == StatusSucceeded {
		options := r.options.Workspace
		options.Generation = tx.prepare.SealedGeneration
		snapshot, err := workspace.Scan(root, options)
		if err != nil || snapshot.TreeDigest != tx.prepare.Binding.SealedTreeDigest {
			status = StatusUnknown
			for i := range results {
				results[i].Detail = "per-object state appears applied but the complete live manifest differs from the sealed generation"
			}
		}
	}
	terminal := terminalRecord{TransactionID: tx.prepare.TransactionID, Status: status, Objects: results}
	if err := r.appendRecord("publication_terminal", tx.prepare.TransactionID, terminal); err != nil {
		return Result{TransactionID: tx.prepare.TransactionID, PublicationID: tx.prepare.PublicationID, Status: StatusUnknown, Objects: results, Binding: tx.prepare.Binding}, errors.Join(ErrRecoveryUnknown, err)
	}
	if err := injectFault(inject, "after-terminal-checkpoint", -1); err != nil {
		return Result{TransactionID: tx.prepare.TransactionID, PublicationID: tx.prepare.PublicationID, Status: status, Objects: results, Binding: tx.prepare.Binding}, err
	}
	result := resultFromTerminal(tx.prepare, terminal)
	if status == StatusUnknown {
		return result, ErrRecoveryUnknown
	}
	return result, nil
}

func classifyObservedObject(root, artifact *os.File, plan objectPlan, latest, applied *progressRecord, started bool, maxBytes int64) (ObjectStatus, string, string) {
	if applied != nil {
		if objectMatchesAt(root, plan.Path, plan.After, applied.ObjectIdentity, maxBytes) {
			return ObjectSucceeded, applied.Durability, "durable per-object checkpoint and observed final object agree"
		}
		return ObjectUnknown, "checkpoint-observation-mismatch", "durable apply checkpoint does not match the current object observation"
	}
	if objectMatchesAt(root, plan.Path, plan.Before, plan.BeforeID, maxBytes) {
		if !started {
			return ObjectFailed, "not-started", "token consumed but no durable apply-start checkpoint exists for this object"
		}
		return ObjectFailed, "preimage-observed", "the exact baseline object remains; consumed authority is not replayed"
	}
	if plan.After.Exists && objectMatchesAt(root, plan.Path, plan.After, nil, maxBytes) && latest != nil && latest.Step == "object-start" {
		return ObjectUnknown, "final-state-without-durable-checkpoint", "final content is visible but parent synchronization/checkpoint is unestablished"
	}
	if !plan.After.Exists && plan.Before.Exists && !pathExistsBeneath(root, plan.Path) &&
		artifactHasRegular(artifact, plan.BackupName, plan.Before, maxBytes) && latest != nil && latest.Step == "object-start" {
		return ObjectUnknown, "unlink-without-durable-checkpoint", "target is absent and original retained, but unlink durability was not checkpointed"
	}
	return ObjectUnknown, "ambiguous-observation", "live state does not uniquely match the baseline or a durable final checkpoint"
}

func (r *Repository) abortPrepared(parent, artifact *os.File, prep prepareRecord, cause error, inject FaultInjector) error {
	if err := removePreparedArtifacts(parent, artifact, prep, inject); err != nil {
		return errors.Join(cause, err)
	}
	objects := make([]ObjectResult, len(prep.Plans))
	for i, plan := range prep.Plans {
		objects[i] = ObjectResult{Path: plan.Path, SourcePath: plan.SourcePath, Operation: plan.Operation, Status: ObjectFailed, Durability: "aborted-before-token-consumption"}
	}
	terminal := terminalRecord{TransactionID: prep.TransactionID, Status: StatusFailed, Objects: objects, Reason: cause.Error()}
	if err := r.appendRecord("publication_aborted", prep.TransactionID, terminal); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func removePreparedArtifacts(parent, artifact *os.File, prep prepareRecord, inject FaultInjector) error {
	for _, plan := range prep.Plans {
		for _, name := range []string{plan.StageName, plan.BackupName} {
			if name == "" {
				continue
			}
			if err := unlinkAt(int(artifact.Fd()), name, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
				return fmt.Errorf("remove pre-token artifact %q: %w", name, err)
			}
			if err := injectFault(inject, "after-unlink", plan.Index); err != nil {
				return err
			}
		}
	}
	if err := artifact.Sync(); err != nil {
		return err
	}
	if err := unlinkAt(int(parent.Fd()), prep.ArtifactName, atRemoveDir); err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err := parent.Sync(); err != nil {
		return err
	}
	return nil
}

func buildBinding(options Options, request Request, chain delta.Result) (CommitBinding, error) {
	chainDigest, err := digestCanonical(struct {
		Baseline    string             `json:"baseline"`
		Sealed      string             `json:"sealed"`
		Policy      string             `json:"policy"`
		Metadata    string             `json:"metadata"`
		Transitions []delta.Transition `json:"transitions"`
	}{chain.BaselineTreeDigest, chain.SealedTreeDigest, request.Chain.PolicyDigest, request.Chain.MetadataPolicyDigest, request.Transitions})
	if err != nil {
		return CommitBinding{}, err
	}
	deltaDigest, err := digestCanonical(chain.ComposedChanges)
	if err != nil {
		return CommitBinding{}, err
	}
	binding := CommitBinding{WorkflowID: options.WorkflowID, OwnerEpoch: options.OwnerEpoch,
		PublicationID: request.PublicationID, OriginProposalID: request.OriginProposalID,
		ChainDigest: chainDigest, DeltaDigest: deltaDigest, BaselineTreeDigest: chain.BaselineTreeDigest,
		SealedTreeDigest: chain.SealedTreeDigest, PolicyDigest: request.Chain.PolicyDigest,
		MetadataPolicyDigest: request.Chain.MetadataPolicyDigest}
	binding.BindingDigest, err = computeBindingDigest(binding)
	return binding, err
}

func computeBindingDigest(binding CommitBinding) (string, error) {
	return digestCanonical(struct {
		WorkflowID           string `json:"workflow_id"`
		OwnerEpoch           uint64 `json:"owner_epoch"`
		PublicationID        string `json:"publication_id"`
		OriginProposalID     string `json:"origin_proposal_id"`
		ChainDigest          string `json:"chain_digest"`
		DeltaDigest          string `json:"delta_digest"`
		BaselineTreeDigest   string `json:"baseline_tree_digest"`
		SealedTreeDigest     string `json:"sealed_tree_digest"`
		PolicyDigest         string `json:"policy_digest"`
		MetadataPolicyDigest string `json:"metadata_policy_digest"`
	}{binding.WorkflowID, binding.OwnerEpoch, binding.PublicationID, binding.OriginProposalID, binding.ChainDigest,
		binding.DeltaDigest, binding.BaselineTreeDigest, binding.SealedTreeDigest, binding.PolicyDigest, binding.MetadataPolicyDigest})
}

func makePlans(chain delta.Result) ([]objectPlan, error) {
	changes := append([]delta.ObjectChange(nil), chain.ComposedChanges...)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	deletes, creates := map[delta.ObjectState][]int{}, map[delta.ObjectState][]int{}
	for i, change := range changes {
		if change.Before.Exists && change.Before.Type == delta.ObjectRegular && !change.After.Exists {
			deletes[change.Before] = append(deletes[change.Before], i)
		}
		if !change.Before.Exists && change.After.Exists && change.After.Type == delta.ObjectRegular {
			creates[change.After] = append(creates[change.After], i)
		}
	}
	sourceFor := map[int]string{}
	for state, removed := range deletes {
		added := creates[state]
		n := len(removed)
		if len(added) < n {
			n = len(added)
		}
		for k := 0; k < n; k++ {
			sourceFor[added[k]] = changes[removed[k]].Path
		}
	}
	plans := make([]objectPlan, 0, len(changes))
	for i, change := range changes {
		if change.Before == change.After {
			continue
		}
		if change.Before.Exists && change.Before.Type != delta.ObjectRegular {
			return nil, fmt.Errorf("%w: directory removal/replacement is unsupported", ErrUnsupportedDelta)
		}
		if change.After.Exists && change.After.Type != delta.ObjectRegular && change.After.Type != delta.ObjectDirectory {
			return nil, ErrUnsupportedDelta
		}
		if change.Before.Exists && change.After.Exists && change.After.Type != delta.ObjectRegular {
			return nil, fmt.Errorf("%w: object type replacement is unsupported", ErrUnsupportedDelta)
		}
		operation := "create"
		switch {
		case change.Before.Exists && !change.After.Exists:
			operation = "delete"
		case change.Before.Exists && change.After.Exists:
			if change.Before.MetadataIdentity != change.After.MetadataIdentity {
				operation = "replacement"
			} else {
				operation = "overwrite"
			}
		case change.After.Exists && change.After.Type == delta.ObjectDirectory:
			operation = "directory_create"
		}
		sourcePath := ""
		if source, ok := sourceFor[i]; ok {
			operation, sourcePath = "rename", source
		}
		plans = append(plans, objectPlan{Index: len(plans), Path: change.Path, SourcePath: sourcePath,
			Operation: operation, Before: change.Before, After: change.After})
	}
	return plans, nil
}

func capturePreimages(live, sealed *os.File, plans []objectPlan, limits workspace.Limits) (map[int]*os.File, error) {
	sources := make(map[int]*os.File)
	fail := func(err error) (map[int]*os.File, error) {
		closeValidatedSources(sources)
		return nil, err
	}
	for i := range plans {
		plan := &plans[i]
		if plan.Before.Exists {
			file, err := openBeneath(live, plan.Path, syscall.O_RDONLY|syscall.O_NONBLOCK)
			if err != nil {
				return fail(errors.Join(ErrBaselineConflict, err))
			}
			id, state, err := inspectRegular(file, limits.MaxFileBytes)
			if err != nil || state != plan.Before {
				_ = file.Close()
				return fail(errors.Join(ErrBaselineConflict, err))
			}
			_ = file.Close()
			plan.BeforeID = &id
		}
		if plan.After.Exists && plan.After.Type == delta.ObjectRegular {
			file, err := openBeneath(sealed, plan.Path, syscall.O_RDONLY|syscall.O_NONBLOCK)
			if err != nil {
				return fail(fmt.Errorf("open sealed object %q: %w", plan.Path, err))
			}
			id, state, err := inspectRegular(file, limits.MaxFileBytes)
			if err != nil || state != plan.After {
				_ = file.Close()
				if err != nil {
					return fail(fmt.Errorf("sealed object %q does not match manifest: %w", plan.Path, err))
				}
				return fail(fmt.Errorf("sealed object %q does not match manifest", plan.Path))
			}
			plan.SourceID = &id
			sources[plan.Index] = file
		}
	}
	return sources, nil
}

func closeValidatedSources(sources map[int]*os.File) {
	for index, source := range sources {
		if source != nil {
			_ = source.Close()
		}
		delete(sources, index)
	}
}

func stageSealedFile(ctx context.Context, source, artifact *os.File, plan objectPlan, maxBytes int64, inject FaultInjector) (fileIdentity, error) {
	if source == nil {
		return fileIdentity{}, errors.New("validated sealed source descriptor is missing")
	}
	var err error
	id, state, err := inspectRegular(source, maxBytes)
	if err != nil || state != plan.After || plan.SourceID == nil || !sameIdentity(*plan.SourceID, id) {
		if err != nil {
			return fileIdentity{}, errors.Join(errors.New("sealed source descriptor changed after validation"), err)
		}
		return fileIdentity{}, errors.New("sealed source descriptor changed after validation")
	}
	fd, err := syscall.Openat(int(artifact.Fd()), plan.StageName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fileIdentity{}, err
	}
	out := os.NewFile(uintptr(fd), plan.StageName)
	defer out.Close()
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fileIdentity{}, err
	}
	h := sha256.New()
	if err := copyHashed(ctx, out, source, h, maxBytes); err != nil {
		return fileIdentity{}, err
	}
	afterID, afterState, err := inspectRegular(source, maxBytes)
	if err != nil || afterState != plan.After || !sameIdentity(id, afterID) {
		return fileIdentity{}, errors.Join(workspace.ErrSourceChanged, err)
	}
	if err := injectFault(inject, "after-stage-write", plan.Index); err != nil {
		return fileIdentity{}, err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != plan.After.ContentDigest {
		return fileIdentity{}, errors.New("staged source content digest mismatch")
	}
	mode := id.Mode & 0o7777
	if mode != 0o644 && mode != 0o755 {
		return fileIdentity{}, workspace.ErrNoncanonicalMode
	}
	if err := syscall.Fchmod(fd, mode); err != nil {
		return fileIdentity{}, err
	}
	if err := out.Sync(); err != nil {
		return fileIdentity{}, err
	}
	if err := injectFault(inject, "after-stage-sync", plan.Index); err != nil {
		return fileIdentity{}, err
	}
	return identifyRegular(out)
}

func stageOriginalFile(ctx context.Context, live, artifact *os.File, plan objectPlan, maxBytes int64, inject FaultInjector) (fileIdentity, error) {
	source, err := openBeneath(live, plan.Path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return fileIdentity{}, err
	}
	defer source.Close()
	id, state, err := inspectRegular(source, maxBytes)
	if err != nil || state != plan.Before || plan.BeforeID == nil || !sameIdentity(*plan.BeforeID, id) {
		return fileIdentity{}, errors.Join(ErrBaselineConflict, err)
	}
	fd, err := syscall.Openat(int(artifact.Fd()), plan.BackupName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fileIdentity{}, err
	}
	out := os.NewFile(uintptr(fd), plan.BackupName)
	defer out.Close()
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return fileIdentity{}, err
	}
	h := sha256.New()
	if err := copyHashed(ctx, out, source, h, maxBytes); err != nil {
		return fileIdentity{}, err
	}
	afterID, afterState, err := inspectRegular(source, maxBytes)
	if err != nil || afterState != plan.Before || !sameIdentity(id, afterID) {
		return fileIdentity{}, errors.Join(ErrBaselineConflict, err)
	}
	if err := injectFault(inject, "after-backup-write", plan.Index); err != nil {
		return fileIdentity{}, err
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != plan.Before.ContentDigest {
		return fileIdentity{}, errors.New("retained original digest mismatch")
	}
	if err := syscall.Fchmod(fd, id.Mode&0o7777); err != nil {
		return fileIdentity{}, err
	}
	if err := out.Sync(); err != nil {
		return fileIdentity{}, err
	}
	if err := injectFault(inject, "after-backup-sync", plan.Index); err != nil {
		return fileIdentity{}, err
	}
	return identifyRegular(out)
}

func copyHashed(ctx context.Context, destination *os.File, source io.Reader, h hash.Hash, maxBytes int64) error {
	buffer := make([]byte, copyChunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			if int64(n) > maxBytes-total {
				return workspace.ErrLimit
			}
			if err := writeFull(destination, buffer[:n]); err != nil {
				return err
			}
			if _, err := h.Write(buffer[:n]); err != nil {
				return err
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}

func inspectRegular(file *os.File, maxBytes int64) (fileIdentity, delta.ObjectState, error) {
	var before syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &before); err != nil {
		return fileIdentity{}, delta.ObjectState{}, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Uid != uint32(syscall.Geteuid()) || before.Gid != uint32(syscall.Getegid()) {
		return fileIdentity{}, delta.ObjectState{}, workspace.ErrUnsupportedMetadata
	}
	mode := before.Mode & 0o7777
	if mode != 0o644 && mode != 0o755 {
		return fileIdentity{}, delta.ObjectState{}, workspace.ErrNoncanonicalMode
	}
	if before.Size < 0 || before.Size > maxBytes {
		return fileIdentity{}, delta.ObjectState{}, workspace.ErrLimit
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fileIdentity{}, delta.ObjectState{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(file, maxBytes+1)); err != nil {
		return fileIdentity{}, delta.ObjectState{}, err
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &after); err != nil {
		return fileIdentity{}, delta.ObjectState{}, err
	}
	if !sameStableStat(before, after) || after.Size > maxBytes {
		return fileIdentity{}, delta.ObjectState{}, workspace.ErrSourceChanged
	}
	state := delta.ObjectState{Exists: true, Type: delta.ObjectRegular,
		ContentDigest: "sha256:" + hex.EncodeToString(h.Sum(nil)), MetadataIdentity: metadataIdentity(delta.ObjectRegular, uint32(mode))}
	return identityFromStat(after), state, nil
}

func artifactHasRegular(artifact *os.File, name string, expected delta.ObjectState, maxBytes int64) bool {
	fd, err := syscall.Openat(int(artifact.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	_, actual, err := inspectRegular(file, maxBytes)
	return err == nil && actual == expected
}

func artifactHasStage(artifact *os.File, name string, expected delta.ObjectState, maxBytes int64) bool {
	return artifactHasRegular(artifact, name, expected, maxBytes)
}

func artifactMatches(artifact *os.File, name string, expected delta.ObjectState, expectedID *fileIdentity, maxBytes int64) bool {
	if expectedID == nil {
		return false
	}
	fd, err := syscall.Openat(int(artifact.Fd()), name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	id, actual, err := inspectRegular(file, maxBytes)
	return err == nil && actual == expected && sameIdentity(*expectedID, id)
}

func stagedIdentities(progress []progressRecord) (*fileIdentity, *fileIdentity) {
	for i := len(progress) - 1; i >= 0; i-- {
		if progress[i].Step == "staged" {
			return progress[i].StageIdentity, progress[i].BackupIdentity
		}
	}
	return nil, nil
}

func verifyParentStill(root *os.File, path string, expected fileIdentity) error {
	parent, _, actual, err := openTargetParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if !sameDirectoryIdentity(expected, actual) {
		return fmt.Errorf("%w: parent identity changed for %q", ErrBaselineConflict, path)
	}
	return nil
}

func verifyCurrentRegularAt(parent *os.File, base string, expected delta.ObjectState, expectedID *fileIdentity, maxBytes int64) error {
	fd, err := syscall.Openat(int(parent.Fd()), base, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), base)
	defer file.Close()
	id, state, err := inspectRegular(file, maxBytes)
	if err != nil {
		return err
	}
	if state != expected || expectedID == nil || !sameIdentity(*expectedID, id) {
		return ErrBaselineConflict
	}
	return nil
}

func objectMatchesAt(root *os.File, path string, expected delta.ObjectState, expectedID *fileIdentity, maxBytes int64) bool {
	if !expected.Exists {
		return !pathExistsBeneath(root, path)
	}
	switch expected.Type {
	case delta.ObjectRegular:
		file, err := openBeneath(root, path, syscall.O_RDONLY|syscall.O_NONBLOCK)
		if err != nil {
			return false
		}
		defer file.Close()
		id, state, err := inspectRegular(file, maxBytes)
		return err == nil && state == expected && (expectedID == nil || sameIdentity(*expectedID, id))
	case delta.ObjectDirectory:
		file, err := openBeneath(root, path, syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return false
		}
		defer file.Close()
		id, err := identifyDirectory(file)
		if err != nil || id.UID != uint32(syscall.Geteuid()) || id.GID != uint32(syscall.Getegid()) || id.Mode&0o7777 != 0o755 ||
			metadataIdentity(delta.ObjectDirectory, 0o755) != expected.MetadataIdentity {
			return false
		}
		return expectedID == nil || sameIdentity(*expectedID, id)
	default:
		return false
	}
}

func pathExistsBeneath(root *os.File, path string) bool {
	file, err := openBeneath(root, path, linuxOPath)
	if err == nil {
		_ = file.Close()
		return true
	}
	return !errors.Is(err, syscall.ENOENT)
}

func pathExistsAt(parent *os.File, name string) (bool, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = syscall.Close(fd)
	return true, nil
}

func openBeneath(root *os.File, relative string, flags int) (*os.File, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, err
	}
	path, err := syscall.BytePtrFromString(relative)
	if err != nil {
		return nil, err
	}
	how := openHow{Flags: uint64(flags | syscall.O_CLOEXEC | syscall.O_NOFOLLOW), Resolve: resolveBeneath | resolveNoSymlinks | resolveNoMagic}
	fd, _, errno := syscall.Syscall6(sysOpenat2, root.Fd(), uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	runtime.KeepAlive(path)
	runtime.KeepAlive(how)
	if errno != 0 {
		if errno == syscall.ENOSYS || errno == syscall.EINVAL {
			return nil, ErrUnsupportedOpenat2
		}
		return nil, errno
	}
	return os.NewFile(fd, relative), nil
}

func openTargetParent(root *os.File, relative string) (*os.File, string, fileIdentity, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, "", fileIdentity{}, err
	}
	parentPath, base := splitParent(relative)
	var parent *os.File
	var err error
	if parentPath == "" {
		fd, e := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return nil, "", fileIdentity{}, e
		}
		parent = os.NewFile(uintptr(fd), "publication-root-parent")
	} else {
		parent, err = openBeneath(root, parentPath, syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return nil, "", fileIdentity{}, err
		}
	}
	identity, err := identifyDirectory(parent)
	if err != nil {
		_ = parent.Close()
		return nil, "", fileIdentity{}, err
	}
	rootMount, parentMount := mountID(root), mountID(parent)
	if rootMount == 0 || parentMount == 0 || rootMount != parentMount {
		_ = parent.Close()
		return nil, "", fileIdentity{}, workspace.ErrNestedMount
	}
	return parent, base, identity, nil
}

func openRootParent(root *os.File) (*os.File, error) {
	fd, err := syscall.Openat(int(root.Fd()), "..", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "live-root-parent"), nil
}

func createArtifactDirectory(parent *os.File, name string) (*os.File, error) {
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := syscall.Fchmod(fd, 0o700); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validateArtifactDirectory(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openArtifactDirectory(parent *os.File, name string, expected *fileIdentity) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err := validateArtifactDirectory(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if expected != nil {
		actual, err := identifyDirectory(file)
		if err != nil || !sameDirectoryIdentity(actual, *expected) {
			_ = file.Close()
			return nil, ErrUntrustedArtifact
		}
	}
	return file, nil
}

func validateArtifactDirectory(file *os.File) error {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrUntrustedArtifact
	}
	return nil
}

func identifyDirectory(file *os.File) (fileIdentity, error) {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return fileIdentity{}, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fileIdentity{}, ErrUntrustedArtifact
	}
	return identityFromStat(info), nil
}

func identifyRegular(file *os.File) (fileIdentity, error) {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return fileIdentity{}, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 {
		return fileIdentity{}, workspace.ErrHardLink
	}
	return identityFromStat(info), nil
}

func identifyAt(parent *os.File, name string) (fileIdentity, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return fileIdentity{}, err
	}
	return identityFromStat(info), nil
}

func identityFromStat(info syscall.Stat_t) fileIdentity {
	return fileIdentity{Device: uint64(info.Dev), Inode: info.Ino, Mode: info.Mode, UID: info.Uid, GID: info.Gid, Links: uint64(info.Nlink), Size: info.Size}
}

func sameIdentity(expected, actual fileIdentity) bool {
	return expected.Device == actual.Device && expected.Inode == actual.Inode && expected.Mode == actual.Mode &&
		expected.UID == actual.UID && expected.GID == actual.GID && expected.Links == actual.Links && expected.Size == actual.Size
}

func sameIdentityNode(left, right fileIdentity) bool {
	return left.Device == right.Device && left.Inode == right.Inode && left.Mode&syscall.S_IFMT == right.Mode&syscall.S_IFMT
}

func sameDirectoryIdentity(left, right fileIdentity) bool {
	return sameIdentityNode(left, right) && left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID
}

func sameStableStat(left, right syscall.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Nlink == right.Nlink &&
		left.Uid == right.Uid && left.Gid == right.Gid && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func metadataIdentity(kind delta.ObjectType, mode uint32) string {
	value := fmt.Sprintf("tbound-normalized-metadata/v1/type=%s/mode=%04o", kind, mode)
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mountID(file *os.File) uint64 {
	info, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return 0
	}
	defer info.Close()
	data, err := io.ReadAll(io.LimitReader(info, 16<<10))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "mnt_id" {
			continue
		}
		var id uint64
		if _, err := fmt.Sscan(strings.TrimSpace(value), &id); err == nil {
			return id
		}
	}
	return 0
}

func validatePrivateStateRoot(file *os.File) error {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrUntrustedStateRoot
	}
	return nil
}

func validateLockFile(file *os.File) error {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o600 {
		return ErrUntrustedStateRoot
	}
	return nil
}

func (r *Repository) acquireLiveRootLock(root, parent *os.File, rootID, parentID fileIdentity) error {
	actualRoot, err := identifyDirectory(root)
	if err != nil || !sameDirectoryIdentity(actualRoot, rootID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	actualParent, err := identifyDirectory(parent)
	if err != nil || !sameDirectoryIdentity(actualParent, parentID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	key := liveRootLockKey(rootID)
	if existing := r.liveLocks[key]; existing != nil {
		if !sameDirectoryIdentity(existing.rootID, rootID) || !sameDirectoryIdentity(existing.parentID, parentID) {
			return ErrLiveRootLockState
		}
		return validateLiveRootLock(existing, root, parent)
	}
	name := liveRootLockName(rootID)
	parentFD, err := syscall.Openat(int(parent.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("retain live-root lock parent descriptor: %w", err)
	}
	retainedParent := os.NewFile(uintptr(parentFD), "live-root-lock-parent")
	fd, err := syscall.Openat(parentFD, name,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		_ = retainedParent.Close()
		return fmt.Errorf("open descriptor-anchored live-root lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	closeLocal := func() { _ = file.Close(); _ = retainedParent.Close() }
	if err := validateLockFile(file); err != nil {
		closeLocal()
		return err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeLocal()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrLiveRootLockBusy
		}
		return fmt.Errorf("flock live-root repository: %w", err)
	}
	if err := parent.Sync(); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		closeLocal()
		return fmt.Errorf("sync live-root lock directory: %w", err)
	}
	lockID, err := identifyRegular(file)
	if err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		closeLocal()
		return err
	}
	lock := &liveRootLock{rootID: rootID, parentID: parentID, lockID: lockID, name: name, parent: retainedParent, file: file}
	if err := validateLiveRootLock(lock, root, parent); err != nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		closeLocal()
		return err
	}
	r.liveLocks[key] = lock
	return nil
}

func validateLiveRootLock(lock *liveRootLock, root, parent *os.File) error {
	if lock == nil || lock.file == nil || lock.parent == nil {
		return ErrLiveRootLockState
	}
	rootID, err := identifyDirectory(root)
	if err != nil || !sameDirectoryIdentity(rootID, lock.rootID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	parentID, err := identifyDirectory(parent)
	if err != nil || !sameDirectoryIdentity(parentID, lock.parentID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	retainedParentID, err := identifyDirectory(lock.parent)
	if err != nil || !sameDirectoryIdentity(retainedParentID, lock.parentID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	heldID, err := identifyRegular(lock.file)
	if err != nil || !sameIdentity(lock.lockID, heldID) {
		return errors.Join(ErrLiveRootLockState, err)
	}
	fd, err := syscall.Openat(int(lock.parent.Fd()), lock.name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.Join(ErrLiveRootLockState, err)
	}
	named := os.NewFile(uintptr(fd), lock.name)
	namedID, statErr := identifyRegular(named)
	_ = named.Close()
	if statErr != nil || !sameIdentity(lock.lockID, namedID) {
		return errors.Join(ErrLiveRootLockState, statErr)
	}
	return nil
}

func releaseLiveRootLock(lock *liveRootLock) error {
	if lock == nil {
		return nil
	}
	var unlockErr, fileErr, parentErr error
	if lock.file != nil {
		unlockErr = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
		fileErr = lock.file.Close()
	}
	if lock.parent != nil {
		parentErr = lock.parent.Close()
	}
	return errors.Join(unlockErr, fileErr, parentErr)
}

func liveRootLockKey(rootID fileIdentity) string {
	return fmt.Sprintf("%016x:%016x", rootID.Device, rootID.Inode)
}

func liveRootLockName(rootID fileIdentity) string {
	return fmt.Sprintf(".tbound-publication-live-%016x-%016x.lock", rootID.Device, rootID.Inode)
}

func chainContainsOriginProposal(transitions []delta.Transition, proposalID string) bool {
	for _, transition := range transitions {
		if transition.Operation.Kind == delta.OperationProposalCall && transition.Operation.ProposalID == proposalID {
			return true
		}
	}
	return false
}

func validateTrustedArtifactParent(parent *os.File) error {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(parent.Fd()), &info); err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || (info.Uid != 0 && info.Uid != uint32(syscall.Geteuid())) || info.Mode&0o022 != 0 {
		return errors.New("target-filesystem staging parent must be root/supervisor-owned and not group/other writable")
	}
	return nil
}

func digestCanonical(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeCanonical(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxRecordBytes {
		return errors.New("record exceeds publication size bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("record has trailing JSON data")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytesEqual(canonical, data) {
		return errors.New("record is not canonical JSON")
	}
	return nil
}

func writeFull(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if n < 0 || n > len(data) {
			return errors.New("invalid write count")
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func unlinkAt(directoryFD int, name string, flags int) error {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(directoryFD), uintptr(unsafe.Pointer(path)), uintptr(flags))
	runtime.KeepAlive(path)
	if errno != 0 {
		return errno
	}
	return nil
}

func randomLabel(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func injectFault(inject FaultInjector, point string, index int) error {
	if inject == nil {
		return nil
	}
	if err := inject(point, index); err != nil {
		return fmt.Errorf("%w at %s object=%d: %v", ErrInjectedFault, point, index, err)
	}
	return nil
}

func validateRelativePath(path string) error {
	if path == "" || len(path) > delta.MaxPathBytes || !utf8.ValidString(path) || strings.HasPrefix(path, "/") || strings.ContainsRune(path, '\\') {
		return delta.ErrInvalidChain
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return delta.ErrInvalidChain
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return delta.ErrInvalidChain
			}
		}
	}
	return nil
}

func validIdentity(value string) bool {
	if value == "" || len(value) > delta.MaxIdentityBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validTransactionID(value string) bool {
	if len(value) != len("pub-")+32 || !strings.HasPrefix(value, "pub-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "pub-"))
	return err == nil
}

func splitParent(path string) (string, string) {
	index := strings.LastIndexByte(path, '/')
	if index < 0 {
		return "", path
	}
	return path[:index], path[index+1:]
}

func pathBase(path string) string { _, base := splitParent(path); return base }
func pathDepth(path string) int   { return strings.Count(path, "/") + 1 }

func sameManifest(left, right delta.TreeManifest) bool {
	if left.MetadataPolicyDigest != right.MetadataPolicyDigest || len(left.Objects) != len(right.Objects) {
		return false
	}
	for i := range left.Objects {
		if left.Objects[i] != right.Objects[i] {
			return false
		}
	}
	return true
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func resultFromTerminal(prep prepareRecord, terminal terminalRecord) Result {
	return Result{TransactionID: prep.TransactionID, PublicationID: prep.PublicationID, Status: terminal.Status,
		Objects: append([]ObjectResult(nil), terminal.Objects...), Binding: prep.Binding}
}

func (r *Repository) appendProgress(progress progressRecord) error {
	return r.appendRecord("publication_checkpoint", progress.TransactionID+"/checkpoint", progress)
}

func (r *Repository) appendRecord(kind, id string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return fmt.Errorf("publication record exceeds %d-byte bound", maxRecordBytes)
	}
	if _, err := r.options.Journal.Append(audit.Event{Kind: kind, ID: id, Data: data}); err != nil {
		return fmt.Errorf("durably append %s: %w", kind, err)
	}
	return nil
}

func readTransactions(trace audit.Trace, workflow string) ([]txView, error) {
	byID := map[string]*txView{}
	usedTokens, usedDecisions := map[string]bool{}, map[string]bool{}
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "publication_prepare":
			var prep prepareRecord
			if err := decodeCanonical(record.Event.Data, &prep); err != nil {
				return nil, fmt.Errorf("decode publication prepare sequence %d: %w", record.Sequence, err)
			}
			if prep.SchemaVersion != publicationSchema || prep.TransactionID != record.Event.ID || !validTransactionID(prep.TransactionID) ||
				!validIdentity(prep.WorkflowID) || !validIdentity(prep.PublicationID) || !validIdentity(prep.OriginProposalID) ||
				prep.OwnerEpoch == 0 || prep.ArtifactName != ".tbound-publication-"+strings.TrimPrefix(prep.TransactionID, "pub-") ||
				len(prep.Plans) > maxPlanObjects || prep.Binding.WorkflowID != prep.WorkflowID || prep.Binding.OwnerEpoch != prep.OwnerEpoch ||
				prep.Binding.PublicationID != prep.PublicationID || prep.Binding.OriginProposalID != prep.OriginProposalID {
				return nil, fmt.Errorf("invalid publication prepare record at sequence %d", record.Sequence)
			}
			bindingDigest, err := computeBindingDigest(prep.Binding)
			if err != nil || bindingDigest != prep.Binding.BindingDigest || !validSHA256(prep.Binding.ChainDigest) ||
				!validSHA256(prep.Binding.DeltaDigest) || !validSHA256(prep.Binding.PolicyDigest) || !validSHA256(prep.Binding.MetadataPolicyDigest) {
				return nil, errors.New("publication prepare binding digest or commitment is invalid")
			}
			if _, exists := byID[prep.TransactionID]; exists {
				return nil, errors.New("duplicate publication transaction ID")
			}
			for i, plan := range prep.Plans {
				if plan.Index != i || validateRelativePath(plan.Path) != nil || strings.HasPrefix(pathBase(plan.Path), ".tbound-publication-") ||
					!validPlan(plan) {
					return nil, errors.New("malformed publication object plan")
				}
			}
			byID[prep.TransactionID] = &txView{prepare: prep, progress: map[int][]progressRecord{}}
		case "publication_checkpoint":
			var progress progressRecord
			if err := decodeCanonical(record.Event.Data, &progress); err != nil {
				return nil, fmt.Errorf("decode publication checkpoint %d: %w", record.Sequence, err)
			}
			view := byID[progress.TransactionID]
			if view == nil || progress.Index < -1 || progress.Index >= len(view.prepare.Plans) {
				return nil, errors.New("publication checkpoint has no matching prepare record")
			}
			if !validProgressStep(progress.Step) {
				return nil, errors.New("unknown publication checkpoint step")
			}
			view.progress[progress.Index] = append(view.progress[progress.Index], progress)
			if progress.Index == -1 && progress.Step == "artifact-ready" && progress.ObjectIdentity != nil {
				if view.artifactID != nil || progress.ObjectIdentity.Mode&syscall.S_IFMT != syscall.S_IFDIR {
					return nil, errors.New("duplicate or malformed artifact identity checkpoint")
				}
				view.artifactID = progress.ObjectIdentity
			}
		case "publication_token_consumed":
			var token tokenRecord
			if err := decodeCanonical(record.Event.Data, &token); err != nil {
				return nil, fmt.Errorf("decode publication token %d: %w", record.Sequence, err)
			}
			view := byID[token.TransactionID]
			if view == nil || view.token != nil || !validIdentity(token.TokenID) || !validIdentity(token.DecisionID) ||
				token.BindingDigest != view.prepare.Binding.BindingDigest || token.Binding != view.prepare.Binding ||
				usedTokens[token.TokenID] || usedDecisions[token.DecisionID] {
				return nil, errors.New("publication token is duplicate or not bound to its exact prepare record")
			}
			usedTokens[token.TokenID], usedDecisions[token.DecisionID] = true, true
			view.token = &token
		case "publication_terminal", "publication_aborted":
			var terminal terminalRecord
			if err := decodeCanonical(record.Event.Data, &terminal); err != nil {
				return nil, fmt.Errorf("decode publication terminal %d: %w", record.Sequence, err)
			}
			view := byID[terminal.TransactionID]
			if view == nil || view.terminal != nil || (record.Event.Kind == "publication_terminal" && view.token == nil) ||
				(record.Event.Kind == "publication_aborted" && view.token != nil) || !validStatus(terminal.Status) || len(terminal.Objects) != len(view.prepare.Plans) {
				return nil, errors.New("publication terminal record violates transaction authority ordering")
			}
			for i, object := range terminal.Objects {
				if object.Path != view.prepare.Plans[i].Path || !validObjectStatus(object.Status) {
					return nil, errors.New("publication terminal has mismatched object results")
				}
			}
			view.terminal = &terminal
		}
	}
	result := make([]txView, 0, len(byID))
	for _, tx := range byID {
		if tx.prepare.WorkflowID == workflow {
			result = append(result, *tx)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].prepare.TransactionID < result[j].prepare.TransactionID })
	return result, nil
}

func readOneTransaction(journal *audit.Journal, workflow, id string) (txView, error) {
	trace, err := journal.Trace()
	if err != nil {
		return txView{}, err
	}
	transactions, err := readTransactions(trace, workflow)
	if err != nil {
		return txView{}, err
	}
	for _, tx := range transactions {
		if tx.prepare.TransactionID == id {
			return tx, nil
		}
	}
	return txView{}, errors.New("publication transaction does not exist")
}

func validProgressStep(step string) bool {
	switch step {
	case "artifact-ready", "staged", "parent-bound", "object-start", "applied":
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusUnknown
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	encoded := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func validPlan(plan objectPlan) bool {
	if plan.Before.Exists {
		if plan.Before.Type != delta.ObjectRegular || !validSHA256(plan.Before.ContentDigest) || !validSHA256(plan.Before.MetadataIdentity) ||
			plan.BeforeID == nil || plan.BeforeID.Mode&syscall.S_IFMT != syscall.S_IFREG || plan.BeforeID.Links != 1 {
			return false
		}
	} else if plan.Before != (delta.ObjectState{}) || plan.BeforeID != nil {
		return false
	}
	if plan.After.Exists {
		if !validSHA256(plan.After.MetadataIdentity) {
			return false
		}
		switch plan.After.Type {
		case delta.ObjectRegular:
			if !validSHA256(plan.After.ContentDigest) || plan.SourceID == nil || plan.SourceID.Mode&syscall.S_IFMT != syscall.S_IFREG || plan.SourceID.Links != 1 ||
				plan.StageName != fmt.Sprintf("stage-%04d", plan.Index) {
				return false
			}
		case delta.ObjectDirectory:
			if plan.Before.Exists || plan.After.ContentDigest != "" || plan.SourceID != nil || plan.StageName != "" {
				return false
			}
		default:
			return false
		}
	} else if plan.After != (delta.ObjectState{}) || plan.SourceID != nil || plan.StageName != "" {
		return false
	}
	if plan.Before.Exists {
		if plan.BackupName != fmt.Sprintf("original-%04d", plan.Index) {
			return false
		}
	} else if plan.BackupName != "" {
		return false
	}
	switch plan.Operation {
	case "create":
		return !plan.Before.Exists && plan.After.Exists && plan.After.Type == delta.ObjectRegular && plan.SourcePath == ""
	case "overwrite":
		return plan.Before.Exists && plan.After.Exists && plan.After.Type == delta.ObjectRegular && plan.SourcePath == ""
	case "replacement":
		return plan.Before.Exists && plan.After.Exists && plan.After.Type == delta.ObjectRegular &&
			plan.Before.MetadataIdentity != plan.After.MetadataIdentity && plan.SourcePath == ""
	case "delete":
		return plan.Before.Exists && !plan.After.Exists && plan.SourcePath == ""
	case "rename":
		return !plan.Before.Exists && plan.After.Exists && plan.After.Type == delta.ObjectRegular && validateRelativePath(plan.SourcePath) == nil
	case "directory_create":
		return !plan.Before.Exists && plan.After.Exists && plan.After.Type == delta.ObjectDirectory && plan.SourcePath == ""
	default:
		return false
	}
}

func validObjectStatus(status ObjectStatus) bool {
	return status == ObjectSucceeded || status == ObjectFailed || status == ObjectUnknown
}
