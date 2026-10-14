//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

var ErrGovernedRecoveryUnavailable = errors.New("host-authorized governed workflow recovery is unavailable")

// governedRecoveryRequest is assembled only by the trusted production host
// composition from an externally authenticated checkpoint. It is not parsed
// from client flags or provider data. All file handles refer to retained
// artifacts; recovery never falls back to creating another Store/workflow.
type governedRecoveryRequest struct {
	Owner                    workflowrecovery.Owner
	Ledger                   *workflowrecovery.Ledger
	WorkflowID               string
	NewOwnerEpoch            uint64
	NewSessionID             string
	NewConversationID        string
	Limits                   workflowrecovery.Limits
	StoreRoot                *os.File
	StoreJournalPath         string
	StoreOptions             sessionrepo.Options
	AdminRoot                *os.File
	GitPin                   sessionlaunch.RecoveryGitPin
	Transcripts              []GovernedRecoveryTranscript
	CommandHistories         []productionRecoveryCommandJournal
	ProductionProfile        piruntime.HostProfile
	D09Template              piruntime.D09CellTemplate
	GatePolicy               gate.Policy
	ProviderProfile          broker.Profile
	ProviderProfileDigest    string
	ImageDigest              string
	RuntimeProfileDigest     string
	SystemPrompt             string
	DeveloperPrompt          string
	StorePolicyDigest        string
	MetadataPolicyDigest     string
	BashOutputPaths          []string
	HistoricalSettlement     historicalSettlementVerifier
	CommandRunner            sessionrepo.CommandRunner
	GovernedCommandRunner    governedCommandRunner
	ProviderTokenUpperBound  uint64
	RequireEvidencedBash     bool
	FreshProviderJournal     *audit.Journal
	FreshProviderJournalPath string
	FreshJournalIdentity     string
	ConversationFactory      func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error)
}

type governedRecoveryCandidate struct {
	request             governedRecoveryRequest
	owner               workflowrecovery.Owner
	ledger              *workflowrecovery.Ledger
	previous            workflowrecovery.Pins
	previousCP          workflowrecovery.Checkpoint
	previousHead        string
	observed            workflowrecovery.Pins
	next                workflowrecovery.Pins
	receipt             workflowrecovery.Receipt
	projection          GovernedRecoveryProjection
	managed             *sessionlaunch.ManagedStore
	admin               *sessionlaunch.AdminParent
	gitCandidate        *sessionlaunch.RecoveryCandidate
	historical          *governedHistoricalStoreVerifier
	commandHistory      governedRecoveryCommandVerifier
	commandStable       interface{ VerifyUnchanged() error }
	needsPreEffectRetry bool
	closed              bool
}

// GovernedRecoverySession is the activated clean session. Its authority has an
// empty proof map, its conversation uses a new empty journal, and the only
// initial prompt is the canonical host-generated recovery envelope.
type governedRecoverySession struct {
	Managed               *sessionlaunch.ManagedStore
	Prepared              *sessionlaunch.PreparedSession
	Admin                 *sessionlaunch.AdminParent
	Store                 *sessionrepo.Store
	Generation            *sessionrepo.Generation
	Authority             *SessionOperationAuthority
	Durable               *DurableExecutor
	Conversation          *providerbridge.Conversation
	RecoveryPromptTask    string
	RecoveryPromptRequest string
	RecoveryPrompt        string
	Projection            GovernedRecoveryProjection
}

func (s *governedRecoverySession) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.Conversation != nil {
		s.Conversation.Close()
	}
	if s.Prepared != nil {
		errs = append(errs, s.Prepared.Close())
	}
	if s.Managed != nil {
		errs = append(errs, s.Managed.Close())
	}
	if s.Admin != nil {
		errs = append(errs, s.Admin.Close())
	}
	return errors.Join(errs...)
}

type governedRecoveryRuntimeBinding struct {
	SchemaVersion            string           `json:"schema_version"`
	RootIdentity             string           `json:"root_identity"`
	StoreIdentity            string           `json:"store_identity"`
	AdmissionIdentity        string           `json:"admission_identity"`
	AdminIdentity            string           `json:"admin_identity"`
	StoreRootIdentity        string           `json:"store_root_identity"`
	StoreJournalIdentity     string           `json:"store_journal_identity"`
	AdmissionJournalIdentity string           `json:"admission_journal_identity"`
	CommandJournalIdentities []string         `json:"command_journal_identities"`
	StorePolicyDigest        string           `json:"store_policy_digest"`
	MetadataPolicyDigest     string           `json:"metadata_policy_digest"`
	SystemPromptDigest       string           `json:"system_prompt_digest"`
	DeveloperPromptDigest    string           `json:"developer_prompt_digest"`
	GatePolicyDigest         string           `json:"gate_policy_digest"`
	ProviderProfileDigest    string           `json:"provider_profile_digest"`
	HostProfileDigest        string           `json:"host_profile_digest"`
	ImageDigest              string           `json:"image_digest"`
	RuntimeProfileDigest     string           `json:"runtime_profile_digest"`
	D09TemplateDigest        string           `json:"d09_template_digest"`
	D06ProvenanceDigest      string           `json:"d06_provenance_digest"`
	PrivateGitIdentityDigest string           `json:"private_git_identity_digest"`
	Limits                   workspace.Limits `json:"limits"`
	XattrProfileDigest       string           `json:"xattr_profile_digest"`
	BashOutputPaths          []string         `json:"bash_output_paths"`
}

type governedRecoveryIdentityPins struct {
	OwnerIncarnationDigest  string
	RootIdentityDigest      string
	StoreIdentityDigest     string
	AdmissionIdentityDigest string
	CommandIdentityDigest   string
	AdminIdentityDigest     string
}

