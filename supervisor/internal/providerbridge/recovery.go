package providerbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"reflect"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
)

// ErrTranscriptReplay identifies a provider journal that cannot be projected
// from the pinned host inputs and exact durable provider evidence.
var ErrTranscriptReplay = errors.New("provider transcript does not match pinned replay inputs")

// RecoveryDisposition describes the next point reached by strict, inert replay.
// None of these values authorize recovery, construction, or another provider
// request. The providerGeneration/tree fields remain at the last released
// provider_tool_result, even if Store evidence has advanced farther.
type RecoveryDisposition string

const (
	DispositionAwaitingHostPrompt       RecoveryDisposition = "awaiting_host_prompt"
	DispositionAwaitingProviderExchange RecoveryDisposition = "awaiting_provider_exchange"
	DispositionAwaitingGateDecision     RecoveryDisposition = "awaiting_gate_decision"
	DispositionPendingProviderResult    RecoveryDisposition = "pending_provider_result"
)

const (
	ProjectionScopeProviderOnly   = "provider_only"
	ProjectionScopeStoreCrosslink = "store_crosslinked"
)

// RecoveryPins contains host-owned inputs required to reproduce the exact
// provider transcript. The prompt text, model, policy and initial generation
// are never inferred from the journal. Model must equal ApprovedModel.
// ProfileID remains a host-pinned label; it does not authenticate an external
// profile artifact or close over all runtime/transport configuration. The
// caller must supply the immutable host profile and its exact prompt inputs.
type RecoveryPins struct {
	Config Config
	Model  string
	Policy gate.Policy
}

// ProviderDecisionBinding is supplied only to a trusted read-only Store
// verifier. It retains the raw proposal arguments for exact authorization
// cross-linking and is not included in TranscriptProjection.
type ProviderDecisionBinding struct {
	WorkflowID             string
	ConversationID         string
	ProfileID              string
	Model                  string
	PolicyDigest           string
	SystemPromptDigest     string
	DeveloperPromptDigest  string
	ProviderSequence       uint64
	ResponseIDIssuer       string
	ResponseID             string
	ToolCallIDIssuer       string
	ToolCallID             string
	Proposal               protocol.Proposal
	GateDecision           gate.Decision
	InputGeneration        string
	InputTreeDigest        string
	ExchangeRecordSequence uint64
	ExchangeRecordHash     string
	DecisionRecordSequence uint64
	DecisionRecordHash     string
}

// ProviderResultBinding binds a released provider result to the exact gate
// decision, result record, output bytes, and provider generation transition.
// Output can contain a read result and is given only to the trusted verifier.
type ProviderResultBinding struct {
	Decision             ProviderDecisionBinding
	ResultRecordSequence uint64
	ResultRecordHash     string
	ResultID             string
	ResultDigest         string
	Result               protocol.Result
	GenerationFrom       string
	GenerationTo         string
	TreeDigestFrom       string
	TreeDigestTo         string
	TransitionID         string
	TransitionSequence   uint64
	EffectID             string
}

// StoreReplayVerifier cross-links provider decisions and released results to
// concrete durable Store evidence. Implementations must be read-only. A nil
// verifier permits provider-only DENY projection, but any ALLOW decision
// requires both methods. This interface itself is not authority: correctness
// depends on the caller supplying the trusted Store verifier.
type StoreReplayVerifier interface {
	VerifyDecision(ProviderDecisionBinding) error
	VerifyResult(ProviderResultBinding) error
}

