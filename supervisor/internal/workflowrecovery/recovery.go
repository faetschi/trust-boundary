// Package workflowrecovery stores the host-owned, cumulative recovery state
// for one registered workflow. Checkpoints contain bounded commitments and
// counters only; provider prompts, messages, arguments, and tool output are
// never copied into this package's ledger.
//
// The local journal below is an integrity-checked transport for checkpoint
// records, not an authenticator. A production Owner must also verify and
// advance a trusted external root for the owner fence and checkpoint head.
package workflowrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

const checkpointSchema = "tbound-workflow-recovery/v2"
const maxSQLiteInteger = uint64(1<<63 - 1)

const (
	DispositionNone                     = "none"
	DispositionAwaitingHostPrompt       = "awaiting_host_prompt"
	DispositionAwaitingProviderExchange = "awaiting_provider_exchange"
	DispositionAwaitingGateDecision     = "awaiting_gate_decision"
	DispositionPendingProviderResult    = "pending_provider_result"
	DispositionTerminal                 = "terminal"
	DispositionRetired                  = "retired"
	DispositionUnknown                  = "unknown"
)

const (
	PendingCallNone     = "none"
	PendingCallInFlight = "in_flight"
	PendingCallCaptured = "captured"
	PendingCallTerminal = "terminal"
	PendingCallRetired  = "retired"
	PendingCallUnknown  = "unknown"
)

var (
	ErrInvalid               = errors.New("invalid workflow recovery state")
	ErrMissing               = errors.New("workflow recovery checkpoint is missing")
	ErrDeadline              = errors.New("original workflow deadline has expired")
	ErrClockUncertain        = errors.New("workflow clock or boot identity cannot be reconstructed; workflow paused")
	ErrExhausted             = errors.New("workflow recovery allowance is exhausted; workflow paused")
	ErrCAS                   = errors.New("workflow recovery checkpoint compare-and-swap failed")
	ErrRollback              = errors.New("workflow recovery checkpoint rollback or divergence detected")
	ErrOwnerFence            = errors.New("workflow recovery owner fence is unavailable or stale")
	ErrRestartRequired       = errors.New("new owner must durably reserve a clean restart before retry admission")
	ErrPendingUnknown        = errors.New("workflow recovery cannot admit with pending or unknown provider/effect state")
	ErrFreshAuthorization    = errors.New("fresh workflow initialization lacks explicit host authorization")
	ErrObservation           = errors.New("workflow recovery observation is missing, stale, or unverified")
	ErrOwnerStore            = errors.New("workflow recovery SQLite owner store is unavailable or invalid")
	ErrOwnerStoreUnsupported = errors.New("durable workflow recovery SQLite owner requires Linux")
	ErrSQLiteEngine          = errors.New("workflow recovery requires SQLite with the WAL-reset fix")
)

// TranscriptPin is a typed commitment to a single held provider conversation
// and its protected transcript journal. Ordinal is one-based and entries are
// kept in workflow order. The current conversation may advance only through
// AdvanceCheckpoint; prior conversations are immutable.
type TranscriptPin struct {
	Ordinal               uint64 `json:"ordinal"`
	ConversationID        string `json:"conversation_id"`
	JournalIdentityDigest string `json:"journal_identity_digest"`
	JournalHead           string `json:"journal_head,omitempty"`
	JournalCount          uint64 `json:"journal_count"`
	ProjectionDigest      string `json:"projection_digest,omitempty"`
	ProjectionHead        string `json:"projection_head,omitempty"`
	ProjectionCount       uint64 `json:"projection_count"`
	Disposition           string `json:"disposition"`
}

// EffectBinding identifies the exact Store operation admitted against this
// workflow. SpecDigest commits to the host-bound operation specification, not
// provider-supplied free text.
type EffectBinding struct {
	OperationID           string `json:"operation_id"`
	SpecDigest            string `json:"spec_digest"`
	WorkflowID            string `json:"workflow_id,omitempty"`
	ConversationID        string `json:"conversation_id,omitempty"`
	HostCallID            string `json:"host_call_id,omitempty"`
	ToolCallID            string `json:"tool_call_id,omitempty"`
	HostCallOutcomeDigest string `json:"host_call_outcome_digest,omitempty"`
	RecoveryGeneration    uint64 `json:"recovery_generation,omitempty"`
	OwnerEpoch            uint64 `json:"owner_epoch,omitempty"`
}

type EffectReservationDisposition string

const (
	EffectReservationReserved EffectReservationDisposition = "reserved"
	EffectReservationTerminal EffectReservationDisposition = "terminal"
	EffectReservationRetired  EffectReservationDisposition = "retired"
	EffectReservationUnknown  EffectReservationDisposition = "unknown"
)

// EffectReservationPin remains in the cumulative checkpoint after settlement.
// A later effect may replace it only after this reservation is terminal.
type EffectReservationPin struct {
	Binding          EffectBinding                `json:"binding"`
	Disposition      EffectReservationDisposition `json:"disposition"`
	OutcomeDigest    string                       `json:"outcome_digest,omitempty"`
	RetirementDigest string                       `json:"retirement_digest,omitempty"`
}

// HostCallBinding identifies the exact encoded outbound provider request and
// its host-selected completion-token cap before dispatch.
type HostCallBinding struct {
	CallID          string `json:"call_id"`
	RequestDigest   string `json:"request_digest"`
	TokenUpperBound uint64 `json:"token_upper_bound"`
}

type CapturedCallDisposition string

const (
	CapturedCallAvailable CapturedCallDisposition = "available"
	CapturedCallConsumed  CapturedCallDisposition = "consumed"
	CapturedCallRetired   CapturedCallDisposition = "retired"
)

// CapturedCallPin binds one provider tool call to the exact terminal HTTP
// reservation and conversation that produced it. It contains identities and
// digests only; provider arguments and output remain outside this ledger.
type CapturedCallPin struct {
	WorkflowID         string                  `json:"workflow_id"`
	ConversationID     string                  `json:"conversation_id"`
	HostCallID         string                  `json:"host_call_id"`
	ToolCallID         string                  `json:"tool_call_id"`
	OutcomeDigest      string                  `json:"outcome_digest"`
	RecoveryGeneration uint64                  `json:"recovery_generation"`
	OwnerEpoch         uint64                  `json:"owner_epoch"`
	Disposition        CapturedCallDisposition `json:"disposition"`
	RetirementDigest   string                  `json:"retirement_digest,omitempty"`
}

type HostCallReservationPin struct {
	Binding          HostCallBinding  `json:"binding"`
	Disposition      string           `json:"disposition"`
	OutcomeDigest    string           `json:"outcome_digest,omitempty"`
	RetirementDigest string           `json:"retirement_digest,omitempty"`
	CapturedCall     *CapturedCallPin `json:"captured_call,omitempty"`
	// RetryableFailure marks a terminal reservation whose provider attempt failed before any effect
	// and was journaled as a retryable failure: the exchange is still awaited and the next attempt may
	// reserve its own host call. It is set only by the verified progress that settles that attempt.
	RetryableFailure bool `json:"retryable_failure,omitempty"`
}

const (
	HostCallReserved = "reserved"
	HostCallTerminal = "terminal"
	HostCallRetired  = "retired"
	HostCallUnknown  = "unknown"
)

// Pins names the exact retained host state that a checkpoint belongs to.
// Every field is an ID, bounded disposition, count, or digest; no field is a
// prompt, response, proposal, argument, or tool output.
type Pins struct {
	WorkflowID                    string                  `json:"workflow_id"`
	SessionID                     string                  `json:"session_id"`
	RecoveryGeneration            uint64                  `json:"recovery_generation"`
	OwnerEpoch                    uint64                  `json:"owner_epoch"`
	OwnerIncarnationDigest        string                  `json:"owner_incarnation_digest"`
	RootIdentityDigest            string                  `json:"root_identity_digest"`
	StoreIdentityDigest           string                  `json:"store_identity_digest"`
	AdmissionIdentityDigest       string                  `json:"admission_identity_digest"`
	CommandIdentityDigest         string                  `json:"command_identity_digest"`
	AdminIdentityDigest           string                  `json:"admin_identity_digest"`
	TaskDigest                    string                  `json:"task_digest"`
	PromptDigest                  string                  `json:"prompt_digest"`
	PolicyDigest                  string                  `json:"policy_digest"`
	ProfileDigest                 string                  `json:"profile_digest"`
	ImageDigest                   string                  `json:"image_digest"`
	RuntimeDigest                 string                  `json:"runtime_digest"`
	D06ProvenanceDigest           string                  `json:"d06_provenance_digest"`
	PrivateGitIdentityDigest      string                  `json:"private_git_identity_digest"`
	StoreBaselineGenerationID     string                  `json:"store_baseline_generation_id"`
	StoreBaselineTreeDigest       string                  `json:"store_baseline_tree_digest"`
	StoreTipGenerationID          string                  `json:"store_tip_generation_id"`
	StoreTipTreeDigest            string                  `json:"store_tip_tree_digest"`
	StoreJournalHead              string                  `json:"store_journal_head,omitempty"`
	StoreJournalCount             uint64                  `json:"store_journal_count"`
	AdmissionJournalHead          string                  `json:"admission_journal_head,omitempty"`
	AdmissionJournalCount         uint64                  `json:"admission_journal_count"`
	EffectReservation             *EffectReservationPin   `json:"effect_reservation,omitempty"`
	HostCallReservation           *HostCallReservationPin `json:"host_call_reservation,omitempty"`
	ProviderTranscripts           []TranscriptPin         `json:"provider_transcripts"`
	ProviderProjectionDigest      string                  `json:"provider_projection_digest"`
	ProviderProjectionHead        string                  `json:"provider_projection_head,omitempty"`
	ProviderProjectionCount       uint64                  `json:"provider_projection_count"`
	ProviderDisposition           string                  `json:"provider_disposition"`
	RecoveryLineageDigest         string                  `json:"recovery_lineage_digest"`
	PreviousRecoveryLineageDigest string                  `json:"previous_recovery_lineage_digest,omitempty"`
	LastDecisionDigest            string                  `json:"last_decision_digest,omitempty"`
	LastResultDigest              string                  `json:"last_result_digest,omitempty"`
	PendingCallDisposition        string                  `json:"pending_call_disposition"`
	// ImportedPrefixDigest and PredecessorTurnReceiptDigest bind the predecessor conversation the
	// workflow started from. Both are empty for a workflow without a predecessor.
	ImportedPrefixDigest         string `json:"imported_prefix_digest,omitempty"`
	PredecessorTurnReceiptDigest string `json:"predecessor_turn_receipt_digest,omitempty"`
}

// OwnerStoreIdentity is passed from the SQLite owner to the host boundary so
// it can check the actual protected path and inode, not merely trust a caller's
// digest string.
type OwnerStoreIdentity struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// OwnerIncarnation is derived from Linux process/namespace/cgroup identity.
// TrustedRoot implementations must independently validate these facts through
// the host boundary before binding them to an owner epoch or checkpoint.
type OwnerIncarnation struct {
	Digest               string `json:"digest"`
	BootID               string `json:"boot_id"`
	PID                  int    `json:"pid"`
	StartTimeTicks       uint64 `json:"start_time_ticks"`
	PIDNamespaceInode    uint64 `json:"pid_namespace_inode"`
	EffectiveUID         uint32 `json:"effective_uid"`
	CgroupIdentityDigest string `json:"cgroup_identity_digest"`
}

// OwnerEpochIdentity identifies a previously acquired host owner that must be
// drained before the root binds a higher epoch.
type OwnerEpochIdentity struct {
	Epoch       uint64           `json:"epoch"`
	Incarnation OwnerIncarnation `json:"incarnation"`
}

// Limits are immutable for the original workflow. Recovery allowances may be
// zero (disabled) but may never exceed D20's two workflow-wide retries and
// clean restarts. Host calls, tokens, and wall deadline must be finite.
type Limits struct {
	MaxPreEffectRetries uint64    `json:"max_pre_effect_retries"`
	MaxCleanRestarts    uint64    `json:"max_clean_restarts"`
	MaxHostCalls        uint64    `json:"max_host_calls"`
	MaxTokens           uint64    `json:"max_tokens"`
	Deadline            time.Time `json:"deadline"`
}

// Counters are cumulative for WorkflowID, not for a session or generation.
// HostCalls and Tokens are charged at reservation time and are never refunded.
type Counters struct {
	PreEffectRetries uint64 `json:"pre_effect_retries"`
	CleanRestarts    uint64 `json:"clean_restarts"`
	HostCalls        uint64 `json:"host_calls"`
	Tokens           uint64 `json:"tokens"`
}

// Checkpoint contains only bounded identity/pin data and cumulative counters.
// ComputedCheckpointDigest commits to every other field, including the
// predecessor digest. The trusted external root authenticates the latest
// digest; this struct's hash alone does not.
type Checkpoint struct {
	SchemaVersion            string   `json:"schema_version"`
	Pins                     Pins     `json:"pins"`
	Limits                   Limits   `json:"limits"`
	Counters                 Counters `json:"counters"`
	PreviousCheckpointDigest string   `json:"previous_checkpoint_digest,omitempty"`
	ObservationDigest        string   `json:"observation_digest,omitempty"`
	ComputedCheckpointDigest string   `json:"computed_checkpoint_digest"`
}

// Owner is the durable, externally authenticated checkpoint store. It binds
// writes to its host-derived process incarnation and exact acquired epoch.
// LoadCheckpoint authenticates its latest head; missing state is never
// initialized implicitly. CompareAndSwapCheckpoint synchronizes the new
// checkpoint and external root head before returning success.
type Owner interface {
	TrustedRootIdentityDigest() string
	OwnerIncarnationDigest() string
	VerifyFence(ctx context.Context, workflow string, ownerEpoch uint64) error
	AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64) error
	LoadCheckpoint(ctx context.Context, workflow string) (Checkpoint, string, bool, error)
	CompareAndSwapCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, expectedDigest string, next Checkpoint, proof TransitionProof) (string, error)
}

// DrainVerifier is required by Open when the candidate epoch differs from the
// retained checkpoint epoch. It verifies that the prior owner/cell is drained
// without treating that old epoch as a current fence or granting authority.
type DrainVerifier interface {
	VerifyOwnersDrained(ctx context.Context, workflow string, afterEpoch, beforeEpoch uint64) error
}

