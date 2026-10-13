// Package webchat is the untrusted presentation gateway for the tbound Pi chat
// workspace.
//
// It is a presentation contract, not an authority store, policy engine,
// evidence verifier, or execution API. It never launches Pi, executes a tool,
// mutates policy, releases an effect, writes the live workspace, or turns a DENY
// into an ALLOW. The Go supervisor retains admission, policy, leases, effect
// execution, durable journals, and publication authority.
//
// The projection is deliberately bounded: only allowlisted structural fields are
// represented here. Raw journals, credentials, provider payloads, arbitrary
// filesystem paths, and unrestricted tool arguments/output have no field in this
// contract.
package webchat

import "time"

const (
	// ContractVersion is the schema of a bounded observation snapshot.
	ContractVersion = "tbound-webchat-observation/v1"
	// DocumentVersion is the schema of an on-disk fixture/replay document.
	DocumentVersion = "tbound-webchat-document/v1"
	// GapVersion is the schema of a stream replay gap notice.
	GapVersion = "tbound-webchat-stream-gap/v1"

	DefaultItemCapacity = 512
	MaxItemCapacity     = 2048
	MaxItems            = 2048
	MaxStreams          = 16
	MaxWarnings         = 32
	MaxGaps             = 64

	// MaxTextBytes bounds transcript text; MaxDetailBytes bounds expanded
	// sanitized detail. Neither carries raw command output or tool arguments.
	MaxTextBytes   = 4096
	MaxDetailBytes = 8192
	MaxLabelBytes  = 256

	MaxSubscribers      = 8
	SubscriberQueueSize = 16
)

// SourceClass says how the displayed observation entered the workspace. It is
// always explicit: absence of an observation is represented by unavailable
// rather than by a synthetic production record. Development fixtures remain
// non-claim-bearing and are never presented as production evidence.
type SourceClass string

const (
	SourceFixture     SourceClass = "fixture"
	SourceLive        SourceClass = "live"
	SourceReplay      SourceClass = "replay"
	SourceUnavailable SourceClass = "unavailable"
)

// Authority distinguishes a projection element that was observed from the
// supervisor's durable transition from caller-supplied or gateway-supplied
// display data. The gateway cannot mint authority: `authoritative` is defined
// only so that it can be rejected at the presentation boundary, and `observed`
// may be carried only by a live projection.
type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityObserved      Authority = "observed"
	AuthorityDiagnostic    Authority = "diagnostic"
	AuthorityUnknown       Authority = "unknown"
)

// Verification says what the presentation layer can honestly claim about an
// observation. Structural audit verification is not policy, effect, provider,
// containment, or publication authority.
type Verification string

const (
	VerificationUnavailable     Verification = "unavailable"
	VerificationUnverifiedInput Verification = "unverified-input"
	VerificationStructuralAudit Verification = "structural-audit-only"
)

// ItemType discriminates the bounded projection union. Each type has a fixed,
// minimal field set; fields that do not belong to the type are rejected so a
// caller cannot smuggle a verdict into a transcript line or a raw result into a
// decision.
type ItemType string

const (
	ItemMessage  ItemType = "message"
	ItemActivity ItemType = "activity"
	ItemDecision ItemType = "decision"
	ItemResult   ItemType = "result"
)

// Role is the transcript role of a message item.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Verdict is the policy decision of a decision item. Only the supervisor gate
// produces it; the gateway never computes or overrides it.
type Verdict string

const (
	VerdictAllow   Verdict = "ALLOW"
	VerdictDeny    Verdict = "DENY"
	VerdictUnknown Verdict = "UNKNOWN"
)

// Tool is the closed mediated tool surface.
type Tool string

const (
	ToolRead  Tool = "read"
	ToolWrite Tool = "write"
	ToolEdit  Tool = "edit"
	ToolBash  Tool = "bash"
)

// ResultState is the settled execution state of a result or activity item.
// UNKNOWN and lost are not success states.
type ResultState string

const (
	ResultPending   ResultState = "pending"
	ResultSucceeded ResultState = "succeeded"
	ResultDenied    ResultState = "denied"
	ResultFailed    ResultState = "failed"
	ResultUnknown   ResultState = "unknown"
	ResultLost      ResultState = "lost"
)

// SessionStatus is the bounded session lifecycle state.
type SessionStatus string