// TranscriptProjection contains only bounded status, identity and commitment
// data. It never exposes replayable messages, prompts, provider responses,
// proposals, arguments, or tool output. Digests can reveal equality and may be
// guessable for low-entropy inputs; they are not confidentiality. The local
// unkeyed journal chain does not authenticate against privileged rewrite or
// truncation. This is not recovery authorization, host/source truth, a
// checkpoint, or a resumable conversation.
//
// Proposal correlation is deterministically replayed from the captured call;
// providerbridge does not journal the adapter's actual proposal bytes, so the
// projection cannot attest those exact IPC bytes.
type TranscriptProjection struct {
	SchemaVersion              string              `json:"schema_version"`
	VerificationScope          string              `json:"verification_scope"`
	WorkflowID                 string              `json:"workflow_id"`
	ConversationID             string              `json:"conversation_id"`
	ProfileID                  string              `json:"profile_id"`
	Model                      string              `json:"model"`
	PolicyDigest               string              `json:"policy_digest"`
	SystemPromptDigest         string              `json:"system_prompt_digest"`
	DeveloperPromptDigest      string              `json:"developer_prompt_digest"`
	InitialGenerationID        string              `json:"initial_generation_id"`
	InitialTreeDigest          string              `json:"initial_tree_digest"`
	ProviderGenerationID       string              `json:"provider_generation_id"`
	ProviderTreeDigest         string              `json:"provider_tree_digest"`
	ProviderSequence           uint64              `json:"provider_sequence"`
	EventCount                 uint64              `json:"event_count"`
	ProviderJournalRecordCount uint64              `json:"provider_journal_record_count"`
	ExchangeCount              uint64              `json:"exchange_count"`
	CapturedCallCount          uint64              `json:"captured_call_count"`
	DecisionCount              uint64              `json:"decision_count"`
	ResultCount                uint64              `json:"result_count"`
	ProviderJournalHead        string              `json:"provider_journal_head"`
	HistoryDigest              string              `json:"history_digest"`
	Disposition                RecoveryDisposition `json:"disposition"`
	ProjectionDigest           string              `json:"projection_digest"`
}

const transcriptProjectionSchema = "tbound-provider-transcript-projection/v1"

type replayDecision struct {
	binding ProviderDecisionBinding
	call    openrouter.ToolCall
}

type replayState uint8

const (
	replayAwaitPrompt replayState = iota + 1
	replayAwaitExchange
	replayAwaitDecision
	replayAwaitResult
)

