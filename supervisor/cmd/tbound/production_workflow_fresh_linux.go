//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// initializeProductionFreshWorkflow establishes the original workflow's
// checkpoint and first empty provider transcript after the Store and D06
// private-Git source binding exist, and before any worker or provider authority
// is exposed. The service catalog's one-use admission and immutable deadline
// are the only source of new workflow identity and limits.
func initializeProductionFreshWorkflow(ctx context.Context, config productionServeConfig,
	admission *productionWorkflowAdmission, profile piruntime.HostProfile,
	template piruntime.D09CellTemplate, policyDigest, metadataPolicyDigest string,
	managed *sessionlaunch.ManagedStore, storeRoot *os.File, adminRoot *os.File,
	storeJournal, providerJournal, commandJournal *audit.Journal, prepared *sessionlaunch.PreparedSession,
	baseline *sessionrepo.Generation, storeOptions sessionrepo.Options,
	runner *productionCommandRunner, authority *SessionOperationAuthority,
	systemPrompt, developerPrompt string) (*providerbridge.Conversation, error) {
	if ctx == nil || admission == nil || admission.Mode != "fresh" || admission.Catalog == nil ||
		admission.Owner == nil || managed == nil || storeRoot == nil || adminRoot == nil ||
		storeJournal == nil || providerJournal == nil || commandJournal == nil || prepared == nil ||
		baseline == nil || runner == nil || authority == nil || systemPrompt == "" || developerPrompt == "" {
		return nil, ErrPiWorkflowCatalog
	}
	entry := admission.Entry
	if entry.WorkflowID == "" || entry.SessionID == "" || entry.ConversationID == "" ||
		admission.CandidateEpoch == 0 || admission.CandidateEpoch != 1 ||
		admission.Owner.OwnerIncarnationDigest() != admission.OwnerIncarnation.Digest ||
		entry.Limits.Deadline.IsZero() {
		return nil, ErrPiWorkflowCatalog
	}
	if err := verifyWorkflowDeadline(ctx, admission.Owner, entry.WorkflowID, entry.Limits.Deadline); err != nil {
		return nil, errors.Join(workflowrecovery.ErrDeadline, err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		return nil, err
	}
	commandTrace, err := commandJournal.Trace()
	if err != nil || len(commandTrace.Records) != 0 || len(commandTrace.Effects) != 0 {
		return nil, errors.Join(errors.New("fresh production command journal must be empty before workflow pinning"), err)
	}
	providerTrace, err := providerJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return nil, errors.Join(errors.New("fresh production provider journal must be empty before workflow pinning"), err)
	}
	storeIdentity, err := recoveryDirectoryIdentityDigest(storeRoot)
	if err != nil {
		return nil, err
	}
	adminIdentity, err := recoveryDirectoryIdentityDigest(adminRoot)
	if err != nil {
		return nil, err
	}
	commandIdentity, err := recoveryFileIdentityDigest(entry.CommandJournalPath, false)
	if err != nil {
		return nil, err
	}
	providerIdentity, err := recoveryFileIdentityDigest(entry.ProviderJournalPath, false)
	if err != nil {
		return nil, err
	}
	if err := verifyJournalPathMatches(entry.StoreJournalPath, storeJournal); err != nil {
		return nil, err
	}
	if err := verifyJournalPathMatches(entry.CommandJournalPath, commandJournal); err != nil {
		return nil, err
	}
	if err := verifyJournalPathMatches(entry.ProviderJournalPath, providerJournal); err != nil {
		return nil, err
	}
	bundle, err := managed.Store().EvidenceBundle()
	if err != nil {
		return nil, err
	}
	admissionTrace, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil {
		return nil, err
	}
	if err := prepared.VerifySourceBinding(managed.Store(), baseline, storeJournal); err != nil {
		return nil, fmt.Errorf("verify D06 source binding before fresh workflow authorization: %w", err)
	}
	d06ProvenanceDigest, privateGitIdentityDigest, repositoryName, err := prepared.RecoveryIdentity()
	if err != nil {
		return nil, err
	}
	policy, err := productionGatePolicy()
	if err != nil {
		return nil, err
	}
	storePolicyDigest, metadataDigest := bareWorkflowDigest(policyDigest), bareWorkflowDigest(metadataPolicyDigest)
	if storePolicyDigest != bareWorkflowDigest(storeOptions.PolicyDigest) ||
		metadataDigest != bareWorkflowDigest(storeOptions.MetadataPolicyDigest) ||
		profile.ProviderProfile == "" ||
		profile.ProviderProfileDigest == "" || profile.PiRuntimeBundleDigest == "" ||
		!validDigestText(profile.Digest) || !validDigestText(template.ExpectedImageDigest) {
		return nil, errors.New("fresh workflow runtime and policy pins are incomplete")
	}
	commandSessions := []productionRecoveryCommandSession{{
		RunID: entry.RunID, SessionID: entry.SessionID, ConversationID: entry.ConversationID,
	}}
	commandPin := productionHistoricalCommandPin{IdentityDigest: commandIdentity,
		Head: auditHeadHash(commandTrace.Records), RecordCount: uint64(len(commandTrace.Records))}
	request := governedRecoveryRequest{
		Owner: admission.Owner, WorkflowID: entry.WorkflowID, NewOwnerEpoch: admission.CandidateEpoch,
		NewSessionID: entry.SessionID, NewConversationID: entry.ConversationID, Limits: entry.Limits,
		StoreRoot: storeRoot, StoreJournalPath: entry.StoreJournalPath,
		StoreOptions: storeOptions, AdminRoot: adminRoot,
		GitPin: sessionlaunch.RecoveryGitPin{RepositoryName: repositoryName,
			ProvenanceDigest: d06ProvenanceDigest, RepositoryIdentityDigest: privateGitIdentityDigest},
		CommandHistories: []productionRecoveryCommandJournal{{Journal: commandJournal,
			Path: entry.CommandJournalPath, Pin: commandPin, Sessions: commandSessions}},
		ProductionProfile: profile, D09Template: template, GatePolicy: policy,
		ProviderProfile: broker.Profile{ID: profile.ProviderProfile, Model: providerbridge.ApprovedModel,
			Messages: []openrouter.Message{
				{Role: openrouter.System, Content: &systemPrompt},
				{Role: openrouter.Developer, Content: &developerPrompt},
			}, ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
			ResponseIDIssuer: governedResponseIssuer, Generation: baseline.ID()},
		ProviderProfileDigest: profile.ProviderProfileDigest,
		ImageDigest:           template.ExpectedImageDigest, RuntimeProfileDigest: profile.PiRuntimeBundleDigest,
		SystemPrompt: systemPrompt, DeveloperPrompt: developerPrompt,
		StorePolicyDigest: storeOptions.PolicyDigest, MetadataPolicyDigest: storeOptions.MetadataPolicyDigest,
		BashOutputPaths: cloneDurableOutputScopePaths(config.BashOutputPaths),
		CommandRunner:   runner, GovernedCommandRunner: runner,
		ProviderTokenUpperBound: productionProviderTokenUpperBound(entry.Limits), RequireEvidencedBash: true,
		FreshProviderJournal: providerJournal, FreshProviderJournalPath: entry.ProviderJournalPath,
		FreshJournalIdentity: providerIdentity,
	}
	runtimeDigest, err := governedRecoveryRuntimeDigest(request)
	if err != nil {
		return nil, fmt.Errorf("pin immutable production runtime before fresh workflow initialization: %w", err)
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(request)
	if err != nil {
		return nil, err
	}
	if identityPins.StoreIdentityDigest != storeIdentity || identityPins.AdminIdentityDigest != adminIdentity {
		return nil, workflowrecovery.ErrOwnerStore
	}
	pins := workflowrecovery.Pins{
		WorkflowID: entry.WorkflowID, SessionID: entry.SessionID, RecoveryGeneration: 1,
		OwnerEpoch: admission.CandidateEpoch, OwnerIncarnationDigest: identityPins.OwnerIncarnationDigest,
		RootIdentityDigest: identityPins.RootIdentityDigest, StoreIdentityDigest: identityPins.StoreIdentityDigest,
		AdmissionIdentityDigest: identityPins.AdmissionIdentityDigest, CommandIdentityDigest: identityPins.CommandIdentityDigest,
		AdminIdentityDigest: identityPins.AdminIdentityDigest,
		TaskDigest:          bareWorkflowDigest(entry.TaskDigest), PromptDigest: bareWorkflowDigest(entry.PromptDigest),
		PolicyDigest: barePolicyDigest(policy.Digest()), ProfileDigest: bareWorkflowDigest(profile.Digest),
		ImageDigest: bareWorkflowDigest(template.ExpectedImageDigest), RuntimeDigest: bareWorkflowDigest(runtimeDigest),
		D06ProvenanceDigest:       bareWorkflowDigest(d06ProvenanceDigest),
		PrivateGitIdentityDigest:  bareWorkflowDigest(privateGitIdentityDigest),
		StoreBaselineGenerationID: baseline.ID(), StoreBaselineTreeDigest: bareStoreTreeDigest(baseline.TreeDigest()),
		StoreTipGenerationID: baseline.ID(), StoreTipTreeDigest: bareStoreTreeDigest(baseline.TreeDigest()),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		AdmissionJournalHead: auditHeadHash(admissionTrace.Records), AdmissionJournalCount: uint64(len(admissionTrace.Records)),
		ProviderProjectionDigest: workflowrecovery.TranscriptPinsDigest(nil),
		ProviderDisposition:      workflowrecovery.DispositionNone, PendingCallDisposition: workflowrecovery.PendingCallNone,
	}
	pins.RecoveryLineageDigest = workflowrecovery.ComputeRecoveryLineageDigest("", workflowrecovery.Pins{}, pins)
	initialReceipt, err := workflowrecovery.InitializeFresh(ctx, admission.Owner, pins, entry.Limits)
	if err != nil {
		return nil, fmt.Errorf("authorize and persist the original host workflow checkpoint: %w", err)
	}
	if err := admission.Catalog.BindInitialCheckpoint(admission, initialReceipt); err != nil {
		return nil, errors.Join(errors.New("bind the fresh protected checkpoint to its exact service admission"), err)
	}
	ledger, err := workflowrecovery.Open(ctx, admission.Owner, workflowrecovery.OpenOptions{
		WorkflowID: entry.WorkflowID, OwnerEpoch: admission.CandidateEpoch,
		RetainedPins: pins, Limits: entry.Limits,
	})
	if err != nil {
		return nil, err
	}
	transcript := workflowrecovery.TranscriptPin{
		Ordinal: 1, ConversationID: entry.ConversationID, JournalIdentityDigest: providerIdentity,
		Disposition: workflowrecovery.DispositionAwaitingHostPrompt,
	}
	progress := productionInitialTranscriptProgressVerifier{
		request: request, previousPins: pins, transcript: transcript,
		managed: managed, prepared: prepared, baseline: baseline,
		storeJournal: storeJournal, providerJournal: providerJournal,
	}
	transcriptReceipt, err := ledger.RegisterInitialTranscript(ctx, progress, transcript)
	if err != nil {
		return nil, fmt.Errorf("persist the initial empty provider transcript before any provider exchange: %w", err)
	}
	if err := admission.Catalog.MarkCheckpoint(entry.AdmissionRecordHash,
		initialReceipt.CheckpointDigest(), transcriptReceipt.CheckpointDigest()); err != nil {
		return nil, errors.Join(errors.New("bind the initial provider transcript checkpoint to the service catalog"), err)
	}
	checkpoint, _ := ledger.Snapshot()
	if err := authority.BindWorkflowLedger(ledger, checkpoint.Pins, request.ProviderTokenUpperBound); err != nil {
		return nil, fmt.Errorf("bind mandatory workflow accounting to actual session operation authority: %w", err)
	}
	if err := authority.BindInitialWorkflowPrompt(entry.TaskID, entry.TaskPrompt, entry.TaskDigest); err != nil {
		return nil, fmt.Errorf("bind exact fresh catalog task and prompt before provider admission: %w", err)
	}
	conversation, err := authority.NewProviderConversation(providerbridge.Config{
		WorkflowID: entry.WorkflowID, ConversationID: entry.ConversationID, ProfileID: profile.ProviderProfile,
		InitialGenerationID: baseline.ID(), InitialTreeDigest: baseline.TreeDigest(),
		SystemPrompt: systemPrompt, DeveloperPrompt: developerPrompt,
	})
	if err != nil {
		return nil, err
	}
	return conversation, nil
}

func productionProviderTokenUpperBound(limits workflowrecovery.Limits) uint64 {
	if limits.MaxTokens < productionProviderCompletionCap {
		return limits.MaxTokens
	}
	return productionProviderCompletionCap
}

type productionInitialTranscriptProgressVerifier struct {
	request         governedRecoveryRequest
	previousPins    workflowrecovery.Pins
	transcript      workflowrecovery.TranscriptPin
	managed         *sessionlaunch.ManagedStore
	prepared        *sessionlaunch.PreparedSession
	baseline        *sessionrepo.Generation
	storeJournal    *audit.Journal
	providerJournal *audit.Journal
}

func (v productionInitialTranscriptProgressVerifier) VerifyProgress(ctx context.Context,
	previous workflowrecovery.Checkpoint, next workflowrecovery.Pins) error {
	if ctx == nil || v.managed == nil || v.prepared == nil || v.baseline == nil ||
		v.storeJournal == nil || v.providerJournal == nil || previous.Pins.WorkflowID != v.previousPins.WorkflowID {
		return workflowrecovery.ErrObservation
	}
	want := cloneWorkflowPins(v.previousPins)
	want.ProviderTranscripts = []workflowrecovery.TranscriptPin{v.transcript}
	want.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(want.ProviderTranscripts)
	want.ProviderProjectionCount = 0
	want.ProviderProjectionHead = ""
	want.ProviderDisposition = workflowrecovery.DispositionAwaitingHostPrompt
	if !reflect.DeepEqual(want, next) || next.RecoveryGeneration != 1 || next.OwnerEpoch != previous.Pins.OwnerEpoch ||
		next.StoreTipGenerationID != v.baseline.ID() ||
		next.StoreTipTreeDigest != bareStoreTreeDigest(v.baseline.TreeDigest()) {
		return workflowrecovery.ErrObservation
	}
	if err := v.request.Owner.VerifyFence(ctx, next.WorkflowID, next.OwnerEpoch); err != nil {
		return errors.Join(workflowrecovery.ErrOwnerFence, err)
	}
	if err := verifyWorkflowDeadline(ctx, v.request.Owner, next.WorkflowID, v.request.Limits.Deadline); err != nil {
		return errors.Join(workflowrecovery.ErrDeadline, err)
	}
	if err := v.prepared.VerifySourceBinding(v.managed.Store(), v.baseline, v.storeJournal); err != nil {
		return err
	}
	bundle, err := v.managed.Store().EvidenceBundle()
	if err != nil {
		return err
	}
	storeTrace, err := v.storeJournal.Trace()
	if err != nil || uint64(len(storeTrace.Records)) != next.StoreJournalCount || auditHeadHash(storeTrace.Records) != next.StoreJournalHead ||
		bundle.AuditRecordCount != next.StoreJournalCount || bundle.AuditHeadHash != next.StoreJournalHead ||
		bundle.Baseline.ID != next.StoreBaselineGenerationID || bundle.Baseline.TreeDigest != v.baseline.TreeDigest() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined || disposition.Unknowns != 0 || len(disposition.Operations) != 0 ||
		disposition.Sealed != v.baseline.ID() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	admissionTrace, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil || uint64(len(admissionTrace.Records)) != next.AdmissionJournalCount ||
		auditHeadHash(admissionTrace.Records) != next.AdmissionJournalHead {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	providerTrace, err := v.providerJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	identity, err := recoveryFileIdentityDigest(v.request.FreshProviderJournalPath, false)
	if err != nil || identity != v.transcript.JournalIdentityDigest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(v.request)
	if err != nil || identityPins.RootIdentityDigest != next.RootIdentityDigest ||
		identityPins.StoreIdentityDigest != next.StoreIdentityDigest ||
		identityPins.AdmissionIdentityDigest != next.AdmissionIdentityDigest ||
		identityPins.CommandIdentityDigest != next.CommandIdentityDigest ||
		identityPins.AdminIdentityDigest != next.AdminIdentityDigest ||
		identityPins.OwnerIncarnationDigest != next.OwnerIncarnationDigest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	runtimeDigest, err := governedRecoveryRuntimeDigest(v.request)
	if err != nil || bareWorkflowDigest(runtimeDigest) != next.RuntimeDigest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	return nil
}

func verifyWorkflowDeadline(ctx context.Context, owner workflowrecovery.Owner, workflow string, deadline time.Time) error {
	verifier, ok := owner.(workflowrecovery.DeadlineVerifier)
	if !ok {
		return workflowrecovery.ErrClockUncertain
	}
	return verifier.VerifyWorkflowDeadline(ctx, workflow, deadline)
}
