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
	ApprovedModel      = openrouter.ApprovedModel
	MaxPromptBytes     = 16 << 10
	MaxTaskIDBytes     = 128
	MaxTranscriptBytes = 8 << 20
	MaxTranscriptTurns = 1024
	MaxIdentityBytes   = 256
	// MaxTurnProjectionBytes bounds the complete canonical Turn JSON, including
	// assistant text, tool arguments, usage, and fixed projection metadata.
	MaxTurnProjectionBytes = 1 << 20
	callIssuer             = "openrouter-tool-call/v1"
	responseIssuer         = "openrouter-response/v1"
	bridgeSchemaVersion    = "tbound-provider-bridge/v1"
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
	// BeforeExchange durably reserves host/provider allowance immediately
	// before each outbound request. A failure closes the conversation without
	// dispatching the request; the reservation must never be refunded.
	BeforeExchange func(context.Context) error
	// SelectCompletionTokenLimit chooses the host-owned per-request upper bound
	// from the remaining workflow budget. Production authority overwrites this
	// callback and binds its positive result into max_completion_tokens.
	SelectCompletionTokenLimit func(context.Context) (uint64, error)
	// ReserveHostCall binds the exact encoded request and token cap before the
	// HTTP transport sees them. CompleteHostCall settles only after the exact
	// response has been durably captured in the provider journal.
	ReserveHostCall  func(context.Context, broker.RequestRecord, uint64) (HostCallBinding, error)
	CompleteHostCall func(context.Context, HostCallCompletion) error
	// AdvanceProgress persists the exact provider/Store projection after a
	// complete tool result is journaled and before that result is released to
	// the adapter. It is supplied only by the governed workflow authority.
	AdvanceProgress func(context.Context) error
	// Route selects the wire profile. The zero Route is the fixed engineering
	// (OpenCode Go) route. The host sets a research route only from a frozen
	// research profile; the Pi worker never supplies it.
	Route openrouter.Route
	// Spend, when non-nil, accounts the monetary cost of every provider request:
	// it reserves a worst case before the request leaves the process and settles
	// the reported usage afterwards (see SpendAccountant). It is nil on the
	// engineering route.
	Spend SpendAccountant
}

// SpendAccountant is the monetary (USD) side of a host call. NextTurn calls it
// at the same point as ReserveHostCall and CompleteHostCall:
//
//   - ReserveSpend runs after the exact request is fixed and before any
//     reservation of the workflow ledger or any network call. An error refuses
//     the request, which is then never sent.
//   - ReleaseSpend runs when a later reservation refuses dispatch, so the
//     request provably never left the process.
//   - SettleSpend runs once the exact provider response is captured, with the
//     raw usage object of that response. An error withholds the response.
//   - UncertainSpend runs when the request may have been sent but its cost is
//     not established: any failure after dispatch, a missing usage object, or a
//     failed settlement. The reservation stays counted.
type SpendAccountant interface {
	ReserveSpend(ctx context.Context, reservation SpendReservation) (unitID string, err error)
	ReleaseSpend(ctx context.Context, unitID string) error
	SettleSpend(ctx context.Context, unitID string, usage json.RawMessage) error
	UncertainSpend(ctx context.Context, unitID string) error
}

// SpendReservation names the exact request that is about to be sent.
type SpendReservation struct {
	WorkflowID     string
	ConversationID string
	// Turn is the one-based provider turn of this conversation.
	Turn uint64
	// CompletionLimit is the host-selected max_completion_tokens bound of the
	// request. Zero means the host selected none.
	CompletionLimit uint64
	Request         broker.RequestRecord
}

// HostCallBinding is the providerbridge view of a workflowrecovery reservation.
// The packages stay independently testable; the production authority converts
// it to the owner package's typed binding.
type HostCallBinding struct {
	CallID          string `json:"call_id"`
	RequestDigest   string `json:"request_digest"`
	TokenUpperBound uint64 `json:"token_upper_bound"`
}

// HostCallCompletion names the exact durable provider exchange that settles a
// prior host-call reservation. OutcomeDigest is the provider audit record hash.
type HostCallCompletion struct {
	Binding       HostCallBinding `json:"binding"`
	OutcomeDigest string          `json:"outcome_digest"`
}