// ReplayTranscript verifies the protected provider journal and deterministically
// reconstructs its current provider history in memory. It does not return that
// history, construct a Conversation, infer budgets/deadlines, or authorize a
// restart. The held Journal supplies a locally hash-verified prefix; its
// unkeyed chain does not establish authenticity against privileged rewrite or
// truncation. The Store verifier is called for each decision when supplied and
// is mandatory for ALLOW.
func ReplayTranscript(journal *audit.Journal, pins RecoveryPins, verifier StoreReplayVerifier) (TranscriptProjection, error) {
	if journal == nil {
		return TranscriptProjection{}, replayError("provider journal is unavailable")
	}
	if err := validateRecoveryPins(pins); err != nil {
		return TranscriptProjection{}, err
	}
	trace, err := journal.Trace()
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	if len(trace.Effects) != 0 {
		return TranscriptProjection{}, replayError("provider journal unexpectedly contains effect traces")
	}

	cfg := pins.Config
	model := pins.Model
	policy := pins.Policy
	stream, err := protocol.NewStream(callIssuer, responseIssuer)
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	system, developer := cfg.SystemPrompt, cfg.DeveloperPrompt
	history := []openrouter.Message{
		{Role: openrouter.System, Content: &system},
		{Role: openrouter.Developer, Content: &developer},
	}
	currentGeneration, currentTree := cfg.InitialGenerationID, cfg.InitialTreeDigest
	state := replayAwaitPrompt
	var pending *replayDecision
	var exchangeRecord audit.Record
	var exchangeSequence uint64
	var ordinal, providerSequence, exchangeCount, callCount, decisionCount, resultCount uint64
	var storeDecisionChecks uint64
	responses := make(map[string]struct{})
	calls := make(map[string]struct{})
	transitions := make(map[string]struct{})
	effects := make(map[string]struct{})
	var transitionSequence uint64

	for _, record := range trace.Records {
		if record.Sequence != ordinal+1 || record.Event.Outcome != "" || ordinal == math.MaxUint64 {
			return TranscriptProjection{}, replayError("provider journal sequence or event outcome is invalid")
		}
		ordinal++
		switch record.Event.Kind {
		case "provider_user_admitted":
			if state != replayAwaitPrompt || pending != nil {
				return TranscriptProjection{}, replayError("host prompt is out of order")
			}
			var admitted promptEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &admitted); err != nil ||
				admitted.WorkflowID != cfg.WorkflowID || admitted.ConversationID != cfg.ConversationID ||
				admitted.Generation != currentGeneration || !validText(admitted.TaskID, MaxTaskIDBytes) ||
				!validPromptText(admitted.Prompt, MaxPromptBytes) ||
				verifyReplayEventID(record, cfg.ConversationID, "user", ordinal) != nil {
				return TranscriptProjection{}, replayError("host prompt evidence is invalid or unpinned")
			}
			content := admitted.Prompt
			if err := appendReplayHistory(&history, model, openrouter.Message{Role: openrouter.User, Content: &content}); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			state = replayAwaitExchange

		case "provider_exchange":
			if state != replayAwaitExchange || providerSequence >= MaxTranscriptTurns || exchangeCount == math.MaxUint64 {
				return TranscriptProjection{}, replayError("provider exchange is out of order or exceeds sequence bounds")
			}
			var evidence exchangeEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				evidence.WorkflowID != cfg.WorkflowID || evidence.Turn != providerSequence+1 ||
				evidence.ResponseIDIssuer != responseIssuer || evidence.ToolCallIDIssuer != callIssuer ||
				evidence.StatusCode != 200 || evidence.Model != model ||
				verifyReplayEventID(record, cfg.ConversationID, "exchange", ordinal) != nil {
				return TranscriptProjection{}, replayError("provider exchange metadata or event identity is invalid")
			}
			if err := verifyReplayRequest(evidence.Request, cfg.ProfileID, model, history); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			captured, err := openrouter.CaptureSSE(bytes.NewReader(evidence.Response))
			if err != nil || captured.Model() != model || captured.ResponseID() != evidence.ResponseID {
				return TranscriptProjection{}, replayError("captured response does not parse to the recorded provider identity")
			}
			if !validText(captured.ResponseID(), MaxIdentityBytes) {
				return TranscriptProjection{}, replayError("provider response ID is invalid")
			}
			if _, exists := responses[captured.ResponseID()]; exists {
				return TranscriptProjection{}, replayError("provider response ID was reused")
			}
			responses[captured.ResponseID()] = struct{}{}
			var priorCalls []openrouter.PriorToolCall
			toolCall, hasCall := captured.ToolCall()
			if hasCall {
				if !sameReplayToolCallProjection(evidence.ToolCall, toolCall) || !validText(toolCall.ID, MaxIdentityBytes) ||
					!validTool(toolCall.Name) || len(toolCall.RawArguments) == 0 {
					return TranscriptProjection{}, replayError("captured tool call differs from its durable projection")
				}
				if _, exists := calls[toolCall.ID]; exists {
					return TranscriptProjection{}, replayError("provider tool-call ID was reused")
				}
				calls[toolCall.ID] = struct{}{}
				trusted, err := captured.TrustedCapture(callIssuer, responseIssuer, currentGeneration, providerSequence+1)
				if err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
				}
				registered := stream.Capture(trusted)
				if !registered.Accepted || registered.StreamClosed || registered.CaptureOrdinal != callCount+1 {
					return TranscriptProjection{}, replayError("protocol stream rejected a replayed provider call")
				}
				priorCalls = []openrouter.PriorToolCall{{ID: toolCall.ID, Name: toolCall.Name, RawArguments: append(json.RawMessage(nil), toolCall.RawArguments...)}}
				callCount++
			} else if evidence.ToolCall != nil {
				return TranscriptProjection{}, replayError("durable exchange claims a tool call absent from the provider response")
			}
			assistantText := captured.AssistantText()
			if err := appendReplayHistory(&history, model, openrouter.Message{Role: openrouter.Assistant, Content: &assistantText, ToolCalls: priorCalls}); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			providerSequence++
			exchangeCount++
			exchangeRecord = record
			if hasCall {
				exchangeSequence = providerSequence
				pending = &replayDecision{call: toolCall}
				state = replayAwaitDecision
			} else {
				state = replayAwaitPrompt
			}

		case "provider_gate_decision":
			if state != replayAwaitDecision || pending == nil || decisionCount == math.MaxUint64 {
				return TranscriptProjection{}, replayError("gate decision has no unique preceding provider call")
			}
			proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion,
				ToolCallID: pending.call.ID, Tool: pending.call.Name,
				Arguments: append(json.RawMessage(nil), pending.call.RawArguments...)}
			proposalBytes, err := protocol.MarshalProposal(proposal)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			matched := stream.Propose(proposalBytes)
			if !matched.Accepted || matched.StreamClosed || matched.Proposal == nil || matched.BrokerCapture == nil {
				return TranscriptProjection{}, replayError("protocol stream could not reproduce the recorded proposal correlation")
			}
			expected := gate.Evaluate(proposal, matched, policy)
			var decision gate.Decision
			if err := unmarshalCanonicalEvent(record.Event.Data, &decision); err != nil ||
				!reflect.DeepEqual(decision, expected) || record.Sequence <= exchangeRecord.Sequence ||
				verifyReplayEventID(record, cfg.ConversationID, "decision", ordinal) != nil {
				return TranscriptProjection{}, replayError("durable gate decision differs from pinned deterministic evaluation")
			}
			binding := ProviderDecisionBinding{
				WorkflowID: cfg.WorkflowID, ConversationID: cfg.ConversationID, ProfileID: cfg.ProfileID,
				Model: model, PolicyDigest: policy.Digest(),
				SystemPromptDigest: digestString(cfg.SystemPrompt), DeveloperPromptDigest: digestString(cfg.DeveloperPrompt),
				ProviderSequence: exchangeSequence, ResponseIDIssuer: responseIssuer,
				ResponseID: capturedResponseID(exchangeRecord), ToolCallIDIssuer: callIssuer,
				ToolCallID: pending.call.ID, Proposal: cloneReplayProposal(proposal), GateDecision: cloneGateDecision(decision),
				InputGeneration: currentGeneration, InputTreeDigest: currentTree,
				ExchangeRecordSequence: exchangeRecord.Sequence, ExchangeRecordHash: exchangeRecord.Hash,
				DecisionRecordSequence: record.Sequence, DecisionRecordHash: record.Hash,
			}
			if decision.Verdict == gate.Allow && !hasReplayVerifier(verifier) {
				return TranscriptProjection{}, replayError("ALLOW decision requires a concrete read-only Store verifier")
			}
			if hasReplayVerifier(verifier) {
				if err := verifier.VerifyDecision(cloneDecisionBinding(binding)); err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify Store decision cross-link: %w", err))
				}
				storeDecisionChecks++
			}
			pending.binding = binding
			state = replayAwaitResult
			decisionCount++

		case "provider_tool_result":
			if state != replayAwaitResult || pending == nil || resultCount == math.MaxUint64 {
				return TranscriptProjection{}, replayError("provider result has no unique preceding gate decision")
			}
			var evidence resultEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				record.Sequence <= pending.binding.DecisionRecordSequence ||
				verifyReplayEventID(record, cfg.ConversationID, evidence.ResultID, ordinal) != nil {
				return TranscriptProjection{}, replayError("provider result record is malformed or out of order")
			}
			if err := validateReplayResult(evidence, *pending, cfg, currentGeneration, currentTree,
				&transitionSequence, transitions, effects); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			decoded, err := protocol.DecodeResult(evidence.Result)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			if pending.binding.GateDecision.Verdict == gate.Allow {
				if !hasReplayVerifier(verifier) {
					return TranscriptProjection{}, replayError("released ALLOW result requires a concrete read-only Store verifier")
				}
				binding := ProviderResultBinding{
					Decision: cloneDecisionBinding(pending.binding), ResultRecordSequence: record.Sequence,
					ResultRecordHash: record.Hash, ResultID: evidence.ResultID, ResultDigest: evidence.ResultDigest,
					Result: cloneProtocolResult(decoded), GenerationFrom: evidence.GenerationFrom,
					GenerationTo: evidence.GenerationTo, TreeDigestFrom: evidence.TreeDigestFrom,
					TreeDigestTo: evidence.TreeDigestTo, TransitionID: evidence.TransitionID,
					TransitionSequence: evidence.TransitionSeq, EffectID: evidence.EffectID,
				}
				if err := verifier.VerifyResult(binding); err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify Store result cross-link: %w", err))
				}
			}
			resultContent := string(evidence.Result)
			message := openrouter.Message{Role: openrouter.Tool, ToolCallID: evidence.ToolCallID, Content: &resultContent}
			if err := appendReplayHistory(&history, model, message); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			currentGeneration, currentTree = evidence.GenerationTo, evidence.TreeDigestTo
			pending = nil
			state = replayAwaitExchange
			resultCount++

		case "provider_tool_result_unknown", "provider_exchange_unknown":
			return TranscriptProjection{}, replayError("provider journal contains an unresolved UNKNOWN provider exchange or effect result")

		default:
			return TranscriptProjection{}, replayError("provider journal contains an unsupported event kind")
		}
	}

	request, err := openrouter.BuildRequest(model, history)
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	projection := TranscriptProjection{
		SchemaVersion:     transcriptProjectionSchema,
		VerificationScope: ProjectionScopeProviderOnly,
		WorkflowID:        cfg.WorkflowID, ConversationID: cfg.ConversationID, ProfileID: cfg.ProfileID,
		Model: model, PolicyDigest: policy.Digest(),
		SystemPromptDigest: digestString(cfg.SystemPrompt), DeveloperPromptDigest: digestString(cfg.DeveloperPrompt),
		InitialGenerationID: cfg.InitialGenerationID, InitialTreeDigest: cfg.InitialTreeDigest,
		ProviderGenerationID: currentGeneration, ProviderTreeDigest: currentTree,
		ProviderSequence: providerSequence, EventCount: ordinal, ProviderJournalRecordCount: uint64(len(trace.Records)), ExchangeCount: exchangeCount,
		CapturedCallCount: callCount, DecisionCount: decisionCount, ResultCount: resultCount,
		HistoryDigest: digestDomain("tbound/provider-history/v1", request.Body()), Disposition: dispositionForReplayState(state),
	}
	if storeDecisionChecks > 0 {
		projection.VerificationScope = ProjectionScopeStoreCrosslink
	}
	if len(trace.Records) > 0 {
		projection.ProviderJournalHead = trace.Records[len(trace.Records)-1].Hash
	}
	projection.ProjectionDigest = transcriptProjectionDigest(projection)
	return projection, nil
}

