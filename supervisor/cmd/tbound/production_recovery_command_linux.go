//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

type productionHistoricalCommandPin struct {
	IdentityDigest string
	Head           string
	RecordCount    uint64
}

type productionRecoveryCommandJournal struct {
	Journal  *audit.Journal
	Path     string
	Pin      productionHistoricalCommandPin
	Sessions []productionRecoveryCommandSession
}

type productionRecoveryCommandSession struct {
	RunID          string
	SessionID      string
	ConversationID string
}

type productionHistoricalBashHistory struct {
	entries     []*productionHistoricalBashVerifier
	byEffect    map[string]*productionHistoricalBashVerifier
	byLeaseView map[string][]*productionHistoricalBashVerifier
}

func newProductionHistoricalBashHistory(inputs []productionRecoveryCommandJournal,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, workflowID string,
	conversationIDs map[string]struct{}) (*productionHistoricalBashHistory, error) {
	if len(inputs) == 0 || workflowID == "" {
		return nil, commandEvidenceRefused("history-inputs")
	}
	history := &productionHistoricalBashHistory{
		byEffect:    make(map[string]*productionHistoricalBashVerifier),
		byLeaseView: make(map[string][]*productionHistoricalBashVerifier),
	}
	seenSession := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Journal == nil || input.Path == "" || input.Pin.IdentityDigest == "" || len(input.Sessions) == 0 {
			return nil, commandEvidenceRefused("history-journal-input")
		}
		for _, session := range input.Sessions {
			if session.RunID == "" || session.SessionID == "" || session.ConversationID == "" {
				return nil, commandEvidenceRefused("history-session-identity")
			}
			if _, duplicate := seenSession[session.SessionID]; duplicate {
				return nil, errors.New("old command journals repeat a workflow session identity")
			}
			seenSession[session.SessionID] = struct{}{}
			if len(conversationIDs) > 0 {
				if _, exists := conversationIDs[session.ConversationID]; !exists {
					return nil, errors.New("old command journal names a conversation outside the pinned transcript set")
				}
			}
			verifier, err := newProductionHistoricalBashVerifier(input.Journal, input.Pin, profile, template,
				session.RunID, session.SessionID, workflowID, session.ConversationID)
			if err != nil {
				return nil, err
			}
			for effectID, event := range verifier.byEffect {
				if _, duplicate := history.byEffect[effectID]; duplicate {
					return nil, errors.New("old command journals repeat one Store effect")
				}
				history.byEffect[effectID] = verifier
				key := event.Operation.ViewID + "\x00" + event.Operation.Operation.LeaseID
				history.byLeaseView[key] = append(history.byLeaseView[key], verifier)
			}
			history.entries = append(history.entries, verifier)
		}
	}
	return history, nil
}

func (h *productionHistoricalBashHistory) VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if h == nil || viewID == "" || leaseID == "" {
		return commandEvidenceRefused("history-settlement-identity")
	}
	verifiers := h.byLeaseView[viewID+"\x00"+leaseID]
	if len(verifiers) != 1 {
		return errors.New("old Bash settlement is not bound to exactly one retained command owner")
	}
	return verifiers[0].VerifySettlement(settlement, viewID, leaseID)
}

func (h *productionHistoricalBashHistory) VerifyStoreBundle(bundle sessionrepo.EvidenceBundle) error {
	if h == nil {
		return commandEvidenceRefused("history-bundle-unavailable")
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return commandEvidenceRefused("history-audit-journal", err)
	}
	used := make(map[string]struct{}, len(h.byEffect))
	for _, operation := range bundle.Operations {
		if !settledBashOperation(operation) {
			continue
		}
		verifier := h.byEffect[operation.EffectID]
		if verifier == nil {
			return fmt.Errorf("historical Bash effect %q ran but has no independently retained command owner", operation.EffectID)
		}
		event := verifier.byEffect[operation.EffectID]
		if !reflect.DeepEqual(event.Operation, operation) || !sameHistoricalTransitionForEvent(event, operation, bundle) {
			return fmt.Errorf("historical D09 command event for effect %q differs from Store evidence", operation.EffectID)
		}
		used[operation.EffectID] = struct{}{}
	}
	if len(used) != len(h.byEffect) {
		return errors.New("a retained D09 command receipt has no unique executed Store Bash effect")
	}
	return nil
}