// DeadlineVerifier asks the protected host root to evaluate the original UTC
// deadline against its persisted same-boot monotonic clock anchor. Production
// SQLite owners must implement it; test owners may use the local clock only.
type DeadlineVerifier interface {
	VerifyWorkflowDeadline(ctx context.Context, workflow string, deadline time.Time) error
}

// TerminalEffectVerifier joins the exact pre-dispatch reservation to the
// independent Store outcome and returns no authority. It must verify the
// complete primary/admission journal evidence represented by next.
type TerminalEffectVerifier interface {
	VerifyTerminalEffect(ctx context.Context, previous Checkpoint, binding EffectBinding, next Pins) error
}

// TerminalEffectOwner verifies a live same-incarnation owner and its current
// head while allowing only the original deadline/clock state to be paused.
// This capability exists solely for settling an already-consumed reservation.
type TerminalEffectOwner interface {
	VerifyTerminalEffectOwner(ctx context.Context, workflow string, ownerEpoch uint64, checkpointDigest string, deadline time.Time) error
}

// OwnerEpochAcquirer advances the externally fenced owner identity. The host
// boundary must bind the returned epoch to the actual owner incarnation and
// drain all superseded cells before allowing the checkpoint/reservation gap.
type OwnerEpochAcquirer interface {
	AcquireOwnerEpoch(ctx context.Context, workflow string, previousEpoch, nextEpoch uint64) error
}

// ProgressVerifier independently checks exact old journal prefixes, Store and
// admission joins, provider transcript projections, and last decision/result
// links before host-observed pins can be durably advanced.
type ProgressVerifier interface {
	VerifyProgress(ctx context.Context, previous Checkpoint, next Pins) error
}

// CapturedOperationVerifier binds an effect reservation to the exact active
// provider call and the host-authorized operation derived from that call. It
// must rejoin the protected ALLOW decision and typed operation specification;
// a digest or caller-supplied boolean alone is not sufficient.
type CapturedOperationVerifier interface {
	VerifyCapturedOperation(ctx context.Context, checkpoint Checkpoint, binding EffectBinding) error
}

// RecoveryVerifier reconstructs the complete ordered cross-journal history
// from independently held roots before approving a monotonic recovery tail.
// Retired requires an explicit host-controlled retirement record and proof that
// no operation authorization or effect intent was issued; nil must never mean
// "assume no effect". Returning nil is a trusted-host assertion and must not
// bind to provider-controlled data or a generic hash-only check.
type RecoveryVerifier interface {
	VerifyRecoveryObservation(ctx context.Context, previous Checkpoint, observed Pins) error
}

// TransitionProof is opaque outside this package. Only Ledger verification
// paths can create one, and the concrete Owner requires it for any pin change.
type TransitionProof struct {
	kind           transitionKind
	reservation    reservationKind
	previousDigest string
	previousPins   Pins
	observedPins   Pins
	targetPins     Pins
	previousCounts Counters
	targetCounts   Counters
	proofDigest    string
}

type transitionRecord struct {
	Kind           transitionKind  `json:"kind"`
	Reservation    reservationKind `json:"reservation_kind,omitempty"`
	PreviousDigest string          `json:"previous_digest"`
	PreviousPins   Pins            `json:"previous_pins"`
	ObservedPins   Pins            `json:"observed_pins"`
	TargetPins     Pins            `json:"target_pins"`
	PreviousCounts Counters        `json:"previous_counters"`
	TargetCounts   Counters        `json:"target_counters"`
	ProofDigest    string          `json:"proof_digest"`
}

func (p TransitionProof) record() transitionRecord {
	return transitionRecord{Kind: p.kind, Reservation: p.reservation, PreviousDigest: p.previousDigest,
		PreviousPins: clonePins(p.previousPins), ObservedPins: clonePins(p.observedPins),
		TargetPins: clonePins(p.targetPins), PreviousCounts: p.previousCounts, TargetCounts: p.targetCounts,
		ProofDigest: p.proofDigest}
}

func (r transitionRecord) proof() TransitionProof {
	return TransitionProof{kind: r.Kind, reservation: r.Reservation, previousDigest: r.PreviousDigest,
		previousPins: clonePins(r.PreviousPins), observedPins: clonePins(r.ObservedPins),
		targetPins: clonePins(r.TargetPins), previousCounts: r.PreviousCounts,
		targetCounts: r.TargetCounts, proofDigest: r.ProofDigest}
}

type transitionKind uint8

const (
	transitionNone transitionKind = iota
	transitionProgress
	transitionRecovery
	transitionReservation
	transitionTerminalEffect
)

type reservationKind uint8

const (
	reservationNone reservationKind = iota
	reservationPreEffectRetry
	reservationHostCall
	reservationEffect
	reservationCleanRestartPreEffectRetry
)

// VerifiedRecoveryObservation is a read-only, verifier-approved append-only
// observation, bound to the exact retained checkpoint digest. It can only be
// consumed once by the matching clean-restart reservation.
type VerifiedRecoveryObservation struct {
	proof TransitionProof
}

// VerifiedProgressObservation approves a monotonic in-session journal/pin
// advance. It does not grant effect authority.
type VerifiedProgressObservation struct {
	proof TransitionProof
}

// FreshWorkflowAuthorizer makes first-ledger creation an explicit, host-
// authorized operation. It is never a fallback for a missing/corrupt recovery
// checkpoint and is not called during clean recovery.
type FreshWorkflowAuthorizer interface {
	InitializeFreshCheckpoint(ctx context.Context, workflow string, ownerEpoch uint64, initial Checkpoint) (string, error)
}

// OpenOptions pins the checkpoint the caller has independently reconstructed
// and supplies a candidate epoch. For a newer epoch, Open asks the external
// root to atomically bind the current process incarnation after old owners/cells
// drain; it never requires the dead epoch to remain fenced. Open never writes a
// workflow checkpoint or creates missing state.
type OpenOptions struct {
	WorkflowID   string
	OwnerEpoch   uint64
	RetainedPins Pins
	Limits       Limits
}

// Remaining reports the still-available workflow allowances after a durable
// reservation.
type Remaining struct {
	PreEffectRetries uint64 `json:"pre_effect_retries"`
	CleanRestarts    uint64 `json:"clean_restarts"`
	HostCalls        uint64 `json:"host_calls"`
	Tokens           uint64 `json:"tokens"`
}

// Receipt proves the exact checkpoint reservation returned by the Owner. It
// carries no effect authority; callers still must complete their ordinary
// source, Store, provider, and operation-admission checks.
type Receipt struct {
	kind               receiptKind
	workflowID         string
	sessionID          string
	recoveryGeneration uint64
	ownerEpoch         uint64
	checkpointDigest   string
	previousDigest     string
	counters           Counters
	remaining          Remaining
	effectBinding      EffectBinding
	hostCallBinding    HostCallBinding
}

func (r Receipt) WorkflowID() string         { return r.workflowID }
func (r Receipt) SessionID() string          { return r.sessionID }
func (r Receipt) RecoveryGeneration() uint64 { return r.recoveryGeneration }
func (r Receipt) OwnerEpoch() uint64         { return r.ownerEpoch }
func (r Receipt) CheckpointDigest() string   { return r.checkpointDigest }
func (r Receipt) PreviousDigest() string     { return r.previousDigest }
func (r Receipt) Counters() Counters         { return r.counters }
func (r Receipt) Remaining() Remaining       { return r.remaining }
func (r Receipt) IsCleanRestart() bool       { return r.kind == receiptCleanRestart }
func (r Receipt) EffectBinding() (EffectBinding, bool) {
	if r.kind != receiptEffect && r.kind != receiptTerminalEffect {
		return EffectBinding{}, false
	}
	return r.effectBinding, r.effectBinding.OperationID != "" && validDigest(r.effectBinding.SpecDigest)
}
func (r Receipt) HostCallBinding() (HostCallBinding, bool) {
	if r.kind != receiptHostCall || !validHostCallBinding(r.hostCallBinding) {
		return HostCallBinding{}, false
	}
	return r.hostCallBinding, true
}

type receiptKind uint8

const (
	receiptFresh receiptKind = iota + 1
	receiptCleanRestart
	receiptPreEffectRetry
	receiptHostCall
	receiptEffect
	receiptProgress
	receiptTerminalEffect
)

// Ledger is a validated snapshot. Mutations use durable compare-and-swap and
// replace this snapshot only after the Owner confirms the trusted root commit.
type Ledger struct {
	mu                     sync.Mutex
	owner                  Owner
	workflowID             string
	fencedEpoch            uint64
	incarnationMatches     bool
	restartReceiptVerified bool
	checkpoint             Checkpoint
	digest                 string
}

// Open reads and validates the exact retained checkpoint. A checkpoint from a
// different process incarnation, a newer owner epoch, or an unsettled provider
// state is inspection-only; it cannot reserve fresh authority before a verified
// clean restart or explicit in-session settlement.
func Open(ctx context.Context, owner Owner, options OpenOptions) (*Ledger, error) {
	if ctx == nil || owner == nil || !validID(options.WorkflowID) || options.OwnerEpoch == 0 ||
		options.RetainedPins.WorkflowID != options.WorkflowID || options.OwnerEpoch < options.RetainedPins.OwnerEpoch {
		return nil, fmt.Errorf("%w: invalid open options", ErrInvalid)
	}
	rootIdentity := owner.TrustedRootIdentityDigest()
	incarnation := owner.OwnerIncarnationDigest()
	if !validDigest(rootIdentity) || options.RetainedPins.RootIdentityDigest != rootIdentity ||
		!validDigest(incarnation) {
		return nil, fmt.Errorf("%w: retained root or owner-incarnation identity is invalid", ErrOwnerFence)
	}
	options.Limits = normalizeLimits(options.Limits)
	if err := validateLimits(options.Limits, time.Now()); err != nil {
		return nil, err
	}
	if err := validatePins(options.RetainedPins); err != nil {
		return nil, err
	}
	cp, digest, exists, err := owner.LoadCheckpoint(ctx, options.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("load workflow checkpoint: %w", err)
	}
	if !exists {
		return nil, ErrMissing
	}
	if err := validateCheckpoint(cp, digest); err != nil {
		return nil, err
	}
	if cp.Pins.WorkflowID != options.WorkflowID || !reflect.DeepEqual(cp.Pins, options.RetainedPins) ||
		!reflect.DeepEqual(cp.Limits, options.Limits) {
		return nil, fmt.Errorf("%w: retained pins or immutable limits differ", ErrRollback)
	}
	if options.OwnerEpoch < cp.Pins.OwnerEpoch {
		return nil, fmt.Errorf("%w: candidate owner epoch precedes retained checkpoint", ErrOwnerFence)
	}
	if options.OwnerEpoch > cp.Pins.OwnerEpoch {
		acquirer, ok := owner.(OwnerEpochAcquirer)
		if !ok {
			return nil, fmt.Errorf("%w: owner cannot acquire an externally fenced replacement epoch", ErrOwnerFence)
		}
		if err := acquirer.AcquireOwnerEpoch(ctx, options.WorkflowID, cp.Pins.OwnerEpoch, options.OwnerEpoch); err != nil {
			return nil, fmt.Errorf("%w: replacement epoch acquisition: %v", ErrOwnerFence, err)
		}
		drain, ok := owner.(DrainVerifier)
		if !ok {
			return nil, fmt.Errorf("%w: owner cannot verify drain of superseded owner epochs", ErrOwnerFence)
		}
		if err := drain.VerifyOwnersDrained(ctx, options.WorkflowID, cp.Pins.OwnerEpoch, options.OwnerEpoch); err != nil {
			return nil, fmt.Errorf("%w: superseded owner/cell drain: %v", ErrOwnerFence, err)
		}
	}
	if err := owner.VerifyFence(ctx, options.WorkflowID, options.OwnerEpoch); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOwnerFence, err)
	}
	if err := validateCheckpointAdmission(cp, time.Now()); err != nil && !errors.Is(err, ErrPendingUnknown) {
		return nil, err
	}
	incarnationMatches := options.OwnerEpoch == cp.Pins.OwnerEpoch && incarnation == cp.Pins.OwnerIncarnationDigest
	return &Ledger{owner: owner, workflowID: options.WorkflowID, fencedEpoch: options.OwnerEpoch,
		incarnationMatches: incarnationMatches, restartReceiptVerified: cp.Counters.CleanRestarts == 0,
		checkpoint: cp, digest: digest}, nil
}

// InitializeFresh writes the first checkpoint for an explicitly authorized
// new workflow. This is never used as a fallback for missing recovery state.
func InitializeFresh(ctx context.Context, owner Owner, pins Pins, limits Limits) (Receipt, error) {
	if ctx == nil || owner == nil || pins.OwnerEpoch == 0 || !validID(pins.WorkflowID) {
		return Receipt{}, fmt.Errorf("%w: invalid fresh workflow inputs", ErrInvalid)
	}
	freshOwner, ok := owner.(FreshWorkflowAuthorizer)
	if !ok {
		return Receipt{}, ErrFreshAuthorization
	}
	rootIdentity := owner.TrustedRootIdentityDigest()
	incarnation := owner.OwnerIncarnationDigest()
	if !validDigest(rootIdentity) || pins.RootIdentityDigest != rootIdentity ||
		!validDigest(incarnation) || pins.OwnerIncarnationDigest != incarnation {
		return Receipt{}, fmt.Errorf("%w: fresh pins differ from the trusted root or owner-incarnation identity", ErrFreshAuthorization)
	}
	limits = normalizeLimits(limits)
	if err := validateLimits(limits, time.Now()); err != nil {
		return Receipt{}, err
	}
	if err := validatePins(pins); err != nil {
		return Receipt{}, err
	}
	if pins.RecoveryGeneration != 1 || pins.ProviderProjectionCount != 0 || len(pins.ProviderTranscripts) != 0 ||
		pins.ProviderDisposition != DispositionNone || pins.PendingCallDisposition != PendingCallNone ||
		pins.PreviousRecoveryLineageDigest != "" {
		return Receipt{}, fmt.Errorf("%w: a fresh workflow must start with an empty recovery lineage and provider history", ErrInvalid)
	}
	cp := Checkpoint{SchemaVersion: checkpointSchema, Pins: clonePins(pins), Limits: limits, Counters: Counters{}}
	cp.ComputedCheckpointDigest = computeCheckpointDigest(cp)
	newDigest, err := freshOwner.InitializeFreshCheckpoint(ctx, pins.WorkflowID, pins.OwnerEpoch, cp)
	if err != nil {
		return Receipt{}, errors.Join(ErrCAS, err)
	}
	if newDigest != cp.ComputedCheckpointDigest {
		return Receipt{}, fmt.Errorf("%w: owner returned a different fresh checkpoint digest", ErrRollback)
	}
	return receiptFor(cp, newDigest, receiptFresh), nil
}

