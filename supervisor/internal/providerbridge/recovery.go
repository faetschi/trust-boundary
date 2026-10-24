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
	"tbound/supervisor/internal/broker"
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
	DispositionRetired                  RecoveryDisposition = "retired"
	DispositionRetiredUnexecuted        RecoveryDisposition = "retired_unexecuted"
)

const (
	retirementEventKind = "provider_lineage_retired"
	retirementKind      = "host_unleased_before_gate/v1"
	idleRetirementKind  = "host_idle_lineage_retired/v1"
)

const (
	ProjectionScopeProviderOnly   = "provider_only"
	ProjectionScopeStoreCrosslink = "store_crosslinked"
)

// RecoveryPins contains host-owned inputs required to reproduce the exact
// provider transcript. The prompt text, model, policy and initial generation
// are never inferred from the journal. Model must equal the model of Config.Route (ApprovedModel on the engineering route).
// ProfileID remains a host-pinned label; it does not authenticate an external
// profile artifact or close over all runtime/transport configuration. The
// caller must supply the immutable host profile and its exact prompt inputs.
type RecoveryPins struct {
	Config                      Config
	Model                       string
	Policy                      gate.Policy
	PreviousOwnerEpoch          uint64
	RetirementOwnerEpoch        uint64
	CheckpointRecordCount       uint64
	CheckpointRecordHead        string
	RetirementVerifier          UnleasedProviderCallRetirementVerifier
	IdleRetirementVerifier      IdleProviderLineageRetirementVerifier
	RequireHostCallReservations bool
	// ImportedPrefixDigest pins the provider_history_imported record that must open the journal. Empty
	// means the conversation has no predecessor and the journal must not contain that record.
	ImportedPrefixDigest string
}

// ProviderCallRetirementBinding is the content-free identity of one captured
// call that is retired before any gate decision, Store authorization, effect,
// or lease. The verifier must establish absence of all matching authority and
// effect facts from the concrete retained Store/command records.
type ProviderCallRetirementBinding struct {
	WorkflowID             string
	ConversationID         string
	ProfileID              string
	OwnerEpoch             uint64
	RetirementKind         string
	ProviderSequence       uint64
	ResponseIDIssuer       string
	ResponseID             string
	ToolCallIDIssuer       string
	ToolCallID             string
	ToolName               string
	ArgumentsDigest        string
	ExchangeRecordSequence uint64
	ExchangeRecordHash     string
	PreviousRecordSequence uint64
	PreviousRecordHash     string
}

// UnleasedProviderCallRetirementVerifier is a concrete host-only join. It must
// reject any prior Store authorization, effect, or command lease for the call.
type UnleasedProviderCallRetirementVerifier interface {
	VerifyUnleasedProviderCallRetirement(ProviderCallRetirementBinding) error
}

// IdleProviderLineageRetirementVerifier proves that a transcript with no
// captured pending call has no unresolved provider, Store, or D09 lease state.
// The concrete host implementation is read-only and separately relies on the
// owner fence/drain proof before allowing this transition.
type IdleProviderLineageRetirementVerifier interface {
	VerifyIdleProviderLineageRetirement(workflowID, conversationID string, providerSequence,
		previousOwnerEpoch, retirementOwnerEpoch uint64) error
}

