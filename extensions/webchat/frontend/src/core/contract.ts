// contract.ts — the typed form of the bounded observation contract
// (`tbound-webchat-observation/v1`, `extensions/webchat/internal/webchat/contract.go`
// and `validate.go`). Types and closed vocabularies only; the runtime checks live
// in validate.ts.
//
// Two shapes are kept on purpose:
//   - `Item` is a discriminated union. Each member has the field set its type
//     allows, so a verdict cannot sit on a message and a raw result cannot sit on a
//     decision at the type level.
//   - `ItemFields` is the flat read view. It is what the Go struct looks like on the
//     wire, and the read-only presentation code (filters, trajectory) reads it. Every
//     `Item` member is assignable to it.
//
// Nothing here is authority. `authoritative` is refused at the gateway boundary, and
// `observed` appears only in a live projection. The claim boundary is enforced by
// the validator, not by these types alone.

export const CONTRACT_VERSION = "tbound-webchat-observation/v1";
export const DOCUMENT_VERSION = "tbound-webchat-document/v1";
export const GAP_VERSION = "tbound-webchat-stream-gap/v1";

export const SOURCE_CLASSES = ["fixture", "live", "replay", "unavailable"] as const;
export type SourceClass = (typeof SOURCE_CLASSES)[number];

export const AUTHORITIES = ["authoritative", "observed", "diagnostic", "unknown"] as const;
export type Authority = (typeof AUTHORITIES)[number];

export const VERIFICATION_SCOPES = ["unavailable", "unverified-input", "structural-audit-only"] as const;
export type VerificationScope = (typeof VERIFICATION_SCOPES)[number];

export const ITEM_TYPES = ["message", "activity", "decision", "result"] as const;
export type ItemType = (typeof ITEM_TYPES)[number];

export const ROLES = ["user", "assistant", "system"] as const;
export type Role = (typeof ROLES)[number];

export const VERDICTS = ["ALLOW", "DENY", "UNKNOWN"] as const;
export type Verdict = (typeof VERDICTS)[number];

export const TOOLS = ["read", "write", "edit", "bash"] as const;
export type Tool = (typeof TOOLS)[number];

// `pending` is the only non-terminal state. `unknown` and `lost` are not success.
export const RESULT_STATES = ["pending", "succeeded", "denied", "failed", "unknown", "lost"] as const;
export type ResultState = (typeof RESULT_STATES)[number];

export const SESSION_STATUSES = [
  "pending",
  "running",
  "completed",
  "failed",
  "cancelled",
  "unknown",
  "unavailable",
] as const;
export type SessionStatus = (typeof SESSION_STATUSES)[number];

export const PUBLICATION_STATES = [
  "unavailable",
  "not-attempted",
  "pending",
  "published",
  "failed",
  "quarantined",
  "unknown",
] as const;
export type PublicationState = (typeof PUBLICATION_STATES)[number];

export const RECOVERY_STATES = ["unavailable", "not-required", "ready", "recovery-required", "quarantined", "unknown"] as const;
export type RecoveryState = (typeof RECOVERY_STATES)[number];

export const STREAM_HEALTH = ["observing", "unavailable", "gap", "complete"] as const;
export type StreamHealth = (typeof STREAM_HEALTH)[number];

// The document boundary (validate.go decodeDocument) admits only these qualification
// values. A document cannot claim containment or an executed protocol. The served
// snapshot carries the same values because the gateway serves only documents today.
export const CONTAINMENT_VALUES = ["not-established", "unavailable"] as const;
export type Containment = (typeof CONTAINMENT_VALUES)[number];

export const H1_VALUES = ["unexecuted", "unavailable"] as const;
export type H1Status = (typeof H1_VALUES)[number];

// Gap reasons are validated by pattern in Go (validateReasonCode), not by a closed
// list. This is the set the gateway emits today. A reason outside this set is still
// a valid pattern and is kept as-is, never coerced.
export const GAP_REASONS_EMITTED = [
  "history_evicted",
  "subscriber_limit",
  "invalid_cursor",
  "cursor_ahead",
  "source_epoch_changed",
] as const;