// prepareGovernedRecovery performs a read-only full replay first, then verifies
// the prior owner fence/drain and reserves the restart. It does not repair the
// Store or install live callbacks. The receipt remains spent if the caller
// later refuses, crashes, or fails to create a worker.
func prepareGovernedRecovery(ctx context.Context, request governedRecoveryRequest) (*governedRecoveryCandidate, error) {
	if ctx == nil || request.Owner == nil || request.WorkflowID == "" || request.StoreRoot == nil || request.AdminRoot == nil ||
		request.StoreOptions.Journal == nil || request.NewOwnerEpoch == 0 ||
		!validIdentityText(request.NewSessionID) || !validIdentityText(request.NewConversationID) ||
		request.ProviderTokenUpperBound == 0 || request.FreshProviderJournal == nil {
		return nil, ErrGovernedRecoveryUnavailable
	}
	if request.StoreOptions.PolicyDigest != request.StorePolicyDigest ||
		request.StoreOptions.MetadataPolicyDigest != request.MetadataPolicyDigest || request.GatePolicy.Digest() == "" {
		return nil, errors.New("recovery Store/provider policy pins are incomplete or inconsistent")
	}
	if err := validateRecoveryJournalPaths(request); err != nil {
		return nil, err
	}
	if err := requireFreshProviderConversation(request.FreshProviderJournal); err != nil {
		return nil, err
	}
	freshIdentity, err := recoveryFileIdentityDigest(request.FreshProviderJournalPath, false)
	if err != nil || freshIdentity != request.FreshJournalIdentity {
		return nil, errors.Join(errors.New("fresh provider journal identity differs from the new checkpoint pin"), err)
	}

	checkpoint, checkpointHead, exists, err := request.Owner.LoadCheckpoint(ctx, request.WorkflowID)
	if err != nil || !exists {
		return nil, errors.Join(workflowrecovery.ErrMissing, err)
	}
	previous := checkpoint.Pins
	if request.Limits != checkpoint.Limits || request.NewOwnerEpoch <= previous.OwnerEpoch ||
		request.NewSessionID == previous.SessionID || request.NewConversationID == "" || previous.WorkflowID != request.WorkflowID {
		return nil, workflowrecovery.ErrOwnerFence
	}
	if err := validateRecoveryHostPins(request, previous); err != nil {
		return nil, err
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(request)
	if err != nil || previous.RootIdentityDigest != identityPins.RootIdentityDigest ||
		previous.StoreIdentityDigest != identityPins.StoreIdentityDigest ||
		previous.AdmissionIdentityDigest != identityPins.AdmissionIdentityDigest ||
		previous.CommandIdentityDigest != identityPins.CommandIdentityDigest ||
		previous.AdminIdentityDigest != identityPins.AdminIdentityDigest {
		return nil, errors.Join(workflowrecovery.ErrRollback, err)
	}
	ledger := request.Ledger
	if ledger == nil {
		ledger, err = workflowrecovery.Open(ctx, request.Owner, workflowrecovery.OpenOptions{
			WorkflowID: previous.WorkflowID, OwnerEpoch: request.NewOwnerEpoch,
			RetainedPins: previous, Limits: checkpoint.Limits,
		})
		if err != nil {
			return nil, err
		}
	} else {
		opened, openedHead := ledger.Snapshot()
		if !reflect.DeepEqual(opened, checkpoint) || openedHead != checkpointHead {
			return nil, workflowrecovery.ErrRollback
		}
		if err := request.Owner.VerifyFence(ctx, request.WorkflowID, request.NewOwnerEpoch); err != nil {
			return nil, errors.Join(workflowrecovery.ErrOwnerFence, err)
		}
	}
	currentCheckpoint, currentHead := ledger.Snapshot()
	if !reflect.DeepEqual(currentCheckpoint, checkpoint) || currentHead != checkpointHead {
		return nil, workflowrecovery.ErrRollback
	}

	previewOptions := request.StoreOptions
	previewOptions.AuthorizeOperation = nil
	previewOptions.VerifyDecision = nil
	previewOptions.VerifySettlement = nil
	commandVerifier, commandStable, err := openRecoveryCommandVerifier(request)
	if err != nil {
		return nil, err
	}
	runtimeDigest, err := governedRecoveryRuntimeDigest(request)
	if err != nil || runtimeDigest != previous.RuntimeDigest {
		return nil, errors.Join(workflowrecovery.ErrRollback, err)
	}
	historicalSettlement := request.HistoricalSettlement
	if commandVerifier != nil {
		historicalSettlement = commandVerifier.VerifySettlement
	}
	historical, err := newGovernedHistoricalStoreVerifier(previewOptions.Journal,
		request.StorePolicyDigest, request.MetadataPolicyDigest, historicalSettlement)
	if err != nil {
		return nil, err
	}
	retirementVerifier := governedUnleasedCallRetirementVerifier{historical: historical, command: commandVerifier}
	for index := range request.Transcripts {
		providerPins := &request.Transcripts[index].Replay.Provider
		providerPins.PreviousOwnerEpoch = previous.OwnerEpoch
		providerPins.RetirementOwnerEpoch = request.NewOwnerEpoch
		providerPins.CheckpointRecordCount = request.Transcripts[index].Pin.JournalCount
		providerPins.CheckpointRecordHead = request.Transcripts[index].Pin.JournalHead
		providerPins.RetirementVerifier = retirementVerifier
		providerPins.IdleRetirementVerifier = retirementVerifier
		providerPins.RequireHostCallReservations = true
	}
	managed, err := sessionlaunch.OpenRecoveryPreview(request.StoreRoot, previewOptions)
	if err != nil {
		return nil, err
	}
	cleanupManaged := true
	defer func() {
		if cleanupManaged {
			_ = managed.Close()
		}
	}()
	store := managed.Store()
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return nil, err
	}
	if err := verifyAuditPathBytes(filepath.Join(request.StoreRoot.Name(), "delta-admission.jsonl"), bundle.AdmissionJournal); err != nil {
		return nil, fmt.Errorf("verify retained admission journal pathname against the held Store preview: %w", err)
	}
	if err := historical.VerifyBundle(bundle); err != nil {
		return nil, fmt.Errorf("verify retained Store authorization/effect/command facts: %w", err)
	}
	projection, pendingVerifier, err := ReplayGovernedHistoryForRecovery(previewOptions.Journal, store,
		request.Transcripts, recoveryCommandVerifiers(commandVerifier)...)
	if err != nil {
		return nil, err
	}
	if len(projection.Transcripts) > 0 {
		lastProjection := projection.Transcripts[len(projection.Transcripts)-1].Provider
		needsLineageRetirement := lastProjection.Disposition == providerbridge.DispositionAwaitingGateDecision ||
			lastProjection.Disposition == providerbridge.DispositionAwaitingHostPrompt ||
			lastProjection.Disposition == providerbridge.DispositionAwaitingProviderExchange
		if needsLineageRetirement {
			if pendingVerifier == nil {
				return nil, workflowrecovery.ErrObservation
			}
			pendingVerifier.resetForReadOnlyReplay()
			lastIndex := len(request.Transcripts) - 1
			if lastProjection.Disposition == providerbridge.DispositionAwaitingGateDecision {
				if _, err := providerbridge.AppendUnleasedProviderCallRetirement(
					request.Transcripts[lastIndex].Journal, request.Transcripts[lastIndex].Replay.Provider, pendingVerifier); err != nil {
					return nil, fmt.Errorf("append externally verified retirement for the exact unleased captured call: %w", err)
				}
			} else if _, err := providerbridge.AppendIdleProviderLineageRetirement(
				request.Transcripts[lastIndex].Journal, request.Transcripts[lastIndex].Replay.Provider, pendingVerifier); err != nil {
				return nil, fmt.Errorf("append externally verified retirement for the idle provider boundary: %w", err)
			}
			projection, err = ReplayGovernedHistory(previewOptions.Journal, store, request.Transcripts, recoveryCommandVerifiers(commandVerifier)...)
			if err != nil {
				return nil, fmt.Errorf("strictly replay the retired provider lineage and all Store cross-links: %w", err)
			}
		}
	}
	observed, err := observedRecoveryPins(previous, checkpointHead, projection, request.Transcripts)
	if err != nil {
		return nil, err
	}
	admin, err := sessionlaunch.OpenAdminParentForRecovery(request.AdminRoot, request.GitPin.RepositoryName)
	if err != nil {
		return nil, err
	}
	cleanupAdmin := true
	defer func() {
		if cleanupAdmin {
			_ = admin.Close()
		}
	}()
	gitPin := request.GitPin
	gitPin.ProvenanceDigest = sha256DigestFromBarePin(previous.D06ProvenanceDigest)
	gitPin.RepositoryIdentityDigest = sha256DigestFromBarePin(previous.PrivateGitIdentityDigest)
	gitPin.VerifyIdentity = func(identity privategit.RepositoryIdentity) error {
		if bareWorkflowDigest(identity.Digest()) != previous.PrivateGitIdentityDigest {
			return privategit.ErrReopenBinding
		}
		return nil
	}
	gitCandidate, err := sessionlaunch.Rebind(managed, admin, gitPin)
	if err != nil {
		return nil, err
	}
	if err := gitCandidate.VerifyPreview(observed, previewOptions); err != nil {
		_ = gitCandidate.Close()
		return nil, err
	}
	verifier := governedRecoveryObservationVerifier{
		request: request, historical: historical, command: commandVerifier,
		commandStable: commandStable, managed: managed, candidate: gitCandidate,
		projectionDigest: projection.Digest, previousHead: checkpointHead,
	}
	observation, err := ledger.ObserveRecovery(ctx, verifier, observed)
	if err != nil {
		_ = gitCandidate.Close()
		return nil, err
	}
	next := cloneWorkflowPins(observed)
	next.SessionID = request.NewSessionID
	next.OwnerIncarnationDigest = identityPins.OwnerIncarnationDigest
	if next.RecoveryGeneration == ^uint64(0) {
		_ = gitCandidate.Close()
		return nil, workflowrecovery.ErrRollback
	}
	next.RecoveryGeneration++
	next.OwnerEpoch = request.NewOwnerEpoch
	newPin := workflowrecovery.TranscriptPin{
		Ordinal: uint64(len(next.ProviderTranscripts) + 1), ConversationID: request.NewConversationID,
		JournalIdentityDigest: freshIdentity, Disposition: workflowrecovery.DispositionAwaitingHostPrompt,
	}
	next.ProviderTranscripts = append(next.ProviderTranscripts, newPin)
	next.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(next.ProviderTranscripts)
	next.ProviderProjectionCount = workflowTranscriptProjectionCount(next.ProviderTranscripts)
	next.ProviderProjectionHead = ""
	next.ProviderDisposition = workflowrecovery.DispositionAwaitingHostPrompt
	next.PendingCallDisposition = workflowrecovery.PendingCallNone
	next.PreviousRecoveryLineageDigest = previous.RecoveryLineageDigest
	next.RecoveryLineageDigest = ""
	next.RecoveryLineageDigest = workflowrecovery.ComputeRecoveryLineageDigest(checkpointHead, previous, next)
	// A retired unleased call charges its pre-effect retry in the same checkpoint
	// as the clean restart, so the receipt is final before any activation step.
	needsPreEffectRetry := hasUnleasedRetiredCall(projection)
	reserve := ledger.ReserveCleanRestart
	if needsPreEffectRetry {
		reserve = ledger.ReserveCleanRestartWithPreEffectRetry
	}
	receipt, err := reserve(ctx, observation, next)
	if err != nil {
		_ = gitCandidate.Close()
		return nil, err
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, next); err != nil {
		_ = gitCandidate.Close()
		return nil, err
	}
	cleanupManaged, cleanupAdmin = false, false
	return &governedRecoveryCandidate{
		request: request, owner: request.Owner, ledger: ledger, previous: previous, previousCP: checkpoint,
		previousHead: checkpointHead, observed: observed, next: next, receipt: receipt,
		projection: projection, managed: managed, admin: admin, gitCandidate: gitCandidate,
		historical: historical, commandHistory: commandVerifier, commandStable: commandStable,
		needsPreEffectRetry: needsPreEffectRetry,
	}, nil
}