const (
	SessionPending     SessionStatus = "pending"
	SessionRunning     SessionStatus = "running"
	SessionCompleted   SessionStatus = "completed"
	SessionFailed      SessionStatus = "failed"
	SessionCancelled   SessionStatus = "cancelled"
	SessionUnknown     SessionStatus = "unknown"
	SessionUnavailable SessionStatus = "unavailable"
)

// PublicationState is the bounded publication projection.
type PublicationState string

const (
	PublicationUnavailable  PublicationState = "unavailable"
	PublicationNotAttempted PublicationState = "not-attempted"
	PublicationPending      PublicationState = "pending"
	PublicationPublished    PublicationState = "published"
	PublicationFailed       PublicationState = "failed"
	PublicationQuarantined  PublicationState = "quarantined"
	PublicationUnknown      PublicationState = "unknown"
)

// RecoveryState is the bounded recovery projection.
type RecoveryState string

const (
	RecoveryUnavailable RecoveryState = "unavailable"
	RecoveryNotRequired RecoveryState = "not-required"
	RecoveryReady       RecoveryState = "ready"
	RecoveryRequired    RecoveryState = "recovery-required"
	RecoveryQuarantined RecoveryState = "quarantined"
	RecoveryUnknown     RecoveryState = "unknown"
)

// StreamHealth describes one projected observation channel.
type StreamHealth string

const (
	StreamObserving   StreamHealth = "observing"
	StreamUnavailable StreamHealth = "unavailable"
	StreamGap         StreamHealth = "gap"
	StreamComplete    StreamHealth = "complete"
)

