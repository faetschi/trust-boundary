//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
)

// historicalSettlementVerifier is an independently retained verifier for the
// old command owner. A newly-created worker/runner must never be allowed to
// vouch for a settlement produced by a prior process.
type historicalSettlementVerifier func(sessionrepo.CommandSettlement, string, string) error

// governedHistoricalStoreVerifier authenticates each replayed Store request
// against the old, held primary journal without appending authorization or
// minting an operation/lease proof. Provider-to-Store joins are then checked by
// ReplayGovernedHistory before the workflow owner may reserve a restart.
type governedHistoricalStoreVerifier struct {
	journal              *audit.Journal
	initialTrace         audit.Trace
	byEffect             map[string]audit.Record
	effects              map[string]audit.EffectTrace
	storePolicyDigest    string
	metadataPolicyDigest string
	verifySettlement     historicalSettlementVerifier
}

func newGovernedHistoricalStoreVerifier(journal *audit.Journal, storePolicyDigest, metadataPolicyDigest string,
	settlementVerifier historicalSettlementVerifier) (*governedHistoricalStoreVerifier, error) {
	if journal == nil || !validBareSHA256(storePolicyDigest) || !validBareSHA256(metadataPolicyDigest) {
		return nil, errors.New("read-only historical verifier requires pinned Store policies")
	}
	trace, err := journal.Trace()
	if err != nil {
		return nil, err
	}
	v := &governedHistoricalStoreVerifier{
		journal: journal, initialTrace: trace, byEffect: make(map[string]audit.Record),
		effects: make(map[string]audit.EffectTrace), storePolicyDigest: storePolicyDigest,
		metadataPolicyDigest: metadataPolicyDigest, verifySettlement: settlementVerifier,
	}
	for _, effect := range trace.Effects {
		if strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			var intent governedReplayOperationIntent
			if decodeCanonicalGoverned(effect.Intent, &intent) == nil && intent.Request.Tool == "bash" && settlementVerifier == nil {
				return nil, errors.New("historical Bash operations require their old command-settlement context")
			}
		}
		if _, duplicate := v.effects[effect.ID]; duplicate {
			return nil, errors.New("historical Store journal repeats an effect ID")
		}
		v.effects[effect.ID] = effect
	}
	for _, record := range trace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil ||
			authorization.SchemaVersion != "tbound-governed-operation-admission/v2" ||
			authorization.EffectID == "" || record.Event.ID != "governed-operation:"+authorization.EffectID ||
			authorization.GateDecision.Verdict != gate.Allow {
			return nil, fmt.Errorf("historical Store authorization at sequence %d is malformed", record.Sequence)
		}
		if _, duplicate := v.byEffect[authorization.EffectID]; duplicate {
			return nil, errors.New("historical Store journal repeats an operation authorization")
		}
		v.byEffect[authorization.EffectID] = record
	}
	return v, nil
}

func (v *governedHistoricalStoreVerifier) verifyOperation(request sessionrepo.OperationRequest) error {
	if v == nil || v.journal == nil || request.EffectID == "" || request.Decision.Outcome != delta.PolicyAllow ||
		request.Decision.PolicyDigest != v.storePolicyDigest || request.Decision.MetadataPolicyDigest != v.metadataPolicyDigest {
		return ErrGovernedSessionEvidence
	}
	authorizationRecord, exists := v.byEffect[request.EffectID]
	if !exists {
		return errors.New("historical Store operation has no durable governed authorization")
	}
	var authorization governedOperationAdmissionRecord
	if decodeCanonicalGoverned(authorizationRecord.Event.Data, &authorization) != nil ||
		authorizationRecord.Event.ID != "governed-operation:"+request.EffectID ||
		authorization.EffectID != request.EffectID || authorization.DecisionID != request.Decision.ID ||
		authorization.Operation != request.Operation || authorization.Tool != request.Tool ||
		!sameDurableOutputScopePaths(authorization.OutputScopePaths, request.OutputScopePaths) ||
		authorization.TypedArgumentDigest != request.ArgumentDigest || authorization.InputGeneration != request.InputGeneration ||
		authorization.InputTreeDigest != request.InputTreeDigest || authorization.GatePolicyDigest == "" {
		return errors.New("historical Store operation differs from its exact durable authorization")
	}
	effect, exists := v.effects[request.EffectID]
	if !exists || effect.Unresolved || effect.Outcome != "success" || effect.IntentSequence <= authorizationRecord.Sequence {
		return errors.New("historical Store authorization has no later successful terminal effect")
	}
	var intent governedReplayOperationIntent
	if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion ||
		!reflect.DeepEqual(intent.Request, request) {
		return errors.New("historical Store effect intent differs from the exact operation request")
	}
	return nil
}