// Activate consumes the durable restart reservation before deferred Store
// repair, then binds a new empty-proof authority and clean provider conversation
// to the current verified Store tip. No old provider messages enter the Pi.
func (c *governedRecoveryCandidate) Activate(ctx context.Context) (*governedRecoverySession, error) {
	if c == nil || ctx == nil || c.closed || c.gitCandidate == nil || c.ledger == nil || !c.receipt.IsCleanRestart() {
		return nil, ErrGovernedRecoveryUnavailable
	}
	if err := c.revalidateAfterReservation(ctx); err != nil {
		return nil, fmt.Errorf("revalidate the spent clean-restart receipt and every retained pin before activation: %w", err)
	}
	request := c.request
	storeJournal := request.StoreOptions.Journal
	authority, err := NewSessionOperationAuthority(storeJournal, request.FreshProviderJournal,
		c.next.WorkflowID, request.NewConversationID, request.ProviderProfile.ID,
		request.GatePolicy, request.StorePolicyDigest, request.MetadataPolicyDigest,
		request.GovernedCommandRunner, request.BashOutputPaths)
	if err != nil {
		return nil, err
	}
	authority.requireEvidencedBashContainment = request.RequireEvidencedBash
	liveOptions := request.StoreOptions
	liveOptions.AuthorizeOperation = authority.AuthorizeOperation
	liveOptions.VerifyDecision = authority.VerifyDecision
	liveOptions.VerifySettlement = authority.VerifySettlement
	prepared, err := c.gitCandidate.Activate(ctx, c.ledger, c.receipt, c.next, liveOptions)
	if err != nil {
		return nil, err
	}
	baseline := c.managed.Store().BaselineGeneration()
	tip := c.managed.Store().TipGeneration()
	if err := authority.BindRecoveredBaseline(c.managed, baseline, tip, c.next); err != nil {
		_ = prepared.Close()
		return nil, err
	}
	if err := authority.BindPreparedSession(prepared); err != nil {
		_ = prepared.Close()
		return nil, err
	}
	if err := authority.BindWorkflowLedger(c.ledger, c.next, request.ProviderTokenUpperBound); err != nil {
		_ = prepared.Close()
		return nil, err
	}
	taskID, recoveryRequestID, recoveryPrompt, err := buildCleanRecoveryEnvelope(c.previous, c.next, c.previousHead, c.receipt, c.ledger)
	if err != nil {
		_ = prepared.Close()
		return nil, err
	}
	if err := authority.BindRecoveryPrompt(taskID, recoveryPrompt); err != nil {
		_ = prepared.Close()
		return nil, err
	}
	config := providerbridge.Config{
		WorkflowID: c.next.WorkflowID, ConversationID: request.NewConversationID,
		ProfileID: request.ProviderProfile.ID, InitialGenerationID: tip.ID(), InitialTreeDigest: tip.TreeDigest(),
		SystemPrompt: request.SystemPrompt, DeveloperPrompt: request.DeveloperPrompt,
	}
	var conversation *providerbridge.Conversation
	if request.ConversationFactory == nil {
		conversation, err = authority.NewProviderConversation(config)
	} else {
		conversation, err = authority.newProviderConversation(config, request.ConversationFactory)
	}
	if err != nil {
		_ = prepared.Close()
		return nil, err
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: c.managed.Store(), Tip: tip, CallIssuer: governedCallIssuer,
		PolicyDigest: request.StorePolicyDigest, MetadataPolicyDigest: request.MetadataPolicyDigest,
		BashOutputPaths: request.BashOutputPaths, CommandRunner: request.CommandRunner,
		AfterDurableOperation: authority.SettleDurableOperation,
	})
	if err != nil {
		conversation.Close()
		_ = prepared.Close()
		return nil, err
	}
	c.closed = true
	managed, admin := c.managed, c.admin
	c.managed, c.admin, c.gitCandidate = nil, nil, nil
	return &governedRecoverySession{
		Managed: managed, Prepared: prepared, Admin: admin, Store: managed.Store(), Generation: tip,
		Authority: authority, Durable: durable, Conversation: conversation,
		RecoveryPromptTask: taskID, RecoveryPromptRequest: recoveryRequestID,
		RecoveryPrompt: recoveryPrompt, Projection: c.projection,
	}, nil
}

