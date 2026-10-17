//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

// bindProductionPiRecoverySession resumes only the host-catalog-selected
// original workflow. The owner epoch and full superseded-cell drain were
// acquired by openProductionWorkflowOwner before this function opens any
// retained audit journal. This path never creates or seeds a Store/admin root.
func bindProductionPiRecoverySession(ctx context.Context, config productionServeConfig,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate,
	artifacts productionOperatorArtifacts, admission *productionWorkflowAdmission) (_ *productionPiSession, resultErr error) {
	if ctx == nil || admission == nil || admission.Mode != "resume" || admission.Owner == nil ||
		admission.Root == nil || admission.Ledger == nil || admission.Catalog == nil ||
		admission.Checkpoint.Pins.WorkflowID != admission.Entry.WorkflowID ||
		admission.CheckpointHead == "" || admission.CandidateEpoch <= admission.Checkpoint.Pins.OwnerEpoch ||
		!reflect.DeepEqual(config.BashOutputPaths, admission.Entry.BashOutputPaths) {
		return nil, ErrPiWorkflowCatalog
	}
	entry, checkpoint := admission.Entry, admission.Checkpoint
	if err := requireWorkflowCatalogPath(entry, piServiceConfig{WorkflowOwnerStoreRoot: piServiceOwnerStoreRoot}); err != nil {
		return nil, err
	}
	if entry.Terminal || !entry.CheckpointBound || !entry.FreshAuthorizationUsed ||
		checkpoint.Limits != entry.Limits || checkpoint.Pins.TaskDigest != entry.TaskDigest ||
		checkpoint.Pins.PromptDigest != entry.PromptDigest || checkpoint.Pins.WorkflowID != entry.WorkflowID ||
		checkpoint.ComputedCheckpointDigest != admission.CheckpointHead {
		return nil, workflowrecovery.ErrRollback
	}
	if checkpoint.Pins.RecoveryGeneration == ^uint64(0) || !validIdentityText(entry.WorkflowID) {
		return nil, workflowrecovery.ErrRollback
	}
	generation := checkpoint.Pins.RecoveryGeneration + 1
	newSessionID := fmt.Sprintf("%s-session-g%d", entry.WorkflowID, generation)
	newConversationID := fmt.Sprintf("%s-conversation-g%d", entry.WorkflowID, generation)
	newRunID := newSessionID
	newProviderPath := productionRecoveryProviderJournalPath(artifacts.AuditRoot, entry.WorkflowID, newConversationID)
	if !validIdentityText(newSessionID) || !validIdentityText(newConversationID) ||
		!filepath.IsAbs(newProviderPath) || filepath.Clean(newProviderPath) != newProviderPath {
		return nil, ErrPiWorkflowCatalog
	}
	if err := verifyWorkflowDeadline(ctx, admission.Owner, entry.WorkflowID, checkpoint.Limits.Deadline); err != nil {
		return nil, errors.Join(workflowrecovery.ErrDeadline, err)
	}
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return nil, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	calibration, err := piruntime.LoadVerifiedD09ManagedRWXattrCalibrationForRegistration(ctx, registration)
	if err != nil {
		return nil, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	xattrVisibility, err := piruntime.WorkspaceXattrAttestation(calibration.Digest, calibration.Authority)
	if err != nil {
		return nil, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	policy, err := productionGatePolicy()
	if err != nil {
		return nil, err
	}
	storePolicyDigest := digestBytes([]byte("tbound/session-policy/v1"))
	metadataPolicyDigest := digestBytes([]byte("tbound/metadata-policy/v1"))
	if !validDigestText(profile.ProviderProfileDigest) || !validDigestText(profile.PiRuntimeBundleDigest) ||
		!validDigestText(template.ExpectedImageDigest) || profile.ProviderProfile == "" ||
		!validDigestText(profile.Digest) {
		return nil, ErrPiWorkflowCatalog
	}
	if templateDigest, digestErr := template.Digest(); digestErr != nil ||
		profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.ContainmentProfileDigest != templateDigest {
		return nil, errors.Join(ErrProductionSealedRuntime, digestErr)
	}

	storeRoot, err := openExistingPrivateRecoveryDirectory(entry.StoreRootPath)
	if err != nil {
		return nil, fmt.Errorf("open catalog-bound retained Store root without creating or repairing it: %w", err)
	}
	closers := []func() error{admission.CloseOwner}
	closers = append(closers, storeRoot.Close)
	var storeJournal, commandJournal, newProviderJournal *audit.Journal
	var oldProviderJournals []*audit.Journal
	var managed *sessionlaunch.ManagedStore
	var admin *sessionlaunch.AdminParent
	var prepared *sessionlaunch.PreparedSession
	var recoveryCandidate *governedRecoveryCandidate
	var ownerScopeBound bool
	var session *productionPiSession
	var commandLauncher *piruntime.D09PodmanCrunLauncher
	fail := func(cause error) (*productionPiSession, error) {
		if session != nil && session.ManagedLiveRootLease != nil && !session.Lifecycle.snapshot().taskAdmitted {
			cause = errors.Join(cause, session.ManagedLiveRootLease.abortBeforeTask(session.Lifecycle.snapshot(), session.ProductionCommandRunner))
		}
		if session != nil && session.Authority != nil {
			session.Authority.mu.Lock()
			conversation := session.Authority.conversation
			session.Authority.mu.Unlock()
			if conversation != nil {
				conversation.Close()
			}
		}
		var cleanupErrs []error
		for index := len(closers) - 1; index >= 0; index-- {
			if closeErr := closers[index](); closeErr != nil {
				cleanupErrs = append(cleanupErrs, closeErr)
				retainProductionCleanup(closers[:index+1])
				break
			}
		}
		if !ownerScopeBound && len(cleanupErrs) == 0 {
			if drainErr := admission.Catalog.MarkOwnerUnexposedDrained(entry.AdmissionRecordHash,
				entry.WorkflowID, admission.CandidateEpoch, admission.OwnerIncarnation, filepath.Clean(template.CgroupsPath)); drainErr != nil {
				cleanupErrs = append(cleanupErrs, drainErr)
			}
		}
		return nil, errors.Join(append([]error{cause}, cleanupErrs...)...)
	}
	storeJournal, err = openExistingRecoveryJournal(entry.StoreJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open exact retained workflow Store journal: %w", err))
	}
	closers = append(closers, storeJournal.Close)
	commandJournal, err = openExistingRecoveryJournal(entry.CommandJournalPath)
	if err != nil {
		return fail(fmt.Errorf("open exact retained workflow command journal: %w", err))
	}
	closers = append(closers, commandJournal.Close)
	adminRoot, err := openExistingPrivateRecoveryDirectory(entry.AdminRootPath)
	if err != nil {
		return fail(fmt.Errorf("open catalog-bound retained private Git admin root without creating it: %w", err))
	}
	closers = append(closers, adminRoot.Close)
	storeOptions := sessionrepo.Options{PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
		Limits: workspace.DefaultLimits(), XattrVisibility: xattrVisibility, Journal: storeJournal}
	if err := verifyJournalPathMatches(entry.StoreJournalPath, storeJournal); err != nil {
		return fail(err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		return fail(err)
	}
	commandTrace, err := commandJournal.Trace()
	if err != nil {
		return fail(err)
	}
	commandIdentity, err := recoveryFileIdentityDigest(entry.CommandJournalPath, false)
	if err != nil {
		return fail(err)
	}
	commandSessions := make([]productionRecoveryCommandSession, 0, len(checkpoint.Pins.ProviderTranscripts))
	transcripts := make([]GovernedRecoveryTranscript, 0, len(checkpoint.Pins.ProviderTranscripts))
	seenProviderPaths := make(map[string]struct{}, len(checkpoint.Pins.ProviderTranscripts)+1)
	for index, pin := range checkpoint.Pins.ProviderTranscripts {
		if pin.Ordinal != uint64(index+1) || pin.ConversationID == "" || pin.ConversationID == newConversationID {
			return fail(errors.Join(workflowrecovery.ErrObservation, ErrPiWorkflowCatalog))
		}
		path := productionRecoveryPinnedProviderJournalPath(artifacts.AuditRoot, entry, pin)
		if _, duplicate := seenProviderPaths[path]; duplicate {
			return fail(workflowrecovery.ErrObservation)
		}
		seenProviderPaths[path] = struct{}{}
		journal, openErr := openExistingRecoveryJournal(path)
		if openErr != nil {
			return fail(fmt.Errorf("open pinned provider transcript %d without creating a replacement: %w", pin.Ordinal, openErr))
		}
		oldProviderJournals = append(oldProviderJournals, journal)
		closers = append(closers, journal.Close)
		identity, identityErr := recoveryFileIdentityDigest(path, false)
		if identityErr != nil || identity != pin.JournalIdentityDigest {
			return fail(errors.Join(workflowrecovery.ErrObservation, identityErr))
		}
		profileMessages := []openrouter.Message{{Role: openrouter.System, Content: stringPointer(productionSystemPrompt)},
			{Role: openrouter.Developer, Content: stringPointer(productionDeveloperPrompt)}}
		providerProfile := broker.Profile{ID: profile.ProviderProfile, Model: config.providerRoute.Model(), Route: config.providerRoute.Route(),
			Messages: profileMessages, ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
			ResponseIDIssuer: governedResponseIssuer}
		providerConfig := providerbridge.Config{WorkflowID: entry.WorkflowID, ConversationID: pin.ConversationID,
			ProfileID: profile.ProviderProfile, SystemPrompt: productionSystemPrompt, DeveloperPrompt: productionDeveloperPrompt}
		replayPins := GovernedReplayPins{
			Provider: providerbridge.RecoveryPins{Config: providerConfig, Model: config.providerRoute.Model(), Policy: policy},
			Profile:  providerProfile, StorePolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
			BashOutputPaths: cloneDurableOutputScopePaths(entry.BashOutputPaths),
		}
		transcripts = append(transcripts, GovernedRecoveryTranscript{Journal: journal, JournalPath: path,
			IdentityDigest: identity, Pin: pin, Replay: replayPins})
		sessionID, runID := recoveryCommandSessionForPin(entry, pin)
		commandSessions = append(commandSessions, productionRecoveryCommandSession{RunID: runID,
			SessionID: sessionID, ConversationID: pin.ConversationID})
	}
	providerTrace, providerTraceErr := audit.Trace{}, error(nil)
	if info, statErr := os.Lstat(newProviderPath); statErr == nil {
		_ = info
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fail(statErr)
	}
	newProviderJournal, providerTraceErr = openNewRecoveryJournal(newProviderPath)
	if providerTraceErr != nil {
		return fail(fmt.Errorf("open only the deterministic empty journal for the next clean conversation: %w", providerTraceErr))
	}
	closers = append(closers, newProviderJournal.Close)
	providerTrace, err = newProviderJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return fail(errors.Join(errors.New("new clean conversation journal must be exactly empty before reservation"), err))
	}
	newProviderIdentity, err := recoveryFileIdentityDigest(newProviderPath, false)
	if err != nil {
		return fail(err)
	}
	storeIdentity, err := recoveryDirectoryIdentityDigest(storeRoot)
	if err != nil {
		return fail(err)
	}
	adminIdentity, err := recoveryDirectoryIdentityDigest(adminRoot)
	if err != nil {
		return fail(err)
	}
	if !validDigestText(storeIdentity) || !validDigestText(adminIdentity) || !validDigestText(commandIdentity) ||
		!validDigestText(newProviderIdentity) || auditHeadHash(storeTrace.Records) == "" && len(storeTrace.Records) != 0 {
		return fail(ErrGovernedRecoveryUnavailable)
	}
	commandPin := productionHistoricalCommandPin{IdentityDigest: commandIdentity,
		Head: auditHeadHash(commandTrace.Records), RecordCount: uint64(len(commandTrace.Records))}
	commandHistories := []productionRecoveryCommandJournal{{Journal: commandJournal, Path: entry.CommandJournalPath,
		Pin: commandPin, Sessions: commandSessions}}
	policyDigest, metadataDigest := bareWorkflowDigest(storePolicyDigest), bareWorkflowDigest(metadataPolicyDigest)
	if policyDigest != bareWorkflowDigest(storeOptions.PolicyDigest) || metadataDigest != bareWorkflowDigest(storeOptions.MetadataPolicyDigest) {
		return fail(ErrGovernedRecoveryUnavailable)
	}
	var commandLauncherForRunner *piruntime.D09PodmanCrunLauncher
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		if commandLauncherForRunner == nil {
			return nil, ErrProductionBashRunnerUnavailable
		}
		inner, runnerErr := piruntime.NewD09CommandRunner(leaseID, commandLauncherForRunner)
		if runnerErr != nil {
			return nil, runnerErr
		}
		return newProductionD09BashRunner(leaseID, inner, commandJournal, newRunID, newSessionID,
			entry.WorkflowID, newConversationID, profile, template), nil
	})
	gitPin, err := productionRecoveryGitPin(storeTrace, checkpoint.Pins)
	if err != nil {
		return fail(err)
	}
	providerProfile := broker.Profile{ID: profile.ProviderProfile, Model: config.providerRoute.Model(), Route: config.providerRoute.Route(),
		Messages: []openrouter.Message{{Role: openrouter.System, Content: stringPointer(productionSystemPrompt)},
			{Role: openrouter.Developer, Content: stringPointer(productionDeveloperPrompt)}},
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
		ResponseIDIssuer: governedResponseIssuer}
	request := governedRecoveryRequest{
		Owner: admission.Owner, Ledger: admission.Ledger, WorkflowID: entry.WorkflowID,
		NewOwnerEpoch: admission.CandidateEpoch, NewSessionID: newSessionID, NewConversationID: newConversationID,
		Limits: checkpoint.Limits, StoreRoot: storeRoot, StoreJournalPath: entry.StoreJournalPath,
		StoreOptions: storeOptions, AdminRoot: adminRoot, GitPin: gitPin,
		Transcripts: transcripts, CommandHistories: commandHistories, ProductionProfile: profile, D09Template: template,
		GatePolicy: policy, ProviderProfile: providerProfile, ProviderRoute: config.providerRoute,
		ProviderProfileDigest: config.providerRoute.ProviderProfileDigest(profile.ProviderProfileDigest),
		ImageDigest:           template.ExpectedImageDigest, RuntimeProfileDigest: profile.PiRuntimeBundleDigest,
		SystemPrompt: productionSystemPrompt, DeveloperPrompt: productionDeveloperPrompt,
		StorePolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
		BashOutputPaths: cloneDurableOutputScopePaths(entry.BashOutputPaths),
		CommandRunner:   runner, GovernedCommandRunner: runner,
		ProviderTokenUpperBound: productionProviderTokenUpperBound(checkpoint.Limits), RequireEvidencedBash: true,
		FreshProviderJournal: newProviderJournal, FreshProviderJournalPath: newProviderPath, FreshJournalIdentity: newProviderIdentity,
		CorpusManifestDigest: entry.CorpusManifestDigest,
	}
	candidate, err := prepareGovernedRecovery(ctx, request)
	if err != nil {
		return fail(fmt.Errorf("read-only replay, external retirement, and durable clean-restart reservation failed: %w", err))
	}
	recoveryCandidate = candidate
	closers = append(closers, recoveryCandidate.Close)

	// The durable restart receipt now exists. Only after this point acquire C's
	// managed-live-root writer capability and run its recovery/source gates.
	publicationStateRoot, err := openExistingPrivateRecoveryDirectory(artifacts.PublicationStateRoot)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, publicationStateRoot.Close)
	publicationJournal, err := openExistingRecoveryJournal(filepath.Join(artifacts.AuditRoot, "publication-audit.jsonl"))
	if err != nil {
		return fail(err)
	}
	closers = append(closers, publicationJournal.Close)
	candidateLiveRoot, err := os.Open(artifacts.LiveRoot)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, candidateLiveRoot.Close)
	publicationID := "production-publication-" + entry.WorkflowID
	publicationEpoch, err := nextManagedRootOwnerEpoch(publicationJournal, candidateLiveRoot)
	if err != nil {
		return fail(err)
	}
	managedRootLease, err := processManagedLiveRootAuthority.acquire(ctx, candidateLiveRoot, template,
		publicationJournal, newRunID, entry.WorkflowID, publicationID, publicationEpoch)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, managedRootLease.close)
	if err := candidateLiveRoot.Close(); err != nil {
		return fail(err)
	}
	liveRoot, err := managedRootLease.duplicateRoot()
	if err != nil {
		return fail(err)
	}
	closers = append(closers, liveRoot.Close)
	if err := recoverManagedLiveRootBeforeCapture(ctx, managedRootLease, liveRoot, publicationStateRoot,
		publicationJournal, publicationEpoch); err != nil {
		return fail(fmt.Errorf("recover managed-root lifecycle only after clean-restart reservation: %w", err))
	}
	verifiedRoot, ok := managedRootLease.verified.(*piruntime.VerifiedD09ManagedLiveRoot)
	if !ok {
		return fail(ErrManagedSourceCaptureUnavailable)
	}
	sourceQualification, err := piruntime.D09ManagedLiveRootSnapshotEvidenceForSeed(ctx, verifiedRoot,
		liveRoot, profile, template, managedRootLease)
	if err != nil {
		return fail(fmt.Errorf("qualify retained managed-root identity under the new owner lease: %w", err))
	}
	qualifiedXattr, err := sourceQualification.XattrVisibilityAttestation()
	if err != nil || qualifiedXattr != xattrVisibility {
		return fail(errors.Join(ErrProductionXattrVisibilityUnavailable, err))
	}

	recovered, err := candidate.Activate(ctx)
	if err != nil {
		return fail(fmt.Errorf("revalidate receipt and retained pins before managed Store/D06 activation: %w", err))
	}
	managed, admin, prepared = recovered.Managed, recovered.Admin, recovered.Prepared
	closers = closers[:len(closers)-1] // Candidate transferred the activated Store/admin/D06 handles.
	closers = append(closers, managed.Close, admin.Close, prepared.Close)
	baseline, tip := managed.Store().BaselineGeneration(), recovered.Generation
	if baseline == nil || tip == nil || managed.Store().TipGeneration() != tip {
		return fail(ErrGovernedRecoveryUnavailable)
	}
	if err := recovered.Authority.BindPreparedSession(prepared); err != nil {
		return fail(err)
	}
	session = &productionPiSession{
		Profile: profile, D09Template: template, Authority: recovered.Authority,
		Prepared: prepared, Store: managed.Store(), Generation: tip, ManagedStore: managed,
		Lifecycle: &productionLifecycleEvidence{}, StoreJournal: storeJournal,
		ProviderJournal: newProviderJournal, CommandJournal: commandJournal,
		Policy: policy, CallIssuer: governedCallIssuer, StorePolicyDigest: storePolicyDigest,
		MetadataPolicyDigest: metadataPolicyDigest, CommandRunner: runner, ProductionCommandRunner: runner,
		SessionID: newSessionID, RunID: newRunID, WorkflowID: entry.WorkflowID,
		ConversationID: newConversationID, ProviderProfileID: profile.ProviderProfile,
		SystemPrompt: productionSystemPrompt, DeveloperPrompt: productionDeveloperPrompt,
		recoveredProviderConversation: recovered.Conversation,
		recoveryPromptTaskID:          recovered.RecoveryPromptTask,
		recoveryPromptRequestID:       recovered.RecoveryPromptRequest,
		recoveryPrompt:                recovered.RecoveryPrompt,
		LiveRoot:                      liveRoot, ManagedLiveRootLease: managedRootLease,
		PublicationStateRoot: publicationStateRoot, PublicationJournal: publicationJournal,
		OwnerEpoch: publicationEpoch, PublicationID: publicationID,
		Workspace: workspace.Options{Generation: tip.ID(), MetadataPolicyDigest: metadataPolicyDigest,
			Limits: workspace.DefaultLimits(), XattrVisibility: qualifiedXattr},
	}
	if err := managedRootLease.revalidate(ctx, liveRoot); err != nil {
		return fail(err)
	}
	source, err := prepared.OpenVerifiedD09ExposureSource(ctx, tip)
	if err != nil {
		return fail(fmt.Errorf("open D09 source for the exact current retained Store tip: %w", err))
	}
	closers = append(closers, source.Close)
	bundleRoot, err := os.Open(profile.PiRuntimeBundleRoot)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, bundleRoot.Close)
	assets, err := sessionlaunch.OpenVerifiedD09RuntimeAssetSource(bundleRoot, sessionlaunch.RuntimeAssetSpec{
		BundleDigest: profile.PiRuntimeBundleDigest, NamespaceProfileDigest: profile.ContainmentProfileDigest,
	})
	if err != nil {
		return fail(err)
	}
	closers = append(closers, assets.Close)
	scope, scopePath, err := openProductionCgroupScopeNamed(ctx, template, profile,
		workflowOwnerScopeName(entry.WorkflowID, admission.CandidateEpoch))
	if err != nil {
		return fail(err)
	}
	scopeIdentity := scope.IdentityDigest()
	if !validDigestText(scopeIdentity) {
		return fail(ErrPiWorkflowCatalog)
	}
	removeScope := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := scope.Remove(cleanupCtx); err != nil {
			return err
		}
		if ownerScopeBound {
			return admission.Catalog.MarkOwnerScopeDrained(entry.AdmissionRecordHash, entry.WorkflowID,
				admission.CandidateEpoch, admission.OwnerIncarnation, filepath.Clean(template.CgroupsPath), "session_cleanup_complete")
		}
		return nil
	}
	closers = append(closers, removeScope)
	if err := admission.Catalog.BindOwnerScope(admission, admission.CandidateEpoch, admission.OwnerIncarnation,
		scopePath, scopeIdentity); err != nil {
		return fail(err)
	}
	ownerScopeBound = true
	launcherConfig := template.Config()
	launcherConfig.CgroupsPath = scopePath
	launcher := piruntime.NewD09PodmanCrunLauncher(launcherConfig)
	closeLauncher := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return launcher.Close(cleanupCtx)
	}
	scopeCloserIndex := len(closers) - 1
	closers = append(closers, nil)
	copy(closers[scopeCloserIndex+1:], closers[scopeCloserIndex:])
	closers[scopeCloserIndex] = closeLauncher
	if err := managedRootLease.bindLauncher(launcher); err != nil {
		return fail(err)
	}
	if err := launcher.BindCgroupScope(scope); err != nil {
		return fail(err)
	}
	commandLauncher = launcher
	commandLauncherForRunner = launcher
	bindings, d09Channels, err := piruntime.BindProductionPiWorkerBindingsWithSource(ctx, profile,
		source, source.SourceCapability(), assets, launcher, scope)
	if err != nil {
		return fail(fmt.Errorf("assemble clean-restart D09 Pi worker bindings: %w", err))
	}
	closers = append(closers, d09Channels.Close)
	session.Bindings, session.D09Channels, session.Channels = bindings, d09Channels, d09Channels.PiWorkerChannels()
	var closeOnce sync.Once
	var closeErr error
	session.Close = func() error {
		closeOnce.Do(func() {
			if retainProductionCleanupIfUnsafe(&productionCleanupQuarantine, session.Lifecycle.snapshot(),
				session.ProductionCommandRunner, closers) {
				closeErr = ErrProductionLifecycleUnsettled
				return
			}
			if session.Authority != nil {
				session.Authority.mu.Lock()
				conversation := session.Authority.conversation
				session.Authority.mu.Unlock()
				if conversation != nil {
					conversation.Close()
				}
			}
			var errs []error
			for index := len(closers) - 1; index >= 0; index-- {
				if err := closers[index](); err != nil {
					errs = append(errs, err)
					retainProductionCleanup(closers[:index+1])
					break
				}
			}
			closeErr = errors.Join(errs...)
		})
		return closeErr
	}
	_ = commandLauncher
	return session, nil
}

