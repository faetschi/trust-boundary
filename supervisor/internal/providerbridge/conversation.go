// Package providerbridge owns the fixed-profile Pi provider conversation.
// Credentials, transcript construction, HTTP exchange, tool-call capture, and
// result journaling stay in Go; the Pi worker receives bounded turn projections.
package providerbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

const (
	ApprovedModel       = "nvidia/nemotron-3.5-lightning:free"
	MaxPromptBytes      = 16 << 10
	MaxTaskIDBytes      = 128
	MaxTranscriptBytes  = 8 << 20
	MaxTranscriptTurns  = 1024
	MaxIdentityBytes    = 256
	callIssuer          = "openrouter-tool-call/v1"
	responseIssuer      = "openrouter-response/v1"
	bridgeSchemaVersion = "tbound-provider-bridge/v1"
)

var (
	ErrClosed            = errors.New("provider conversation is closed")
	ErrWrongState        = errors.New("provider conversation is not in the required state")
	ErrResultWithheld    = errors.New("durable provider tool result could not be recorded; result withheld")
	ErrInvalidGeneration = errors.New("durable executor returned an invalid generation transition")
)

// eventJournal is implemented by audit.Journal. A successful Append means the
// journal write and file sync completed, but does not attest filesystem power-
// loss behavior or the caller's identity.
type eventJournal interface {
	Append(audit.Event) (audit.Record, error)
}

type Config struct {
	ConversationID      string
	WorkflowID          string
	ProfileID           string
	InitialGenerationID string
	InitialTreeDigest   string
	SystemPrompt        string
	DeveloperPrompt     string
}

type Conversation struct {
	mu sync.Mutex

	cfg         Config
	model       string
	doer        broker.HTTPDoer
	journal     eventJournal
	correlation *protocol.Stream

	history            []openrouter.Message
	turns              uint64
	eventOrdinal       uint64
	sequence           uint64
	generation         string
	treeDigest         string
	responseIDs        map[string]struct{}
	transitionSequence uint64
	transitionIDs      map[string]struct{}
	effectIDs          map[string]struct{}
	state              state
	current            *currentCall
	closed             bool
}

type state uint8

const (
	stateNeedsPrompt state = iota + 1
	stateReadyToExchange
	stateAwaitingProposal
	stateAwaitingDecision
	stateAwaitingExecution
	stateExecuting
	stateClosed
)

type currentCall struct {
	responseID string
	call       openrouter.ToolCall
	sequence   uint64
	generation string
	proposal   protocol.Proposal
	matched    correlation.Decision
	decision   gate.Decision
}

// ToolCallProjection is the exact bounded provider capture translated for Pi.
// Its fields are correlation metadata only and never execution authority.
type ToolCallProjection struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Turn is a broker-owned response projection. It contains no endpoint, header,
// credential, prior conversation history, or caller-selected model.
type Turn struct {
	SchemaVersion string              `json:"schema_version"`
	ResponseID    string              `json:"response_id"`
	Model         string              `json:"model"`
	Created       int64               `json:"created"`
	Generation    string              `json:"generation"`
	Sequence      uint64              `json:"sequence"`
	AssistantText string              `json:"assistant_text"`
	FinishReason  string              `json:"finish_reason"`
	Usage         json.RawMessage     `json:"usage"`
	ToolCall      *ToolCallProjection `json:"tool_call,omitempty"`
	JournalSeq    uint64              `json:"journal_sequence"`
	JournalHash   string              `json:"journal_hash"`
}

type requestEvidence struct {
	ProfileID     string      `json:"profile_id"`
	Method        string      `json:"method"`
	URL           string      `json:"url"`
	Host          string      `json:"host"`
	ContentLength int64       `json:"content_length"`
	Headers       httpHeaders `json:"headers"`
	Body          []byte      `json:"body"`
}

type httpHeaders map[string][]string

type exchangeEvidence struct {
	WorkflowID       string              `json:"workflow_id"`
	Turn             uint64              `json:"turn"`
	Request          requestEvidence     `json:"request"`
	StatusCode       int                 `json:"status_code"`
	Response         []byte              `json:"response"`
	ResponseIDIssuer string              `json:"response_id_issuer"`
	ToolCallIDIssuer string              `json:"tool_call_id_issuer"`
	ResponseID       string              `json:"response_id"`
	Model            string              `json:"model"`
	ToolCall         *ToolCallProjection `json:"tool_call,omitempty"`
}

type promptEvidence struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"`
	Generation     string `json:"generation"`
	Prompt         string `json:"prompt"`
}