func validateRecoveryPins(pins RecoveryPins) error {
	cfg := pins.Config
	if pins.Model != ApprovedModel || pins.Policy.Digest() == "" ||
		!validText(cfg.ConversationID, MaxIdentityBytes) || !validText(cfg.WorkflowID, MaxIdentityBytes) ||
		!validText(cfg.ProfileID, MaxIdentityBytes) || !validText(cfg.InitialGenerationID, MaxIdentityBytes) ||
		!validReplayTreeDigest(cfg.InitialTreeDigest) ||
		!validPromptText(cfg.SystemPrompt, openrouter.MaxAssistantTextBytes) ||
		!validPromptText(cfg.DeveloperPrompt, openrouter.MaxAssistantTextBytes) {
		return replayError("recovery pins do not satisfy the fixed host profile")
	}
	return nil
}

func verifyReplayRequest(request requestEvidence, profileID, model string, history []openrouter.Message) error {
	built, err := openrouter.BuildRequest(model, history)
	if err != nil {
		return err
	}
	endpoint, _ := url.Parse(openrouter.Endpoint)
	wantHeaders := httpHeaders{
		"Accept":       {"text/event-stream"},
		"Content-Type": {"application/json"},
	}
	if request.ProfileID != profileID || request.Method != "POST" || request.URL != openrouter.Endpoint ||
		request.Host != endpoint.Host || request.ContentLength != int64(len(built.Body())) ||
		!reflect.DeepEqual(request.Headers, wantHeaders) || !bytes.Equal(request.Body, built.Body()) {
		return errors.New("recorded provider request does not byte-match the pinned broker history and fixed HTTP profile")
	}
	return nil
}

