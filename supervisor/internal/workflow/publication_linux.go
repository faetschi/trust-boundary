//go:build linux

// Package workflow composes a session repository, a trusted runtime
// attestation, the supervisor-only workspace_commit gate, and recoverable
// publication. It has no provider or presentation dependency.
package workflow

import (
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
	"unicode"
	"unicode/utf8"

	"github.com/gowebpki/jcs"
	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

var (
	ErrAdmissionClosed = errors.New("workflow proposal admission is closed")
	ErrRootQuarantined = errors.New("live root has an unresolved publication lifecycle record")
	ErrRootActive      = errors.New("live root has an active in-process publication owner")
	ErrFinalizerConfig = errors.New("publication finalizer is missing a required trusted dependency")
	ErrLifecycleReplay = errors.New("workflow publication identity was already registered for this live root")
)

type RootIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type rootPendingRecord struct {
	Root          RootIdentity      `json:"root"`
	WorkflowID    string            `json:"workflow_id"`
	OwnerEpoch    uint64            `json:"owner_epoch"`
	PublicationID string            `json:"publication_id"`
	Workspace     workspace.Options `json:"workspace"`
}

type rootTransactionRecord struct {
	Root          RootIdentity `json:"root"`
	WorkflowID    string       `json:"workflow_id"`
	PublicationID string       `json:"publication_id"`
	TransactionID string       `json:"transaction_id"`
}

type rootResolvedRecord struct {
	Root          RootIdentity       `json:"root"`
	WorkflowID    string             `json:"workflow_id"`
	PublicationID string             `json:"publication_id"`
	TransactionID string             `json:"transaction_id,omitempty"`
	Status        publication.Status `json:"status"`
	Reason        string             `json:"reason,omitempty"`
}

type pendingRoot struct {
	rootPendingRecord
	transactionID string
}

// RootLedger is the durable cross-workflow quarantine ledger. All workflow
// repositories for a registered live root must use this same supervisor-owned
// audit journal; audit.Open's process-wide flock serializes independent owners.
type RootLedger struct {
	state   *rootLedgerState
	journal *audit.Journal
}

func (l *RootLedger) uses(journal *audit.Journal) bool { return l != nil && l.journal == journal }

type rootLedgerState struct {
	mu     sync.Mutex
	active map[RootIdentity]*RootReservation
}

// RootReservation marks one in-process owner while it may still be publishing.
// It is deliberately not durable: after process death, the audit journal's OS
// flock prevents a replacement process from inspecting/recovering until the
// old owner is gone.
type RootReservation struct {
	state *rootLedgerState
	root  RootIdentity
	key   string
	once  sync.Once
}

func (r *RootReservation) Release() {
	if r == nil || r.state == nil {
		return
	}
	r.once.Do(func() {
		r.state.mu.Lock()
		if r.state.active[r.root] == r {
			delete(r.state.active, r.root)
		}
		r.state.mu.Unlock()
	})
}

var rootLedgerJournalStates sync.Map // map[*audit.Journal]*rootLedgerState

func NewRootLedger(journal *audit.Journal) (*RootLedger, error) {
	if journal == nil {
		return nil, errors.New("workflow root ledger requires a shared durable audit journal")
	}
	state, _ := rootLedgerJournalStates.LoadOrStore(journal, &rootLedgerState{active: make(map[RootIdentity]*RootReservation)})
	return &RootLedger{journal: journal, state: state.(*rootLedgerState)}, nil
}

// RecoverBeforeAdmission reconciles every unresolved publication registered
// against this live-root inode. If a durable publication prepare exists, the
// owning workflow repository is opened through the trusted resolver and the
// publication API observes it. UNKNOWN remains quarantined. A pending marker
// with no prepare is safe to resolve as no live effect can precede prepare.
func (l *RootLedger) RecoverBeforeAdmission(ctx context.Context, liveRoot *os.File, open RecoveryRepositoryOpener) error {
	if l == nil || l.journal == nil || l.state == nil || liveRoot == nil || open == nil || ctx == nil {
		return ErrFinalizerConfig
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := identifyRoot(liveRoot)
	if err != nil {
		return err
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.state.active[root] != nil {
		return fmt.Errorf("%w: %w", ErrRootQuarantined, ErrRootActive)
	}
	pending, trace, err := l.pendingForRoot(root)
	if err != nil {
		return err
	}
	for _, item := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		txID, terminal, err := findPublication(trace, item.WorkflowID, item.OwnerEpoch, item.PublicationID, item.transactionID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRootQuarantined, err)
		}
		if txID == "" {
			if err := l.resolve(item, "", publication.StatusFailed, "no publication prepare exists; no live effect could have begun"); err != nil {
				return err
			}
			continue
		}
		if terminal != "" {
			if terminal == publication.StatusUnknown {
				return fmt.Errorf("%w: workflow %s transaction %s is UNKNOWN", ErrRootQuarantined, item.WorkflowID, txID)
			}
			if err := l.resolve(item, txID, terminal, "recovered existing durable terminal record"); err != nil {
				return err
			}
			continue
		}
		repository, err := open(item.WorkflowID)
		if err != nil {
			return fmt.Errorf("%w: open recovery repository for workflow %s: %v", ErrRootQuarantined, item.WorkflowID, err)
		}
		if isNilRecoveryRepository(repository) {
			return fmt.Errorf("%w: recovery opener returned no repository for workflow %s", ErrRootQuarantined, item.WorkflowID)
		}
		binding, bound := repository.Binding()
		if !bound || binding.WorkflowID != item.WorkflowID || binding.OwnerEpoch <= item.OwnerEpoch ||
			binding.Journal != l.journal || binding.Workspace != item.Workspace {
			closeErr := repository.Close()
			return errors.Join(fmt.Errorf("%w: recovery repository binding does not match registered workflow %s", ErrRootQuarantined, item.WorkflowID), closeErr)
		}
		result, recoverErr := repository.Recover(liveRoot, txID, nil)
		closeErr := repository.Close()
		if closeErr != nil {
			return errors.Join(fmt.Errorf("close publication recovery repository: %w", closeErr), recoverErr)
		}
		if result.Status == publication.StatusUnknown || recoverErr != nil {
			return fmt.Errorf("%w: workflow %s transaction %s recovery=%s: %v", ErrRootQuarantined, item.WorkflowID, txID, result.Status, recoverErr)
		}
		if err := l.resolve(item, txID, result.Status, "reconciled by publication.Recover"); err != nil {
			return err
		}
	}
	remaining, _, err := l.pendingForRoot(root)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return ErrRootQuarantined
	}
	return nil
}

