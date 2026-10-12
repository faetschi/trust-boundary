//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

var (
	ErrManagedLiveRootUnavailable      = errors.New("registered managed live-root writer authority is unavailable")
	ErrManagedLiveRootRetained         = errors.New("managed live-root writer authority is retained after an unknown publication/cleanup outcome")
	ErrManagedSourceCaptureUnavailable = errors.New("verified current managed-root source capture is unavailable")
)

type managedLiveRootDescriptorIdentity struct {
	Path       string
	Device     uint64
	Inode      uint64
	MountID    uint64
	Filesystem uint64
	UID        uint32
	GID        uint32
	Mode       uint32
}

type managedLiveRootKey struct {
	RegistrationDigest string
	Device             uint64
	Inode              uint64
	MountID            uint64
}

type managedLiveRootAuthority struct {
	mu                 sync.Mutex
	active             map[managedLiveRootKey]*managedLiveRootLease
	verifyRegistration func(context.Context, *os.File) (managedRootRegistrationVerifier, error) // test-only seam; production uses the fixed Runtime A verifier
}

type managedRootRegistrationVerifier interface {
	Registration() piruntime.D09ManagedLiveRootRegistration
	DuplicateRoot() (*os.File, error)
	Revalidate(context.Context, *os.File) error
	Close() error
}

type managedRootCgroupIdentityProvider interface {
	CgroupIdentity() (piruntime.D09ManagedLiveRootCgroupIdentity, bool)
}

type managedLiveRootLease struct {
	mu                     sync.Mutex
	authority              *managedLiveRootAuthority
	key                    managedLiveRootKey
	verified               managedRootRegistrationVerifier
	root                   *os.File // private duplicate retained independently of the Finalizer descriptor
	journal                *audit.Journal
	identity               managedLiveRootDescriptorIdentity
	serviceCgroup          piruntime.D09ManagedLiveRootCgroupIdentity
	registrationDigest     string
	runID                  string
	workflowID             string
	publicationID          string
	ownerEpoch             uint64
	admissionSeq           uint64
	admissionHash          string
	priorRuns              []managedLiveRootPriorRun
	recoveryComplete       bool
	recoveryRecordSequence uint64
	recoveryRecordHash     string
	taskAdmitted           bool
	baselineID             string
	baselineTree           string
	permit                 *managedLivePublicationPermit
	terminalStatus         publication.Status
	terminalLedgerSeq      uint64
	terminalLedgerHash     string
	noPublication          bool
	abortedBeforeTask      bool
	released               bool
}

type managedLivePublicationPermit struct {
	lease        *managedLiveRootLease
	baselineID   string
	baselineTree string
	tipID        string
	tipTree      string
	state        managedLivePermitState
}

type managedLivePermitState uint8

const (
	managedLivePermitIssued managedLivePermitState = iota + 1
	managedLivePermitApplying
	managedLivePermitTerminal
)

var processManagedLiveRootAuthority = &managedLiveRootAuthority{active: make(map[managedLiveRootKey]*managedLiveRootLease)}

type managedLiveRootRunAdmittedEvent struct {
	SchemaVersion      string                                     `json:"schema_version"`
	Root               workflow.RootIdentity                      `json:"root"`
	RegistrationDigest string                                     `json:"registration_digest"`
	Descriptor         managedLiveRootDescriptorIdentity          `json:"descriptor"`
	ServiceCgroup      piruntime.D09ManagedLiveRootCgroupIdentity `json:"service_cgroup"`
	RunID              string                                     `json:"run_id"`
	WorkflowID         string                                     `json:"workflow_id"`
	PublicationID      string                                     `json:"publication_id"`
	OwnerEpoch         uint64                                     `json:"owner_epoch"`
	ProcessID          int                                        `json:"process_id"`
	ServiceUID         int                                        `json:"service_uid"`
	ServiceGID         int                                        `json:"service_gid"`
}

type managedLiveRootRunResolvedEvent struct {
	SchemaVersion              string                `json:"schema_version"`
	Root                       workflow.RootIdentity `json:"root"`
	RegistrationDigest         string                `json:"registration_digest"`
	RunID                      string                `json:"run_id"`
	WorkflowID                 string                `json:"workflow_id"`
	PublicationID              string                `json:"publication_id"`
	OwnerEpoch                 uint64                `json:"owner_epoch"`
	AdmissionSequence          uint64                `json:"admission_sequence"`
	AdmissionRecordHash        string                `json:"admission_record_hash"`
	Outcome                    string                `json:"outcome"`
	ResolvedByRunID            string                `json:"resolved_by_run_id,omitempty"`
	ResolvedByOwnerEpoch       uint64                `json:"resolved_by_owner_epoch,omitempty"`
	LifecycleJournalSequence   uint64                `json:"lifecycle_journal_sequence"`
	LifecycleJournalRecordHash string                `json:"lifecycle_journal_record_hash"`
}

type managedLiveRootPriorRun struct {
	Event    managedLiveRootRunAdmittedEvent
	Sequence uint64
	Hash     string
}