// ReserveCleanRestart consumes one workflow-wide clean restart and seals the
// new host-generated session/recovery lineage before any fresh worker,
// provider conversation, or authority is opened.
func (l *Ledger) ReserveCleanRestart(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins) (Receipt, error) {
	return l.reserveCleanRestart(ctx, observation, nextPins, false)
}

// ReserveCleanRestartWithPreEffectRetry atomically consumes one clean restart and
// one pre-effect retry for a verified retired-unexecuted provider call. Both
// allowances and the new lineage commit in one checkpoint; the returned receipt
// is bound to that final checkpoint, so no later reservation can stale it.
func (l *Ledger) ReserveCleanRestartWithPreEffectRetry(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins) (Receipt, error) {
	return l.reserveCleanRestart(ctx, observation, nextPins, true)
}

func (l *Ledger) reserveCleanRestart(ctx context.Context, observation VerifiedRecoveryObservation, nextPins Pins, chargeRetry bool) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if l.checkpoint.Counters.CleanRestarts >= l.checkpoint.Limits.MaxCleanRestarts {
		return Receipt{}, ErrExhausted
	}
	if chargeRetry && l.checkpoint.Counters.PreEffectRetries >= l.checkpoint.Limits.MaxPreEffectRetries {
		return Receipt{}, ErrExhausted
	}
	if err := validatePins(nextPins); err != nil {
		return Receipt{}, err
	}
	if nextPins.WorkflowID != l.workflowID || nextPins.OwnerEpoch != l.fencedEpoch ||
		nextPins.OwnerEpoch <= l.checkpoint.Pins.OwnerEpoch ||
		nextPins.RecoveryGeneration != l.checkpoint.Pins.RecoveryGeneration+1 ||
		nextPins.SessionID == l.checkpoint.Pins.SessionID ||
		nextPins.OwnerIncarnationDigest != l.owner.OwnerIncarnationDigest() {
		return Receipt{}, fmt.Errorf("%w: clean restart must bind a newer fenced epoch, session, and recovery generation", ErrInvalid)
	}
	proof := observation.proof
	if proof.kind != transitionRecovery || proof.reservation != reservationNone || proof.previousDigest != l.digest ||
		!reflect.DeepEqual(proof.previousPins, l.checkpoint.Pins) || !validDigest(proof.proofDigest) {
		return Receipt{}, ErrObservation
	}
	if err := validPinsMonotonic(proof.previousPins, proof.observedPins); err != nil {
		return Receipt{}, fmt.Errorf("%w: recovery observation is not monotonic: %v", ErrObservation, err)
	}
	if err := validateRestartPins(proof.observedPins, nextPins, l.digest); err != nil {
		return Receipt{}, err
	}
	if chargeRetry {
		if !retiredUnleasedCallObserved(proof.observedPins) {
			return Receipt{}, fmt.Errorf("%w: pre-effect retry requires an observed retired unleased call", ErrObservation)
		}
		proof.reservation = reservationCleanRestartPreEffectRetry
	}
	proof.targetPins = clonePins(nextPins)
	next := l.checkpoint
	next.Pins = clonePins(nextPins)
	next.Counters.CleanRestarts++
	if chargeRetry {
		next.Counters.PreEffectRetries++
	}
	next.PreviousCheckpointDigest = l.digest
	next.ObservationDigest = proof.proofDigest
	next.ComputedCheckpointDigest = ""
	return l.commit(ctx, next, proof, receiptCleanRestart)
}

// ReservePreEffectRetry durably charges one pre-effect retry in the current
// active session/owner epoch. Replacement supervisors must reserve a clean
// restart first; retries do not change or replenish workflow identity/budgets.
func (l *Ledger) ReservePreEffectRetry(ctx context.Context) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateCheckpointAdmission(l.checkpoint, time.Now()); err != nil {
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.PreEffectRetries >= l.checkpoint.Limits.MaxPreEffectRetries {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	next.Counters.PreEffectRetries++
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationPreEffectRetry, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptPreEffectRetry)
}

