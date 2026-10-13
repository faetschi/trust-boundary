//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/sessionrepo"
)

func (a *SessionOperationAuthority) VerifyOrigin(bundle sessionrepo.EvidenceBundle, transition delta.Transition) error {
	storeTrace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return err
	}
	if uint64(len(storeTrace.Records)) != bundle.AuditRecordCount || auditHeadHash(storeTrace.Records) != bundle.AuditHeadHash {
		return ErrGovernedSessionEvidence
	}
	storeJournalTrace, err := a.storeJournal.Trace()
	if err != nil {
		return err
	}
	storeJournalBytes, err := marshalGovernedAuditRecords(storeJournalTrace.Records)
	if err != nil || !bytes.Equal(storeJournalBytes, bundle.AuditJournal) {
		return errors.Join(ErrGovernedSessionEvidence, err)
	}
	providerTrace, err := a.providerJournal.Trace()
	if err != nil {
		return err
	}
	proof, result, err := a.providerResultForTransition(providerTrace, transition)
	if err != nil {
		return err
	}
	operation, decision, typedDigest, err := a.bindOperation(proof)
	if err != nil || proof.result == nil || !reflect.DeepEqual(*proof.result, result) || operation != transition.Operation ||
		result.TransitionID != transition.ID || result.TransitionSeq != transition.Sequence || result.Tool != transition.Tool ||
		result.GenerationFrom != transition.InputGeneration || result.GenerationTo != transition.OutputGeneration ||
		result.TreeDigestFrom != transition.InputTreeDigest || result.TreeDigestTo != transition.OutputTreeDigest || result.EffectID == "" {
		return ErrGovernedSessionEvidence
	}
	protocolResult, err := protocol.DecodeResult(result.Result)
	if err != nil {
		return err
	}
	if protocolResult.SchemaVersion != protocol.ResultSchemaVersion || protocolResult.ResponseID == nil ||
		protocolResult.ResponseID.Issuer != governedResponseIssuer || protocolResult.ResponseID.Opaque != result.ResponseID ||
		protocolResult.ToolCallID != result.ToolCallID || protocolResult.Tool != result.Tool || protocolResult.Sequence != result.Sequence ||
		protocolResult.CanonicalArgumentsDigest != result.ArgumentDigest || protocolResult.Verdict != string(gate.Allow) {
		return ErrGovernedSessionEvidence
	}
	var operationEvidence *sessionrepo.OperationEvidence
	for index := range bundle.Operations {
		candidate := &bundle.Operations[index]
		if candidate.EffectID == result.EffectID {
			if operationEvidence != nil {
				return errors.New("store evidence repeats a governed effect ID")
			}
			operationEvidence = candidate
		}
	}
	if operationEvidence == nil || operationEvidence.Operation != operation || operationEvidence.DecisionID != decision.ID ||
		operationEvidence.Tool != result.Tool || operationEvidence.ArgumentDigest != typedDigest ||
		operationEvidence.InputGeneration != result.GenerationFrom || operationEvidence.InputTreeDigest != result.TreeDigestFrom ||
		operationEvidence.OutputGeneration != result.GenerationTo || operationEvidence.OutputTreeDigest != result.TreeDigestTo ||
		operationEvidence.Outcome != "success" || operationEvidence.ResultDigest == "" || operationEvidence.AuditSequence == 0 {
		return ErrGovernedSessionEvidence
	}
	if result.Tool == "bash" {
		if operationEvidence.ViewID != transition.ViewID ||
			operationEvidence.ExecutionContextDigest != transition.ExecutionContextDigest ||
			operationEvidence.CommandRunnerProfile == "" || operationEvidence.ExitCode == nil {
			return ErrGovernedSessionEvidence
		}
		if a.requireEvidencedBashContainment {
			if operationEvidence.CommandContainmentStatus != "not-established" {
				return ErrGovernedSessionEvidence
			}
			proposalID, proposalErr := durableTrustedIdentity("proposal", proof.proposal, proof.decision)
			providerCall := delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
				CallIssuer: governedCallIssuer, CallID: proof.proposal.ToolCallID}
			verifier, ok := a.runner.(governedBashEvidenceOriginVerifier)
			if proposalErr != nil || !ok || verifier.VerifyProductionBashEvidenceWithOrigin(context.Background(), bundle,
				providerCall, transition, *operationEvidence) != nil {
				return ErrGovernedSessionEvidence
			}
		} else if operationEvidence.CommandContainmentStatus != "not-established" {
			return ErrGovernedSessionEvidence
		}
	}
	expectedOutputScopePaths, scopeErr := governedOperationOutputScope(proof.proposal, a.bashOutputPaths)
	if scopeErr != nil {
		return errors.Join(ErrGovernedSessionEvidence, scopeErr)
	}
	var operationAdmission *governedOperationAdmissionRecord
	var operationAdmissionSequence uint64
	for _, record := range storeTrace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		var candidate governedOperationAdmissionRecord
		if unmarshalGovernedJSON(record.Event.Data, &candidate) != nil || candidate.EffectID != result.EffectID {
			continue
		}
		if operationAdmission != nil {
			return errors.New("store journal repeats one governed operation admission")
		}
		operationAdmission = &candidate
		operationAdmissionSequence = record.Sequence
	}
	if operationAdmission == nil || operationAdmission.SchemaVersion != "tbound-governed-operation-admission/v2" ||
		operationAdmission.WorkflowID != a.workflowID || operationAdmission.ConversationID != a.conversationID ||
		operationAdmission.ProviderProfileID != a.providerProfileID || operationAdmission.DecisionID != decision.ID ||
		operationAdmission.Operation != operation || operationAdmission.Tool != result.Tool ||
		!sameDurableOutputScopePaths(operationAdmission.OutputScopePaths, expectedOutputScopePaths) ||
		operationAdmission.TypedArgumentDigest != typedDigest || operationAdmission.InputGeneration != result.GenerationFrom ||
		operationAdmission.InputTreeDigest != result.TreeDigestFrom || operationAdmission.GatePolicyDigest != a.gatePolicyDigest ||
		operationAdmission.ProviderDecisionSequence != proof.decisionSequence ||
		operationAdmission.ProviderDecisionRecordHash != proof.decisionHash || !reflect.DeepEqual(operationAdmission.GateDecision, proof.decision) {
		return ErrGovernedSessionEvidence
	}
	var effectTrace *audit.EffectTrace
	for index := range storeTrace.Effects {
		candidate := &storeTrace.Effects[index]
		if candidate.ID == result.EffectID {
			if effectTrace != nil {
				return ErrGovernedSessionEvidence
			}
			effectTrace = candidate
		}
	}
	if effectTrace == nil || operationAdmissionSequence >= effectTrace.IntentSequence || operationEvidence.AuditSequence != effectTrace.OutcomeSequence ||
		effectTrace.Outcome != "success" {
		return ErrGovernedSessionEvidence
	}
	providerGateFound := false
	for _, record := range providerTrace.Records {
		if record.Sequence == proof.decisionSequence && record.Hash == proof.decisionHash && record.Event.Kind == "provider_gate_decision" {
			var decisionRecord gate.Decision
			if unmarshalGovernedJSON(record.Event.Data, &decisionRecord) == nil && reflect.DeepEqual(decisionRecord, proof.decision) {
				providerGateFound = true
			}
		}
	}
	if !providerGateFound {
		return ErrGovernedSessionEvidence
	}
	if result.Tool == "bash" {
		var mutation durableMutationSummaryPayload
		if decodeGovernedResult(protocolResult.Output, &mutation) != nil || mutation.Command == nil || !mutation.Command.ExitObserved ||
			mutation.Command.ExitCode != *operationEvidence.ExitCode || mutation.Command.CommandContainmentStatus != operationEvidence.CommandContainmentStatus ||
			mutation.Command.CommandRunnerProfile != operationEvidence.CommandRunnerProfile {
			return ErrGovernedSessionEvidence
		}
	}
	return nil
}

