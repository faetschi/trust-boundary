//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

const productionProviderCompletionCap uint64 = 1024

type activeWorkflowProgressVerifier struct {
	authority        *SessionOperationAuthority
	expectedHostCall *providerbridge.HostCallCompletion
}

type capturedOperationVerifier struct {
	authority *SessionOperationAuthority
	request   sessionrepo.OperationRequest
}

func capturedOperationObservation(stage string) error {
	return fmt.Errorf("%w: captured operation %s", workflowrecovery.ErrObservation, stage)
}

func (v capturedOperationVerifier) VerifyCapturedOperation(ctx context.Context,
	checkpoint workflowrecovery.Checkpoint, binding workflowrecovery.EffectBinding) error {
	// sessionrepo calls this verifier under Store.mu. buildActiveProgressPins
	// previously reconstructed the primary and admission journals and committed
	// both exact heads/counts into this checkpoint. audit.Open holds an exclusive
	// writer flock for each journal, and Store appends use this same mutex, so
	// under the AH private-supervisor-store boundary no supported writer can
	// advance admission evidence between that snapshot and this callback. This
	// does not claim protection from privileged out-of-band file rewriting.
	a := v.authority
	if ctx == nil || a == nil || ctx.Err() != nil || binding.OperationID != v.request.EffectID ||
		binding.OperationID == "" || !validWorkflowDigest(binding.SpecDigest) {
		return capturedOperationObservation("request binding is malformed")
	}
	a.mu.Lock()
	storeJournal, providerJournal := a.storeJournal, a.providerJournal
	workflowID, sessionID, conversationID := a.workflowID, a.sessionID, a.conversationID
	profileID, config, policy := a.providerProfileID, a.providerConfig, a.gatePolicy
	currentPins := cloneWorkflowPins(a.workflowLedgerPins)
	verifiedStorePins := cloneWorkflowPins(a.workflowVerifiedStorePins)
	outputPaths := cloneDurableOutputScopePaths(a.bashOutputPaths)
	a.mu.Unlock()
	pins := checkpoint.Pins
	if storeJournal == nil || providerJournal == nil ||
		workflowID == "" || sessionID == "" || conversationID == "" || profileID == "" ||
		!reflect.DeepEqual(currentPins, pins) || !reflect.DeepEqual(verifiedStorePins, pins) ||
		pins.WorkflowID != workflowID || pins.SessionID != sessionID ||
		pins.ProviderDisposition != workflowrecovery.DispositionAwaitingGateDecision ||
		pins.PendingCallDisposition != workflowrecovery.PendingCallCaptured || len(pins.ProviderTranscripts) == 0 {
		return capturedOperationObservation("checkpoint is not the exact pending gate capture")
	}
	captured := pins.HostCallReservation
	if captured == nil || captured.Disposition != workflowrecovery.HostCallTerminal || captured.CapturedCall == nil ||
		captured.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable ||
		binding.WorkflowID != pins.WorkflowID || binding.ConversationID != captured.CapturedCall.ConversationID ||
		binding.ConversationID != conversationID || binding.HostCallID != captured.CapturedCall.HostCallID ||
		binding.ToolCallID != captured.CapturedCall.ToolCallID || binding.HostCallOutcomeDigest != captured.CapturedCall.OutcomeDigest ||
		binding.RecoveryGeneration != pins.RecoveryGeneration || binding.RecoveryGeneration != captured.CapturedCall.RecoveryGeneration ||
		binding.OwnerEpoch != pins.OwnerEpoch || binding.OwnerEpoch != captured.CapturedCall.OwnerEpoch {
		return capturedOperationObservation("host call, tool call, workflow, or fence does not match the capture")
	}
	if err := a.requirePreparedJournal(); err != nil {
		return err
	}
	proof, err := a.proofForDecisionID(v.request.Decision.ID)
	if err != nil {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	operation, decision, typedDigest, err := a.bindOperation(proof)
	expectedOutputPaths, outputErr := governedOperationOutputScope(proof.proposal, outputPaths)
	if err != nil || outputErr != nil {
		return errors.Join(capturedOperationObservation("ALLOW proof and typed Store operation do not agree"), err, outputErr)
	}
	if binding.SpecDigest != bareSHA256Digest(typedDigest) {
		return capturedOperationObservation("typed operation digest differs from the effect reservation")
	}
	if v.request.Tool != proof.proposal.Tool || v.request.Operation != operation || v.request.Decision != decision ||
		v.request.ArgumentDigest != typedDigest {
		return capturedOperationObservation("typed operation request differs from the exact provider ALLOW")
	}
	if v.request.InputGeneration != pins.StoreTipGenerationID {
		return capturedOperationObservation("typed operation request names another Store generation")
	}
	requestTreeDigest := bareStoreTreeDigest(v.request.InputTreeDigest)
	if requestTreeDigest == "" || requestTreeDigest != pins.StoreTipTreeDigest {
		return capturedOperationObservation("typed operation request names another Store tree")
	}
	if v.request.EffectID == "" {
		return capturedOperationObservation("typed operation request has an empty effect")
	}
	if !sameDurableOutputScopePaths(v.request.OutputScopePaths, expectedOutputPaths) {
		return capturedOperationObservation("typed operation output scope differs from the captured proposal")
	}
	proofTreeDigest := bareStoreTreeDigest(proof.treeDigest)
	if proof.call != binding.ToolCallID || proof.result != nil || proof.decision.Verdict != gate.Allow ||
		proof.decisionSequence == 0 || proof.generation != pins.StoreTipGenerationID ||
		proofTreeDigest == "" || proofTreeDigest != pins.StoreTipTreeDigest {
		return capturedOperationObservation("provider capture is not the exact ALLOW at the pinned Store tip")
	}
	activePin := pins.ProviderTranscripts[len(pins.ProviderTranscripts)-1]
	if activePin.ConversationID != conversationID || activePin.Disposition != workflowrecovery.DispositionAwaitingGateDecision ||
		activePin.JournalCount == 0 || activePin.JournalHead == "" {
		return capturedOperationObservation("active transcript pin does not end at the pending gate decision")
	}
	providerTrace, err := providerJournal.Trace()
	if err != nil || uint64(len(providerTrace.Records)) != activePin.JournalCount+1 ||
		proof.decisionSequence != uint64(len(providerTrace.Records)) ||
		providerTrace.Records[activePin.JournalCount-1].Hash != activePin.JournalHead ||
		providerTrace.Records[len(providerTrace.Records)-1].Event.Kind != "provider_gate_decision" {
		return errors.Join(capturedOperationObservation("provider journal tail is not the one pending gate decision"), err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil || uint64(len(storeTrace.Records)) != pins.StoreJournalCount || auditHeadHash(storeTrace.Records) != pins.StoreJournalHead ||
		workflowrecovery.TranscriptPinsDigest(pins.ProviderTranscripts) != pins.ProviderProjectionDigest {
		return errors.Join(capturedOperationObservation("verified pre-effect Store or provider checkpoint snapshot changed"), err)
	}
	for _, effect := range storeTrace.Effects {
		if effect.ID == binding.OperationID {
			return capturedOperationObservation("Store already contains this operation effect")
		}
	}
	for _, record := range storeTrace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		var authorization governedOperationAdmissionRecord
		if unmarshalGovernedJSON(record.Event.Data, &authorization) != nil ||
			authorization.EffectID == binding.OperationID || authorization.DecisionID == v.request.Decision.ID {
			return capturedOperationObservation("Store already contains this operation authorization")
		}
	}
	providerPins := providerbridge.RecoveryPins{
		Config: config, Model: config.Route.Resolved().Model, Policy: policy,
		CheckpointRecordCount: activePin.JournalCount, CheckpointRecordHead: activePin.JournalHead,
		RequireHostCallReservations: true,
		ImportedPrefixDigest:        importedPrefixReplayPin(config.Predecessor.Digest(), len(providerTrace.Records)),
	}
	verifier := capturedCallProviderReplayVerifier{authority: a, prefixCount: activePin.JournalCount,
		checkpoint: checkpoint, binding: binding, proof: proof, request: v.request,
		typedDigest: typedDigest, decisionID: v.request.Decision.ID}
	projection, err := providerbridge.ReplayTranscript(providerJournal, providerPins, verifier)
	if err != nil || projection.Disposition != providerbridge.DispositionPendingProviderResult ||
		projection.HostCall == nil || projection.HostCall.CallID != captured.Binding.CallID ||
		projection.HostCall.RequestDigest != captured.Binding.RequestDigest ||
		projection.HostCall.TokenUpperBound != captured.Binding.TokenUpperBound ||
		projection.HostCallOutcomeDigest != captured.OutcomeDigest ||
		projection.ProviderJournalRecordCount != uint64(len(providerTrace.Records)) ||
		projection.CapturedCallCount != projection.DecisionCount || projection.DecisionCount != projection.ResultCount+1 {
		return errors.Join(capturedOperationObservation("pending provider projection does not match the exact host reservation and ALLOW"), err)
	}
	return nil
}

type capturedCallProviderReplayVerifier struct {
	authority   *SessionOperationAuthority
	prefixCount uint64
	checkpoint  workflowrecovery.Checkpoint
	binding     workflowrecovery.EffectBinding
	proof       governedCapture
	request     sessionrepo.OperationRequest
	typedDigest string
	decisionID  string
}

func (v capturedCallProviderReplayVerifier) VerifyDecision(binding providerbridge.ProviderDecisionBinding) error {
	if binding.DecisionRecordSequence <= v.prefixCount {
		// This exact prefix was replay-verified and checkpointed by the protected
		// owner before the provider exchange. Current Store operation authorization
		// uses the checkpointed Store tip and the Store's own under-lock request.
		return nil
	}
	a := v.authority
	bindingTreeDigest := bareStoreTreeDigest(binding.InputTreeDigest)
	if a == nil || binding.DecisionRecordSequence != v.proof.decisionSequence ||
		binding.DecisionRecordHash != v.proof.decisionHash || binding.WorkflowID != v.checkpoint.Pins.WorkflowID ||
		binding.ConversationID != v.binding.ConversationID || binding.ProfileID != a.providerProfileID ||
		binding.Model != a.currentProviderRoute().Model || binding.PolicyDigest != a.gatePolicyDigest ||
		binding.ToolCallID != v.binding.ToolCallID || binding.ResponseID != v.proof.response ||
		binding.ProviderSequence != v.proof.sequence || binding.InputGeneration != v.checkpoint.Pins.StoreTipGenerationID ||
		bindingTreeDigest == "" || bindingTreeDigest != v.checkpoint.Pins.StoreTipTreeDigest ||
		binding.GateDecision.Verdict != gate.Allow || !sameCapturedProposal(binding.Proposal, v.proof.proposal) ||
		!reflect.DeepEqual(binding.GateDecision, v.proof.decision) {
		return workflowrecovery.ErrObservation
	}
	operation, decision, typedDigest, err := a.bindOperation(v.proof)
	if err != nil || operation.Kind == "" || binding.GateDecision.Tool != v.request.Tool || decision.ID != v.decisionID ||
		typedDigest != v.typedDigest || bareSHA256Digest(typedDigest) != v.binding.SpecDigest ||
		v.request.Operation != operation || v.request.Decision != decision || v.request.ArgumentDigest != typedDigest ||
		v.request.EffectID != v.binding.OperationID {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	return nil
}

func (v capturedCallProviderReplayVerifier) VerifyResult(binding providerbridge.ProviderResultBinding) error {
	if binding.ResultRecordSequence <= v.prefixCount {
		return nil
	}
	return workflowrecovery.ErrPendingUnknown
}

func (v activeWorkflowProgressVerifier) VerifyProgress(ctx context.Context, previous workflowrecovery.Checkpoint,
	next workflowrecovery.Pins) error {
	if ctx == nil || v.authority == nil {
		return workflowrecovery.ErrObservation
	}
	expected, err := v.authority.buildActiveProgressPins(ctx, v.expectedHostCall)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(previous.Pins, v.authority.currentLedgerPins()) || !reflect.DeepEqual(expected, next) {
		return workflowrecovery.ErrObservation
	}
	return nil
}

func (v capturedCallProviderReplayVerifier) VerifyAdmissionDenial(binding providerbridge.ProviderAdmissionDenialBinding) error {
	if binding.ResultRecordSequence <= v.prefixCount {
		return nil
	}
	return workflowrecovery.ErrPendingUnknown
}

func (a *SessionOperationAuthority) currentLedgerPins() workflowrecovery.Pins {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneWorkflowPins(a.workflowLedgerPins)
}

func (a *SessionOperationAuthority) selectProviderCompletionLimit(ctx context.Context) (uint64, error) {
	if a == nil || ctx == nil {
		return 0, workflowrecovery.ErrMissing
	}
	a.mu.Lock()
	ledger, perCallCap := a.workflowLedger, a.providerTokenUpperBound
	a.mu.Unlock()
	if ledger == nil || perCallCap == 0 {
		return 0, workflowrecovery.ErrMissing
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	checkpoint, _ := ledger.Snapshot()
	if checkpoint.Counters.Tokens >= checkpoint.Limits.MaxTokens {
		return 0, workflowrecovery.ErrExhausted
	}
	remaining := checkpoint.Limits.MaxTokens - checkpoint.Counters.Tokens
	if perCallCap > remaining {
		perCallCap = remaining
	}
	if perCallCap == 0 {
		return 0, workflowrecovery.ErrExhausted
	}
	return perCallCap, nil
}

func (a *SessionOperationAuthority) reserveProviderHostCall(ctx context.Context, request broker.RequestRecord,
	tokenUpperBound uint64) (providerbridge.HostCallBinding, error) {
	if a == nil || ctx == nil || tokenUpperBound == 0 {
		return providerbridge.HostCallBinding{}, workflowrecovery.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return providerbridge.HostCallBinding{}, err
	}
	a.mu.Lock()
	ledger := a.workflowLedger
	a.mu.Unlock()
	if ledger == nil {
		return providerbridge.HostCallBinding{}, workflowrecovery.ErrMissing
	}
	callID, err := newWorkflowCallID()
	if err != nil {
		return providerbridge.HostCallBinding{}, err
	}
	requestDigest := broker.DigestRequestRecord(request)
	if len(requestDigest) != sha256.Size*2 {
		return providerbridge.HostCallBinding{}, workflowrecovery.ErrObservation
	}
	receipt, err := ledger.ReserveHostCall(ctx, workflowrecovery.HostCallBinding{
		CallID: callID, RequestDigest: requestDigest, TokenUpperBound: tokenUpperBound,
	})
	if err != nil {
		return providerbridge.HostCallBinding{}, fmt.Errorf("durably charge exact provider request and completion cap before HTTP dispatch: %w", err)
	}
	reserved, ok := receipt.HostCallBinding()
	if !ok || reserved.CallID != callID || reserved.RequestDigest != requestDigest || reserved.TokenUpperBound != tokenUpperBound {
		return providerbridge.HostCallBinding{}, workflowrecovery.ErrObservation
	}
	checkpoint, _ := ledger.Snapshot()
	a.mu.Lock()
	a.workflowLedgerPins = cloneWorkflowPins(checkpoint.Pins)
	a.mu.Unlock()
	return providerbridge.HostCallBinding{CallID: reserved.CallID, RequestDigest: reserved.RequestDigest,
		TokenUpperBound: reserved.TokenUpperBound}, nil
}

func newWorkflowCallID() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate host provider-call identity: %w", err)
	}
	return "host-provider-call-" + hex.EncodeToString(token[:]), nil
}

func (a *SessionOperationAuthority) completeProviderHostCall(ctx context.Context, completion providerbridge.HostCallCompletion) error {
	if a == nil || ctx == nil || completion.Binding.CallID == "" ||
		!validWorkflowDigest(completion.Binding.RequestDigest) || !validWorkflowDigest(completion.OutcomeDigest) {
		return workflowrecovery.ErrObservation
	}
	if _, err := a.advanceWorkflowProgress(ctx, &completion); err != nil {
		return fmt.Errorf("persist verified provider-exchange outcome before releasing the response: %w", err)
	}
	return nil
}

func (a *SessionOperationAuthority) advanceProviderProgress(ctx context.Context) error {
	if _, err := a.advanceWorkflowProgress(ctx, nil); err != nil {
		return fmt.Errorf("persist verified provider/Store result before release: %w", err)
	}
	return nil
}

func (a *SessionOperationAuthority) advanceWorkflowProgress(ctx context.Context,
	expectedHostCall *providerbridge.HostCallCompletion) (workflowrecovery.Receipt, error) {
	if a == nil || ctx == nil {
		return workflowrecovery.Receipt{}, workflowrecovery.ErrObservation
	}
	a.mu.Lock()
	ledger := a.workflowLedger
	a.mu.Unlock()
	if ledger == nil {
		return workflowrecovery.Receipt{}, workflowrecovery.ErrMissing
	}
	next, err := a.buildActiveProgressPins(ctx, expectedHostCall)
	if err != nil {
		return workflowrecovery.Receipt{}, err
	}
	receipt, err := ledger.AdvanceCheckpoint(ctx, activeWorkflowProgressVerifier{authority: a,
		expectedHostCall: cloneHostCallCompletion(expectedHostCall)}, next)
	if err != nil {
		return workflowrecovery.Receipt{}, err
	}
	checkpoint, _ := ledger.Snapshot()
	a.mu.Lock()
	a.workflowLedgerPins = cloneWorkflowPins(checkpoint.Pins)
	// buildActiveProgressPins replayed actual Store/admission evidence before
	// this owner checkpoint advanced. Cache that exact snapshot for the next
	// Store authorization callback, which executes under Store.mu.
	a.workflowVerifiedStorePins = cloneWorkflowPins(checkpoint.Pins)
	a.mu.Unlock()
	return receipt, nil
}

func cloneHostCallCompletion(completion *providerbridge.HostCallCompletion) *providerbridge.HostCallCompletion {
	if completion == nil {
		return nil
	}
	copy := *completion
	return &copy
}

func (a *SessionOperationAuthority) buildActiveProgressPins(ctx context.Context,
	expectedHostCall *providerbridge.HostCallCompletion) (workflowrecovery.Pins, error) {
	if a == nil || ctx == nil {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	if err := ctx.Err(); err != nil {
		return workflowrecovery.Pins{}, err
	}
	a.mu.Lock()
	ledger, currentPins := a.workflowLedger, cloneWorkflowPins(a.workflowLedgerPins)
	store, baseline := a.store, a.baseline
	storeJournal, providerJournal := a.storeJournal, a.providerJournal
	workflowID, sessionID, conversationID, profileID := a.workflowID, a.sessionID, a.conversationID, a.providerProfileID
	storePolicy, metadataPolicy := a.storePolicyDigest, a.metadataPolicyDigest
	policy, config := a.gatePolicy, a.providerConfig
	outputPaths := cloneDurableOutputScopePaths(a.bashOutputPaths)
	priorAuthorizationSequence := a.workflowPriorAuthorizationSequence
	a.mu.Unlock()
	if ledger == nil || store == nil || baseline == nil || storeJournal == nil || providerJournal == nil ||
		workflowID == "" || sessionID == "" || conversationID == "" || profileID == "" {
		return workflowrecovery.Pins{}, workflowrecovery.ErrMissing
	}
	checkpoint, _ := ledger.Snapshot()
	if !reflect.DeepEqual(checkpoint.Pins, currentPins) || checkpoint.Pins.WorkflowID != workflowID ||
		checkpoint.Pins.SessionID != sessionID || len(checkpoint.Pins.ProviderTranscripts) == 0 {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	activeIndex := len(checkpoint.Pins.ProviderTranscripts) - 1
	activePin := checkpoint.Pins.ProviderTranscripts[activeIndex]
	if activePin.ConversationID != conversationID || activePin.Ordinal != uint64(activeIndex+1) ||
		config.WorkflowID != workflowID || config.ConversationID != conversationID || config.ProfileID != profileID ||
		config.InitialGenerationID == "" || config.InitialTreeDigest == "" || policy.Digest() == "" {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	storeBundle, err := store.EvidenceBundle()
	if err != nil {
		return workflowrecovery.Pins{}, err
	}
	if storeBundle.Baseline.ID != baseline.ID() || storeBundle.Baseline.TreeDigest != baseline.TreeDigest() ||
		storeBundle.PolicyDigest != storePolicy || storeBundle.MetadataPolicyDigest != metadataPolicy {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	profile := broker.Profile{
		ID: profileID, Model: config.Route.Resolved().Model, Route: config.Route,
		Messages:     providerbridge.InitialPromptMessages(config.Route.Resolved(), config.SystemPrompt, config.DeveloperPrompt),
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
		ResponseIDIssuer: governedResponseIssuer, Generation: config.InitialGenerationID,
	}
	providerTrace, err := providerJournal.Trace()
	if err != nil {
		return workflowrecovery.Pins{}, errors.Join(workflowrecovery.ErrObservation, err)
	}
	providerPins := providerbridge.RecoveryPins{
		Config: config, Model: config.Route.Resolved().Model, Policy: policy,
		CheckpointRecordCount: activePin.JournalCount, CheckpointRecordHead: activePin.JournalHead,
		RequireHostCallReservations: true,
		ImportedPrefixDigest:        importedPrefixReplayPin(config.Predecessor.Digest(), len(providerTrace.Records)),
	}
	replayPins := GovernedReplayPins{
		Provider: providerPins, Profile: profile, Baseline: storeBundle.Baseline,
		StorePolicyDigest: storePolicy, MetadataPolicyDigest: metadataPolicy,
		BashOutputPaths: outputPaths,
	}
	priorStoreTrace, err := storeJournal.Trace()
	if err != nil || uint64(len(priorStoreTrace.Records)) < checkpoint.Pins.StoreJournalCount ||
		checkpoint.Pins.StoreJournalCount == 0 && checkpoint.Pins.StoreJournalHead != "" ||
		checkpoint.Pins.StoreJournalCount > 0 && priorStoreTrace.Records[checkpoint.Pins.StoreJournalCount-1].Hash != checkpoint.Pins.StoreJournalHead {
		return workflowrecovery.Pins{}, errors.Join(workflowrecovery.ErrObservation, err)
	}
	// The worker's launch proof is journaled in the Store journal after the workflow checkpoint was
	// taken and before the first provider exchange. It is the only record that may follow the
	// checkpointed prefix here; it changes no Store state, and this checkpoint commits the extended
	// count and head. Anything else after the prefix is unaccounted Store evidence.
	storeTail := priorStoreTrace.Records[checkpoint.Pins.StoreJournalCount:]
	if err := verifyLaunchProofTail(storeTail, workflowID, sessionID, conversationID); err != nil {
		return workflowrecovery.Pins{}, errors.Join(workflowrecovery.ErrObservation, err)
	}
	storeCount, storeHead := uint64(len(priorStoreTrace.Records)), auditHeadHash(priorStoreTrace.Records)
	projection, verifier, err := replayGovernedTranscript(providerJournal, storeJournal, store, replayPins,
		priorAuthorizationSequence, true)
	if err != nil {
		return workflowrecovery.Pins{}, errors.Join(workflowrecovery.ErrObservation, err)
	}
	if projection.StoreAuditRecordCount != storeCount ||
		projection.StoreAuditHead != storeHead ||
		projection.AdmissionRecordCount != checkpoint.Pins.AdmissionJournalCount ||
		projection.AdmissionJournalHead != checkpoint.Pins.AdmissionJournalHead ||
		projection.StoreHeadGeneration != checkpoint.Pins.StoreTipGenerationID ||
		bareStoreTreeDigest(projection.StoreHeadTreeDigest) != checkpoint.Pins.StoreTipTreeDigest {
		return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
	}
	commands := activeWorkflowCommandVerifier{authority: a, ctx: ctx}
	if err := commands.VerifyGovernedVerifier(verifier); err != nil {
		return workflowrecovery.Pins{}, err
	}
	if err := ctx.Err(); err != nil {
		return workflowrecovery.Pins{}, err
	}
	if expectedHostCall != nil {
		call := projection.Provider.HostCall
		if call == nil || call.CallID != expectedHostCall.Binding.CallID || call.RequestDigest != expectedHostCall.Binding.RequestDigest ||
			call.TokenUpperBound != expectedHostCall.Binding.TokenUpperBound ||
			projection.Provider.HostCallOutcomeDigest != expectedHostCall.OutcomeDigest {
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
	}
	next := cloneWorkflowPins(checkpoint.Pins)
	next.StoreJournalCount, next.StoreJournalHead = storeCount, storeHead
	active := &next.ProviderTranscripts[activeIndex]
	active.JournalCount = projection.Provider.ProviderJournalRecordCount
	active.JournalHead = projection.Provider.ProviderJournalHead
	active.ProjectionCount = projection.Provider.EventCount
	active.ProjectionDigest = bareWorkflowDigest(projection.Provider.ProjectionDigest)
	active.ProjectionHead = projection.Provider.ProviderJournalHead
	active.Disposition = workflowDispositionFromReplay(projection.Provider.Disposition)
	next.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(next.ProviderTranscripts)
	next.ProviderProjectionCount = workflowTranscriptProjectionCount(next.ProviderTranscripts)
	next.ProviderProjectionHead = active.ProjectionHead
	next.ProviderDisposition = active.Disposition
	if projection.Provider.PendingCall == nil && projection.Provider.DecisionCount == projection.Provider.ResultCount &&
		projection.Provider.CapturedCallCount > 0 &&
		(next.PendingCallDisposition == workflowrecovery.PendingCallNone ||
			next.PendingCallDisposition == workflowrecovery.PendingCallInFlight ||
			next.PendingCallDisposition == workflowrecovery.PendingCallCaptured) {
		next.PendingCallDisposition = workflowrecovery.PendingCallTerminal
	}
	if projection.Provider.Disposition == providerbridge.DispositionAwaitingGateDecision &&
		projection.Provider.PendingCall != nil {
		if next.PendingCallDisposition == workflowrecovery.PendingCallUnknown {
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
		next.PendingCallDisposition = workflowrecovery.PendingCallCaptured
	}
	if expectedHostCall != nil {
		reservation := next.HostCallReservation
		if reservation == nil || reservation.Disposition != workflowrecovery.HostCallReserved ||
			reservation.Binding.CallID != expectedHostCall.Binding.CallID ||
			reservation.Binding.RequestDigest != expectedHostCall.Binding.RequestDigest ||
			reservation.Binding.TokenUpperBound != expectedHostCall.Binding.TokenUpperBound {
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
		reservation.Disposition = workflowrecovery.HostCallTerminal
		reservation.OutcomeDigest = expectedHostCall.OutcomeDigest
		// The settled attempt is a retryable failure exactly when the replayed journal closed it that way.
		reservation.RetryableFailure = projection.Provider.HostCallRetryableFailure
		if projection.Provider.Disposition == providerbridge.DispositionAwaitingGateDecision &&
			projection.Provider.PendingCall != nil {
			if projection.Provider.HostCall == nil ||
				projection.Provider.HostCall.CallID != expectedHostCall.Binding.CallID ||
				projection.Provider.HostCallOutcomeDigest != expectedHostCall.OutcomeDigest ||
				projection.Provider.PendingCall.ToolCallID == "" {
				return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
			}
			reservation.CapturedCall = &workflowrecovery.CapturedCallPin{
				WorkflowID: workflowID, ConversationID: conversationID,
				HostCallID:         expectedHostCall.Binding.CallID,
				ToolCallID:         projection.Provider.PendingCall.ToolCallID,
				OutcomeDigest:      expectedHostCall.OutcomeDigest,
				RecoveryGeneration: checkpoint.Pins.RecoveryGeneration,
				OwnerEpoch:         checkpoint.Pins.OwnerEpoch,
				Disposition:        workflowrecovery.CapturedCallAvailable,
			}
		}
	}
	if expectedHostCall == nil && projection.Provider.PendingCall == nil && next.HostCallReservation != nil &&
		next.HostCallReservation.CapturedCall != nil {
		captured := next.HostCallReservation.CapturedCall
		switch captured.Disposition {
		case workflowrecovery.CapturedCallConsumed:
			if next.EffectReservation == nil || next.EffectReservation.Disposition != workflowrecovery.EffectReservationTerminal ||
				next.EffectReservation.Binding.ToolCallID != captured.ToolCallID {
				return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
			}
			next.HostCallReservation.CapturedCall = nil
		case workflowrecovery.CapturedCallAvailable:
			// A released DENY leaves the conversation awaiting the model's answer to the DENY result.
			if projection.Provider.Disposition != providerbridge.DispositionAwaitingHostPrompt &&
				projection.Provider.Disposition != providerbridge.DispositionAwaitingProviderExchange ||
				projection.Provider.DecisionCount != projection.Provider.ResultCount {
				return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
			}
			retirementDigest, err := activeDeniedCaptureDigest(providerJournal, captured.ToolCallID)
			if err != nil {
				return workflowrecovery.Pins{}, err
			}
			captured.Disposition = workflowrecovery.CapturedCallRetired
			captured.RetirementDigest = retirementDigest
			next.PendingCallDisposition = workflowrecovery.PendingCallRetired
		default:
			return workflowrecovery.Pins{}, workflowrecovery.ErrObservation
		}
	}
	return next, nil
}

func activeDeniedCaptureDigest(providerJournal *audit.Journal, toolCallID string) (string, error) {
	if providerJournal == nil || toolCallID == "" {
		return "", workflowrecovery.ErrObservation
	}
	trace, err := providerJournal.Trace()
	if err != nil {
		return "", err
	}
	var decisionRecord, resultRecord *audit.Record
	for index := range trace.Records {
		record := &trace.Records[index]
		switch record.Event.Kind {
		case "provider_gate_decision":
			var decision gate.Decision
			if unmarshalGovernedJSON(record.Event.Data, &decision) != nil || decision.ToolCallID != toolCallID {
				continue
			}
			if decisionRecord != nil {
				return "", workflowrecovery.ErrObservation
			}
			copy := *record
			decisionRecord = &copy
		case "provider_tool_result":
			var result providerResultEvidence
			if unmarshalGovernedJSON(record.Event.Data, &result) != nil || result.ToolCallID != toolCallID {
				continue
			}
			if resultRecord != nil {
				return "", workflowrecovery.ErrObservation
			}
			copy := *record
			resultRecord = &copy
		}
	}
	if decisionRecord == nil || resultRecord == nil || decisionRecord.Sequence >= resultRecord.Sequence {
		return "", workflowrecovery.ErrObservation
	}
	var decision gate.Decision
	var result providerResultEvidence
	if unmarshalGovernedJSON(decisionRecord.Event.Data, &decision) != nil || decision.Verdict != gate.Deny ||
		unmarshalGovernedJSON(resultRecord.Event.Data, &result) != nil || result.ToolCallID != toolCallID ||
		result.EffectID != "" || result.TransitionID != "" {
		return "", workflowrecovery.ErrObservation
	}
	protocolResult, decodeErr := protocol.DecodeResult(result.Result)
	canonicalResult, marshalErr := protocol.MarshalResult(protocolResult)
	if decodeErr != nil || marshalErr != nil || !bytes.Equal(canonicalResult, result.Result) ||
		protocolResult.ToolCallID != toolCallID || protocolResult.Verdict != string(gate.Deny) ||
		protocolResult.PolicyDigest != decision.PolicyDigest || protocolResult.ReasonCode != decision.ReasonCode ||
		protocolResult.Tool != decision.Tool || protocolResult.Sequence != decision.Sequence || len(protocolResult.Output) != 0 {
		return "", workflowrecovery.ErrObservation
	}
	payload, err := json.Marshal(struct {
		ToolCallID string       `json:"tool_call_id"`
		Decision   audit.Record `json:"decision"`
		Result     audit.Record `json:"result"`
	}{toolCallID, *decisionRecord, *resultRecord})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("tbound-denied-provider-call-retirement/v1\x00"), payload...))
	return hex.EncodeToString(sum[:]), nil
}

func workflowDispositionFromReplay(disposition providerbridge.RecoveryDisposition) string {
	switch disposition {
	case providerbridge.DispositionAwaitingHostPrompt:
		return workflowrecovery.DispositionAwaitingHostPrompt
	case providerbridge.DispositionAwaitingProviderExchange:
		return workflowrecovery.DispositionAwaitingProviderExchange
	case providerbridge.DispositionAwaitingGateDecision:
		return workflowrecovery.DispositionAwaitingGateDecision
	case providerbridge.DispositionPendingProviderResult:
		return workflowrecovery.DispositionPendingProviderResult
	case providerbridge.DispositionRetired, providerbridge.DispositionRetiredUnexecuted:
		return workflowrecovery.DispositionRetired
	default:
		return workflowrecovery.DispositionUnknown
	}
}

type activeWorkflowCommandVerifier struct {
	authority *SessionOperationAuthority
	ctx       context.Context
}

func (v activeWorkflowCommandVerifier) VerifyGovernedVerifier(replay *governedStoreReplayVerifier) error {
	if v.authority == nil || v.ctx == nil || replay == nil {
		return workflowrecovery.ErrObservation
	}
	a := v.authority
	a.mu.Lock()
	runner := a.runner
	conversationID := a.conversationID
	evidenced := a.requireEvidencedBashContainment
	proofs := make(map[delta.OperationIdentity]governedOperationProof, len(a.operationProofs))
	for operation, proof := range a.operationProofs {
		proofs[operation] = proof
	}
	a.mu.Unlock()
	for _, decision := range replay.decisions {
		// A refused Bash candidate has no transition for D09 evidence to bind to: nothing was promoted.
		if decision == nil || decision.binding.ConversationID != conversationID || decision.entry.Tool != "bash" || decision.denied {
			continue
		}
		proof, ok := proofs[decision.entry.Operation]
		if !ok || proof.request.EffectID != decision.entry.EffectID || runner == nil {
			return workflowrecovery.ErrObservation
		}
		transition, ok := transitionForOperationEvidence(replay.bundle, decision.entry)
		if !ok {
			return workflowrecovery.ErrObservation
		}
		if evidenced {
			verifier, ok := runner.(governedBashEvidenceOriginVerifier)
			if !ok {
				return workflowrecovery.ErrObservation
			}
			proposalID, err := durableTrustedIdentity("proposal", proof.capture.proposal, proof.capture.decision)
			if err != nil {
				return err
			}
			providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
				CallIssuer: governedCallIssuer, CallID: proof.capture.proposal.ToolCallID}
			if err := verifier.VerifyProductionBashEvidenceWithOrigin(v.ctx, replay.bundle, providerCall, transition, decision.entry); err != nil {
				return errors.Join(ErrGovernedSessionEvidence, err)
			}
		} else if verifier, ok := runner.(governedBashEvidenceVerifier); ok {
			if err := verifier.VerifyProductionBashEvidence(v.ctx, decision.entry); err != nil {
				return errors.Join(ErrGovernedSessionEvidence, err)
			}
		}
	}
	return nil
}

func transitionForOperationEvidence(bundle sessionrepo.EvidenceBundle, operation sessionrepo.OperationEvidence) (delta.Transition, bool) {
	var match *delta.Transition
	for index := range bundle.ApprovedDeltaLedger {
		candidate := &bundle.ApprovedDeltaLedger[index]
		if candidate.Operation != operation.Operation || candidate.Tool != operation.Tool ||
			candidate.InputGeneration != operation.InputGeneration || candidate.InputTreeDigest != operation.InputTreeDigest ||
			candidate.OutputGeneration != operation.OutputGeneration || candidate.OutputTreeDigest != operation.OutputTreeDigest ||
			candidate.Decision.ID != operation.DecisionID {
			continue
		}
		if match != nil {
			return delta.Transition{}, false
		}
		copy := *candidate
		match = &copy
	}
	if match == nil {
		return delta.Transition{}, false
	}
	return *match, true
}

func (a *SessionOperationAuthority) SettleDurableOperation(ctx context.Context, proposal protocol.Proposal,
	decision gate.Decision) error {
	if a == nil || ctx == nil {
		return workflowrecovery.ErrObservation
	}
	a.mu.Lock()
	ledger := a.workflowLedger
	var operationProof *governedOperationProof
	for _, proof := range a.operationProofs {
		if reflect.DeepEqual(proof.capture.decision, decision) && proof.capture.proposal.ToolCallID == proposal.ToolCallID &&
			proof.request.Tool == proposal.Tool {
			copy := proof
			if operationProof != nil {
				a.mu.Unlock()
				return workflowrecovery.ErrObservation
			}
			operationProof = &copy
		}
	}
	a.mu.Unlock()
	if ledger == nil {
		return nil
	}
	if operationProof == nil || !operationProof.effectReserved {
		return workflowrecovery.ErrObservation
	}
	binding, ok := operationProof.effectReceipt.EffectBinding()
	if !ok || binding.OperationID != operationProof.request.EffectID ||
		binding.SpecDigest != bareWorkflowDigest(operationProof.request.ArgumentDigest) {
		return workflowrecovery.ErrObservation
	}
	checkpoint, _ := ledger.Snapshot()
	nextPins, outcomeDigest, err := a.terminalEffectPins(ctx, checkpoint, *operationProof, binding)
	if err != nil {
		return fmt.Errorf("derive terminal Store effect evidence pins: %w", err)
	}
	if nextPins.EffectReservation == nil || nextPins.EffectReservation.OutcomeDigest != outcomeDigest {
		return fmt.Errorf("derived terminal Store effect pins do not carry their outcome digest: %w", workflowrecovery.ErrObservation)
	}
	_, err = ledger.RecordTerminalEffectOutcome(ctx, operationProof.effectReceipt,
		activeTerminalEffectVerifier{authority: a, operation: *operationProof}, nextPins)
	if err != nil {
		return fmt.Errorf("owner rejected terminal Store effect evidence: %w", err)
	}
	checkpoint, _ = ledger.Snapshot()
	a.mu.Lock()
	a.workflowLedgerPins = cloneWorkflowPins(checkpoint.Pins)
	a.mu.Unlock()
	return nil
}

type activeTerminalEffectVerifier struct {
	authority *SessionOperationAuthority
	operation governedOperationProof
}

func (v activeTerminalEffectVerifier) VerifyTerminalEffect(ctx context.Context, previous workflowrecovery.Checkpoint,
	binding workflowrecovery.EffectBinding, next workflowrecovery.Pins) error {
	if v.authority == nil {
		return workflowrecovery.ErrObservation
	}
	expected, _, err := v.authority.terminalEffectPins(ctx, previous, v.operation, binding)
	if err != nil {
		return fmt.Errorf("revalidate terminal Store effect evidence: %w", err)
	}
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("terminal Store effect evidence changed during owner settlement: %w", workflowrecovery.ErrObservation)
	}
	return nil
}

type terminalEffectDigestEvidence struct {
	WorkflowID     string                         `json:"workflow_id"`
	SessionID      string                         `json:"session_id"`
	ConversationID string                         `json:"conversation_id"`
	Binding        workflowrecovery.EffectBinding `json:"binding"`
	Authorization  audit.Record                   `json:"authorization"`
	Effect         audit.EffectTrace              `json:"effect"`
	Operation      sessionrepo.OperationEvidence  `json:"operation"`
	Admission      *audit.Record                  `json:"admission,omitempty"`
	StoreHead      string                         `json:"store_head"`
	StoreCount     uint64                         `json:"store_count"`
	AdmissionHead  string                         `json:"admission_head"`
	AdmissionCount uint64                         `json:"admission_count"`
	StoreTip       string                         `json:"store_tip"`
	StoreTree      string                         `json:"store_tree"`
}

func (a *SessionOperationAuthority) terminalEffectPins(ctx context.Context, previous workflowrecovery.Checkpoint,
	proof governedOperationProof, binding workflowrecovery.EffectBinding) (workflowrecovery.Pins, string, error) {
	stage := "authorization binding"
	fail := func(err error) (workflowrecovery.Pins, string, error) {
		return workflowrecovery.Pins{}, "", fmt.Errorf("terminal effect evidence %s: %w", stage, err)
	}
	if a == nil || ctx == nil || !proof.effectReserved || proof.request.EffectID != binding.OperationID ||
		bareWorkflowDigest(proof.request.ArgumentDigest) != binding.SpecDigest ||
		proof.record.Sequence == 0 || proof.record.Event.Kind != governedOperationAuthorizationKind ||
		proof.record.Event.ID != "governed-operation:"+binding.OperationID {
		return fail(workflowrecovery.ErrObservation)
	}
	a.mu.Lock()
	ledger, currentPins := a.workflowLedger, cloneWorkflowPins(a.workflowLedgerPins)
	store, storeJournal := a.store, a.storeJournal
	providerJournal := a.providerJournal
	workflowID, sessionID, conversationID, profileID := a.workflowID, a.sessionID, a.conversationID, a.providerProfileID
	storePolicy, metadataPolicy := a.storePolicyDigest, a.metadataPolicyDigest
	outputPaths := cloneDurableOutputScopePaths(a.bashOutputPaths)
	evidencedBash := a.requireEvidencedBashContainment
	runner := a.runner
	a.mu.Unlock()
	stage = "current owner reservation"
	if ledger == nil || store == nil || storeJournal == nil || providerJournal == nil ||
		!reflect.DeepEqual(previous.Pins, currentPins) || previous.Pins.WorkflowID != workflowID ||
		previous.Pins.SessionID != sessionID || previous.Pins.EffectReservation == nil ||
		previous.Pins.EffectReservation.Disposition != workflowrecovery.EffectReservationReserved ||
		previous.Pins.EffectReservation.Binding != binding {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "captured operation binding"
	operation, expectedDecision, typedDigest, err := a.bindOperation(proof.capture)
	if err != nil || operation != proof.request.Operation || expectedDecision != proof.request.Decision || typedDigest != proof.request.ArgumentDigest {
		return fail(errors.Join(workflowrecovery.ErrObservation, err))
	}
	stage = "provider capture projection"
	providerTrace, err := providerJournal.Trace()
	if err != nil {
		return fail(err)
	}
	currentCapture, err := a.resolveFromTrace(providerTrace, proof.request.Decision.ID)
	if err != nil || !reflect.DeepEqual(currentCapture, proof.capture) {
		return fail(errors.Join(workflowrecovery.ErrObservation, err))
	}
	stage = "authorization admission record"
	var admission governedOperationAdmissionRecord
	if unmarshalGovernedJSON(proof.record.Event.Data, &admission) != nil ||
		admission.SchemaVersion != "tbound-governed-operation-admission/v2" || admission.WorkflowID != workflowID ||
		admission.ConversationID != conversationID || admission.ProviderProfileID != profileID ||
		admission.EffectID != binding.OperationID || admission.DecisionID != proof.request.Decision.ID ||
		admission.Operation != proof.request.Operation || admission.Tool != proof.request.Tool ||
		!sameDurableOutputScopePaths(admission.OutputScopePaths, proof.request.OutputScopePaths) ||
		admission.TypedArgumentDigest != typedDigest || admission.InputGeneration != proof.request.InputGeneration ||
		admission.InputTreeDigest != proof.request.InputTreeDigest || admission.GatePolicyDigest != a.gatePolicyDigest ||
		admission.ProviderDecisionSequence != proof.capture.decisionSequence ||
		admission.ProviderDecisionRecordHash != proof.capture.decisionHash ||
		!reflect.DeepEqual(admission.GateDecision, proof.capture.decision) {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "authorized output scope"
	expectedOutputScopePaths, scopeErr := governedOperationOutputScope(proof.capture.proposal, outputPaths)
	if scopeErr != nil || !sameDurableOutputScopePaths(proof.request.OutputScopePaths, expectedOutputScopePaths) {
		return fail(errors.Join(workflowrecovery.ErrObservation, scopeErr))
	}
	stage = "Store evidence bundle"
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return fail(err)
	}
	stage = "Store audit journal"
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		return fail(err)
	}
	encodedStoreTrace, err := marshalGovernedAuditRecords(storeTrace.Records)
	if err != nil || !bytes.Equal(encodedStoreTrace, bundle.AuditJournal) ||
		uint64(len(storeTrace.Records)) != bundle.AuditRecordCount || auditHeadHash(storeTrace.Records) != bundle.AuditHeadHash ||
		uint64(len(storeTrace.Records)) <= previous.Pins.StoreJournalCount ||
		previous.Pins.StoreJournalCount > 0 && storeTrace.Records[previous.Pins.StoreJournalCount-1].Hash != previous.Pins.StoreJournalHead {
		return fail(errors.Join(workflowrecovery.ErrObservation, err))
	}
	stage = "admission journal prefix"
	admissionTrace, err := audit.Verify(bytes.NewReader(bundle.AdmissionJournal))
	if err != nil || uint64(len(admissionTrace.Records)) != bundle.AdmissionRecordCount ||
		auditHeadHash(admissionTrace.Records) != bundle.AdmissionHeadHash ||
		previous.Pins.AdmissionJournalCount > uint64(len(admissionTrace.Records)) ||
		previous.Pins.AdmissionJournalCount > 0 && admissionTrace.Records[previous.Pins.AdmissionJournalCount-1].Hash != previous.Pins.AdmissionJournalHead {
		return fail(errors.Join(workflowrecovery.ErrObservation, err))
	}
	stage = "Store baseline and policy pins"
	if bundle.PolicyDigest != storePolicy || bundle.MetadataPolicyDigest != metadataPolicy ||
		len(bundle.Generations) == 0 || bundle.Baseline.ID != previous.Pins.StoreBaselineGenerationID ||
		bareStoreTreeDigest(bundle.Baseline.TreeDigest) != previous.Pins.StoreBaselineTreeDigest {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "Store evidence reconstruction"
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined || disposition.Unknowns != 0 || disposition.Successes != disposition.Attempts {
		return fail(errors.Join(workflowrecovery.ErrObservation, err))
	}
	stage = "unique Store operation evidence"
	var operationEvidence *sessionrepo.OperationEvidence
	for index := range bundle.Operations {
		candidate := &bundle.Operations[index]
		if candidate.EffectID != binding.OperationID {
			continue
		}
		if operationEvidence != nil {
			return fail(workflowrecovery.ErrObservation)
		}
		copy := *candidate
		operationEvidence = &copy
	}
	var effect *audit.EffectTrace
	for index := range storeTrace.Effects {
		candidate := &storeTrace.Effects[index]
		if candidate.ID != binding.OperationID {
			continue
		}
		if effect != nil {
			return fail(workflowrecovery.ErrObservation)
		}
		copy := *candidate
		effect = &copy
	}
	stage = "terminal Store effect and operation crosslink"
	if effect == nil || effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence == 0 || effect.OutcomeSequence == 0 ||
		int(effect.IntentSequence) > len(storeTrace.Records) || int(effect.OutcomeSequence) > len(storeTrace.Records) ||
		storeTrace.Records[effect.IntentSequence-1].Event.Kind != "effect_intent" ||
		storeTrace.Records[effect.IntentSequence-1].Event.ID != binding.OperationID ||
		!bytes.Equal(storeTrace.Records[effect.IntentSequence-1].Event.Data, effect.Intent) ||
		storeTrace.Records[effect.OutcomeSequence-1].Event.Kind != "effect_outcome" ||
		storeTrace.Records[effect.OutcomeSequence-1].Event.ID != binding.OperationID ||
		storeTrace.Records[effect.OutcomeSequence-1].Event.Outcome != "success" ||
		!bytes.Equal(storeTrace.Records[effect.OutcomeSequence-1].Event.Data, effect.Result) || operationEvidence == nil ||
		operationEvidence.AuditSequence != effect.OutcomeSequence || operationEvidence.Tool != proof.request.Tool ||
		operationEvidence.EffectID != binding.OperationID || operationEvidence.Operation != proof.request.Operation ||
		operationEvidence.DecisionID != proof.request.Decision.ID || operationEvidence.ArgumentDigest != typedDigest ||
		operationEvidence.InputGeneration != proof.request.InputGeneration || operationEvidence.InputTreeDigest != proof.request.InputTreeDigest ||
		(operationEvidence.Outcome != "success" && !deniedAfterEffect(*operationEvidence)) ||
		operationEvidence.ResultDigest != digestBytes(effect.Result) || operationEvidence.ResultDigest == "" {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "tool-specific admission evidence"
	if proof.request.Tool == "read" {
		if operationEvidence.Admission != nil || bundle.AdmissionRecordCount != previous.Pins.AdmissionJournalCount ||
			bundle.AdmissionHeadHash != previous.Pins.AdmissionJournalHead ||
			operationEvidence.OutputGeneration != operationEvidence.InputGeneration ||
			operationEvidence.OutputTreeDigest != operationEvidence.InputTreeDigest {
			return fail(workflowrecovery.ErrObservation)
		}
	} else if operationEvidence.Admission == nil || (!deniedAfterEffect(*operationEvidence) &&
		(operationEvidence.OutputGeneration == "" || operationEvidence.OutputTreeDigest == "")) {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "admission outcome crosslink"
	denied := deniedAfterEffect(*operationEvidence)
	if operationEvidence.Admission != nil {
		ref := *operationEvidence.Admission
		wantOutcome := "allow"
		if denied {
			wantOutcome = "deny"
		}
		if ref.Sequence == 0 || ref.Sequence > uint64(len(admissionTrace.Records)) ||
			admissionTrace.Records[ref.Sequence-1].Hash != ref.RecordHash ||
			ref.IntentDigest != digestBytes(effect.Intent) || ref.Outcome != wantOutcome {
			return fail(workflowrecovery.ErrObservation)
		}
		var admitted governedReplayAdmissionRecord
		if decodeCanonicalGoverned(admissionTrace.Records[ref.Sequence-1].Event.Data, &admitted) != nil ||
			admitted.Request.EffectID != binding.OperationID || admitted.Request.Operation != proof.request.Operation ||
			admitted.Request.Decision != proof.request.Decision || admitted.Request.Tool != proof.request.Tool ||
			admitted.Request.ArgumentDigest != typedDigest || admitted.IntentDigest != ref.IntentDigest || admitted.Outcome != ref.Outcome {
			return fail(workflowrecovery.ErrObservation)
		}
	}
	if proof.request.Tool == "bash" {
		transition, ok := transitionForOperationEvidence(bundle, *operationEvidence)
		// A refused candidate has no transition; nothing was promoted for D09 evidence to bind to.
		if ok == denied || operationEvidence.Operation.Kind != delta.OperationLease || operationEvidence.ExitCode == nil ||
			operationEvidence.CommandRunnerProfile == "" || operationEvidence.CommandContainmentStatus != "not-established" {
			return fail(workflowrecovery.ErrObservation)
		}
		if evidencedBash && !denied {
			verifier, ok := runner.(governedBashEvidenceOriginVerifier)
			if !ok {
				return workflowrecovery.Pins{}, "", workflowrecovery.ErrObservation
			}
			proposalID, err := durableTrustedIdentity("proposal", proof.capture.proposal, proof.capture.decision)
			if err != nil {
				return workflowrecovery.Pins{}, "", err
			}
			providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
				CallIssuer: governedCallIssuer, CallID: proof.capture.proposal.ToolCallID}
			if err := verifier.VerifyProductionBashEvidenceWithOrigin(ctx, bundle, providerCall, transition, *operationEvidence); err != nil {
				return workflowrecovery.Pins{}, "", errors.Join(ErrGovernedSessionEvidence, err)
			}
		}
	}
	var admissionRecord *audit.Record
	if operationEvidence.Admission != nil {
		copy := admissionTrace.Records[operationEvidence.Admission.Sequence-1]
		admissionRecord = &copy
	}
	stage = "terminal Store tip generation"
	latest := bundle.Generations[len(bundle.Generations)-1]
	if latest.ID != operationEvidence.InputGeneration && latest.ID != operationEvidence.OutputGeneration ||
		(latest.ID == operationEvidence.InputGeneration && latest.TreeDigest != operationEvidence.InputTreeDigest) ||
		(latest.ID == operationEvidence.OutputGeneration && latest.TreeDigest != operationEvidence.OutputTreeDigest) {
		return fail(workflowrecovery.ErrObservation)
	}
	stage = "terminal evidence digest"
	evidence := terminalEffectDigestEvidence{
		WorkflowID: workflowID, SessionID: sessionID, ConversationID: conversationID, Binding: binding,
		Authorization: proof.record, Effect: *effect, Operation: *operationEvidence, Admission: admissionRecord,
		StoreHead: bundle.AuditHeadHash, StoreCount: bundle.AuditRecordCount,
		AdmissionHead: bundle.AdmissionHeadHash, AdmissionCount: bundle.AdmissionRecordCount,
		StoreTip: latest.ID, StoreTree: latest.TreeDigest,
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return workflowrecovery.Pins{}, "", err
	}
	h := sha256.Sum256(append([]byte("tbound-workflow-terminal-store-outcome/v1\x00"), encoded...))
	outcomeDigest := hex.EncodeToString(h[:])
	next := cloneWorkflowPins(previous.Pins)
	next.StoreJournalHead = bundle.AuditHeadHash
	next.StoreJournalCount = bundle.AuditRecordCount
	next.AdmissionJournalHead = bundle.AdmissionHeadHash
	next.AdmissionJournalCount = bundle.AdmissionRecordCount
	next.StoreTipGenerationID = latest.ID
	next.StoreTipTreeDigest = bareStoreTreeDigest(latest.TreeDigest)
	reservation := *next.EffectReservation
	reservation.Disposition = workflowrecovery.EffectReservationTerminal
	reservation.OutcomeDigest = outcomeDigest
	next.EffectReservation = &reservation
	return next, outcomeDigest, nil
}

func validWorkflowDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// sameCapturedProposal compares the replayed provider call with the proof read back from the journal. The
// journal nests the arguments through encoding/json, which HTML-escapes <, > and &, so their bytes differ
// from the captured response's while the canonical JSON is the same.
func sameCapturedProposal(replayed, journaled protocol.Proposal) bool {
	left, leftErr := json.Marshal(replayed.Arguments)
	right, rightErr := json.Marshal(journaled.Arguments)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right) &&
		replayed.SchemaVersion == journaled.SchemaVersion && replayed.ToolCallID == journaled.ToolCallID && replayed.Tool == journaled.Tool
}

// verifyLaunchProofTail accepts an empty tail or exactly one durable D09 launch-proof record of
// this workflow, session and conversation (journalProductionLaunchProof). The record is written by
// the host from the launcher's validated proof and carries no Store mutation.
func verifyLaunchProofTail(tail []audit.Record, workflowID, sessionID, conversationID string) error {
	if len(tail) == 0 {
		return nil
	}
	if len(tail) != 1 || tail[0].Event.Kind != "production_tcb_proof" || tail[0].Event.Outcome != "" {
		return errors.New("Store journal holds records past the checkpoint other than one launch proof")
	}
	var event productionLaunchTCBProofEvent
	decoder := json.NewDecoder(bytes.NewReader(tail[0].Event.Data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil || event.SchemaVersion != "tbound-production-launch-tcb-proof/v1" ||
		event.WorkflowID != workflowID || event.SessionID != sessionID || event.ConversationID != conversationID ||
		event.RunID == "" || tail[0].Event.ID != "production-tcb-proof:"+event.RunID {
		return errors.Join(errors.New("launch proof record does not belong to this workflow, session and conversation"), err)
	}
	return nil
}