// acquireManagedLiveRoot verifies A's fixed-anchor registration against the
// actual opened descriptor, then retains two independent descriptors: one for
// the authority and one for the workflow Finalizer. The service-policy
// registration is not itself a quiescence permit; publication still requires
// the run-scoped drain and one-use permit below.
func (a *managedLiveRootAuthority) acquire(ctx context.Context, candidate *os.File, template piruntime.D09CellTemplate,
	journal *audit.Journal, runID, workflowID, publicationID string, ownerEpoch uint64) (*managedLiveRootLease, error) {
	if a == nil || ctx == nil || candidate == nil || journal == nil || runID == "" || workflowID == "" || publicationID == "" || ownerEpoch == 0 {
		return nil, ErrManagedLiveRootUnavailable
	}
	var verified managedRootRegistrationVerifier
	var err error
	if a.verifyRegistration != nil {
		verified, err = a.verifyRegistration(ctx, candidate)
	} else {
		verified, err = piruntime.VerifyD09ManagedLiveRoot(ctx, candidate)
	}
	if err != nil {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	closeVerified := true
	defer func() {
		if closeVerified {
			_ = verified.Close()
		}
	}()
	registration := verified.Registration()
	if registration.Digest != template.ManagedLiveRootRegistrationDigest || !validDigestText(registration.Digest) {
		return nil, fmt.Errorf("%w: registration digest differs from the signed D09 template", ErrManagedLiveRootUnavailable)
	}
	root, err := verified.DuplicateRoot()
	if err != nil {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	before, err := measureManagedLiveRootDescriptor(root)
	if err != nil {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	if err := verified.Revalidate(ctx, root); err != nil {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	after, err := measureManagedLiveRootDescriptor(root)
	if err != nil || before != after {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, errors.New("held managed live-root identity changed during registration revalidation"), err)
	}
	if !managedRegistrationMatchesDescriptor(registration, after) {
		return nil, ErrManagedLiveRootUnavailable
	}
	cgroupProvider, ok := verified.(managedRootCgroupIdentityProvider)
	if !ok {
		return nil, ErrManagedLiveRootUnavailable
	}
	serviceCgroup, ok := cgroupProvider.CgroupIdentity()
	if !ok || serviceCgroup.Path != registration.ServiceCgroupPath || serviceCgroup.Device == 0 ||
		serviceCgroup.Inode == 0 || serviceCgroup.MountID == 0 {
		return nil, ErrManagedLiveRootUnavailable
	}
	expectedEpoch, err := nextManagedRootOwnerEpoch(journal, root)
	if err != nil || ownerEpoch != expectedEpoch {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, errors.New("managed live-root run epoch is stale or skipped"), err)
	}
	key := managedLiveRootKey{RegistrationDigest: registration.Digest, Device: after.Device, Inode: after.Inode, MountID: after.MountID}
	lease := &managedLiveRootLease{authority: a, key: key, verified: verified, root: root, identity: after,
		journal: journal, serviceCgroup: serviceCgroup,
		registrationDigest: registration.Digest, runID: runID, workflowID: workflowID, publicationID: publicationID,
		ownerEpoch: ownerEpoch}
	rootID := workflow.RootIdentity{Device: after.Device, Inode: after.Inode}
	priorRuns, err := managedLiveRootPriorRuns(journal, rootID, registration.Digest)
	if err != nil {
		return nil, err
	}
	lease.priorRuns = priorRuns
	a.mu.Lock()
	if a.active[key] != nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("%w: another managed-root run already owns this registered root", ErrManagedLiveRootUnavailable)
	}
	admissionEvent := managedLiveRootRunAdmittedEvent{SchemaVersion: "tbound-managed-live-root-run-admitted/v1",
		Root: rootID, RegistrationDigest: registration.Digest, Descriptor: after, ServiceCgroup: serviceCgroup, RunID: runID,
		WorkflowID: workflowID, PublicationID: publicationID, OwnerEpoch: ownerEpoch,
		ProcessID: os.Getpid(), ServiceUID: registration.ServiceUID, ServiceGID: registration.ServiceGID}
	encodedAdmission, err := json.Marshal(admissionEvent)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	admissionRecord, err := journal.Append(audit.Event{Kind: "managed_live_root_run_admitted",
		ID: managedLiveRootRunEventID(rootID, registration.Digest, runID, ownerEpoch), Data: encodedAdmission})
	if err != nil {
		a.mu.Unlock()
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	lease.admissionSeq, lease.admissionHash = admissionRecord.Sequence, admissionRecord.Hash
	a.active[key] = lease
	a.mu.Unlock()
	closeVerified, closeRoot = false, false
	return lease, nil
}

func measureManagedLiveRootDescriptor(root *os.File) (managedLiveRootDescriptorIdentity, error) {
	if root == nil {
		return managedLiveRootDescriptorIdentity{}, ErrManagedLiveRootUnavailable
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() {
		return managedLiveRootDescriptorIdentity{}, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return managedLiveRootDescriptorIdentity{}, ErrManagedLiveRootUnavailable
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Fstatfs(int(root.Fd()), &filesystem); err != nil {
		return managedLiveRootDescriptorIdentity{}, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	mountID, err := productionDescriptorMountID(root.Fd())
	if err != nil {
		return managedLiveRootDescriptorIdentity{}, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil || !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return managedLiveRootDescriptorIdentity{}, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	return managedLiveRootDescriptorIdentity{Path: filepath.Clean(path), Device: uint64(stat.Dev), Inode: stat.Ino,
		MountID: mountID, Filesystem: uint64(filesystem.Type), UID: stat.Uid, GID: stat.Gid,
		Mode: uint32(stat.Mode & 0o7777)}, nil
}

func managedRegistrationMatchesDescriptor(registration piruntime.D09ManagedLiveRootRegistration,
	identity managedLiveRootDescriptorIdentity) bool {
	return registration.RootPath == identity.Path && registration.RootDevice == identity.Device &&
		registration.RootInode == identity.Inode && registration.RootMountID == identity.MountID &&
		registration.RootFilesystemMagic == identity.Filesystem && uint32(registration.RootUID) == identity.UID &&
		uint32(registration.RootGID) == identity.GID && registration.RootMode == identity.Mode
}

func (l *managedLiveRootLease) duplicateRoot() (*os.File, error) {
	if l == nil {
		return nil, ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.verified == nil || l.root == nil {
		return nil, ErrManagedLiveRootUnavailable
	}
	duplicate, err := l.verified.DuplicateRoot()
	if err != nil {
		return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	identity, err := measureManagedLiveRootDescriptor(duplicate)
	if err != nil || identity != l.identity {
		_ = duplicate.Close()
		return nil, errors.Join(ErrManagedLiveRootUnavailable, errors.New("managed root duplicate differs from the held identity"), err)
	}
	return duplicate, nil
}

func (l *managedLiveRootLease) bindLauncher(launcher *piruntime.D09PodmanCrunLauncher) error {
	if l == nil || launcher == nil {
		return ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.verified == nil || l.registrationDigest == "" {
		return ErrManagedLiveRootUnavailable
	}
	verified, ok := l.verified.(*piruntime.VerifiedD09ManagedLiveRoot)
	if !ok {
		return ErrManagedLiveRootUnavailable
	}
	if err := launcher.BindManagedLiveRoot(verified); err != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	registration, bound := launcher.ManagedLiveRootRegistration()
	if !bound || registration.Digest != l.registrationDigest {
		return ErrManagedLiveRootUnavailable
	}
	return nil
}

func (l *managedLiveRootLease) revalidate(ctx context.Context, candidate *os.File) error {
	if l == nil || ctx == nil || candidate == nil {
		return ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.verified == nil || l.root == nil {
		return ErrManagedLiveRootUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := measureManagedLiveRootDescriptor(candidate)
	if err != nil || identity != l.identity {
		return errors.Join(ErrManagedLiveRootUnavailable, errors.New("publication live-root handle changed from its managed identity"), err)
	}
	if err := l.verified.Revalidate(ctx, candidate); err != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	cgroupProvider, ok := l.verified.(managedRootCgroupIdentityProvider)
	if !ok {
		return ErrManagedLiveRootUnavailable
	}
	serviceCgroup, ok := cgroupProvider.CgroupIdentity()
	if !ok || serviceCgroup != l.serviceCgroup {
		return ErrManagedLiveRootUnavailable
	}
	held, err := measureManagedLiveRootDescriptor(l.root)
	if err != nil || held != l.identity {
		return errors.Join(ErrManagedLiveRootUnavailable, errors.New("authority-held live-root identity changed"), err)
	}
	return nil
}

func (l *managedLiveRootLease) issuePublicationPermit(ctx context.Context, candidate *os.File,
	baseline, tip *sessionrepo.Generation) error {
	if baseline == nil || tip == nil || baseline.ID() == "" || tip.ID() == "" || baseline.ID() == tip.ID() ||
		!validManagedTreeDigest(baseline.TreeDigest()) || !validManagedTreeDigest(tip.TreeDigest()) {
		return ErrManagedLiveRootUnavailable
	}
	if err := l.revalidate(ctx, candidate); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.recoveryComplete || l.permit != nil || l.terminalStatus != "" || l.noPublication || l.abortedBeforeTask {
		return ErrManagedLiveRootUnavailable
	}
	l.permit = &managedLivePublicationPermit{lease: l, baselineID: baseline.ID(), baselineTree: baseline.TreeDigest(),
		tipID: tip.ID(), tipTree: tip.TreeDigest(), state: managedLivePermitIssued}
	return nil
}

func (l *managedLiveRootLease) verifyPermitForTransition(ctx context.Context, live *os.File) error {
	if err := l.revalidate(ctx, live); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.permit == nil || l.permit.state != managedLivePermitIssued && l.permit.state != managedLivePermitApplying {
		return ErrManagedLiveRootUnavailable
	}
	return nil
}

func (l *managedLiveRootLease) completeAdmissionRecovery(ctx context.Context) error {
	if l == nil || ctx == nil || l.journal == nil || l.root == nil {
		return ErrManagedLiveRootUnavailable
	}
	if err := l.revalidate(ctx, l.root); err != nil {
		return err
	}
	root := workflow.RootIdentity{Device: l.identity.Device, Inode: l.identity.Inode}
	trace, pending, err := managedRootLedgerState(l.journal, root)
	if err != nil {
		return err
	}
	if len(pending) != 0 {
		return fmt.Errorf("%w: root-ledger recovery left %d unresolved publication owners", ErrManagedLiveRootRetained, len(pending))
	}
	for _, prior := range l.priorRuns {
		if err := ctx.Err(); err != nil {
			return err
		}
		admissionID := managedLiveRootRunEventID(root, prior.Event.RegistrationDigest, prior.Event.RunID, prior.Event.OwnerEpoch)
		matched := 0
		for _, record := range trace.Records {
			if record.Event.Kind == "managed_live_root_run_admitted" && record.Event.ID == admissionID &&
				record.Sequence == prior.Sequence && record.Hash == prior.Hash {
				matched++
			}
		}
		if matched != 1 {
			return ErrManagedLiveRootUnavailable
		}
		resolved := managedLiveRootRunResolvedEvent{SchemaVersion: "tbound-managed-live-root-run-resolved/v1",
			Root: prior.Event.Root, RegistrationDigest: prior.Event.RegistrationDigest,
			RunID: prior.Event.RunID, WorkflowID: prior.Event.WorkflowID, PublicationID: prior.Event.PublicationID,
			OwnerEpoch: prior.Event.OwnerEpoch, AdmissionSequence: prior.Sequence, AdmissionRecordHash: prior.Hash,
			Outcome: "reconciled-after-root-ledger-recovery", ResolvedByRunID: l.runID,
			ResolvedByOwnerEpoch: l.ownerEpoch, LifecycleJournalSequence: lastManagedLedgerSequence(trace),
			LifecycleJournalRecordHash: lastManagedLedgerHash(trace)}
		encoded, err := json.Marshal(resolved)
		if err != nil {
			return err
		}
		if _, err := l.journal.Append(audit.Event{Kind: "managed_live_root_run_reconciled",
			ID: admissionID + "/reconciled/" + l.runID, Data: encoded}); err != nil {
			return errors.Join(ErrManagedLiveRootUnavailable, err)
		}
	}
	if err := l.revalidate(ctx, l.root); err != nil {
		return err
	}
	trace, pending, err = managedRootLedgerState(l.journal, root)
	if err != nil || len(pending) != 0 || len(trace.Records) == 0 {
		return errors.Join(ErrManagedLiveRootRetained, err, errors.New("managed-root recovery left an unresolved or empty lifecycle journal"))
	}
	rootLedgerSequence := lastManagedLedgerSequence(trace)
	rootLedgerHash := lastManagedLedgerHash(trace)
	if rootLedgerSequence == 0 || rootLedgerHash == "" {
		return ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.recoveryComplete || l.recoveryRecordSequence != 0 || l.taskAdmitted || l.permit != nil ||
		l.terminalStatus != "" || l.noPublication || l.abortedBeforeTask {
		return ErrManagedLiveRootUnavailable
	}
	ready := managedLiveRootSourceCaptureReadyEvent{
		SchemaVersion: managedLiveRootSourceCaptureReadySchema, Root: root,
		RegistrationDigest: l.registrationDigest, RunID: l.runID, WorkflowID: l.workflowID,
		PublicationID: l.publicationID, OwnerEpoch: l.ownerEpoch,
		AdmissionSequence: l.admissionSeq, AdmissionRecordHash: l.admissionHash,
		LifecycleJournalSequence: rootLedgerSequence, LifecycleJournalRecordHash: rootLedgerHash,
	}
	encoded, err := json.Marshal(ready)
	if err != nil {
		return err
	}
	record, err := l.journal.Append(audit.Event{Kind: managedLiveRootSourceCaptureReadyKind,
		ID: managedLiveRootSourceCaptureReadyEventID(root, l.registrationDigest, l.runID, l.ownerEpoch), Data: encoded})
	if err != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	if record.Sequence != uint64(len(trace.Records))+1 || record.PreviousHash != rootLedgerHash ||
		record.Event.Kind != managedLiveRootSourceCaptureReadyKind {
		return ErrManagedLiveRootUnavailable
	}
	l.recoveryComplete = true
	l.recoveryRecordSequence, l.recoveryRecordHash = record.Sequence, record.Hash
	return nil
}

// recoverManagedLiveRootBeforeCapture resolves prior publication owners while
// the current run still holds the verified managed-root lease. A fresh source
// seed must not observe the root until both the durable publication ledger and
// this run's admitted-owner history have been reconciled.
func recoverManagedLiveRootBeforeCapture(ctx context.Context, lease *managedLiveRootLease,
	liveRoot, publicationStateRoot *os.File, journal *audit.Journal, ownerEpoch uint64) error {
	if ctx == nil || lease == nil || liveRoot == nil || publicationStateRoot == nil || journal == nil || ownerEpoch == 0 {
		return ErrManagedLiveRootUnavailable
	}
	if err := lease.revalidate(ctx, liveRoot); err != nil {
		return err
	}
	ledger, err := workflow.NewRootLedger(journal)
	if err != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	openRecovery := func(workflowID string) (workflow.RecoveryRepository, error) {
		recoveryWorkspace, err := managedRecoveryWorkspace(journal, liveRoot, workflowID)
		if err != nil {
			return nil, err
		}
		return publication.Acquire(publication.Options{
			WorkflowID: workflowID, OwnerEpoch: ownerEpoch, StateRoot: publicationStateRoot,
			Journal: journal, Workspace: recoveryWorkspace,
		})
	}
	if err := ledger.RecoverBeforeAdmission(ctx, liveRoot, openRecovery); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	if err := lease.completeAdmissionRecovery(ctx); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	return lease.revalidate(ctx, liveRoot)
}

func (l *managedLiveRootLease) abortBeforeTask(lifecycle productionLifecycleSnapshot, runner *productionCommandRunner) error {
	if l == nil || lifecycle.taskAdmitted || lifecycle.turnCompleted || runner != nil && runner.Pending() != 0 {
		return ErrManagedLiveRootRetained
	}
	if lifecycle.workerLaunchAttempted && (!lifecycle.workerLaunched || !lifecycle.workerClosed || lifecycle.workerCloseErr != nil ||
		lifecycle.settlement.State != piruntime.SettlementStopped || !lifecycle.settlement.ExitObserved || !lifecycle.settlement.DescendantsSettled) {
		return ErrManagedLiveRootRetained
	}
	if lifecycle.channelsStarted && (!lifecycle.channelsJoined || lifecycle.channelsErr != nil) {
		return ErrManagedLiveRootRetained
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.permit != nil || l.terminalStatus != "" || l.taskAdmitted {
		return ErrManagedLiveRootRetained
	}
	l.recoveryComplete, l.noPublication, l.abortedBeforeTask = true, true, true
	return nil
}

func lastManagedLedgerSequence(trace audit.Trace) uint64 {
	if len(trace.Records) == 0 {
		return 0
	}
	return trace.Records[len(trace.Records)-1].Sequence
}

func lastManagedLedgerHash(trace audit.Trace) string {
	if len(trace.Records) == 0 {
		return ""
	}
	return trace.Records[len(trace.Records)-1].Hash
}

func managedRootLedgerState(journal *audit.Journal, root workflow.RootIdentity) (audit.Trace, map[string]managedRootPendingWire, error) {
	if journal == nil || root == (workflow.RootIdentity{}) {
		return audit.Trace{}, nil, ErrManagedLiveRootUnavailable
	}
	trace, err := journal.Trace()
	if err != nil {
		return audit.Trace{}, nil, errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	pending := make(map[string]managedRootPendingWire)
	transactions := make(map[string]string)
	resolved := make(map[string]struct{})
	for _, frame := range trace.Records {
		switch frame.Event.Kind {
		case "workflow_publication_pending":
			var record managedRootPendingWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil || record.Root == (workflow.RootIdentity{}) ||
				record.WorkflowID == "" || record.PublicationID == "" || record.OwnerEpoch == 0 {
				return trace, nil, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != root {
				continue
			}
			key := managedWorkflowEventID(root, record.WorkflowID, record.PublicationID)
			if frame.Event.ID != key {
				return trace, nil, ErrManagedLiveRootUnavailable
			}
			if _, exists := pending[key]; exists {
				return trace, nil, ErrManagedLiveRootUnavailable
			}
			pending[key] = record
		case "workflow_publication_transaction":
			var record managedRootTransactionWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil || record.Root == (workflow.RootIdentity{}) ||
				record.WorkflowID == "" || record.PublicationID == "" || record.TransactionID == "" {
				return trace, nil, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != root {
				continue
			}
			key := managedWorkflowEventID(root, record.WorkflowID, record.PublicationID)
			if _, exists := pending[key]; !exists || transactions[key] != "" || frame.Event.ID != key+"/transaction" {
				return trace, nil, ErrManagedLiveRootUnavailable
			}
			transactions[key] = record.TransactionID
		case "workflow_publication_resolved":
			var record managedRootResolvedWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil || record.Root == (workflow.RootIdentity{}) ||
				record.WorkflowID == "" || record.PublicationID == "" ||
				(record.Status != publication.StatusSucceeded && record.Status != publication.StatusFailed) {
				return trace, nil, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != root {
				continue
			}
			key := managedWorkflowEventID(root, record.WorkflowID, record.PublicationID)
			item, exists := pending[key]
			if !exists || frame.Event.ID != key+"/resolved" ||
				transactions[key] != "" && transactions[key] != record.TransactionID {
				return trace, nil, ErrManagedLiveRootUnavailable
			}
			if _, duplicate := resolved[key]; duplicate {
				return trace, nil, ErrManagedLiveRootUnavailable
			}
			resolved[key] = struct{}{}
			delete(pending, key)
			delete(transactions, key)
			_ = item
		}
	}
	return trace, pending, nil
}

func (l *managedLiveRootLease) authorizePublication(ctx context.Context, live *os.File,
	request workflow.EvidenceRequest, baseline *sessionrepo.Generation) error {
	if baseline == nil || len(request.Bundle.Generations) == 0 || request.Workflow != l.workflowID || request.OwnerEpoch != l.ownerEpoch ||
		request.Bundle.ValidatedChain.BaselineGeneration != l.permitBaselineID() ||
		request.Bundle.ValidatedChain.BaselineTreeDigest != l.permitBaselineTree() ||
		request.Bundle.ValidatedChain.SealedGeneration != l.permitTipID() ||
		request.Bundle.ValidatedChain.SealedTreeDigest != l.permitTipTree() ||
		request.Verified.BaselineGeneration != request.Bundle.ValidatedChain.BaselineGeneration ||
		request.Verified.SealedGeneration != request.Bundle.ValidatedChain.SealedGeneration ||
		request.Verified.BaselineTreeDigest != request.Bundle.ValidatedChain.BaselineTreeDigest ||
		request.Verified.SealedTreeDigest != request.Bundle.ValidatedChain.SealedTreeDigest ||
		baseline.ID() != request.Bundle.ValidatedChain.BaselineGeneration ||
		baseline.TreeDigest() != request.Bundle.ValidatedChain.BaselineTreeDigest ||
		request.Bundle.Baseline.ID != request.Bundle.ValidatedChain.BaselineGeneration ||
		request.Bundle.Baseline.TreeDigest != request.Bundle.ValidatedChain.BaselineTreeDigest ||
		request.Bundle.Generations[len(request.Bundle.Generations)-1].ID != request.Bundle.ValidatedChain.SealedGeneration ||
		request.Bundle.Generations[len(request.Bundle.Generations)-1].TreeDigest != request.Bundle.ValidatedChain.SealedTreeDigest ||
		request.Bundle.ValidatedChain.TransitionCount != uint64(len(request.Bundle.ApprovedDeltaLedger)) {
		return ErrManagedLiveRootUnavailable
	}
	if err := l.revalidate(ctx, live); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || l.permit == nil || l.permit.state != managedLivePermitIssued {
		return ErrManagedLiveRootUnavailable
	}
	l.permit.state = managedLivePermitApplying // one-use; held through RootLedger and publication terminal status
	return nil
}

func (l *managedLiveRootLease) permitBaselineID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.permit == nil {
		return ""
	}
	return l.permit.baselineID
}

func (l *managedLiveRootLease) permitBaselineTree() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.permit == nil {
		return ""
	}
	return l.permit.baselineTree
}

func (l *managedLiveRootLease) permitTipID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.permit == nil {
		return ""
	}
	return l.permit.tipID
}

func (l *managedLiveRootLease) permitTipTree() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.permit == nil {
		return ""
	}
	return l.permit.tipTree
}

func (l *managedLiveRootLease) markNoPublication() error {
	if l == nil {
		return ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.recoveryComplete || l.terminalStatus != "" || l.permit != nil && l.permit.state == managedLivePermitApplying {
		return ErrManagedLiveRootUnavailable
	}
	l.noPublication = true
	return nil
}

func (l *managedLiveRootLease) recordDurableTerminal(ctx context.Context, journal *audit.Journal,
	status publication.Status, transactionID string) error {
	if l == nil || ctx == nil || journal == nil ||
		status != publication.StatusSucceeded && status != publication.StatusFailed {
		return ErrManagedLiveRootUnavailable
	}
	if err := l.revalidate(ctx, l.root); err != nil {
		return err
	}
	trace, err := journal.Trace()
	if err != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	root := workflow.RootIdentity{Device: l.identity.Device, Inode: l.identity.Inode}
	pendingID := managedWorkflowEventID(root, l.workflowID, l.publicationID)
	var pendingSeq, transactionSeq, resolvedSeq uint64
	var resolvedHash string
	var resolved managedRootResolvedWire
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending":
			var candidate managedRootPendingWire
			if err := unmarshalGovernedJSON(record.Event.Data, &candidate); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if candidate.Root == root && candidate.WorkflowID == l.workflowID && candidate.PublicationID == l.publicationID {
				if record.Event.ID != pendingID || candidate.OwnerEpoch != l.ownerEpoch || pendingSeq != 0 {
					return ErrManagedLiveRootUnavailable
				}
				pendingSeq = record.Sequence
			}
		case "workflow_publication_transaction":
			var candidate managedRootTransactionWire
			if err := unmarshalGovernedJSON(record.Event.Data, &candidate); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if candidate.Root == root && candidate.WorkflowID == l.workflowID && candidate.PublicationID == l.publicationID {
				if record.Event.ID != pendingID+"/transaction" || transactionSeq != 0 || candidate.TransactionID != transactionID {
					return ErrManagedLiveRootUnavailable
				}
				transactionSeq = record.Sequence
			}
		case "workflow_publication_resolved":
			var candidate managedRootResolvedWire
			if err := unmarshalGovernedJSON(record.Event.Data, &candidate); err != nil {
				return errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if candidate.Root == root && candidate.WorkflowID == l.workflowID && candidate.PublicationID == l.publicationID {
				if record.Event.ID != pendingID+"/resolved" || resolvedSeq != 0 {
					return ErrManagedLiveRootUnavailable
				}
				resolvedSeq, resolvedHash, resolved = record.Sequence, record.Hash, candidate
			}
		}
	}
	if pendingSeq == 0 || resolvedSeq <= pendingSeq || resolved.Status != status || resolved.TransactionID != transactionID ||
		(transactionID == "" && transactionSeq != 0) || (transactionID != "" && (transactionSeq <= pendingSeq || transactionSeq >= resolvedSeq)) {
		return ErrManagedLiveRootUnavailable
	}
	trace, pending, err := managedRootLedgerState(journal, root)
	if err != nil || len(pending) != 0 {
		return errors.Join(ErrManagedLiveRootUnavailable, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.recoveryComplete || l.permit == nil || l.permit.state != managedLivePermitApplying {
		return ErrManagedLiveRootUnavailable
	}
	l.permit.state = managedLivePermitTerminal
	l.terminalStatus = status
	l.terminalLedgerSeq, l.terminalLedgerHash = resolvedSeq, resolvedHash
	_ = trace
	return nil
}

func (l *managedLiveRootLease) canRelease() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return true
	}
	if !l.recoveryComplete {
		return false
	}
	if l.permit != nil && l.permit.state == managedLivePermitApplying && l.terminalStatus == "" {
		return false
	}
	return l.noPublication || l.permit == nil || l.permit.state == managedLivePermitIssued || l.permit.state == managedLivePermitTerminal
}

func (l *managedLiveRootLease) close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	if !l.recoveryComplete || l.permit != nil && l.permit.state == managedLivePermitApplying && l.terminalStatus == "" ||
		l.admissionSeq == 0 || l.admissionHash == "" {
		l.mu.Unlock()
		return ErrManagedLiveRootRetained
	}
	verified, root, authority, key, journal := l.verified, l.root, l.authority, l.key, l.journal
	l.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.revalidate(ctx, root); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	rootID := workflow.RootIdentity{Device: l.identity.Device, Inode: l.identity.Inode}
	trace, pending, err := managedRootLedgerState(journal, rootID)
	if err != nil || len(pending) != 0 {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	outcome := "publication-not-started"
	rootLedgerSeq, rootLedgerHash := lastManagedLedgerSequence(trace), lastManagedLedgerHash(trace)
	l.mu.Lock()
	switch {
	case l.terminalStatus == publication.StatusSucceeded:
		outcome = "publication-succeeded"
		rootLedgerSeq, rootLedgerHash = l.terminalLedgerSeq, l.terminalLedgerHash
	case l.terminalStatus == publication.StatusFailed:
		outcome = "publication-failed"
		rootLedgerSeq, rootLedgerHash = l.terminalLedgerSeq, l.terminalLedgerHash
	case l.abortedBeforeTask:
		outcome = "run-aborted-before-task"
	case l.noPublication:
		outcome = "no-publication"
	case l.permit != nil && l.permit.state == managedLivePermitIssued:
		outcome = "publication-refused-before-apply"
	}
	l.mu.Unlock()
	admissionID := managedLiveRootRunEventID(rootID, l.registrationDigest, l.runID, l.ownerEpoch)
	journalTrace, err := journal.Trace()
	if err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	matchedAdmission := 0
	for _, record := range journalTrace.Records {
		if record.Event.Kind == "managed_live_root_run_admitted" && record.Event.ID == admissionID &&
			record.Sequence == l.admissionSeq && record.Hash == l.admissionHash {
			matchedAdmission++
		}
		if record.Event.Kind == "managed_live_root_run_released" && record.Event.ID == admissionID+"/released" {
			return ErrManagedLiveRootRetained

		}
	}
	if matchedAdmission != 1 {
		return ErrManagedLiveRootRetained
	}
	resolved := managedLiveRootRunResolvedEvent{SchemaVersion: "tbound-managed-live-root-run-resolved/v1",
		Root: rootID, RegistrationDigest: l.registrationDigest, RunID: l.runID, WorkflowID: l.workflowID,
		PublicationID: l.publicationID, OwnerEpoch: l.ownerEpoch, AdmissionSequence: l.admissionSeq,
		AdmissionRecordHash: l.admissionHash, Outcome: outcome, LifecycleJournalSequence: rootLedgerSeq,
		LifecycleJournalRecordHash: rootLedgerHash}
	if outcome == "" || rootLedgerSeq == 0 || rootLedgerHash == "" {
		return ErrManagedLiveRootRetained
	}
	if err := root.Close(); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	if err := verified.Close(); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	data, err := json.Marshal(resolved)
	if err != nil {
		return err
	}
	if _, err := journal.Append(audit.Event{Kind: "managed_live_root_run_released", ID: admissionID + "/released", Data: data}); err != nil {
		return errors.Join(ErrManagedLiveRootRetained, err)
	}
	if authority != nil {
		authority.mu.Lock()
		if authority.active[key] != l {
			authority.mu.Unlock()
			return ErrManagedLiveRootRetained
		}
		delete(authority.active, key)
		authority.mu.Unlock()
	}
	l.mu.Lock()
	l.verified, l.root, l.released = nil, nil, true
	l.mu.Unlock()
	return nil
}

func managedRecoveryWorkspace(journal *audit.Journal, root *os.File, workflowID string) (workspace.Options, error) {
	if journal == nil || root == nil || workflowID == "" {
		return workspace.Options{}, ErrManagedLiveRootUnavailable
	}
	identity, err := measureManagedLiveRootDescriptor(root)
	if err != nil {
		return workspace.Options{}, err
	}
	rootID := workflow.RootIdentity{Device: identity.Device, Inode: identity.Inode}
	trace, err := journal.Trace()
	if err != nil {
		return workspace.Options{}, err
	}
	pending := make(map[string]managedRootPendingWire)
	seen := make(map[string]struct{})
	for _, frame := range trace.Records {
		switch frame.Event.Kind {
		case "workflow_publication_pending":
			var record managedRootPendingWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil || record.Root == (workflow.RootIdentity{}) ||
				record.WorkflowID == "" || record.OwnerEpoch == 0 || record.PublicationID == "" {
				return workspace.Options{}, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != rootID {
				continue
			}
			key := managedWorkflowEventID(record.Root, record.WorkflowID, record.PublicationID)
			if frame.Event.ID != key {
				return workspace.Options{}, ErrManagedLiveRootUnavailable
			}
			if _, duplicate := seen[key]; duplicate {
				return workspace.Options{}, ErrManagedLiveRootUnavailable
			}
			seen[key] = struct{}{}
			pending[key] = record
		case "workflow_publication_transaction":
			var record managedRootTransactionWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil || record.TransactionID == "" {
				return workspace.Options{}, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != rootID {
				continue
			}
			key := managedWorkflowEventID(record.Root, record.WorkflowID, record.PublicationID)
			item, exists := pending[key]
			if !exists || frame.Event.ID != key+"/transaction" {
				return workspace.Options{}, ErrManagedLiveRootUnavailable
			}
			itemWorkspace, marshalErr := json.Marshal(item.Workspace)
			if marshalErr != nil || itemWorkspace == nil {
				return workspace.Options{}, errors.Join(ErrManagedLiveRootUnavailable, marshalErr)
			}
			// Retain only the pending root/workspace; transaction presence is
			// rechecked by RootLedger.RecoverBeforeAdmission itself.
			pending[key] = item
		case "workflow_publication_resolved":
			var record managedRootResolvedWire
			if err := unmarshalGovernedJSON(frame.Event.Data, &record); err != nil ||
				(record.Status != publication.StatusSucceeded && record.Status != publication.StatusFailed) {
				return workspace.Options{}, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if record.Root != rootID {
				continue
			}
			key := managedWorkflowEventID(record.Root, record.WorkflowID, record.PublicationID)
			if _, exists := pending[key]; !exists || frame.Event.ID != key+"/resolved" {
				return workspace.Options{}, ErrManagedLiveRootUnavailable
			}
			delete(pending, key)
		}
	}
	var recovered *managedRootPendingWire
	for _, record := range pending {
		if record.WorkflowID != workflowID {
			continue
		}
		if recovered != nil {
			return workspace.Options{}, ErrManagedLiveRootUnavailable
		}
		candidate := record
		recovered = &candidate
	}
	if recovered == nil {
		return workspace.Options{}, fmt.Errorf("%w: no unresolved registered root-ledger owner for workflow %q", ErrManagedLiveRootUnavailable, workflowID)
	}
	return recovered.Workspace, nil
}

type managedRootPendingWire struct {
	Root          workflow.RootIdentity `json:"root"`
	WorkflowID    string                `json:"workflow_id"`
	OwnerEpoch    uint64                `json:"owner_epoch"`
	PublicationID string                `json:"publication_id"`
	Workspace     workspace.Options     `json:"workspace"`
}

type managedRootTransactionWire struct {
	Root          workflow.RootIdentity `json:"root"`
	WorkflowID    string                `json:"workflow_id"`
	PublicationID string                `json:"publication_id"`
	TransactionID string                `json:"transaction_id"`
}

type managedRootResolvedWire struct {
	Root          workflow.RootIdentity `json:"root"`
	WorkflowID    string                `json:"workflow_id"`
	PublicationID string                `json:"publication_id"`
	TransactionID string                `json:"transaction_id,omitempty"`
	Status        publication.Status    `json:"status"`
	Reason        string                `json:"reason,omitempty"`
}

func nextManagedRootOwnerEpoch(journal *audit.Journal, root *os.File) (uint64, error) {
	if journal == nil || root == nil {
		return 0, ErrManagedLiveRootUnavailable
	}
	identity, err := measureManagedLiveRootDescriptor(root)
	if err != nil {
		return 0, err
	}
	trace, err := journal.Trace()
	if err != nil {
		return 0, err
	}
	rootIdentity := workflow.RootIdentity{Device: identity.Device, Inode: identity.Inode}
	var highest uint64
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "workflow_publication_pending":
			var candidate managedRootPendingWire
			if err := unmarshalGovernedJSON(record.Event.Data, &candidate); err != nil || candidate.Root == (workflow.RootIdentity{}) ||
				candidate.WorkflowID == "" || candidate.PublicationID == "" || candidate.OwnerEpoch == 0 {
				return 0, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if candidate.Root == rootIdentity && candidate.OwnerEpoch > highest {
				highest = candidate.OwnerEpoch
			}
		case "managed_live_root_run_admitted":
			var candidate managedLiveRootRunAdmittedEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &candidate); err != nil ||
				candidate.SchemaVersion != "tbound-managed-live-root-run-admitted/v1" || candidate.Root == (workflow.RootIdentity{}) ||
				!validDigestText(candidate.RegistrationDigest) || candidate.RunID == "" || candidate.WorkflowID == "" ||
				candidate.PublicationID == "" || candidate.OwnerEpoch == 0 || candidate.ProcessID <= 0 {
				return 0, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if candidate.Root == rootIdentity && candidate.OwnerEpoch > highest {
				highest = candidate.OwnerEpoch
			}
		}
	}
	if highest == ^uint64(0) {
		return 0, ErrManagedLiveRootUnavailable
	}
	return highest + 1, nil
}

func managedLiveRootRunEventID(root workflow.RootIdentity, registrationDigest, runID string, ownerEpoch uint64) string {
	encoded, _ := json.Marshal(struct {
		Root               workflow.RootIdentity `json:"root"`
		RegistrationDigest string                `json:"registration_digest"`
		RunID              string                `json:"run_id"`
		OwnerEpoch         uint64                `json:"owner_epoch"`
	}{root, registrationDigest, runID, ownerEpoch})
	sum := sha256.Sum256(encoded)
	return "managed-root-run-" + hex.EncodeToString(sum[:])
}

func managedLiveRootPriorRuns(journal *audit.Journal, root workflow.RootIdentity, registrationDigest string) ([]managedLiveRootPriorRun, error) {
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	pending := make(map[string]managedLiveRootPriorRun)
	resolved := make(map[string]struct{})
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "managed_live_root_run_admitted":
			var admitted managedLiveRootRunAdmittedEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &admitted); err != nil ||
				admitted.SchemaVersion != "tbound-managed-live-root-run-admitted/v1" || admitted.Root == (workflow.RootIdentity{}) ||
				!validDigestText(admitted.RegistrationDigest) || admitted.RunID == "" || admitted.WorkflowID == "" ||
				admitted.PublicationID == "" || admitted.OwnerEpoch == 0 || admitted.ProcessID <= 0 ||
				admitted.ServiceUID <= 0 || admitted.ServiceGID <= 0 || admitted.Descriptor.Device != admitted.Root.Device ||
				admitted.Descriptor.Inode != admitted.Root.Inode || admitted.Descriptor.MountID == 0 ||
				admitted.Descriptor.Path == "" || admitted.Descriptor.Mode != 0o700 ||
				admitted.ServiceCgroup.Path == "" || admitted.ServiceCgroup.Device == 0 || admitted.ServiceCgroup.Inode == 0 ||
				admitted.ServiceCgroup.MountID == 0 ||
				record.Event.ID != managedLiveRootRunEventID(admitted.Root, admitted.RegistrationDigest, admitted.RunID, admitted.OwnerEpoch) {
				return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if admitted.Root != root {
				continue
			}
			key := record.Event.ID
			if _, duplicate := pending[key]; duplicate {
				return nil, ErrManagedLiveRootUnavailable
			}
			if _, alreadyResolved := resolved[key]; alreadyResolved {
				return nil, ErrManagedLiveRootUnavailable
			}
			pending[key] = managedLiveRootPriorRun{Event: admitted, Sequence: record.Sequence, Hash: record.Hash}
		case "managed_live_root_run_released", "managed_live_root_run_reconciled":
			var ended managedLiveRootRunResolvedEvent
			if err := unmarshalGovernedJSON(record.Event.Data, &ended); err != nil ||
				ended.SchemaVersion != "tbound-managed-live-root-run-resolved/v1" || ended.Root == (workflow.RootIdentity{}) ||
				!validDigestText(ended.RegistrationDigest) || ended.RunID == "" || ended.WorkflowID == "" ||
				ended.PublicationID == "" || ended.OwnerEpoch == 0 || ended.AdmissionSequence == 0 || ended.AdmissionRecordHash == "" ||
				ended.Outcome != "publication-succeeded" && ended.Outcome != "publication-failed" &&
					ended.Outcome != "no-publication" && ended.Outcome != "run-aborted-before-task" &&
					ended.Outcome != "publication-not-started" && ended.Outcome != "publication-refused-before-apply" &&
					ended.Outcome != "reconciled-after-root-ledger-recovery" {
				return nil, errors.Join(ErrManagedLiveRootUnavailable, err)
			}
			if ended.Root != root {
				continue
			}
			if record.Event.Kind == "managed_live_root_run_reconciled" && ended.Outcome != "reconciled-after-root-ledger-recovery" ||
				record.Event.Kind == "managed_live_root_run_released" && ended.Outcome == "reconciled-after-root-ledger-recovery" {
				return nil, ErrManagedLiveRootUnavailable
			}
			key := managedLiveRootRunEventID(ended.Root, ended.RegistrationDigest, ended.RunID, ended.OwnerEpoch)
			prior, exists := pending[key]
			if !exists || prior.Sequence != ended.AdmissionSequence || prior.Hash != ended.AdmissionRecordHash ||
				prior.Event.WorkflowID != ended.WorkflowID || prior.Event.PublicationID != ended.PublicationID {
				return nil, ErrManagedLiveRootUnavailable
			}
			expectedSuffix := "/released"
			if record.Event.Kind == "managed_live_root_run_reconciled" {
				expectedSuffix = "/reconciled/" + ended.ResolvedByRunID
				if ended.ResolvedByRunID == "" || ended.ResolvedByOwnerEpoch == 0 || ended.LifecycleJournalSequence == 0 ||
					ended.LifecycleJournalRecordHash == "" {
					return nil, ErrManagedLiveRootUnavailable
				}
			}
			if record.Event.ID != key+expectedSuffix {
				return nil, ErrManagedLiveRootUnavailable
			}
			if _, duplicate := resolved[key]; duplicate {
				return nil, ErrManagedLiveRootUnavailable
			}
			resolved[key] = struct{}{}
			delete(pending, key)
		}
	}
	if len(pending) == 0 {
		return nil, nil
	}
	if registrationDigest == "" {
		return nil, ErrManagedLiveRootUnavailable
	}
	priorRuns := make([]managedLiveRootPriorRun, 0, len(pending))
	for _, prior := range pending {
		if prior.Event.RegistrationDigest != registrationDigest {
			return nil, fmt.Errorf("%w: unresolved run admission was issued under a different managed-root registration", ErrManagedLiveRootUnavailable)
		}
		priorRuns = append(priorRuns, prior)
	}
	return priorRuns, nil
}

func managedWorkflowEventID(root workflow.RootIdentity, workflowID, publicationID string) string {
	encoded, _ := json.Marshal(struct {
		Root        workflow.RootIdentity `json:"root"`
		Workflow    string                `json:"workflow"`
		Publication string                `json:"publication"`
	}{root, workflowID, publicationID})
	sum := sha256.Sum256(encoded)
	return "root-" + hex.EncodeToString(sum[:])
}

func validManagedTreeDigest(value string) bool {
	const suffixBytes = 64
	prefix := delta.TreeDigestProfile
	if len(value) != len(prefix)+suffixBytes || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := value[len(prefix):]
	if encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func (l *managedLiveRootLease) matchesFinalizer(ctx context.Context, live *os.File, runID, workflowID string, epoch uint64) error {
	if l == nil || l.runID != runID || l.workflowID != workflowID || l.ownerEpoch != epoch {
		return ErrManagedLiveRootUnavailable
	}
	return l.revalidate(ctx, live)
}