type resultEvidence struct {
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

// NewFromEnvironment uses the existing secure Go credential loader and its
// redirect-forbidding transport. Only the single historically authorized model
// is accepted; provider configuration cannot come from Pi or Node.
func NewFromEnvironment(cfg Config, journal *audit.Journal) (*Conversation, error) {
	credentials, err := openrouter.LoadCredentials()
	if err != nil {
		return nil, errors.New("approved OpenRouter credentials are unavailable")
	}
	return NewFromCredentials(cfg, credentials, journal)
}

// NewFromCredentials constructs the production-shaped owner from a secure
// loader result. The API key remains encapsulated by Credentials/HTTPDoer.
func NewFromCredentials(cfg Config, credentials openrouter.Credentials, journal *audit.Journal) (*Conversation, error) {
	if credentials.ModelID() != ApprovedModel {
		return nil, errors.New("provider profile model is not the approved model")
	}
	if journal == nil {
		return nil, errors.New("provider conversation requires the protected audit journal")
	}
	doer, err := credentials.NewHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		return nil, fmt.Errorf("configure secure OpenRouter transport: %w", err)
	}
	return newWithDoer(cfg, credentials.ModelID(), doer, journal)
}

// newWithDoer is an in-package offline fixture seam. Production callers must
// use NewFromCredentials/NewFromEnvironment and the protected audit.Journal.
func newWithDoer(cfg Config, model string, doer broker.HTTPDoer, journal eventJournal) (*Conversation, error) {
	if reflectNil(doer) || reflectNil(journal) {
		return nil, errors.New("provider conversation requires an injected HTTP Doer and durable journal")
	}
	if model != ApprovedModel {
		return nil, errors.New("provider profile model is not the approved model")
	}
	for name, value := range map[string]string{
		"conversation ID": cfg.ConversationID, "workflow ID": cfg.WorkflowID,
		"profile ID": cfg.ProfileID, "initial generation ID": cfg.InitialGenerationID,
		"initial tree digest": cfg.InitialTreeDigest,
	} {
		if !validText(value, MaxIdentityBytes) {
			return nil, fmt.Errorf("invalid %s", name)
		}
	}
	if !validPromptText(cfg.SystemPrompt, openrouter.MaxAssistantTextBytes) ||
		!validPromptText(cfg.DeveloperPrompt, openrouter.MaxAssistantTextBytes) {
		return nil, errors.New("registered system/developer prompts must be bounded nonempty text")
	}
	stream, err := protocol.NewStream(callIssuer, responseIssuer)
	if err != nil {
		return nil, fmt.Errorf("register provider identifier issuers: %w", err)
	}
	system, developer := cfg.SystemPrompt, cfg.DeveloperPrompt
	history := []openrouter.Message{
		{Role: openrouter.System, Content: &system},
		{Role: openrouter.Developer, Content: &developer},
	}
	if _, err := openrouter.BuildRequest(model, history); err != nil {
		return nil, fmt.Errorf("validate immutable provider prompt: %w", err)
	}
	return &Conversation{
		cfg: cfg, model: model, doer: doer, journal: journal,
		correlation: stream, history: history, generation: cfg.InitialGenerationID,
		treeDigest: cfg.InitialTreeDigest, responseIDs: make(map[string]struct{}),
		transitionIDs: make(map[string]struct{}), effectIDs: make(map[string]struct{}), state: stateNeedsPrompt,
	}, nil
}

// AdmitPrompt accepts only trusted host/session-control input. Node never sends
// a prompt or Pi transcript through the provider channel.
func (c *Conversation) AdmitPrompt(taskID, prompt string) error {
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.state == stateClosed {
		return ErrClosed
	}
	if c.state != stateNeedsPrompt {
		return ErrWrongState
	}
	if !validText(taskID, MaxTaskIDBytes) || !validPromptText(prompt, MaxPromptBytes) {
		return errors.New("user task identity or prompt is invalid or exceeds its bound")
	}
	data, err := json.Marshal(promptEvidence{
		WorkflowID: c.cfg.WorkflowID, ConversationID: c.cfg.ConversationID,
		TaskID: taskID, Generation: c.generation, Prompt: prompt,
	})
	if err != nil {
		return c.failLocked(fmt.Errorf("encode user admission: %w", err))
	}
	record, err := c.appendLocked("provider_user_admitted", "user", data)
	if err != nil {
		return c.failLocked(fmt.Errorf("persist user admission: %w", err))
	}
	_ = record // Record is retained in the journal; the prompt itself has no Pi-issued receipt.
	if err := c.appendHistoryLocked(openrouter.Message{Role: openrouter.User, Content: &prompt}); err != nil {
		return c.failLocked(fmt.Errorf("append admitted user message: %w", err))
	}
	c.state = stateReadyToExchange
	return nil
}