func (h *productionHistoricalBashHistory) VerifyUnleasedProviderCallRetirement(
	binding providerbridge.ProviderCallRetirementBinding) error {
	if h == nil || binding.WorkflowID == "" || binding.ConversationID == "" ||
		binding.ToolCallIDIssuer != governedCallIssuer || binding.ToolCallID == "" {
		return commandEvidenceRefused("history-call-retirement")
	}
	for _, verifier := range h.entries {
		for _, event := range verifier.byEffect {
			if event.WorkflowID == binding.WorkflowID && event.ConversationID == binding.ConversationID &&
				event.ProviderCall.CallIssuer == binding.ToolCallIDIssuer && event.ProviderCall.CallID == binding.ToolCallID {
				return errors.New("captured provider call already has a retained D09 command lease/receipt")
			}
		}
	}
	return h.VerifyUnchanged()
}

func (h *productionHistoricalBashHistory) VerifyIdleProviderLineageRetirement(workflowID, conversationID string,
	providerSequence, previousOwnerEpoch, retirementOwnerEpoch uint64) error {
	if h == nil || workflowID == "" || conversationID == "" ||
		retirementOwnerEpoch <= previousOwnerEpoch {
		return commandEvidenceRefused("history-lineage-retirement")
	}
	return h.VerifyUnchanged()
}

func (h *productionHistoricalBashHistory) VerifyGovernedVerifier(verifier *governedStoreReplayVerifier) error {
	if h == nil || verifier == nil {
		return commandEvidenceRefused("history-governed-verifier-unavailable")
	}
	for _, decision := range verifier.decisions {
		if decision.binding.Proposal.Tool != "bash" || decision.binding.GateDecision.Verdict != gate.Allow {
			continue
		}
		if !decision.resultSeen || decision.effect.ID == "" || decision.entry.EffectID != decision.effect.ID {
			return errors.New("historical provider Bash result is not bound to a completed Store effect")
		}
		commandOwner := h.byEffect[decision.effect.ID]
		if commandOwner == nil {
			return errors.New("historical provider Bash result has no retained D09 command receipt")
		}
		event := commandOwner.byEffect[decision.effect.ID]
		proposalID, err := durableTrustedIdentity("proposal", decision.binding.Proposal, decision.binding.GateDecision)
		wantProviderCall := delta.OperationIdentity{Kind: delta.OperationProposalCall,
			ProposalID: proposalID, CallIssuer: governedCallIssuer, CallID: decision.binding.Proposal.ToolCallID}
		if err != nil || !reflect.DeepEqual(event.ProviderCall, wantProviderCall) ||
			!reflect.DeepEqual(event.Operation, decision.entry) {
			return errors.New("old D09 command receipt does not bind the exact provider proposal and Store operation")
		}
	}
	return nil
}

func (h *productionHistoricalBashHistory) VerifyUnchanged() error {
	if h == nil {
		return commandEvidenceRefused("history-unchanged-unavailable")
	}
	for _, verifier := range h.entries {
		if err := verifier.VerifyUnchanged(); err != nil {
			return err
		}
	}
	return nil
}

// productionHistoricalBashVerifier reads only the retained old command journal
// and pinned old D09 profile. It has no command runner, lease registrar, worker
// view, or execution method, so a new process cannot manufacture settlement
// evidence for a prior Bash operation.
type productionHistoricalBashVerifier struct {
	journal        *audit.Journal
	initialTrace   audit.Trace
	profile        piruntime.HostProfile
	template       piruntime.D09CellTemplate
	runID          string
	sessionID      string
	workflowID     string
	conversationID string
	profileDigest  string
	templateDigest string
	byEffect       map[string]productionHistoricalCommandEvent
	byLeaseView    map[string][]productionHistoricalCommandEvent
}