type lineageRetirementEvidence struct {
	SchemaVersion          string `json:"schema_version"`
	WorkflowID             string `json:"workflow_id"`
	ConversationID         string `json:"conversation_id"`
	OwnerEpoch             uint64 `json:"owner_epoch"`
	PreviousOwnerEpoch     uint64 `json:"previous_owner_epoch"`
	RetirementKind         string `json:"retirement_kind"`
	ProviderSequence       uint64 `json:"provider_sequence"`
	ResponseIDIssuer       string `json:"response_id_issuer,omitempty"`
	ResponseID             string `json:"response_id,omitempty"`
	ToolCallIDIssuer       string `json:"tool_call_id_issuer,omitempty"`
	ToolCallID             string `json:"tool_call_id,omitempty"`
	ToolName               string `json:"tool_name,omitempty"`
	ArgumentsDigest        string `json:"arguments_digest,omitempty"`
	ExchangeRecordSequence uint64 `json:"exchange_record_sequence,omitempty"`
	ExchangeRecordHash     string `json:"exchange_record_hash,omitempty"`
	PreviousRecordSequence uint64 `json:"previous_record_sequence"`
	PreviousRecordHash     string `json:"previous_record_hash,omitempty"`
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

// ProviderAdmissionDenialBinding binds the released DENY Result of an ALLOWed operation whose settled
// change delta admission refused to the gate decision, the provider_admission_denied record and the
// reason code. The provider generation does not move and no transition exists. The verifier must find
// the Store effect EffectID holding a denied admission with exactly this reason and no promotion.
type ProviderAdmissionDenialBinding struct {
	Decision             ProviderDecisionBinding
	ResultRecordSequence uint64
	ResultRecordHash     string
	ResultID             string
	ResultDigest         string
	Result               protocol.Result
	EffectID             string
	ReasonCode           string
}

// StoreReplayVerifier cross-links provider decisions and released results to
// concrete durable Store evidence. Implementations must be read-only. A nil
// verifier permits provider-only DENY projection, but any ALLOW decision
// requires both methods. This interface itself is not authority: correctness
// depends on the caller supplying the trusted Store verifier.
type StoreReplayVerifier interface {
	VerifyDecision(ProviderDecisionBinding) error
	VerifyResult(ProviderResultBinding) error
	VerifyAdmissionDenial(ProviderAdmissionDenialBinding) error
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
	SchemaVersion              string                         `json:"schema_version"`
	VerificationScope          string                         `json:"verification_scope"`
	WorkflowID                 string                         `json:"workflow_id"`
	ConversationID             string                         `json:"conversation_id"`
	ProfileID                  string                         `json:"profile_id"`
	Model                      string                         `json:"model"`
	PolicyDigest               string                         `json:"policy_digest"`
	SystemPromptDigest         string                         `json:"system_prompt_digest"`
	DeveloperPromptDigest      string                         `json:"developer_prompt_digest"`
	InitialGenerationID        string                         `json:"initial_generation_id"`
	InitialTreeDigest          string                         `json:"initial_tree_digest"`
	ProviderGenerationID       string                         `json:"provider_generation_id"`
	ProviderTreeDigest         string                         `json:"provider_tree_digest"`
	ProviderSequence           uint64                         `json:"provider_sequence"`
	EventCount                 uint64                         `json:"event_count"`
	ProviderJournalRecordCount uint64                         `json:"provider_journal_record_count"`
	ExchangeCount              uint64                         `json:"exchange_count"`
	ProviderRetries            uint64                         `json:"provider_retries,omitempty"`
	CapturedCallCount          uint64                         `json:"captured_call_count"`
	DecisionCount              uint64                         `json:"decision_count"`
	ResultCount                uint64                         `json:"result_count"`
	ProviderJournalHead        string                         `json:"provider_journal_head"`
	HistoryDigest              string                         `json:"history_digest"`
	Disposition                RecoveryDisposition            `json:"disposition"`
	PendingCall                *ProviderCallRetirementBinding `json:"pending_call,omitempty"`
	RetiredCall                *ProviderCallRetirementBinding `json:"retired_call,omitempty"`
	HostCall                   *HostCallBinding               `json:"host_call,omitempty"`
	HostCallOutcomeDigest      string                         `json:"host_call_outcome_digest,omitempty"`
	// HostCallRetryableFailure is true when HostCall was a failed attempt closed by a retryable-failure
	// record, not by an exchange.
	HostCallRetryableFailure bool   `json:"host_call_retryable_failure,omitempty"`
	ImportedPrefixDigest     string `json:"imported_prefix_digest,omitempty"`
	ProjectionDigest         string `json:"projection_digest"`
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
	replayRetired
)

// ReplayTranscript verifies the protected provider journal and deterministically
// reconstructs its current provider history in memory. It does not return that
// history, construct a Conversation, infer budgets/deadlines, or authorize a
// restart. The held Journal supplies a locally hash-verified prefix; its
// unkeyed chain does not establish authenticity against privileged rewrite or
// truncation. The Store verifier is called for each decision when supplied and
// is mandatory for ALLOW.
func ReplayTranscript(journal *audit.Journal, pins RecoveryPins, verifier StoreReplayVerifier) (TranscriptProjection, error) {
	return replayTranscript(journal, pins, verifier, nil)
}

// replayedHistory is what ReplayPredecessorContext keeps from a strict replay: the messages after the
// system and developer prompts and every response and tool-call ID the transcript holds.
type replayedHistory struct {
	messages         []openrouter.Message
	responses, calls map[string]struct{}
}

// replayTranscript is the one strict replay. A non-nil out receives the replayed history on success.
func replayTranscript(journal *audit.Journal, pins RecoveryPins, verifier StoreReplayVerifier, out *replayedHistory) (TranscriptProjection, error) {
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
	if pins.CheckpointRecordCount == 0 {
		if pins.CheckpointRecordHead != "" {
			return TranscriptProjection{}, replayError("empty checkpointed provider prefix has a non-empty head")
		}
	} else if pins.CheckpointRecordCount > uint64(len(trace.Records)) || pins.CheckpointRecordHead == "" ||
		trace.Records[pins.CheckpointRecordCount-1].Hash != pins.CheckpointRecordHead {
		return TranscriptProjection{}, replayError("provider journal does not retain its exact authenticated checkpoint prefix")
	}
	if len(trace.Effects) != 0 {
		return TranscriptProjection{}, replayError("provider journal unexpectedly contains effect traces")
	}

	cfg := pins.Config
	model := pins.Model
	route := cfg.Route.Resolved()
	policy := pins.Policy
	stream, err := protocol.NewStream(callIssuer, responseIssuer)
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	history := InitialPromptMessages(route, cfg.SystemPrompt, cfg.DeveloperPrompt)
	promptPrefix := len(history)
	currentGeneration, currentTree := cfg.InitialGenerationID, cfg.InitialTreeDigest
	state := replayAwaitPrompt
	imported, awaitingImportedPrompt := false, false
	var pending *replayDecision
	var pendingHostCall *hostCallReservationEvidence
	var lastHostCall *HostCallBinding
	var lastHostCallOutcomeDigest string
	var lastHostCallRetryable bool
	var exchangeRecord audit.Record
	var exchangeSequence uint64
	var retiredCall *ProviderCallRetirementBinding
	var ordinal, providerSequence, exchangeCount, callCount, decisionCount, resultCount uint64
	var turnRetries, totalRetries uint64
	hostCallIDs := make(map[string]struct{})
	var storeDecisionChecks uint64
	responses := make(map[string]struct{})
	calls := make(map[string]struct{})
	transitions := make(map[string]struct{})
	effects := make(map[string]struct{})
	var transitionSequence uint64
	// Hint-bearing results must use one format throughout the journal, including its tail.
	hintFormat := 0
	replayResultText := func(encoded []byte, result protocol.Result, hint *HintSelection) (string, error) {
		if permittedAlternativeHints[hintReason(result)] != "" {
			format := 1
			if hint != nil {
				format = 2
			}
			if hintFormat != 0 && hintFormat != format {
				return "", replayError("provider journal contains mixed hint metadata formats")
			}
			hintFormat = format
		}
		return journaledResultText(encoded, result, hint)
	}

	for _, record := range trace.Records {
		if record.Sequence != ordinal+1 || record.Event.Outcome != "" || ordinal == math.MaxUint64 {
			return TranscriptProjection{}, replayError("provider journal sequence or event outcome is invalid")
		}
		ordinal++
		if awaitingImportedPrompt && record.Event.Kind != "provider_user_admitted" {
			return TranscriptProjection{}, replayError("imported provider history is not followed by its host prompt")
		}
		awaitingImportedPrompt = false
		switch record.Event.Kind {
		case historyImportedKind:
			if pins.ImportedPrefixDigest == "" || ordinal != 1 {
				return TranscriptProjection{}, replayError("imported provider history is unpinned or not the first provider record")
			}
			var evidence historyImportedEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				verifyReplayEventID(record, cfg.ConversationID, historyImportedID, ordinal) != nil {
				return TranscriptProjection{}, replayError("imported provider history record is malformed")
			}
			messages, err := validateImportedHistory(evidence, cfg, pins.ImportedPrefixDigest)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			if err := appendReplayHistory(&history, route, messages...); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			for _, id := range evidence.ReservedResponseIDs {
				responses[id] = struct{}{}
			}
			for _, id := range evidence.ReservedToolCallIDs {
				calls[id] = struct{}{}
			}
			imported, awaitingImportedPrompt = true, true

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
			if err := appendReplayHistory(&history, route, openrouter.Message{Role: openrouter.User, Content: &content}); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			state = replayAwaitExchange

		case "provider_host_call_reserved":
			if state != replayAwaitExchange || pendingHostCall != nil || providerSequence >= MaxTranscriptTurns {
				return TranscriptProjection{}, replayError("provider host-call reservation is out of order")
			}
			var evidence hostCallReservationEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				evidence.WorkflowID != cfg.WorkflowID || evidence.ConversationID != cfg.ConversationID ||
				evidence.ProfileID != cfg.ProfileID || evidence.Turn != providerSequence+1 ||
				!validHostCallBinding(evidence.Binding) ||
				verifyReplayEventID(record, cfg.ConversationID, "host-call-reserved", ordinal) != nil {
				return TranscriptProjection{}, replayError("provider host-call reservation identity is invalid")
			}
			if err := verifyReplayRequestWithCompletionLimit(evidence.Request, cfg.ProfileID, cfg.ConversationID, route, history,
				evidence.Binding.TokenUpperBound); err != nil || requestEvidenceDigest(evidence.Request) != evidence.Binding.RequestDigest {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, errors.New("provider host-call reservation does not bind the exact request"), err)
			}
			if _, reused := hostCallIDs[evidence.Binding.CallID]; reused {
				return TranscriptProjection{}, replayError("provider host-call identity was reused by a later reservation")
			}
			hostCallIDs[evidence.Binding.CallID] = struct{}{}
			pendingHostCall = &evidence

		case exchangeRetryKind:
			// A failed, retryable attempt of the turn being awaited: it closes exactly the open
			// reservation, is itself retryable by the same rule the live path applied, and the
			// turn stays awaiting its exchange. At most MaxProviderRetries per turn.
			if state != replayAwaitExchange || pendingHostCall == nil || turnRetries >= MaxProviderRetries || totalRetries == math.MaxUint64 {
				return TranscriptProjection{}, replayError("provider retryable failure is out of order or exceeds the retry bound")
			}
			var evidence exchangeRetryEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				evidence.WorkflowID != cfg.WorkflowID || evidence.Turn != providerSequence+1 || evidence.Attempt != turnRetries+1 ||
				evidence.HostCall != pendingHostCall.Binding || !reflect.DeepEqual(evidence.Request, pendingHostCall.Request) ||
				!RetryableProviderFailure(evidence.StatusCode, evidence.ContentType, evidence.Response, evidence.RetryAfter) ||
				verifyReplayEventID(record, cfg.ConversationID, exchangeRetryID, ordinal) != nil {
				return TranscriptProjection{}, replayError("provider retryable failure does not bind its reserved host request")
			}
			binding := pendingHostCall.Binding
			lastHostCall = &binding
			lastHostCallOutcomeDigest = record.Hash
			lastHostCallRetryable = true
			pendingHostCall = nil
			turnRetries++
			totalRetries++

		case "provider_exchange":
			if state != replayAwaitExchange || providerSequence >= MaxTranscriptTurns || exchangeCount == math.MaxUint64 {
				return TranscriptProjection{}, replayError("provider exchange is out of order or exceeds sequence bounds")
			}
			var evidence exchangeEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				evidence.WorkflowID != cfg.WorkflowID || evidence.Turn != providerSequence+1 ||
				evidence.ResponseIDIssuer != responseIssuer || evidence.ToolCallIDIssuer != callIssuer ||
				evidence.StatusCode != 200 || !route.AcceptsResponseModel(evidence.Model) ||
				verifyReplayEventID(record, cfg.ConversationID, "exchange", ordinal) != nil {
				return TranscriptProjection{}, replayError("provider exchange metadata or event identity is invalid")
			}
			completionLimit := uint64(0)
			if pendingHostCall != nil {
				completionLimit = pendingHostCall.Binding.TokenUpperBound
				if evidence.HostCall == nil || *evidence.HostCall != pendingHostCall.Binding ||
					!reflect.DeepEqual(evidence.Request, pendingHostCall.Request) {
					return TranscriptProjection{}, replayError("provider exchange differs from its reserved host request")
				}
			} else if evidence.HostCall != nil || pins.RequireHostCallReservations {
				return TranscriptProjection{}, replayError("provider exchange has no matching host-call reservation")
			}
			if err := verifyReplayRequestWithCompletionLimit(evidence.Request, cfg.ProfileID, cfg.ConversationID, route, history, completionLimit); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			if len(evidence.Request.Body) > broker.MaxDurableExchangeBytes ||
				len(evidence.Response) > broker.MaxDurableExchangeBytes-len(evidence.Request.Body) {
				return TranscriptProjection{}, replayError("provider exchange exceeds its durable raw-byte budget")
			}
			captured, err := openrouter.CaptureSSE(bytes.NewReader(evidence.Response))
			if err != nil || !route.AcceptsResponseModel(captured.Model()) || captured.ResponseID() != evidence.ResponseID {
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
			replayedTurn := Turn{
				SchemaVersion: bridgeSchemaVersion, ResponseID: captured.ResponseID(), Model: projectedModel(route, captured.Model()),
				Created: captured.Created(), Generation: currentGeneration, Sequence: providerSequence + 1,
				AssistantText: assistantText, FinishReason: captured.FinishReason(), Usage: captured.Usage(),
			}
			if hasCall {
				replayedTurn.ToolCall = &ToolCallProjection{
					ID: toolCall.ID, Name: toolCall.Name, Arguments: append(json.RawMessage(nil), toolCall.RawArguments...),
				}
			}
			if err := validateTurnProjectionBudget(replayedTurn); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			if err := appendReplayHistory(&history, route, openrouter.Message{Role: openrouter.Assistant, Content: &assistantText, ToolCalls: priorCalls}); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			providerSequence++
			exchangeCount++
			turnRetries = 0
			exchangeRecord = record
			if pendingHostCall != nil {
				binding := pendingHostCall.Binding
				lastHostCall = &binding
				lastHostCallOutcomeDigest = record.Hash
				lastHostCallRetryable = false
				pendingHostCall = nil
			}
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
			resultContent, err := replayResultText(evidence.Result, decoded, evidence.Hint)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			message := openrouter.Message{Role: openrouter.Tool, ToolCallID: evidence.ToolCallID, Content: &resultContent}
			if err := appendReplayHistory(&history, route, message); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			currentGeneration, currentTree = evidence.GenerationTo, evidence.TreeDigestTo
			pending = nil
			state = replayAwaitExchange
			resultCount++

		case "provider_admission_denied":
			if state != replayAwaitResult || pending == nil || resultCount == math.MaxUint64 {
				return TranscriptProjection{}, replayError("admission denial has no unique preceding gate decision")
			}
			var evidence admissionDeniedEvidence
			if err := unmarshalCanonicalEvent(record.Event.Data, &evidence); err != nil ||
				record.Sequence <= pending.binding.DecisionRecordSequence ||
				verifyReplayEventID(record, cfg.ConversationID, evidence.ResultID, ordinal) != nil {
				return TranscriptProjection{}, replayError("admission denial record is malformed or out of order")
			}
			if err := validateReplayAdmissionDenial(evidence, *pending, cfg, currentGeneration, currentTree, effects); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			decoded, err := protocol.DecodeResult(evidence.Result)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			if !hasReplayVerifier(verifier) {
				return TranscriptProjection{}, replayError("admission denial requires a concrete read-only Store verifier")
			}
			if err := verifier.VerifyAdmissionDenial(ProviderAdmissionDenialBinding{
				Decision: cloneDecisionBinding(pending.binding), ResultRecordSequence: record.Sequence,
				ResultRecordHash: record.Hash, ResultID: evidence.ResultID, ResultDigest: evidence.ResultDigest,
				Result: cloneProtocolResult(decoded), EffectID: evidence.EffectID, ReasonCode: evidence.ReasonCode,
			}); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify Store admission denial cross-link: %w", err))
			}
			denialContent, err := replayResultText(evidence.Result, decoded, evidence.Hint)
			if err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			denialMessage := openrouter.Message{Role: openrouter.Tool, ToolCallID: evidence.ToolCallID, Content: &denialContent}
			if err := appendReplayHistory(&history, route, denialMessage); err != nil {
				return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
			}
			pending = nil
			state = replayAwaitExchange
			resultCount++

		case retirementEventKind:
			if pins.PreviousOwnerEpoch == 0 {
				return TranscriptProjection{}, replayError("provider lineage retirement lacks a retained predecessor owner")
			}
			var retirement lineageRetirementEvidence
			if unmarshalCanonicalEvent(record.Event.Data, &retirement) != nil {
				return TranscriptProjection{}, replayError("provider lineage retirement is not canonical evidence")
			}
			checkpointed := record.Sequence <= pins.CheckpointRecordCount
			retirementEpochValid := checkpointed && pins.CheckpointRecordCount > 0 &&
				retirement.PreviousOwnerEpoch > 0 && retirement.OwnerEpoch > retirement.PreviousOwnerEpoch &&
				retirement.OwnerEpoch <= pins.PreviousOwnerEpoch
			if !checkpointed {
				retirementEpochValid = pins.PreviousOwnerEpoch > 0 &&
					pins.RetirementOwnerEpoch > pins.PreviousOwnerEpoch &&
					retirement.PreviousOwnerEpoch == pins.PreviousOwnerEpoch &&
					retirement.OwnerEpoch > pins.PreviousOwnerEpoch &&
					retirement.OwnerEpoch <= pins.RetirementOwnerEpoch
			}
			if !retirementEpochValid || retirement.SchemaVersion != "tbound-provider-lineage-retirement/v1" ||
				retirement.WorkflowID != cfg.WorkflowID || retirement.ConversationID != cfg.ConversationID ||
				verifyReplayEventID(record, cfg.ConversationID, "lineage_retired", ordinal) != nil {
				return TranscriptProjection{}, replayError("provider lineage retirement has an invalid owner pair or event identity")
			}
			switch retirement.RetirementKind {
			case retirementKind:
				if state != replayAwaitDecision || pending == nil || pins.RetirementVerifier == nil {
					return TranscriptProjection{}, replayError("unleased call retirement has no captured pending call or host verifier")
				}
				binding, err := providerCallRetirementBinding(cfg.WorkflowID, cfg.ConversationID, cfg.ProfileID,
					providerSequence, exchangeSequence, exchangeRecord, pending.call, trace.Records, record.Sequence)
				if err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
				}
				if retirement.ProviderSequence != binding.ProviderSequence ||
					retirement.ResponseIDIssuer != binding.ResponseIDIssuer || retirement.ResponseID != binding.ResponseID ||
					retirement.ToolCallIDIssuer != binding.ToolCallIDIssuer || retirement.ToolCallID != binding.ToolCallID ||
					retirement.ToolName != binding.ToolName || retirement.ArgumentsDigest != binding.ArgumentsDigest ||
					retirement.ExchangeRecordSequence != binding.ExchangeRecordSequence ||
					retirement.ExchangeRecordHash != binding.ExchangeRecordHash ||
					retirement.PreviousRecordSequence != binding.PreviousRecordSequence ||
					retirement.PreviousRecordHash != binding.PreviousRecordHash {
					return TranscriptProjection{}, replayError("provider lineage retirement does not bind the exact captured call and journal prefix")
				}
				if err := pins.RetirementVerifier.VerifyUnleasedProviderCallRetirement(binding); err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify no Store authorization, effect, or command lease for retired call: %w", err))
				}
				binding.OwnerEpoch = retirement.OwnerEpoch
				binding.RetirementKind = retirement.RetirementKind
				retiredCall = &binding
				pending = nil
				state = replayRetired
			case idleRetirementKind:
				if (state != replayAwaitPrompt && state != replayAwaitExchange) || pending != nil || pins.IdleRetirementVerifier == nil ||
					retirement.ProviderSequence != providerSequence || retirement.ResponseIDIssuer != "" || retirement.ResponseID != "" ||
					retirement.ToolCallIDIssuer != "" || retirement.ToolCallID != "" || retirement.ToolName != "" ||
					retirement.ArgumentsDigest != "" || retirement.ExchangeRecordSequence != 0 || retirement.ExchangeRecordHash != "" {
					return TranscriptProjection{}, replayError("idle lineage retirement is not an idle provider boundary")
				}
				lastIndex := len(trace.Records) - 2
				if lastIndex < 0 || retirement.PreviousRecordSequence != trace.Records[lastIndex].Sequence ||
					retirement.PreviousRecordHash != trace.Records[lastIndex].Hash {
					return TranscriptProjection{}, replayError("idle lineage retirement does not bind its exact journal predecessor")
				}
				if err := pins.IdleRetirementVerifier.VerifyIdleProviderLineageRetirement(cfg.WorkflowID,
					cfg.ConversationID, providerSequence, retirement.PreviousOwnerEpoch, retirement.OwnerEpoch); err != nil {
					return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify idle lineage has no unresolved authority or command lease: %w", err))
				}
				pending = nil
				state = replayRetired
			default:
				return TranscriptProjection{}, replayError("provider lineage retirement uses an unsupported disposition")
			}

		case "provider_tool_result_unknown", "provider_exchange_unknown":
			return TranscriptProjection{}, replayError("provider journal contains an unresolved UNKNOWN provider exchange or effect result")

		default:
			return TranscriptProjection{}, replayError("provider journal contains an unsupported event kind")
		}
	}
	if pendingHostCall != nil {
		return TranscriptProjection{}, replayError("provider journal ends with an unsettled host-call reservation")
	}
	if pins.ImportedPrefixDigest != "" && !imported {
		return TranscriptProjection{}, replayError("pinned imported provider history is missing from the journal")
	}

	request, err := openrouter.BuildRequestForRoute(route, history, 0)
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
		ProviderSequence: providerSequence, EventCount: ordinal, ProviderJournalRecordCount: uint64(len(trace.Records)), ExchangeCount: exchangeCount, ProviderRetries: totalRetries,
		CapturedCallCount: callCount, DecisionCount: decisionCount, ResultCount: resultCount,
		HistoryDigest: digestDomain("tbound/provider-history/v2", request.Body()), Disposition: dispositionForReplayState(state, retiredCall != nil),
		HostCall: lastHostCall, HostCallOutcomeDigest: lastHostCallOutcomeDigest, HostCallRetryableFailure: lastHostCallRetryable,
		ImportedPrefixDigest: pins.ImportedPrefixDigest,
	}
	if state == replayAwaitDecision && pending != nil {
		binding, err := providerCallRetirementBinding(cfg.WorkflowID, cfg.ConversationID, cfg.ProfileID,
			providerSequence, exchangeSequence, exchangeRecord, pending.call, trace.Records, 0)
		if err != nil {
			return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
		}
		projection.PendingCall = &binding
	}
	if retiredCall != nil {
		projection.RetiredCall = retiredCall
	}
	if storeDecisionChecks > 0 {
		projection.VerificationScope = ProjectionScopeStoreCrosslink
	}
	if len(trace.Records) > 0 {
		projection.ProviderJournalHead = trace.Records[len(trace.Records)-1].Hash
	}
	projection.ProjectionDigest = transcriptProjectionDigest(projection)
	if out != nil {
		*out = replayedHistory{messages: cloneMessages(history[promptPrefix:]), responses: responses, calls: calls}
	}
	return projection, nil
}

