//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workspace"
)

var (
	ErrProductionQuiescenceUnavailable = errors.New("trusted production live-root quiescence attestor is unavailable")
	ErrProductionLifecycleUnsettled    = errors.New("production worker or command lifecycle is not proven settled")
	ErrProductionCommandEvidence       = errors.New("production Bash operation lacks registered signed D09 settlement evidence")
)

// productionLifecycleEvidence records observations made by this run. These
// fields are populated only from PiSDKWorker.Close, the joined governed loops,
// task admission, turn_end, and the actual command runner receipt map. They are
// necessary but not sufficient for publication: live-root writer quiescence
// still requires a trusted host attestor, which this CLI does not have.
type productionLifecycleEvidence struct {
	mu sync.Mutex

	proofDigest           string
	proofSeq              uint64
	proofRunID            string
	proofRecordHash       string
	proofEventID          string
	workerLaunchAttempted bool
	workerLaunched        bool
	workerPID             int
	workerClosed          bool
	workerCloseErr        error
	settlement            piruntime.Settlement
	taskAdmitted          bool
	turnCompleted         bool
	channelsStarted       bool
	channelsJoined        bool
	channelsErr           error
}

type productionLifecycleSnapshot struct {
	proofDigest           string
	proofSeq              uint64
	proofRunID            string
	proofRecordHash       string
	proofEventID          string
	workerLaunchAttempted bool
	workerLaunched        bool
	workerPID             int
	workerClosed          bool
	workerCloseErr        error
	settlement            piruntime.Settlement
	taskAdmitted          bool
	turnCompleted         bool
	channelsStarted       bool
	channelsJoined        bool
	channelsErr           error
}

func (e *productionLifecycleEvidence) setProof(runID, digest string, sequence uint64, recordHash, eventID string) error {
	if e == nil || runID == "" || !validDigestText(digest) || sequence == 0 || recordHash == "" || eventID == "" {
		return errors.New("production TCB proof journal reference is incomplete")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proofSeq != 0 {
		return errors.New("production TCB proof was already journaled for this run")
	}
	e.proofRunID, e.proofDigest, e.proofSeq = runID, digest, sequence
	e.proofRecordHash, e.proofEventID = recordHash, eventID
	return nil
}

func (e *productionLifecycleEvidence) markTaskAdmitted() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.taskAdmitted = true
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markTurnCompleted() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.turnCompleted = true
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markChannelsJoined(err error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.channelsJoined, e.channelsErr = true, err
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markChannelsStarted() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.channelsStarted = true
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markWorkerClosed(settlement piruntime.Settlement, closeErr error) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.workerClosed, e.settlement, e.workerCloseErr = true, settlement, closeErr
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markWorkerLaunched(pid int) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.workerLaunched, e.workerPID = pid > 0, pid
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) markWorkerLaunchAttempted() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.workerLaunchAttempted = true
	e.mu.Unlock()
}