func newProductionHistoricalBashVerifier(journal *audit.Journal, pin productionHistoricalCommandPin,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, runID, sessionID, workflowID, conversationID string) (*productionHistoricalBashVerifier, error) {
	if journal == nil || pin.IdentityDigest == "" || runID == "" || sessionID == "" || workflowID == "" || conversationID == "" ||
		profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.SettlementProfile != piruntime.D09SettlementProfile {
		return nil, commandEvidenceRefused("historical-profile")
	}
	profileDigest := profile.Digest
	if !validDigestText(profileDigest) {
		return nil, commandEvidenceRefused("historical-profile-digest")
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.ContainmentProfileDigest != templateDigest {
		return nil, commandEvidenceRefused("historical-template-digest", err)
	}
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	if uint64(len(trace.Records)) != pin.RecordCount || auditHeadHash(trace.Records) != pin.Head {
		return nil, errors.New("retained D09 command journal differs from its host-pinned identity/head")
	}
	v := &productionHistoricalBashVerifier{
		journal: journal, initialTrace: trace, profile: profile, template: template,
		runID: runID, sessionID: sessionID, workflowID: workflowID, conversationID: conversationID,
		profileDigest: profileDigest, templateDigest: templateDigest,
		byEffect:    make(map[string]productionHistoricalCommandEvent),
		byLeaseView: make(map[string][]productionHistoricalCommandEvent),
	}
	for _, record := range trace.Records {
		if record.Event.Kind != productionD09SettledKind {
			return nil, errors.New("retained D09 command journal contains an unsupported event")
		}
		event, ok := decodeProductionHistoricalCommandEvent(record.Event.Data, runID, sessionID, workflowID, conversationID, profile.ID, profileDigest)
		if !ok ||
			event.Operation.EffectID == "" || event.Operation.Tool != "bash" ||
			event.Operation.Operation.Kind != delta.OperationLease || event.Operation.Operation.LeaseID == "" ||
			event.Operation.ViewID == "" {
			return nil, fmt.Errorf("retained D09 command event %d is not bound to the exact old host profile/session", record.Sequence)
		}
		if _, duplicate := v.byEffect[event.Operation.EffectID]; duplicate {
			return nil, errors.New("retained D09 command journal repeats one Store effect")
		}
		if err := validateHistoricalD09Receipt(event, profile, template, templateDigest); err != nil {
			return nil, fmt.Errorf("validate retained D09 command receipt %d: %w", record.Sequence, err)
		}
		v.byEffect[event.Operation.EffectID] = event
		key := event.Operation.ViewID + "\x00" + event.Operation.Operation.LeaseID
		v.byLeaseView[key] = append(v.byLeaseView[key], event)
	}
	return v, nil
}

// productionHistoricalCommandEvent is a retained production_d09_command_settled record of either shape. Transition is
// the approved transition of a promoted command, or the refused candidate of a command that delta admission
// denied after it ran; deniedAfterEffect(Operation) tells the two apart.
type productionHistoricalCommandEvent struct {
	WorkflowID     string
	ConversationID string
	ProviderCall   delta.OperationIdentity
	Operation      sessionrepo.OperationEvidence
	Transition     delta.Transition
	ReceiptDigest  string
	Receipt        json.RawMessage
}

// decodeProductionHistoricalCommandEvent decodes exactly the two shapes the live binder writes, each
// bound to the old run, session, workflow, conversation and host profile. A promoted event must carry a
// success operation and a denied event a denied_after_effect one.
func decodeProductionHistoricalCommandEvent(data []byte, runID, sessionID, workflowID, conversationID, profileID, profileDigest string) (productionHistoricalCommandEvent, bool) {
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	if json.Unmarshal(data, &header) != nil {
		return productionHistoricalCommandEvent{}, false
	}
	switch header.SchemaVersion {
	case productionD09SettledSchemaSuccess:
		var event productionD09CommandSettledEvent
		if unmarshalGovernedJSON(data, &event) != nil || event.RunID != runID || event.SessionID != sessionID ||
			event.WorkflowID != workflowID || event.ConversationID != conversationID ||
			event.ProfileID != profileID || event.ProfileDigest != profileDigest || event.Operation.Outcome != "success" {
			return productionHistoricalCommandEvent{}, false
		}
		return productionHistoricalCommandEvent{WorkflowID: event.WorkflowID, ConversationID: event.ConversationID,
			ProviderCall: event.ProviderCall, Operation: event.Operation, Transition: event.Transition,
			ReceiptDigest: event.ReceiptDigest, Receipt: event.Receipt}, true
	case productionD09SettledSchemaDenied:
		var event productionD09DeniedCommandSettledEvent
		if unmarshalGovernedJSON(data, &event) != nil || event.Outcome != productionD09OutcomeDenied ||
			event.RunID != runID || event.SessionID != sessionID || event.WorkflowID != workflowID ||
			event.ConversationID != conversationID || event.ProfileID != profileID || event.ProfileDigest != profileDigest ||
			!deniedAfterEffect(event.Operation) ||
			event.DeniedCandidateDigest != productionD09DeniedCandidateDigest(event.DeniedCandidate) {
			return productionHistoricalCommandEvent{}, false
		}
		return productionHistoricalCommandEvent{WorkflowID: event.WorkflowID, ConversationID: event.ConversationID,
			ProviderCall: event.ProviderCall, Operation: event.Operation, Transition: event.DeniedCandidate,
			ReceiptDigest: event.ReceiptDigest, Receipt: event.Receipt}, true
	}
	return productionHistoricalCommandEvent{}, false
}

func validateHistoricalD09Receipt(event productionHistoricalCommandEvent, profile piruntime.HostProfile,
	template piruntime.D09CellTemplate, templateDigest string) error {
	var receipt piruntime.D09CommandReceipt
	if len(event.Receipt) == 0 || unmarshalGovernedJSON(event.Receipt, &receipt) != nil || receipt.ValidateSnapshot() != nil {
		return commandEvidenceRefused("historical-receipt-snapshot")
	}
	if receipt.ReceiptDigest != event.ReceiptDigest || receipt.TemplateDigest != templateDigest ||
		receipt.ImageDigest != template.ExpectedImageDigest || receipt.ImageRef != registeredD09ImageDigestRef(template) ||
		receipt.LeaseID != event.Operation.Operation.LeaseID || receipt.ViewID != event.Operation.ViewID ||
		receipt.Settlement.LeaseID != event.Operation.Operation.LeaseID ||
		receipt.Settlement.ViewID != event.Operation.ViewID ||
		receipt.Settlement.ExitCode != receipt.ExitCode ||
		event.Operation.ExitCode == nil || *event.Operation.ExitCode != receipt.ExitCode ||
		profile.ContainmentProfileDigest != templateDigest || profile.SettlementProfile != piruntime.D09SettlementProfile {
		return commandEvidenceRefused("historical-receipt-binding")
	}
	if receipt.Proof.CapEff != "0000000000000000" || !receipt.Proof.NoNewPrivs || receipt.Proof.Seccomp != 2 ||
		!receipt.Proof.CgroupV2 || !receipt.Proof.LandlockTSYNC || receipt.Proof.LandlockABI < template.LandlockABI ||
		receipt.Proof.LandlockRulesetDigest != template.LandlockRulesetDigest || !receipt.Proof.ConsumerMountsVerified ||
		!receipt.Proof.RawSourceFDsClosed || !receipt.Proof.NoExternalRoutes || receipt.Proof.WorkerMountID == 0 ||
		receipt.Proof.WorkerMountID != receipt.Proof.RuntimeMountID {
		return commandEvidenceRefused("historical-receipt-proof")
	}
	return nil
}

func (v *productionHistoricalBashVerifier) VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if v == nil || viewID == "" || leaseID == "" {
		return commandEvidenceRefused("historical-settlement-identity")
	}
	events := v.byLeaseView[viewID+"\x00"+leaseID]
	if len(events) != 1 {
		return errors.New("historical Bash settlement does not resolve to one old D09 command event")
	}
	var receipt piruntime.D09CommandReceipt
	if unmarshalGovernedJSON(events[0].Receipt, &receipt) != nil || receipt.ValidateSnapshot() != nil ||
		!reflect.DeepEqual(receipt.Settlement, settlement) || events[0].Operation.ViewID != viewID ||
		events[0].Operation.Operation.LeaseID != leaseID {
		return commandEvidenceRefused("historical-settlement-receipt")
	}
	return nil
}