func (a *SessionOperationAuthority) VerifyTransition(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if err := a.VerifyDecision(decision, binding); err != nil {
		return err
	}
	proof, err := a.proofForDecisionID(decision.ID)
	if err != nil {
		return err
	}
	if proof.result == nil || proof.result.TransitionID != binding.ID || proof.result.TransitionSeq != binding.Sequence ||
		proof.result.GenerationFrom != binding.InputGeneration || proof.result.GenerationTo != binding.OutputGeneration ||
		proof.result.TreeDigestFrom != binding.InputTreeDigest || proof.result.TreeDigestTo != binding.OutputTreeDigest {
		return ErrGovernedSessionEvidence
	}
	return nil
}

func (a *SessionOperationAuthority) providerResultForTransition(trace audit.Trace, transition delta.Transition) (governedCapture, providerResultEvidence, error) {
	for _, record := range trace.Records {
		if record.Event.Kind != "provider_tool_result" {
			continue
		}
		var result providerResultEvidence
		if unmarshalGovernedJSON(record.Event.Data, &result) != nil || result.TransitionID != transition.ID || result.TransitionSeq != transition.Sequence {
			continue
		}
		if result.WorkflowID != a.workflowID || result.ConversationID != a.conversationID || result.ResponseIDIssuer != governedResponseIssuer ||
			result.ToolCallIDIssuer != governedCallIssuer || result.ResultID != stableProviderResultID(a.workflowID, a.conversationID, result.ResponseID, result.ToolCallID) ||
			result.ResultDigest != digestBytes(result.Result) || !strings.HasPrefix(record.Event.ID, a.conversationID+":"+result.ResultID+":") {
			return governedCapture{}, providerResultEvidence{}, ErrGovernedSessionEvidence
		}
		protocolResult, decodeErr := protocol.DecodeResult(result.Result)
		if decodeErr != nil || protocolResult.ResponseID == nil || protocolResult.ResponseID.Issuer != result.ResponseIDIssuer ||
			protocolResult.ResponseID.Opaque != result.ResponseID || protocolResult.ToolCallID != result.ToolCallID ||
			protocolResult.Tool != result.Tool || protocolResult.Sequence != result.Sequence ||
			protocolResult.CanonicalArgumentsDigest != result.ArgumentDigest || protocolResult.Verdict != string(gate.Allow) || len(protocolResult.Output) == 0 {
			return governedCapture{}, providerResultEvidence{}, ErrGovernedSessionEvidence
		}
		decisionID := durableDecisionIDFromResult(trace, result, record.Sequence)
		if decisionID == "" {
			return governedCapture{}, providerResultEvidence{}, ErrGovernedSessionEvidence
		}
		proof, proofErr := a.resolveFromTrace(trace, decisionID)
		if proofErr != nil || proof.result == nil || !reflect.DeepEqual(*proof.result, result) || proof.decisionSequence >= record.Sequence {
			return governedCapture{}, providerResultEvidence{}, errors.Join(ErrGovernedSessionEvidence, proofErr)
		}
		return proof, result, nil
	}
	return governedCapture{}, providerResultEvidence{}, ErrGovernedSessionEvidence
}

