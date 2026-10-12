//go:build linux

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
)

const (
	managedLiveRootSourceCaptureReadySchema = "tbound-managed-live-source-capture-ready/v1"
	managedLiveRootSourceCaptureReadyKind   = "managed_live_root_source_capture_ready"
)

type managedLiveRootSourceCaptureReadyEvent struct {
	SchemaVersion              string                `json:"schema_version"`
	Root                       workflow.RootIdentity `json:"root"`
	RegistrationDigest         string                `json:"registration_digest"`
	RunID                      string                `json:"run_id"`
	WorkflowID                 string                `json:"workflow_id"`
	PublicationID              string                `json:"publication_id"`
	OwnerEpoch                 uint64                `json:"owner_epoch"`
	AdmissionSequence          uint64                `json:"admission_sequence"`
	AdmissionRecordHash        string                `json:"admission_record_hash"`
	LifecycleJournalSequence   uint64                `json:"lifecycle_journal_sequence"`
	LifecycleJournalRecordHash string                `json:"lifecycle_journal_record_hash"`
}

var _ piruntime.D09ManagedSourceLease = (*managedLiveRootLease)(nil)

func managedLiveRootSourceCaptureReadyEventID(root workflow.RootIdentity, registrationDigest, runID string, ownerEpoch uint64) string {
	return managedLiveRootRunEventID(root, registrationDigest, runID, ownerEpoch) + "/source-capture-ready"
}