// Presentation tones: the closed vocabulary the status dot, the chips and the rail
// share. `muted` exists only on a rail lane that did not act, so it is not a record
// tone.
export const TONES = ["ok", "allow", "deny", "reported", "pending", "unknown", "idle", "lost"] as const;
export type Tone = (typeof TONES)[number];

export interface ItemBase {
  readonly schema_version: typeof CONTRACT_VERSION;
  // 0 means "unset" in a document (the store assigns 1..N). A served item is >= 1.
  readonly sequence: number;
  readonly id: string;
  readonly turn_id?: string;
  readonly occurred_at: string;
  readonly authority: Authority;
  readonly verification_scope: VerificationScope;
}

// Transcript text is required unless it was truncated away entirely (validate.go).
export type MessageItem = ItemBase & {
  readonly type: "message";
  readonly role: Role;
} & (
    | { readonly text: string; readonly text_truncated?: boolean }
    | { readonly text?: undefined; readonly text_truncated: true }
  );

export type ActivityItem = ItemBase & {
  readonly type: "activity";
  readonly kind: string;
  readonly tool: Tool;
  readonly tool_call_id: string;
  readonly summary: string;
  readonly state: ResultState;
  readonly detail?: string;
};

export type DecisionItem = ItemBase & {
  readonly type: "decision";
  readonly tool: Tool;
  readonly tool_call_id: string;
  readonly verdict: Verdict;
  readonly reason_code: string;
  readonly policy_digest?: string;
  readonly summary?: string;
};

export type ResultItem = ItemBase & {
  readonly type: "result";
  readonly tool: Tool;
  readonly tool_call_id: string;
  readonly state: ResultState;
  readonly summary?: string;
  readonly detail?: string;
  readonly digest?: string;
  // Output completeness only. It is not part of the terminal outcome.
  readonly lossy?: boolean;
};

export type Item = MessageItem | ActivityItem | DecisionItem | ResultItem;

// The flat read view. Each property is optional unless every item type has it.
export interface ItemFields {
  readonly schema_version: string;
  readonly sequence: number;
  readonly id: string;
  readonly type: ItemType;
  readonly turn_id?: string;
  readonly occurred_at: string;
  readonly authority: Authority;
  readonly verification_scope: VerificationScope;
  readonly role?: Role;
  readonly text?: string;
  readonly text_truncated?: boolean;
  readonly kind?: string;
  readonly summary?: string;
  readonly detail?: string;
  readonly state?: ResultState;
  readonly tool?: Tool;
  readonly tool_call_id?: string;
  readonly verdict?: Verdict;
  readonly reason_code?: string;
  readonly policy_digest?: string;
  readonly lossy?: boolean;
  readonly digest?: string;
}

// The read model's view of an item: any subset of the flat fields. The presentation
// functions must handle a partial element (a missing field reads as absent), so they take
// this rather than the validated Item. A value that reaches the store is always a full Item.
export type ItemView = Partial<ItemFields>;

export interface SessionHeader {
  readonly session_id?: string;
  readonly workflow_id?: string;
  readonly title?: string;
  readonly status: SessionStatus;
  readonly profile?: string;
  readonly pi_version?: string;
  readonly created_at?: string;
  readonly updated_at?: string;
  // Absent when the source did not state a turn count; a stated zero is zero turns. The two are never conflated.
  readonly turn_count?: number;
}

// Each count is absent when the source did not state it, and a number (zero included) when it did. The gateway omits an
// absent count and writes a stated one, so presence is the only signal: a digest or the other counts never stand in.
export interface GovernancePanel {
  readonly policy_digest?: string;
  readonly allow_count?: number;
  readonly deny_count?: number;
  readonly unknown_count?: number;
  readonly note?: string;
}

export interface ProvenancePanel {
  readonly available: boolean;
  readonly profile_id?: string;
  readonly profile_hash?: string;
  readonly source_digest?: string;
  readonly containment_profile?: string;
  readonly containment_digest?: string;
  readonly provider_profile?: string;
  readonly runtime_closure?: string;
  readonly tcb_proof_digest?: string;
  readonly tcb_proof_sequence?: number;
  readonly note?: string;
}