// VerifyBundle performs non-granting validation of every retained Store
// operation, effect, and transition against the exact held Store journal. The
// concrete full provider transcript replay remains responsible for proving the
// provider decision/call joins; this pass never calls a live authority.
func (v *governedHistoricalStoreVerifier) VerifyBundle(bundle sessionrepo.EvidenceBundle) error {
	if v == nil {
		return ErrGovernedSessionEvidence
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return errors.Join(ErrGovernedSessionEvidence, err)
	}
	used := make(map[string]struct{}, len(v.byEffect))
	for _, effect := range trace.Effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		var action struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(effect.Intent, &action) == nil && action.Action == "seed" {
			continue
		}
		var intent governedReplayOperationIntent
		if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion {
			return errors.New("historical Store operation intent is malformed")
		}
		if err := v.verifyOperation(intent.Request); err != nil {
			return err
		}
		used[intent.Request.EffectID] = struct{}{}
		if intent.Request.Tool == "bash" {
			var result governedReplayCommandEffect
			if decodeCanonicalGoverned(effect.Result, &result) != nil {
				return errors.New("historical Bash effect result is malformed")
			}
			if err := v.verifyCommandSettlement(result.Settlement, intent.Request.ViewID, intent.Request.Operation.LeaseID); err != nil {
				return fmt.Errorf("verify old Bash settlement from its retained command context: %w", err)
			}
		}
	}
	if len(used) != len(v.byEffect) {
		return errors.New("Store authorization records are not one-to-one with retained operation effects")
	}
	for _, transition := range bundle.ApprovedDeltaLedger {
		binding := delta.TransitionBinding{
			ID: transition.ID, Sequence: transition.Sequence, Tool: transition.Tool,
			ArgumentDigest: transition.ArgumentDigest, ViewID: transition.ViewID,
			ExecutionContextDigest: transition.ExecutionContextDigest,
			InputGeneration:        transition.InputGeneration, OutputGeneration: transition.OutputGeneration,
			InputTreeDigest: transition.InputTreeDigest, OutputTreeDigest: transition.OutputTreeDigest,
			Operation: transition.Operation, Changes: append([]delta.ObjectChange(nil), transition.Changes...),
		}
		if err := v.verifyDecision(transition.Decision, binding); err != nil {
			return fmt.Errorf("verify historical transition %q against its retained authorization: %w", transition.ID, err)
		}
	}
	return nil
}

// VerifyUnleasedProviderCallRetirement is a read-only join over every retained
// Store authorization and effect intent. A provider capture is retireable only
// when neither record names its exact call identity; no live authority or
// settlement callback is reachable from this verifier.
func (v *governedHistoricalStoreVerifier) VerifyUnleasedProviderCallRetirement(
	binding providerbridge.ProviderCallRetirementBinding) error {
	if v == nil || v.journal == nil || binding.WorkflowID == "" || binding.ConversationID == "" ||
		binding.ToolCallIDIssuer != governedCallIssuer || binding.ToolCallID == "" {
		return ErrGovernedSessionEvidence
	}
	if err := v.VerifyUnchanged(); err != nil {
		return err
	}
	for _, record := range v.byEffect {
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil {
			return errors.New("retained Store authorization changed or is malformed during retirement inspection")
		}
		if authorization.WorkflowID == binding.WorkflowID && authorization.ConversationID == binding.ConversationID &&
			(authorization.GateDecision.ToolCallID == binding.ToolCallID ||
				authorization.Operation.CallIssuer == binding.ToolCallIDIssuer && authorization.Operation.CallID == binding.ToolCallID) {
			return errors.New("captured provider call already has a Store authorization")
		}
	}
	for _, effect := range v.effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		authorizationRecord, authorized := v.byEffect[effect.ID]
		if !authorized {
			continue
		}
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(authorizationRecord.Event.Data, &authorization) != nil {
			return errors.New("retained Store authorization is malformed during retirement inspection")
		}
		if authorization.WorkflowID != binding.WorkflowID || authorization.ConversationID != binding.ConversationID {
			continue
		}
		var intent governedReplayOperationIntent
		if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion {
			return errors.New("retained Store effect intent is malformed during retirement inspection")
		}
		if intent.Request.Operation.CallIssuer == binding.ToolCallIDIssuer && intent.Request.Operation.CallID == binding.ToolCallID {
			return errors.New("captured provider call already has a Store effect intent or lease")
		}
	}
	return nil
}

func (v *governedHistoricalStoreVerifier) verifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if v == nil || decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != v.storePolicyDigest ||
		decision.MetadataPolicyDigest != v.metadataPolicyDigest ||
		(binding.Operation.Kind != delta.OperationProposalCall && binding.Operation.Kind != delta.OperationLease) {
		return ErrGovernedSessionEvidence
	}
	var matched *audit.Record
	var authorization governedOperationAdmissionRecord
	for _, record := range v.byEffect {
		var candidate governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &candidate) != nil || candidate.Operation != binding.Operation {
			continue
		}
		if matched != nil {
			return errors.New("historical Store transition has multiple operation authorizations")
		}
		copy := record
		matched, authorization = &copy, candidate
	}
	if matched == nil || authorization.DecisionID != decision.ID || authorization.Tool != binding.Tool ||
		authorization.TypedArgumentDigest != binding.ArgumentDigest || authorization.InputGeneration != binding.InputGeneration ||
		authorization.InputTreeDigest != binding.InputTreeDigest || authorization.GateDecision.Verdict != gate.Allow {
		return errors.New("historical Store transition is not bound to its exact prior authorization")
	}
	if _, exists := v.effects[authorization.EffectID]; !exists {
		return errors.New("historical Store transition authorization has no retained effect identity")
	}
	return nil
}

func (v *governedHistoricalStoreVerifier) verifyCommandSettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if v == nil || v.verifySettlement == nil || viewID == "" || leaseID == "" {
		return errors.New("historical Bash settlement context is unavailable")
	}
	return v.verifySettlement(settlement, viewID, leaseID)
}

func (v *governedHistoricalStoreVerifier) VerifyUnchanged() error {
	if v == nil || v.journal == nil {
		return ErrGovernedSessionEvidence
	}
	trace, err := v.journal.Trace()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(v.initialTrace, trace) {
		return errors.New("historical Store journal changed during read-only recovery validation")
	}
	return nil
}