// D09ManagedSourceLeaseEvidence exposes the currently held managed-root epoch
// only to the exact verified registration capability acquired by this lease.
func (l *managedLiveRootLease) D09ManagedSourceLeaseEvidence(ctx context.Context,
	verified *piruntime.VerifiedD09ManagedLiveRoot) (piruntime.D09ManagedSourceLeaseEvidence, error) {
	if l == nil || ctx == nil || verified == nil {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	bound, ok := l.verified.(*piruntime.VerifiedD09ManagedLiveRoot)
	l.mu.Unlock()
	if !ok || bound != verified {
		return piruntime.D09ManagedSourceLeaseEvidence{}, errors.Join(ErrManagedLiveRootUnavailable,
			piruntime.ErrD09ManagedLiveRootRefused, errors.New("managed-source request used a different verified registration capability"))
	}
	return l.currentManagedSourceLeaseEvidence(ctx)
}

func (l *managedLiveRootLease) VerifyD09ManagedSourceLease(ctx context.Context,
	verified *piruntime.VerifiedD09ManagedLiveRoot, expected piruntime.D09ManagedSourceLeaseEvidence) error {
	if l == nil || verified == nil || expected.Validate() != nil {
		return errors.Join(ErrManagedLiveRootUnavailable, piruntime.ErrD09ManagedLiveRootRefused)
	}
	l.mu.Lock()
	bound, ok := l.verified.(*piruntime.VerifiedD09ManagedLiveRoot)
	l.mu.Unlock()
	if !ok || bound != verified {
		return errors.Join(ErrManagedLiveRootUnavailable, piruntime.ErrD09ManagedLiveRootRefused)
	}
	observed, err := l.currentManagedSourceLeaseEvidence(ctx)
	if err != nil || observed != expected {
		return errors.Join(ErrManagedLiveRootUnavailable, piruntime.ErrD09ManagedLiveRootRefused, err,
			errors.New("managed-root source lease epoch or lifecycle journal changed"))
	}
	return nil
}

func (l *managedLiveRootLease) currentManagedSourceLeaseEvidence(ctx context.Context) (piruntime.D09ManagedSourceLeaseEvidence, error) {
	if l == nil || ctx == nil {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}
	l.mu.Lock()
	if l.released || !l.recoveryComplete || l.recoveryRecordSequence == 0 || l.recoveryRecordHash == "" ||
		l.admissionSeq == 0 || l.admissionHash == "" || l.taskAdmitted || l.permit != nil || l.terminalStatus != "" ||
		l.noPublication || l.abortedBeforeTask || l.verified == nil || l.root == nil || l.journal == nil {
		l.mu.Unlock()
		return piruntime.D09ManagedSourceLeaseEvidence{}, errors.Join(ErrManagedLiveRootUnavailable,
			piruntime.ErrD09ManagedLiveRootRefused, errors.New("managed-root source capture is outside its recovered pre-task epoch"))
	}
	root, verified, journal := l.root, l.verified, l.journal
	identity, serviceCgroup := l.identity, l.serviceCgroup
	registrationDigest, runID, workflowID := l.registrationDigest, l.runID, l.workflowID
	publicationID, ownerEpoch := l.publicationID, l.ownerEpoch
	admissionSequence, admissionHash := l.admissionSeq, l.admissionHash
	recoverySequence, recoveryHash := l.recoveryRecordSequence, l.recoveryRecordHash
	l.mu.Unlock()

	if err := l.revalidate(ctx, root); err != nil {
		return piruntime.D09ManagedSourceLeaseEvidence{}, errors.Join(ErrManagedLiveRootUnavailable, piruntime.ErrD09ManagedLiveRootRefused, err)
	}
	registration := verified.Registration()
	cgroupProvider, ok := verified.(managedRootCgroupIdentityProvider)
	if !ok || registration.Digest != registrationDigest || registration.RootDevice != identity.Device ||
		registration.RootInode != identity.Inode || registration.RootMountID != identity.MountID ||
		registration.ServiceCgroupPath != serviceCgroup.Path {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}
	observedCgroup, ok := cgroupProvider.CgroupIdentity()
	if !ok || observedCgroup != serviceCgroup {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}

	rootID := workflow.RootIdentity{Device: identity.Device, Inode: identity.Inode}
	trace, pending, err := managedRootLedgerState(journal, rootID)
	if err != nil || len(pending) != 0 || len(trace.Records) == 0 || uint64(len(trace.Records)) != recoverySequence {
		return piruntime.D09ManagedSourceLeaseEvidence{}, errors.Join(ErrManagedLiveRootUnavailable,
			piruntime.ErrD09ManagedLiveRootRefused, err, errors.New("managed-root lifecycle journal is no longer at the recovered capture head"))
	}
	admissionID := managedLiveRootRunEventID(rootID, registrationDigest, runID, ownerEpoch)
	readyID := managedLiveRootSourceCaptureReadyEventID(rootID, registrationDigest, runID, ownerEpoch)
	var admissionMatches, recoveryMatches int
	var readyRecord audit.Record
	for _, record := range trace.Records {
		switch {
		case record.Event.Kind == "managed_live_root_run_admitted" && record.Event.ID == admissionID:
			var admitted managedLiveRootRunAdmittedEvent
			if record.Sequence != admissionSequence || record.Hash != admissionHash ||
				unmarshalGovernedJSON(record.Event.Data, &admitted) != nil ||
				admitted.SchemaVersion != "tbound-managed-live-root-run-admitted/v1" || admitted.Root != rootID ||
				admitted.RegistrationDigest != registrationDigest || admitted.Descriptor != identity ||
				admitted.ServiceCgroup != serviceCgroup || admitted.RunID != runID || admitted.WorkflowID != workflowID ||
				admitted.PublicationID != publicationID || admitted.OwnerEpoch != ownerEpoch {
				return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
			}
			admissionMatches++
		case record.Event.Kind == managedLiveRootSourceCaptureReadyKind && record.Event.ID == readyID:
			var ready managedLiveRootSourceCaptureReadyEvent
			if record.Sequence != recoverySequence || record.Hash != recoveryHash ||
				unmarshalGovernedJSON(record.Event.Data, &ready) != nil ||
				ready.SchemaVersion != managedLiveRootSourceCaptureReadySchema || ready.Root != rootID ||
				ready.RegistrationDigest != registrationDigest || ready.RunID != runID || ready.WorkflowID != workflowID ||
				ready.PublicationID != publicationID || ready.OwnerEpoch != ownerEpoch ||
				ready.AdmissionSequence != admissionSequence || ready.AdmissionRecordHash != admissionHash ||
				ready.LifecycleJournalSequence+1 != record.Sequence || ready.LifecycleJournalRecordHash != record.PreviousHash {
				return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
			}
			readyRecord = record
			recoveryMatches++
		case record.Event.Kind == "managed_live_root_run_released" && record.Event.ID == admissionID+"/released":
			return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
		}
	}
	if admissionMatches != 1 || recoveryMatches != 1 || readyRecord.Sequence != uint64(len(trace.Records)) ||
		readyRecord.Hash != trace.Records[len(trace.Records)-1].Hash {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}

	taskStateDigest, err := managedSourceLeaseStateDigest("tbound-d09-managed-source-task-state/v1", struct {
		RunID               string `json:"run_id"`
		OwnerEpoch          uint64 `json:"owner_epoch"`
		AdmissionSequence   uint64 `json:"admission_sequence"`
		AdmissionRecordHash string `json:"admission_record_hash"`
		RecoverySequence    uint64 `json:"recovery_record_sequence"`
		RecoveryRecordHash  string `json:"recovery_record_hash"`
		TaskAdmitted        bool   `json:"task_admitted"`
	}{runID, ownerEpoch, admissionSequence, admissionHash, recoverySequence, recoveryHash, false})
	if err != nil {
		return piruntime.D09ManagedSourceLeaseEvidence{}, err
	}
	admissionRecordDigest := managedSourceJournalDigest(admissionHash)
	recoveryRecordDigest := managedSourceJournalDigest(recoveryHash)
	journalHeadDigest := managedSourceJournalDigest(trace.Records[len(trace.Records)-1].Hash)
	if !piruntime.ValidD09Digest(admissionRecordDigest) || !piruntime.ValidD09Digest(recoveryRecordDigest) ||
		!piruntime.ValidD09Digest(journalHeadDigest) {
		return piruntime.D09ManagedSourceLeaseEvidence{}, ErrManagedLiveRootUnavailable
	}
	permitStateDigest, err := managedSourceLeaseStateDigest("tbound-d09-managed-source-permit-state/v1", struct {
		RunID          string `json:"run_id"`
		OwnerEpoch     uint64 `json:"owner_epoch"`
		PermitPresent  bool   `json:"permit_present"`
		TerminalStatus string `json:"terminal_status"`
	}{runID, ownerEpoch, false, ""})
	if err != nil {
		return piruntime.D09ManagedSourceLeaseEvidence{}, err
	}
	evidence := piruntime.D09ManagedSourceLeaseEvidence{
		RunID: runID, WorkflowID: workflowID, OwnerEpoch: ownerEpoch, RegistrationDigest: registrationDigest,
		RootDevice: identity.Device, RootInode: identity.Inode, RootMountID: identity.MountID,
		ServiceCgroupPath: serviceCgroup.Path, ServiceCgroupDevice: serviceCgroup.Device,
		ServiceCgroupInode: serviceCgroup.Inode, ServiceCgroupMountID: serviceCgroup.MountID,
		RecoveryState: piruntime.D09ManagedSourceRecoveryState, RecoveryRecordSequence: recoverySequence,
		RecoveryRecordHash: recoveryRecordDigest, LeaseJournalRecordCount: uint64(len(trace.Records)),
		LeaseJournalHead: journalHeadDigest, AdmissionSequence: admissionSequence,
		AdmissionRecordHash: admissionRecordDigest, TaskState: piruntime.D09ManagedSourceTaskState,
		TaskStateDigest: taskStateDigest, PermitState: piruntime.D09ManagedSourcePermitState,
		PermitStateDigest: permitStateDigest,
	}
	return piruntime.NewD09ManagedSourceLeaseEvidence(evidence)
}

func managedSourceJournalDigest(hash string) string {
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != hash {
		return ""
	}
	return "sha256:" + hash
}

func managedSourceLeaseStateDigest(domain string, state any) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	message := append([]byte(domain+"\x00"), encoded...)
	return piruntime.DigestBytes(message), nil
}