func durableDecisionIDFromResult(trace audit.Trace, result providerResultEvidence, resultSequence uint64) string {
	decisionID := ""
	for i := len(trace.Records) - 1; i >= 0; i-- {
		record := trace.Records[i]
		if record.Sequence >= resultSequence {
			continue
		}
		if record.Event.Kind != "provider_gate_decision" {
			continue
		}
		var decision gate.Decision
		if unmarshalGovernedJSON(record.Event.Data, &decision) == nil && decision.ResponseID != nil &&
			decision.ResponseID.Issuer == result.ResponseIDIssuer && decision.ResponseID.Opaque == result.ResponseID &&
			decision.ToolCallID == result.ToolCallID && decision.Tool == result.Tool && decision.Sequence == result.Sequence &&
			decision.Verdict == gate.Allow && decision.CanonicalArgumentsDigest == result.ArgumentDigest {
			proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: result.ToolCallID, Tool: result.Tool}
			candidate, err := durableTrustedIdentity("decision", proposal, decision)
			if err != nil || decisionID != "" {
				return ""
			}
			decisionID = candidate
		}
	}
	return decisionID
}

func stableProviderResultID(workflowID, conversationID, responseID, callID string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("tbound/provider-result/v1\x00"))
	_, _ = hash.Write([]byte(workflowID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(conversationID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(responseID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(callID))
	return "provider-result:" + hex.EncodeToString(hash.Sum(nil))
}

func verifyProviderEventID(record audit.Record, conversationID, eventID string, ordinal uint64) error {
	expected := fmt.Sprintf("%s:%s:%d", conversationID, eventID, ordinal)
	if record.Event.ID != expected {
		return fmt.Errorf("%w: provider audit event identity/ordinal mismatch", ErrGovernedSessionEvidence)
	}
	return nil
}