// sameReplayToolCallProjection compares the redundant event projection using
// the same JSON encoder path as the journal writer. RawMessage fields are
// compacted and HTML-escaped when nested by encoding/json, so comparing their
// in-memory bytes with CaptureSSE's original argument bytes would reject valid
// captures containing characters such as >, <, or &. The response remains the
// source of truth: expected is built from CaptureSSE output, and replayed
// proposals are likewise derived from that parsed capture.
func sameReplayToolCallProjection(recorded *ToolCallProjection, captured openrouter.ToolCall) bool {
	if recorded == nil {
		return false
	}
	expected := ToolCallProjection{
		ID:        captured.ID,
		Name:      captured.Name,
		Arguments: append(json.RawMessage(nil), captured.RawArguments...),
	}
	want, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	got, err := json.Marshal(recorded)
	return err == nil && bytes.Equal(got, want)
}

func validateReplayResult(evidence resultEvidence, pending replayDecision, cfg Config,
	currentGeneration, currentTree string, lastTransition *uint64, transitions, effects map[string]struct{}) error {
	decision := pending.binding.GateDecision
	call := pending.call
	if evidence.WorkflowID != cfg.WorkflowID || evidence.ConversationID != cfg.ConversationID ||
		evidence.ResponseIDIssuer != responseIssuer || evidence.ToolCallIDIssuer != callIssuer ||
		evidence.ResponseID != pending.binding.ResponseID || evidence.ToolCallID != call.ID ||
		evidence.Tool != call.Name || evidence.ArgumentDigest != decision.CanonicalArgumentsDigest ||
		evidence.Sequence != pending.binding.ProviderSequence || evidence.GenerationFrom != currentGeneration ||
		evidence.TreeDigestFrom != currentTree || evidence.ResultID != stableResultID(cfg.WorkflowID, cfg.ConversationID, evidence.ResponseID, evidence.ToolCallID) ||
		evidence.ResultDigest != digestBytes(evidence.Result) || len(evidence.Result) == 0 {
		return errors.New("provider result does not match its captured call and input provider tip")
	}
	decoded, err := protocol.DecodeResult(evidence.Result)
	if err != nil {
		return err
	}
	canonical, err := protocol.MarshalResult(decoded)
	if err != nil || !bytes.Equal(canonical, evidence.Result) {
		return errors.New("provider result is not the exact canonical protocol result")
	}
	wantResult := resultFor(pending.binding.Proposal, decision, decoded.Output)
	if !reflect.DeepEqual(decoded, wantResult) {
		return errors.New("protocol result fields differ from the exact recorded gate decision")
	}
	if decision.Verdict == gate.Deny {
		if len(decoded.Output) != 0 || evidence.GenerationTo != currentGeneration || evidence.TreeDigestTo != currentTree ||
			evidence.TransitionID != "" || evidence.TransitionSeq != 0 || evidence.EffectID != "" {
			return errors.New("DENY result contains output, Store evidence, or a generation change")
		}
	} else if decision.Verdict == gate.Allow {
		nextGeneration, nextTree, transitionID, transitionSequence, effectID, err := validateExecutorOutput(
			call.Name, decoded.Output, currentGeneration, currentTree)
		if err != nil {
			return err
		}
		if evidence.GenerationTo != nextGeneration || evidence.TreeDigestTo != nextTree || !validReplayTreeDigest(nextTree) ||
			evidence.TransitionID != transitionID || evidence.TransitionSeq != transitionSequence || evidence.EffectID != effectID {
			return errors.New("provider result summary differs from its generation and effect envelope")
		}
		if transitionSequence != 0 {
			if transitionSequence <= *lastTransition {
				return errors.New("provider mutation transition sequence is invalid")
			}
			if _, exists := transitions[transitionID]; exists {
				return errors.New("provider mutation transition ID was reused")
			}
			if _, exists := effects[effectID]; exists {
				return errors.New("provider mutation effect ID was reused")
			}
			transitions[transitionID] = struct{}{}
			effects[effectID] = struct{}{}
			*lastTransition = transitionSequence
		}
	} else {
		return errors.New("provider gate decision has an unsupported verdict")
	}
	if evidence.GenerationTo == "" || evidence.TreeDigestTo == "" {
		return errors.New("provider result has no output generation commitment")
	}
	return nil
}