func (l *managedLiveRootLease) markTaskAdmitted() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.taskAdmitted = true
	l.mu.Unlock()
}

type managedRootSeedQualificationProof struct {
	qualificationDigest string
	snapshot            piruntime.D09ManagedLiveRootSnapshotEvidence
	xattr               piruntime.D09ManagedRootXattrEvidence
	revalidate          func(context.Context, *os.File) error
}

func managedRootSeedProofFromQualification(
	qualification *piruntime.D09ManagedLiveRootSnapshotQualification) (managedRootSeedQualificationProof, error) {
	if qualification == nil {
		return managedRootSeedQualificationProof{}, ErrManagedSourceCaptureUnavailable
	}
	snapshot, err := qualification.SnapshotEvidence()
	if err != nil {
		return managedRootSeedQualificationProof{}, errors.Join(ErrManagedSourceCaptureUnavailable, err)
	}
	xattr, err := qualification.XattrEvidence()
	if err != nil {
		return managedRootSeedQualificationProof{}, errors.Join(ErrManagedSourceCaptureUnavailable, err)
	}
	return managedRootSeedQualificationProof{
		qualificationDigest: qualification.SnapshotEvidenceDigest(), snapshot: snapshot, xattr: xattr,
		revalidate: qualification.RevalidateRootSnapshot,
	}, nil
}