// ReserveHostCall consumes a provider/host call and a conservative token
// upper bound before dispatch. Ambiguous usage, cancellation, and crashes keep
// the reservation charged; there is intentionally no refund API.
func (l *Ledger) ReserveHostCall(ctx context.Context, binding HostCallBinding) (Receipt, error) {
	if l == nil || ctx == nil || !validHostCallBinding(binding) {
		return Receipt{}, fmt.Errorf("%w: invalid host call reservation", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateHostCallReservationAdmission(l.checkpoint, time.Now()); err != nil {
		return Receipt{}, err
	}
	if len(l.checkpoint.Pins.ProviderTranscripts) == 0 {
		return Receipt{}, ErrObservation
	}
	if previous := l.checkpoint.Pins.HostCallReservation; previous != nil && previous.Binding.CallID == binding.CallID {
		return Receipt{}, fmt.Errorf("%w: provider call identity was already consumed", ErrObservation)
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.HostCalls >= l.checkpoint.Limits.MaxHostCalls ||
		binding.TokenUpperBound > l.checkpoint.Limits.MaxTokens-l.checkpoint.Counters.Tokens {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	next.Counters.HostCalls++
	next.Counters.Tokens += binding.TokenUpperBound
	next.Pins.HostCallReservation = &HostCallReservationPin{Binding: binding, Disposition: HostCallReserved}
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationHostCall, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptHostCall)
}

// ReserveEffect consumes one workflow-wide host-call allowance before an
// irreversible governed effect. It charges no provider tokens and has no
// refund path if the effect callback or process later fails.
func (l *Ledger) ReserveEffect(ctx context.Context, binding EffectBinding, verifier CapturedOperationVerifier) (Receipt, error) {
	if l == nil || ctx == nil || !validEffectBinding(binding) {
		return Receipt{}, fmt.Errorf("%w: invalid effect reservation", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := validateEffectReservationAdmission(ctx, l.checkpoint, binding, verifier, time.Now()); err != nil {
		return Receipt{}, err
	}
	if len(l.checkpoint.Pins.ProviderTranscripts) == 0 {
		return Receipt{}, ErrObservation
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	if l.checkpoint.Counters.HostCalls >= l.checkpoint.Limits.MaxHostCalls {
		return Receipt{}, ErrExhausted
	}
	next := l.checkpoint
	next.Pins = clonePins(l.checkpoint.Pins)
	if next.Pins.EffectReservation != nil &&
		next.Pins.EffectReservation.Disposition != EffectReservationTerminal &&
		next.Pins.EffectReservation.Disposition != EffectReservationRetired {
		return Receipt{}, ErrPendingUnknown
	}
	if next.Pins.ProviderDisposition == DispositionAwaitingGateDecision {
		captured := next.Pins.HostCallReservation.CapturedCall
		captured.Disposition = CapturedCallConsumed
		captured.RetirementDigest = ""
	}
	next.Pins.EffectReservation = &EffectReservationPin{Binding: binding, Disposition: EffectReservationReserved}
	next.Counters.HostCalls++
	next.PreviousCheckpointDigest = l.digest
	next.ComputedCheckpointDigest = ""
	proof := makeReservationProof(reservationEffect, l.digest, l.checkpoint, next)
	return l.commit(ctx, next, proof, receiptEffect)
}

// RecordTerminalEffectOutcome accounts for a known, independently verified
// Store outcome from an effect whose authority was already durably consumed.
// It can advance only terminal Store pins and the disposition of that exact
// reservation. It never restores counters or permits another effect.
func (l *Ledger) RecordTerminalEffectOutcome(ctx context.Context, reservation Receipt,
	verifier TerminalEffectVerifier, nextPins Pins) (Receipt, error) {
	if l == nil || ctx == nil || verifier == nil || reservation.kind != receiptEffect {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.owner == nil || reservation.workflowID != l.workflowID ||
		reservation.sessionID != l.checkpoint.Pins.SessionID ||
		reservation.recoveryGeneration != l.checkpoint.Pins.RecoveryGeneration ||
		reservation.ownerEpoch != l.fencedEpoch || reservation.ownerEpoch != l.checkpoint.Pins.OwnerEpoch ||
		reservation.checkpointDigest != l.digest || reservation.previousDigest != l.checkpoint.PreviousCheckpointDigest ||
		reservation.counters != l.checkpoint.Counters ||
		l.checkpoint.Pins.EffectReservation == nil ||
		l.checkpoint.Pins.EffectReservation.Disposition != EffectReservationReserved ||
		reservation.effectBinding != l.checkpoint.Pins.EffectReservation.Binding {
		l.mu.Unlock()
		return Receipt{}, ErrObservation
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		l.mu.Unlock()
		return Receipt{}, ErrOwnerFence
	}
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	owner := l.owner
	workflowID := l.workflowID
	deadline := l.checkpoint.Limits.Deadline
	binding := l.checkpoint.Pins.EffectReservation.Binding
	l.mu.Unlock()

	terminalOwner, ok := owner.(TerminalEffectOwner)
	if !ok {
		return Receipt{}, fmt.Errorf("%w: owner cannot verify same-incarnation terminal accounting", ErrOwnerFence)
	}
	if err := terminalOwner.VerifyTerminalEffectOwner(ctx, workflowID, fencedEpoch, previousDigest, deadline); err != nil {
		return Receipt{}, fmt.Errorf("%w: live owner/fence check for terminal accounting: %v", ErrOwnerFence, err)
	}
	if err := validateTerminalEffectPins(previous.Pins, nextPins); err != nil {
		return Receipt{}, err
	}
	if err := verifier.VerifyTerminalEffect(ctx, previous, binding, clonePins(nextPins)); err != nil {
		return Receipt{}, fmt.Errorf("%w: terminal Store outcome verifier: %v", ErrObservation, err)
	}

	next := previous
	next.Pins = clonePins(nextPins)
	next.PreviousCheckpointDigest = previousDigest
	next.ObservationDigest = digestTerminalEffect(previousDigest, previous.Pins, next.Pins, previous.Counters)
	next.ComputedCheckpointDigest = ""
	proof := TransitionProof{kind: transitionTerminalEffect, reservation: reservationEffect,
		previousDigest: previousDigest, previousPins: clonePins(previous.Pins),
		observedPins: clonePins(next.Pins), targetPins: clonePins(next.Pins),
		previousCounts: previous.Counters, targetCounts: next.Counters,
		proofDigest: next.ObservationDigest}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != previousDigest || l.fencedEpoch != fencedEpoch || !reflect.DeepEqual(l.checkpoint, previous) {
		return Receipt{}, ErrCAS
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrOwnerFence
	}
	return l.commit(ctx, next, proof, receiptTerminalEffect)
}

// AdvanceCheckpoint records newly verified monotonic host observations. It
// cannot change identity, deadline, limits, or counters and never grants fresh
// authority. Prior transcript pins are immutable; only the active transcript
// may extend its protected journal/projection head.
func (l *Ledger) AdvanceCheckpoint(ctx context.Context, verifier ProgressVerifier, nextPins Pins) (Receipt, error) {
	if l == nil || ctx == nil {
		return Receipt{}, fmt.Errorf("%w: nil ledger or context", ErrInvalid)
	}
	if verifier == nil {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.checkpoint.Pins.EffectReservation != nil &&
		l.checkpoint.Pins.EffectReservation.Disposition == EffectReservationReserved {
		l.mu.Unlock()
		return Receipt{}, ErrPendingUnknown
	}
	if err := l.checkDeadline(ctx); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		l.mu.Unlock()
		return Receipt{}, ErrRestartRequired
	}
	if err := validatePins(nextPins); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	if err := validateObservationAdvance(l.checkpoint.Pins, nextPins); err != nil {
		l.mu.Unlock()
		return Receipt{}, err
	}
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	l.mu.Unlock()
	if err := verifier.VerifyProgress(ctx, previous, clonePins(nextPins)); err != nil {
		return Receipt{}, fmt.Errorf("%w: progress verifier: %v", ErrObservation, err)
	}
	proof := TransitionProof{kind: transitionProgress, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), observedPins: clonePins(nextPins),
		targetPins: clonePins(nextPins), proofDigest: digestObservation(transitionProgress, previousDigest, previous.Pins, nextPins)}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != previousDigest || l.fencedEpoch != fencedEpoch || !reflect.DeepEqual(l.checkpoint, previous) {
		return Receipt{}, ErrCAS
	}
	if err := l.checkDeadline(ctx); err != nil {
		return Receipt{}, err
	}
	if err := l.requireCurrentIncarnation(); err != nil {
		return Receipt{}, ErrRestartRequired
	}
	next := previous
	next.Pins = clonePins(nextPins)
	next.PreviousCheckpointDigest = previousDigest
	next.ObservationDigest = proof.proofDigest
	next.ComputedCheckpointDigest = ""
	return l.commit(ctx, next, proof, receiptProgress)
}

// RegisterInitialTranscript durably binds the first empty provider conversation
// to the workflow before ReserveHostCall can charge or dispatch an exchange.
// Clean restarts append their next conversation through ReserveCleanRestart and
// are not eligible for this path.
func (l *Ledger) RegisterInitialTranscript(ctx context.Context, verifier ProgressVerifier, transcript TranscriptPin) (Receipt, error) {
	if l == nil || ctx == nil || verifier == nil {
		return Receipt{}, ErrObservation
	}
	l.mu.Lock()
	if l.checkpoint.Pins.RecoveryGeneration != 1 || len(l.checkpoint.Pins.ProviderTranscripts) != 0 ||
		l.checkpoint.Pins.ProviderDisposition != DispositionNone || l.checkpoint.Pins.PendingCallDisposition != PendingCallNone {
		l.mu.Unlock()
		return Receipt{}, fmt.Errorf("%w: initial transcript can only be registered once before provider use", ErrInvalid)
	}
	nextPins := clonePins(l.checkpoint.Pins)
	nextPins.ProviderTranscripts = []TranscriptPin{transcript}
	nextPins.ProviderDisposition = DispositionAwaitingHostPrompt
	nextPins.ProviderProjectionCount = 0
	nextPins.ProviderProjectionHead = ""
	nextPins.ProviderProjectionDigest = digestTranscriptPins(nextPins.ProviderTranscripts)
	// AdvanceCheckpoint owns the mutex and executes the verifier outside this
	// registration-specific state check.
	l.mu.Unlock()
	return l.AdvanceCheckpoint(ctx, verifier, nextPins)
}

// ObserveRecovery verifies every retained journal/transcript join and returns
// an opaque candidate tail. It performs no write, owner transition, or fresh
// authority operation.
func (l *Ledger) ObserveRecovery(ctx context.Context, verifier RecoveryVerifier, observed Pins) (VerifiedRecoveryObservation, error) {
	if l == nil || ctx == nil || verifier == nil {
		return VerifiedRecoveryObservation{}, ErrObservation
	}
	l.mu.Lock()
	previous := l.checkpoint
	previous.Pins = clonePins(previous.Pins)
	previousDigest := l.digest
	fencedEpoch := l.fencedEpoch
	l.mu.Unlock()
	if fencedEpoch <= previous.Pins.OwnerEpoch {
		return VerifiedRecoveryObservation{}, ErrRestartRequired
	}
	if err := validatePins(observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := validPinsMonotonic(previous.Pins, observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := validateObservedDisposition(observed); err != nil {
		return VerifiedRecoveryObservation{}, err
	}
	if err := verifier.VerifyRecoveryObservation(ctx, previous, clonePins(observed)); err != nil {
		return VerifiedRecoveryObservation{}, fmt.Errorf("%w: recovery verifier: %v", ErrObservation, err)
	}
	proof := TransitionProof{kind: transitionRecovery, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), observedPins: clonePins(observed),
		proofDigest: digestObservation(transitionRecovery, previousDigest, previous.Pins, observed)}
	return VerifiedRecoveryObservation{proof: proof}, nil
}

func (l *Ledger) commit(ctx context.Context, next Checkpoint, proof TransitionProof, kind receiptKind) (Receipt, error) {
	next.ComputedCheckpointDigest = computeCheckpointDigest(next)
	var validationErr error
	if kind == receiptProgress || kind == receiptTerminalEffect || kind == receiptHostCall || kind == receiptEffect {
		validationErr = validateCheckpoint(next, next.ComputedCheckpointDigest)
	} else {
		validationErr = validateCheckpointAdmission(next, time.Now())
	}
	if validationErr != nil {
		return Receipt{}, validationErr
	}
	newDigest, err := l.owner.CompareAndSwapCheckpoint(ctx, l.workflowID, next.Pins.OwnerEpoch, l.digest, next, proof)
	if err != nil {
		if errors.Is(err, ErrExhausted) {
			// The owner returns ErrExhausted before its transaction commits: either
			// the effect headroom gate refused, or a counter is over its limit. Neither
			// wrote anything, so neither is a compare-and-swap conflict.
			return Receipt{}, err
		}
		return Receipt{}, errors.Join(ErrCAS, err)
	}
	if newDigest != next.ComputedCheckpointDigest {
		return Receipt{}, fmt.Errorf("%w: owner returned a different checkpoint digest", ErrRollback)
	}
	l.checkpoint = next
	l.digest = newDigest
	if kind == receiptCleanRestart {
		l.incarnationMatches = true
		l.restartReceiptVerified = false
	} else if kind == receiptFresh {
		l.restartReceiptVerified = true
	}
	return receiptFor(next, newDigest, kind), nil
}

func (l *Ledger) checkDeadline(ctx context.Context) error {
	if l == nil {
		return ErrMissing
	}
	if verifier, ok := l.owner.(DeadlineVerifier); ok {
		if err := verifier.VerifyWorkflowDeadline(ctx, l.workflowID, l.checkpoint.Limits.Deadline); err != nil {
			return err
		}
	}
	if !time.Now().Before(l.checkpoint.Limits.Deadline) {
		return ErrDeadline
	}
	return nil
}

func (l *Ledger) requireCurrentIncarnation() error {
	if l == nil || l.owner == nil || !l.incarnationMatches || !l.restartReceiptVerified ||
		l.fencedEpoch != l.checkpoint.Pins.OwnerEpoch ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		return ErrRestartRequired
	}
	return nil
}

func (l *Ledger) Snapshot() (Checkpoint, string) {
	if l == nil {
		return Checkpoint{}, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	copy := l.checkpoint
	copy.Pins = clonePins(copy.Pins)
	return copy, l.digest
}

// VerifyCleanRestartReceipt confirms that receipt is the exact latest,
// externally authenticated clean-restart checkpoint for expectedPins.
func (l *Ledger) VerifyCleanRestartReceipt(ctx context.Context, receipt Receipt, expectedPins Pins) error {
	if l == nil || ctx == nil || receipt.kind != receiptCleanRestart {
		return ErrObservation
	}
	l.mu.Lock()
	if !l.incarnationMatches || l.owner == nil ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		l.mu.Unlock()
		return ErrOwnerFence
	}
	if receipt.workflowID != l.workflowID || receipt.ownerEpoch != l.checkpoint.Pins.OwnerEpoch ||
		receipt.sessionID != l.checkpoint.Pins.SessionID || receipt.recoveryGeneration != l.checkpoint.Pins.RecoveryGeneration ||
		receipt.checkpointDigest != l.digest || receipt.previousDigest != l.checkpoint.PreviousCheckpointDigest ||
		!reflect.DeepEqual(expectedPins, l.checkpoint.Pins) || receipt.counters != l.checkpoint.Counters {
		l.mu.Unlock()
		return ErrObservation
	}
	owner := l.owner
	workflowID := l.workflowID
	ownerEpoch := receipt.ownerEpoch
	expected := l.checkpoint
	expected.Pins = clonePins(expected.Pins)
	l.mu.Unlock()
	cp, digest, exists, err := owner.LoadCheckpoint(ctx, workflowID)
	if err != nil || !exists || digest != receipt.checkpointDigest || !reflect.DeepEqual(cp, expected) {
		return errors.Join(ErrRollback, err)
	}
	if err := owner.VerifyFence(ctx, workflowID, ownerEpoch); err != nil {
		return fmt.Errorf("%w: receipt owner fence: %v", ErrOwnerFence, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.digest != receipt.checkpointDigest || !reflect.DeepEqual(l.checkpoint, expected) ||
		l.owner.OwnerIncarnationDigest() != l.checkpoint.Pins.OwnerIncarnationDigest {
		return ErrObservation
	}
	l.restartReceiptVerified = true
	return nil
}

func receiptFor(cp Checkpoint, digest string, kind receiptKind) Receipt {
	receipt := Receipt{
		kind: kind, workflowID: cp.Pins.WorkflowID, sessionID: cp.Pins.SessionID,
		recoveryGeneration: cp.Pins.RecoveryGeneration, ownerEpoch: cp.Pins.OwnerEpoch,
		checkpointDigest: digest, previousDigest: cp.PreviousCheckpointDigest,
		counters: cp.Counters, remaining: Remaining{
			PreEffectRetries: subtract(cp.Limits.MaxPreEffectRetries, cp.Counters.PreEffectRetries),
			CleanRestarts:    subtract(cp.Limits.MaxCleanRestarts, cp.Counters.CleanRestarts),
			HostCalls:        subtract(cp.Limits.MaxHostCalls, cp.Counters.HostCalls),
			Tokens:           subtract(cp.Limits.MaxTokens, cp.Counters.Tokens),
		},
	}
	if (kind == receiptEffect || kind == receiptTerminalEffect) && cp.Pins.EffectReservation != nil {
		receipt.effectBinding = cp.Pins.EffectReservation.Binding
	}
	if kind == receiptHostCall && cp.Pins.HostCallReservation != nil {
		receipt.hostCallBinding = cp.Pins.HostCallReservation.Binding
	}
	return receipt
}

func subtract(maximum, used uint64) uint64 {
	if used >= maximum {
		return 0
	}
	return maximum - used
}

func normalizeLimits(limits Limits) Limits {
	if !limits.Deadline.IsZero() {
		limits.Deadline = limits.Deadline.Round(0).UTC()
	}
	return limits
}

func validateLimits(limits Limits, now time.Time) error {
	if limits.MaxPreEffectRetries > 2 || limits.MaxCleanRestarts > 2 ||
		limits.MaxHostCalls == 0 || limits.MaxTokens == 0 || limits.MaxHostCalls > maxSQLiteInteger ||
		limits.MaxTokens > maxSQLiteInteger || limits.Deadline.IsZero() {
		return fmt.Errorf("%w: limits must be finite, deadline-bearing, and within D20 totals", ErrInvalid)
	}
	if !now.Before(limits.Deadline) {
		return ErrDeadline
	}
	if limits.Deadline.Location() != time.UTC && limits.Deadline.Location().String() != "UTC" {
		return fmt.Errorf("%w: deadline must be normalized to UTC", ErrInvalid)
	}
	return nil
}

func validateCheckpointAdmission(cp Checkpoint, now time.Time) error {
	if err := validateCheckpoint(cp, cp.ComputedCheckpointDigest); err != nil {
		return err
	}
	if err := validateLimits(cp.Limits, now); err != nil {
		return err
	}
	if cp.Pins.PendingCallDisposition == PendingCallInFlight || cp.Pins.PendingCallDisposition == PendingCallCaptured ||
		cp.Pins.PendingCallDisposition == PendingCallUnknown ||
		isUnsettledDisposition(cp.Pins.ProviderDisposition) ||
		cp.Pins.EffectReservation != nil && (cp.Pins.EffectReservation.Disposition == EffectReservationReserved ||
			cp.Pins.EffectReservation.Disposition == EffectReservationUnknown) ||
		cp.Pins.HostCallReservation != nil && (cp.Pins.HostCallReservation.Disposition == HostCallReserved ||
			cp.Pins.HostCallReservation.Disposition == HostCallUnknown) {
		return ErrPendingUnknown
	}
	for _, transcript := range cp.Pins.ProviderTranscripts {
		if isUnsettledDisposition(transcript.Disposition) {
			return ErrPendingUnknown
		}
	}
	return nil
}

// validateHostCallReservationAdmission permits one additional provider
// reservation after a successful tool result has been durably checkpointed.
// The ordinary admission gate remains closed to AwaitingProviderExchange; the
// exception is local to ReserveHostCall and requires the exact preceding
// captured effect to be terminal in the current conversation/fence.
func validateHostCallReservationAdmission(cp Checkpoint, now time.Time) error {
	if err := validateCheckpointAdmission(cp, now); err == nil {
		return nil
	} else if !errors.Is(err, ErrPendingUnknown) {
		return err
	}
	if !isPostResultProviderExchangeReady(cp) && !isRetryableFailureProviderExchangeReady(cp) &&
		!isDeniedResultProviderExchangeReady(cp) {
		return ErrPendingUnknown
	}
	return nil
}

// deniedCallAwaitsProviderAnswer is the state a released DENY leaves: the active conversation awaits
// the model's answer to the DENY result, and the denied call, captured in that conversation under the
// current fence, is retired with a commitment to its journaled DENY decision and result. A retired
// call can never be consumed: effect admission requires an available capture.
func deniedCallAwaitsProviderAnswer(p Pins) bool {
	if p.ProviderDisposition != DispositionAwaitingProviderExchange || p.PendingCallDisposition != PendingCallRetired ||
		len(p.ProviderTranscripts) == 0 || p.HostCallReservation == nil ||
		p.HostCallReservation.Disposition != HostCallTerminal || p.HostCallReservation.CapturedCall == nil {
		return false
	}
	active := p.ProviderTranscripts[len(p.ProviderTranscripts)-1]
	captured := p.HostCallReservation.CapturedCall
	return active.Disposition == DispositionAwaitingProviderExchange && captured.Disposition == CapturedCallRetired &&
		validDigest(captured.RetirementDigest) && captured.ConversationID == active.ConversationID &&
		captured.RecoveryGeneration == p.RecoveryGeneration && captured.OwnerEpoch == p.OwnerEpoch
}

// isDeniedResultProviderExchangeReady admits the provider reservation that answers a released DENY.
// The new reservation replaces the retired capture, so its call identity cannot be charged again.
func isDeniedResultProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if !deniedCallAwaitsProviderAnswer(pins) {
		return false
	}
	for _, transcript := range pins.ProviderTranscripts[:len(pins.ProviderTranscripts)-1] {
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	effect := pins.EffectReservation
	return effect == nil || effect.Disposition == EffectReservationTerminal || effect.Disposition == EffectReservationRetired
}

// isRetryableFailureProviderExchangeReady admits one further provider reservation after a
// failed, retryable provider attempt was journaled and its reservation settled: the exchange is
// still awaited, the failed attempt's host call is terminal with an outcome digest and captured
// nothing, and no effect or captured call is open. Whether the retry bound holds is enforced by the
// provider conversation and its replay; the host-call and token ceilings charged by every
// reservation bound the total here. The prior call may be settled by its effect or retired by a DENY.
func isRetryableFailureProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if pins.ProviderDisposition != DispositionAwaitingProviderExchange || len(pins.ProviderTranscripts) == 0 ||
		pins.PendingCallDisposition != PendingCallNone && pins.PendingCallDisposition != PendingCallTerminal &&
			pins.PendingCallDisposition != PendingCallRetired {
		return false
	}
	activeIndex := len(pins.ProviderTranscripts) - 1
	if pins.ProviderTranscripts[activeIndex].Disposition != DispositionAwaitingProviderExchange {
		return false
	}
	for index, transcript := range pins.ProviderTranscripts {
		if index != activeIndex && transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	host, effect := pins.HostCallReservation, pins.EffectReservation
	if host == nil || host.Disposition != HostCallTerminal || !host.RetryableFailure || host.CapturedCall != nil ||
		!validDigest(host.OutcomeDigest) || host.RetirementDigest != "" {
		return false
	}
	return effect == nil || effect.Disposition == EffectReservationTerminal || effect.Disposition == EffectReservationRetired
}

func isPostResultProviderExchangeReady(cp Checkpoint) bool {
	pins := cp.Pins
	if pins.ProviderDisposition != DispositionAwaitingProviderExchange ||
		pins.PendingCallDisposition != PendingCallTerminal || len(pins.ProviderTranscripts) == 0 {
		return false
	}
	activeIndex := len(pins.ProviderTranscripts) - 1
	active := pins.ProviderTranscripts[activeIndex]
	if active.Disposition != DispositionAwaitingProviderExchange {
		return false
	}
	for index, transcript := range pins.ProviderTranscripts {
		if index == activeIndex {
			continue
		}
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return false
		}
	}
	host := pins.HostCallReservation
	effect := pins.EffectReservation
	if host == nil || host.Disposition != HostCallTerminal || host.CapturedCall != nil ||
		!validDigest(host.OutcomeDigest) || host.RetirementDigest != "" ||
		effect == nil || effect.Disposition != EffectReservationTerminal ||
		!validDigest(effect.OutcomeDigest) || effect.RetirementDigest != "" {
		return false
	}
	binding := effect.Binding
	return binding.WorkflowID == pins.WorkflowID && binding.ConversationID == active.ConversationID &&
		binding.HostCallID == host.Binding.CallID && binding.HostCallOutcomeDigest == host.OutcomeDigest &&
		binding.RecoveryGeneration == pins.RecoveryGeneration && binding.OwnerEpoch == pins.OwnerEpoch
}

// validateEffectReservationAdmission keeps the ordinary admission gate intact.
// The only exception is one captured provider call that has an exact terminal
// host-call reservation and current-fence capture pin. The verifier must join
// that capture to the protected ALLOW decision and typed host operation.
func validateEffectReservationAdmission(ctx context.Context, cp Checkpoint, binding EffectBinding,
	verifier CapturedOperationVerifier, now time.Time) error {
	if err := validateCheckpoint(cp, cp.ComputedCheckpointDigest); err != nil {
		return err
	}
	if err := validateLimits(cp.Limits, now); err != nil {
		return err
	}
	if cp.Pins.ProviderDisposition != DispositionAwaitingGateDecision {
		if hasCapturedEffectBinding(binding) {
			return ErrObservation
		}
		return validateCheckpointAdmission(cp, now)
	}
	if cp.Pins.PendingCallDisposition != PendingCallCaptured || len(cp.Pins.ProviderTranscripts) == 0 {
		return ErrPendingUnknown
	}
	if cp.Pins.EffectReservation != nil && (cp.Pins.EffectReservation.Disposition == EffectReservationReserved ||
		cp.Pins.EffectReservation.Disposition == EffectReservationUnknown) {
		return ErrPendingUnknown
	}
	reservation := cp.Pins.HostCallReservation
	if reservation == nil || reservation.Disposition != HostCallTerminal || !validDigest(reservation.OutcomeDigest) ||
		reservation.CapturedCall == nil || reservation.CapturedCall.Disposition != CapturedCallAvailable {
		return ErrPendingUnknown
	}
	if verifier == nil || !matchesCapturedEffectBinding(*reservation.CapturedCall, binding) {
		return ErrObservation
	}
	for i, transcript := range cp.Pins.ProviderTranscripts {
		if i == len(cp.Pins.ProviderTranscripts)-1 {
			if transcript.Disposition != DispositionAwaitingGateDecision {
				return ErrPendingUnknown
			}
		} else if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return ErrPendingUnknown
		}
	}
	verifiedCheckpoint := cp
	verifiedCheckpoint.Pins = clonePins(cp.Pins)
	if err := verifier.VerifyCapturedOperation(ctx, verifiedCheckpoint, binding); err != nil {
		return fmt.Errorf("%w: captured operation verifier: %v", ErrObservation, err)
	}
	return nil
}

func hasCapturedEffectBinding(binding EffectBinding) bool {
	return binding.WorkflowID != "" || binding.ConversationID != "" || binding.HostCallID != "" ||
		binding.ToolCallID != "" || binding.HostCallOutcomeDigest != "" || binding.RecoveryGeneration != 0 ||
		binding.OwnerEpoch != 0
}

func matchesCapturedEffectBinding(captured CapturedCallPin, binding EffectBinding) bool {
	return binding.WorkflowID == captured.WorkflowID && binding.ConversationID == captured.ConversationID &&
		binding.HostCallID == captured.HostCallID && binding.ToolCallID == captured.ToolCallID &&
		binding.HostCallOutcomeDigest == captured.OutcomeDigest &&
		binding.RecoveryGeneration == captured.RecoveryGeneration && binding.OwnerEpoch == captured.OwnerEpoch
}

func validateCheckpoint(cp Checkpoint, digest string) error {
	if cp.SchemaVersion != checkpointSchema || cp.Pins.WorkflowID == "" || cp.Pins.OwnerEpoch == 0 ||
		!validDigest(digest) || cp.ComputedCheckpointDigest != digest || computeCheckpointDigest(cp) != digest {
		return fmt.Errorf("%w: checkpoint schema or digest mismatch", ErrRollback)
	}
	if cp.PreviousCheckpointDigest != "" && !validDigest(cp.PreviousCheckpointDigest) {
		return fmt.Errorf("%w: invalid predecessor digest", ErrRollback)
	}
	if err := validatePins(cp.Pins); err != nil {
		return err
	}
	if err := validateLimitsStructure(cp.Limits); err != nil {
		return err
	}
	if cp.Counters.PreEffectRetries > cp.Limits.MaxPreEffectRetries ||
		cp.Counters.CleanRestarts > cp.Limits.MaxCleanRestarts ||
		cp.Counters.HostCalls > cp.Limits.MaxHostCalls || cp.Counters.Tokens > cp.Limits.MaxTokens {
		return ErrExhausted
	}
	return nil
}

func validateLimitsStructure(limits Limits) error {
	if limits.MaxPreEffectRetries > 2 || limits.MaxCleanRestarts > 2 ||
		limits.MaxHostCalls == 0 || limits.MaxTokens == 0 || limits.MaxHostCalls > maxSQLiteInteger ||
		limits.MaxTokens > maxSQLiteInteger || limits.Deadline.IsZero() {
		return fmt.Errorf("%w: limits must be finite, deadline-bearing, and within D20 totals", ErrInvalid)
	}
	if limits.Deadline.Location() != time.UTC && limits.Deadline.Location().String() != "UTC" {
		return fmt.Errorf("%w: deadline must be normalized to UTC", ErrInvalid)
	}
	return nil
}

func validatePins(p Pins) error {
	if !validID(p.WorkflowID) || !validID(p.SessionID) || p.RecoveryGeneration == 0 || p.OwnerEpoch == 0 ||
		!validDigest(p.OwnerIncarnationDigest) || !validDigest(p.RootIdentityDigest) || !validDigest(p.StoreIdentityDigest) ||
		!validDigest(p.AdmissionIdentityDigest) || !validDigest(p.CommandIdentityDigest) || !validDigest(p.AdminIdentityDigest) ||
		!validDigest(p.TaskDigest) || !validDigest(p.PromptDigest) || !validDigest(p.PolicyDigest) ||
		!validDigest(p.ProfileDigest) || !validDigest(p.ImageDigest) || !validDigest(p.RuntimeDigest) ||
		!validDigest(p.D06ProvenanceDigest) || !validDigest(p.PrivateGitIdentityDigest) ||
		!validID(p.StoreBaselineGenerationID) || !validDigest(p.StoreBaselineTreeDigest) ||
		!validID(p.StoreTipGenerationID) || !validDigest(p.StoreTipTreeDigest) ||
		!validDigest(p.ProviderProjectionDigest) || !validDigest(p.RecoveryLineageDigest) ||
		!validDisposition(p.ProviderDisposition) || !validPendingCallDisposition(p.PendingCallDisposition) ||
		len(p.ProviderTranscripts) > 10000 ||
		p.ImportedPrefixDigest != "" && !validDigest(p.ImportedPrefixDigest) ||
		p.PredecessorTurnReceiptDigest != "" && !validDigest(p.PredecessorTurnReceiptDigest) {
		return fmt.Errorf("%w: invalid checkpoint pins", ErrInvalid)
	}
	if p.StoreJournalCount == 0 && p.StoreJournalHead != "" || p.StoreJournalCount > 0 && !validDigest(p.StoreJournalHead) ||
		p.AdmissionJournalCount == 0 && p.AdmissionJournalHead != "" || p.AdmissionJournalCount > 0 && !validDigest(p.AdmissionJournalHead) {
		return fmt.Errorf("%w: Store/admission head and count disagree", ErrInvalid)
	}
	if p.EffectReservation != nil {
		reservation := p.EffectReservation
		if !validEffectBinding(reservation.Binding) {
			return fmt.Errorf("%w: invalid effect reservation binding", ErrInvalid)
		}
		switch reservation.Disposition {
		case EffectReservationReserved:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: reserved effect already has an outcome", ErrRollback)
			}
		case EffectReservationTerminal:
			if !validDigest(reservation.OutcomeDigest) || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: terminal effect has no valid outcome digest", ErrInvalid)
			}
		case EffectReservationRetired:
			if !validDigest(reservation.RetirementDigest) || reservation.OutcomeDigest != "" {
				return fmt.Errorf("%w: retired effect has no valid retirement digest", ErrInvalid)
			}
		case EffectReservationUnknown:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: unknown effect has fabricated settlement evidence", ErrRollback)
			}
		default:
			return fmt.Errorf("%w: invalid effect reservation disposition", ErrInvalid)
		}
	}
	if p.HostCallReservation != nil {
		reservation := p.HostCallReservation
		if !validHostCallBinding(reservation.Binding) {
			return fmt.Errorf("%w: invalid host-call reservation binding", ErrInvalid)
		}
		switch reservation.Disposition {
		case HostCallReserved, HostCallUnknown:
			if reservation.OutcomeDigest != "" || reservation.RetirementDigest != "" || reservation.RetryableFailure {
				return fmt.Errorf("%w: unsettled host call has fabricated terminal evidence", ErrRollback)
			}
		case HostCallTerminal:
			if !validDigest(reservation.OutcomeDigest) || reservation.RetirementDigest != "" {
				return fmt.Errorf("%w: terminal host call has no valid result digest", ErrInvalid)
			}
			if reservation.RetryableFailure && reservation.CapturedCall != nil {
				return fmt.Errorf("%w: a retryable failed attempt captured a call", ErrInvalid)
			}
		case HostCallRetired:
			if !validDigest(reservation.RetirementDigest) || reservation.OutcomeDigest != "" || reservation.RetryableFailure {
				return fmt.Errorf("%w: retired host call has no valid retirement digest", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: invalid host-call disposition", ErrInvalid)
		}
	}
	if p.ProviderProjectionCount != totalProjectionCount(p.ProviderTranscripts) ||
		p.ProviderProjectionDigest != digestTranscriptPins(p.ProviderTranscripts) {
		return fmt.Errorf("%w: ordered transcript list does not match its projection commitment", ErrRollback)
	}
	if len(p.ProviderTranscripts) == 0 {
		if p.ProviderProjectionHead != "" || p.ProviderDisposition != DispositionNone {
			return fmt.Errorf("%w: empty provider history has a non-empty tip", ErrInvalid)
		}
	} else {
		last := p.ProviderTranscripts[len(p.ProviderTranscripts)-1]
		if p.ProviderProjectionHead != last.ProjectionHead || p.ProviderDisposition != last.Disposition {
			return fmt.Errorf("%w: provider tip differs from last transcript", ErrRollback)
		}
	}
	seenConversation := make(map[string]struct{}, len(p.ProviderTranscripts))
	seenJournal := make(map[string]struct{}, len(p.ProviderTranscripts))
	for index, transcript := range p.ProviderTranscripts {
		if transcript.Ordinal != uint64(index+1) || !validID(transcript.ConversationID) ||
			!validDigest(transcript.JournalIdentityDigest) || !validDisposition(transcript.Disposition) ||
			transcript.Disposition == DispositionNone ||
			transcript.JournalCount == 0 && transcript.JournalHead != "" ||
			transcript.JournalCount > 0 && !validDigest(transcript.JournalHead) ||
			transcript.ProjectionCount == 0 && (transcript.ProjectionDigest != "" || transcript.ProjectionHead != "") ||
			transcript.ProjectionCount > 0 && (!validDigest(transcript.ProjectionDigest) || !validDigest(transcript.ProjectionHead)) {
			return fmt.Errorf("%w: invalid ordered provider transcript pin %d", ErrInvalid, index)
		}
		if _, ok := seenConversation[transcript.ConversationID]; ok {
			return fmt.Errorf("%w: duplicate conversation ID", ErrRollback)
		}
		if _, ok := seenJournal[transcript.JournalIdentityDigest]; ok {
			return fmt.Errorf("%w: duplicate provider journal identity", ErrRollback)
		}
		seenConversation[transcript.ConversationID] = struct{}{}
		seenJournal[transcript.JournalIdentityDigest] = struct{}{}
	}
	if err := validateCapturedCallPin(p); err != nil {
		return err
	}
	if p.PreviousRecoveryLineageDigest != "" && !validDigest(p.PreviousRecoveryLineageDigest) ||
		p.LastDecisionDigest != "" && !validDigest(p.LastDecisionDigest) ||
		p.LastResultDigest != "" && !validDigest(p.LastResultDigest) {
		return fmt.Errorf("%w: invalid recovery lineage or decision/result reference", ErrInvalid)
	}
	return nil
}

func validateCapturedCallPin(p Pins) error {
	reservation := p.HostCallReservation
	if reservation == nil || reservation.CapturedCall == nil {
		if p.ProviderDisposition == DispositionAwaitingGateDecision || p.PendingCallDisposition == PendingCallCaptured {
			return fmt.Errorf("%w: gate-decision state lacks a typed captured-call binding", ErrPendingUnknown)
		}
		return nil
	}
	captured := reservation.CapturedCall
	if reservation.Disposition != HostCallTerminal || !validID(captured.WorkflowID) ||
		!validID(captured.ConversationID) || !validID(captured.HostCallID) || !validID(captured.ToolCallID) ||
		!validDigest(captured.OutcomeDigest) || captured.WorkflowID != p.WorkflowID ||
		captured.HostCallID != reservation.Binding.CallID || captured.OutcomeDigest != reservation.OutcomeDigest ||
		len(p.ProviderTranscripts) == 0 {
		return fmt.Errorf("%w: captured call differs from its workflow, conversation, or terminal host-call reservation", ErrRollback)
	}
	if captured.Disposition != CapturedCallRetired &&
		(captured.RecoveryGeneration != p.RecoveryGeneration || captured.OwnerEpoch != p.OwnerEpoch) {
		return fmt.Errorf("%w: live captured call belongs to another workflow generation or owner fence", ErrOwnerFence)
	}
	if captured.Disposition == CapturedCallRetired {
		foundRetiredConversation := deniedCallAwaitsProviderAnswer(p)
		for index, transcript := range p.ProviderTranscripts {
			if transcript.ConversationID == captured.ConversationID && transcript.Disposition == DispositionRetired {
				foundRetiredConversation = true
				break
			}
			if index == len(p.ProviderTranscripts)-1 && transcript.ConversationID == captured.ConversationID &&
				p.ProviderDisposition == DispositionAwaitingHostPrompt &&
				(p.PendingCallDisposition == PendingCallNone || p.PendingCallDisposition == PendingCallTerminal ||
					p.PendingCallDisposition == PendingCallRetired) {
				foundRetiredConversation = true
				break
			}
		}
		if !foundRetiredConversation {
			return fmt.Errorf("%w: retired captured call is not retained in its retired conversation", ErrRollback)
		}
	} else if captured.ConversationID != p.ProviderTranscripts[len(p.ProviderTranscripts)-1].ConversationID {
		return fmt.Errorf("%w: live captured call differs from the active conversation", ErrRollback)
	}
	switch captured.Disposition {
	case CapturedCallAvailable:
		if captured.RetirementDigest != "" || p.ProviderDisposition != DispositionAwaitingGateDecision ||
			p.PendingCallDisposition != PendingCallCaptured || p.EffectReservation != nil &&
			(p.EffectReservation.Disposition == EffectReservationReserved || p.EffectReservation.Disposition == EffectReservationUnknown) {
			return fmt.Errorf("%w: available captured call is not awaiting its first bound gate operation", ErrPendingUnknown)
		}
	case CapturedCallConsumed:
		if captured.RetirementDigest != "" || p.ProviderDisposition != DispositionAwaitingGateDecision ||
			p.PendingCallDisposition != PendingCallCaptured || p.EffectReservation == nil ||
			!matchesCapturedEffectBinding(*captured, p.EffectReservation.Binding) {
			return fmt.Errorf("%w: consumed captured call lacks its exact effect reservation", ErrRollback)
		}
	case CapturedCallRetired:
		retiredState := p.ProviderDisposition == DispositionRetired && p.PendingCallDisposition == PendingCallRetired
		promptState := p.ProviderDisposition == DispositionAwaitingHostPrompt &&
			(p.PendingCallDisposition == PendingCallNone || p.PendingCallDisposition == PendingCallTerminal ||
				p.PendingCallDisposition == PendingCallRetired)
		if !validDigest(captured.RetirementDigest) || (!retiredState && !promptState && !deniedCallAwaitsProviderAnswer(p)) {
			return fmt.Errorf("%w: captured call retirement is not joined to a retired or restarted provider state", ErrRollback)
		}
	default:
		return fmt.Errorf("%w: invalid captured-call disposition", ErrInvalid)
	}
	return nil
}

// validateOwnerTransition replays the durable owner history. Reservations
// consume one allowance at a time, except that retirement of an unexecuted
// captured call may atomically consume one clean restart and one pre-effect retry.
// Progress commits may change verified pins without changing budgets; clean
// restarts advance exactly one generation and consume exactly one workflow-wide
// restart.
func validateOwnerTransition(previous Checkpoint, previousDigest string, priorExists bool, next Checkpoint, expected string, proof TransitionProof) error {
	if err := validateCheckpoint(next, next.ComputedCheckpointDigest); err != nil {
		return err
	}
	if !priorExists {
		if expected != "" || next.PreviousCheckpointDigest != "" || next.ObservationDigest != "" ||
			next.Pins.RecoveryGeneration != 1 || proof.kind != transitionNone || proof.reservation != reservationNone ||
			proof.proofDigest != "" || next.Counters != (Counters{}) ||
			next.Pins.EffectReservation != nil || next.Pins.HostCallReservation != nil ||
			len(next.Pins.ProviderTranscripts) != 0 || next.Pins.ProviderDisposition != DispositionNone ||
			next.Pins.PendingCallDisposition != PendingCallNone {
			return fmt.Errorf("%w: fresh checkpoint must begin an empty ledger", ErrRollback)
		}
		return nil
	}
	if expected == "" || expected != previousDigest || next.PreviousCheckpointDigest != previousDigest {
		return ErrCAS
	}
	if next.Limits != previous.Limits || next.Counters.PreEffectRetries < previous.Counters.PreEffectRetries ||
		next.Counters.CleanRestarts < previous.Counters.CleanRestarts ||
		next.Counters.HostCalls < previous.Counters.HostCalls || next.Counters.Tokens < previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.reservation == reservationCleanRestartPreEffectRetry {
		return validateCleanRestartPreEffectRetryTransition(proof, previousDigest, previous, next)
	}
	if next.Pins.WorkflowID != previous.Pins.WorkflowID ||
		next.Counters.PreEffectRetries-previous.Counters.PreEffectRetries+
			next.Counters.CleanRestarts-previous.Counters.CleanRestarts+
			next.Counters.HostCalls-previous.Counters.HostCalls > 1 {
		return ErrRollback
	}
	sameCounters := next.Counters == previous.Counters
	if next.Pins.RecoveryGeneration == previous.Pins.RecoveryGeneration {
		if next.Pins.OwnerEpoch != previous.Pins.OwnerEpoch || next.Pins.SessionID != previous.Pins.SessionID ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts {
			return ErrRollback
		}
		if sameCounters {
			if proof.kind == transitionTerminalEffect {
				return validateTerminalEffectTransition(proof, previousDigest, previous, next)
			}
			if proof.kind != transitionProgress || proof.reservation != reservationNone || proof.previousDigest != previousDigest ||
				!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.observedPins, next.Pins) ||
				!reflect.DeepEqual(proof.targetPins, next.Pins) || proof.proofDigest != next.ObservationDigest ||
				proof.proofDigest != digestObservation(transitionProgress, previousDigest, previous.Pins, next.Pins) {
				return ErrObservation
			}
			return validateObservationAdvance(previous.Pins, next.Pins)
		}
		if next.Counters.PreEffectRetries == previous.Counters.PreEffectRetries+1 &&
			next.Counters.CleanRestarts == previous.Counters.CleanRestarts &&
			next.Counters.HostCalls == previous.Counters.HostCalls && next.Counters.Tokens == previous.Counters.Tokens {
			if !reflect.DeepEqual(previous.Pins, next.Pins) {
				return ErrRollback
			}
			return validateReservationProof(proof, reservationPreEffectRetry, previousDigest, previous, next)
		}
		if next.Counters.HostCalls == previous.Counters.HostCalls+1 &&
			next.Counters.PreEffectRetries == previous.Counters.PreEffectRetries &&
			next.Counters.CleanRestarts == previous.Counters.CleanRestarts {
			if next.ObservationDigest != previous.ObservationDigest {
				return ErrRollback
			}
			if next.Counters.Tokens == previous.Counters.Tokens {
				if err := validateEffectReservationTransition(previous.Pins, next.Pins); err != nil {
					return err
				}
				return validateReservationProof(proof, reservationEffect, previousDigest, previous, next)
			}
			if next.Counters.Tokens > previous.Counters.Tokens {
				if err := validateHostCallReservationTransition(previous.Pins, next.Pins); err != nil {
					return err
				}
				return validateReservationProof(proof, reservationHostCall, previousDigest, previous, next)
			}
			return ErrRollback
		}
		return ErrRollback
	}
	if previous.Pins.RecoveryGeneration == ^uint64(0) || next.Pins.RecoveryGeneration != previous.Pins.RecoveryGeneration+1 ||
		next.Pins.OwnerEpoch <= previous.Pins.OwnerEpoch || next.Pins.SessionID == previous.Pins.SessionID ||
		next.Counters.CleanRestarts != previous.Counters.CleanRestarts+1 ||
		next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
		next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.kind != transitionRecovery || proof.reservation != reservationNone || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestObservation(transitionRecovery, previousDigest, previous.Pins, proof.observedPins) {
		return ErrObservation
	}
	if err := validateObservationAdvance(previous.Pins, proof.observedPins); err != nil {
		return err
	}
	return validateRestartPins(proof.observedPins, next.Pins, previousDigest)
}

func validateReservationProof(proof TransitionProof, kind reservationKind, previousDigest string, previous, next Checkpoint) error {
	if proof.kind != transitionReservation || proof.reservation != kind || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		!reflect.DeepEqual(proof.previousCounts, previous.Counters) || !reflect.DeepEqual(proof.targetCounts, next.Counters) ||
		!reflect.DeepEqual(proof.observedPins, Pins{}) || proof.proofDigest != digestReservationProof(proof) {
		return ErrObservation
	}
	if next.ObservationDigest != previous.ObservationDigest {
		return ErrObservation
	}
	switch kind {
	case reservationPreEffectRetry:
		if next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries+1 ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts ||
			next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
			return ErrRollback
		}
	case reservationHostCall:
		if next.Counters.HostCalls != previous.Counters.HostCalls+1 ||
			next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts ||
			next.Counters.Tokens <= previous.Counters.Tokens ||
			next.Counters.Tokens-previous.Counters.Tokens != next.Pins.HostCallReservation.Binding.TokenUpperBound {
			return ErrRollback
		}
	case reservationEffect:
		if next.Counters.HostCalls != previous.Counters.HostCalls+1 ||
			next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries ||
			next.Counters.CleanRestarts != previous.Counters.CleanRestarts || next.Counters.Tokens != previous.Counters.Tokens {
			return ErrRollback
		}
	default:
		return ErrObservation
	}
	return nil
}

// validateCleanRestartPreEffectRetryTransition accepts exactly one atomic
// recovery that consumes one clean restart and one pre-effect retry. The retry
// is justified only by an observed retirement of an unexecuted captured call;
// every other counter and the host-call/token budget must be unchanged.
func validateCleanRestartPreEffectRetryTransition(proof TransitionProof, previousDigest string, previous, next Checkpoint) error {
	if previous.Pins.RecoveryGeneration == ^uint64(0) || next.Pins.WorkflowID != previous.Pins.WorkflowID ||
		next.Pins.RecoveryGeneration != previous.Pins.RecoveryGeneration+1 ||
		next.Pins.OwnerEpoch <= previous.Pins.OwnerEpoch || next.Pins.SessionID == previous.Pins.SessionID ||
		next.Counters.CleanRestarts != previous.Counters.CleanRestarts+1 ||
		next.Counters.PreEffectRetries != previous.Counters.PreEffectRetries+1 ||
		next.Counters.HostCalls != previous.Counters.HostCalls || next.Counters.Tokens != previous.Counters.Tokens {
		return ErrRollback
	}
	if proof.kind != transitionRecovery || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestObservation(transitionRecovery, previousDigest, previous.Pins, proof.observedPins) ||
		!retiredUnleasedCallObserved(proof.observedPins) {
		return ErrObservation
	}
	if err := validateObservationAdvance(previous.Pins, proof.observedPins); err != nil {
		return err
	}
	return validateRestartPins(proof.observedPins, next.Pins, previousDigest)
}

// retiredUnleasedCallObserved is the only observation that can justify charging a
// pre-effect retry: the provider lineage is retired and its captured call was
// retired without execution, with a commitment to that retirement.
func retiredUnleasedCallObserved(p Pins) bool {
	return p.ProviderDisposition == DispositionRetired && p.PendingCallDisposition == PendingCallRetired &&
		p.HostCallReservation != nil && p.HostCallReservation.CapturedCall != nil &&
		p.HostCallReservation.CapturedCall.Disposition == CapturedCallRetired &&
		validDigest(p.HostCallReservation.CapturedCall.RetirementDigest)
}

func validateRestartPins(old, next Pins, previousCheckpointDigest string) error {
	if old.OwnerEpoch == ^uint64(0) || next.OwnerEpoch <= old.OwnerEpoch {
		return fmt.Errorf("%w: clean restart must advance the owner epoch", ErrOwnerFence)
	}
	if old.EffectReservation != nil && old.EffectReservation.Disposition != EffectReservationTerminal &&
		old.EffectReservation.Disposition != EffectReservationRetired {
		return ErrPendingUnknown
	}
	if old.HostCallReservation != nil && old.HostCallReservation.Disposition != HostCallTerminal &&
		old.HostCallReservation.Disposition != HostCallRetired {
		return ErrPendingUnknown
	}
	hostCallPreserved := reflect.DeepEqual(old.HostCallReservation, next.HostCallReservation) ||
		preservesRetiredCapturedCall(old.HostCallReservation, next.HostCallReservation)
	if old.WorkflowID != next.WorkflowID || old.RootIdentityDigest != next.RootIdentityDigest ||
		old.StoreIdentityDigest != next.StoreIdentityDigest || old.AdmissionIdentityDigest != next.AdmissionIdentityDigest ||
		old.CommandIdentityDigest != next.CommandIdentityDigest || old.AdminIdentityDigest != next.AdminIdentityDigest ||
		old.TaskDigest != next.TaskDigest || old.PromptDigest != next.PromptDigest ||
		old.ImportedPrefixDigest != next.ImportedPrefixDigest || old.PredecessorTurnReceiptDigest != next.PredecessorTurnReceiptDigest ||
		old.PolicyDigest != next.PolicyDigest || old.ProfileDigest != next.ProfileDigest || old.ImageDigest != next.ImageDigest ||
		old.RuntimeDigest != next.RuntimeDigest || old.D06ProvenanceDigest != next.D06ProvenanceDigest ||
		old.PrivateGitIdentityDigest != next.PrivateGitIdentityDigest ||
		old.StoreBaselineGenerationID != next.StoreBaselineGenerationID || old.StoreBaselineTreeDigest != next.StoreBaselineTreeDigest ||
		old.StoreTipGenerationID != next.StoreTipGenerationID || old.StoreTipTreeDigest != next.StoreTipTreeDigest ||
		old.StoreJournalHead != next.StoreJournalHead || old.StoreJournalCount != next.StoreJournalCount ||
		old.AdmissionJournalHead != next.AdmissionJournalHead || old.AdmissionJournalCount != next.AdmissionJournalCount ||
		!reflect.DeepEqual(old.EffectReservation, next.EffectReservation) ||
		!hostCallPreserved {
		return fmt.Errorf("%w: clean restart changed immutable workflow, D06, or retained Store pins", ErrRollback)
	}
	if next.PreviousRecoveryLineageDigest != old.RecoveryLineageDigest ||
		next.LastDecisionDigest != old.LastDecisionDigest || next.LastResultDigest != old.LastResultDigest ||
		next.PendingCallDisposition != PendingCallNone ||
		len(next.ProviderTranscripts) != len(old.ProviderTranscripts)+1 {
		return fmt.Errorf("%w: clean restart omitted recovery lineage, decision/result, or transcript history", ErrRollback)
	}
	for i := range old.ProviderTranscripts {
		if !reflect.DeepEqual(old.ProviderTranscripts[i], next.ProviderTranscripts[i]) {
			return fmt.Errorf("%w: clean restart changed historical transcript %d", ErrRollback, i)
		}
		if old.ProviderTranscripts[i].Disposition != DispositionTerminal && old.ProviderTranscripts[i].Disposition != DispositionRetired {
			return fmt.Errorf("%w: historical transcript %d is not terminal or retired", ErrPendingUnknown, i)
		}
	}
	last := next.ProviderTranscripts[len(next.ProviderTranscripts)-1]
	if last.Ordinal != uint64(len(next.ProviderTranscripts)) ||
		last.JournalCount != 0 || last.JournalHead != "" || last.ProjectionCount != 0 ||
		last.ProjectionDigest != "" || last.ProjectionHead != "" || last.Disposition != DispositionAwaitingHostPrompt {
		return fmt.Errorf("%w: new provider conversation must start with an empty retained transcript", ErrInvalid)
	}
	if next.RecoveryLineageDigest == old.RecoveryLineageDigest || !validDigest(next.RecoveryLineageDigest) {
		return fmt.Errorf("%w: clean restart requires a new host-generated recovery lineage", ErrInvalid)
	}
	if previousCheckpointDigest == "" || !validDigest(previousCheckpointDigest) {
		return fmt.Errorf("%w: missing predecessor checkpoint commitment", ErrRollback)
	}
	if next.RecoveryLineageDigest != ComputeRecoveryLineageDigest(previousCheckpointDigest, old, next) {
		return fmt.Errorf("%w: recovery lineage does not bind the predecessor checkpoint and new envelope", ErrRollback)
	}
	return nil
}

func preservesRetiredCapturedCall(old, next *HostCallReservationPin) bool {
	if old == nil || next == nil || old.CapturedCall == nil || next.CapturedCall == nil ||
		old.CapturedCall.Disposition != CapturedCallAvailable || next.CapturedCall.Disposition != CapturedCallRetired ||
		!validDigest(next.CapturedCall.RetirementDigest) {
		return false
	}
	oldWithoutCapture, nextWithoutCapture := *old, *next
	oldCapture, nextCapture := *old.CapturedCall, *next.CapturedCall
	oldWithoutCapture.CapturedCall, nextWithoutCapture.CapturedCall = nil, nil
	oldCapture.Disposition, oldCapture.RetirementDigest = CapturedCallRetired, nextCapture.RetirementDigest
	return reflect.DeepEqual(oldWithoutCapture, nextWithoutCapture) && reflect.DeepEqual(oldCapture, nextCapture)
}

func validateObservationAdvance(old, next Pins) error {
	if old.WorkflowID != next.WorkflowID || old.SessionID != next.SessionID ||
		old.RecoveryGeneration != next.RecoveryGeneration || old.OwnerEpoch != next.OwnerEpoch ||
		old.OwnerIncarnationDigest != next.OwnerIncarnationDigest || old.RootIdentityDigest != next.RootIdentityDigest || old.StoreIdentityDigest != next.StoreIdentityDigest ||
		old.AdmissionIdentityDigest != next.AdmissionIdentityDigest || old.CommandIdentityDigest != next.CommandIdentityDigest ||
		old.AdminIdentityDigest != next.AdminIdentityDigest ||
		old.TaskDigest != next.TaskDigest || old.PromptDigest != next.PromptDigest ||
		old.ImportedPrefixDigest != next.ImportedPrefixDigest || old.PredecessorTurnReceiptDigest != next.PredecessorTurnReceiptDigest ||
		old.PolicyDigest != next.PolicyDigest || old.ProfileDigest != next.ProfileDigest || old.ImageDigest != next.ImageDigest ||
		old.RuntimeDigest != next.RuntimeDigest || old.D06ProvenanceDigest != next.D06ProvenanceDigest ||
		old.PrivateGitIdentityDigest != next.PrivateGitIdentityDigest ||
		old.StoreBaselineGenerationID != next.StoreBaselineGenerationID || old.StoreBaselineTreeDigest != next.StoreBaselineTreeDigest ||
		old.RecoveryLineageDigest != next.RecoveryLineageDigest || old.PreviousRecoveryLineageDigest != next.PreviousRecoveryLineageDigest {
		return fmt.Errorf("%w: observation changed immutable workflow pins", ErrRollback)
	}
	if next.StoreJournalCount < old.StoreJournalCount || next.AdmissionJournalCount < old.AdmissionJournalCount {
		return ErrRollback
	}
	if next.StoreJournalCount == old.StoreJournalCount && (next.StoreJournalHead != old.StoreJournalHead ||
		next.StoreTipGenerationID != old.StoreTipGenerationID || next.StoreTipTreeDigest != old.StoreTipTreeDigest) {
		return ErrRollback
	}
	if next.AdmissionJournalCount == old.AdmissionJournalCount && next.AdmissionJournalHead != old.AdmissionJournalHead {
		return ErrRollback
	}
	if err := validateEffectReservationAdvance(old.EffectReservation, next.EffectReservation); err != nil {
		return err
	}
	if err := validateHostCallReservationAdvance(old.HostCallReservation, next.HostCallReservation); err != nil {
		return err
	}
	if err := validateCapturedCallObservation(old, next); err != nil {
		return err
	}
	initialTranscriptAppend := len(old.ProviderTranscripts) == 0 && len(next.ProviderTranscripts) == 1 &&
		old.RecoveryGeneration == 1 && old.ProviderDisposition == DispositionNone &&
		old.PendingCallDisposition == PendingCallNone && old.ProviderProjectionCount == 0 &&
		next.PendingCallDisposition == PendingCallNone && next.ProviderDisposition == DispositionAwaitingHostPrompt &&
		next.ProviderProjectionCount == 0 && next.ProviderProjectionHead == "" &&
		next.ProviderTranscripts[0].Ordinal == 1 && next.ProviderTranscripts[0].JournalCount == 0 &&
		next.ProviderTranscripts[0].JournalHead == "" && next.ProviderTranscripts[0].ProjectionCount == 0 &&
		next.ProviderTranscripts[0].ProjectionDigest == "" && next.ProviderTranscripts[0].ProjectionHead == "" &&
		next.ProviderTranscripts[0].Disposition == DispositionAwaitingHostPrompt
	if !initialTranscriptAppend && len(old.ProviderTranscripts) != len(next.ProviderTranscripts) {
		return fmt.Errorf("%w: observation cannot add or drop conversations", ErrRollback)
	}
	for i := range old.ProviderTranscripts {
		prior, current := old.ProviderTranscripts[i], next.ProviderTranscripts[i]
		if i != len(old.ProviderTranscripts)-1 {
			if !reflect.DeepEqual(prior, current) {
				return fmt.Errorf("%w: observation changed historical transcript %d", ErrRollback, i)
			}
			continue
		}
		if prior.Ordinal != current.Ordinal || prior.ConversationID != current.ConversationID ||
			prior.JournalIdentityDigest != current.JournalIdentityDigest || current.JournalCount < prior.JournalCount ||
			current.ProjectionCount < prior.ProjectionCount ||
			current.JournalCount == prior.JournalCount && current.JournalHead != prior.JournalHead ||
			current.ProjectionCount == prior.ProjectionCount && (current.ProjectionHead != prior.ProjectionHead || current.ProjectionDigest != prior.ProjectionDigest) {
			return fmt.Errorf("%w: active transcript regressed or changed identity", ErrRollback)
		}
	}
	if next.ProviderProjectionCount < old.ProviderProjectionCount ||
		(!initialTranscriptAppend && next.ProviderProjectionCount == old.ProviderProjectionCount &&
			len(old.ProviderTranscripts) == 0 && next.ProviderProjectionDigest != old.ProviderProjectionDigest) {
		return fmt.Errorf("%w: provider projection commitment regressed", ErrRollback)
	}
	if old.LastDecisionDigest != "" && old.LastDecisionDigest != next.LastDecisionDigest ||
		old.LastResultDigest != "" && old.LastResultDigest != next.LastResultDigest {
		return fmt.Errorf("%w: prior decision or result digest changed", ErrRollback)
	}
	if old.PendingCallDisposition == PendingCallTerminal || old.PendingCallDisposition == PendingCallRetired {
		if next.PendingCallDisposition != old.PendingCallDisposition && !isNextCapturedCallAfterSettledCall(old, next) {
			return fmt.Errorf("%w: terminal call disposition was rewritten", ErrRollback)
		}
	}
	return nil
}

// A terminal/retired provider tool call is immutable once settled, but a later
// host request may capture a different call in the same conversation. The
// owner first durably charges that new HostCallBinding, so this narrowly
// permits only its exact Reserved-to-Terminal capture transition.
func isNextCapturedCallAfterSettledCall(old, next Pins) bool {
	canCaptureAfterSettledResult := old.ProviderDisposition == DispositionAwaitingHostPrompt
	if old.ProviderDisposition == DispositionAwaitingProviderExchange && len(old.ProviderTranscripts) != 0 {
		active := old.ProviderTranscripts[len(old.ProviderTranscripts)-1]
		canCaptureAfterSettledResult = active.Disposition == DispositionAwaitingProviderExchange
	}
	if (old.PendingCallDisposition != PendingCallTerminal && old.PendingCallDisposition != PendingCallRetired) ||
		!canCaptureAfterSettledResult ||
		next.PendingCallDisposition != PendingCallCaptured || next.ProviderDisposition != DispositionAwaitingGateDecision ||
		old.HostCallReservation == nil || old.HostCallReservation.Disposition != HostCallReserved ||
		old.HostCallReservation.CapturedCall != nil || next.HostCallReservation == nil ||
		next.HostCallReservation.Disposition != HostCallTerminal || next.HostCallReservation.CapturedCall == nil ||
		next.HostCallReservation.CapturedCall.Disposition != CapturedCallAvailable ||
		old.HostCallReservation.Binding != next.HostCallReservation.Binding {
		return false
	}
	return true
}

func validateCapturedCallObservation(old, next Pins) error {
	var previous, current *CapturedCallPin
	if old.HostCallReservation != nil {
		previous = old.HostCallReservation.CapturedCall
	}
	if next.HostCallReservation != nil {
		current = next.HostCallReservation.CapturedCall
	}
	if reflect.DeepEqual(previous, current) {
		return nil
	}
	if previous == nil && current != nil && current.Disposition == CapturedCallAvailable {
		if old.EffectReservation != nil && old.EffectReservation.Binding.ToolCallID == current.ToolCallID {
			return fmt.Errorf("%w: provider tool-call identity already has a consumed effect reservation", ErrObservation)
		}
		if old.HostCallReservation != nil && old.HostCallReservation.Disposition == HostCallReserved &&
			next.HostCallReservation != nil && next.HostCallReservation.Disposition == HostCallTerminal &&
			old.ProviderDisposition != DispositionAwaitingGateDecision &&
			next.ProviderDisposition == DispositionAwaitingGateDecision &&
			next.PendingCallDisposition == PendingCallCaptured {
			return nil
		}
		return fmt.Errorf("%w: capture can appear only with the verified terminal provider exchange", ErrObservation)
	}
	if previous != nil && previous.Disposition == CapturedCallAvailable && current != nil &&
		current.Disposition == CapturedCallRetired && previous.WorkflowID == current.WorkflowID &&
		previous.ConversationID == current.ConversationID && previous.HostCallID == current.HostCallID &&
		previous.ToolCallID == current.ToolCallID && previous.OutcomeDigest == current.OutcomeDigest &&
		previous.RecoveryGeneration == current.RecoveryGeneration && previous.OwnerEpoch == current.OwnerEpoch &&
		current.RetirementDigest != "" && next.HostCallReservation != nil &&
		next.HostCallReservation.Disposition == HostCallTerminal &&
		(next.ProviderDisposition == DispositionRetired && next.PendingCallDisposition == PendingCallRetired ||
			next.ProviderDisposition == DispositionAwaitingHostPrompt &&
				(next.PendingCallDisposition == PendingCallNone || next.PendingCallDisposition == PendingCallRetired) ||
			deniedCallAwaitsProviderAnswer(next)) {
		return nil
	}
	if previous != nil && previous.Disposition == CapturedCallConsumed && current == nil &&
		old.ProviderDisposition == DispositionAwaitingGateDecision &&
		next.ProviderDisposition != DispositionAwaitingGateDecision &&
		old.EffectReservation != nil && old.EffectReservation.Disposition == EffectReservationTerminal &&
		reflect.DeepEqual(old.EffectReservation, next.EffectReservation) &&
		old.HostCallReservation != nil && next.HostCallReservation != nil &&
		old.HostCallReservation.Binding == next.HostCallReservation.Binding &&
		old.HostCallReservation.OutcomeDigest == next.HostCallReservation.OutcomeDigest &&
		next.HostCallReservation.Disposition == HostCallTerminal {
		return nil
	}
	return fmt.Errorf("%w: captured call binding was rewritten, duplicated, or dropped without terminal settlement", ErrRollback)
}

func validateTerminalEffectPins(old, next Pins) error {
	if old.EffectReservation == nil || old.EffectReservation.Disposition != EffectReservationReserved ||
		next.EffectReservation == nil || next.EffectReservation.Disposition != EffectReservationTerminal ||
		old.EffectReservation.Binding != next.EffectReservation.Binding ||
		!validDigest(next.EffectReservation.OutcomeDigest) || next.EffectReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if next.StoreJournalCount <= old.StoreJournalCount || next.StoreJournalHead == old.StoreJournalHead ||
		!validDigest(next.StoreJournalHead) || next.AdmissionJournalCount < old.AdmissionJournalCount ||
		next.AdmissionJournalCount == old.AdmissionJournalCount && next.AdmissionJournalHead != old.AdmissionJournalHead ||
		next.AdmissionJournalCount > old.AdmissionJournalCount && next.AdmissionJournalHead == old.AdmissionJournalHead {
		return fmt.Errorf("%w: terminal Store outcome did not advance a durable journal head", ErrObservation)
	}
	expected := clonePins(old)
	expected.StoreJournalHead = next.StoreJournalHead
	expected.StoreJournalCount = next.StoreJournalCount
	expected.StoreTipGenerationID = next.StoreTipGenerationID
	expected.StoreTipTreeDigest = next.StoreTipTreeDigest
	expected.AdmissionJournalHead = next.AdmissionJournalHead
	expected.AdmissionJournalCount = next.AdmissionJournalCount
	expected.EffectReservation = next.EffectReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: terminal effect accounting changed unrelated workflow pins", ErrRollback)
	}
	return validatePins(next)
}

func validateTerminalEffectTransition(proof TransitionProof, previousDigest string, previous, next Checkpoint) error {
	if proof.reservation != reservationEffect || proof.previousDigest != previousDigest ||
		!reflect.DeepEqual(proof.previousPins, previous.Pins) ||
		!reflect.DeepEqual(proof.observedPins, next.Pins) || !reflect.DeepEqual(proof.targetPins, next.Pins) ||
		proof.previousCounts != previous.Counters || proof.targetCounts != next.Counters ||
		next.Counters != previous.Counters || next.Limits != previous.Limits ||
		proof.proofDigest != next.ObservationDigest ||
		proof.proofDigest != digestTerminalEffect(previousDigest, previous.Pins, next.Pins, previous.Counters) {
		return ErrObservation
	}
	return validateTerminalEffectPins(previous.Pins, next.Pins)
}

func validateEffectReservationTransition(old, next Pins) error {
	if next.EffectReservation == nil || next.EffectReservation.Disposition != EffectReservationReserved ||
		next.EffectReservation.OutcomeDigest != "" || next.EffectReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if old.EffectReservation != nil {
		if old.EffectReservation.Disposition != EffectReservationTerminal && old.EffectReservation.Disposition != EffectReservationRetired {
			return ErrPendingUnknown
		}
		if old.EffectReservation.Binding.OperationID == next.EffectReservation.Binding.OperationID {
			return fmt.Errorf("%w: consumed effect operation identity cannot be reused", ErrObservation)
		}
	}
	expected := clonePins(old)
	if old.ProviderDisposition == DispositionAwaitingGateDecision {
		if old.HostCallReservation == nil || old.HostCallReservation.CapturedCall == nil ||
			old.HostCallReservation.CapturedCall.Disposition != CapturedCallAvailable ||
			next.HostCallReservation == nil || next.HostCallReservation.CapturedCall == nil ||
			next.HostCallReservation.CapturedCall.Disposition != CapturedCallConsumed ||
			!matchesCapturedEffectBinding(*old.HostCallReservation.CapturedCall, next.EffectReservation.Binding) {
			return ErrObservation
		}
		expected.HostCallReservation.CapturedCall.Disposition = CapturedCallConsumed
		expected.HostCallReservation.CapturedCall.RetirementDigest = ""
	}
	expected.EffectReservation = next.EffectReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: effect reservation changed unrelated pins", ErrRollback)
	}
	return nil
}

func validateHostCallReservationTransition(old, next Pins) error {
	if next.HostCallReservation == nil || next.HostCallReservation.Disposition != HostCallReserved ||
		next.HostCallReservation.OutcomeDigest != "" || next.HostCallReservation.RetirementDigest != "" {
		return ErrObservation
	}
	if old.HostCallReservation != nil {
		if old.HostCallReservation.Disposition != HostCallTerminal && old.HostCallReservation.Disposition != HostCallRetired {
			return ErrPendingUnknown
		}
		if old.HostCallReservation.Binding.CallID == next.HostCallReservation.Binding.CallID {
			return fmt.Errorf("%w: consumed provider call identity cannot be reused", ErrObservation)
		}
	}
	expected := clonePins(old)
	expected.HostCallReservation = next.HostCallReservation
	if !reflect.DeepEqual(expected, next) {
		return fmt.Errorf("%w: provider reservation changed unrelated pins", ErrRollback)
	}
	return nil
}

func validateEffectReservationAdvance(old, next *EffectReservationPin) error {
	if reflect.DeepEqual(old, next) {
		return nil
	}
	if old == nil || next == nil || old.Binding != next.Binding {
		return fmt.Errorf("%w: effect reservation identity was added, removed, or rewritten", ErrRollback)
	}
	switch old.Disposition {
	case EffectReservationReserved, EffectReservationUnknown:
		if next.Disposition == EffectReservationTerminal || next.Disposition == EffectReservationRetired ||
			next.Disposition == EffectReservationUnknown {
			return nil
		}
	}
	return fmt.Errorf("%w: effect reservation disposition regressed or was rewritten", ErrRollback)
}

func validateHostCallReservationAdvance(old, next *HostCallReservationPin) error {
	if reflect.DeepEqual(old, next) {
		return nil
	}
	if old == nil || next == nil || old.Binding != next.Binding {
		return fmt.Errorf("%w: provider request reservation identity was added, removed, or rewritten", ErrRollback)
	}
	if old.Disposition == HostCallTerminal && next.Disposition == HostCallTerminal {
		oldWithoutCapture, nextWithoutCapture := *old, *next
		oldWithoutCapture.CapturedCall, nextWithoutCapture.CapturedCall = nil, nil
		if reflect.DeepEqual(oldWithoutCapture, nextWithoutCapture) {
			return nil
		}
	}
	switch old.Disposition {
	case HostCallReserved, HostCallUnknown:
		if next.Disposition == HostCallTerminal || next.Disposition == HostCallRetired || next.Disposition == HostCallUnknown {
			return nil
		}
	}
	return fmt.Errorf("%w: provider request reservation disposition regressed or was rewritten", ErrRollback)
}

func validPinsMonotonic(old, next Pins) error { return validateObservationAdvance(old, next) }

func validateObservedDisposition(p Pins) error {
	if p.PendingCallDisposition == PendingCallInFlight || p.PendingCallDisposition == PendingCallCaptured ||
		p.PendingCallDisposition == PendingCallUnknown ||
		isUnsettledDisposition(p.ProviderDisposition) {
		return ErrPendingUnknown
	}
	if p.EffectReservation != nil && (p.EffectReservation.Disposition == EffectReservationReserved ||
		p.EffectReservation.Disposition == EffectReservationUnknown) {
		return ErrPendingUnknown
	}
	if p.HostCallReservation != nil && (p.HostCallReservation.Disposition == HostCallReserved ||
		p.HostCallReservation.Disposition == HostCallUnknown) {
		return ErrPendingUnknown
	}
	for i, transcript := range p.ProviderTranscripts {
		if transcript.Disposition != DispositionTerminal && transcript.Disposition != DispositionRetired {
			return fmt.Errorf("%w: transcript %d lacks terminal lineage disposition", ErrPendingUnknown, i)
		}
	}
	return nil
}

func digestObservation(kind transitionKind, previousDigest string, previous, observed Pins) string {
	record := struct {
		Schema         string         `json:"schema"`
		Kind           transitionKind `json:"kind"`
		PreviousDigest string         `json:"previous_digest"`
		PreviousPins   Pins           `json:"previous_pins"`
		ObservedPins   Pins           `json:"observed_pins"`
	}{checkpointSchema, kind, previousDigest, clonePins(previous), clonePins(observed)}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-verified-observation/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func digestTerminalEffect(previousDigest string, previous, terminal Pins, counters Counters) string {
	record := struct {
		Schema         string          `json:"schema"`
		Kind           transitionKind  `json:"kind"`
		Reservation    reservationKind `json:"reservation_kind"`
		PreviousDigest string          `json:"previous_digest"`
		PreviousPins   Pins            `json:"previous_pins"`
		TerminalPins   Pins            `json:"terminal_pins"`
		Counters       Counters        `json:"counters"`
	}{checkpointSchema, transitionTerminalEffect, reservationEffect, previousDigest,
		clonePins(previous), clonePins(terminal), counters}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-terminal-effect-accounting/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func makeReservationProof(kind reservationKind, previousDigest string, previous, next Checkpoint) TransitionProof {
	proof := TransitionProof{
		kind: transitionReservation, reservation: kind, previousDigest: previousDigest,
		previousPins: clonePins(previous.Pins), targetPins: clonePins(next.Pins),
		previousCounts: previous.Counters, targetCounts: next.Counters,
	}
	proof.proofDigest = digestReservationProof(proof)
	return proof
}

func digestReservationProof(proof TransitionProof) string {
	record := struct {
		Schema           string          `json:"schema"`
		Kind             transitionKind  `json:"kind"`
		Reservation      reservationKind `json:"reservation_kind"`
		PreviousDigest   string          `json:"previous_digest"`
		PreviousPins     Pins            `json:"previous_pins"`
		TargetPins       Pins            `json:"target_pins"`
		PreviousCounters Counters        `json:"previous_counters"`
		TargetCounters   Counters        `json:"target_counters"`
	}{checkpointSchema, proof.kind, proof.reservation, proof.previousDigest,
		clonePins(proof.previousPins), clonePins(proof.targetPins), proof.previousCounts, proof.targetCounts}
	encoded, _ := json.Marshal(record)
	sum := sha256.Sum256(append([]byte("tbound-workflow-reservation-proof/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func digestTranscriptPins(transcripts []TranscriptPin) string {
	encoded, _ := json.Marshal(transcripts)
	sum := sha256.Sum256(append([]byte("tbound-provider-transcript-pins/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func computeCheckpointDigest(cp Checkpoint) string {
	cp.ComputedCheckpointDigest = ""
	encoded, _ := json.Marshal(cp)
	sum := sha256.Sum256(append([]byte("tbound-workflow-checkpoint/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}

func validDisposition(disposition string) bool {
	switch disposition {
	case DispositionNone, DispositionAwaitingHostPrompt, DispositionAwaitingProviderExchange,
		DispositionAwaitingGateDecision, DispositionPendingProviderResult,
		DispositionTerminal, DispositionRetired, DispositionUnknown:
		return true
	default:
		return false
	}
}

func validPendingCallDisposition(disposition string) bool {
	switch disposition {
	case PendingCallNone, PendingCallInFlight, PendingCallCaptured, PendingCallTerminal, PendingCallRetired, PendingCallUnknown:
		return true
	default:
		return false
	}
}

func isUnsettledDisposition(disposition string) bool {
	switch disposition {
	case DispositionAwaitingProviderExchange, DispositionAwaitingGateDecision,
		DispositionPendingProviderResult, DispositionUnknown:
		return true
	default:
		return false
	}
}

func totalProjectionCount(transcripts []TranscriptPin) uint64 {
	var count uint64
	for _, transcript := range transcripts {
		if ^uint64(0)-count < transcript.ProjectionCount {
			return ^uint64(0)
		}
		count += transcript.ProjectionCount
	}
	return count
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validEffectBinding(binding EffectBinding) bool {
	if !validID(binding.OperationID) || !validDigest(binding.SpecDigest) {
		return false
	}
	if !hasCapturedEffectBinding(binding) {
		return true
	}
	return validID(binding.WorkflowID) && validID(binding.ConversationID) && validID(binding.HostCallID) &&
		validID(binding.ToolCallID) && validDigest(binding.HostCallOutcomeDigest) &&
		binding.RecoveryGeneration > 0 && binding.OwnerEpoch > 0
}

func validHostCallBinding(binding HostCallBinding) bool {
	return validID(binding.CallID) && validDigest(binding.RequestDigest) && binding.TokenUpperBound > 0 &&
		binding.TokenUpperBound <= maxSQLiteInteger
}

func validID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func clonePins(p Pins) Pins {
	p.ProviderTranscripts = append([]TranscriptPin(nil), p.ProviderTranscripts...)
	if p.EffectReservation != nil {
		copy := *p.EffectReservation
		p.EffectReservation = &copy
	}
	if p.HostCallReservation != nil {
		copy := *p.HostCallReservation
		if copy.CapturedCall != nil {
			capturedCopy := *copy.CapturedCall
			copy.CapturedCall = &capturedCopy
		}
		p.HostCallReservation = &copy
	}
	return p
}

// TranscriptPinsDigest lets trusted callers construct the aggregate list
// commitment without exposing transcript contents.
func TranscriptPinsDigest(transcripts []TranscriptPin) string {
	return digestTranscriptPins(transcripts)
}

// CheckpointDigest returns the digest that the trusted external root must
// anchor for this checkpoint. It is a commitment only; it does not authenticate
// the checkpoint by itself.
func CheckpointDigest(cp Checkpoint) string {
	return computeCheckpointDigest(cp)
}

// ComputeRecoveryLineageDigest binds the host-generated restart envelope to
// the exact predecessor checkpoint and immutable ordered continuation pins.
// The caller may compute it before assigning Pins.RecoveryLineageDigest; that
// field is cleared while hashing to avoid a self-reference.
func ComputeRecoveryLineageDigest(previousCheckpointDigest string, previous, next Pins) string {
	copy := clonePins(next)
	copy.RecoveryLineageDigest = ""
	envelope := struct {
		Schema                   string `json:"schema"`
		PreviousCheckpointDigest string `json:"previous_checkpoint_digest"`
		PreviousRecoveryLineage  string `json:"previous_recovery_lineage_digest"`
		PreviousSessionID        string `json:"previous_session_id"`
		PreviousGeneration       uint64 `json:"previous_generation"`
		PreviousOwnerEpoch       uint64 `json:"previous_owner_epoch"`
		NextPins                 Pins   `json:"next_pins"`
	}{checkpointSchema, previousCheckpointDigest, previous.RecoveryLineageDigest,
		previous.SessionID, previous.RecoveryGeneration, previous.OwnerEpoch, copy}
	encoded, _ := json.Marshal(envelope)
	sum := sha256.Sum256(append([]byte("tbound-recovery-lineage/v1\n"), encoded...))
	return hex.EncodeToString(sum[:])
}