// NextTurn constructs the complete registered request from broker-owned
// history, performs the actual Go OpenRouter broker exchange, captures the
// response/call before release, and persists exact request/response evidence.
func (c *Conversation) NextTurn(ctx context.Context) (Turn, error) {
	if c == nil || ctx == nil {
		return Turn{}, ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.state == stateClosed {
		return Turn{}, ErrClosed
	}
	if c.state != stateReadyToExchange {
		return Turn{}, ErrWrongState
	}
	if err := ctx.Err(); err != nil {
		return Turn{}, c.failLocked(err)
	}
	if c.turns >= MaxTranscriptTurns || c.sequence == math.MaxUint64 {
		return Turn{}, c.failLocked(errors.New("provider conversation turn/sequence budget exhausted"))
	}
	profile := broker.Profile{
		ID: c.cfg.ProfileID, Model: c.model, Messages: cloneMessages(c.history),
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: callIssuer,
		ResponseIDIssuer: responseIssuer, Generation: c.generation,
	}
	requestBroker, err := broker.New(profile, c.doer)
	if err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("construct registered provider broker: %w", err))
	}
	captured, exchangeErr := requestBroker.Exchange(ctx)
	records := requestBroker.Records()
	if exchangeErr != nil {
		persistErr := c.persistFailedExchangeLocked(records, exchangeErr)
		return Turn{}, c.failLocked(errors.Join(fmt.Errorf("registered provider exchange failed; response withheld: %w", exchangeErr), persistErr))
	}
	if err := ctx.Err(); err != nil {
		persistErr := c.persistFailedExchangeLocked(records, err)
		return Turn{}, c.failLocked(errors.Join(fmt.Errorf("provider exchange canceled; response withheld: %w", err), persistErr))
	}
	if len(records) != 1 || !records[0].HasResponse() {
		return Turn{}, c.failLocked(errors.New("broker did not retain exactly one complete exchange"))
	}
	if captured.Model() != c.model {
		return Turn{}, c.failLocked(errors.New("captured response model differs from registered model"))
	}
	if c.turns == math.MaxUint64 || c.sequence == math.MaxUint64 {
		return Turn{}, c.failLocked(errors.New("provider turn sequence exhausted"))
	}
	nextSequence := c.sequence + 1
	projection := Turn{
		SchemaVersion: bridgeSchemaVersion, ResponseID: captured.ResponseID(), Model: captured.Model(),
		Created: captured.Created(), Generation: c.generation, Sequence: nextSequence,
		AssistantText: captured.AssistantText(), FinishReason: captured.FinishReason(), Usage: captured.Usage(),
	}
	var priorCall openrouter.PriorToolCall
	var capturedCall *openrouter.ToolCall
	if call, ok := captured.ToolCall(); ok {
		if !validText(call.ID, MaxIdentityBytes) || !validTool(call.Name) || len(call.RawArguments) == 0 {
			return Turn{}, c.failLocked(errors.New("captured tool call is outside the fixed bridge schema"))
		}
		projection.ToolCall = &ToolCallProjection{ID: call.ID, Name: call.Name, Arguments: call.RawArguments}
		priorCall = openrouter.PriorToolCall{ID: call.ID, Name: call.Name, RawArguments: call.RawArguments}
		capturedCall = &call
	}
	assistantText := captured.AssistantText()
	if assistantText == "" && projection.ToolCall == nil {
		return Turn{}, c.failLocked(errors.New("provider response has neither assistant text nor a tool call"))
	}
	callArray := []openrouter.PriorToolCall(nil)
	if projection.ToolCall != nil {
		callArray = []openrouter.PriorToolCall{priorCall}
	}
	requestRecord := records[0].Request()
	evidence := exchangeEvidence{
		WorkflowID: c.cfg.WorkflowID, Turn: c.turns + 1,
		Request: requestEvidence{
			ProfileID: requestRecord.ProfileID(), Method: requestRecord.Method(), URL: requestRecord.URL(),
			Host: requestRecord.Host(), ContentLength: requestRecord.ContentLength(),
			Headers: httpHeaders(requestRecord.Header()), Body: requestRecord.Body(),
		},
		StatusCode: records[0].StatusCode(), Response: records[0].ResponseBody(),
		ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
		ResponseID: captured.ResponseID(), Model: captured.Model(), ToolCall: projection.ToolCall,
	}
	encodedEvidence, err := json.Marshal(evidence)
	if err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("encode exact provider exchange evidence: %w", err))
	}
	journalRecord, err := c.appendLocked("provider_exchange", "exchange", encodedEvidence)
	if err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("persist exact provider exchange; response withheld: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("provider exchange canceled before release: %w", err))
	}
	if _, duplicate := c.responseIDs[captured.ResponseID()]; duplicate {
		return Turn{}, c.failLocked(errors.New("provider response ID was reused across conversation turns"))
	}
	if capturedCall != nil {
		trusted, err := captured.TrustedCapture(callIssuer, responseIssuer, c.generation, nextSequence)
		if err != nil {
			return Turn{}, c.failLocked(fmt.Errorf("derive captured tool-call identity: %w", err))
		}
		decision := c.correlation.Capture(trusted)
		if !decision.Accepted || decision.StreamClosed {
			return Turn{}, c.failLocked(fmt.Errorf("register captured tool call: %s", decision.ReasonCode))
		}
	}
	if err := c.appendHistoryLocked(openrouter.Message{Role: openrouter.Assistant, Content: &assistantText, ToolCalls: callArray}); err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("append captured assistant response: %w", err))
	}
	c.turns++
	c.sequence = nextSequence
	c.responseIDs[captured.ResponseID()] = struct{}{}
	projection.JournalSeq = journalRecord.Sequence
	projection.JournalHash = journalRecord.Hash
	if projection.ToolCall != nil {
		c.current = &currentCall{
			responseID: captured.ResponseID(), call: *capturedCall,
			sequence: nextSequence, generation: c.generation,
		}
		c.state = stateAwaitingProposal
	} else {
		c.state = stateNeedsPrompt
	}
	return projection, nil
}