export interface PublicationPanel {
  readonly state: PublicationState;
  readonly transaction_id?: string;
  readonly observed_at?: string;
  readonly detail?: string;
}

export interface RecoveryPanel {
  readonly state: RecoveryState;
  readonly generation?: string;
  readonly last_completed_sequence?: number;
  readonly detail?: string;
}

export interface QualificationPanel {
  readonly g1: boolean;
  readonly h1: H1Status;
  readonly containment: Containment;
  readonly claim_bearing: boolean;
  readonly note?: string;
}

export interface Panels {
  readonly governance: GovernancePanel;
  readonly provenance: ProvenancePanel;
  readonly publication: PublicationPanel;
  readonly recovery: RecoveryPanel;
  readonly qualification: QualificationPanel;
}

export interface StreamStatus {
  readonly id: string;
  readonly label: string;
  readonly health: StreamHealth;
  readonly available: boolean;
  readonly reason?: string;
}

// A durable continuity gap in a projected stream.
export interface GapRecord {
  readonly id: string;
  readonly reason: string;
  readonly first_missing: number;
  readonly last_missing: number;
  readonly recorded_at: string;
  readonly detail?: string;
}

// A stream-level gap: the server's replay gap (SSE `gap` event) or one the client
// records itself while recovering (`sequence_gap`, `invalid_item`).
export interface ReplayGap {
  readonly schema_version?: typeof GAP_VERSION;
  readonly reason: string;
  readonly requested_cursor?: string;
  readonly epoch?: string;
  readonly earliest?: number;
  readonly latest?: number;
}

// The product service's session view, carried as is by a live snapshot (`Snapshot.live`, web-chat-contract.md 9.5). The
// client echoes `revision` as `expected_revision`. A turn `state` is the service's own word (admitted, linked,
// awaiting_review, terminal, abandoned, unknown), so it is a string here and the presentation reads it conservatively.
export interface LiveBudget {
  readonly host_calls: number;
  readonly tokens: number;
  readonly duration_seconds: number;
}

export interface LiveTurn {
  readonly id: string;
  readonly ordinal: number;
  readonly state: string;
  readonly prompt: string;
  readonly outcome?: string;
  readonly terminal_reason?: string;
  readonly cancel_requested?: boolean;
  readonly resolution?: string;
  readonly candidate_digest?: string;
  readonly review_timeout_seconds?: number;
}

export interface LiveSessionView {
  readonly id: string;
  readonly project_id: string;
  readonly provider_profile: string;
  readonly publication_mode: string;
  readonly read_only?: boolean;
  readonly revision: number;
  readonly blocked?: boolean;
  readonly budget: LiveBudget;
  readonly consumed: LiveBudget;
  readonly reserved: LiveBudget;
  readonly total_turns: number;
  readonly turns_truncated?: boolean;
  readonly turns?: readonly LiveTurn[];
}

export interface Snapshot {
  readonly schema_version: typeof CONTRACT_VERSION;
  readonly epoch: string;
  readonly cursor: string;
  readonly source: SourceClass;
  readonly verification_scope: VerificationScope;
  readonly generated_at: string;
  readonly session: SessionHeader;
  readonly panels: Panels;
  readonly streams: readonly StreamStatus[];
  readonly items: readonly Item[];
  readonly gaps?: readonly GapRecord[];
  readonly warnings?: readonly string[];
  // Only a live projection carries it (validate.ts refuses it anywhere else).
  readonly live?: LiveSessionView;
}

// A fixture or replay envelope. The snapshot inside is caller input and is always
// downgraded before it is accepted.
export interface Document {
  readonly schema_version: typeof DOCUMENT_VERSION;
  readonly snapshot: Snapshot;
}

// SSE payloads. `item` and `session` carry the Go Event struct; `gap` is a ReplayGap.
export interface ItemEvent {
  readonly schema_version: typeof CONTRACT_VERSION;
  readonly cursor: string;
  readonly kind: "item";
  readonly item: Item;
}

export interface SessionEvent {
  readonly schema_version: typeof CONTRACT_VERSION;
  readonly cursor: string;
  readonly kind: "session";
  readonly session?: SessionHeader;
  readonly panels?: Panels;
  readonly streams?: readonly StreamStatus[];
}