func (v *productionHistoricalBashVerifier) VerifyStoreBundle(bundle sessionrepo.EvidenceBundle) error {
	if v == nil {
		return commandEvidenceRefused("historical-bundle-unavailable")
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return commandEvidenceRefused("historical-audit-journal", err)
	}
	used := make(map[string]struct{}, len(v.byEffect))
	for _, operation := range bundle.Operations {
		if !settledBashOperation(operation) {
			continue
		}
		event, exists := v.byEffect[operation.EffectID]
		if !exists || !reflect.DeepEqual(event.Operation, operation) ||
			!sameHistoricalTransitionForEvent(event, operation, bundle) {
			return fmt.Errorf("executed historical Bash effect %q has no exact old D09 receipt", operation.EffectID)
		}
		if _, duplicate := used[operation.EffectID]; duplicate {
			return errors.New("historical Store repeats a D09 command effect")
		}
		used[operation.EffectID] = struct{}{}
	}
	if len(used) != len(v.byEffect) {
		return errors.New("retained D09 command journal contains a receipt with no executed Store Bash effect")
	}
	return nil
}

func sameHistoricalTransitionForEvent(event productionHistoricalCommandEvent, operation sessionrepo.OperationEvidence,
	bundle sessionrepo.EvidenceBundle) bool {
	denied := deniedAfterEffect(operation)
	matched := 0
	for _, transition := range bundle.ApprovedDeltaLedger {
		if denied || transition.ID != event.Transition.ID {
			continue
		}
		if !reflect.DeepEqual(transition, event.Transition) || transition.Operation != operation.Operation ||
			transition.Tool != operation.Tool || transition.InputGeneration != operation.InputGeneration ||
			transition.OutputGeneration != operation.OutputGeneration || transition.InputTreeDigest != operation.InputTreeDigest ||
			transition.OutputTreeDigest != operation.OutputTreeDigest || transition.ArgumentDigest != operation.ArgumentDigest {
			return false
		}
		matched++
	}
	if !denied && matched != 1 {
		return false
	}
	var primary audit.Trace
	primary, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return false
	}
	var effect *audit.EffectTrace
	for index := range primary.Effects {
		if primary.Effects[index].ID == operation.EffectID {
			if effect != nil {
				return false
			}
			effect = &primary.Effects[index]
		}
	}
	if effect == nil || effect.Unresolved || effect.Outcome != "success" || operation.ExitCode == nil ||
		event.Operation.AuditSequence != effect.OutcomeSequence || digestBytes(effect.Result) != operation.ResultDigest {
		return false
	}
	var result productionD09CommandEffectResult
	if unmarshalGovernedJSON(effect.Result, &result) != nil ||
		!sameDurableTransition(result.Transition, event.Transition) || !reflect.DeepEqual(result.Settlement, mustD09Settlement(event)) ||
		result.RunnerProfile != piruntime.D09CommandRunnerProfile {
		return false
	}
	var intent governedReplayOperationIntent
	if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.Request.EffectID != operation.EffectID ||
		intent.Request.Operation != operation.Operation || intent.Request.Tool != operation.Tool ||
		intent.Request.InputGeneration != operation.InputGeneration || intent.Request.InputTreeDigest != operation.InputTreeDigest ||
		intent.Request.ArgumentDigest != operation.ArgumentDigest {
		return false
	}
	if denied && verifyProductionD09DeniedRecord(bundle, effect.Intent, intent.Request, operation, event.Transition) != nil {
		return false
	}
	// DeclaredOutputPaths is not serialized (json:"-"): both decoded specs take the scope the request recorded.
	result.Spec.DeclaredOutputPaths = cloneDurableOutputScopePaths(intent.Request.OutputScopePaths)
	intent.Command.DeclaredOutputPaths = cloneDurableOutputScopePaths(intent.Request.OutputScopePaths)
	if !reflect.DeepEqual(result.Spec, intent.Command) || result.Command.ExitCode != *operation.ExitCode ||
		!result.Command.ExitObserved || result.Settlement.LeaseID != operation.Operation.LeaseID ||
		result.Settlement.ViewID != operation.ViewID || !result.Settlement.ProcessScopeEmpty ||
		!result.Settlement.WritersStopped || !result.Settlement.MountDetached {
		return false
	}
	var receipt piruntime.D09CommandReceipt
	if unmarshalGovernedJSON(event.Receipt, &receipt) != nil || receipt.ValidateSnapshot() != nil ||
		receipt.Settlement != result.Settlement || receipt.ExitCode != result.Command.ExitCode ||
		!result.Command.ExitObserved || digestBytes(mustJSON(result.Command)) != receipt.ResultDigest ||
		digestBytes(mustJSON(result.Settlement)) != receipt.SettlementDigest ||
		digestBytes(mustJSON(result.Spec)) != receipt.CommandSpecDigest {
		return false
	}
	return true
}

