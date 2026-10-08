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
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/ipc"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	governedCallIssuer                 = "openrouter-tool-call/v1"
	governedResponseIssuer             = "openrouter-response/v1"
	maxGovernedOperationProofs         = 1024
	governedOperationAuthorizationKind = "governed_operation_authorized"
)

var (
	ErrGovernedSessionEvidence = errors.New("governed session audit evidence did not authorize the exact operation")
	ErrLeaseOriginUnsupported  = errors.New("publication finalizer does not yet accept authenticated terminal lease origins")
)

type providerExchangeEvidence struct {
	WorkflowID       string                  `json:"workflow_id"`
	Turn             uint64                  `json:"turn"`
	Request          providerRequestEvidence `json:"request"`
	StatusCode       int                     `json:"status_code"`
	Response         []byte                  `json:"response"`
	ResponseIDIssuer string                  `json:"response_id_issuer"`
	ToolCallIDIssuer string                  `json:"tool_call_id_issuer"`
	ResponseID       string                  `json:"response_id"`
	Model            string                  `json:"model"`
	ToolCall         *struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_call,omitempty"`
}

type providerRequestEvidence struct {
	ProfileID     string              `json:"profile_id"`
	Method        string              `json:"method"`
	URL           string              `json:"url"`
	Host          string              `json:"host"`
	ContentLength int64               `json:"content_length"`
	Headers       map[string][]string `json:"headers"`
	Body          []byte              `json:"body"`
}

type providerPromptEvidence struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"`
	Generation     string `json:"generation"`
	Prompt         string `json:"prompt"`
}

type providerResultEvidence struct {
	WorkflowID       string `json:"workflow_id"`
	ConversationID   string `json:"conversation_id"`
	ResponseIDIssuer string `json:"response_id_issuer"`
	ToolCallIDIssuer string `json:"tool_call_id_issuer"`
	ResponseID       string `json:"response_id"`
	ToolCallID       string `json:"tool_call_id"`
	Tool             string `json:"tool"`
	ArgumentDigest   string `json:"argument_digest"`
	Sequence         uint64 `json:"sequence"`
	GenerationFrom   string `json:"generation_from"`
	GenerationTo     string `json:"generation_to"`
	TreeDigestFrom   string `json:"tree_digest_from"`
	TreeDigestTo     string `json:"tree_digest_to"`
	ResultID         string `json:"result_id"`
	ResultDigest     string `json:"result_digest"`
	Result           []byte `json:"result"`
	TransitionID     string `json:"transition_id,omitempty"`
	TransitionSeq    uint64 `json:"transition_sequence,omitempty"`
	EffectID         string `json:"effect_id,omitempty"`
}

type governedCapture struct {
	proposal         protocol.Proposal
	decision         gate.Decision
	response         string
	call             string
	sequence         uint64
	generation       string
	treeDigest       string
	exchangeSequence uint64
	decisionSequence uint64
	decisionHash     string
	result           *providerResultEvidence
}

type governedOperationProof struct {
	capture governedCapture
	request sessionrepo.OperationRequest
	record  audit.Record
}

type governedOperationAdmissionRecord struct {
	SchemaVersion              string                  `json:"schema_version"`
	WorkflowID                 string                  `json:"workflow_id"`
	ConversationID             string                  `json:"conversation_id"`
	ProviderProfileID          string                  `json:"provider_profile_id"`
	EffectID                   string                  `json:"effect_id"`
	DecisionID                 string                  `json:"decision_id"`
	Operation                  delta.OperationIdentity `json:"operation"`
	Tool                       string                  `json:"tool"`
	TypedArgumentDigest        string                  `json:"typed_argument_digest"`
	InputGeneration            string                  `json:"input_generation"`
	InputTreeDigest            string                  `json:"input_tree_digest"`
	GatePolicyDigest           string                  `json:"gate_policy_digest"`
	ProviderDecisionSequence   uint64                  `json:"provider_decision_sequence"`
	ProviderDecisionRecordHash string                  `json:"provider_decision_record_hash"`
	GateDecision               gate.Decision           `json:"gate_decision"`
}

// SessionOperationAuthority authenticates Store operations against the exact
// synced providerbridge capture and gate-decision journal records. It is not
// manufactured from a caller boolean, filename, Pi observation, or callback
// returning nil. Store options created from this object still inherit the
// external provider profile and sessionrepo's existing TCB assumptions.
type SessionOperationAuthority struct {
	mu                   sync.Mutex
	storeJournal         *audit.Journal
	providerJournal      *audit.Journal
	workflowID           string
	conversationID       string
	providerProfileID    string
	gatePolicyDigest     string
	storePolicyDigest    string
	metadataPolicyDigest string
	store                *sessionrepo.Store
	managedStore         *sessionlaunch.ManagedStore
	baseline             *sessionrepo.Generation
	prepared             *sessionlaunch.PreparedSession
	initialGeneration    string
	initialTreeDigest    string
	baselineBound        bool
	runner               *sessionlaunch.DevelopmentCommandRunner
	conversation         *providerbridge.Conversation
	operationProofs      map[delta.OperationIdentity]governedOperationProof
}

