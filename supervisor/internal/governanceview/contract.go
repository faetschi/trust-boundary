// Package governanceview defines the bounded, read-only observation contract
// used by the native-Pi companion view. It is a presentation contract, not an
// authority store, policy engine, evidence verifier, or execution API.
package governanceview

import "time"

const (
	ContractVersion       = "tbound-governance-observation/v1"
	DocumentVersion       = "tbound-governance-document/v1"
	ReplayGapVersion      = "tbound-governance-replay-gap/v1"
	DefaultRecordCapacity = 128
	MaxRecordCapacity     = 256
	MaxSources            = 32
	MaxRecords            = 256
	MaxLinks              = 6
	MaxEvidenceRefs       = 4
	MaxWarnings           = 32
	MaxTextBytes          = 2048
	MaxOutputBytes        = 8192
	MaxProjectTests       = 32
	MaxActionOutputs      = 32
	MaxPreflightChecks    = 32
	MaxSubscribers        = 8
	SubscriberQueueSize   = 8
)

// Mode says how the displayed observation entered the companion. It is always
// explicit; absence of an observation is represented by unavailable rather
// than by a synthetic production record.
type Mode string

const (
	ModeFixture     Mode = "fixture"
	ModeLive        Mode = "live"
	ModeReplay      Mode = "replay"
	ModeUnavailable Mode = "unavailable"
)

// Authority distinguishes durable/verified records from Pi or viewer
// diagnostics. A diagnostic observation never becomes authoritative merely by
// being rendered here.
type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityDiagnostic    Authority = "diagnostic"
	AuthorityUnknown       Authority = "unknown"
)

// VerificationScope describes what the presentation layer can honestly say
// about an observation. Structural audit verification is not policy, effect,
// provider, containment, or publication authority.
type VerificationScope string

const (
	VerificationUnavailable     VerificationScope = "unavailable"
	VerificationUnverifiedInput VerificationScope = "unverified-input"
	VerificationStructuralAudit VerificationScope = "structural-audit-only"
)

// Namespace keeps IDs from unrelated governance stages from being conflated.
type Namespace string

const (
	NamespaceProvider   Namespace = "provider"
	NamespacePi         Namespace = "pi"
	NamespaceProposal   Namespace = "proposal"
	NamespaceDecision   Namespace = "decision"
	NamespaceEffect     Namespace = "effect"
	NamespaceGeneration Namespace = "generation"
	NamespaceSession    Namespace = "session"
	NamespaceSource     Namespace = "source"
)

// State values deliberately distinguish in-flight, negative, unavailable,
// and successful outcomes. UNKNOWN is not a success state.
type State string

const (
	StatePending   State = "pending"
	StateDenied    State = "denied"
	StateFailed    State = "failed"
	StateWithheld  State = "withheld"
	StateUnknown   State = "unknown"
	StateCompleted State = "completed"
)

type SourceHealth string

const (
	SourceObserving   SourceHealth = "observing"
	SourceUnavailable SourceHealth = "unavailable"
	SourceGap         SourceHealth = "gap"
	SourceComplete    SourceHealth = "complete"
)

type PreflightState string

const (
	PreflightPassed      PreflightState = "passed"
	PreflightFailed      PreflightState = "failed"
	PreflightUnavailable PreflightState = "unavailable"
	PreflightPending     PreflightState = "pending"
)

type PublicationState string

const (
	PublicationUnavailable PublicationState = "unavailable"
	PublicationStaged      PublicationState = "staged"
	PublicationPublished   PublicationState = "published"
	PublicationQuarantined PublicationState = "quarantined"
)

type RecoveryState string

const (
	RecoveryUnavailable RecoveryState = "unavailable"
	RecoveryReady       RecoveryState = "ready"
	RecoveryRequired    RecoveryState = "recovery-required"
	RecoveryQuarantined RecoveryState = "quarantined"
)