func recordManagedRootSourceCaptureProof(journal *audit.Journal, runID string, proof managedRootSeedQualificationProof) error {
	if journal == nil || runID == "" || runID != proof.snapshot.RunID ||
		!piruntime.ValidD09Digest(proof.qualificationDigest) ||
		!piruntime.ValidD09Digest(proof.snapshot.EvidenceDigest) || !piruntime.ValidD09Digest(proof.xattr.EvidenceDigest) {
		return ErrManagedSourceCaptureUnavailable
	}
	evidence := struct {
		SchemaVersion       string                                       `json:"schema_version"`
		RunID               string                                       `json:"run_id"`
		QualificationDigest string                                       `json:"qualification_digest"`
		Snapshot            piruntime.D09ManagedLiveRootSnapshotEvidence `json:"snapshot"`
		Xattr               piruntime.D09ManagedRootXattrEvidence        `json:"xattr"`
	}{"tbound-managed-root-source-capture/v1", runID, proof.qualificationDigest, proof.snapshot, proof.xattr}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if _, err := journal.Append(audit.Event{Kind: "managed_root_source_capture_qualified",
		ID: "managed-root-source-capture:" + runID, Data: encoded}); err != nil {
		return errors.Join(ErrManagedSourceCaptureUnavailable, err)
	}
	return nil
}

// seedQualifiedManagedRootSource is the service path's typed bridge from A's
// opaque qualification to the legacy Store.Seed quiescence parameter. The only
// production caller supplies the qualification produced by the D09 gate.
func seedQualifiedManagedRootSource(ctx context.Context, store *sessionrepo.Store, source *os.File,
	journal *audit.Journal, runID string, qualification *piruntime.D09ManagedLiveRootSnapshotQualification) (*sessionrepo.Generation, error) {
	proof, err := managedRootSeedProofFromQualification(qualification)
	if err != nil {
		return nil, err
	}
	return seedManagedRootSourceWithProof(ctx, store, source, journal, runID, proof)
}

func seedManagedRootSourceWithProof(ctx context.Context, store *sessionrepo.Store, source *os.File,
	journal *audit.Journal, runID string, proof managedRootSeedQualificationProof) (*sessionrepo.Generation, error) {
	if ctx == nil || store == nil || source == nil || journal == nil || runID == "" || proof.revalidate == nil {
		return nil, ErrManagedSourceCaptureUnavailable
	}
	if err := recordManagedRootSourceCaptureProof(journal, runID, proof); err != nil {
		return nil, err
	}
	if err := proof.revalidate(ctx, source); err != nil {
		refusalErr := recordManagedRootSourceCaptureRefusal(journal, runID, proof, "before-seed", nil)
		return nil, errors.Join(ErrManagedSourceCaptureUnavailable, err, refusalErr)
	}
	generation, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		return nil, err
	}
	if err := proof.revalidate(ctx, source); err != nil {
		refusalErr := recordManagedRootSourceCaptureRefusal(journal, runID, proof, "after-seed", generation)
		return nil, errors.Join(ErrManagedSourceCaptureUnavailable, err, refusalErr)
	}
	if err := recordManagedRootSourceSeedProof(journal, runID, proof, generation); err != nil {
		return nil, err
	}
	return generation, nil
}