// Correlate is the piruntime.Broker method. It accepts only the strict Pi
// proposal envelope and matches against the globally ordered broker captures.
func (c *Conversation) Correlate(ctx context.Context, proposal protocol.Proposal) (correlation.Decision, error) {
	if c == nil || ctx == nil {
		return correlation.Decision{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return correlation.Decision{}, err
	}
	encoded, err := protocol.MarshalProposal(proposal)
	if err != nil {
		return correlation.Decision{}, fmt.Errorf("encode provider proposal: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return correlation.Decision{}, c.failLocked(fmt.Errorf("proposal canceled before correlation: %w", err))
	}
	if c.closed || c.state == stateClosed {
		return correlation.Decision{Phase: correlation.PhaseProposal, ReasonCode: correlation.ReasonSessionClosed, StreamClosed: true}, nil
	}
	if c.state != stateAwaitingProposal || c.current == nil {
		return correlation.Decision{}, c.failLocked(ErrWrongState)
	}
	decision := c.correlation.Propose(encoded)
	if !decision.Accepted || decision.StreamClosed || decision.Proposal == nil {
		c.state = stateClosed
		c.closed = true
		return decision, nil
	}
	call := c.current
	if decision.Proposal.ResponseID.Opaque != call.responseID ||
		decision.Proposal.ToolCallID.Opaque != call.call.ID ||
		decision.Proposal.ToolName != call.call.Name ||
		decision.Proposal.Generation != call.generation ||
		decision.Proposal.Sequence != call.sequence {
		return decision, c.failLocked(errors.New("correlator result differs from retained provider capture"))
	}
	call.proposal = proposal
	call.matched = decision
	c.state = stateAwaitingDecision
	return decision, nil
}

// RecordDecision is the piruntime.DurableDecisionRecorder method. The exact
// gate result is journaled before any executor callback. DENY tool results are
// appended to the canonical provider history before the IPC result is released.
func (c *Conversation) RecordDecision(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) error {
	if c == nil || ctx == nil {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return c.failLocked(fmt.Errorf("gate decision canceled before persistence: %w", err))
	}
	if c.closed || c.state == stateClosed {
		return ErrClosed
	}
	if c.state != stateAwaitingDecision || c.current == nil || !sameProposal(proposal, c.current.proposal) || !decisionMatches(decision, c.current) {
		return c.failLocked(errors.New("gate decision does not match the current captured proposal"))
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return c.failLocked(fmt.Errorf("encode durable gate decision: %w", err))
	}
	if _, err := c.appendLocked("provider_gate_decision", "decision", encoded); err != nil {
		return c.failLocked(fmt.Errorf("persist gate decision: %w", err))
	}
	c.current.decision = decision
	if decision.Verdict == gate.Deny {
		result := resultFor(proposal, decision, nil)
		if err := c.appendToolResultLocked(result, "", c.treeDigest, 0); err != nil {
			return c.failLocked(fmt.Errorf("persist denied tool result; result withheld: %w", err))
		}
		return nil
	}
	if decision.Verdict != gate.Allow {
		return c.failLocked(errors.New("unsupported gate verdict"))
	}
	c.state = stateAwaitingExecution
	return nil
}

// Executor wraps the actual trusted executor and adds the exact completed
// protocol result to the journal/history before returning output to Supervisor.
type Executor interface {
	Execute(context.Context, protocol.Proposal, gate.Decision) (json.RawMessage, error)
}

// ResultingExecutor implements the piruntime.Executor method contract without
// importing the runtime package, so the owner remains independently buildable.
type ResultingExecutor struct {
	conversation *Conversation
	inner        Executor
}

func NewExecutor(conversation *Conversation, inner Executor) (*ResultingExecutor, error) {
	if conversation == nil || isNilExecutor(inner) {
		return nil, errors.New("provider result wrapper requires a conversation and trusted executor")
	}
	return &ResultingExecutor{conversation: conversation, inner: inner}, nil
}

func (e *ResultingExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	if e == nil || e.conversation == nil || isNilExecutor(e.inner) || ctx == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, e.conversation.fail(fmt.Errorf("execution canceled before executor dispatch: %w", err))
	}
	c := e.conversation
	c.mu.Lock()
	if c.closed || c.state == stateClosed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.state != stateAwaitingExecution || c.current == nil || !sameProposal(proposal, c.current.proposal) ||
		decision.Verdict != gate.Allow || !decisionMatches(decision, c.current) {
		c.mu.Unlock()
		return nil, c.fail(errors.New("executor call does not match the durable ALLOW decision"))
	}
	c.state = stateExecuting
	c.mu.Unlock()

	output, err := e.inner.Execute(ctx, proposal, decision)
	if err != nil {
		return nil, c.recordUnknownExecution(proposal, decision, fmt.Errorf("trusted executor failed; provider result withheld: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return nil, c.recordUnknownExecution(proposal, decision, fmt.Errorf("execution canceled; provider result withheld: %w", err), output)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.state != stateExecuting || c.current == nil || !sameProposal(proposal, c.current.proposal) {
		return nil, c.recordUnknownExecutionLocked(proposal, decision, errors.New("conversation changed while executor was running"), output)
	}
	nextGeneration, nextTreeDigest, transitionID, transitionSequence, effectID, err := validateExecutorOutput(proposal.Tool, output, c.generation, c.treeDigest)
	if err != nil {
		return nil, c.recordUnknownExecutionLocked(proposal, decision, fmt.Errorf("trusted executor result lacks durable generation binding: %w", err), output)
	}
	if transitionSequence != 0 {
		if transitionSequence <= c.transitionSequence {
			return nil, c.recordUnknownExecutionLocked(proposal, decision, errors.New("executor transition sequence is stale or replayed"), output)
		}
		if _, duplicate := c.transitionIDs[transitionID]; duplicate {
			return nil, c.recordUnknownExecutionLocked(proposal, decision, errors.New("executor transition ID was replayed"), output)
		}
		if _, duplicate := c.effectIDs[effectID]; duplicate {
			return nil, c.recordUnknownExecutionLocked(proposal, decision, errors.New("executor effect ID was replayed"), output)
		}
	}
	result := resultFor(proposal, decision, output)
	if err := c.appendToolResultLocked(result, nextGeneration, nextTreeDigest, transitionSequence, transitionID, effectID); err != nil {
		return nil, c.recordUnknownExecutionLocked(proposal, decision, fmt.Errorf("persist exact executor result; IPC result withheld: %w", err), output)
	}
	if transitionSequence != 0 {
		c.transitionSequence = transitionSequence
		c.transitionIDs[transitionID] = struct{}{}
		c.effectIDs[effectID] = struct{}{}
	}
	return append(json.RawMessage(nil), output...), nil
}

func (c *Conversation) recordUnknownExecution(proposal protocol.Proposal, decision gate.Decision, cause error, output ...json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recordUnknownExecutionLocked(proposal, decision, cause, output...)
}

func (c *Conversation) recordUnknownExecutionLocked(proposal protocol.Proposal, decision gate.Decision, cause error, output ...json.RawMessage) error {
	if c.current != nil && sameProposal(proposal, c.current.proposal) && decisionMatches(decision, c.current) {
		var rawOutput json.RawMessage
		if len(output) != 0 {
			rawOutput = append(json.RawMessage(nil), output[0]...)
		}
		rawDigest := sha256.Sum256(rawOutput)
		result := resultFor(proposal, decision, rawOutput)
		resultBytes, resultErr := protocol.MarshalResult(result)
		resultDigest := ""
		if resultErr == nil {
			resultHash := sha256.Sum256(resultBytes)
			resultDigest = "sha256:" + hex.EncodeToString(resultHash[:])
		}
		data, marshalErr := json.Marshal(struct {
			WorkflowID           string `json:"workflow_id"`
			ConversationID       string `json:"conversation_id"`
			ResponseIDIssuer     string `json:"response_id_issuer"`
			ToolCallIDIssuer     string `json:"tool_call_id_issuer"`
			ResponseID           string `json:"response_id"`
			ToolCallID           string `json:"tool_call_id"`
			Generation           string `json:"generation"`
			ResultID             string `json:"result_id"`
			ResultDigest         string `json:"result_digest,omitempty"`
			ExecutorOutputDigest string `json:"executor_output_digest"`
			ExecutorOutput       []byte `json:"executor_output,omitempty"`
			Outcome              string `json:"outcome"`
			Failure              string `json:"failure"`
		}{
			WorkflowID: c.cfg.WorkflowID, ConversationID: c.cfg.ConversationID,
			ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
			ResponseID: c.current.responseID, ToolCallID: c.current.call.ID,
			Generation: c.current.generation, ResultID: stableResultID(c.cfg.WorkflowID, c.cfg.ConversationID, c.current.responseID, c.current.call.ID),
			ResultDigest: resultDigest, ExecutorOutputDigest: "sha256:" + hex.EncodeToString(rawDigest[:]), ExecutorOutput: rawOutput,
			Outcome: "UNKNOWN", Failure: safeFailure(cause),
		})
		if marshalErr == nil {
			if _, appendErr := c.appendLocked("provider_tool_result_unknown", "unknown-result", data); appendErr != nil {
				cause = errors.Join(cause, fmt.Errorf("persist UNKNOWN provider result: %w", appendErr))
			}
		} else {
			cause = errors.Join(cause, marshalErr)
		}
	}
	return c.failLocked(errors.Join(ErrResultWithheld, cause))
}

// Close poisons the current transcript and forbids later provider or tool turns.
func (c *Conversation) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.state = stateClosed
	c.current = nil
	c.mu.Unlock()
}

func (c *Conversation) appendToolResultLocked(result protocol.Result, nextGeneration, nextTreeDigest string, transitionSequence uint64, transitionIDs ...string) error {
	if c.current == nil {
		return ErrWrongState
	}
	encoded, err := protocol.MarshalResult(result)
	if err != nil {
		return fmt.Errorf("encode exact protocol result: %w", err)
	}
	resultHash := sha256.Sum256(encoded)
	resultDigest := "sha256:" + hex.EncodeToString(resultHash[:])
	resultID := stableResultID(c.cfg.WorkflowID, c.cfg.ConversationID, c.current.responseID, c.current.call.ID)
	fromGeneration := c.generation
	toGeneration := fromGeneration
	fromTreeDigest := c.treeDigest
	toTreeDigest := fromTreeDigest
	transitionID := ""
	effectID := ""
	if len(transitionIDs) > 0 {
		transitionID = transitionIDs[0]
	}
	if len(transitionIDs) > 1 {
		effectID = transitionIDs[1]
	}
	if nextGeneration != "" {
		toGeneration = nextGeneration
		toTreeDigest = nextTreeDigest
	}
	evidence, err := json.Marshal(resultEvidence{
		WorkflowID: c.cfg.WorkflowID, ConversationID: c.cfg.ConversationID,
		ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
		ResponseID: c.current.responseID, ToolCallID: c.current.call.ID, Tool: c.current.call.Name,
		ArgumentDigest: c.current.call.CanonicalArgumentsDigest, Sequence: c.current.sequence,
		GenerationFrom: fromGeneration, GenerationTo: toGeneration, ResultID: resultID,
		TreeDigestFrom: fromTreeDigest, TreeDigestTo: toTreeDigest,
		ResultDigest: resultDigest, Result: encoded, TransitionID: transitionID,
		TransitionSeq: transitionSequence, EffectID: effectID,
	})
	if err != nil {
		return err
	}
	_, err = c.appendLocked("provider_tool_result", resultID, evidence)
	if err != nil {
		return err
	}
	content := string(encoded)
	message := openrouter.Message{Role: openrouter.Tool, ToolCallID: c.current.call.ID, Content: &content}
	if err := c.appendHistoryLocked(message); err != nil {
		return err
	}
	c.generation = toGeneration
	if nextGeneration != "" {
		c.treeDigest = nextTreeDigest
	}
	c.current = nil
	c.state = stateReadyToExchange
	return nil
}

func (c *Conversation) appendHistoryLocked(message openrouter.Message) error {
	proposed := append(cloneMessages(c.history), message)
	if len(proposed) > MaxTranscriptTurns*3+2 {
		return errors.New("provider transcript message limit exhausted")
	}
	request, err := openrouter.BuildRequest(c.model, proposed)
	if err != nil {
		return fmt.Errorf("bounded broker transcript rejected: %w", err)
	}
	if len(request.Body()) > openrouter.MaxRequestBytes || len(request.Body()) > MaxTranscriptBytes {
		return errors.New("provider transcript byte budget exhausted")
	}
	c.history = proposed
	return nil
}

func (c *Conversation) appendLocked(kind, id string, data []byte) (audit.Record, error) {
	if c.eventOrdinal == math.MaxUint64 {
		return audit.Record{}, errors.New("provider evidence sequence exhausted")
	}
	eventID := fmt.Sprintf("%s:%s:%d", c.cfg.ConversationID, id, c.eventOrdinal+1)
	record, err := c.journal.Append(audit.Event{Kind: kind, ID: eventID, Data: append([]byte(nil), data...)})
	if err != nil {
		return audit.Record{}, err
	}
	c.eventOrdinal++
	return record, nil
}

func (c *Conversation) persistFailedExchangeLocked(records []broker.ExchangeRecord, failure error) error {
	if len(records) == 0 {
		return nil
	}
	record := records[len(records)-1]
	evidence := struct {
		WorkflowID string          `json:"workflow_id"`
		Request    requestEvidence `json:"request"`
		StatusCode int             `json:"status_code"`
		Response   []byte          `json:"response"`
		Failure    string          `json:"failure"`
	}{WorkflowID: c.cfg.WorkflowID, Request: requestEvidence{
		ProfileID: record.Request().ProfileID(), Method: record.Request().Method(), URL: record.Request().URL(),
		Host: record.Request().Host(), ContentLength: record.Request().ContentLength(),
		Headers: httpHeaders(record.Request().Header()), Body: record.Request().Body(),
	}, StatusCode: record.StatusCode(), Response: record.ResponseBody(), Failure: safeFailure(failure)}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = c.appendLocked("provider_exchange_unknown", "exchange", data)
	return err
}

func (c *Conversation) fail(err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failLocked(err)
}

func (c *Conversation) failLocked(err error) error {
	c.closed = true
	c.state = stateClosed
	if err == nil {
		return ErrClosed
	}
	return errors.Join(ErrClosed, err)
}

func resultFor(proposal protocol.Proposal, decision gate.Decision, output json.RawMessage) protocol.Result {
	return protocol.Result{
		SchemaVersion: protocol.ResultSchemaVersion, ResponseID: decision.ResponseID,
		ToolCallID: proposal.ToolCallID, Tool: proposal.Tool, Sequence: decision.Sequence,
		CanonicalArgumentsDigest: decision.CanonicalArgumentsDigest, Verdict: string(decision.Verdict),
		ReasonCode: decision.ReasonCode, PolicyDigest: decision.PolicyDigest,
		Output: append(json.RawMessage(nil), output...),
	}
}

type generationSummary struct {
	ID         string `json:"id"`
	TreeDigest string `json:"tree_digest"`
}

type readSummary struct {
	Tool          string            `json:"tool"`
	Path          string            `json:"path"`
	Generation    generationSummary `json:"generation"`
	Size          int               `json:"size"`
	ContentDigest string            `json:"content_digest"`
	Content       string            `json:"content"`
	Outcome       string            `json:"outcome"`
}

type mutationSummary struct {
	Tool       string            `json:"tool"`
	Generation generationSummary `json:"generation"`
	Transition struct {
		ID       string `json:"id"`
		Sequence uint64 `json:"sequence"`
	} `json:"transition"`
	EffectID string `json:"effect_id"`
	Outcome  string `json:"outcome"`
	Command  *struct {
		ExitCode                 int    `json:"exit_code"`
		ExitObserved             bool   `json:"exit_observed"`
		CommandContainmentStatus string `json:"command_containment_status"`
		CommandRunnerProfile     string `json:"command_runner_profile"`
	} `json:"command,omitempty"`
}

// validateExecutorOutput accepts only the strict result envelopes produced by
// the existing durable executor. The envelope binds the current generation and
// (for mutations) the durable transition/effect identity before history moves.
func validateExecutorOutput(tool string, raw json.RawMessage, currentGeneration, currentTreeDigest string) (nextGeneration, nextTreeDigest, transitionID string, transitionSequence uint64, effectID string, err error) {
	if len(raw) == 0 || len(raw) > protocol.MaxFrameBytes {
		return "", "", "", 0, "", errors.New("executor output is empty or oversized")
	}
	if err = protocol.ValidateStrictJSON(raw); err != nil {
		return "", "", "", 0, "", err
	}
	switch tool {
	case "read":
		var result readSummary
		if err = decodeExact(raw, &result); err != nil {
			return "", "", "", 0, "", err
		}
		content := []byte(result.Content)
		digest := sha256.Sum256(content)
		if result.Tool != "read" || !validText(result.Path, openrouter.MaxRequestBytes) || result.Outcome != "success" ||
			result.Size < 0 || result.Size != len(content) || result.Generation.ID != currentGeneration ||
			result.Generation.TreeDigest != currentTreeDigest || result.ContentDigest != "sha256:"+hex.EncodeToString(digest[:]) {
			return "", "", "", 0, "", errors.New("read result does not match current durable generation/content")
		}
		return currentGeneration, currentTreeDigest, "", 0, "", nil
	case "write", "edit", "bash":
		var result mutationSummary
		if err = decodeExact(raw, &result); err != nil {
			return "", "", "", 0, "", err
		}
		if result.Tool != tool || result.Outcome != "success" ||
			!validText(result.Generation.ID, MaxIdentityBytes) || result.Generation.ID == currentGeneration ||
			!validText(result.Generation.TreeDigest, MaxIdentityBytes) ||
			!validText(result.Transition.ID, MaxIdentityBytes) || result.Transition.Sequence == 0 ||
			!validText(result.EffectID, MaxIdentityBytes) {
			return "", "", "", 0, "", ErrInvalidGeneration
		}
		if tool == "bash" && result.Command == nil {
			return "", "", "", 0, "", errors.New("bash result lacks settled command summary")
		}
		if tool != "bash" && result.Command != nil {
			return "", "", "", 0, "", errors.New("non-bash result contains a command summary")
		}
		return result.Generation.ID, result.Generation.TreeDigest, result.Transition.ID, result.Transition.Sequence, result.EffectID, nil
	default:
		return "", "", "", 0, "", errors.New("executor returned a result for an unregistered tool")
	}
}

func decodeExact(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("executor result contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decisionMatches(decision gate.Decision, current *currentCall) bool {
	return current != nil && decision.ResponseID != nil &&
		decision.ResponseID.Issuer == responseIssuer && decision.ResponseID.Opaque == current.responseID &&
		decision.ToolCallID == current.call.ID && decision.Tool == current.call.Name &&
		decision.Sequence == current.sequence && decision.CanonicalArgumentsDigest == current.call.CanonicalArgumentsDigest
}

func sameProposal(left, right protocol.Proposal) bool {
	return left.SchemaVersion == right.SchemaVersion && left.ToolCallID == right.ToolCallID && left.Tool == right.Tool &&
		bytes.Equal(left.Arguments, right.Arguments)
}

func cloneMessages(messages []openrouter.Message) []openrouter.Message {
	cloned := make([]openrouter.Message, len(messages))
	for i, message := range messages {
		cloned[i] = message
		if message.Content != nil {
			content := *message.Content
			cloned[i].Content = &content
		}
		if message.ToolCalls != nil {
			cloned[i].ToolCalls = make([]openrouter.PriorToolCall, len(message.ToolCalls))
			for j, call := range message.ToolCalls {
				cloned[i].ToolCalls[j] = call
				cloned[i].ToolCalls[j].RawArguments = append(json.RawMessage(nil), call.RawArguments...)
			}
		}
	}
	return cloned
}

func validText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPromptText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

func validTool(value string) bool {
	for _, tool := range broker.DeclaredToolManifest() {
		if value == tool {
			return true
		}
	}
	return false
}

func stableResultID(workflowID, conversationID, responseID, callID string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("tbound/provider-result/v1\x00"))
	_, _ = h.Write([]byte(workflowID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(conversationID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(responseID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(callID))
	return "provider-result:" + hex.EncodeToString(h.Sum(nil))
}

func safeFailure(err error) string {
	if err == nil {
		return "unknown provider failure"
	}
	text := err.Error()
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}

func isNilExecutor(value Executor) bool {
	if value == nil {
		return true
	}
	return reflectNil(value)
}

func reflectNil(value any) bool {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