type Conversation struct {
	mu sync.Mutex

	cfg         Config
	model       string
	route       openrouter.Route
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
	HostCall         *HostCallBinding    `json:"host_call,omitempty"`
}

type hostCallReservationEvidence struct {
	WorkflowID     string          `json:"workflow_id"`
	ConversationID string          `json:"conversation_id"`
	ProfileID      string          `json:"profile_id"`
	Turn           uint64          `json:"turn"`
	Binding        HostCallBinding `json:"binding"`
	Request        requestEvidence `json:"request"`
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

// admissionDeniedEvidence is the provider_admission_denied record: the exact DENY Result released for an
// ALLOWed operation whose settled change delta admission refused. The generation does not move and no
// transition exists. EffectID names the Store effect that holds the refusal; ReasonCode is its stable code.
type admissionDeniedEvidence struct {
	resultEvidence
	ReasonCode string `json:"reason_code"`
}

// NewFromEnvironment uses the existing secure Go credential loader and its
// redirect-forbidding transport. The OpenCode Go model and route are fixed;
// provider configuration cannot come from Pi or Node.
func NewFromEnvironment(cfg Config, journal *audit.Journal) (*Conversation, error) {
	credentials, err := openrouter.LoadCredentials()
	if err != nil {
		return nil, errors.New("OpenCode Go credentials are unavailable")
	}
	return NewFromCredentials(cfg, credentials, journal)
}

// NewFromCredentials constructs the production-shaped owner from a secure
// loader result. The API key remains encapsulated by Credentials/HTTPDoer.
func NewFromCredentials(cfg Config, credentials openrouter.Credentials, journal *audit.Journal) (*Conversation, error) {
	if route := cfg.Route.Resolved(); route.IsResearch() {
		// The research route takes its model, endpoint and timeout from the frozen
		// profile route. The credential only carries the API key.
		if journal == nil {
			return nil, errors.New("provider conversation requires the protected audit journal")
		}
		doer, err := credentials.NewHTTPDoerForEndpoint(route.Endpoint, route.Timeout)
		if err != nil {
			return nil, fmt.Errorf("configure secure research transport: %w", err)
		}
		return newWithDoer(cfg, route.Model, doer, journal)
	}
	if credentials.ModelID() != ApprovedModel {
		return nil, errors.New("provider profile model is not the approved model")
	}
	if journal == nil {
		return nil, errors.New("provider conversation requires the protected audit journal")
	}
	doer, err := credentials.NewHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		return nil, fmt.Errorf("configure secure OpenCode Go transport: %w", err)
	}
	return newWithDoer(cfg, credentials.ModelID(), doer, journal)
}