func recordManagedRootSourceCaptureRefusal(journal *audit.Journal, runID string,
	proof managedRootSeedQualificationProof, stage string, generation *sessionrepo.Generation) error {
	if journal == nil || runID == "" || runID != proof.snapshot.RunID ||
		!piruntime.ValidD09Digest(proof.qualificationDigest) || stage != "before-seed" && stage != "after-seed" {
		return ErrManagedSourceCaptureUnavailable
	}
	evidence := struct {
		SchemaVersion       string `json:"schema_version"`
		RunID               string `json:"run_id"`
		QualificationDigest string `json:"qualification_digest"`
		Stage               string `json:"stage"`
		GenerationID        string `json:"generation_id,omitempty"`
		TreeDigest          string `json:"tree_digest,omitempty"`
		ManifestDigest      string `json:"manifest_digest,omitempty"`
	}{SchemaVersion: "tbound-managed-root-source-capture-refused/v1", RunID: runID,
		QualificationDigest: proof.qualificationDigest, Stage: stage}
	if generation != nil {
		manifest := generation.Manifest()
		manifestBytes, err := json.Marshal(manifest)
		if err != nil || generation.ID() != "g0" || manifest.Generation != generation.ID() ||
			!validManagedTreeDigest(generation.TreeDigest()) {
			return errors.Join(ErrManagedSourceCaptureUnavailable, err)
		}
		evidence.GenerationID, evidence.TreeDigest = generation.ID(), generation.TreeDigest()
		evidence.ManifestDigest = digestBytes(manifestBytes)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if _, err := journal.Append(audit.Event{Kind: "managed_root_source_capture_refused",
		ID: "managed-root-source-capture-refused:" + runID + ":" + stage, Data: encoded}); err != nil {
		return errors.Join(ErrManagedSourceCaptureUnavailable, err)
	}
	return nil
}
func recordManagedRootSourceSeedProof(journal *audit.Journal, runID string,
	proof managedRootSeedQualificationProof, generation *sessionrepo.Generation) error {
	if journal == nil || runID == "" || runID != proof.snapshot.RunID || generation == nil || generation.ID() != "g0" ||
		!piruntime.ValidD09Digest(proof.qualificationDigest) ||
		!piruntime.ValidD09Digest(proof.snapshot.EvidenceDigest) || !piruntime.ValidD09Digest(proof.xattr.EvidenceDigest) {
		return ErrManagedSourceCaptureUnavailable
	}
	manifest := generation.Manifest()
	if manifest.Generation != generation.ID() || !validManagedTreeDigest(generation.TreeDigest()) {
		return ErrManagedSourceCaptureUnavailable
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	evidence := struct {
		SchemaVersion          string `json:"schema_version"`
		RunID                  string `json:"run_id"`
		QualificationDigest    string `json:"qualification_digest"`
		SnapshotEvidenceDigest string `json:"snapshot_evidence_digest"`
		XattrEvidenceDigest    string `json:"xattr_evidence_digest"`
		GenerationID           string `json:"generation_id"`
		TreeDigest             string `json:"tree_digest"`
		ManifestDigest         string `json:"manifest_digest"`
	}{"tbound-managed-root-source-capture-seeded/v1", runID, proof.qualificationDigest,
		proof.snapshot.EvidenceDigest, proof.xattr.EvidenceDigest, generation.ID(), generation.TreeDigest(), digestBytes(manifestBytes)}
	if !piruntime.ValidD09Digest(evidence.ManifestDigest) {
		return ErrManagedSourceCaptureUnavailable
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if _, err := journal.Append(audit.Event{Kind: "managed_root_source_capture_seeded",
		ID: "managed-root-source-capture-seeded:" + runID, Data: encoded}); err != nil {
		return errors.Join(ErrManagedSourceCaptureUnavailable, err)
	}
	return nil
}