// Item is one bounded, ordered projection element. Summary and Detail are
// display text only; they are never executable commands, request bodies, or
// policy decisions.
type Item struct {
	SchemaVersion string       `json:"schema_version"`
	Sequence      uint64       `json:"sequence"`
	ID            string       `json:"id"`
	Type          ItemType     `json:"type"`
	TurnID        string       `json:"turn_id,omitempty"`
	OccurredAt    time.Time    `json:"occurred_at"`
	Authority     Authority    `json:"authority"`
	Verification  Verification `json:"verification_scope"`

	// message
	Role          Role   `json:"role,omitempty"`
	Text          string `json:"text,omitempty"`
	TextTruncated bool   `json:"text_truncated,omitempty"`

	// activity
	Kind    string      `json:"kind,omitempty"`
	Summary string      `json:"summary,omitempty"`
	Detail  string      `json:"detail,omitempty"`
	State   ResultState `json:"state,omitempty"`

	// tool (activity, decision, result)
	Tool Tool `json:"tool,omitempty"`

	// decision
	ToolCallID   string  `json:"tool_call_id,omitempty"`
	Verdict      Verdict `json:"verdict,omitempty"`
	ReasonCode   string  `json:"reason_code,omitempty"`
	PolicyDigest string  `json:"policy_digest,omitempty"`

	// result
	Lossy  bool   `json:"lossy,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// SessionHeader is the bounded session identity and lifecycle projection.
type SessionHeader struct {
	SessionID  string        `json:"session_id,omitempty"`
	WorkflowID string        `json:"workflow_id,omitempty"`
	Title      string        `json:"title,omitempty"`
	Status     SessionStatus `json:"status"`
	Profile    string        `json:"profile,omitempty"`
	PiVersion  string        `json:"pi_version,omitempty"`
	CreatedAt  time.Time     `json:"created_at,omitempty"`
	UpdatedAt  time.Time     `json:"updated_at,omitempty"`
	TurnCount  int           `json:"turn_count"`
}

// GovernancePanel summarises policy decisions observed so far. It is a count of
// observed decisions, not a claim about policy correctness.
type GovernancePanel struct {
	PolicyDigest string `json:"policy_digest,omitempty"`
	AllowCount   int    `json:"allow_count"`
	DenyCount    int    `json:"deny_count"`
	UnknownCount int    `json:"unknown_count"`
	Note         string `json:"note,omitempty"`
}

// ProvenancePanel exposes bounded provenance references. Digests are references
// to authoritative artifacts, not the artifacts themselves; the gateway never
// serves the underlying bytes.
type ProvenancePanel struct {
	Available          bool   `json:"available"`
	ProfileID          string `json:"profile_id,omitempty"`
	ProfileHash        string `json:"profile_hash,omitempty"`
	SourceDigest       string `json:"source_digest,omitempty"`
	ContainmentProfile string `json:"containment_profile,omitempty"`
	ContainmentDigest  string `json:"containment_digest,omitempty"`
	ProviderProfile    string `json:"provider_profile,omitempty"`
	RuntimeClosure     string `json:"runtime_closure,omitempty"`
	TCBProofDigest     string `json:"tcb_proof_digest,omitempty"`
	TCBProofSequence   uint64 `json:"tcb_proof_sequence,omitempty"`
	Note               string `json:"note,omitempty"`
}

// PublicationPanel is the bounded publication projection. Publication is a
// separate authority from a policy ALLOW and from a successful execution.
type PublicationPanel struct {
	State         PublicationState `json:"state"`
	TransactionID string           `json:"transaction_id,omitempty"`
	ObservedAt    *time.Time       `json:"observed_at,omitempty"`
	Detail        string           `json:"detail,omitempty"`
}

// RecoveryPanel is the bounded recovery projection.
type RecoveryPanel struct {
	State                 RecoveryState `json:"state"`
	Generation            string        `json:"generation,omitempty"`
	LastCompletedSequence uint64        `json:"last_completed_sequence,omitempty"`
	Detail                string        `json:"detail,omitempty"`
}

// QualificationPanel records the product's current, honest qualification
// status. Green UI or passing tests never change these values.
type QualificationPanel struct {
	G1           bool   `json:"g1"`
	H1           string `json:"h1"`
	Containment  string `json:"containment"`
	ClaimBearing bool   `json:"claim_bearing"`
	Note         string `json:"note,omitempty"`
}

// Panels groups the non-transcript governance projections.
type Panels struct {
	Governance    GovernancePanel    `json:"governance"`
	Provenance    ProvenancePanel    `json:"provenance"`
	Publication   PublicationPanel   `json:"publication"`
	Recovery      RecoveryPanel      `json:"recovery"`
	Qualification QualificationPanel `json:"qualification"`
}

// StreamStatus is one projected observation channel with explicit continuity.
type StreamStatus struct {
	ID        string       `json:"id"`
	Label     string       `json:"label"`
	Health    StreamHealth `json:"health"`
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
}

// GapRecord is a durable record of a continuity gap in a projected stream. A
// gap is never hidden and never invented into success.
type GapRecord struct {
	ID           string    `json:"id"`
	Reason       string    `json:"reason"`
	FirstMissing uint64    `json:"first_missing"`
	LastMissing  uint64    `json:"last_missing"`
	RecordedAt   time.Time `json:"recorded_at"`
	Detail       string    `json:"detail,omitempty"`
}

// Snapshot is atomically rendered by the client. Cursor is a stream cursor, not
// an authority claim.
type Snapshot struct {
	SchemaVersion string         `json:"schema_version"`
	Epoch         string         `json:"epoch"`
	Cursor        string         `json:"cursor"`
	Source        SourceClass    `json:"source"`
	Verification  Verification   `json:"verification_scope"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Session       SessionHeader  `json:"session"`
	Panels        Panels         `json:"panels"`
	Streams       []StreamStatus `json:"streams"`
	Items         []Item         `json:"items"`
	Gaps          []GapRecord    `json:"gaps,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// Document is an on-disk fixture/replay envelope. It is caller-supplied input
// and is always downgraded before it is accepted.
type Document struct {
	SchemaVersion string   `json:"schema_version"`
	Snapshot      Snapshot `json:"snapshot"`
}

// ReplayGap is emitted when a requested cursor cannot be served from the
// retained window.
type ReplayGap struct {
	SchemaVersion   string `json:"schema_version"`
	Reason          string `json:"reason"`
	RequestedCursor string `json:"requested_cursor,omitempty"`
	Epoch           string `json:"epoch"`
	Earliest        uint64 `json:"earliest,omitempty"`
	Latest          uint64 `json:"latest"`
}

// Event is one server-sent event. Kind is "item" or "session".
type Event struct {
	SchemaVersion string         `json:"schema_version"`
	Cursor        string         `json:"cursor"`
	Kind          string         `json:"kind"`
	Item          *Item          `json:"item,omitempty"`
	Session       *SessionHeader `json:"session,omitempty"`
	Panels        *Panels        `json:"panels,omitempty"`
}
