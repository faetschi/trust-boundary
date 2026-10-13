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
	runner               governedCommandRunner
	conversation         *providerbridge.Conversation
	operationProofs      map[delta.OperationIdentity]governedOperationProof
	// requireEvidencedBashContainment makes VerifyOrigin require positive,
	// evidenced Bash containment (a registered signed command-cell profile and
	// settlement) instead of the development "not-established" marker. The
	// production composition sets it; development/E05 tests leave it false.
	requireEvidencedBashContainment bool
}

// governedCommandRunner is the authority's measured command-settlement seam. It
// is deliberately narrower than sessionrepo.CommandRunner: the authority only
// registers the authenticated Bash view/lease and re-verifies the one-use
// settlement receipt. sessionlaunch.DevelopmentCommandRunner and the registered
// signed D09 Bash command-cell runner both satisfy it.
type governedCommandRunner interface {
	Profile() string
	RegisterLease(viewID, leaseID string) error
	VerifySettlement(settlement sessionrepo.CommandSettlement, viewID, leaseID string) error
}

type governedBashEvidenceVerifier interface {
	VerifyProductionBashEvidence(context.Context, sessionrepo.OperationEvidence) error
}

type governedBashEvidenceOriginVerifier interface {
	VerifyProductionBashEvidenceWithOrigin(context.Context, sessionrepo.EvidenceBundle, delta.OperationIdentity,
		delta.Transition, sessionrepo.OperationEvidence) error
}

func NewSessionOperationAuthority(storeJournal, providerJournal *audit.Journal, workflowID, conversationID, providerProfileID string, gatePolicy gate.Policy,
	storePolicyDigest, metadataPolicyDigest string, runner governedCommandRunner) (*SessionOperationAuthority, error) {
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
	type loopResult struct {
		provider bool
		err      error
	}
	done := make(chan loopResult, 2)
	go func() {
		done <- loopResult{provider: true, err: providerbridge.Serve(bridgeCtx, providerChannel, conversation)}
	}()
	go func() { done <- loopResult{err: supervisor.Serve(bridgeCtx)} }()
	first := <-done
	cancel()
	_ = providerChannel.Close()
	second := <-done
	var serveErr, bridgeErr error
	for _, result := range []loopResult{first, second} {
		if result.provider {
			bridgeErr = result.err
		} else {
			serveErr = result.err
		}
	}
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

// ResolveTerminalLeaseOrigin authenticates the originating proposal-call
// identity of a terminal command lease from the durable session ledger. The
// association is the proposal-call transition whose output generation/tree is
// exactly the lease's input generation/tree, re-verified through VerifyOrigin;
// a caller-supplied origin string is never accepted.
func (a *SessionOperationAuthority) ResolveTerminalLeaseOrigin(ctx context.Context, bundle sessionrepo.EvidenceBundle, terminal delta.Transition) (delta.OperationIdentity, error) {
	if a == nil || ctx == nil {
		return delta.OperationIdentity{}, ErrGovernedSessionEvidence
	}
	if err := ctx.Err(); err != nil {
		return delta.OperationIdentity{}, err
	}
	if terminal.Operation.Kind != delta.OperationLease || terminal.Tool != "bash" || terminal.InputGeneration == "" ||
		terminal.OutputGeneration == "" || terminal.InputTreeDigest == "" || terminal.OutputTreeDigest == "" ||
		len(bundle.ApprovedDeltaLedger) == 0 || !reflect.DeepEqual(bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1], terminal) {
		return delta.OperationIdentity{}, ErrGovernedSessionEvidence
	}
	// Authenticate the terminal Bash lease itself, not the preceding edit/write
	// whose output happens to equal this lease's input generation. VerifyOrigin
	// checks the exact provider response/call, canonical arguments, gate decision,
	// command lease/view/context, Store authorization receipt, effect intent/outcome,
	// Bash result, and full transition generation/tree binding.
	if err := a.VerifyOrigin(bundle, terminal); err != nil {
		return delta.OperationIdentity{}, err
	}
	providerTrace, err := a.providerJournal.Trace()
	if err != nil {
		return delta.OperationIdentity{}, err
	}
	proof, result, err := a.providerResultForTransition(providerTrace, terminal)
	if err != nil || proof.proposal.Tool != "bash" || proof.proposal.ToolCallID == "" ||
		result.Tool != "bash" || result.GenerationFrom != terminal.InputGeneration || result.GenerationTo != terminal.OutputGeneration ||
		result.TreeDigestFrom != terminal.InputTreeDigest || result.TreeDigestTo != terminal.OutputTreeDigest {
		return delta.OperationIdentity{}, errors.Join(ErrGovernedSessionEvidence, err)
	}
	operation, _, _, err := a.bindOperation(proof)
	if err != nil || operation != terminal.Operation {
		return delta.OperationIdentity{}, errors.Join(ErrGovernedSessionEvidence, err)
	}
	proposalID, err := durableTrustedIdentity("proposal", proof.proposal, proof.decision)
	if err != nil {
		return delta.OperationIdentity{}, err
	}
	return delta.OperationIdentity{Kind: delta.OperationProposalCall, ProposalID: proposalID,
		CallIssuer: governedCallIssuer, CallID: proof.proposal.ToolCallID}, nil
}

// CheckFinalizerOrigin authenticates the final approved operation and, for a
// command lease, resolves the exact terminal Bash provider call. It returns no
// caller-controlled origin string.
func (a *SessionOperationAuthority) CheckFinalizerOrigin(bundle sessionrepo.EvidenceBundle) error {
	if len(bundle.ApprovedDeltaLedger) == 0 {
		return errors.New("publication requires at least one approved mutation")
	}
	terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	if terminal.Operation.Kind == delta.OperationLease {
		if err := a.VerifyOrigin(bundle, terminal); err != nil {
			return err
		}
		_, err := a.ResolveTerminalLeaseOrigin(context.Background(), bundle, terminal)
		return err
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
