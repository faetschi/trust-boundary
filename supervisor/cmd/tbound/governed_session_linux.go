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
	"tbound/supervisor/internal/workflowrecovery"
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
	HostCall *providerbridge.HostCallBinding `json:"host_call,omitempty"`
}

type providerRetryEvidence struct {
	WorkflowID  string                         `json:"workflow_id"`
	Turn        uint64                         `json:"turn"`
	Attempt     uint64                         `json:"attempt"`
	Request     providerRequestEvidence        `json:"request"`
	StatusCode  int                            `json:"status_code"`
	Response    []byte                         `json:"response"`
	RetryAfter  string                         `json:"retry_after,omitempty"`
	ContentType string                         `json:"content_type,omitempty"`
	HostCall    providerbridge.HostCallBinding `json:"host_call"`
}

type providerHostCallReservationEvidence struct {
	WorkflowID     string                         `json:"workflow_id"`
	ConversationID string                         `json:"conversation_id"`
	ProfileID      string                         `json:"profile_id"`
	Turn           uint64                         `json:"turn"`
	Binding        providerbridge.HostCallBinding `json:"binding"`
	Request        providerRequestEvidence        `json:"request"`
}

type providerLineageRetirementEvidence struct {
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

type providerRequestEvidence struct {
	ProfileID     string              `json:"profile_id"`
	Method        string              `json:"method"`
	URL           string              `json:"url"`
	Host          string              `json:"host"`
	ContentLength int64               `json:"content_length"`
	Headers       map[string][]string `json:"headers"`
	Body          []byte              `json:"body"`
}

func providerRequestEvidenceDigest(request providerRequestEvidence) string {
	encoded, err := json.Marshal(struct {
		ProfileID     string              `json:"profile_id"`
		Method        string              `json:"method"`
		URL           string              `json:"url"`
		Host          string              `json:"host"`
		ContentLength int64               `json:"content_length"`
		Headers       map[string][]string `json:"headers"`
		Body          []byte              `json:"body"`
	}{request.ProfileID, request.Method, request.URL, request.Host, request.ContentLength, request.Headers, request.Body})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("tbound-provider-request/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

type providerPromptEvidence struct {
	WorkflowID     string `json:"workflow_id"`
	ConversationID string `json:"conversation_id"`
	TaskID         string `json:"task_id"`
	Generation     string `json:"generation"`
	Prompt         string `json:"prompt"`
}

type providerResultEvidence struct {
	WorkflowID       string                        `json:"workflow_id"`
	ConversationID   string                        `json:"conversation_id"`
	ResponseIDIssuer string                        `json:"response_id_issuer"`
	ToolCallIDIssuer string                        `json:"tool_call_id_issuer"`
	ResponseID       string                        `json:"response_id"`
	ToolCallID       string                        `json:"tool_call_id"`
	Tool             string                        `json:"tool"`
	ArgumentDigest   string                        `json:"argument_digest"`
	Sequence         uint64                        `json:"sequence"`
	GenerationFrom   string                        `json:"generation_from"`
	GenerationTo     string                        `json:"generation_to"`
	TreeDigestFrom   string                        `json:"tree_digest_from"`
	TreeDigestTo     string                        `json:"tree_digest_to"`
	ResultID         string                        `json:"result_id"`
	ResultDigest     string                        `json:"result_digest"`
	Result           []byte                        `json:"result"`
	Hint             *providerbridge.HintSelection `json:"hint,omitempty"`
	TransitionID     string                        `json:"transition_id,omitempty"`
	TransitionSeq    uint64                        `json:"transition_sequence,omitempty"`
	EffectID         string                        `json:"effect_id,omitempty"`
}

// providerAdmissionDeniedEvidence is the provider_admission_denied journal record: the DENY Result of an
// ALLOWed operation whose settled change delta admission refused. It has no transition and does not move
// the provider generation; EffectID is the Store effect holding the refusal.
type providerAdmissionDeniedEvidence struct {
	providerResultEvidence
	ReasonCode string `json:"reason_code"`
}

// checkAdmissionDenialResult checks that a denial record carries exactly the DENY Result of its reason
// code for the ALLOW decision: same correlation and policy digest, no output, no transition, an unchanged
// provider generation, and a Store effect. It returns the decoded Result.
func checkAdmissionDenialResult(denial providerAdmissionDeniedEvidence, decision gate.Decision) (protocol.Result, error) {
	result, err := protocol.DecodeResult(denial.Result)
	canonical, marshalErr := protocol.MarshalResult(result)
	if err != nil || marshalErr != nil || !bytes.Equal(canonical, denial.Result) ||
		decision.Verdict != gate.Allow || !protocol.ValidAdmissionReasonCode(denial.ReasonCode) ||
		result.ResponseID == nil || result.ResponseID.Issuer != denial.ResponseIDIssuer || result.ResponseID.Opaque != denial.ResponseID ||
		result.ToolCallID != denial.ToolCallID || result.Tool != denial.Tool || result.Sequence != denial.Sequence ||
		result.CanonicalArgumentsDigest != denial.ArgumentDigest || result.Verdict != string(gate.Deny) ||
		result.ReasonCode != denial.ReasonCode || result.PolicyDigest != decision.PolicyDigest || len(result.Output) != 0 ||
		decision.CanonicalArgumentsDigest != denial.ArgumentDigest || decision.Tool != denial.Tool || decision.Sequence != denial.Sequence ||
		denial.TransitionID != "" || denial.TransitionSeq != 0 || denial.EffectID == "" ||
		denial.GenerationTo != denial.GenerationFrom || denial.TreeDigestTo != denial.TreeDigestFrom {
		return protocol.Result{}, errors.Join(ErrGovernedSessionEvidence, err, marshalErr)
	}
	return result, nil
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
	capture        governedCapture
	request        sessionrepo.OperationRequest
	record         audit.Record
	effectReceipt  workflowrecovery.Receipt
	effectReserved bool
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
	OutputScopePaths           []string                `json:"output_scope_paths"`
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
	mu                                 sync.Mutex
	storeJournal                       *audit.Journal
	providerJournal                    *audit.Journal
	workflowID                         string
	sessionID                          string
	conversationID                     string
	providerProfileID                  string
	gatePolicy                         gate.Policy
	gatePolicyDigest                   string
	storePolicyDigest                  string
	metadataPolicyDigest               string
	bashOutputPaths                    []string
	commandOutputBytes                 int // per-stream Bash output bound shown to the provider; zero is the research summary
	store                              *sessionrepo.Store
	managedStore                       *sessionlaunch.ManagedStore
	baseline                           *sessionrepo.Generation
	prepared                           *sessionlaunch.PreparedSession
	initialGeneration                  string
	initialTreeDigest                  string
	baselineBound                      bool
	runner                             governedCommandRunner
	conversation                       *providerbridge.Conversation
	operationProofs                    map[delta.OperationIdentity]governedOperationProof
	retained                           *governedRetainedHistory
	workflowLedger                     *workflowrecovery.Ledger
	workflowLedgerPins                 workflowrecovery.Pins
	workflowVerifiedStorePins          workflowrecovery.Pins
	providerTokenUpperBound            uint64
	providerConfig                     providerbridge.Config
	workflowPriorAuthorizationSequence uint64
	requireWorkflowLedger              bool
	recoveryPromptTaskID               string
	recoveryPrompt                     string
	recoveryPromptAdmitted             bool
	initialPromptTaskID                string
	initialPrompt                      string
	initialPromptAdmitted              bool
	// requireEvidencedBashContainment makes VerifyOrigin require positive,
	// evidenced Bash containment (a registered signed command-cell profile and
	// settlement) instead of the development "not-established" marker. The
	// production composition sets it; development/E05 tests leave it false.
	requireEvidencedBashContainment bool

	// providerRoute, providerSpend and providerFactory are the host-bound provider
	// route (BindProviderRoute). All three are zero on the engineering route: the
	// fixed OpenCode Go profile, no spend hook, and the service key-file factory.
	providerRoute   openrouter.Route
	providerSpend   providerbridge.SpendAccountant
	providerFactory func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error)
	hintProvider    providerbridge.HintProvider

	// admissionScope is the delta-admission rule of the Store (BindCorpusBinding). The zero value is
	// the registered exact-path rule without protected paths.
	admissionScope sessionrepo.AdmissionScope
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
	storePolicyDigest, metadataPolicyDigest string, runner governedCommandRunner, bashOutputPaths ...[]string) (*SessionOperationAuthority, error) {
	if len(bashOutputPaths) > 1 {
		return nil, errors.New("governed session authority accepts at most one Bash output scope")
	}
	if storeJournal == nil || providerJournal == nil || storeJournal == providerJournal ||
		!validIdentityText(workflowID) || !validIdentityText(conversationID) || !validIdentityText(providerProfileID) ||
		gatePolicy.Digest() == "" || !validBareSHA256(storePolicyDigest) || !validBareSHA256(metadataPolicyDigest) {
		return nil, errors.New("governed session authority requires distinct protected Store/provider journals and fixed profile identities")
	}
	var configuredBashOutputPaths []string
	if len(bashOutputPaths) == 1 {
		configuredBashOutputPaths = cloneDurableOutputScopePaths(bashOutputPaths[0])
		if configuredBashOutputPaths != nil {
			if err := sessionrepo.ValidateOutputScopePaths(configuredBashOutputPaths); err != nil {
				return nil, fmt.Errorf("governed session Bash output scope: %w", err)
			}
		}
	}
	return &SessionOperationAuthority{
		storeJournal: storeJournal, providerJournal: providerJournal,
		workflowID: workflowID, conversationID: conversationID,
		providerProfileID: providerProfileID, gatePolicy: gatePolicy,
		gatePolicyDigest: gatePolicy.Digest(), storePolicyDigest: storePolicyDigest,
		metadataPolicyDigest: metadataPolicyDigest, runner: runner,
		bashOutputPaths: configuredBashOutputPaths,
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

// BindRecoveredBaseline binds a new, empty-proof SessionOperationAuthority to
// an already-recovered Store. The original D06 baseline remains the provenance
// source, while provider state starts at the separately verified current Store
// tip. No prior operation proof, Bash lease, or provider history is restored.
func (a *SessionOperationAuthority) BindRecoveredBaseline(managed *sessionlaunch.ManagedStore,
	baseline, tip *sessionrepo.Generation, pins workflowrecovery.Pins) error {
	if a == nil || managed == nil || baseline == nil || tip == nil || managed.Store() == nil ||
		managed.Store().IsRecoveryPreview() || pins.WorkflowID != a.workflowID || pins.SessionID == "" ||
		pins.RecoveryGeneration == 0 || pins.OwnerEpoch == 0 {
		return ErrGovernedSessionEvidence
	}
	store := managed.Store()
	verified, err := store.Verify()
	if err != nil {
		return err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return err
	}
	if bundle.Baseline.ID != baseline.ID() || bundle.Baseline.TreeDigest != baseline.TreeDigest() ||
		bundle.PolicyDigest != a.storePolicyDigest || bundle.MetadataPolicyDigest != a.metadataPolicyDigest ||
		len(bundle.Generations) == 0 || bundle.Generations[len(bundle.Generations)-1].ID != tip.ID() ||
		bundle.Generations[len(bundle.Generations)-1].TreeDigest != tip.TreeDigest() ||
		verified.SealedGeneration != tip.ID() || verified.SealedTreeDigest != tip.TreeDigest() ||
		pins.StoreBaselineGenerationID != baseline.ID() || pins.StoreBaselineTreeDigest != bareStoreTreeDigest(baseline.TreeDigest()) ||
		pins.StoreTipGenerationID != tip.ID() || pins.StoreTipTreeDigest != bareStoreTreeDigest(tip.TreeDigest()) {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.baselineBound || a.managedStore != nil || a.store != nil || a.conversation != nil || len(a.operationProofs) != 0 {
		return errors.New("recovered authority was already bound or contains prior operation proofs")
	}
	a.managedStore, a.store, a.baseline = managed, store, baseline
	a.sessionID, a.initialGeneration, a.initialTreeDigest = pins.SessionID, tip.ID(), tip.TreeDigest()
	a.baselineBound, a.workflowLedgerPins = true, pins
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

// BindWorkflowLedger attaches the validated durable workflow ledger before a
// provider conversation or effect can be admitted. Provider requests reserve a
// conservative token upper bound before HTTP dispatch; Store operations reserve
// their host-call allowance before writing authorization or effect intent.
func (a *SessionOperationAuthority) BindWorkflowLedger(ledger *workflowrecovery.Ledger,
	pins workflowrecovery.Pins, providerTokenUpperBound uint64) error {
	if a == nil || ledger == nil || providerTokenUpperBound == 0 {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	store, baseline, baselineBound, sessionID := a.store, a.baseline, a.baselineBound, a.sessionID
	a.mu.Unlock()
	if store == nil || baseline == nil || !baselineBound {
		return ErrGovernedSessionEvidence
	}
	checkpoint, _ := ledger.Snapshot()
	if !reflect.DeepEqual(checkpoint.Pins, pins) || pins.WorkflowID != a.workflowID ||
		(sessionID != "" && pins.SessionID != sessionID) ||
		pins.RecoveryGeneration == 0 || pins.OwnerEpoch == 0 || len(pins.ProviderTranscripts) == 0 {
		return workflowrecovery.ErrObservation
	}
	lastTranscript := pins.ProviderTranscripts[len(pins.ProviderTranscripts)-1]
	var projectedCount uint64
	for _, transcript := range pins.ProviderTranscripts {
		projectedCount += transcript.ProjectionCount
	}
	if lastTranscript.ConversationID != a.conversationID || lastTranscript.JournalCount != 0 || lastTranscript.JournalHead != "" ||
		lastTranscript.ProjectionCount != 0 || lastTranscript.ProjectionDigest != "" || lastTranscript.ProjectionHead != "" ||
		lastTranscript.Disposition != workflowrecovery.DispositionAwaitingHostPrompt ||
		workflowrecovery.TranscriptPinsDigest(pins.ProviderTranscripts) != pins.ProviderProjectionDigest ||
		pins.ProviderProjectionCount != projectedCount || pins.ProviderProjectionHead != lastTranscript.ProjectionHead ||
		pins.ProviderDisposition != workflowrecovery.DispositionAwaitingHostPrompt {
		return workflowrecovery.ErrObservation
	}
	providerTrace, err := a.providerJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return errors.Join(ErrProviderRecoveryRequired, err)
	}
	storeTrace, err := a.storeJournal.Trace()
	if err != nil || pins.StoreJournalCount != uint64(len(storeTrace.Records)) ||
		pins.StoreJournalHead != auditHeadHash(storeTrace.Records) {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil || bundle.PolicyDigest != a.storePolicyDigest || bundle.MetadataPolicyDigest != a.metadataPolicyDigest ||
		len(bundle.Generations) == 0 || bundle.Baseline.ID != pins.StoreBaselineGenerationID ||
		bareStoreTreeDigest(bundle.Baseline.TreeDigest) != pins.StoreBaselineTreeDigest {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	latest := bundle.Generations[len(bundle.Generations)-1]
	if latest.ID != pins.StoreTipGenerationID ||
		bareStoreTreeDigest(latest.TreeDigest) != pins.StoreTipTreeDigest {
		return workflowrecovery.ErrObservation
	}
	var priorAuthorizationSequence uint64
	for _, record := range storeTrace.Records[:pins.StoreJournalCount] {
		if record.Event.Kind == governedOperationAuthorizationKind {
			priorAuthorizationSequence = record.Sequence
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.baselineBound || a.prepared == nil || a.conversation != nil || a.workflowLedger != nil {
		return errors.New("workflow ledger must be bound once after verified D06 and before provider admission")
	}
	if a.workflowLedger != nil {
		return errors.New("workflow ledger was already bound")
	}
	if a.sessionID == "" && (pins.RecoveryGeneration != 1 || a.workflowLedgerPins.WorkflowID != "" ||
		len(a.workflowLedgerPins.ProviderTranscripts) != 0) {
		return workflowrecovery.ErrObservation
	}
	if a.sessionID != "" && a.sessionID != pins.SessionID {
		return workflowrecovery.ErrObservation
	}
	if a.workflowLedgerPins.WorkflowID != "" || len(a.workflowLedgerPins.ProviderTranscripts) != 0 {
		if !reflect.DeepEqual(a.workflowLedgerPins, pins) {
			return workflowrecovery.ErrObservation
		}
	} else if !a.baselineBound || a.managedStore == nil || a.managedStore.Store() != a.store || a.baseline == nil ||
		pins.StoreBaselineGenerationID != a.baseline.ID() ||
		pins.StoreBaselineTreeDigest != bareStoreTreeDigest(a.baseline.TreeDigest()) {
		return workflowrecovery.ErrObservation
	}
	if a.sessionID == "" {
		// The host-authorized original checkpoint is the sole source of the
		// fresh session identity. Replacement generations already have a
		// catalog-pinned session bound by their recovery composition.
		a.sessionID = pins.SessionID
	}
	a.workflowLedger = ledger
	a.workflowLedgerPins = pins
	a.providerTokenUpperBound = providerTokenUpperBound
	a.workflowPriorAuthorizationSequence = priorAuthorizationSequence
	a.requireWorkflowLedger = true
	return nil
}

// BindRecoveryPrompt pins the only first prompt permitted for a clean restart.
// It is a bounded host-generated envelope, not a transcript or a replay of old
// Pi/provider history.
func (a *SessionOperationAuthority) BindRecoveryPrompt(taskID, prompt string) error {
	if a == nil || !validIdentityText(taskID) || !boundedProductionPrompt(prompt) {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workflowLedger == nil || a.workflowLedgerPins.SessionID != a.sessionID || a.recoveryPrompt != "" || a.conversation != nil {
		return workflowrecovery.ErrMissing
	}
	a.recoveryPromptTaskID, a.recoveryPrompt = taskID, prompt
	return nil
}

// BindInitialWorkflowPrompt pins the host-admitted task from the fresh service
// catalog before the initial provider conversation receives any prompt.
func (a *SessionOperationAuthority) BindInitialWorkflowPrompt(taskID, prompt, taskDigest string) error {
	if a == nil || !validIdentityText(taskID) || !boundedProductionPrompt(prompt) ||
		!validDigestText(taskDigest) {
		return ErrGovernedSessionEvidence
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workflowLedger == nil || a.workflowLedgerPins.RecoveryGeneration != 1 ||
		a.workflowLedgerPins.SessionID != a.sessionID || a.conversation != nil ||
		a.initialPrompt != "" || a.recoveryPrompt != "" ||
		bareWorkflowDigest(taskDigest) != a.workflowLedgerPins.TaskDigest ||
		bareWorkflowDigest(digestBytes([]byte(prompt))) != a.workflowLedgerPins.PromptDigest {
		return workflowrecovery.ErrObservation
	}
	a.initialPromptTaskID, a.initialPrompt = taskID, prompt
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
	expectedOutputScopePaths, err := governedOperationOutputScope(proof.proposal, a.bashOutputPaths)
	if err != nil || !sameDurableOutputScopePaths(request.OutputScopePaths, expectedOutputScopePaths) {
		return errors.Join(ErrGovernedSessionEvidence, err)
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
	ledger, requireLedger := a.workflowLedger, a.requireWorkflowLedger
	a.mu.Unlock()
	if requireLedger && ledger == nil {
		return workflowrecovery.ErrMissing
	}
	var effectReceipt workflowrecovery.Receipt
	var effectReserved bool
	if ledger != nil {
		checkpoint, _ := ledger.Snapshot()
		reservation := checkpoint.Pins.HostCallReservation
		if reservation == nil || reservation.CapturedCall == nil ||
			reservation.CapturedCall.Disposition != workflowrecovery.CapturedCallAvailable {
			return workflowrecovery.ErrPendingUnknown
		}
		captured := reservation.CapturedCall
		binding := workflowrecovery.EffectBinding{
			OperationID: request.EffectID, SpecDigest: bareSHA256Digest(typedDigest),
			WorkflowID: checkpoint.Pins.WorkflowID, ConversationID: captured.ConversationID,
			HostCallID: captured.HostCallID, ToolCallID: captured.ToolCallID,
			HostCallOutcomeDigest: captured.OutcomeDigest,
			RecoveryGeneration:    captured.RecoveryGeneration, OwnerEpoch: captured.OwnerEpoch,
		}
		receipt, err := ledger.ReserveEffect(context.Background(), binding,
			capturedOperationVerifier{authority: a, request: request})
		if err != nil {
			return fmt.Errorf("durably reserve workflow effect allowance before operation authorization: %w", err)
		}
		effectReceipt, effectReserved = receipt, true
		checkpoint, _ = ledger.Snapshot()
		a.mu.Lock()
		a.workflowLedgerPins = cloneWorkflowPins(checkpoint.Pins)
		a.mu.Unlock()
	}
	request.OutputScopePaths = cloneDurableOutputScopePaths(request.OutputScopePaths)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.operationProofs) >= maxGovernedOperationProofs {
		return errors.New("governed operation proof budget exhausted")
	}
	if _, duplicate := a.operationProofs[operation]; duplicate {
		return errors.New("governed operation identity was already admitted")
	}
	admission := governedOperationAdmissionRecord{
		SchemaVersion: "tbound-governed-operation-admission/v2", WorkflowID: a.workflowID,
		ConversationID: a.conversationID, ProviderProfileID: a.providerProfileID,
		EffectID: request.EffectID, DecisionID: request.Decision.ID, Operation: operation,
		Tool: request.Tool, OutputScopePaths: cloneDurableOutputScopePaths(request.OutputScopePaths),
		TypedArgumentDigest: typedDigest, InputGeneration: request.InputGeneration,
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
	a.operationProofs[operation] = governedOperationProof{capture: proof, request: request, record: authorizationRecord,
		effectReceipt: effectReceipt, effectReserved: effectReserved}
	return nil
}

func governedOperationOutputScope(proposal protocol.Proposal, bashOutputPaths []string) ([]string, error) {
	var paths []string
	switch proposal.Tool {
	case "read":
		return nil, nil
	case "write":
		path, _, err := durableWriteArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		paths = []string{path}
	case "edit":
		path, _, err := durableEditArguments(proposal.Arguments)
		if err != nil {
			return nil, err
		}
		paths = []string{path}
	case "bash":
		if bashOutputPaths == nil {
			return nil, sessionrepo.ErrOutputScopeRequired
		}
		paths = cloneDurableOutputScopePaths(bashOutputPaths)
	default:
		return nil, errors.New("unsupported governed output-scope tool")
	}
	return paths, sessionrepo.ValidateOutputScopePaths(paths)
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
	expectedOutputScopePaths, scopeErr := governedOperationOutputScope(proof.proposal, a.bashOutputPaths)
	if scopeErr != nil || !sameDurableOutputScopePaths(authorization.request.OutputScopePaths, expectedOutputScopePaths) {
		return errors.Join(ErrGovernedSessionEvidence, scopeErr)
	}
	if authorization.record.Sequence == 0 || authorization.record.Event.Kind != governedOperationAuthorizationKind ||
		authorization.record.Event.ID != "governed-operation:"+authorization.request.EffectID ||
		authorization.request.Decision != decision || authorization.request.Operation != binding.Operation ||
		authorization.request.Tool != binding.Tool ||
		authorization.request.InputGeneration != binding.InputGeneration || authorization.request.InputTreeDigest != binding.InputTreeDigest {
		return ErrGovernedSessionEvidence
	}
	var admitted governedOperationAdmissionRecord
	if unmarshalGovernedJSON(authorization.record.Event.Data, &admitted) != nil ||
		admitted.SchemaVersion != "tbound-governed-operation-admission/v2" || admitted.WorkflowID != a.workflowID ||
		admitted.ConversationID != a.conversationID || admitted.ProviderProfileID != a.providerProfileID ||
		admitted.EffectID != authorization.request.EffectID || admitted.DecisionID != decision.ID ||
		admitted.Operation != binding.Operation || admitted.Tool != binding.Tool ||
		!sameDurableOutputScopePaths(admitted.OutputScopePaths, authorization.request.OutputScopePaths) ||
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
	route := a.providerRoute.Resolved()
	a.mu.Unlock()
	if !bound {
		return governedCapture{}, errors.New("governed baseline is not bound")
	}
	currentGeneration, currentTree := initialGeneration, initialTree
	exchanges := make(map[string]governedCapture)
	exchangeRecords := make(map[uint64]audit.Record)
	decisions := make(map[string]gate.Decision)
	var pendingHostCall *providerHostCallReservationEvidence
	proofs := make([]governedCapture, 0, 16)
	var providerOrdinal, providerTurn uint64
	var activeCaptureKey string
	lineageRetired := false
	for recordIndex, record := range trace.Records {
		if lineageRetired || record.Sequence != providerOrdinal+1 || record.Event.Outcome != "" {
			return governedCapture{}, ErrGovernedSessionEvidence
		}
		providerOrdinal = record.Sequence
		if pendingHostCall != nil && record.Event.Kind == "provider_exchange_retryable_failure" {
			// A failed, retryable attempt closes exactly the open reservation (see providerbridge/retry.go).
			if err := verifyProviderEventID(record, a.conversationID, "exchange-retry", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var retry providerRetryEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &retry); err != nil || retry.WorkflowID != a.workflowID ||
				retry.Turn != pendingHostCall.Turn || retry.Turn != providerTurn+1 || retry.HostCall != pendingHostCall.Binding ||
				!reflect.DeepEqual(retry.Request, pendingHostCall.Request) ||
				providerRequestEvidenceDigest(retry.Request) != pendingHostCall.Binding.RequestDigest ||
				!providerbridge.RetryableProviderFailure(retry.StatusCode, retry.ContentType, retry.Response, retry.RetryAfter) {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			pendingHostCall = nil
			continue
		}
		if pendingHostCall != nil && record.Event.Kind != "provider_exchange" {
			if record.Event.Kind == "provider_exchange_unknown" {
				if err := verifyProviderEventID(record, a.conversationID, "exchange", providerOrdinal); err != nil {
					return governedCapture{}, err
				}
				return governedCapture{}, errors.New("provider host-call reservation ended in an unresolved UNKNOWN exchange")
			}
			return governedCapture{}, errors.New("provider host-call reservation was not followed immediately by its exchange")
		}
		switch record.Event.Kind {
		case "provider_history_imported":
			// A product turn journal opens with its imported predecessor history; providerbridge replay verifies
			// its messages. Here it must be first and carry the predecessor this conversation was built with.
			var imported struct {
				ImportedPrefixDigest string `json:"imported_prefix_digest"`
			}
			a.mu.Lock()
			predecessor := a.providerConfig.Predecessor.Digest()
			a.mu.Unlock()
			if recordIndex != 0 || predecessor == "" ||
				verifyProviderEventID(record, a.conversationID, "history-imported", providerOrdinal) != nil ||
				json.Unmarshal(record.Event.Data, &imported) != nil || imported.ImportedPrefixDigest != predecessor {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
		case "provider_user_admitted":
			if activeCaptureKey != "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if err := verifyProviderEventID(record, a.conversationID, "user", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var prompt providerPromptEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &prompt); err != nil || prompt.WorkflowID != a.workflowID ||
				prompt.ConversationID != a.conversationID || prompt.Generation != currentGeneration || strings.TrimSpace(prompt.Prompt) == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
		case "provider_host_call_reserved":
			if pendingHostCall != nil || activeCaptureKey != "" {
				return governedCapture{}, errors.New("provider journal contains overlapping host-call reservations")
			}
			if err := verifyProviderEventID(record, a.conversationID, "host-call-reserved", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var reservation providerHostCallReservationEvidence
			if unmarshalGovernedJSON(record.Event.Data, &reservation) != nil ||
				reservation.WorkflowID != a.workflowID || reservation.ConversationID != a.conversationID ||
				reservation.ProfileID != a.providerProfileID || providerTurn == ^uint64(0) || reservation.Turn != providerTurn+1 ||
				!validIdentityText(reservation.Binding.CallID) || !validWorkflowDigest(reservation.Binding.RequestDigest) ||
				reservation.Binding.TokenUpperBound == 0 || reservation.Request.ProfileID != a.providerProfileID ||
				reservation.Request.Method != "POST" || reservation.Request.URL != route.Endpoint ||
				reservation.Request.Host == "" || len(reservation.Request.Body) == 0 ||
				providerRequestEvidenceDigest(reservation.Request) != reservation.Binding.RequestDigest {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			copy := reservation
			pendingHostCall = &copy
		case "provider_exchange":
			if err := verifyProviderEventID(record, a.conversationID, "exchange", providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			var exchange providerExchangeEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &exchange); err != nil || exchange.WorkflowID != a.workflowID ||
				providerTurn == ^uint64(0) || exchange.Turn != providerTurn+1 || exchange.ResponseIDIssuer != governedResponseIssuer || exchange.ToolCallIDIssuer != governedCallIssuer ||
				exchange.ResponseID == "" || !route.AcceptsResponseModel(exchange.Model) || exchange.StatusCode != 200 ||
				exchange.Request.ProfileID != a.providerProfileID || exchange.Request.Method != "POST" ||
				exchange.Request.URL != route.Endpoint || exchange.Request.Host == "" || len(exchange.Request.Body) == 0 {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if pendingHostCall != nil {
				if pendingHostCall.Turn != exchange.Turn || exchange.HostCall == nil || *exchange.HostCall != pendingHostCall.Binding ||
					!reflect.DeepEqual(exchange.Request, pendingHostCall.Request) ||
					providerRequestEvidenceDigest(exchange.Request) != pendingHostCall.Binding.RequestDigest {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				pendingHostCall = nil
			} else if exchange.HostCall != nil {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			providerTurn = exchange.Turn
			if exchange.ToolCall == nil {
				if activeCaptureKey != "" {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				exchangeRecords[exchange.Turn] = record
				continue
			}
			if activeCaptureKey != "" {
				return governedCapture{}, ErrGovernedSessionEvidence
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
			exchangeRecords[exchange.Turn] = record
			activeCaptureKey = key
		case providerbridge.SoftRefusalEvent:
			capture, ok := exchanges[activeCaptureKey]
			_, decided := decisions[activeCaptureKey]
			var refusal providerbridge.SoftRefusalEvidence
			if !ok || decided || capture.generation != currentGeneration || capture.treeDigest != currentTree || decodeCanonicalGoverned(record.Event.Data, &refusal) != nil ||
				verifyProviderEventID(record, a.conversationID, "soft-refusal", providerOrdinal) != nil {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if err := a.verifyCheckpointedSoftRefusal(trace, record, refusal, capture); err != nil {
				return governedCapture{}, err
			}
			activeCaptureKey = ""
		case "provider_gate_decision":
			if activeCaptureKey == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
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
			if !ok || key != activeCaptureKey || capture.exchangeSequence >= record.Sequence || gateDecision.ResponseID.Issuer != governedResponseIssuer ||
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
			if activeCaptureKey == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
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
			if captureKey != activeCaptureKey {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
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
			// A refusal has no Store summary and no Store proof: its exact fixed bytes and the unchanged tip are its whole shape.
			noEffect := result.Status == protocol.ResultStatusNoEffectRefused
			if noEffect && (result.Verdict != string(gate.Allow) || !protocol.NoEffectResultMatches(result, resultEvidence.Result) ||
				resultEvidence.TransitionID != "" || resultEvidence.TransitionSeq != 0 || resultEvidence.EffectID != "") {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if result.Verdict == string(gate.Allow) && result.Tool == "read" && !noEffect {
				var read durableReadSummaryPayload
				if decodeGovernedResult(result.Output, &read) != nil || read.Tool != "read" || read.Generation.ID != currentGeneration ||
					read.Generation.TreeDigest != currentTree || read.Outcome != "success" ||
					read.ContentDigest == "" || read.Size != len(read.Content) || read.ContentDigest != durableContentDigest([]byte(read.Content)) {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
			}
			if result.Verdict == string(gate.Allow) && !noEffect {
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
			} else if !noEffect && (result.Verdict != string(gate.Deny) || resultEvidence.TransitionID != "" || len(result.Output) != 0 ||
				resultEvidence.TransitionSeq != 0 || resultEvidence.EffectID != "") {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			currentGeneration, currentTree = resultEvidence.GenerationTo, resultEvidence.TreeDigestTo
			activeCaptureKey = ""
		case "provider_admission_denied":
			if activeCaptureKey == "" {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			var denial providerAdmissionDeniedEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &denial); err != nil || denial.WorkflowID != a.workflowID ||
				denial.ConversationID != a.conversationID || denial.ResponseIDIssuer != governedResponseIssuer ||
				denial.ToolCallIDIssuer != governedCallIssuer || denial.GenerationFrom != currentGeneration ||
				denial.TreeDigestFrom != currentTree || denial.ResultDigest != digestBytes(denial.Result) ||
				denial.ResultID != stableProviderResultID(a.workflowID, a.conversationID, denial.ResponseID, denial.ToolCallID) {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if err := verifyProviderEventID(record, a.conversationID, denial.ResultID, providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			denialKey := providerCaptureKey(denial.ResponseID, denial.ToolCallID)
			capture, captured := exchanges[denialKey]
			decision, decided := decisions[denialKey]
			if denialKey != activeCaptureKey || !captured || !decided || capture.sequence != denial.Sequence ||
				capture.proposal.Tool != denial.Tool {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			if _, err := checkAdmissionDenialResult(denial, decision); err != nil {
				return governedCapture{}, err
			}
			matchedDenial := 0
			for i := range proofs {
				proof := &proofs[i]
				if proof.response != denial.ResponseID || proof.call != denial.ToolCallID || proof.sequence != denial.Sequence {
					continue
				}
				if proof.decisionSequence >= record.Sequence || proof.generation != denial.GenerationFrom ||
					proof.treeDigest != denial.TreeDigestFrom || proof.result != nil {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				copy := denial.providerResultEvidence
				proof.result = &copy
				matchedDenial++
			}
			if matchedDenial != 1 {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			activeCaptureKey = ""
		case "provider_tool_result_unknown", "provider_exchange_unknown":
			id := "unknown-result"
			if record.Event.Kind == "provider_exchange_unknown" {
				id = "exchange"
			}
			if err := verifyProviderEventID(record, a.conversationID, id, providerOrdinal); err != nil {
				return governedCapture{}, err
			}
			return governedCapture{}, errors.New("provider conversation contains an unresolved UNKNOWN result/exchange")
		case "provider_lineage_retired":
			var retirement providerLineageRetirementEvidence
			if !unmarshalCanonicalGovernedJSON(record.Event.Data, &retirement) ||
				retirement.SchemaVersion != "tbound-provider-lineage-retirement/v1" ||
				retirement.WorkflowID != a.workflowID || retirement.ConversationID != a.conversationID ||
				retirement.PreviousOwnerEpoch == 0 || retirement.OwnerEpoch <= retirement.PreviousOwnerEpoch ||
				retirement.ProviderSequence != providerTurn || recordIndex == 0 ||
				retirement.PreviousRecordSequence != trace.Records[recordIndex-1].Sequence ||
				retirement.PreviousRecordHash == "" || retirement.PreviousRecordHash != trace.Records[recordIndex-1].Hash ||
				verifyProviderEventID(record, a.conversationID, "lineage_retired", providerOrdinal) != nil {
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			switch retirement.RetirementKind {
			case "host_idle_lineage_retired/v1":
				if activeCaptureKey != "" || retirement.ResponseIDIssuer != "" || retirement.ResponseID != "" ||
					retirement.ToolCallIDIssuer != "" || retirement.ToolCallID != "" || retirement.ToolName != "" ||
					retirement.ArgumentsDigest != "" || retirement.ExchangeRecordSequence != 0 || retirement.ExchangeRecordHash != "" {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
			case "host_unleased_before_gate/v1":
				capture, ok := exchanges[activeCaptureKey]
				exchangeRecord, hasExchange := exchangeRecords[retirement.ProviderSequence]
				_, hasDecision := decisions[activeCaptureKey]
				if !ok || !hasExchange || hasDecision || capture.sequence != retirement.ProviderSequence ||
					capture.response != retirement.ResponseID || capture.call != retirement.ToolCallID ||
					retirement.ResponseIDIssuer != governedResponseIssuer || retirement.ToolCallIDIssuer != governedCallIssuer ||
					retirement.ToolName != capture.proposal.Tool || retirement.ArgumentsDigest != digestBytes(capture.proposal.Arguments) ||
					retirement.ExchangeRecordSequence != exchangeRecord.Sequence || retirement.ExchangeRecordHash != exchangeRecord.Hash ||
					retirement.PreviousRecordSequence != exchangeRecord.Sequence || retirement.PreviousRecordHash != exchangeRecord.Hash {
					return governedCapture{}, ErrGovernedSessionEvidence
				}
				activeCaptureKey = ""
			default:
				return governedCapture{}, ErrGovernedSessionEvidence
			}
			lineageRetired = true
		default:
			return governedCapture{}, fmt.Errorf("%w: unsupported provider journal event kind %q", ErrGovernedSessionEvidence, record.Event.Kind)
		}
	}
	if pendingHostCall != nil {
		return governedCapture{}, ErrGovernedSessionEvidence
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
	argumentDigest, err := sessionrepoArgumentDigest(proof.proposal, a.bashOutputPaths)
	return operation, decision, argumentDigest, err
}

func sessionrepoArgumentDigest(proposal protocol.Proposal, bashOutputPaths []string) (string, error) {
	var value any
	switch proposal.Tool {
	case "read":
		path, _, err := durableReadArguments(proposal.Arguments)
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
		return sessionrepo.CommandArgumentsDigest(sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command},
			DeclaredOutputPaths: cloneDurableOutputScopePaths(bashOutputPaths)})
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
	bashOutputPaths := cloneDurableOutputScopePaths(authority.bashOutputPaths)
	commandOutputBytes := authority.commandOutputBytes
	authority.mu.Unlock()
	tip := durable.Tip()
	if broker != ownedConversation || policy.Digest() != gateDigest || durable.store != store || baseline == nil || tip == nil ||
		tip.ID() != baseline.ID() || tip.TreeDigest() != baseline.TreeDigest() ||
		!sameDurableOutputScopePaths(durable.bashOutputPaths, bashOutputPaths) || durable.commandOutputBytes != commandOutputBytes {
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
	return governedChannelsError(serveErr, bridgeErr)
}

// providerTurnFailure is RunGovernedChannels' error when its only failure is the provider bridge
// closing the conversation after a failed provider turn: the supervisor loop returned nil.
type providerTurnFailure struct{ err error }

func (f providerTurnFailure) Error() string { return f.err.Error() }
func (f providerTurnFailure) Unwrap() error { return f.err }

// commandNotStartedFailure is RunGovernedChannels' error when the supervisor loop failed on a command
// cell that the runner proved never started its command. The provider result wrapper recorded that
// call as UNKNOWN and closed the conversation, which serializes it with every provider exchange, so
// whatever the bridge returned after that leaves no exchange open.
type commandNotStartedFailure struct{ err error }

func (f commandNotStartedFailure) Error() string { return f.err.Error() }
func (f commandNotStartedFailure) Unwrap() error { return f.err }

// governedChannelsError is the error RunGovernedChannels returns for its two loop results.
func governedChannelsError(serveErr, bridgeErr error) error {
	if serveErr == nil && errors.Is(bridgeErr, providerbridge.ErrProviderTurnFailed) {
		return providerTurnFailure{err: bridgeErr}
	}
	if errors.Is(serveErr, protocol.ErrCommandNotStarted) {
		return commandNotStartedFailure{err: errors.Join(serveErr, bridgeErr)}
	}
	return errors.Join(serveErr, bridgeErr)
}

// BindAdmissionScope sets the delta-admission scope of the Store a product turn creates. It is called
// once, before the Store exists, with the scope the catalog admitted the turn under.
func (a *SessionOperationAuthority) BindAdmissionScope(scope sessionrepo.AdmissionScope) error {
	if a == nil {
		return ErrGovernedSessionEvidence
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("admission scope: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.store != nil {
		return errors.New("admission scope must be bound before the Store exists")
	}
	a.admissionScope = scope
	return nil
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
		AuthorizeOperation: a.AuthorizeOperation, VerifyDecision: a.VerifyDecision, VerifySettlement: a.VerifySettlement,
		AdmissionScope: a.admissionScope}
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
	if a.retained.contains(terminal.Sequence) {
		// The replay that froze this transition authenticated its origin. The new conversation's journal holds none of
		// its provider results, so the lookup below cannot resolve it.
		return a.retained.bash.leaseOrigin(terminal)
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

func unmarshalCanonicalGovernedJSON(raw []byte, target any) bool {
	if err := unmarshalGovernedJSON(raw, target); err != nil {
		return false
	}
	encoded, err := json.Marshal(target)
	return err == nil && bytes.Equal(encoded, raw)
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

func sameManifest(a, b delta.TreeManifest) bool { return reflect.DeepEqual(a, b) }