func (e *productionLifecycleEvidence) snapshot() productionLifecycleSnapshot {
	if e == nil {
		return productionLifecycleSnapshot{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return productionLifecycleSnapshot{
		proofDigest: e.proofDigest, proofSeq: e.proofSeq, proofRunID: e.proofRunID,
		proofRecordHash: e.proofRecordHash, proofEventID: e.proofEventID,
		workerLaunchAttempted: e.workerLaunchAttempted, workerLaunched: e.workerLaunched, workerPID: e.workerPID,
		workerClosed: e.workerClosed, workerCloseErr: e.workerCloseErr, settlement: e.settlement,
		taskAdmitted: e.taskAdmitted, turnCompleted: e.turnCompleted,
		channelsStarted: e.channelsStarted, channelsJoined: e.channelsJoined, channelsErr: e.channelsErr,
	}
}

func productionCleanupSafe(evidence productionLifecycleSnapshot, runner *productionCommandRunner) bool {
	if runner != nil && runner.Pending() != 0 {
		return false
	}
	if !evidence.workerLaunchAttempted {
		return true
	}
	if !evidence.workerLaunched || !evidence.workerClosed || evidence.workerCloseErr != nil || evidence.settlement.State != piruntime.SettlementStopped ||
		!evidence.settlement.ExitObserved || !evidence.settlement.DescendantsSettled ||
		(evidence.channelsStarted && (!evidence.channelsJoined || evidence.channelsErr != nil)) {
		return false
	}
	return true
}

func validateProductionLifecycle(evidence productionLifecycleSnapshot, runID string, runner *productionCommandRunner) error {
	if evidence.proofRunID != runID || !validDigestText(evidence.proofDigest) || evidence.proofSeq == 0 ||
		evidence.proofRecordHash == "" || evidence.proofEventID != "production-tcb-proof:"+runID ||
		!evidence.workerLaunched || evidence.workerPID <= 0 || !evidence.taskAdmitted || !evidence.turnCompleted || !evidence.workerClosed || evidence.workerCloseErr != nil ||
		!evidence.channelsJoined || evidence.channelsErr != nil || evidence.settlement.State != piruntime.SettlementStopped ||
		!evidence.settlement.ExitObserved || !evidence.settlement.DescendantsSettled {
		return ErrProductionLifecycleUnsettled
	}
	if runner == nil || runner.Pending() != 0 {
		return ErrProductionLifecycleUnsettled
	}
	return nil
}

// productionBashEvidenceVerifier is intentionally stricter than a profile
// string. A signed D09 runner must verify the durable OperationEvidence against
// its retained per-command proof/settlement; absent implementations refuse.
type productionBashEvidenceVerifier interface {
	VerifyProductionBashEvidence(context.Context, sessionrepo.OperationEvidence) error
}

func verifyProductionBashEvidence(ctx context.Context, runner sessionrepo.CommandRunner, operation sessionrepo.OperationEvidence) error {
	if ctx == nil || runner == nil || operation.Tool != "bash" || operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile == "" {
		return ErrProductionCommandEvidence
	}
	profile, ok := runner.(sessionrepo.CommandRunnerProfile)
	if !ok || profile.Profile() == "" || operation.CommandRunnerProfile != profile.Profile() {
		return ErrProductionCommandEvidence
	}
	verifier, ok := runner.(productionBashEvidenceVerifier)
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifier.VerifyProductionBashEvidence(ctx, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

type productionLaunchTCBProofEvent struct {
	SchemaVersion       string                `json:"schema_version"`
	RunID               string                `json:"run_id"`
	SessionID           string                `json:"session_id"`
	WorkflowID          string                `json:"workflow_id"`
	ConversationID      string                `json:"conversation_id"`
	SourceGenerationID  string                `json:"source_generation_id"`
	SourceBindingDigest string                `json:"source_binding_digest"`
	ProfileID           string                `json:"profile_id"`
	ProfileDigest       string                `json:"profile_digest"`
	ProofDigest         string                `json:"proof_digest"`
	Proof               piruntime.D09TCBProof `json:"proof"`
}

// journalProductionLaunchProof verifies the launcher's captured proof against
// the exact signed profile and registered template and durably appends it before
// the task/control descriptor is released. A worker observation or a caller
// constructed JSON object cannot reach this function from the launch path: the
// value is obtained from PiSDKWorker.D09LaunchProof, which is filled only from
// the launcher's proof pipe after entrypoint validation.
func journalProductionLaunchProof(journal *audit.Journal, lifecycle *productionLifecycleEvidence,
	runID, sessionID, workflowID, conversationID, generationID, sourceBindingDigest string,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, launcherPID int, proof piruntime.D09TCBProof) error {
	if journal == nil || lifecycle == nil || runID == "" || sessionID == "" || workflowID == "" || conversationID == "" ||
		generationID == "" || !validDigestText(sourceBindingDigest) || profile.Mode != piruntime.ModeProduction ||
		profile.ContainmentProfile != piruntime.D09ContainmentProfile || profile.SettlementProfile != piruntime.D09SettlementProfile ||
		profile.ID == "" || !validDigestText(profile.Digest) || !validDigestText(profile.ContainmentProfileDigest) {
		return piruntime.ErrProductionLauncherUnavailable
	}
	if lifecycle.snapshot().proofSeq != 0 {
		return errors.New("production launch proof is already journaled for this run")
	}
	templateDigest, err := template.Digest()
	if err != nil || templateDigest != profile.ContainmentProfileDigest || template.LandlockABI < 8 ||
		proof.Schema != "tbound-pi-entrypoint/v1" || proof.UID != template.CellUID || proof.GID != template.CellGID ||
		proof.CapEff != "0000000000000000" || !proof.NoNewPrivs || proof.Seccomp != 2 || !proof.CgroupV2 ||
		!proof.LandlockTSYNC || proof.LandlockABI < template.LandlockABI ||
		proof.LandlockRulesetDigest != template.LandlockRulesetDigest ||
		proof.ImageDigest != template.ExpectedImageDigest || proof.TemplateDigest != templateDigest ||
		proof.ContainerName == "" || proof.LeaderPID <= 0 || proof.ContainerLeaderPID != 1 ||
		launcherPID <= 0 || proof.LauncherProcessPID != launcherPID || !proof.ConsumerMountsVerified || !proof.RawSourceFDsClosed ||
		proof.WorkerMountID == 0 || proof.RuntimeMountID == 0 || proof.WorkerDevice == 0 || proof.WorkerInode == 0 ||
		proof.RuntimeDevice == 0 || proof.RuntimeInode == 0 || !strings.HasSuffix(proof.ImageRef, "@"+template.ExpectedImageDigest) {
		return fmt.Errorf("%w: captured launch proof does not match the signed D09 template", piruntime.ErrProductionLauncherUnavailable)
	}
	encodedProof, err := json.Marshal(proof)
	if err != nil {
		return fmt.Errorf("encode captured D09 launch proof: %w", err)
	}
	proofDigest := digestBytes(encodedProof)
	event := productionLaunchTCBProofEvent{
		SchemaVersion: "tbound-production-launch-tcb-proof/v1", RunID: runID,
		SessionID: sessionID, WorkflowID: workflowID, ConversationID: conversationID,
		SourceGenerationID: generationID, SourceBindingDigest: sourceBindingDigest,
		ProfileID: profile.ID, ProfileDigest: profile.Digest, ProofDigest: proofDigest, Proof: proof,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode durable D09 launch proof event: %w", err)
	}
	record, err := journal.Append(audit.Event{Kind: "production_tcb_proof", ID: "production-tcb-proof:" + runID, Data: data})
	if err != nil {
		return fmt.Errorf("durably append D09 launch proof before task admission: %w", err)
	}
	return lifecycle.setProof(runID, proofDigest, record.Sequence, record.Hash, record.Event.ID)
}

func verifyProductionLaunchProofJournal(journal *audit.Journal, snapshot productionLifecycleSnapshot,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, runID, sessionID, workflowID, conversationID,
	generationID, sourceBindingDigest string) error {
	if journal == nil || snapshot.proofRunID != runID || snapshot.proofSeq == 0 || snapshot.proofRecordHash == "" ||
		snapshot.proofEventID != "production-tcb-proof:"+runID {
		return ErrProductionLifecycleUnsettled
	}
	trace, err := journal.Trace()
	if err != nil {
		return errors.Join(ErrProductionLifecycleUnsettled, err)
	}
	var matched *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		if record.Event.Kind == "production_tcb_proof" && record.Event.ID == snapshot.proofEventID {
			if matched != nil {
				return ErrProductionLifecycleUnsettled
			}
			matched = record
		}
	}
	if matched == nil || matched.Sequence != snapshot.proofSeq || matched.Hash != snapshot.proofRecordHash {
		return ErrProductionLifecycleUnsettled
	}
	var event productionLaunchTCBProofEvent
	decoder := json.NewDecoder(bytes.NewReader(matched.Event.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return errors.Join(ErrProductionLifecycleUnsettled, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrProductionLifecycleUnsettled
	}
	encodedProof, err := json.Marshal(event.Proof)
	if err != nil || event.SchemaVersion != "tbound-production-launch-tcb-proof/v1" || event.RunID != runID ||
		event.SessionID != sessionID || event.WorkflowID != workflowID || event.ConversationID != conversationID ||
		event.SourceGenerationID != generationID || event.SourceBindingDigest != sourceBindingDigest ||
		event.ProfileID != profile.ID || event.ProfileDigest != profile.Digest || digestBytes(encodedProof) != event.ProofDigest ||
		event.ProofDigest != snapshot.proofDigest || event.Proof.TemplateDigest != profile.ContainmentProfileDigest ||
		event.Proof.LauncherProcessPID != snapshot.workerPID || event.Proof.UID != template.CellUID || event.Proof.GID != template.CellGID ||
		event.Proof.CapEff != "0000000000000000" || !event.Proof.NoNewPrivs || event.Proof.Seccomp != 2 || !event.Proof.CgroupV2 ||
		!event.Proof.LandlockTSYNC || event.Proof.LandlockABI < template.LandlockABI ||
		event.Proof.LandlockRulesetDigest != template.LandlockRulesetDigest ||
		event.Proof.ImageDigest != template.ExpectedImageDigest || !strings.HasSuffix(event.Proof.ImageRef, "@"+template.ExpectedImageDigest) ||
		event.Proof.ContainerName == "" || event.Proof.LeaderPID <= 0 || event.Proof.ContainerLeaderPID != 1 ||
		!event.Proof.ConsumerMountsVerified || !event.Proof.RawSourceFDsClosed ||
		event.Proof.WorkerMountID == 0 || event.Proof.RuntimeMountID == 0 ||
		event.Proof.WorkerDevice == 0 || event.Proof.WorkerInode == 0 || event.Proof.RuntimeDevice == 0 || event.Proof.RuntimeInode == 0 {
		return errors.Join(ErrProductionLifecycleUnsettled, err)
	}
	return nil
}

// productionTrust is the trusted host workflow.Trust for the production
// session. Every origin/transition decision is delegated to the real
// SessionOperationAuthority; admission, publication prerequisites, quiescence,
// and the terminal lease origin are concrete checks over the bound protected
// session. No callback returns nil success.
type productionTrust struct {
	authority            *SessionOperationAuthority
	storeJournal         *audit.Journal
	baseline             *sessionrepo.Generation
	profile              piruntime.HostProfile
	template             piruntime.D09CellTemplate
	sessionID            string
	workflowID           string
	conversationID       string
	sourceGenerationID   string
	sourceBindingDigest  string
	policyDigest         string
	metadataPolicyDigest string
	lifecycle            *productionLifecycleEvidence
	runner               *productionCommandRunner
	runID                string
	liveRoot             *os.File
	liveRootLease        *managedLiveRootLease
	ownerEpoch           uint64
}

func (t *productionTrust) CheckAdmission(ctx context.Context) error {
	if t == nil || t.authority == nil || ctx == nil {
		return errors.New("production publication trust requires a bound session and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.liveRootLease == nil || t.liveRoot == nil || t.liveRootLease.matchesFinalizer(ctx, t.liveRoot,
		t.runID, t.workflowID, t.ownerEpoch) != nil {
		return ErrManagedLiveRootUnavailable
	}
	return t.authority.requirePreparedSource()
}

func (t *productionTrust) CheckPublicationPrerequisites(ctx context.Context, request workflow.EvidenceRequest) error {
	if t == nil || t.authority == nil || ctx == nil {
		return errors.New("production publication trust requires a bound session and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.authority.requirePreparedSource(); err != nil {
		return err
	}
	if t.lifecycle == nil || t.authority.runner == nil || t.authority.runner != t.runner {
		return ErrProductionLifecycleUnsettled
	}
	if err := validateProductionLifecycle(t.lifecycle.snapshot(), t.runID, t.runner); err != nil {
		return err
	}
	if err := verifyProductionLaunchProofJournal(t.storeJournal, t.lifecycle.snapshot(), t.profile, t.template, t.runID,
		t.sessionID, t.workflowID, t.conversationID, t.sourceGenerationID, t.sourceBindingDigest); err != nil {
		return err
	}
	if request.Bundle.PolicyDigest != t.policyDigest || request.Bundle.MetadataPolicyDigest != t.metadataPolicyDigest {
		return errors.New("publication bundle policy differs from the registered production session")
	}
	for _, operation := range request.Bundle.Operations {
		if operation.Tool != "bash" {
			continue
		}
		transition, err := productionTransitionForOperation(request.Bundle, operation)
		if err != nil {
			return err
		}
		if err := t.authority.VerifyOrigin(request.Bundle, transition); err != nil {
			return fmt.Errorf("revalidate D09 receipt against authenticated provider-call origin before publication: %w", err)
		}
		if err := verifyProductionBashEvidence(ctx, t.runner, operation); err != nil {
			return err
		}
	}
	if t.liveRootLease == nil || t.liveRoot == nil {
		return ErrManagedLiveRootUnavailable
	}
	if err := t.liveRootLease.authorizePublication(ctx, t.liveRoot, request, t.baseline); err != nil {
		return fmt.Errorf("managed live-root one-use publication permit refused: %w", err)
	}
	return nil
}

func productionTransitionForOperation(bundle sessionrepo.EvidenceBundle, operation sessionrepo.OperationEvidence) (delta.Transition, error) {
	var matched delta.Transition
	count := 0
	for _, candidate := range bundle.ApprovedDeltaLedger {
		if candidate.Tool != operation.Tool || candidate.Operation != operation.Operation ||
			candidate.ArgumentDigest != operation.ArgumentDigest || candidate.ViewID != operation.ViewID ||
			candidate.ExecutionContextDigest != operation.ExecutionContextDigest ||
			candidate.InputGeneration != operation.InputGeneration || candidate.InputTreeDigest != operation.InputTreeDigest ||
			candidate.OutputGeneration != operation.OutputGeneration || candidate.OutputTreeDigest != operation.OutputTreeDigest {
			continue
		}
		matched = candidate
		count++
	}
	if count != 1 {
		return delta.Transition{}, fmt.Errorf("production Bash evidence matched %d approved transitions", count)
	}
	return matched, nil
}

func (t *productionTrust) AttestQuiescent(ctx context.Context, live *os.File, baseline, tip *sessionrepo.Generation) error {
	if t == nil || t.authority == nil || ctx == nil || live == nil || baseline == nil || tip == nil {
		return errors.New("production quiescence requires live/baseline/tip handles")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.baseline == nil || baseline.ID() != t.baseline.ID() || baseline.TreeDigest() != t.baseline.TreeDigest() {
		return errors.New("publication baseline differs from the bound production baseline")
	}
	if t.lifecycle == nil || t.authority.runner == nil || t.authority.runner != t.runner {
		return ErrProductionLifecycleUnsettled
	}
	if err := t.authority.requirePreparedSource(); err != nil {
		return err
	}
	if err := verifyProductionLaunchProofJournal(t.storeJournal, t.lifecycle.snapshot(), t.profile, t.template, t.runID,
		t.sessionID, t.workflowID, t.conversationID, t.sourceGenerationID, t.sourceBindingDigest); err != nil {
		return err
	}
	if t.liveRootLease == nil || t.liveRoot != live || t.ownerEpoch == 0 ||
		t.liveRootLease.matchesFinalizer(ctx, live, t.runID, t.workflowID, t.ownerEpoch) != nil {
		return ErrManagedLiveRootUnavailable
	}
	if err := validateProductionLifecycle(t.lifecycle.snapshot(), t.runID, t.runner); err != nil {
		return err
	}
	// The managed-root authority is admitted only by Runtime A's fixed signed
	// writer-domain registration and current service measurement. This run then
	// proves STOPPED Pi, joined loops, zero pending command lease/receipt, exact
	// baseline/tip binding, and an unchanged held root before issuing a one-use
	// permit. Neither directory mode nor the publication flock grants this permit.
	return t.liveRootLease.issuePublicationPermit(ctx, live, baseline, tip)
}

func (t *productionTrust) VerifyTransition(ctx context.Context, decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if t == nil || t.authority == nil || ctx == nil {
		return ErrGovernedSessionEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.liveRootLease == nil || t.liveRootLease.verifyPermitForTransition(ctx, t.liveRoot) != nil {
		return ErrManagedLiveRootUnavailable
	}
	return t.authority.VerifyTransition(decision, binding)
}

func (t *productionTrust) VerifyOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	if t == nil || t.authority == nil || ctx == nil {
		return ErrGovernedSessionEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.liveRootLease == nil || t.liveRootLease.verifyPermitForTransition(ctx, t.liveRoot) != nil {
		return ErrManagedLiveRootUnavailable
	}
	return t.authority.VerifyOrigin(bundle, transition)
}

func (t *productionTrust) ResolveTerminalLeaseOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, terminal delta.Transition) (delta.OperationIdentity, error) {
	if t == nil || t.authority == nil {
		return delta.OperationIdentity{}, ErrGovernedSessionEvidence
	}
	if t.liveRootLease == nil || t.liveRootLease.verifyPermitForTransition(ctx, t.liveRoot) != nil {
		return delta.OperationIdentity{}, ErrManagedLiveRootUnavailable
	}
	return t.authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal)
}

// buildPublicationRuntime constructs the real publication finalizer around the
// durable executor using the protected live/state roots, shared lifecycle
// journal, and the production Trust. It is assigned to the session before Serve.
func (s *productionPiSession) buildPublicationRuntime(durable *DurableExecutor) (*PublicationRuntime, error) {
	if s == nil || durable == nil || s.Authority == nil || s.Prepared == nil || s.Store == nil || s.Generation == nil ||
		s.LiveRoot == nil || s.ManagedLiveRootLease == nil || s.PublicationStateRoot == nil ||
		s.PublicationJournal == nil || s.OwnerEpoch == 0 {
		return nil, errors.New("production publication requires the bound session, protected roots, journal, and owner epoch")
	}
	ledger, err := workflow.NewRootLedger(s.PublicationJournal)
	if err != nil {
		return nil, fmt.Errorf("create shared root ledger: %w", err)
	}
	trust := &productionTrust{
		authority: s.Authority, storeJournal: s.StoreJournal, baseline: s.Generation, profile: s.Profile, template: s.D09Template,
		sessionID: s.SessionID, workflowID: s.WorkflowID, conversationID: s.ConversationID,
		sourceGenerationID: s.Generation.ID(), sourceBindingDigest: s.Prepared.Provenance().BindingDigest,
		policyDigest: s.StorePolicyDigest, metadataPolicyDigest: s.MetadataPolicyDigest,
		lifecycle: s.Lifecycle, runner: s.ProductionCommandRunner, runID: s.RunID,
		liveRoot: s.LiveRoot, liveRootLease: s.ManagedLiveRootLease, ownerEpoch: s.OwnerEpoch,
	}
	openPublication := func(workflowID string, epoch uint64, workspaceOptions workspace.Options) func() (*publication.Repository, error) {
		return func() (*publication.Repository, error) {
			return publication.Acquire(publication.Options{
				WorkflowID: workflowID, OwnerEpoch: epoch,
				StateRoot: s.PublicationStateRoot, Journal: s.PublicationJournal, Workspace: workspaceOptions,
			})
		}
	}
	options := workflow.FinalizerOptions{
		WorkflowID: s.WorkflowID, OwnerEpoch: s.OwnerEpoch, PublicationID: s.PublicationID,
		LiveRoot: s.LiveRoot, Store: s.Store, Baseline: s.Generation, Workspace: s.Workspace,
		Journal: s.PublicationJournal, Ledger: ledger, OpenPublication: openPublication(s.WorkflowID, s.OwnerEpoch, s.Workspace),
		OpenRecovery: func(workflowID string) (workflow.RecoveryRepository, error) {
			if err := s.ManagedLiveRootLease.revalidate(context.Background(), s.LiveRoot); err != nil {
				return nil, fmt.Errorf("revalidate managed root before recovery of workflow %q: %w", workflowID, err)
			}
			recoveryWorkspace, err := managedRecoveryWorkspace(s.PublicationJournal, s.LiveRoot, workflowID)
			if err != nil {
				return nil, err
			}
			return openPublication(workflowID, s.OwnerEpoch, recoveryWorkspace)()
		},
		Trust: trust,
	}
	return NewPublicationRuntime(durable, options)
}