func validateRecoveryPins(pins RecoveryPins) error {
	cfg := pins.Config
	if pins.Model != cfg.Route.Resolved().Model || cfg.Route.Validate() != nil || pins.Policy.Digest() == "" ||
		!validText(cfg.ConversationID, MaxIdentityBytes) || !validText(cfg.WorkflowID, MaxIdentityBytes) ||
		!validText(cfg.ProfileID, MaxIdentityBytes) || !validText(cfg.InitialGenerationID, MaxIdentityBytes) ||
		!validReplayTreeDigest(cfg.InitialTreeDigest) ||
		!validPromptText(cfg.SystemPrompt, openrouter.MaxAssistantTextBytes) ||
		!validPromptText(cfg.DeveloperPrompt, openrouter.MaxAssistantTextBytes) ||
		pins.ImportedPrefixDigest != "" && !validPrefixedDigest(pins.ImportedPrefixDigest) {
		return replayError("recovery pins do not satisfy the fixed host profile")
	}
	return nil
}

func verifyReplayRequestWithCompletionLimit(request requestEvidence, profileID, conversationID string,
	route openrouter.Route, history []openrouter.Message, completionLimit uint64) error {
	route = route.Resolved()
	built, err := openrouter.BuildRequestForRoute(route, history, completionLimit)
	if err != nil {
		return err
	}
	endpoint, _ := url.Parse(route.Endpoint)
	wantHeaders := httpHeaders(route.RequestHeaders(conversationID))
	if request.ProfileID != profileID || request.Method != "POST" || request.URL != route.Endpoint ||
		request.Host != endpoint.Host || request.ContentLength != int64(len(built.Body())) ||
		!reflect.DeepEqual(request.Headers, wantHeaders) || !bytes.Equal(request.Body, built.Body()) {
		return errors.New("recorded provider request does not byte-match the pinned broker history and fixed HTTP profile")
	}
	return nil
}