func (l *RootLedger) Begin(liveRoot *os.File, workflowID string, ownerEpoch uint64, publicationID string, workspaceOptions workspace.Options) (*RootReservation, error) {
	if l == nil || l.journal == nil || l.state == nil || liveRoot == nil || !validWorkflowIdentity(workflowID) ||
		ownerEpoch == 0 || !validWorkflowIdentity(publicationID) || !validWorkspaceBinding(workspaceOptions) {
		return nil, ErrFinalizerConfig
	}
	root, err := identifyRoot(liveRoot)
	if err != nil {
		return nil, err
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.state.active[root] != nil {
		return nil, fmt.Errorf("%w: %w", ErrRootQuarantined, ErrRootActive)
	}
	pending, trace, err := l.pendingForRoot(root)
	if err != nil {
		return nil, err
	}
	if len(pending) != 0 {
		return nil, ErrRootQuarantined
	}
	for _, frame := range trace.Records {
		if frame.Event.Kind != "workflow_publication_pending" {
			continue
		}
		var prior rootPendingRecord
		if err := decodeRootRecord(frame.Event.Data, &prior); err != nil {
			return nil, err
		}
		if prior.Root == root && prior.WorkflowID == workflowID && prior.PublicationID == publicationID {
			return nil, ErrLifecycleReplay
		}
	}
	record := rootPendingRecord{Root: root, WorkflowID: workflowID, OwnerEpoch: ownerEpoch,
		PublicationID: publicationID, Workspace: workspaceOptions}
	encoded, _ := json.Marshal(record)
	_, err = l.journal.Append(audit.Event{Kind: "workflow_publication_pending", ID: workflowEventID(record.Root, workflowID, publicationID), Data: encoded})
	if err != nil {
		return nil, err
	}
	reservation := &RootReservation{state: l.state, root: root, key: workflowEventID(root, workflowID, publicationID)}
	l.state.active[root] = reservation
	return reservation, nil
}

func (l *RootLedger) RecordTransaction(reservation *RootReservation, liveRoot *os.File, workflowID, publicationID, transactionID string) error {
	if transactionID == "" {
		return nil
	}
	if l == nil || l.journal == nil || l.state == nil || reservation == nil || liveRoot == nil ||
		!validWorkflowIdentity(workflowID) || !validWorkflowIdentity(publicationID) || !validWorkflowIdentity(transactionID) {
		return ErrFinalizerConfig
	}
	root, err := identifyRoot(liveRoot)
	if err != nil {
		return err
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.state.active[root] != reservation || reservation.state != l.state || reservation.root != root ||
		reservation.key != workflowEventID(root, workflowID, publicationID) {
		return ErrRootQuarantined
	}
	pending, _, err := l.pendingForRoot(root)
	if err != nil {
		return err
	}
	found := false
	for _, item := range pending {
		if item.WorkflowID == workflowID && item.PublicationID == publicationID {
			if item.transactionID != "" {
				return ErrRootQuarantined
			}
			found = true
			break
		}
	}
	if !found {
		return ErrRootQuarantined
	}
	encoded, _ := json.Marshal(rootTransactionRecord{Root: root, WorkflowID: workflowID, PublicationID: publicationID, TransactionID: transactionID})
	_, err = l.journal.Append(audit.Event{Kind: "workflow_publication_transaction", ID: workflowEventID(root, workflowID, publicationID) + "/transaction", Data: encoded})
	return err
}

func (l *RootLedger) Resolve(reservation *RootReservation, liveRoot *os.File, workflowID, publicationID, transactionID string, status publication.Status) error {
	if l == nil || l.journal == nil || l.state == nil || reservation == nil || liveRoot == nil || !validWorkflowIdentity(workflowID) || !validWorkflowIdentity(publicationID) ||
		status != publication.StatusSucceeded && status != publication.StatusFailed {
		return ErrRootQuarantined
	}
	root, err := identifyRoot(liveRoot)
	if err != nil {
		return err
	}
	l.state.mu.Lock()
	defer l.state.mu.Unlock()
	if l.state.active[root] != reservation || reservation.state != l.state || reservation.root != root ||
		reservation.key != workflowEventID(root, workflowID, publicationID) {
		return ErrRootQuarantined
	}
	pending, _, err := l.pendingForRoot(root)
	if err != nil {
		return err
	}
	for _, item := range pending {
		if item.WorkflowID == workflowID && item.PublicationID == publicationID {
			if item.transactionID != "" && item.transactionID != transactionID {
				return ErrRootQuarantined
			}
			return l.resolve(item, transactionID, status, "publication returned a known terminal status")
		}
	}
	return ErrRootQuarantined
}

func (l *RootLedger) pendingForRoot(root RootIdentity) ([]pendingRoot, audit.Trace, error) {
	trace, err := l.journal.Trace()
	if err != nil {
		return nil, audit.Trace{}, err
	}
	pending := map[string]pendingRoot{}
	for _, frame := range trace.Records {
		switch frame.Event.Kind {
		case "workflow_publication_pending":
			var record rootPendingRecord
			if err := decodeRootRecord(frame.Event.Data, &record); err != nil || record.Root == (RootIdentity{}) ||
				!validWorkflowIdentity(record.WorkflowID) || record.OwnerEpoch == 0 || !validWorkflowIdentity(record.PublicationID) ||
				!validWorkspaceBinding(record.Workspace) {
				return nil, trace, fmt.Errorf("invalid workflow root-pending record at audit sequence %d", frame.Sequence)
			}
			if record.Root == root {
				key := workflowEventID(record.Root, record.WorkflowID, record.PublicationID)
				if _, duplicate := pending[key]; duplicate {
					return nil, trace, errors.New("duplicate unresolved workflow root registration")
				}
				pending[key] = pendingRoot{rootPendingRecord: record}
			}
		case "workflow_publication_transaction":
			var record rootTransactionRecord
			if err := decodeRootRecord(frame.Event.Data, &record); err != nil || !validWorkflowIdentity(record.TransactionID) {
				return nil, trace, fmt.Errorf("invalid workflow root transaction record at audit sequence %d", frame.Sequence)
			}
			if record.Root == root {
				key := workflowEventID(record.Root, record.WorkflowID, record.PublicationID)
				item, ok := pending[key]
				if !ok || item.transactionID != "" {
					return nil, trace, errors.New("orphan or duplicate workflow root transaction record")
				}
				item.transactionID = record.TransactionID
				pending[key] = item
			}
		case "workflow_publication_resolved":
			var record rootResolvedRecord
			if err := decodeRootRecord(frame.Event.Data, &record); err != nil || record.Root == (RootIdentity{}) ||
				!validWorkflowIdentity(record.WorkflowID) || !validWorkflowIdentity(record.PublicationID) ||
				(record.Status != publication.StatusSucceeded && record.Status != publication.StatusFailed) {
				return nil, trace, fmt.Errorf("invalid workflow root-resolved record at audit sequence %d", frame.Sequence)
			}
			if record.Root == root {
				key := workflowEventID(record.Root, record.WorkflowID, record.PublicationID)
				item, exists := pending[key]
				if !exists || item.transactionID != "" && item.transactionID != record.TransactionID {
					return nil, trace, errors.New("orphan or mismatched workflow root resolution record")
				}
				delete(pending, key)
			}
		}
	}
	items := make([]pendingRoot, 0, len(pending))
	for _, item := range pending {
		items = append(items, item)
	}
	return items, trace, nil
}

func (l *RootLedger) resolve(item pendingRoot, transactionID string, status publication.Status, reason string) error {
	root := item.Root
	encoded, _ := json.Marshal(rootResolvedRecord{Root: root, WorkflowID: item.WorkflowID,
		PublicationID: item.PublicationID, TransactionID: transactionID, Status: status, Reason: reason})
	_, err := l.journal.Append(audit.Event{Kind: "workflow_publication_resolved",
		ID: workflowEventID(root, item.WorkflowID, item.PublicationID) + "/resolved", Data: encoded})
	return err
}

type publicationPrepareIndex struct {
	TransactionID string `json:"transaction_id"`
	WorkflowID    string `json:"workflow_id"`
	OwnerEpoch    uint64 `json:"owner_epoch"`
	PublicationID string `json:"publication_id"`
}

type publicationTerminalIndex struct {
	TransactionID string             `json:"transaction_id"`
	Status        publication.Status `json:"status"`
}

func findPublication(trace audit.Trace, workflowID string, ownerEpoch uint64, publicationID, knownTransactionID string) (string, publication.Status, error) {
	txID := knownTransactionID
	terminals := map[string]publication.Status{}
	for _, frame := range trace.Records {
		switch frame.Event.Kind {
		case "publication_prepare":
			var prepare publicationPrepareIndex
			if err := json.Unmarshal(frame.Event.Data, &prepare); err != nil {
				return "", "", errors.New("cannot decode publication prepare while recovering root registry")
			}
			if prepare.WorkflowID == workflowID && prepare.PublicationID == publicationID {
				if prepare.OwnerEpoch != ownerEpoch || prepare.TransactionID == "" {
					return "", "", errors.New("publication prepare owner does not match the registered root lifecycle")
				}
				if txID != "" && txID != prepare.TransactionID {
					return "", "", errors.New("multiple publication prepares match one registered root commit")
				}
				txID = prepare.TransactionID
			}
		case "publication_terminal", "publication_aborted":
			var terminal publicationTerminalIndex
			if err := json.Unmarshal(frame.Event.Data, &terminal); err != nil || terminal.TransactionID == "" {
				return "", "", errors.New("cannot decode publication terminal while recovering root registry")
			}
			terminals[terminal.TransactionID] = terminal.Status
		}
	}
	if txID == "" {
		return "", "", nil
	}
	return txID, terminals[txID], nil
}

type RecoveryRepository interface {
	Binding() (publication.RepositoryBinding, bool)
	Recover(*os.File, string, publication.FaultInjector) (publication.Result, error)
	Close() error
}

type RecoveryRepositoryOpener func(workflowID string) (RecoveryRepository, error)

type Admission struct {
	mu       sync.Mutex
	open     bool
	closed   bool
	inFlight int
	drained  chan struct{}
}

func NewAdmission() *Admission { return &Admission{} }

func (a *Admission) Activate() error {
	if a == nil {
		return ErrAdmissionClosed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrAdmissionClosed
	}
	a.open = true
	return nil
}

func (a *Admission) Do(ctx context.Context, effect func() error) error {
	if effect == nil {
		return ErrAdmissionClosed
	}
	release, err := a.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return effect()
}

func (a *Admission) Acquire(ctx context.Context) (func(), error) {
	if a == nil || ctx == nil {
		return nil, ErrAdmissionClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if !a.open || a.closed {
		a.mu.Unlock()
		return nil, ErrAdmissionClosed
	}
	a.inFlight++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.inFlight--
			if a.closed && a.inFlight == 0 && a.drained != nil {
				close(a.drained)
				a.drained = nil
			}
			a.mu.Unlock()
		})
	}, nil
}