func productionRecoveryProviderJournalPath(auditRoot, workflowID, conversationID string) string {
	if !filepath.IsAbs(auditRoot) || filepath.Clean(auditRoot) != auditRoot ||
		!validIdentityText(workflowID) || !validIdentityText(conversationID) ||
		strings.ContainsAny(conversationID, "/\\\x00") {
		return ""
	}
	return filepath.Join(auditRoot, conversationID+"-provider-audit.jsonl")
}

func productionRecoveryPinnedProviderJournalPath(auditRoot string, entry piServiceWorkflowEntry,
	pin workflowrecovery.TranscriptPin) string {
	if pin.ConversationID == entry.ConversationID {
		return entry.ProviderJournalPath
	}
	return productionRecoveryProviderJournalPath(auditRoot, entry.WorkflowID, pin.ConversationID)
}

func recoveryCommandSessionForPin(entry piServiceWorkflowEntry, pin workflowrecovery.TranscriptPin) (sessionID, runID string) {
	if pin.ConversationID == entry.ConversationID {
		return entry.SessionID, entry.RunID
	}
	return pin.ConversationID, pin.ConversationID
}

func openExistingPrivateRecoveryDirectory(path string) (*os.File, error) {
	if _, err := recoveryFileIdentityDigest(path, true); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	identity, err := recoveryDirectoryIdentityDigest(file)
	pathIdentity, pathErr := recoveryFileIdentityDigest(path, true)
	if err != nil || pathErr != nil || identity != pathIdentity {
		_ = file.Close()
		return nil, errors.Join(ErrGovernedRecoveryUnavailable, err, pathErr)
	}
	return file, nil
}