func (c *governedRecoveryCandidate) revalidateAfterReservation(ctx context.Context) error {
	if c == nil || ctx == nil || c.owner == nil || c.ledger == nil || c.managed == nil || c.gitCandidate == nil ||
		c.historical == nil || !c.receipt.IsCleanRestart() {
		return ErrGovernedRecoveryUnavailable
	}
	request := c.request
	if err := validateRecoveryJournalPaths(request); err != nil {
		return err
	}
	if err := validateRecoveryHostPins(request, c.previous); err != nil {
		return err
	}
	if err := c.owner.VerifyFence(ctx, c.next.WorkflowID, c.next.OwnerEpoch); err != nil {
		return errors.Join(workflowrecovery.ErrOwnerFence, err)
	}
	checkpoint, checkpointHead, exists, err := c.owner.LoadCheckpoint(ctx, c.next.WorkflowID)
	reserved, reservedHead := c.ledger.Snapshot()
	retryCharge := uint64(0)
	if c.needsPreEffectRetry {
		retryCharge = 1
	}
	if err != nil || !exists || !reflect.DeepEqual(checkpoint, reserved) || checkpointHead != reservedHead ||
		checkpoint.Pins.SessionID != c.next.SessionID || checkpoint.Pins.OwnerEpoch != c.next.OwnerEpoch ||
		checkpoint.Pins.RecoveryGeneration != c.next.RecoveryGeneration ||
		checkpoint.Counters.CleanRestarts != c.previousCP.Counters.CleanRestarts+1 ||
		checkpoint.Counters.PreEffectRetries != c.previousCP.Counters.PreEffectRetries+retryCharge ||
		checkpoint.ComputedCheckpointDigest != checkpointHead {
		return errors.Join(workflowrecovery.ErrRollback, err)
	}
	if err := c.ledger.VerifyCleanRestartReceipt(ctx, c.receipt, c.next); err != nil {
		return err
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(request)
	if err != nil || identityPins.RootIdentityDigest != c.previous.RootIdentityDigest ||
		identityPins.StoreIdentityDigest != c.previous.StoreIdentityDigest ||
		identityPins.AdmissionIdentityDigest != c.previous.AdmissionIdentityDigest ||
		identityPins.CommandIdentityDigest != c.previous.CommandIdentityDigest ||
		identityPins.AdminIdentityDigest != c.previous.AdminIdentityDigest {
		return errors.Join(workflowrecovery.ErrRollback, err)
	}
	freshIdentity, err := recoveryFileIdentityDigest(request.FreshProviderJournalPath, false)
	if err != nil || freshIdentity != request.FreshJournalIdentity || freshIdentity != c.next.ProviderTranscripts[len(c.next.ProviderTranscripts)-1].JournalIdentityDigest {
		return errors.Join(workflowrecovery.ErrRollback, err)
	}
	bundle, err := c.managed.Store().EvidenceBundle()
	if err != nil {
		return err
	}
	if err := c.historical.VerifyBundle(bundle); err != nil {
		return err
	}
	var commandVerifiers []governedRecoveryCommandVerifier
	if c.commandHistory != nil {
		commandVerifiers = append(commandVerifiers, c.commandHistory)
	}
	projection, err := ReplayGovernedHistory(request.StoreOptions.Journal, c.managed.Store(), request.Transcripts, commandVerifiers...)
	if err != nil || projection.Digest != c.projection.Digest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	expectedObserved, err := observedRecoveryPins(c.previous, c.previousHead, projection, request.Transcripts)
	if err != nil || !reflect.DeepEqual(expectedObserved, c.observed) {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	if err := c.gitCandidate.VerifyPreview(c.observed, request.StoreOptions); err != nil {
		return err
	}
	if err := c.historical.VerifyUnchanged(); err != nil {
		return err
	}
	if c.commandStable != nil {
		if err := c.commandStable.VerifyUnchanged(); err != nil {
			return err
		}
	}
	return nil
}

func hasUnleasedRetiredCall(projection GovernedRecoveryProjection) bool {
	if len(projection.Transcripts) == 0 {
		return false
	}
	return projection.Transcripts[len(projection.Transcripts)-1].Provider.Disposition == providerbridge.DispositionRetiredUnexecuted
}

type governedUnleasedCallRetirementVerifier struct {
	historical *governedHistoricalStoreVerifier
	command    governedRecoveryCommandVerifier
}

func (v governedUnleasedCallRetirementVerifier) VerifyUnleasedProviderCallRetirement(
	binding providerbridge.ProviderCallRetirementBinding) error {
	if v.historical == nil {
		return ErrGovernedRecoveryUnavailable
	}
	if err := v.historical.VerifyUnleasedProviderCallRetirement(binding); err != nil {
		return err
	}
	if v.command != nil {
		if err := v.command.VerifyUnleasedProviderCallRetirement(binding); err != nil {
			return err
		}
	}
	return nil
}

func (v governedUnleasedCallRetirementVerifier) VerifyIdleProviderLineageRetirement(workflowID, conversationID string,
	providerSequence, previousOwnerEpoch, retirementOwnerEpoch uint64) error {
	if v.historical == nil || workflowID == "" || conversationID == "" || retirementOwnerEpoch <= previousOwnerEpoch {
		return ErrGovernedRecoveryUnavailable
	}
	if err := v.historical.VerifyUnchanged(); err != nil {
		return err
	}
	if v.command != nil {
		if err := v.command.VerifyIdleProviderLineageRetirement(workflowID, conversationID,
			providerSequence, previousOwnerEpoch, retirementOwnerEpoch); err != nil {
			return err
		}
	}
	return nil
}

func (c *governedRecoveryCandidate) Close() error {
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	var errs []error
	if c.gitCandidate != nil {
		errs = append(errs, c.gitCandidate.Close())
	}
	if c.managed != nil {
		errs = append(errs, c.managed.Close())
	}
	if c.admin != nil {
		errs = append(errs, c.admin.Close())
	}
	return errors.Join(errs...)
}

type governedRecoveryObservationVerifier struct {
	request          governedRecoveryRequest
	historical       *governedHistoricalStoreVerifier
	command          governedRecoveryCommandVerifier
	commandStable    interface{ VerifyUnchanged() error }
	managed          *sessionlaunch.ManagedStore
	candidate        *sessionlaunch.RecoveryCandidate
	projectionDigest string
	previousHead     string
}

func (v governedRecoveryObservationVerifier) VerifyRecoveryObservation(ctx context.Context,
	previous workflowrecovery.Checkpoint, observed workflowrecovery.Pins) error {
	if ctx == nil || v.managed == nil || v.candidate == nil || v.historical == nil ||
		previous.ComputedCheckpointDigest != v.previousHead || previous.Pins.WorkflowID != observed.WorkflowID {
		return workflowrecovery.ErrObservation
	}
	if err := validateRecoveryJournalPaths(v.request); err != nil {
		return err
	}
	bundle, err := v.managed.Store().EvidenceBundle()
	if err != nil {
		return err
	}
	if err := v.historical.VerifyBundle(bundle); err != nil {
		return err
	}
	var commandVerifiers []governedRecoveryCommandVerifier
	if v.command != nil {
		commandVerifiers = append(commandVerifiers, v.command)
	}
	projection, err := ReplayGovernedHistory(v.request.StoreOptions.Journal, v.managed.Store(), v.request.Transcripts, commandVerifiers...)
	if err != nil || projection.Digest != v.projectionDigest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	expected, err := observedRecoveryPins(previous.Pins, previous.ComputedCheckpointDigest, projection, v.request.Transcripts)
	if err != nil || !reflect.DeepEqual(expected, observed) {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	if err := v.candidate.VerifyPreview(observed, v.request.StoreOptions); err != nil {
		return err
	}
	if err := v.historical.VerifyUnchanged(); err != nil {
		return err
	}
	if v.commandStable != nil {
		if err := v.commandStable.VerifyUnchanged(); err != nil {
			return err
		}
	}
	return nil
}

func observedRecoveryPins(previous workflowrecovery.Pins, previousHead string,
	projection GovernedRecoveryProjection, transcripts []GovernedRecoveryTranscript) (workflowrecovery.Pins, error) {
	if len(previous.ProviderTranscripts) != len(transcripts) || len(projection.Transcripts) != len(transcripts) {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	observed := cloneWorkflowPins(previous)
	var latestHostCall *providerbridge.HostCallBinding
	var latestHostCallOutcome string
	var unleasedCall *providerbridge.ProviderCallRetirementBinding
	for index, transcript := range transcripts {
		old := previous.ProviderTranscripts[index]
		actual := transcript.Pin
		projected := projection.Transcripts[index].Provider
		if projected.HostCall != nil {
			binding := *projected.HostCall
			latestHostCall = &binding
			latestHostCallOutcome = projected.HostCallOutcomeDigest
		}
		if !reflect.DeepEqual(old, actual) || actual.Ordinal != uint64(index+1) ||
			actual.ConversationID != projected.ConversationID || actual.JournalIdentityDigest != transcript.IdentityDigest ||
			projected.ProviderJournalRecordCount < actual.JournalCount || projected.EventCount < actual.ProjectionCount {
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
		if index != len(transcripts)-1 &&
			(projected.ProviderJournalRecordCount != actual.JournalCount || projected.EventCount != actual.ProjectionCount) {
			return workflowrecovery.Pins{}, errors.New("only the latest workflow conversation may have an uncheckpointed monotonic tail")
		}
		if actual.Disposition == workflowrecovery.DispositionTerminal || actual.Disposition == workflowrecovery.DispositionRetired {
			if projected.ProviderJournalRecordCount != actual.JournalCount || projected.EventCount != actual.ProjectionCount ||
				projected.ProviderJournalHead != actual.JournalHead || bareWorkflowDigest(projected.ProjectionDigest) != actual.ProjectionDigest {
				return workflowrecovery.Pins{}, errors.New("a terminal historical provider transcript changed after its checkpoint")
			}
		} else if index == len(transcripts)-1 {
			retiredCall := projected.Disposition == providerbridge.DispositionRetiredUnexecuted &&
				projected.RetiredCall != nil
			idleRetirement := projected.Disposition == providerbridge.DispositionRetired &&
				projected.RetiredCall == nil
			if projected.EventCount == actual.ProjectionCount || (!retiredCall && !idleRetirement) || projected.PendingCall != nil {
				return workflowrecovery.Pins{}, errors.New("active provider transcript lacks an explicit externally verified lineage retirement")
			}
			actual.Disposition = workflowrecovery.DispositionRetired
			if retiredCall {
				unleasedCall = projected.RetiredCall
			}
		}
		actual.JournalCount = projected.ProviderJournalRecordCount
		actual.JournalHead = projected.ProviderJournalHead
		actual.ProjectionCount = projected.EventCount
		actual.ProjectionDigest = bareWorkflowDigest(projected.ProjectionDigest)
		actual.ProjectionHead = projected.ProviderJournalHead
		observed.ProviderTranscripts[index] = actual
	}
	if reservation := observed.HostCallReservation; reservation != nil {
		switch reservation.Disposition {
		case workflowrecovery.HostCallReserved, workflowrecovery.HostCallUnknown:
			if latestHostCall == nil || latestHostCall.CallID != reservation.Binding.CallID ||
				latestHostCall.RequestDigest != reservation.Binding.RequestDigest ||
				latestHostCall.TokenUpperBound != reservation.Binding.TokenUpperBound ||
				!validWorkflowDigest(latestHostCallOutcome) {
				return workflowrecovery.Pins{}, workflowrecovery.ErrPendingUnknown
			}
			reservation.Disposition = workflowrecovery.HostCallTerminal
			reservation.OutcomeDigest = latestHostCallOutcome
		case workflowrecovery.HostCallTerminal, workflowrecovery.HostCallRetired:
		default:
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
	}
	if reservation := observed.EffectReservation; reservation != nil &&
		(reservation.Disposition == workflowrecovery.EffectReservationReserved ||
			reservation.Disposition == workflowrecovery.EffectReservationUnknown) {
		// Provider result delivery and Store terminal accounting are distinct
		// boundaries. This replay currently admits only exact host-call terminal
		// reconciliation; a consumed Store reservation without its typed terminal
		// receipt remains paused for a dedicated Store/authorization verifier.
		return workflowrecovery.Pins{}, workflowrecovery.ErrPendingUnknown
	}
	observed.StoreJournalHead = projection.StoreHead
	observed.StoreJournalCount = projection.StoreCount
	observed.AdmissionJournalHead = projection.AdmissionHead
	observed.AdmissionJournalCount = projection.AdmissionCount
	observed.StoreTipGenerationID = projection.StoreTip
	observed.StoreTipTreeDigest = bareStoreTreeDigest(projection.StoreTree)
	observed.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(observed.ProviderTranscripts)
	observed.ProviderProjectionCount = workflowTranscriptProjectionCount(observed.ProviderTranscripts)
	if len(observed.ProviderTranscripts) == 0 {
		observed.ProviderProjectionHead = ""
		observed.ProviderDisposition = workflowrecovery.DispositionNone
	} else {
		last := observed.ProviderTranscripts[len(observed.ProviderTranscripts)-1]
		observed.ProviderProjectionHead = last.ProjectionHead
		observed.ProviderDisposition = last.Disposition
	}
	if previous.PendingCallDisposition == workflowrecovery.PendingCallInFlight ||
		previous.PendingCallDisposition == workflowrecovery.PendingCallUnknown {
		return workflowrecovery.Pins{}, workflowrecovery.ErrPendingUnknown
	}
	if previous.PendingCallDisposition != workflowrecovery.PendingCallTerminal &&
		previous.PendingCallDisposition != workflowrecovery.PendingCallRetired {
		observed.PendingCallDisposition = workflowrecovery.PendingCallNone
		for _, projection := range projection.Transcripts {
			if projection.Provider.CapturedCallCount > 0 {
				observed.PendingCallDisposition = workflowrecovery.PendingCallTerminal
			}
		}
	}
	if unleasedCall != nil {
		// The host has externally retired an unleased captured call. Its retirement
		// digest commits to the exact retirement binding, and the pending-call
		// lineage becomes retired rather than terminal.
		reservation := observed.HostCallReservation
		if reservation == nil || reservation.Disposition != workflowrecovery.HostCallTerminal ||
			reservation.CapturedCall == nil || reservation.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable ||
			reservation.CapturedCall.ConversationID != unleasedCall.ConversationID ||
			reservation.CapturedCall.ToolCallID != unleasedCall.ToolCallID {
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
		// cloneWorkflowPins is shallow, so build fresh reservation values rather than
		// mutating the retained checkpoint that the caller still holds.
		captured := *reservation.CapturedCall
		captured.Disposition = workflowrecovery.CapturedCallRetired
		captured.RetirementDigest = recoveryIdentityDigest(*unleasedCall)
		retired := *reservation
		retired.CapturedCall = &captured
		observed.HostCallReservation = &retired
		observed.PendingCallDisposition = workflowrecovery.PendingCallRetired
	}
	_ = previousHead // The ledger observation proof binds this exact predecessor digest.
	return observed, nil
}

func workflowTranscriptProjectionCount(transcripts []workflowrecovery.TranscriptPin) uint64 {
	var count uint64
	for _, transcript := range transcripts {
		count += transcript.ProjectionCount
	}
	return count
}

func recoveryRemaining(maximum, used uint64) uint64 {
	if used >= maximum {
		return 0
	}
	return maximum - used
}

func cloneWorkflowPins(pins workflowrecovery.Pins) workflowrecovery.Pins {
	pins.ProviderTranscripts = append([]workflowrecovery.TranscriptPin(nil), pins.ProviderTranscripts...)
	return pins
}

func buildCleanRecoveryEnvelope(previous, next workflowrecovery.Pins, previousHead string,
	receipt workflowrecovery.Receipt, ledger *workflowrecovery.Ledger) (string, string, string, error) {
	if ledger == nil || !receipt.IsCleanRestart() || receipt.PreviousDigest() != previousHead ||
		next.RecoveryLineageDigest != workflowrecovery.ComputeRecoveryLineageDigest(previousHead, previous, next) {
		return "", "", "", workflowrecovery.ErrObservation
	}
	checkpoint, _ := ledger.Snapshot()
	if !reflect.DeepEqual(checkpoint.Pins, next) {
		return "", "", "", workflowrecovery.ErrRollback
	}
	remaining := workflowrecovery.Remaining{
		PreEffectRetries: recoveryRemaining(checkpoint.Limits.MaxPreEffectRetries, checkpoint.Counters.PreEffectRetries),
		CleanRestarts:    recoveryRemaining(checkpoint.Limits.MaxCleanRestarts, checkpoint.Counters.CleanRestarts),
		HostCalls:        recoveryRemaining(checkpoint.Limits.MaxHostCalls, checkpoint.Counters.HostCalls),
		Tokens:           recoveryRemaining(checkpoint.Limits.MaxTokens, checkpoint.Counters.Tokens),
	}
	envelope := struct {
		SchemaVersion            string                     `json:"schema_version"`
		WorkflowID               string                     `json:"workflow_id"`
		SessionID                string                     `json:"session_id"`
		RecoveryGeneration       uint64                     `json:"recovery_generation"`
		OwnerEpoch               uint64                     `json:"owner_epoch"`
		PreviousCheckpointDigest string                     `json:"previous_checkpoint_digest"`
		RecoveryLineageDigest    string                     `json:"recovery_lineage_digest"`
		TaskDigest               string                     `json:"task_digest"`
		PromptDigest             string                     `json:"prompt_digest"`
		PolicyDigest             string                     `json:"policy_digest"`
		ProfileDigest            string                     `json:"profile_digest"`
		ImageDigest              string                     `json:"image_digest"`
		RuntimeDigest            string                     `json:"runtime_digest"`
		BaselineGeneration       string                     `json:"baseline_generation"`
		BaselineTreeDigest       string                     `json:"baseline_tree_digest"`
		VerifiedStoreTip         string                     `json:"verified_store_tip"`
		VerifiedStoreTreeDigest  string                     `json:"verified_store_tree_digest"`
		Remaining                workflowrecovery.Remaining `json:"remaining"`
		NextAction               string                     `json:"next_action"`
		PriorTranscriptCount     int                        `json:"prior_transcript_count"`
	}{"tbound-clean-recovery-envelope/v1", next.WorkflowID, next.SessionID, next.RecoveryGeneration,
		next.OwnerEpoch, receipt.PreviousDigest(), next.RecoveryLineageDigest,
		next.TaskDigest, next.PromptDigest, next.PolicyDigest, next.ProfileDigest, next.ImageDigest, next.RuntimeDigest,
		next.StoreBaselineGenerationID, next.StoreBaselineTreeDigest, next.StoreTipGenerationID,
		next.StoreTipTreeDigest, remaining, "admit_one_new_provider_prompt", len(previous.ProviderTranscripts)}
	if envelope.NextAction == "" {
		return "", "", "", workflowrecovery.ErrRollback
	}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > productionPromptMaxBytes {
		return "", "", "", errors.Join(ErrGovernedRecoveryUnavailable, err)
	}
	sessionHash := digestBytes([]byte(next.SessionID))
	taskID := "recovery-" + strings.TrimPrefix(sessionHash, "sha256:")[:32]
	return taskID, taskID + "-request", string(encoded), nil
}

func validateRecoveryHostPins(request governedRecoveryRequest, previous workflowrecovery.Pins) error {
	profileDigest := request.ProviderProfileDigest
	if request.ProductionProfile.Mode == piruntime.ModeProduction {
		profileDigest = request.ProductionProfile.Digest
	}
	if request.WorkflowID != previous.WorkflowID || request.GitPin.RepositoryName == "" ||
		bareWorkflowDigest(request.GitPin.ProvenanceDigest) != previous.D06ProvenanceDigest ||
		bareWorkflowDigest(request.GitPin.RepositoryIdentityDigest) != previous.PrivateGitIdentityDigest ||
		bareWorkflowDigest(profileDigest) != previous.ProfileDigest ||
		bareWorkflowDigest(request.ImageDigest) != previous.ImageDigest ||
		barePolicyDigest(request.GatePolicy.Digest()) != previous.PolicyDigest ||
		request.SystemPrompt == "" || request.DeveloperPrompt == "" ||
		request.StorePolicyDigest != request.StoreOptions.PolicyDigest ||
		request.MetadataPolicyDigest != request.StoreOptions.MetadataPolicyDigest ||
		request.ProviderProfile.ID == "" || request.ProviderProfile.Model != providerbridge.ApprovedModel ||
		!reflect.DeepEqual(request.ProviderProfile.ToolManifest, broker.DeclaredToolManifest()) ||
		bareWorkflowDigest(request.RuntimeProfileDigest) == "" {
		return workflowrecovery.ErrRollback
	}
	if request.ProductionProfile.Mode == piruntime.ModeProduction {
		templateDigest, err := request.D09Template.Digest()
		if len(request.CommandHistories) == 0 || err != nil ||
			request.ProductionProfile.ContainmentProfileDigest != templateDigest ||
			bareWorkflowDigest(request.ProductionProfile.ProviderProfileDigest) != bareWorkflowDigest(request.ProviderProfileDigest) ||
			request.ProviderProfile.ID != request.ProductionProfile.ProviderProfile ||
			bareWorkflowDigest(request.D09Template.ExpectedImageDigest) != previous.ImageDigest ||
			bareWorkflowDigest(request.ProductionProfile.PiRuntimeBundleDigest) != bareWorkflowDigest(request.RuntimeProfileDigest) {
			return errors.New("production recovery requires every retained old D09 command journal and exact runtime profile")
		}
	}
	for _, transcript := range request.Transcripts {
		wantProfile := request.ProviderProfile
		wantProfile.Generation = transcript.Replay.Provider.Config.InitialGenerationID
		if transcript.Pin.ConversationID == request.NewConversationID || transcript.Replay.Provider.Config.WorkflowID != previous.WorkflowID ||
			transcript.Replay.Provider.Config.ProfileID != request.ProviderProfile.ID ||
			transcript.Replay.Provider.Config.SystemPrompt != request.SystemPrompt ||
			transcript.Replay.Provider.Config.DeveloperPrompt != request.DeveloperPrompt ||
			transcript.Replay.Provider.Model != providerbridge.ApprovedModel ||
			transcript.Replay.Provider.Policy.Digest() != request.GatePolicy.Digest() ||
			!reflect.DeepEqual(transcript.Replay.Provider.Policy, request.GatePolicy) ||
			!reflect.DeepEqual(transcript.Replay.Profile, wantProfile) ||
			transcript.Replay.StorePolicyDigest != request.StorePolicyDigest ||
			transcript.Replay.MetadataPolicyDigest != request.MetadataPolicyDigest ||
			!reflect.DeepEqual(transcript.Replay.BashOutputPaths, request.BashOutputPaths) {
			return errors.New("retained provider transcript host profile differs from the authenticated workflow checkpoint")
		}
	}
	return nil
}

func validateRecoveryJournalPaths(request governedRecoveryRequest) error {
	if request.StoreRoot == nil || request.StoreJournalPath == "" ||
		!filepath.IsAbs(request.StoreJournalPath) || filepath.Clean(request.StoreJournalPath) != request.StoreJournalPath {
		return ErrGovernedRecoveryUnavailable
	}
	storeRootPath := request.StoreRoot.Name()
	if !filepath.IsAbs(storeRootPath) || filepath.Clean(storeRootPath) != storeRootPath {
		return ErrGovernedRecoveryUnavailable
	}
	if _, err := recoveryDirectoryIdentityDigest(request.StoreRoot); err != nil {
		return err
	}
	if _, err := recoveryFileIdentityDigest(request.StoreJournalPath, false); err != nil {
		return err
	}
	if err := verifyJournalPathMatches(request.StoreJournalPath, request.StoreOptions.Journal); err != nil {
		return fmt.Errorf("verify Store journal path against the held audit descriptor: %w", err)
	}
	if _, err := recoveryFileIdentityDigest(filepath.Join(storeRootPath, "delta-admission.jsonl"), false); err != nil {
		return err
	}
	for _, transcript := range request.Transcripts {
		if transcript.Journal == nil || transcript.JournalPath == "" ||
			!filepath.IsAbs(transcript.JournalPath) || filepath.Clean(transcript.JournalPath) != transcript.JournalPath {
			return ErrGovernedRecoveryUnavailable
		}
		identity, err := recoveryFileIdentityDigest(transcript.JournalPath, false)
		if err != nil || identity != transcript.IdentityDigest || identity != transcript.Pin.JournalIdentityDigest {
			return errors.Join(workflowrecovery.ErrObservation, err)
		}
		if err := verifyJournalPathMatches(transcript.JournalPath, transcript.Journal); err != nil {
			return fmt.Errorf("provider transcript path differs from its held journal descriptor: %w", err)
		}
	}
	freshIdentity, freshErr := recoveryFileIdentityDigest(request.FreshProviderJournalPath, false)
	if freshErr != nil || freshIdentity != request.FreshJournalIdentity {
		return errors.Join(workflowrecovery.ErrObservation, freshErr)
	}
	if err := verifyJournalPathMatches(request.FreshProviderJournalPath, request.FreshProviderJournal); err != nil {
		return fmt.Errorf("fresh provider path differs from its held journal descriptor: %w", err)
	}
	for _, command := range request.CommandHistories {
		if command.Journal == nil || command.Path == "" || !filepath.IsAbs(command.Path) || filepath.Clean(command.Path) != command.Path {
			return ErrGovernedRecoveryUnavailable
		}
		identity, err := recoveryFileIdentityDigest(command.Path, false)
		trace, traceErr := command.Journal.Trace()
		if err != nil || traceErr != nil || identity != command.Pin.IdentityDigest ||
			uint64(len(trace.Records)) != command.Pin.RecordCount || auditHeadHash(trace.Records) != command.Pin.Head {
			return errors.Join(workflowrecovery.ErrObservation, err, traceErr)
		}
		if err := verifyJournalPathMatches(command.Path, command.Journal); err != nil {
			return fmt.Errorf("old command journal path differs from its held descriptor: %w", err)
		}
	}
	if request.FreshProviderJournalPath == "" {
		return ErrGovernedRecoveryUnavailable
	}
	return nil
}

func verifyJournalPathMatches(path string, journal *audit.Journal) error {
	if journal == nil {
		return ErrGovernedRecoveryUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fromPath, err := audit.Verify(bytes.NewReader(data))
	if err != nil {
		return err
	}
	held, err := journal.Trace()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fromPath, held) {
		return errors.New("journal path and held descriptor have different verified records")
	}
	return nil
}

func verifyAuditPathBytes(path string, want []byte) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, want) {
		return errors.New("audit path bytes differ from the retained evidence snapshot")
	}
	return nil
}

func openRecoveryCommandVerifier(request governedRecoveryRequest) (governedRecoveryCommandVerifier, interface{ VerifyUnchanged() error }, error) {
	if len(request.CommandHistories) == 0 {
		return nil, nil, nil
	}
	if request.ProductionProfile.Mode != piruntime.ModeProduction {
		return nil, nil, errors.New("non-production recovery must supply its own old command context through test-only composition")
	}
	conversationIDs := make(map[string]struct{}, len(request.Transcripts))
	for _, transcript := range request.Transcripts {
		conversationIDs[transcript.Pin.ConversationID] = struct{}{}
	}
	verifier, err := newProductionHistoricalBashHistory(request.CommandHistories, request.ProductionProfile,
		request.D09Template, request.WorkflowID, conversationIDs)
	if err != nil {
		return nil, nil, err
	}
	return verifier, verifier, nil
}

// recoveryCommandVerifiers passes no verifier when the retained history has no
// command journal; a nil interface must never reach the variadic replay.
func recoveryCommandVerifiers(verifier governedRecoveryCommandVerifier) []governedRecoveryCommandVerifier {
	if verifier == nil {
		return nil
	}
	return []governedRecoveryCommandVerifier{verifier}
}

func recoveryDirectoryIdentityDigest(root *os.File) (string, error) {
	if root == nil {
		return "", ErrGovernedRecoveryUnavailable
	}
	info, err := root.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
		return "", ErrGovernedRecoveryUnavailable
	}
	return recoveryDirectoryStatIdentity(stat), nil
}

// recoveryDirectoryStatIdentity is the one directory identity shape. Held
// descriptors and retained paths both use it; link counts are file-only identity.
func recoveryDirectoryStatIdentity(stat *syscall.Stat_t) string {
	return recoveryIdentityDigest(struct {
		Device uint64 `json:"device"`
		Inode  uint64 `json:"inode"`
		Mode   uint32 `json:"mode"`
		UID    uint32 `json:"uid"`
		GID    uint32 `json:"gid"`
	}{uint64(stat.Dev), stat.Ino, stat.Mode, stat.Uid, stat.Gid})
}

func recoveryFileIdentityDigest(path string, directory bool) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", ErrGovernedRecoveryUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
		return "", ErrGovernedRecoveryUnavailable
	}
	if directory {
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			return "", ErrGovernedRecoveryUnavailable
		}
		// A retained directory is matched by the same identity its held descriptor reports.
		return recoveryDirectoryStatIdentity(stat), nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 {
		return "", ErrGovernedRecoveryUnavailable
	}
	return recoveryIdentityDigest(struct {
		Device uint64 `json:"device"`
		Inode  uint64 `json:"inode"`
		Mode   uint32 `json:"mode"`
		UID    uint32 `json:"uid"`
		GID    uint32 `json:"gid"`
		Links  uint64 `json:"links"`
	}{uint64(stat.Dev), stat.Ino, stat.Mode, stat.Uid, stat.Gid, uint64(stat.Nlink)}), nil
}

func recoveryIdentityDigest(identity any) string {
	encoded, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(append([]byte("tbound-recovery-identity/v1\x00"), encoded...))
	return hex.EncodeToString(hash[:])
}

func governedRecoveryRuntimeDigest(request governedRecoveryRequest) (string, error) {
	templateDigest, err := request.D09Template.Digest()
	if err != nil && request.ProductionProfile.Mode == piruntime.ModeProduction {
		return "", err
	}
	storeRoot, err := recoveryDirectoryIdentityDigest(request.StoreRoot)
	if err != nil {
		return "", err
	}
	storeJournal, err := recoveryFileIdentityDigest(request.StoreJournalPath, false)
	if err != nil {
		return "", err
	}
	storePath := request.StoreRoot.Name()
	admissionJournal, err := recoveryFileIdentityDigest(filepath.Join(storePath, "delta-admission.jsonl"), false)
	if err != nil {
		return "", err
	}
	commandPins := make([]string, 0, len(request.CommandHistories))
	for _, command := range request.CommandHistories {
		actualCommandIdentity, identityErr := recoveryFileIdentityDigest(command.Path, false)
		commandTrace, traceErr := command.Journal.Trace()
		if identityErr != nil || traceErr != nil || actualCommandIdentity != command.Pin.IdentityDigest ||
			uint64(len(commandTrace.Records)) != command.Pin.RecordCount || auditHeadHash(commandTrace.Records) != command.Pin.Head {
			return "", errors.Join(workflowrecovery.ErrObservation, identityErr, traceErr)
		}
		commandPins = append(commandPins, command.Pin.IdentityDigest)
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(request)
	if err != nil {
		return "", err
	}
	binding := governedRecoveryRuntimeBinding{
		SchemaVersion: "tbound-governed-recovery-runtime/v2", StoreRootIdentity: storeRoot,
		RootIdentity: identityPins.RootIdentityDigest, StoreIdentity: identityPins.StoreIdentityDigest,
		AdmissionIdentity: identityPins.AdmissionIdentityDigest, AdminIdentity: identityPins.AdminIdentityDigest,
		StoreJournalIdentity: storeJournal, AdmissionJournalIdentity: admissionJournal,
		CommandJournalIdentities: commandPins, StorePolicyDigest: request.StorePolicyDigest,
		MetadataPolicyDigest:  request.MetadataPolicyDigest,
		SystemPromptDigest:    digestBytes([]byte(request.SystemPrompt)),
		DeveloperPromptDigest: digestBytes([]byte(request.DeveloperPrompt)),
		GatePolicyDigest:      request.GatePolicy.Digest(),
		ProviderProfileDigest: bareWorkflowDigest(request.ProviderProfileDigest), ImageDigest: bareWorkflowDigest(request.ImageDigest),
		HostProfileDigest:    bareWorkflowDigest(request.ProductionProfile.Digest),
		RuntimeProfileDigest: bareWorkflowDigest(request.RuntimeProfileDigest), D09TemplateDigest: bareWorkflowDigest(templateDigest),
		D06ProvenanceDigest:      bareWorkflowDigest(request.GitPin.ProvenanceDigest),
		PrivateGitIdentityDigest: bareWorkflowDigest(request.GitPin.RepositoryIdentityDigest),
		Limits:                   request.StoreOptions.Limits, XattrProfileDigest: request.StoreOptions.XattrVisibility.ProfileDigest,
		BashOutputPaths: cloneDurableOutputScopePaths(request.BashOutputPaths),
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append([]byte("tbound-workflow-runtime-binding/v2\x00"), encoded...))
	return hex.EncodeToString(hash[:]), nil
}

func collectGovernedRecoveryIdentityPins(request governedRecoveryRequest) (governedRecoveryIdentityPins, error) {
	if request.Owner == nil {
		return governedRecoveryIdentityPins{}, ErrGovernedRecoveryUnavailable
	}
	rootIdentity := request.Owner.TrustedRootIdentityDigest()
	incarnation := request.Owner.OwnerIncarnationDigest()
	storeIdentity, err := recoveryDirectoryIdentityDigest(request.StoreRoot)
	if err != nil {
		return governedRecoveryIdentityPins{}, err
	}
	storePath := request.StoreRoot.Name()
	admissionIdentity, err := recoveryFileIdentityDigest(filepath.Join(storePath, "delta-admission.jsonl"), false)
	if err != nil {
		return governedRecoveryIdentityPins{}, err
	}
	adminIdentity, err := recoveryDirectoryIdentityDigest(request.AdminRoot)
	if err != nil {
		return governedRecoveryIdentityPins{}, err
	}
	commandIdentities := make([]string, 0, len(request.CommandHistories))
	for _, command := range request.CommandHistories {
		identity, err := recoveryFileIdentityDigest(command.Path, false)
		if err != nil || identity != command.Pin.IdentityDigest {
			return governedRecoveryIdentityPins{}, errors.Join(workflowrecovery.ErrObservation, err)
		}
		commandIdentities = append(commandIdentities, identity)
	}
	commandIdentity := ""
	switch len(commandIdentities) {
	case 0:
		commandIdentity = recoveryIdentityDigest([]string{})
	case 1:
		commandIdentity = commandIdentities[0]
	default:
		commandIdentity = recoveryIdentityDigest(struct {
			Schema string   `json:"schema"`
			Files  []string `json:"files"`
		}{"tbound-workflow-command-journals/v1", commandIdentities})
	}
	for _, digest := range []string{rootIdentity, incarnation, storeIdentity, admissionIdentity, commandIdentity, adminIdentity} {
		if !validBareWorkflowDigest(digest) {
			return governedRecoveryIdentityPins{}, workflowrecovery.ErrOwnerFence
		}
	}
	return governedRecoveryIdentityPins{
		OwnerIncarnationDigest: incarnation, RootIdentityDigest: rootIdentity,
		StoreIdentityDigest: storeIdentity, AdmissionIdentityDigest: admissionIdentity,
		CommandIdentityDigest: commandIdentity, AdminIdentityDigest: adminIdentity,
	}, nil
}

func bareWorkflowDigest(value string) string {
	return bareSHA256Digest(value)
}

func bareSHA256Digest(value string) string {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || strings.Count(value, prefix) != 1 {
		return ""
	}
	digest := strings.TrimPrefix(value, prefix)
	if !validBareWorkflowDigest(digest) {
		return ""
	}
	return digest
}

func sha256DigestFromBarePin(value string) string {
	if !validBareWorkflowDigest(value) {
		return ""
	}
	return "sha256:" + value
}

func barePolicyDigest(value string) string {
	prefix := gate.ProfileVersion + ":sha256:"
	if !strings.HasPrefix(value, prefix) || strings.Count(value, ":sha256:") != 1 {
		return ""
	}
	digest := strings.TrimPrefix(value, prefix)
	if !validBareWorkflowDigest(digest) {
		return ""
	}
	return digest
}

func bareStoreTreeDigest(value string) string {
	if !strings.HasPrefix(value, delta.TreeDigestProfile) || strings.Count(value, ":sha256:") != 1 {
		return ""
	}
	digest := strings.TrimPrefix(value, delta.TreeDigestProfile)
	if !validBareWorkflowDigest(digest) {
		return ""
	}
	return digest
}

func validBareWorkflowDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (r governedRecoveryRequest) workflowPinsPolicyMatches(pins workflowrecovery.Pins) bool {
	return barePolicyDigest(r.GatePolicy.Digest()) == pins.PolicyDigest &&
		bareWorkflowDigest(r.ProviderProfileDigest) == pins.ProfileDigest &&
		bareWorkflowDigest(r.ImageDigest) == pins.ImageDigest &&
		bareWorkflowDigest(r.GitPin.ProvenanceDigest) == pins.D06ProvenanceDigest &&
		bareWorkflowDigest(r.GitPin.RepositoryIdentityDigest) == pins.PrivateGitIdentityDigest
}