func (a *Admission) CloseAndWait(ctx context.Context) error {
	if a == nil || ctx == nil {
		return ErrAdmissionClosed
	}
	a.mu.Lock()
	a.open, a.closed = false, true
	if a.inFlight == 0 {
		a.mu.Unlock()
		return nil
	}
	if a.drained == nil {
		a.drained = make(chan struct{})
	}
	drained := a.drained
	a.mu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type EvidenceRequest struct {
	Bundle     sessionrepo.EvidenceBundle
	Verified   delta.Result
	LiveRoot   *os.File
	Workflow   string
	OwnerEpoch uint64
}

// Trust is supplied only by the trusted host composition root. A nil or
// incomplete implementation prevents admission/finalization; a browser or
// model decision is never accepted as an implementation of this interface.
type Trust interface {
	CheckAdmission(context.Context) error
	CheckPublicationPrerequisites(context.Context, EvidenceRequest) error
	AttestQuiescent(context.Context, *os.File, *sessionrepo.Generation, *sessionrepo.Generation) error
	VerifyTransition(context.Context, delta.PolicyDecision, delta.TransitionBinding) error
	VerifyOrigin(context.Context, sessionrepo.EvidenceBundle, delta.Transition) error
}

type FinalizerOptions struct {
	WorkflowID      string
	OwnerEpoch      uint64
	PublicationID   string
	LiveRoot        *os.File
	Store           *sessionrepo.Store
	Baseline        *sessionrepo.Generation
	CurrentTip      func() *sessionrepo.Generation
	Workspace       workspace.Options
	Journal         *audit.Journal
	Ledger          *RootLedger
	OpenPublication func() (*publication.Repository, error)
	OpenRecovery    RecoveryRepositoryOpener
	Trust           Trust
	FaultInjector   publication.FaultInjector
}

type Finalizer struct {
	options         FinalizerOptions
	admission       *Admission
	commit          *gate.InternalCommitGate
	repo            *publication.Repository
	mu              sync.Mutex
	prepared        bool
	finished        bool
	finalizeStarted bool
	evidence        sessionrepo.EvidenceBundle
	origin          delta.Transition
}

func NewFinalizer(options FinalizerOptions) (*Finalizer, error) {
	if !validWorkflowIdentity(options.WorkflowID) || !validWorkflowIdentity(options.PublicationID) || options.OwnerEpoch == 0 ||
		options.LiveRoot == nil || options.Store == nil || options.Baseline == nil || options.CurrentTip == nil ||
		options.Journal == nil || options.Ledger == nil || !options.Ledger.uses(options.Journal) ||
		options.OpenPublication == nil || options.OpenRecovery == nil || options.Trust == nil ||
		options.Workspace.Limits.MaxObjects <= 0 || options.Workspace.MetadataPolicyDigest == "" ||
		!options.Workspace.XattrVisibility.Complete || options.Workspace.XattrVisibility.ProfileDigest == "" {
		return nil, ErrFinalizerConfig
	}
	commit, err := gate.NewInternalCommitGate(options.Journal)
	if err != nil {
		return nil, err
	}
	return &Finalizer{options: options, admission: NewAdmission(), commit: commit}, nil
}

func (f *Finalizer) Admission() *Admission {
	if f == nil {
		return nil
	}
	return f.admission
}

// RecoverBeforeAdmission must complete before the host starts IPC proposal
// handling. It reconciles registered old work first, then obtains the current
// workflow publication repository, then asks the trusted runtime to admit this
// session profile. No proposal is admitted on any failure.
func (f *Finalizer) RecoverBeforeAdmission(ctx context.Context) error {
	if f == nil || ctx == nil {
		return ErrFinalizerConfig
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepared {
		return nil
	}
	if err := f.options.Ledger.RecoverBeforeAdmission(ctx, f.options.LiveRoot, f.options.OpenRecovery); err != nil {
		return err
	}
	repository, err := f.options.OpenPublication()
	if err != nil {
		return fmt.Errorf("open current publication repository after recovery: %w", err)
	}
	if repository == nil {
		return errors.New("open current publication repository returned nil without an error")
	}
	binding, bound := repository.Binding()
	if !bound || binding.WorkflowID != f.options.WorkflowID || binding.OwnerEpoch != f.options.OwnerEpoch ||
		binding.Journal != f.options.Journal || binding.Workspace != f.options.Workspace {
		closeErr := repository.Close()
		return errors.Join(errors.New("current publication repository binding does not match finalizer workflow, owner epoch, journal, and workspace"), closeErr)
	}
	if err := f.options.Trust.CheckAdmission(ctx); err != nil {
		_ = repository.Close()
		return fmt.Errorf("trusted workflow admission refused: %w", err)
	}
	f.repo, f.prepared = repository, true
	return nil
}

// Finalize closes proposal admission and drains all accepted effects before
// verifying the real sessionrepo tip. It then attests quiescence, authorizes a
// supervisor-only workspace_commit, publishes, and resolves the durable root
// lifecycle record only for a known terminal status.
func (f *Finalizer) Finalize(ctx context.Context) (publication.Result, error) {
	if f == nil || ctx == nil {
		return publication.Result{}, ErrFinalizerConfig
	}
	if err := f.admission.CloseAndWait(ctx); err != nil {
		return publication.Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.prepared || f.repo == nil || f.finished {
		return publication.Result{}, ErrFinalizerConfig
	}
	if f.finalizeStarted {
		return publication.Result{}, errors.New("workflow publication finalization was already attempted")
	}
	f.finalizeStarted = true
	tip := f.options.CurrentTip()
	if tip == nil {
		return publication.Result{}, errors.New("workflow finalizer has no current durable generation")
	}
	if err := f.options.Trust.AttestQuiescent(ctx, f.options.LiveRoot, f.options.Baseline, tip); err != nil {
		return publication.Result{}, fmt.Errorf("trusted quiescence attestation refused publication: %w", err)
	}
	verified, err := f.options.Store.Verify()
	if err != nil {
		return publication.Result{}, fmt.Errorf("verify session repository before publication: %w", err)
	}
	evidence, err := f.options.Store.Evidence()
	if err != nil {
		return publication.Result{}, fmt.Errorf("read verified session evidence: %w", err)
	}
	bundle, err := f.options.Store.EvidenceBundle()
	if err != nil {
		return publication.Result{}, fmt.Errorf("read reconstructable session evidence: %w", err)
	}
	if !reflect.DeepEqual(verified, evidence.ValidatedChain) || !reflect.DeepEqual(verified, bundle.ValidatedChain) ||
		evidence.Baseline.ID != f.options.Baseline.ID() || evidence.Baseline.TreeDigest != f.options.Baseline.TreeDigest() ||
		len(evidence.Generations) == 0 || evidence.Generations[len(evidence.Generations)-1].ID != tip.ID() ||
		evidence.Generations[len(evidence.Generations)-1].TreeDigest != tip.TreeDigest() ||
		!reflect.DeepEqual(evidence.ApprovedDeltaLedger, bundle.ApprovedDeltaLedger) ||
		evidence.PolicyDigest == "" || evidence.MetadataPolicyDigest != f.options.Workspace.MetadataPolicyDigest {
		return publication.Result{}, errors.New("session Verify/Evidence/bundle do not identify the same baseline, approved ledger, and current tip")
	}
	if verified.TransitionCount == 0 || len(evidence.ApprovedDeltaLedger) == 0 {
		return publication.Result{}, errors.New("publication requires at least one approved mutation and an authenticated origin")
	}
	origin := evidence.ApprovedDeltaLedger[len(evidence.ApprovedDeltaLedger)-1]
	if origin.Operation.Kind != delta.OperationProposalCall || origin.Operation.ProposalID == "" {
		return publication.Result{}, errors.New("terminal workflow transition has no proposal-call origin; lease-only publication fails closed")
	}
	for _, transition := range evidence.ApprovedDeltaLedger {
		if err := f.options.Trust.VerifyTransition(ctx, transition.Decision, delta.TransitionBinding{
			ID: transition.ID, Sequence: transition.Sequence, Tool: transition.Tool,
			ArgumentDigest: transition.ArgumentDigest, ViewID: transition.ViewID,
			ExecutionContextDigest: transition.ExecutionContextDigest,
			InputGeneration:        transition.InputGeneration, OutputGeneration: transition.OutputGeneration,
			InputTreeDigest: transition.InputTreeDigest, OutputTreeDigest: transition.OutputTreeDigest,
			Operation: transition.Operation, Changes: transition.Changes,
		}); err != nil {
			return publication.Result{}, fmt.Errorf("trusted approved-transition verification refused publication: %w", err)
		}
	}
	if err := f.options.Trust.VerifyOrigin(ctx, bundle, origin); err != nil {
		return publication.Result{}, fmt.Errorf("trusted originating proposal receipt refused publication: %w", err)
	}
	readiness := EvidenceRequest{Bundle: bundle, Verified: verified, LiveRoot: f.options.LiveRoot,
		Workflow: f.options.WorkflowID, OwnerEpoch: f.options.OwnerEpoch}
	if err := f.options.Trust.CheckPublicationPrerequisites(ctx, readiness); err != nil {
		return publication.Result{}, fmt.Errorf("trusted production publication prerequisites are not established: %w", err)
	}
	baseRoot, err := openGenerationReadable(f.options.Baseline)
	if err != nil {
		return publication.Result{}, fmt.Errorf("open retained baseline generation descriptor: %w", err)
	}
	defer baseRoot.Close()
	tipRoot, err := openGenerationReadable(tip)
	if err != nil {
		return publication.Result{}, fmt.Errorf("open retained sealed generation descriptor: %w", err)
	}
	defer tipRoot.Close()
	baseOptions := f.options.Workspace
	baseOptions.Generation, baseOptions.QuiescentRoot = evidence.Baseline.ID, true
	baseSnapshot, err := workspace.Scan(baseRoot, baseOptions)
	if err != nil || baseSnapshot.TreeDigest != evidence.Baseline.TreeDigest || !reflect.DeepEqual(baseSnapshot.Manifest, evidence.Baseline.Manifest) {
		return publication.Result{}, errors.Join(errors.New("retained baseline descriptor differs from verified evidence"), err)
	}
	sealedEvidence := evidence.Generations[len(evidence.Generations)-1]
	sealedOptions := f.options.Workspace
	sealedOptions.Generation, sealedOptions.QuiescentRoot = sealedEvidence.ID, true
	sealedSnapshot, err := workspace.Scan(tipRoot, sealedOptions)
	if err != nil || sealedSnapshot.TreeDigest != sealedEvidence.TreeDigest || !reflect.DeepEqual(sealedSnapshot.Manifest, sealedEvidence.Manifest) {
		return publication.Result{}, errors.Join(errors.New("retained sealed descriptor differs from verified evidence"), err)
	}
	reservation, err := f.options.Ledger.Begin(f.options.LiveRoot, f.options.WorkflowID, f.options.OwnerEpoch,
		f.options.PublicationID, f.options.Workspace)
	if err != nil {
		return publication.Result{}, err
	}
	defer reservation.Release()
	f.evidence, f.origin = bundle, origin
	request := publication.Request{
		LiveRoot: f.options.LiveRoot, SealedRoot: tipRoot,
		Chain: delta.ChainSpec{Baseline: evidence.Baseline.Manifest, Sealed: sealedEvidence.Manifest,
			ExpectedBaselineTreeDigest: evidence.Baseline.TreeDigest, ExpectedSealedTreeDigest: sealedEvidence.TreeDigest,
			PolicyDigest: evidence.PolicyDigest, MetadataPolicyDigest: evidence.MetadataPolicyDigest},
		Transitions:      append([]delta.Transition(nil), evidence.ApprovedDeltaLedger...),
		OriginProposalID: origin.Operation.ProposalID, PublicationID: f.options.PublicationID,
		VerifyTransition: func(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
			return f.options.Trust.VerifyTransition(ctx, decision, binding)
		},
	}
	result, publishErr := f.repo.Publish(ctx, request, func(binding publication.CommitBinding) (publication.Authorization, error) {
		return f.commit.Authorize(ctx, binding, f.verifyCommitBinding)
	}, f.options.FaultInjector)
	if result.TransactionID != "" {
		if err := f.options.Ledger.RecordTransaction(reservation, f.options.LiveRoot, f.options.WorkflowID, f.options.PublicationID, result.TransactionID); err != nil {
			return result, errors.Join(publishErr, fmt.Errorf("record publication transaction in shared root ledger: %w", err))
		}
	}
	if result.Status == publication.StatusSucceeded || result.Status == publication.StatusFailed {
		if err := f.options.Ledger.Resolve(reservation, f.options.LiveRoot, f.options.WorkflowID, f.options.PublicationID, result.TransactionID, result.Status); err != nil {
			return result, errors.Join(publishErr, fmt.Errorf("resolve durable live-root lifecycle record: %w", err))
		}
		f.finished = true
	}
	return result, publishErr
}

func (f *Finalizer) verifyCommitBinding(ctx context.Context, binding publication.CommitBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	chainDigest, deltaDigest, digestErr := expectedPublicationDigests(f.evidence, f.evidence.ValidatedChain)
	if digestErr != nil {
		return digestErr
	}
	if binding.WorkflowID != f.options.WorkflowID || binding.OwnerEpoch != f.options.OwnerEpoch ||
		binding.PublicationID != f.options.PublicationID || binding.OriginProposalID != f.origin.Operation.ProposalID ||
		binding.BaselineTreeDigest != f.evidence.Baseline.TreeDigest || len(f.evidence.Generations) == 0 ||
		binding.SealedTreeDigest != f.evidence.Generations[len(f.evidence.Generations)-1].TreeDigest ||
		binding.PolicyDigest != f.evidence.PolicyDigest || binding.MetadataPolicyDigest != f.evidence.MetadataPolicyDigest ||
		binding.ChainDigest != chainDigest || binding.DeltaDigest != deltaDigest || !validWorkflowDigest(binding.BindingDigest) {
		return errors.New("workspace_commit binding does not match this verified session, origin, policy, and lifecycle")
	}
	return nil
}

func expectedPublicationDigests(bundle sessionrepo.EvidenceBundle, chain delta.Result) (string, string, error) {
	chainJSON, err := json.Marshal(struct {
		Baseline    string             `json:"baseline"`
		Sealed      string             `json:"sealed"`
		Policy      string             `json:"policy"`
		Metadata    string             `json:"metadata"`
		Transitions []delta.Transition `json:"transitions"`
	}{chain.BaselineTreeDigest, chain.SealedTreeDigest, bundle.PolicyDigest, bundle.MetadataPolicyDigest, bundle.ApprovedDeltaLedger})
	if err != nil {
		return "", "", err
	}
	chainDigest, err := workflowCanonicalDigest(chainJSON)
	if err != nil {
		return "", "", err
	}
	deltaJSON, err := json.Marshal(chain.ComposedChanges)
	if err != nil {
		return "", "", err
	}
	deltaDigest, err := workflowCanonicalDigest(deltaJSON)
	return chainDigest, deltaDigest, err
}

func workflowCanonicalDigest(encoded []byte) (string, error) {
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (f *Finalizer) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.repo == nil {
		return nil
	}
	repository := f.repo
	f.repo = nil
	return repository.Close()
}

func openGenerationReadable(generation *sessionrepo.Generation) (*os.File, error) {
	opath, err := generation.ReadOnlyMountSource()
	if err != nil {
		return nil, err
	}
	fd, openErr := syscall.Openat(int(opath.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	closeErr := opath.Close()
	if openErr != nil || closeErr != nil {
		if openErr == nil {
			_ = syscall.Close(fd)
		}
		return nil, errors.Join(openErr, closeErr)
	}
	return os.NewFile(uintptr(fd), "verified-generation-root"), nil
}

func validWorkspaceBinding(options workspace.Options) bool {
	return options.Limits.MaxObjects > 0 && options.Limits.MaxDepth > 0 && options.Limits.MaxFileBytes > 0 &&
		options.Limits.MaxTotalBytes > 0 && options.MetadataPolicyDigest != "" && options.XattrVisibility.Complete &&
		options.XattrVisibility.ProfileDigest != ""
}

func isNilRecoveryRepository(repository RecoveryRepository) bool {
	if repository == nil {
		return true
	}
	value := reflect.ValueOf(repository)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

func identifyRoot(root *os.File) (RootIdentity, error) {
	var info syscall.Stat_t
	if root == nil {
		return RootIdentity{}, ErrFinalizerConfig
	}
	if err := syscall.Fstat(int(root.Fd()), &info); err != nil {
		return RootIdentity{}, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Ino == 0 {
		return RootIdentity{}, errors.New("workflow live root is not a stable directory descriptor")
	}
	return RootIdentity{Device: uint64(info.Dev), Inode: info.Ino}, nil
}

func workflowEventID(root RootIdentity, workflowID, publicationID string) string {
	encoded, _ := json.Marshal(struct {
		Root        RootIdentity `json:"root"`
		Workflow    string       `json:"workflow"`
		Publication string       `json:"publication"`
	}{root, workflowID, publicationID})
	sum := sha256.Sum256(encoded)
	return "root-" + hex.EncodeToString(sum[:])
}

func decodeRootRecord(data []byte, target any) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return errors.New("workflow root lifecycle record is outside the configured bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("workflow root lifecycle record has trailing JSON")
	}
	return nil
}

func validWorkflowIdentity(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validWorkflowDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range strings.TrimPrefix(value, "sha256:") {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