func validHostCallBinding(binding HostCallBinding) bool {
	return validText(binding.CallID, MaxIdentityBytes) && validLowerHexDigest(binding.RequestDigest) && binding.TokenUpperBound > 0
}

func validLowerHexDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func requestEvidenceDigest(request requestEvidence) string {
	encoded, err := json.Marshal(struct {
		ProfileID     string      `json:"profile_id"`
		Method        string      `json:"method"`
		URL           string      `json:"url"`
		Host          string      `json:"host"`
		ContentLength int64       `json:"content_length"`
		Headers       httpHeaders `json:"headers"`
		Body          []byte      `json:"body"`
	}{request.ProfileID, request.Method, request.URL, request.Host, request.ContentLength, request.Headers, request.Body})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("tbound-provider-request/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
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

// validateReplayAdmissionDenial accepts exactly one shape: an ALLOW gate decision followed by the DENY
// Result of its reason code, with no output, no transition and no generation change, and a Store effect
// that no other result used.
func validateReplayAdmissionDenial(evidence admissionDeniedEvidence, pending replayDecision, cfg Config,
	currentGeneration, currentTree string, effects map[string]struct{}) error {
	decision := pending.binding.GateDecision
	if decision.Verdict != gate.Allow {
		return errors.New("admission denial follows a gate decision that did not allow the operation")
	}
	if err := validateReplayResultIdentity(evidence.resultEvidence, pending, cfg, currentGeneration, currentTree); err != nil {
		return err
	}
	if !protocol.ValidAdmissionReasonCode(evidence.ReasonCode) {
		return errors.New("admission denial has no stable reason code")
	}
	decoded, err := protocol.DecodeResult(evidence.Result)
	if err != nil {
		return err
	}
	canonical, err := protocol.MarshalResult(decoded)
	if err != nil || !bytes.Equal(canonical, evidence.Result) {
		return errors.New("admission denial is not the exact canonical protocol result")
	}
	if !reflect.DeepEqual(decoded, admissionDeniedResult(pending.binding.Proposal, decision, evidence.ReasonCode)) {
		return errors.New("admission denial result differs from the recorded gate decision and reason code")
	}
	if evidence.GenerationTo != currentGeneration || evidence.TreeDigestTo != currentTree ||
		evidence.TransitionID != "" || evidence.TransitionSeq != 0 || !validText(evidence.EffectID, MaxIdentityBytes) {
		return errors.New("admission denial moves the provider generation, carries a transition, or lacks its Store effect")
	}
	if _, exists := effects[evidence.EffectID]; exists {
		return errors.New("admission denial effect ID was reused")
	}
	effects[evidence.EffectID] = struct{}{}
	return nil
}

func validateReplayResult(evidence resultEvidence, pending replayDecision, cfg Config,
	currentGeneration, currentTree string, lastTransition *uint64, transitions, effects map[string]struct{}) error {
	decision := pending.binding.GateDecision
	call := pending.call
	if err := validateReplayResultIdentity(evidence, pending, cfg, currentGeneration, currentTree); err != nil {
		return err
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

// validateReplayResultIdentity checks that a result record belongs to the pending captured call at the
// current provider tip.
func validateReplayResultIdentity(evidence resultEvidence, pending replayDecision, cfg Config,
	currentGeneration, currentTree string) error {
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
	return nil
}

func appendReplayHistory(history *[]openrouter.Message, route openrouter.Route, messages ...openrouter.Message) error {
	proposed := append(cloneMessages(*history), messages...)
	if len(proposed) > MaxTranscriptTurns*3+2 {
		return errors.New("provider replay message budget exhausted")
	}
	request, err := openrouter.BuildRequestForRoute(route, proposed, 0)
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

func dispositionForReplayState(state replayState, hasRetiredCall bool) RecoveryDisposition {
	switch state {
	case replayAwaitPrompt:
		return DispositionAwaitingHostPrompt
	case replayAwaitExchange:
		return DispositionAwaitingProviderExchange
	case replayAwaitDecision:
		return DispositionAwaitingGateDecision
	case replayRetired:
		if hasRetiredCall {
			return DispositionRetiredUnexecuted
		}
		return DispositionRetired
	default:
		return DispositionPendingProviderResult
	}
}

// AppendUnleasedProviderCallRetirement writes one explicit host-observed
// retirement for a captured call that has no gate decision. The verifier runs
// before append, and replay runs again after the durable write. It must be a
// read-only verifier; this function never settles or authorizes an operation.
func AppendUnleasedProviderCallRetirement(journal *audit.Journal, pins RecoveryPins,
	verifier StoreReplayVerifier) (TranscriptProjection, error) {
	if journal == nil || pins.PreviousOwnerEpoch == 0 || pins.RetirementOwnerEpoch <= pins.PreviousOwnerEpoch ||
		pins.RetirementVerifier == nil {
		return TranscriptProjection{}, replayError("host retirement requires a strictly newer acquired owner and a concrete no-authority verifier")
	}
	projection, err := ReplayTranscript(journal, pins, verifier)
	if err != nil {
		return TranscriptProjection{}, err
	}
	if projection.Disposition != DispositionAwaitingGateDecision || projection.PendingCall == nil || projection.RetiredCall != nil {
		return TranscriptProjection{}, replayError("only one captured call awaiting its first gate decision can be retired")
	}
	binding := *projection.PendingCall
	if err := pins.RetirementVerifier.VerifyUnleasedProviderCallRetirement(binding); err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("prove captured call has no Store authorization, effect, or command lease: %w", err))
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Sequence != projection.ProviderJournalRecordCount {
		return TranscriptProjection{}, errors.Join(replayError("provider journal changed before retirement append"), err)
	}
	last := trace.Records[len(trace.Records)-1]
	evidence := lineageRetirementEvidence{
		SchemaVersion: "tbound-provider-lineage-retirement/v1", WorkflowID: binding.WorkflowID,
		ConversationID: binding.ConversationID, OwnerEpoch: pins.RetirementOwnerEpoch,
		PreviousOwnerEpoch: pins.PreviousOwnerEpoch, RetirementKind: retirementKind,
		ProviderSequence: binding.ProviderSequence, ResponseIDIssuer: binding.ResponseIDIssuer,
		ResponseID: binding.ResponseID, ToolCallIDIssuer: binding.ToolCallIDIssuer,
		ToolCallID: binding.ToolCallID, ToolName: binding.ToolName, ArgumentsDigest: binding.ArgumentsDigest,
		ExchangeRecordSequence: binding.ExchangeRecordSequence, ExchangeRecordHash: binding.ExchangeRecordHash,
		PreviousRecordSequence: last.Sequence, PreviousRecordHash: last.Hash,
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return TranscriptProjection{}, err
	}
	ordinal := projection.EventCount + 1
	if ordinal == 0 {
		return TranscriptProjection{}, replayError("provider journal sequence exhausted")
	}
	retirementRecord, err := journal.Append(audit.Event{Kind: retirementEventKind,
		ID: fmt.Sprintf("%s:lineage_retired:%d", binding.ConversationID, ordinal), Data: data})
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	trace, err = journal.Trace()
	if err != nil || retirementRecord.Sequence != ordinal || uint64(len(trace.Records)) != ordinal ||
		trace.Records[len(trace.Records)-1].Hash != retirementRecord.Hash {
		return TranscriptProjection{}, errors.Join(replayError("durable retirement append did not preserve the exact journal head"), err)
	}
	verified, err := ReplayTranscript(journal, pins, verifier)
	if err != nil || verified.Disposition != DispositionRetiredUnexecuted || verified.PendingCall != nil ||
		verified.RetiredCall == nil || verified.RetiredCall.OwnerEpoch != pins.RetirementOwnerEpoch ||
		verified.RetiredCall.ToolCallID != binding.ToolCallID ||
		verified.ProviderJournalRecordCount != ordinal || verified.ProviderJournalHead != retirementRecord.Hash {
		return TranscriptProjection{}, errors.Join(replayError("durable retirement did not replay to the exact retired call"), err)
	}
	return verified, nil
}

// AppendIdleProviderLineageRetirement records a clean host boundary for an
// active transcript with no captured call pending. Full provider/Store replay
// and the concrete old-owner drain are still required before invocation.
func AppendIdleProviderLineageRetirement(journal *audit.Journal, pins RecoveryPins,
	verifier StoreReplayVerifier) (TranscriptProjection, error) {
	if journal == nil || pins.PreviousOwnerEpoch == 0 || pins.RetirementOwnerEpoch <= pins.PreviousOwnerEpoch ||
		pins.IdleRetirementVerifier == nil {
		return TranscriptProjection{}, replayError("idle retirement requires a strictly newer acquired owner and concrete host verifier")
	}
	projection, err := ReplayTranscript(journal, pins, verifier)
	if err != nil {
		return TranscriptProjection{}, err
	}
	if (projection.Disposition != DispositionAwaitingHostPrompt && projection.Disposition != DispositionAwaitingProviderExchange) ||
		projection.PendingCall != nil || projection.RetiredCall != nil {
		return TranscriptProjection{}, replayError("only an idle provider boundary with no captured call can be retired as idle")
	}
	if err := pins.IdleRetirementVerifier.VerifyIdleProviderLineageRetirement(projection.WorkflowID,
		projection.ConversationID, projection.ProviderSequence, pins.PreviousOwnerEpoch, pins.RetirementOwnerEpoch); err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, fmt.Errorf("verify old idle lineage has no unresolved authority or command lease: %w", err))
	}
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) == 0 || trace.Records[len(trace.Records)-1].Sequence != projection.ProviderJournalRecordCount {
		return TranscriptProjection{}, errors.Join(replayError("provider journal changed before idle retirement append"), err)
	}
	last := trace.Records[len(trace.Records)-1]
	evidence := lineageRetirementEvidence{
		SchemaVersion: "tbound-provider-lineage-retirement/v1", WorkflowID: projection.WorkflowID,
		ConversationID: projection.ConversationID, OwnerEpoch: pins.RetirementOwnerEpoch,
		PreviousOwnerEpoch: pins.PreviousOwnerEpoch, RetirementKind: idleRetirementKind,
		ProviderSequence: projection.ProviderSequence, PreviousRecordSequence: last.Sequence,
		PreviousRecordHash: last.Hash,
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return TranscriptProjection{}, err
	}
	ordinal := projection.EventCount + 1
	if ordinal == 0 {
		return TranscriptProjection{}, replayError("provider journal sequence exhausted")
	}
	retirementRecord, err := journal.Append(audit.Event{Kind: retirementEventKind,
		ID: fmt.Sprintf("%s:lineage_retired:%d", projection.ConversationID, ordinal), Data: data})
	if err != nil {
		return TranscriptProjection{}, errors.Join(ErrTranscriptReplay, err)
	}
	trace, err = journal.Trace()
	if err != nil || retirementRecord.Sequence != ordinal || uint64(len(trace.Records)) != ordinal ||
		trace.Records[len(trace.Records)-1].Hash != retirementRecord.Hash {
		return TranscriptProjection{}, errors.Join(replayError("durable idle retirement append did not preserve the exact journal head"), err)
	}
	projection.Disposition = DispositionRetired
	projection.EventCount = ordinal
	projection.ProviderJournalRecordCount = ordinal
	projection.ProviderJournalHead = retirementRecord.Hash
	projection.ProjectionDigest = transcriptProjectionDigest(projection)
	return projection, nil
}

func providerCallRetirementBinding(workflowID, conversationID, profileID string, providerSequence,
	exchangeSequence uint64, exchange audit.Record, call openrouter.ToolCall, records []audit.Record,
	retirementSequence uint64) (ProviderCallRetirementBinding, error) {
	if workflowID == "" || conversationID == "" || profileID == "" || providerSequence == 0 ||
		exchangeSequence != providerSequence || exchange.Sequence == 0 || exchange.Hash == "" || len(call.RawArguments) == 0 ||
		call.ID == "" || call.Name == "" || len(records) == 0 {
		return ProviderCallRetirementBinding{}, errors.New("captured call retirement identity is incomplete")
	}
	if retirementSequence != 0 && (retirementSequence != uint64(len(records)) || retirementSequence <= exchange.Sequence) {
		return ProviderCallRetirementBinding{}, errors.New("retirement record does not follow the captured call's exact journal prefix")
	}
	previousIndex := len(records) - 1
	if retirementSequence != 0 {
		previousIndex--
	}
	if previousIndex < 0 {
		return ProviderCallRetirementBinding{}, errors.New("captured call has no preceding journal record")
	}
	previous := records[previousIndex]
	if previous.Sequence == 0 || previous.Hash == "" || previous.Sequence != uint64(previousIndex+1) {
		return ProviderCallRetirementBinding{}, errors.New("captured call does not have a contiguous authenticated journal prefix")
	}
	responseID := capturedResponseID(exchange)
	if responseID == "" {
		return ProviderCallRetirementBinding{}, errors.New("captured call has no response identity")
	}
	return ProviderCallRetirementBinding{
		WorkflowID: workflowID, ConversationID: conversationID, ProfileID: profileID,
		RetirementKind: retirementKind, ProviderSequence: providerSequence,
		ResponseIDIssuer: responseIssuer, ResponseID: responseID,
		ToolCallIDIssuer: callIssuer, ToolCallID: call.ID, ToolName: call.Name,
		ArgumentsDigest: digestBytes(call.RawArguments), ExchangeRecordSequence: exchange.Sequence,
		ExchangeRecordHash: exchange.Hash, PreviousRecordSequence: previous.Sequence,
		PreviousRecordHash: previous.Hash,
	}, nil
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