func appendReplayHistory(history *[]openrouter.Message, model string, message openrouter.Message) error {
	proposed := append(cloneMessages(*history), message)
	if len(proposed) > MaxTranscriptTurns*3+2 {
		return errors.New("provider replay message budget exhausted")
	}
	request, err := openrouter.BuildRequest(model, proposed)
	if err != nil {
		return err
	}
	if len(request.Body()) > MaxTranscriptBytes {
		return errors.New("provider replay transcript byte budget exhausted")
	}
	*history = proposed
	return nil
}

func unmarshalCanonicalEvent(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("provider evidence contains trailing JSON")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, raw) {
		return errors.New("provider evidence is not in the exact durable JSON encoding")
	}
	return nil
}

func verifyReplayEventID(record audit.Record, conversationID, suffix string, ordinal uint64) error {
	want := fmt.Sprintf("%s:%s:%d", conversationID, suffix, ordinal)
	if record.Event.ID != want {
		return errors.New("provider audit event ID/ordinal does not match the pinned conversation")
	}
	return nil
}

func dispositionForReplayState(state replayState) RecoveryDisposition {
	switch state {
	case replayAwaitPrompt:
		return DispositionAwaitingHostPrompt
	case replayAwaitExchange:
		return DispositionAwaitingProviderExchange
	case replayAwaitDecision:
		return DispositionAwaitingGateDecision
	default:
		return DispositionPendingProviderResult
	}
}