func openExistingRecoveryJournal(path string) (*audit.Journal, error) {
	want, err := recoveryFileIdentityDigest(path, false)
	if err != nil {
		return nil, err
	}
	journal, err := audit.Open(path)
	if err != nil {
		return nil, err
	}
	actual, identityErr := recoveryFileIdentityDigest(path, false)
	if identityErr != nil || actual != want {
		_ = journal.Close()
		return nil, errors.Join(ErrGovernedRecoveryUnavailable, identityErr)
	}
	return journal, nil
}

func openNewRecoveryJournal(path string) (*audit.Journal, error) {
	if _, err := os.Lstat(path); err == nil {
		if _, identityErr := recoveryFileIdentityDigest(path, false); identityErr != nil {
			return nil, identityErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	journal, err := audit.Open(path)
	if err != nil {
		return nil, err
	}
	trace, traceErr := journal.Trace()
	if traceErr != nil || len(trace.Records) != 0 || len(trace.Effects) != 0 {
		_ = journal.Close()
		return nil, errors.Join(errors.New("next clean provider conversation journal is not empty"), traceErr)
	}
	return journal, nil
}

func productionRecoveryGitPin(trace audit.Trace, pins workflowrecovery.Pins) (sessionlaunch.RecoveryGitPin, error) {
	type readyEvent struct {
		SchemaVersion string                   `json:"schema_version"`
		Provenance    sessionlaunch.Provenance `json:"provenance"`
		Snapshot      privategit.Snapshot      `json:"snapshot"`
	}
	var selected *readyEvent
	for _, record := range trace.Records {
		if record.Event.Kind != "sessionlaunch_private_git_ready" {
			continue
		}
		var ready readyEvent
		// Only the current D06 record version is accepted; an upgrade requires draining retained sessions first.
		if unmarshalGovernedJSON(record.Event.Data, &ready) == nil && ready.SchemaVersion != sessionrepo.D06ProvenanceSchemaVersion {
			return sessionlaunch.RecoveryGitPin{}, errors.Join(workflowrecovery.ErrObservation, fmt.Errorf("%w: record version %q, supported %q",
				sessionrepo.ErrUnsupportedProvenanceVersion, ready.SchemaVersion, sessionrepo.D06ProvenanceSchemaVersion))
		}
		if unmarshalGovernedJSON(record.Event.Data, &ready) != nil ||
			ready.Provenance.PrivateGit.RepositoryName == "" ||
			ready.Snapshot.TreeDigest != ready.Provenance.SourceTreeDigest ||
			!reflect.DeepEqual(ready.Snapshot.Manifest, ready.Provenance.PrivateGit.SourceManifest) {
			return sessionlaunch.RecoveryGitPin{}, workflowrecovery.ErrObservation
		}
		if selected != nil {
			return sessionlaunch.RecoveryGitPin{}, errors.New("retained Store journal contains multiple D06 private Git roots")
		}
		selected = &ready
	}
	if selected == nil || !validBareWorkflowDigest(pins.D06ProvenanceDigest) ||
		!validBareWorkflowDigest(pins.PrivateGitIdentityDigest) {
		return sessionlaunch.RecoveryGitPin{}, workflowrecovery.ErrMissing
	}
	return sessionlaunch.RecoveryGitPin{
		RepositoryName:           selected.Provenance.PrivateGit.RepositoryName,
		ProvenanceDigest:         sha256DigestFromBarePin(pins.D06ProvenanceDigest),
		RepositoryIdentityDigest: sha256DigestFromBarePin(pins.PrivateGitIdentityDigest),
		VerifyIdentity: func(identity privategit.RepositoryIdentity) error {
			if bareWorkflowDigest(identity.Digest()) != pins.PrivateGitIdentityDigest {
				return privategit.ErrReopenBinding
			}
			return nil
		},
	}, nil
}