// Source is a registered input identity. Paths are intentionally absent.
type Source struct {
	ID         string       `json:"id"`
	Label      string       `json:"label"`
	Mode       Mode         `json:"mode"`
	Health     SourceHealth `json:"health"`
	Available  bool         `json:"available"`
	Epoch      string       `json:"epoch,omitempty"`
	Continuity string       `json:"continuity,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

type PreflightCheck struct {
	Name   string         `json:"name"`
	State  PreflightState `json:"state"`
	Detail string         `json:"detail,omitempty"`
}

type SessionHeader struct {
	SessionID string `json:"session_id,omitempty"`
	Status    State  `json:"status"`
	Profile   string `json:"profile,omitempty"`
	Pi        string `json:"pi,omitempty"`
}

// Links is the only supported correlation surface. IDs remain in their
// declared namespaces and are not interpreted as filesystem paths.
type Links struct {
	ProviderID   string `json:"provider_id,omitempty"`
	PiID         string `json:"pi_id,omitempty"`
	ProposalID   string `json:"proposal_id,omitempty"`
	DecisionID   string `json:"decision_id,omitempty"`
	EffectID     string `json:"effect_id,omitempty"`
	GenerationID string `json:"generation_id,omitempty"`
}

type EvidenceReference struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
}

// Record is a bounded structural projection. Detail and Summary are display
// text only; they are not executable commands, paths, or policy decisions.
type Record struct {
	SchemaVersion string              `json:"schema_version"`
	Sequence      uint64              `json:"sequence"`
	ID            string              `json:"id"`
	SourceID      string              `json:"source_id"`
	Mode          Mode                `json:"mode"`
	Verification  VerificationScope   `json:"verification_scope"`
	Namespace     Namespace           `json:"namespace"`
	Kind          string              `json:"kind"`
	Status        State               `json:"status"`
	Authority     Authority           `json:"authority"`
	OccurredAt    time.Time           `json:"occurred_at"`
	Summary       string              `json:"summary"`
	Detail        string              `json:"detail,omitempty"`
	Links         Links               `json:"links"`
	Evidence      []EvidenceReference `json:"evidence,omitempty"`
}

type GenerationView struct {
	ID            string `json:"id,omitempty"`
	Baseline      string `json:"baseline,omitempty"`
	Sealed        bool   `json:"sealed"`
	MetadataState string `json:"metadata_state"`
	TreeDigest    string `json:"tree_digest,omitempty"`
	ContentsState string `json:"contents_state"`
	DiffState     string `json:"diff_state"`
	Note          string `json:"note,omitempty"`
}

type PublicationView struct {
	State  PublicationState `json:"state"`
	Detail string           `json:"detail,omitempty"`
}

type RecoveryView struct {
	State  RecoveryState `json:"state"`
	Detail string        `json:"detail,omitempty"`
}

type ProjectTestOutput struct {
	Name            string `json:"name"`
	State           State  `json:"state"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// ActionOutput is observed Pi/runtime output, never a browser-triggered
// command. It is separate from project-test output so a test exit state cannot
// be mistaken for an effect settlement.
type ActionOutput struct {
	Name            string            `json:"name"`
	State           State             `json:"state"`
	Authority       Authority         `json:"authority"`
	Mode            Mode              `json:"mode"`
	Verification    VerificationScope `json:"verification_scope"`
	Output          string            `json:"output,omitempty"`
	OutputTruncated bool              `json:"output_truncated,omitempty"`
	Detail          string            `json:"detail,omitempty"`
}

// Snapshot is atomically rendered by the client. Cursor is a stream cursor,
// not an authority claim. Retained contents are called out separately from
// metadata/hash commitments.
type Snapshot struct {
	SchemaVersion string              `json:"schema_version"`
	Epoch         string              `json:"epoch"`
	Cursor        string              `json:"cursor"`
	Mode          Mode                `json:"mode"`
	Verification  VerificationScope   `json:"verification_scope"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Session       SessionHeader       `json:"session"`
	Preflight     []PreflightCheck    `json:"preflight"`
	Sources       []Source            `json:"sources"`
	Publication   PublicationView     `json:"publication"`
	Recovery      RecoveryView        `json:"recovery"`
	Generation    GenerationView      `json:"generation"`
	Actions       []ActionOutput      `json:"actions"`
	ProjectTests  []ProjectTestOutput `json:"project_tests"`
	Records       []Record            `json:"records"`
	Warnings      []string            `json:"warnings,omitempty"`
}

type Document struct {
	SchemaVersion string   `json:"schema_version"`
	Snapshot      Snapshot `json:"snapshot"`
}

type ReplayGap struct {
	SchemaVersion   string `json:"schema_version"`
	Reason          string `json:"reason"`
	RequestedCursor string `json:"requested_cursor,omitempty"`
	Epoch           string `json:"epoch"`
	Earliest        uint64 `json:"earliest,omitempty"`
	Latest          uint64 `json:"latest"`
}

type Event struct {
	SchemaVersion string   `json:"schema_version"`
	Cursor        string   `json:"cursor"`
	Record        Record   `json:"record"`
	Sources       []Source `json:"sources,omitempty"`
}