// newWithDoer is an in-package offline fixture seam. Production callers must
// use NewFromCredentials/NewFromEnvironment and the protected audit.Journal.
func newWithDoer(cfg Config, model string, doer broker.HTTPDoer, journal eventJournal) (*Conversation, error) {
	if reflectNil(doer) || reflectNil(journal) {
		return nil, errors.New("provider conversation requires an injected HTTP Doer and durable journal")
	}
	route := cfg.Route.Resolved()
	if model != route.Model {
		return nil, errors.New("provider profile model is not the approved model")
	}
	if err := route.Validate(); err != nil {
		return nil, err
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
	managedCallReservation := cfg.SelectCompletionTokenLimit != nil || cfg.ReserveHostCall != nil || cfg.CompleteHostCall != nil
	if managedCallReservation && (cfg.SelectCompletionTokenLimit == nil || cfg.ReserveHostCall == nil ||
		cfg.CompleteHostCall == nil || cfg.BeforeExchange != nil) {
		return nil, errors.New("host-call accounting requires a complete selector, reservation, and settlement binding")
	}
	stream, err := protocol.NewStream(callIssuer, responseIssuer)
	if err != nil {
		return nil, fmt.Errorf("register provider identifier issuers: %w", err)
	}
	history := InitialPromptMessages(route, cfg.SystemPrompt, cfg.DeveloperPrompt)
	if _, err := openrouter.BuildRequestForRoute(route, history, 0); err != nil {
		return nil, fmt.Errorf("validate immutable provider prompt: %w", err)
	}
	return &Conversation{
		cfg: cfg, model: model, route: route, doer: doer, journal: journal,
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
// history, performs the actual OpenCode Go broker exchange, captures the
// response/call before release, and persists exact request/response evidence.
func (c *Conversation) NextTurn(ctx context.Context) (turn Turn, turnErr error) {
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
	var hostCallBinding *HostCallBinding
	var completionLimit uint64
	// Monetary accounting (research route only). spendUnit is the open ledger
	// reservation of this request. Once the request may have left the process
	// (spendDispatched) and until SettleSpend has run (spendSettled), any return
	// from NextTurn leaves the cost unestablished and marks the unit uncertain.
	var spendUnit string
	spendDispatched, spendSettled := false, false
	// disposed is set where the failed exchange is left disposed of in the provider journal.
	disposed := false
	defer func() {
		if spendUnit != "" && spendDispatched && !spendSettled {
			if err := c.cfg.Spend.UncertainSpend(context.WithoutCancel(ctx), spendUnit); err != nil {
				turnErr = errors.Join(turnErr, fmt.Errorf("mark provider spend uncertain: %w", err))
				disposed = false
			}
		}
		if disposed && turnErr != nil {
			turnErr = journaledFailure{turnErr}
		}
	}()
	var captured openrouter.CapturedResponse
	var exchangeErr error
	var records []broker.ExchangeRecord
	// A pre-effect provider failure may be retried (see retry.go): only with host-call accounting and
	// without monetary spend, so every attempt is its own reserved host call.
	retryable := c.cfg.Spend == nil && c.cfg.SelectCompletionTokenLimit != nil && c.cfg.ReserveHostCall != nil && c.cfg.CompleteHostCall != nil
	for attempt := uint64(0); ; attempt++ {
		hostCallBinding = nil
		profile := broker.Profile{
			ID: c.cfg.ProfileID, ConversationID: c.cfg.ConversationID, Model: c.model, Route: c.cfg.Route, Messages: cloneMessages(c.history),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: callIssuer,
			ResponseIDIssuer: responseIssuer, Generation: c.generation,
		}
		requestBroker, err := broker.New(profile, c.doer)
		if err != nil {
			return Turn{}, c.failLocked(fmt.Errorf("construct registered provider broker: %w", err))
		}
		completionLimit = 0
		if c.cfg.SelectCompletionTokenLimit != nil {
			completionLimit, err = c.cfg.SelectCompletionTokenLimit(ctx)
			if err != nil || completionLimit == 0 {
				return Turn{}, c.failLocked(errors.Join(errors.New("host selected no positive provider completion-token cap"), err))
			}
		} else if c.cfg.BeforeExchange != nil {
			if err := c.cfg.BeforeExchange(ctx); err != nil {
				return Turn{}, c.failLocked(fmt.Errorf("reserve durable provider allowance before dispatch: %w", err))
			}
		}
		captured, exchangeErr = requestBroker.ExchangeWithCompletionLimit(ctx, completionLimit, func(request broker.RequestRecord) error {
			if c.cfg.Spend != nil {
				unit, spendErr := c.cfg.Spend.ReserveSpend(ctx, SpendReservation{WorkflowID: c.cfg.WorkflowID,
					ConversationID: c.cfg.ConversationID, Turn: c.turns + 1, CompletionLimit: completionLimit, Request: request})
				if spendErr != nil {
					return fmt.Errorf("reserve provider spend before dispatch: %w", spendErr)
				}
				spendUnit = unit
			}
			// refuse reports a refusal that happens before the request is handed to the
			// transport, after the spend reservation: the request provably never left.
			refuse := func(cause error) error {
				if spendUnit != "" {
					if err := c.cfg.Spend.ReleaseSpend(ctx, spendUnit); err != nil {
						spendDispatched = true // the release is not durable; keep the unit counted
						return errors.Join(cause, fmt.Errorf("release provider spend: %w", err))
					}
					spendUnit = ""
				}
				return cause
			}
			if c.cfg.ReserveHostCall == nil {
				spendDispatched = spendUnit != ""
				return nil
			}
			binding, reserveErr := c.cfg.ReserveHostCall(ctx, request, completionLimit)
			if reserveErr != nil {
				return refuse(reserveErr)
			}
			if !validHostCallBinding(binding) || len(binding.RequestDigest) != 64 || binding.RequestDigest != broker.DigestRequestRecord(request) ||
				binding.TokenUpperBound != completionLimit {
				return refuse(errors.New("host-call reservation does not bind the exact outbound request and completion cap"))
			}
			hostCallBinding = &binding
			reservation := hostCallReservationEvidence{WorkflowID: c.cfg.WorkflowID,
				ConversationID: c.cfg.ConversationID, ProfileID: c.cfg.ProfileID,
				Turn: c.turns + 1, Binding: binding, Request: requestEvidenceFromBroker(request)}
			encoded, marshalErr := json.Marshal(reservation)
			if marshalErr != nil {
				return refuse(marshalErr)
			}
			if _, appendErr := c.appendLocked("provider_host_call_reserved", "host-call-reserved", encoded); appendErr != nil {
				return refuse(fmt.Errorf("persist exact host-call reservation before HTTP dispatch: %w", appendErr))
			}
			spendDispatched = spendUnit != ""
			return nil
		})
		records = requestBroker.Records()
		if exchangeErr != nil && retryable && attempt < MaxProviderRetries && ctx.Err() == nil &&
			hostCallBinding != nil && retryableExchange(records) {
			if retryErr := c.recordRetryableFailureLocked(ctx, records, *hostCallBinding, attempt+1); retryErr != nil {
				return Turn{}, c.failLocked(retryErr)
			}
			continue
		}
		break
	}
	if exchangeErr != nil {
		persistErr := c.persistFailedExchangeLocked(records, exchangeErr, hostCallBinding)
		disposed = exchangeDisposed(persistErr, records, hostCallBinding)
		return Turn{}, c.failLocked(errors.Join(fmt.Errorf("registered provider exchange failed; response withheld: %w", exchangeErr), persistErr))
	}
	if err := ctx.Err(); err != nil {
		persistErr := c.persistFailedExchangeLocked(records, err, hostCallBinding)
		disposed = exchangeDisposed(persistErr, records, hostCallBinding)
		return Turn{}, c.failLocked(errors.Join(fmt.Errorf("provider exchange canceled; response withheld: %w", err), persistErr))
	}
	if len(records) != 1 || !records[0].HasResponse() {
		return Turn{}, c.failLocked(errors.New("broker did not retain exactly one complete exchange"))
	}
	if !c.route.AcceptsResponseModel(captured.Model()) {
		return Turn{}, c.failLocked(errors.New("captured response model differs from registered model"))
	}
	if c.turns == math.MaxUint64 || c.sequence == math.MaxUint64 {
		return Turn{}, c.failLocked(errors.New("provider turn sequence exhausted"))
	}
	nextSequence := c.sequence + 1
	projection := Turn{
		SchemaVersion: bridgeSchemaVersion, ResponseID: captured.ResponseID(), Model: projectedModel(c.route, captured.Model()),
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
	if err := validateTurnProjectionBudget(projection); err != nil {
		persistErr := c.persistFailedExchangeLocked(records, err, hostCallBinding)
		disposed = exchangeDisposed(persistErr, records, hostCallBinding)
		return Turn{}, c.failLocked(errors.Join(fmt.Errorf("provider response projection exceeds its durable evidence budget: %w", err), persistErr))
	}
	callArray := []openrouter.PriorToolCall(nil)
	if projection.ToolCall != nil {
		callArray = []openrouter.PriorToolCall{priorCall}
	}
	requestRecord := records[0].Request()
	evidence := exchangeEvidence{
		WorkflowID: c.cfg.WorkflowID, Turn: c.turns + 1,
		Request:    requestEvidenceFromBroker(requestRecord),
		StatusCode: records[0].StatusCode(), Response: records[0].ResponseBody(),
		ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
		ResponseID: captured.ResponseID(), Model: captured.Model(), ToolCall: projection.ToolCall, HostCall: hostCallBinding,
	}
	encodedEvidence, err := json.Marshal(evidence)
	if err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("encode exact provider exchange evidence: %w", err))
	}
	journalRecord, err := c.appendLocked("provider_exchange", "exchange", encodedEvidence)
	if err != nil {
		return Turn{}, c.failLocked(fmt.Errorf("persist exact provider exchange; response withheld: %w", err))
	}
	if spendUnit != "" {
		// SettleSpend leaves the unit in its final state (settled, overage-settled
		// or uncertain) whether or not it returns an error.
		spendSettled = true
		if err := c.cfg.Spend.SettleSpend(ctx, spendUnit, captured.Usage()); err != nil {
			return Turn{}, c.failLocked(fmt.Errorf("settle provider spend; response withheld: %w", err))
		}
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
	if hostCallBinding != nil {
		if err := c.cfg.CompleteHostCall(ctx, HostCallCompletion{Binding: *hostCallBinding, OutcomeDigest: journalRecord.Hash}); err != nil {
			return Turn{}, c.failLocked(fmt.Errorf("settle exact provider exchange reservation before result release: %w", err))
		}
	}
	return projection, nil
}

// projectedModel is the model id shown to the Pi worker in a Turn. The worker pins
// one local model label (ApprovedModel) and refuses any other, whichever provider
// route serves the request, so the research route reports that label there. The
// provider's own model id stays in the durable exchange evidence and is checked
// against the route's model before the response is accepted.
func projectedModel(route openrouter.Route, providerModel string) string {
	if route.Resolved().IsResearch() {
		return ApprovedModel
	}
	return providerModel
}

func requestEvidenceFromBroker(record broker.RequestRecord) requestEvidence {
	return requestEvidence{
		ProfileID: record.ProfileID(), Method: record.Method(), URL: record.URL(),
		Host: record.Host(), ContentLength: record.ContentLength(),
		Headers: httpHeaders(record.Header()), Body: record.Body(),
	}
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
		if c.cfg.AdvanceProgress != nil {
			if err := c.cfg.AdvanceProgress(ctx); err != nil {
				return c.failLocked(fmt.Errorf("advance workflow checkpoint after denied provider result: %w", err))
			}
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
	var denied *protocol.AdmissionDeniedError
	if errors.As(err, &denied) {
		return nil, c.recordAdmissionDenied(ctx, proposal, decision, denied)
	}
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
	if c.cfg.AdvanceProgress != nil {
		if err := c.cfg.AdvanceProgress(ctx); err != nil {
			return nil, c.failLocked(fmt.Errorf("advance workflow checkpoint after durable provider result: %w", err))
		}
	}
	return append(json.RawMessage(nil), output...), nil
}

// recordAdmissionDenied journals the refusal of an ALLOWed operation by delta admission as one
// provider_admission_denied record, appends the DENY Result to the history and advances the workflow
// checkpoint like any released result, and returns the typed denial for the supervisor to release. The
// provider generation does not move: the candidate was discarded. A denial that cannot be recorded
// exactly is an UNKNOWN execution.
func (c *Conversation) recordAdmissionDenied(ctx context.Context, proposal protocol.Proposal, decision gate.Decision,
	denied *protocol.AdmissionDeniedError) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.state != stateExecuting || c.current == nil || !sameProposal(proposal, c.current.proposal) {
		return c.recordUnknownExecutionLocked(proposal, decision, errors.New("conversation changed while executor was running"))
	}
	if err := ctx.Err(); err != nil {
		return c.recordUnknownExecutionLocked(proposal, decision, fmt.Errorf("execution canceled; provider result withheld: %w", err))
	}
	if !protocol.ValidAdmissionReasonCode(denied.ReasonCode) || !validText(denied.EffectID, MaxIdentityBytes) {
		return c.recordUnknownExecutionLocked(proposal, decision, errors.New("admission denial has no stable reason code or Store effect"))
	}
	if _, duplicate := c.effectIDs[denied.EffectID]; duplicate {
		return c.recordUnknownExecutionLocked(proposal, decision, errors.New("admission denial effect ID was replayed"))
	}
	result := admissionDeniedResult(proposal, decision, denied.ReasonCode)
	if err := c.appendResultLocked("provider_admission_denied", result, denied.ReasonCode, c.generation, c.treeDigest, 0, "", denied.EffectID); err != nil {
		return c.recordUnknownExecutionLocked(proposal, decision, fmt.Errorf("persist admission denial; IPC result withheld: %w", err))
	}
	c.effectIDs[denied.EffectID] = struct{}{}
	if c.cfg.AdvanceProgress != nil {
		if err := c.cfg.AdvanceProgress(ctx); err != nil {
			return c.failLocked(fmt.Errorf("advance workflow checkpoint after admission denial: %w", err))
		}
	}
	return denied
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
	transitionID, effectID := "", ""
	if len(transitionIDs) > 0 {
		transitionID = transitionIDs[0]
	}
	if len(transitionIDs) > 1 {
		effectID = transitionIDs[1]
	}
	return c.appendResultLocked("provider_tool_result", result, "", nextGeneration, nextTreeDigest, transitionSequence, transitionID, effectID)
}

// appendResultLocked journals the exact protocol result of the current call and appends it to the history.
// A non-empty denialReason makes it the provider_admission_denied record of an ALLOWed operation.
func (c *Conversation) appendResultLocked(kind string, result protocol.Result, denialReason, nextGeneration, nextTreeDigest string,
	transitionSequence uint64, transitionID, effectID string) error {
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
	if nextGeneration != "" {
		toGeneration = nextGeneration
		toTreeDigest = nextTreeDigest
	}
	base := resultEvidence{
		WorkflowID: c.cfg.WorkflowID, ConversationID: c.cfg.ConversationID,
		ResponseIDIssuer: responseIssuer, ToolCallIDIssuer: callIssuer,
		ResponseID: c.current.responseID, ToolCallID: c.current.call.ID, Tool: c.current.call.Name,
		ArgumentDigest: c.current.call.CanonicalArgumentsDigest, Sequence: c.current.sequence,
		GenerationFrom: fromGeneration, GenerationTo: toGeneration, ResultID: resultID,
		TreeDigestFrom: fromTreeDigest, TreeDigestTo: toTreeDigest,
		ResultDigest: resultDigest, Result: encoded, TransitionID: transitionID,
		TransitionSeq: transitionSequence, EffectID: effectID,
	}
	var evidence []byte
	if denialReason != "" {
		evidence, err = json.Marshal(admissionDeniedEvidence{resultEvidence: base, ReasonCode: denialReason})
	} else {
		evidence, err = json.Marshal(base)
	}
	if err != nil {
		return err
	}
	_, err = c.appendLocked(kind, resultID, evidence)
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
	request, err := openrouter.BuildRequestForRoute(c.route, proposed, 0)
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
	event := audit.Event{Kind: kind, ID: eventID, Data: append([]byte(nil), data...)}
	if err := preflightAuditFrame(event); err != nil {
		return audit.Record{}, err
	}
	record, err := c.journal.Append(event)
	if err != nil {
		return audit.Record{}, err
	}
	c.eventOrdinal++
	return record, nil
}

// preflightAuditFrame applies audit.Append's JSON serializer to a record with
// the largest possible sequence and fixed-width hash fields. This avoids
// dispatch/release paths depending on an estimate that ignores nested base64
// and RawMessage escaping in provider evidence.
func preflightAuditFrame(event audit.Event) error {
	record := audit.Record{
		Version: 1, Sequence: math.MaxUint64, PreviousHash: strings.Repeat("0", 64),
		Event: event, Hash: strings.Repeat("0", 64),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(encoded)+1 > audit.MaxRecordBytes {
		return audit.ErrRecordTooLarge
	}
	return nil
}

// validateTurnProjectionBudget serializes the full public projection with the
// largest journal receipt fields. json.Marshal uses the same HTML escaping as
// durable RawMessage evidence, so this cap includes the worst-case expansion
// of both assistant text and tool arguments before a capture is accepted.
func validateTurnProjectionBudget(projection Turn) error {
	projection.JournalSeq = math.MaxUint64
	projection.JournalHash = strings.Repeat("0", 64)
	encoded, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	if len(encoded) > MaxTurnProjectionBytes {
		return errors.New("canonical provider turn projection exceeds one mebibyte")
	}
	return nil
}

// journaledFailure marks a NextTurn error whose failed exchange is disposed of in the provider
// journal: persistFailedExchangeLocked wrote its unknown record, or nothing reserved was left open.
// It keeps the wrapped error's text, which reaches the worker.
type journaledFailure struct{ error }

func (f journaledFailure) Unwrap() error { return f.error }

// exchangeDisposed reports whether persistFailedExchangeLocked left the exchange disposed of. With no
// exchange record to journal, a made host-call reservation stays open (it is not in the journal).
func exchangeDisposed(persistErr error, records []broker.ExchangeRecord, hostCall *HostCallBinding) bool {
	return persistErr == nil && (len(records) > 0 || hostCall == nil)
}

func (c *Conversation) persistFailedExchangeLocked(records []broker.ExchangeRecord, failure error,
	hostCall *HostCallBinding) error {
	if len(records) == 0 {
		return nil
	}
	record := records[len(records)-1]
	evidence := struct {
		WorkflowID string           `json:"workflow_id"`
		Request    requestEvidence  `json:"request"`
		StatusCode int              `json:"status_code"`
		Response   []byte           `json:"response"`
		Failure    string           `json:"failure"`
		HostCall   *HostCallBinding `json:"host_call,omitempty"`
	}{WorkflowID: c.cfg.WorkflowID, Request: requestEvidenceFromBroker(record.Request()),
		StatusCode: record.StatusCode(), Response: record.ResponseBody(), Failure: safeFailure(failure), HostCall: hostCall}
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
		Status: statusFor(proposal.Tool, decision.Verdict, output),
		Output: append(json.RawMessage(nil), output...),
	}
}

// statusFor projects the failure of an ALLOWed bash command into the stable public Result status. The gate
// verdict stays ALLOW (the command was permitted and ran); a settled command summary with a nonzero or an
// unobserved exit is the failure. It is a pure function of the Result inputs, so the recovery replay
// recomputes it from the recorded output. A mutation that fails to settle never reaches a Result: an outcome
// other than "success" fails validateExecutorOutput and the execution is withheld as UNKNOWN, so a failed
// write or edit ends the run instead of reading as a pivot.
func statusFor(tool string, verdict gate.Verdict, output json.RawMessage) string {
	if tool != "bash" || verdict != gate.Allow || len(output) == 0 {
		return ""
	}
	var summary struct {
		Command *struct {
			ExitCode     int  `json:"exit_code"`
			ExitObserved bool `json:"exit_observed"`
		} `json:"command"`
	}
	if json.Unmarshal(output, &summary) != nil || summary.Command == nil {
		return ""
	}
	if summary.Command.ExitCode != 0 || !summary.Command.ExitObserved {
		return protocol.ResultStatusCommandFailed
	}
	return ""
}

// admissionDeniedResult is the Result of an ALLOWed operation that delta admission refused: the gate
// decision's correlation with a DENY verdict and the admission reason code, and no output.
func admissionDeniedResult(proposal protocol.Proposal, decision gate.Decision, reasonCode string) protocol.Result {
	result := resultFor(proposal, decision, nil)
	result.Verdict, result.ReasonCode = string(gate.Deny), reasonCode
	return result
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

// InitialPromptMessages builds the immutable prompt prefix of a conversation: the system
// prompt and the registered developer prompt. The fresh path and the recovery replay both
// build it here, so a rebuilt request body is identical to the original.
//
// The OpenCode Go engineering route rejects the "developer" role ("chat message role
// developer is not supported"), so there the developer prompt text goes out as a second
// "system" message; DeveloperPrompt and its digest fields are unchanged. The research
// route keeps "developer": its request shape is frozen by the research profile.
func InitialPromptMessages(route openrouter.Route, systemPrompt, developerPrompt string) []openrouter.Message {
	system, developer := systemPrompt, developerPrompt
	developerRole := openrouter.Developer
	if !route.IsResearch() {
		developerRole = openrouter.System
	}
	return []openrouter.Message{
		{Role: openrouter.System, Content: &system},
		{Role: developerRole, Content: &developer},
	}
}