func capturedResponseID(record audit.Record) string {
	var evidence exchangeEvidence
	if json.Unmarshal(record.Event.Data, &evidence) != nil {
		return ""
	}
	return evidence.ResponseID
}

func cloneReplayProposal(proposal protocol.Proposal) protocol.Proposal {
	proposal.Arguments = append(json.RawMessage(nil), proposal.Arguments...)
	return proposal
}

func cloneGateDecision(decision gate.Decision) gate.Decision {
	if decision.ResponseID != nil {
		id := *decision.ResponseID
		decision.ResponseID = &id
	}
	return decision
}

func cloneDecisionBinding(binding ProviderDecisionBinding) ProviderDecisionBinding {
	binding.Proposal = cloneReplayProposal(binding.Proposal)
	binding.GateDecision = cloneGateDecision(binding.GateDecision)
	return binding
}

func cloneProtocolResult(result protocol.Result) protocol.Result {
	result.Output = append(json.RawMessage(nil), result.Output...)
	if result.ResponseID != nil {
		id := *result.ResponseID
		result.ResponseID = &id
	}
	return result
}

func hasReplayVerifier(verifier StoreReplayVerifier) bool {
	return verifier != nil && !reflectNil(verifier)
}

func digestString(value string) string { return digestBytes([]byte(value)) }

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func digestDomain(domain string, value []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(value)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func validReplayTreeDigest(value string) bool {
	prefix := delta.TreeDigestProfile
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	encoded := value[len(prefix):]
	if encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

// VerifyTranscriptProjectionCommitment checks the domain-separated digest over
// the complete canonical projection with ProjectionDigest set to the empty
// string. The digest commits to identity, pinned policy/prompts, counts, tip,
// head, history digest and disposition; it adds no external authenticity.
func VerifyTranscriptProjectionCommitment(projection TranscriptProjection) bool {
	if projection.SchemaVersion != transcriptProjectionSchema || projection.ProjectionDigest == "" {
		return false
	}
	return projection.ProjectionDigest == transcriptProjectionDigest(projection)
}

func transcriptProjectionDigest(projection TranscriptProjection) string {
	projection.ProjectionDigest = ""
	encoded, err := json.Marshal(projection)
	if err != nil {
		return ""
	}
	return digestDomain(transcriptProjectionSchema, encoded)
}

func replayError(message string) error { return fmt.Errorf("%w: %s", ErrTranscriptReplay, message) }
