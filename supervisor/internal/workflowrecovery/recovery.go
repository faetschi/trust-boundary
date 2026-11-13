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
	"errors"
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
	// RetryableFailure marks a terminal reservation whose provider attempt released nothing and was
	// journaled as a retryable failure: a pre-effect HTTP failure, or a decision-13 parallel-call refusal
	// the provider did process. The exchange is still awaited and the next attempt may reserve its own
	// host call. It is set only by the verified progress that settles that attempt.
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