func NewSessionOperationAuthority(storeJournal, providerJournal *audit.Journal, workflowID, conversationID, providerProfileID string, gatePolicy gate.Policy,
	storePolicyDigest, metadataPolicyDigest string, runner *sessionlaunch.DevelopmentCommandRunner) (*SessionOperationAuthority, error) {
	if storeJournal == nil || providerJournal == nil || storeJournal == providerJournal ||
		!validIdentityText(workflowID) || !validIdentityText(conversationID) || !validIdentityText(providerProfileID) ||
		gatePolicy.Digest() == "" || !validBareSHA256(storePolicyDigest) || !validBareSHA256(metadataPolicyDigest) {
		return nil, errors.New("governed session authority requires distinct protected Store/provider journals and fixed profile identities")
	}
	return &SessionOperationAuthority{
		storeJournal: storeJournal, providerJournal: providerJournal,
		workflowID: workflowID, conversationID: conversationID,
		providerProfileID: providerProfileID,
		gatePolicyDigest:  gatePolicy.Digest(), storePolicyDigest: storePolicyDigest,
		metadataPolicyDigest: metadataPolicyDigest, runner: runner,
		operationProofs: make(map[delta.OperationIdentity]governedOperationProof),
	}, nil
}

// BindBaseline is called after sessionrepo.Seed and before Conversation prompt
// admission. It compares the supplied generation to verified retained bundle
// evidence; it does not accept a caller-provided generation label alone.
func (a *SessionOperationAuthority) BindBaseline(store *sessionrepo.Store, generation *sessionrepo.Generation) error {
	if a == nil || store == nil || generation == nil {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	managed := a.managedStore
	a.mu.Unlock()
	if managed == nil || managed.Store() != store {
		return errors.New("baseline Store was not created with this authority's concrete session callbacks")
	}
	verified, err := store.Verify()
	if err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	trace, err := a.storeJournal.Trace()
	if err != nil {
		return err
	}
	encodedTrace, err := marshalGovernedAuditRecords(trace.Records)
	if err != nil || !bytes.Equal(encodedTrace, bundle.AuditJournal) {
		return errors.Join(ErrGovernedSessionEvidence, err)
	}
	if len(bundle.Generations) != 1 || bundle.Baseline.ID != generation.ID() ||
		bundle.Baseline.TreeDigest != generation.TreeDigest() || verified.SealedGeneration != generation.ID() ||
		verified.SealedTreeDigest != generation.TreeDigest() || bundle.PolicyDigest != a.storePolicyDigest ||
		bundle.MetadataPolicyDigest != a.metadataPolicyDigest || !sameManifest(bundle.Baseline.Manifest, generation.Manifest()) {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.baselineBound {
		return errors.New("governed session baseline was already bound")
	}
	a.store, a.baseline = store, generation
	a.initialGeneration, a.initialTreeDigest, a.baselineBound = generation.ID(), generation.TreeDigest(), true
	return nil
}

func (a *SessionOperationAuthority) BindPreparedSession(prepared *sessionlaunch.PreparedSession) error {
	if a == nil || prepared == nil {
		return sessionlaunch.ErrProvenanceMissing
	}
	a.mu.Lock()
	store, generation, journal, bound := a.store, a.baseline, a.storeJournal, a.baselineBound
	if !bound || a.prepared != nil {
		a.mu.Unlock()
		return errors.New("baseline is unbound or D06 preparation was already attached")
	}
	a.mu.Unlock()
	if err := prepared.VerifySourceBinding(store, generation, journal); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.prepared != nil || a.store != store || a.baseline != generation || a.storeJournal != journal {
		return errors.New("governed session source changed while D06 preparation was being attached")
	}
	a.prepared = prepared
	return nil
}

func (a *SessionOperationAuthority) AuthorizeOperation(request sessionrepo.OperationRequest) error {
	if a == nil || a.storeJournal == nil || a.providerJournal == nil {
		return ErrGovernedSessionEvidence
	}
	if err := a.requirePreparedJournal(); err != nil {
		return err
	}
	proof, err := a.proofForDecisionID(request.Decision.ID)
	if err != nil {
		return err
	}
	operation, decision, typedDigest, err := a.bindOperation(proof)
	if err != nil {
		return err
	}
	if request.Tool != proof.proposal.Tool || request.Operation != operation || request.Decision != decision ||
		request.InputGeneration != proof.generation || request.InputTreeDigest != proof.treeDigest ||
		request.ArgumentDigest != typedDigest || request.EffectID == "" {
		return ErrGovernedSessionEvidence
	}
	if proof.proposal.Tool == "bash" {
		if !validGovernedViewID(request.ViewID) || request.ExecutionContextDigest != governedExecutionContextDigest(request.ViewID, request.InputTreeDigest, request.Operation.LeaseID) ||
			a.runner == nil {
			return ErrGovernedSessionEvidence
		}
	} else if request.ViewID != "" || request.ExecutionContextDigest != "" {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.operationProofs) >= maxGovernedOperationProofs {
		return errors.New("governed operation proof budget exhausted")
	}
	if _, duplicate := a.operationProofs[operation]; duplicate {
		return errors.New("governed operation identity was already admitted")
	}
	admission := governedOperationAdmissionRecord{
		SchemaVersion: "tbound-governed-operation-admission/v1", WorkflowID: a.workflowID,
		ConversationID: a.conversationID, ProviderProfileID: a.providerProfileID,
		EffectID: request.EffectID, DecisionID: request.Decision.ID, Operation: operation,
		Tool: request.Tool, TypedArgumentDigest: typedDigest, InputGeneration: request.InputGeneration,
		InputTreeDigest: request.InputTreeDigest, GatePolicyDigest: a.gatePolicyDigest,
		ProviderDecisionSequence: proof.decisionSequence, ProviderDecisionRecordHash: proof.decisionHash,
		GateDecision: proof.decision,
	}
	encodedAdmission, err := json.Marshal(admission)
	if err != nil {
		return err
	}
	authorizationRecord, err := a.storeJournal.Append(audit.Event{
		Kind: governedOperationAuthorizationKind, ID: "governed-operation:" + request.EffectID, Data: encodedAdmission,
	})
	if err != nil {
		return fmt.Errorf("durably bind Store effect to captured provider decision before execution: %w", err)
	}
	if proof.proposal.Tool == "bash" {
		if err := a.runner.RegisterLease(request.ViewID, request.Operation.LeaseID); err != nil {
			return fmt.Errorf("register authenticated Bash view/lease: %w", err)
		}
	}
	a.operationProofs[operation] = governedOperationProof{capture: proof, request: request, record: authorizationRecord}
	return nil
}

func (a *SessionOperationAuthority) VerifyDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if a == nil || decision.Outcome != delta.PolicyAllow || decision.PolicyDigest != a.storePolicyDigest ||
		decision.MetadataPolicyDigest != a.metadataPolicyDigest {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	authorization, exists := a.operationProofs[binding.Operation]
	prepared := a.prepared != nil && a.baselineBound
	a.mu.Unlock()
	if !exists || !prepared {
		return ErrGovernedSessionEvidence
	}
	proof := authorization.capture
	if authorization.record.Sequence == 0 || authorization.record.Event.Kind != governedOperationAuthorizationKind ||
		authorization.record.Event.ID != "governed-operation:"+authorization.request.EffectID ||
		authorization.request.Decision != decision || authorization.request.Operation != binding.Operation ||
		authorization.request.Tool != binding.Tool ||
		authorization.request.InputGeneration != binding.InputGeneration || authorization.request.InputTreeDigest != binding.InputTreeDigest {
		return ErrGovernedSessionEvidence
	}
	var admitted governedOperationAdmissionRecord
	if unmarshalGovernedJSON(authorization.record.Event.Data, &admitted) != nil ||
		admitted.SchemaVersion != "tbound-governed-operation-admission/v1" || admitted.WorkflowID != a.workflowID ||
		admitted.ConversationID != a.conversationID || admitted.ProviderProfileID != a.providerProfileID ||
		admitted.EffectID != authorization.request.EffectID || admitted.DecisionID != decision.ID ||
		admitted.Operation != binding.Operation || admitted.Tool != binding.Tool ||
		admitted.TypedArgumentDigest != binding.ArgumentDigest || admitted.InputGeneration != binding.InputGeneration ||
		admitted.InputTreeDigest != binding.InputTreeDigest || admitted.GatePolicyDigest != a.gatePolicyDigest ||
		admitted.ProviderDecisionSequence != proof.decisionSequence || admitted.ProviderDecisionRecordHash != proof.decisionHash ||
		!reflect.DeepEqual(admitted.GateDecision, proof.decision) {
		return ErrGovernedSessionEvidence
	}
	operation, expectedDecision, typedDigest, err := a.bindOperation(proof)
	if err != nil {
		return err
	}
	if expectedDecision != decision || binding.Tool != proof.proposal.Tool || binding.ArgumentDigest != typedDigest ||
		binding.Operation != operation || binding.InputGeneration != proof.generation || binding.InputTreeDigest != proof.treeDigest ||
		authorization.request.ArgumentDigest != typedDigest ||
		!validNextGeneration(binding.InputGeneration, binding.OutputGeneration) {
		return ErrGovernedSessionEvidence
	}
	if proof.proposal.Tool == "bash" {
		if !validGovernedViewID(binding.ViewID) || binding.ExecutionContextDigest != governedExecutionContextDigest(binding.ViewID, binding.InputTreeDigest, binding.Operation.LeaseID) {
			return ErrGovernedSessionEvidence
		}
	} else if binding.ViewID != "" || binding.ExecutionContextDigest != "" {
		return ErrGovernedSessionEvidence
	}
	if proof.result != nil && (proof.result.TransitionID != binding.ID || proof.result.TransitionSeq != binding.Sequence ||
		proof.result.GenerationFrom != binding.InputGeneration || proof.result.GenerationTo != binding.OutputGeneration ||
		proof.result.TreeDigestFrom != binding.InputTreeDigest || proof.result.TreeDigestTo != binding.OutputTreeDigest) {
		return ErrGovernedSessionEvidence
	}
	if err := validateToolChangeScope(proof.proposal, binding); err != nil {
		return err
	}
	return nil
}

func (a *SessionOperationAuthority) VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if a == nil || a.runner == nil {
		return errors.New("governed session has no measured command-settlement runner")
	}
	return a.runner.VerifySettlement(settlement, viewID, leaseID)
}

func (a *SessionOperationAuthority) proofForDecisionID(decisionID string) (governedCapture, error) {
	if a == nil || a.providerJournal == nil || decisionID == "" {
		return governedCapture{}, ErrGovernedSessionEvidence
	}
	trace, err := a.providerJournal.Trace()
	if err != nil {
		return governedCapture{}, err
	}
	return a.resolveFromTrace(trace, decisionID)
}

func (a *SessionOperationAuthority) resolveFromTrace(trace audit.Trace, decisionID string) (governedCapture, error) {
	a.mu.Lock()
	initialGeneration, initialTree, bound := a.initialGeneration, a.initialTreeDigest, a.baselineBound
	a.mu.Unlock()
	if !bound {
		return governedCapture{}, errors.New("governed baseline is not bound")
	}
	currentGeneration, currentTree := initialGeneration, initialTree
	exchanges := make(map[string]governedCapture)
	decisions := make(map[string]gate.Decision)
	proofs := make([]governedCapture, 0, 16)
	var providerOrdinal uint64
	for _, record := range trace.Records {
		switch record.Event.Kind {
		case "provider_user_admitted":
			providerOrdinal++
			if err := verifyProviderEventID(record, a.conversationID, "user", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var prompt providerPromptEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &prompt); err != nil || prompt.WorkflowID != a.workflowID ||
				prompt.ConversationID != a.conversationID || prompt.Generation != currentGeneration || strings.TrimSpace(prompt.Prompt) == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
		case "provider_exchange":
			providerOrdinal++
			if err := verifyProviderEventID(record, a.conversationID, "exchange", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var exchange providerExchangeEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &exchange); err != nil || exchange.WorkflowID != a.workflowID ||
				exchange.Turn == 0 || exchange.ResponseIDIssuer != governedResponseIssuer || exchange.ToolCallIDIssuer != governedCallIssuer ||
				exchange.ResponseID == "" || exchange.Model != providerbridge.ApprovedModel || exchange.StatusCode != 200 ||
				exchange.Request.ProfileID != a.providerProfileID || exchange.Request.Method != "POST" ||
				exchange.Request.URL != openrouter.Endpoint || exchange.Request.Host == "" || len(exchange.Request.Body) == 0 {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if exchange.ToolCall == nil {
				continue
			}
			proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: exchange.ToolCall.ID,
				Tool: exchange.ToolCall.Name, Arguments: append(json.RawMessage(nil), exchange.ToolCall.Arguments...)}
			if proposal.ToolCallID == "" || proposal.Tool == "" || len(proposal.Arguments) == 0 {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			key := providerCaptureKey(exchange.ResponseID, proposal.ToolCallID)
			if _, duplicate := exchanges[key]; duplicate {
				return governedCapture{}, errors.New("provider audit reused a response/call identity")
			}
			exchanges[key] = governedCapture{proposal: proposal, response: exchange.ResponseID, call: proposal.ToolCallID,
				sequence: exchange.Turn, generation: currentGeneration, treeDigest: currentTree, exchangeSequence: record.Sequence}
		case "provider_gate_decision":
			providerOrdinal++
			if err := verifyProviderEventID(record, a.conversationID, "decision", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var gateDecision gate.Decision
			if err := unmarshalGovernedJSON(record.Event.Data, &gateDecision); err != nil || gateDecision.PolicyDigest != a.gatePolicyDigest ||
				gateDecision.ResponseID == nil || gateDecision.ToolCallID == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			key := providerCaptureKey(gateDecision.ResponseID.Opaque, gateDecision.ToolCallID)
			capture, ok := exchanges[key]
			if !ok || capture.exchangeSequence >= record.Sequence || gateDecision.ResponseID.Issuer != governedResponseIssuer ||
				gateDecision.Tool != capture.proposal.Tool || gateDecision.Sequence != capture.sequence {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			digest, err := protocol.CanonicalArgumentsDigest(capture.proposal.Tool, capture.proposal.Arguments)
			if err != nil || digest != gateDecision.CanonicalArgumentsDigest {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if _, duplicate := decisions[key]; duplicate {
				return governedCapture{}, errors.New("provider call has more than one durable gate decision")
			}
			decisions[key] = gateDecision
			if gateDecision.Verdict == gate.Deny {
				continue
			}
			if gateDecision.Verdict != gate.Allow || gateDecision.ReasonCode != "policy_rule_allow" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			capture.decision = gateDecision
			capture.decisionSequence = record.Sequence
			capture.decisionHash = record.Hash
			proofs = append(proofs, capture)
		case "provider_tool_result":
			providerOrdinal++
			var resultEvidence providerResultEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &resultEvidence); err != nil || resultEvidence.WorkflowID != a.workflowID ||
				resultEvidence.ConversationID != a.conversationID || resultEvidence.ResponseIDIssuer != governedResponseIssuer ||
				resultEvidence.ToolCallIDIssuer != governedCallIssuer || resultEvidence.GenerationFrom != currentGeneration ||
				resultEvidence.TreeDigestFrom != currentTree || resultEvidence.ResultDigest != digestBytes(resultEvidence.Result) {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if err := verifyProviderEventID(record, a.conversationID, resultEvidence.ResultID, providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			result, err := protocol.DecodeResult(resultEvidence.Result)
			if err != nil || result.ResponseID == nil || result.ResponseID.Issuer != resultEvidence.ResponseIDIssuer || result.ResponseID.Opaque != resultEvidence.ResponseID ||
				result.ToolCallID != resultEvidence.ToolCallID || result.Tool != resultEvidence.Tool ||
				result.Sequence != resultEvidence.Sequence || result.CanonicalArgumentsDigest != resultEvidence.ArgumentDigest ||
				resultEvidence.ResultID != stableProviderResultID(a.workflowID, a.conversationID, resultEvidence.ResponseID, resultEvidence.ToolCallID) ||
				!strings.HasPrefix(record.Event.ID, a.conversationID+":"+resultEvidence.ResultID+":") {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			captureKey := providerCaptureKey(resultEvidence.ResponseID, resultEvidence.ToolCallID)
			capture, captured := exchanges[captureKey]
			decision, decided := decisions[captureKey]
			if !captured || !decided || capture.sequence != resultEvidence.Sequence || capture.proposal.Tool != resultEvidence.Tool ||
				decision.ResponseID == nil || decision.ResponseID.Opaque != resultEvidence.ResponseID || decision.ToolCallID != resultEvidence.ToolCallID ||
				decision.Tool != result.Tool || decision.ReasonCode != result.ReasonCode || decision.PolicyDigest != result.PolicyDigest ||
				decision.CanonicalArgumentsDigest != resultEvidence.ArgumentDigest || decision.Verdict != gate.Verdict(result.Verdict) {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			matchedProofs := 0
			if resultEvidence.TransitionID != "" {
				if resultEvidence.TransitionSeq == 0 || resultEvidence.EffectID == "" || resultEvidence.GenerationTo == currentGeneration ||
					resultEvidence.TreeDigestTo == "" {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				var mutation durableMutationSummaryPayload
				if decodeGovernedResult(result.Output, &mutation) != nil || mutation.Tool != result.Tool || mutation.Outcome != "success" ||
					mutation.Generation.ID != resultEvidence.GenerationTo || mutation.Generation.TreeDigest != resultEvidence.TreeDigestTo ||
					mutation.Transition.ID != resultEvidence.TransitionID || mutation.Transition.Sequence != resultEvidence.TransitionSeq ||
					mutation.EffectID != resultEvidence.EffectID {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				if result.Tool == "bash" && (a.runner == nil || mutation.Command == nil || !mutation.Command.ExitObserved ||
					mutation.Command.CommandContainmentStatus != "not-established" ||
					mutation.Command.CommandRunnerProfile != a.runner.Profile()) {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
			} else if resultEvidence.TransitionSeq != 0 || resultEvidence.EffectID != "" || resultEvidence.GenerationTo != currentGeneration ||
				resultEvidence.TreeDigestTo != currentTree {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if result.Verdict == string(gate.Allow) && result.Tool == "read" {
				var read durableReadSummaryPayload
				if decodeGovernedResult(result.Output, &read) != nil || read.Tool != "read" || read.Generation.ID != currentGeneration ||
					read.Generation.TreeDigest != currentTree || read.Outcome != "success" ||
					read.ContentDigest == "" || read.Size != len(read.Content) || read.ContentDigest != durableContentDigest([]byte(read.Content)) {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
			}
			if result.Verdict == string(gate.Allow) {
				for i := range proofs {
					proof := &proofs[i]
					if proof.response != resultEvidence.ResponseID || proof.call != resultEvidence.ToolCallID || proof.sequence != resultEvidence.Sequence {
						continue
					}
					if proof.decisionSequence >= record.Sequence || proof.proposal.Tool != result.Tool ||
						result.ResponseID.Issuer != governedResponseIssuer || result.PolicyDigest != proof.decision.PolicyDigest ||
						result.ReasonCode != proof.decision.ReasonCode ||
						proof.decision.CanonicalArgumentsDigest != resultEvidence.ArgumentDigest ||
						proof.generation != resultEvidence.GenerationFrom || proof.treeDigest != resultEvidence.TreeDigestFrom || proof.result != nil {
						return governedCapture{}, ErrGovernedSessionEvidence
					}
					copy := resultEvidence
					proof.result = &copy
					matchedProofs++
				}
				if matchedProofs != 1 {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
			} else if result.Verdict != string(gate.Deny) || resultEvidence.TransitionID != "" || len(result.Output) != 0 ||
				resultEvidence.TransitionSeq != 0 || resultEvidence.EffectID != "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			currentGeneration, currentTree = resultEvidence.GenerationTo, resultEvidence.TreeDigestTo
		case "provider_tool_result_unknown", "provider_exchange_unknown":
			providerOrdinal++
			id := "unknown-result"
			if record.Event.Kind == "provider_exchange_unknown" {
				id = "exchange"
			}
			if err := verifyProviderEventID(record, a.conversationID, id, providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			return governedCapture{}, errors.New("provider conversation contains an unresolved UNKNOWN result/exchange")
		}
	}
	var matched governedCapture
	count := 0
	for _, proof := range proofs {
		operation, policyDecision, _, err := a.bindOperation(proof)
		if err == nil && policyDecision.ID == decisionID && operation.Kind != "" {
			matched = proof
			count++
		}
	}
	if count != 1 {
		return governedCapture{}, fmt.Errorf("%w: durable decision ID maps to %d provider captures", ErrGovernedSessionEvidence, count)
	}
	return matched, nil
}

func (a *SessionOperationAuthority) bindOperation(proof governedCapture) (delta.OperationIdentity, delta.PolicyDecision, string, error) {
	operation, err := durableOperationIdentity(proof.proposal, proof.decision, governedCallIssuer)
	if err != nil {
		return delta.OperationIdentity{}, delta.PolicyDecision{}, "", err
	}
	decision, err := durablePolicyDecision(proof.proposal, proof.decision, a.storePolicyDigest, a.metadataPolicyDigest)
	if err != nil {
		return delta.OperationIdentity{}, delta.PolicyDecision{}, "", err
	}
	argumentDigest, err := sessionrepoArgumentDigest(proof.proposal)
	return operation, decision, argumentDigest, err
}

func sessionrepoArgumentDigest(proposal protocol.Proposal) (string, error) {
	var value any
	switch proposal.Tool {
	case "read":
		path, err := durableReadArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		value = struct {
			Path     string `json:"path"`
			MaxBytes int64  `json:"max_bytes"`
		}{path, durableReadMaxBytes}
	case "write":
		path, content, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		value = struct {
			Path          string `json:"path"`
			ContentDigest string `json:"content_digest"`
			ContentBytes  int    `json:"content_bytes"`
		}{path, durableContentDigest(content), len(content)}
	case "edit":
		path, edits, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		replacements := make([]sessionrepo.Replacement, len(edits))
		for i, edit := range edits {
			replacements[i] = sessionrepo.Replacement{OldText: edit.OldText, NewText: edit.NewText}
		}
		value = struct {
			Path  string                    `json:"path"`
			Edits []sessionrepo.Replacement `json:"edits"`
		}{path, replacements}
	case "bash":
		command, err := durableBashArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		value = sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command}}
	default:
		return "", errors.New("unsupported governed session tool")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

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
	if result.Tool == "bash" && (operationEvidence.ViewID != transition.ViewID ||
		operationEvidence.ExecutionContextDigest != transition.ExecutionContextDigest ||
		operationEvidence.CommandContainmentStatus != "not-established" ||
		operationEvidence.CommandRunnerProfile == "" || operationEvidence.ExitCode == nil) {
		return ErrGovernedSessionEvidence
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
	if operationAdmission == nil || operationAdmission.SchemaVersion != "tbound-governed-operation-admission/v1" ||
		operationAdmission.WorkflowID != a.workflowID || operationAdmission.ConversationID != a.conversationID ||
		operationAdmission.ProviderProfileID != a.providerProfileID || operationAdmission.DecisionID != decision.ID ||
		operationAdmission.Operation != operation || operationAdmission.Tool != result.Tool ||
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

func (a *SessionOperationAuthority) NewProviderConversation(config providerbridge.Config) (*providerbridge.Conversation, error) {
	if err := a.requirePreparedSource(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	config.InitialGenerationID, config.InitialTreeDigest = a.initialGeneration, a.initialTreeDigest
	if config.WorkflowID != a.workflowID || config.ConversationID != a.conversationID || config.ProfileID != a.providerProfileID {
		a.mu.Unlock()
		return nil, errors.New("provider conversation identity differs from durable governed session")
	}
	if a.conversation != nil {
		a.mu.Unlock()
		return nil, errors.New("governed provider conversation already exists")
	}
	a.mu.Unlock()
	conversation, err := providerbridge.NewFromEnvironment(config, a.providerJournal)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conversation != nil {
		conversation.Close()
		return nil, errors.New("governed provider conversation raced another constructor")
	}
	a.conversation = conversation
	return conversation, nil
}

// AdmitPrompt must be called by trusted host/session-control before that exact
// prompt is forwarded into Pi. It confirms that the real DurableExecutor tip
// is the verified Store tip and that the D06 source/provenance is still valid;
// providerbridge then durably records user admission before entering history.
func (a *SessionOperationAuthority) AdmitPrompt(conversation *providerbridge.Conversation, durable *DurableExecutor,
	taskID, prompt string) error {
	if err := a.requirePreparedSource(); err != nil {
		return err
	}
	if conversation == nil || durable == nil {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	store, ownedConversation := a.store, a.conversation
	a.mu.Unlock()
	if durable.store != store || conversation != ownedConversation {
		return errors.New("prompt admission conversation/executor is not bound to this retained session Store")
	}
	verified, err := store.Verify()
	if err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	tip := durable.Tip()
	if tip == nil || verified.SealedGeneration != tip.ID() || verified.SealedTreeDigest != tip.TreeDigest() ||
		len(bundle.Generations) == 0 || bundle.Generations[len(bundle.Generations)-1].ID != tip.ID() ||
		bundle.Generations[len(bundle.Generations)-1].TreeDigest != tip.TreeDigest() {
		return errors.New("prompt admission refused because DurableExecutor tip differs from verified Store tip")
	}
	if err := conversation.AdmitPrompt(taskID, prompt); err != nil {
		return fmt.Errorf("persist host-admitted prompt before Pi forwarding: %w", err)
	}
	return nil
}

func (a *SessionOperationAuthority) requirePreparedSource() error {
	if a == nil {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	prepared, store, generation, journal, bound := a.prepared, a.store, a.baseline, a.storeJournal, a.baselineBound
	a.mu.Unlock()
	if !bound || prepared == nil {
		return errors.New("verified D06 source and durable provenance must be bound before provider/tool activity")
	}
	return prepared.VerifySourceBinding(store, generation, journal)
}

func (a *SessionOperationAuthority) requirePreparedJournal() error {
	if a == nil {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	prepared, bound := a.prepared, a.baselineBound
	a.mu.Unlock()
	if !bound || prepared == nil {
		return errors.New("verified D06 source and durable provenance must be bound before operation authority")
	}
	return prepared.VerifyDurableProvenance()
}

func NewGovernedSupervisor(server *ipc.Server, broker *providerbridge.Conversation, authority *SessionOperationAuthority,
	policy gate.Policy, durable *DurableExecutor) (*Supervisor, error) {
	if server == nil || broker == nil || authority == nil || durable == nil || policy.Digest() == "" {
		return nil, errors.New("governed supervisor requires server, provider conversation, session authority, policy and durable executor")
	}
	if err := authority.requirePreparedSource(); err != nil {
		return nil, err
	}
	authority.mu.Lock()
	store, baseline, gateDigest, ownedConversation := authority.store, authority.baseline, authority.gatePolicyDigest, authority.conversation
	authority.mu.Unlock()
	tip := durable.Tip()
	if broker != ownedConversation || policy.Digest() != gateDigest || durable.store != store || baseline == nil || tip == nil ||
		tip.ID() != baseline.ID() || tip.TreeDigest() != baseline.TreeDigest() {
		return nil, errors.New("governed supervisor policy or durable executor tip differs from admitted session profile")
	}
	resultingExecutor, err := providerbridge.NewExecutor(broker, durable)
	if err != nil {
		return nil, err
	}
	return &Supervisor{IPC: server, Broker: broker, Policy: policy, Decisions: broker, Executor: resultingExecutor}, nil
}

// RunGovernedChannels connects the concrete provider bridge to the existing
// supervisor over separate already-created duplex channels. The caller must
// provide the launcher-approved channel role map; this function assigns no FD
// numbers and authenticates no descriptors on its own.
func RunGovernedChannels(ctx context.Context, supervisor *Supervisor, providerChannel io.ReadWriteCloser,
	conversation *providerbridge.Conversation) error {
	if ctx == nil || supervisor == nil || providerChannel == nil || conversation == nil {
		return errors.New("governed channels require context, supervisor, provider duplex and conversation")
	}
	if supervisor.NoEffect != nil || supervisor.Broker != conversation || supervisor.Decisions != conversation {
		return errors.New("governed channel runner requires one owner Conversation as Broker and durable DecisionRecorder")
	}
	if _, ok := supervisor.Executor.(*providerbridge.ResultingExecutor); !ok {
		return errors.New("governed channel runner requires providerbridge.NewExecutor around the real DurableExecutor")
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- providerbridge.Serve(bridgeCtx, providerChannel, conversation) }()
	serveErr := supervisor.Serve(bridgeCtx)
	cancel()
	_ = providerChannel.Close()
	bridgeErr := <-bridgeDone
	conversation.Close()
	return errors.Join(serveErr, bridgeErr)
}

func (a *SessionOperationAuthority) CreateStore(root *os.File, limits workspace.Limits, xattr workspace.XattrVisibilityAttestation,
	binder sessionrepo.Binder) (*sessionlaunch.ManagedStore, error) {
	if a == nil || a.storeJournal == nil || !validBareSHA256(a.storePolicyDigest) || !validBareSHA256(a.metadataPolicyDigest) {
		return nil, errors.New("governed session authority is not configured")
	}
	if root == nil {
		return nil, errors.New("governed Store requires an already-open private root descriptor")
	}
	options := sessionrepo.Options{PolicyDigest: a.storePolicyDigest, MetadataPolicyDigest: a.metadataPolicyDigest,
		Limits: limits, XattrVisibility: xattr, Journal: a.storeJournal, ExposureBinder: binder,
		AuthorizeOperation: a.AuthorizeOperation, VerifyDecision: a.VerifyDecision, VerifySettlement: a.VerifySettlement}
	managed, err := sessionlaunch.CreateStore(root, options)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managedStore != nil {
		_ = managed.Close()
		return nil, errors.New("governed session Store was already created")
	}
	a.managedStore, a.store = managed, managed.Store()
	return managed, nil
}

func governedSessionrepoArgumentDigest(proposal protocol.Proposal) (string, error) {
	var args any
	switch proposal.Tool {
	case "read":
		path, err := durableReadArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		args = struct {
			Path     string `json:"path"`
			MaxBytes int64  `json:"max_bytes"`
		}{path, durableReadMaxBytes}
	case "write":
		path, content, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		args = struct {
			Path          string `json:"path"`
			ContentDigest string `json:"content_digest"`
			ContentBytes  int    `json:"content_bytes"`
		}{path, durableContentDigest(content), len(content)}
	case "edit":
		path, edits, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		replacements := make([]sessionrepo.Replacement, len(edits))
		for i, edit := range edits {
			replacements[i] = sessionrepo.Replacement{OldText: edit.OldText, NewText: edit.NewText}
		}
		args = struct {
			Path  string                    `json:"path"`
			Edits []sessionrepo.Replacement `json:"edits"`
		}{path, replacements}
	case "bash":
		command, err := durableBashArguments(proposal.Arguments)
		if err != nil {
			return "", err
		}
		args = sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command}}
	default:
		return "", errors.New("unsupported durable session tool")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func providerCaptureKey(response, call string) string { return response + "\x00" + call }

func validGovernedViewID(view string) bool {
	if len(view) != len("view-")+48 || !strings.HasPrefix(view, "view-") || strings.ToLower(view) != view {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(view, "view-"))
	return err == nil
}

func validNextGeneration(input, output string) bool {
	if !strings.HasPrefix(input, "g") || !strings.HasPrefix(output, "g") {
		return false
	}
	in, err := strconv.ParseUint(strings.TrimPrefix(input, "g"), 10, 64)
	if err != nil || fmt.Sprintf("g%d", in) != input {
		return false
	}
	out, err := strconv.ParseUint(strings.TrimPrefix(output, "g"), 10, 64)
	if err != nil || fmt.Sprintf("g%d", out) != output {
		return false
	}
	return in != ^uint64(0) && out == in+1
}

// CheckFinalizerOrigin documents the current workflow contract boundary. The
// proposal↔lease receipt can be independently verified, but workflow.Finalizer
// still rejects terminal lease origins before calling its Trust verifier.
func (a *SessionOperationAuthority) CheckFinalizerOrigin(bundle sessionrepo.EvidenceBundle) error {
	if len(bundle.ApprovedDeltaLedger) == 0 {
		return errors.New("publication requires at least one approved mutation")
	}
	terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	if terminal.Operation.Kind == delta.OperationLease {
		if err := a.VerifyOrigin(bundle, terminal); err != nil {
			return err
		}
		return ErrLeaseOriginUnsupported
	}
	if terminal.Operation.Kind != delta.OperationProposalCall {
		return ErrGovernedSessionEvidence
	}
	return a.VerifyOrigin(bundle, terminal)
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func marshalGovernedAuditRecords(records []audit.Record) ([]byte, error) {
	var result bytes.Buffer
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		result.Write(encoded)
		result.WriteByte('\n')
	}
	return result.Bytes(), nil
}

func auditHeadHash(records []audit.Record) string {
	if len(records) == 0 {
		return ""
	}
	return records[len(records)-1].Hash
}

func unmarshalGovernedJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("governed audit event contains trailing JSON")
	}
	return nil
}

func decodeGovernedResult(raw []byte, target any) error { return unmarshalGovernedJSON(raw, target) }

func governedExecutionContextDigest(viewID, treeDigest, leaseID string) string {
	encoded, err := json.Marshal(struct {
		ViewID         string `json:"view_id"`
		ViewTreeDigest string `json:"view_tree_digest"`
		LeaseID        string `json:"lease_id"`
	}{viewID, treeDigest, leaseID})
	if err != nil {
		return ""
	}
	return digestBytes(encoded)
}

func validateToolChangeScope(proposal protocol.Proposal, binding delta.TransitionBinding) error {
	switch proposal.Tool {
	case "write":
		path, _, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return err
		}
		for _, change := range binding.Changes {
			if change.Path != path {
				return ErrGovernedSessionEvidence
			}
		}
	case "edit":
		path, _, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return err
		}
		for _, change := range binding.Changes {
			if change.Path != path {
				return ErrGovernedSessionEvidence
			}
		}
	}
	return nil
}

func sameManifest(a, b delta.TreeManifest) bool { return reflect.DeepEqual(a, b) }