// settledBashOperation reports whether the Store ran a Bash command for operation, promoted or refused after
// the effect: either way a D09 cell settled it and a retained settlement must account for it.
func settledBashOperation(operation sessionrepo.OperationEvidence) bool {
	return operation.Tool == "bash" && (operation.Outcome == "success" || deniedAfterEffect(operation))
}

func mustD09Settlement(event productionHistoricalCommandEvent) sessionrepo.CommandSettlement {
	var receipt piruntime.D09CommandReceipt
	_ = unmarshalGovernedJSON(event.Receipt, &receipt)
	return receipt.Settlement
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (v *productionHistoricalBashVerifier) VerifyUnchanged() error {
	if v == nil || v.journal == nil {
		return commandEvidenceRefused("historical-unchanged-unavailable")
	}
	trace, err := v.journal.Trace()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(v.initialTrace, trace) {
		return errors.New("retained D09 command journal changed during historical verification")
	}
	return nil
}

func (v *productionHistoricalBashVerifier) VerifyGovernedVerifier(verifier *governedStoreReplayVerifier) error {
	if v == nil || verifier == nil {
		return commandEvidenceRefused("historical-governed-verifier-unavailable")
	}
	for _, decision := range verifier.decisions {
		if decision.binding.Proposal.Tool != "bash" || decision.binding.GateDecision.Verdict != gate.Allow {
			continue
		}
		if !decision.resultSeen || decision.effect.ID == "" || decision.entry.EffectID != decision.effect.ID {
			return errors.New("historical provider Bash result is not bound to a completed Store effect")
		}
		event, exists := v.byEffect[decision.effect.ID]
		proposalID, err := durableTrustedIdentity("proposal", decision.binding.Proposal, decision.binding.GateDecision)
		wantProviderCall := delta.OperationIdentity{Kind: delta.OperationProposalCall,
			ProposalID: proposalID, CallIssuer: governedCallIssuer, CallID: decision.binding.Proposal.ToolCallID}
		if !exists || err != nil || !reflect.DeepEqual(event.ProviderCall, wantProviderCall) ||
			!reflect.DeepEqual(event.Operation, decision.entry) {
			return errors.New("old D09 command receipt does not bind the exact provider proposal and Store operation")
		}
	}
	return nil
}
