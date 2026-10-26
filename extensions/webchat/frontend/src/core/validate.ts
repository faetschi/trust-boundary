// validate.ts — the strict runtime check for the observation contract. It is the
// TypeScript mirror of `validate.go` (ValidateSnapshot, validateItem, decodeDocument)
// and is the only place untrusted JSON becomes a typed value.
//
// Rules, each traced to the Go side:
//   - Every object is closed: a key outside its struct's JSON keys is refused
//     (Go's DisallowUnknownFields). A type's forbidden fields are refused by absence
//     from its key set, so a verdict cannot ride on a message.
//   - An unknown enum value is refused. It is never mapped to a default.
//   - Text is bounded in UTF-8 bytes and single-line; identifiers and references
//     match the Go patterns; a reason code matches its pattern.
//   - authoritative is refused. observed is refused outside a live projection. Each
//     item's scope must equal its snapshot's. The rule (verification.ts) applies to a
//     served snapshot's items and to a streamed item, which is judged in the projection
//     its stream is bound to. A streamed item is never judged by a weaker rule.
//   - A document is downgraded before it is checked (decodeDocument): authority
//     becomes diagnostic and verification becomes unverified-input. Its qualification
//     panel is normalized, and a document that claims containment or an executed
//     protocol is refused.
//   - Sequences: a served snapshot has sequence 1..N in order and a cursor that ends
//     at the last one. A document may leave sequence unset (0) for every item.
//   - `live` (the product service's session view) is admitted only in a served snapshot
//     whose source is live, as validate.go ValidateSnapshot allows it. Its id must be the
//     snapshot's epoch.
//
// Deliberate differences from Go, each reported in phase2-parity.md:
//   - null is refused where Go decodes it as the zero value.
//   - A zero value in a forbidden field (for example "kind": "" on a message) is
//     refused. Go accepts it, because its check tests for a non-zero value.
//   - An unknown authority or verification value in a document is refused before the
//     downgrade. Go downgrades it silently.
//   - h1 and containment are closed to the values a document may carry.
//
// No clock and no global is read here. The caller passes nowMs.

import {
  AUTHORITIES,
  CONTAINMENT_VALUES,
  CONTRACT_VERSION,
  DOCUMENT_VERSION,
  GAP_VERSION,
  H1_VALUES,
  ITEM_TYPES,
  PUBLICATION_STATES,
  RECOVERY_STATES,
  RESULT_STATES,
  ROLES,
  SESSION_STATUSES,
  SOURCE_CLASSES,
  STREAM_HEALTH,
  TOOLS,
  VERDICTS,
  VERIFICATION_SCOPES,
  type Document,
  type Item,
  type ItemEvent,
  type ItemType,
  type LiveSessionView,
  type Panels,
  type ReplayGap,
  type SessionEvent,
  type SessionHeader,
  type Snapshot,
  type SourceClass,
  type StreamStatus,
  type VerificationScope,
} from "./contract";
import {
  authorityFault,
  scopeFault,
  type AdmissionFault,
  type ProjectionContext,
} from "./verification";

export interface ValidateOptions {
  // The caller's clock, in milliseconds since the epoch. occurred_at may not be more
  // than five minutes ahead of it (validate.go).
  readonly nowMs: number;
}

export class ContractError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ContractError";
  }
}

// An item the projection does not admit (verification.ts). It is a ContractError, so every caller that catches the
// contract catches it too, and it carries the typed fault for a caller that needs the reason.
export class AdmissionError extends ContractError {
  readonly fault: AdmissionFault;

  constructor(message: string, fault: AdmissionFault) {
    super(message);
    this.name = "AdmissionError";
    this.fault = fault;
  }
}

// The wording of each refused claim. The fault is the typed reason; the message is for people.
const ADMISSION_MESSAGES: Readonly<Record<AdmissionFault, string>> = {
  authoritative: "authoritative authority is unavailable to this presentation contract",
  "observed-outside-live": "observed authority is refused outside a live projection",
  "scope-mismatch": "verification scope does not match the snapshot",
};

function admission(path: string, fault: AdmissionFault): AdmissionError {
  return new AdmissionError(`${path}: ${ADMISSION_MESSAGES[fault]}`, fault);
}

// Go's zero time.Time ("0001-01-01T00:00:00Z") in milliseconds.
const ZERO_TIME_MS = -62135596800000;
const FUTURE_SKEW_MS = 5 * 60 * 1000;

// Bounds (contract.go).
const MAX_ITEMS = 2048;
const MAX_STREAMS = 16;
const MAX_WARNINGS = 32;
const MAX_GAPS = 64;
const MAX_TEXT_BYTES = 4096;
const MAX_DETAIL_BYTES = 8192;
const MAX_LABEL_BYTES = 256;

const IDENTIFIER = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const REFERENCE = /^[A-Za-z0-9][A-Za-z0-9._:+=/-]{0,191}$/;
const REASON_CODE = /^[a-z][a-z0-9_.-]{0,63}$/;
const EPOCH = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

type Rec = Readonly<Record<string, unknown>>;

function fail(path: string, message: string): never {
  throw new ContractError(`${path}: ${message}`);
}

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asRecord(path: string, value: unknown): Rec {
  if (!isRecord(value)) fail(path, "must be an object");
  return value;
}

function closedKeys(path: string, record: Rec, allowed: readonly string[]): void {
  for (const key of Object.keys(record)) {
    if (!allowed.includes(key)) fail(path, `unknown field ${JSON.stringify(key)}`);
  }
}

// A present value of the wrong JSON type is refused, and so is null.
function optional(path: string, record: Rec, key: string): unknown {
  if (!Object.hasOwn(record, key)) return undefined;
  const value = record[key];
  if (value === null) fail(`${path}.${key}`, "must not be null");
  return value;
}

function required(path: string, record: Rec, key: string): unknown {
  if (!Object.hasOwn(record, key) || record[key] === undefined) fail(`${path}.${key}`, "is required");
  return optional(path, record, key);
}

function oneOf<const T extends string>(path: string, allowed: readonly T[], value: unknown): T {
  for (const candidate of allowed) {
    if (candidate === value) return candidate;
  }
  return fail(path, `unsupported value ${JSON.stringify(value)}`);
}

function stringValue(path: string, value: unknown): string {
  if (typeof value !== "string") fail(path, "must be a string");
  return value;
}

function boolValue(path: string, value: unknown): boolean {
  if (typeof value !== "boolean") fail(path, "must be a boolean");
  return value;
}

function countValue(path: string, value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    fail(path, "must be a non-negative integer");
  }
  return value;
}

// UTF-8 byte length, the unit Go's len() and the contract bounds use.
export function utf8Length(value: string): number {
  let bytes = 0;
  for (const char of value) {
    const code = char.codePointAt(0) ?? 0;
    if (code < 0x80) bytes += 1;
    else if (code < 0x800) bytes += 2;
    else if (code < 0x10000) bytes += 3;
    else bytes += 4;
  }
  return bytes;
}

// A display string: bounded, and single-line apart from newline and tab.
function textValue(path: string, value: unknown, limit: number, allowEmpty: boolean): string {
  const text = stringValue(path, value);
  if (!allowEmpty && text.trim() === "") fail(path, "is required");
  if (utf8Length(text) > limit) fail(path, `exceeds ${limit} bytes`);
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if ((code < 0x20 && code !== 0x0a && code !== 0x09) || (code >= 0x7f && code <= 0x9f)) {
      fail(path, "contains a control character");
    }
  }
  return text;
}

function identifierValue(path: string, value: unknown, allowEmpty: boolean): string {
  const text = stringValue(path, value);
  if (text === "") {
    if (allowEmpty) return text;
    fail(path, "is required");
  }
  if (!IDENTIFIER.test(text)) fail(path, "is not a bounded identifier");
  return text;
}

function referenceValue(path: string, value: unknown, allowEmpty: boolean): string {
  const text = stringValue(path, value);
  if (text === "") {
    if (allowEmpty) return text;
    fail(path, "is required");
  }
  if (!REFERENCE.test(text)) fail(path, "is not a bounded reference");
  return text;
}

function reasonCodeValue(path: string, value: unknown): string {
  const text = stringValue(path, value);
  if (!REASON_CODE.test(text)) fail(path, "is not a registered reason code");
  return text;
}

// RFC 3339 with a zone, as Go's time.Time requires. Returns the instant in ms.
function timeValue(path: string, value: unknown): number {
  const text = stringValue(path, value);
  if (!RFC3339.test(text)) fail(path, "is not an RFC 3339 timestamp");
  const ms = Date.parse(text);
  if (Number.isNaN(ms)) fail(path, "is not a valid timestamp");
  return ms;
}

function nonZeroTime(path: string, value: unknown): string {
  const ms = timeValue(path, value);
  if (ms === ZERO_TIME_MS) fail(path, "must not be the zero time");
  return stringValue(path, value);
}

const ITEM_COMMON = [
  "schema_version",
  "sequence",
  "id",
  "type",
  "turn_id",
  "occurred_at",
  "authority",
  "verification_scope",
] as const;

// The closed key set of each item type. Every key not listed here is a field that
// does not belong to the type (validate.go forbiddenField).
const ITEM_KEYS: Readonly<Record<ItemType, readonly string[]>> = {
  message: [...ITEM_COMMON, "role", "text", "text_truncated"],
  activity: [...ITEM_COMMON, "kind", "tool", "tool_call_id", "summary", "detail", "state"],
  decision: [...ITEM_COMMON, "tool", "tool_call_id", "verdict", "reason_code", "policy_digest", "summary"],
  result: [...ITEM_COMMON, "tool", "tool_call_id", "state", "summary", "detail", "digest", "lossy"],
};

export interface ItemContext {
  // The source and scope of the projection the item is judged in: its snapshot, or for a
  // streamed item the snapshot its stream is bound to. The rule is verification.ts.
  readonly source: SourceClass;
  readonly scope: VerificationScope;
  readonly nowMs: number;
  // served: sequence is >= 1. document: sequence is 0 for every item, or 1..N for all. event: sequence >= 1.
  readonly sequenceMode: "served" | "document" | "event";
}

// Checks one item and narrows it to the contract type. Each check mirrors validateItem.
export function assertItem(path: string, value: unknown, context: ItemContext): asserts value is Item {
  const record = asRecord(path, value);
  const type = oneOf(`${path}.type`, ITEM_TYPES, record.type);
  closedKeys(path, record, ITEM_KEYS[type]);

  if (record.schema_version !== CONTRACT_VERSION) fail(path, "unsupported schema version");
  identifierValue(`${path}.id`, required(path, record, "id"), false);
  const turnId = optional(path, record, "turn_id");
  if (turnId !== undefined) identifierValue(`${path}.turn_id`, turnId, true);
  const occurredMs = timeValue(`${path}.occurred_at`, required(path, record, "occurred_at"));
  if (occurredMs === ZERO_TIME_MS) fail(`${path}.occurred_at`, "is invalid: zero time");
  if (occurredMs > context.nowMs + FUTURE_SKEW_MS) fail(`${path}.occurred_at`, "is in the future");
  const authority = oneOf(`${path}.authority`, AUTHORITIES, required(path, record, "authority"));
  // The claim rule (verification.ts) in the Go validator's order: authority first, then the scope it must match.
  const refusedAuthority = authorityFault(context.source, authority);
  if (refusedAuthority !== null) throw admission(path, refusedAuthority);
  const scope = oneOf(`${path}.verification_scope`, VERIFICATION_SCOPES, required(path, record, "verification_scope"));
  const refusedScope = scopeFault(scope, context.scope);
  if (refusedScope !== null) throw admission(path, refusedScope);
  // A document may omit sequence: decodeDocument reads it as 0 (unset) and the store numbers
  // the item later. A served item always has it.
  const sequenceValue = optional(path, record, "sequence");
  if (sequenceValue === undefined && context.sequenceMode !== "document") fail(`${path}.sequence`, "is required");
  const sequence = sequenceValue ?? 0;
  if (typeof sequence !== "number" || !Number.isSafeInteger(sequence) || sequence < 0) {
    fail(`${path}.sequence`, "must be a non-negative integer");
  }
  if (context.sequenceMode !== "document" && sequence < 1) fail(`${path}.sequence`, "must be at least 1");

  switch (type) {
    case "message": {
      oneOf(`${path}.role`, ROLES, required(path, record, "role"));
      const truncated = optional(path, record, "text_truncated");
      const truncatedFlag = truncated === undefined ? false : boolValue(`${path}.text_truncated`, truncated);
      const text = optional(path, record, "text");
      const body = text === undefined ? "" : textValue(`${path}.text`, text, MAX_TEXT_BYTES, true);
      if (body.trim() === "" && !truncatedFlag) fail(path, "transcript text is required");
      return;
    }
    case "activity": {
      identifierValue(`${path}.kind`, required(path, record, "kind"), false);
      oneOf(`${path}.tool`, TOOLS, required(path, record, "tool"));
      oneOf(`${path}.state`, RESULT_STATES, required(path, record, "state"));
      textValue(`${path}.summary`, required(path, record, "summary"), MAX_TEXT_BYTES, false);
      optionalText(path, record, "detail", MAX_DETAIL_BYTES);
      identifierValue(`${path}.tool_call_id`, required(path, record, "tool_call_id"), false);
      return;
    }
    case "decision": {
      oneOf(`${path}.tool`, TOOLS, required(path, record, "tool"));
      identifierValue(`${path}.tool_call_id`, required(path, record, "tool_call_id"), false);
      oneOf(`${path}.verdict`, VERDICTS, required(path, record, "verdict"));
      reasonCodeValue(`${path}.reason_code`, required(path, record, "reason_code"));
      const digest = optional(path, record, "policy_digest");
      if (digest !== undefined) referenceValue(`${path}.policy_digest`, digest, true);
      optionalText(path, record, "summary", MAX_TEXT_BYTES);
      return;
    }
    case "result": {
      oneOf(`${path}.tool`, TOOLS, required(path, record, "tool"));
      identifierValue(`${path}.tool_call_id`, required(path, record, "tool_call_id"), false);
      oneOf(`${path}.state`, RESULT_STATES, required(path, record, "state"));
      optionalText(path, record, "summary", MAX_TEXT_BYTES);
      optionalText(path, record, "detail", MAX_DETAIL_BYTES);
      const digest = optional(path, record, "digest");
      if (digest !== undefined) referenceValue(`${path}.digest`, digest, true);
      const lossy = optional(path, record, "lossy");
      if (lossy !== undefined) boolValue(`${path}.lossy`, lossy);
      return;
    }
  }
}

function optionalText(path: string, record: Rec, key: string, limit: number): void {
  const value = optional(path, record, key);
  if (value !== undefined) textValue(`${path}.${key}`, value, limit, true);
}

function optionalIdentifier(path: string, record: Rec, key: string): void {
  const value = optional(path, record, key);
  if (value !== undefined) identifierValue(`${path}.${key}`, value, true);
}

// A count the source states, or none: an absent count is not zero, and a stated count is a non-negative integer.
function optionalCount(path: string, record: Rec, key: string): void {
  const value = optional(path, record, key);
  if (value !== undefined) countValue(`${path}.${key}`, value);
}

function optionalReference(path: string, record: Rec, key: string): void {
  const value = optional(path, record, key);
  if (value !== undefined) referenceValue(`${path}.${key}`, value, true);
}

function assertStream(path: string, value: unknown): asserts value is StreamStatus {
  const record = asRecord(path, value);
  closedKeys(path, record, ["id", "label", "health", "available", "reason"]);
  identifierValue(`${path}.id`, required(path, record, "id"), false);
  textValue(`${path}.label`, required(path, record, "label"), MAX_LABEL_BYTES, false);
  oneOf(`${path}.health`, STREAM_HEALTH, required(path, record, "health"));
  const available = boolValue(`${path}.available`, required(path, record, "available"));
  optionalText(path, record, "reason", MAX_TEXT_BYTES);
  if (record.health === "unavailable" && available) fail(path, "an unavailable stream cannot be available");
}

function assertSession(path: string, value: unknown): asserts value is SessionHeader {
  const record = asRecord(path, value);
  closedKeys(path, record, [
    "session_id",
    "workflow_id",
    "title",
    "status",
    "profile",
    "pi_version",
    "created_at",
    "updated_at",
    "turn_count",
  ]);
  optionalIdentifier(path, record, "session_id");
  optionalIdentifier(path, record, "workflow_id");
  optionalText(path, record, "title", MAX_LABEL_BYTES);
  oneOf(`${path}.status`, SESSION_STATUSES, required(path, record, "status"));
  optionalIdentifier(path, record, "profile");
  optionalIdentifier(path, record, "pi_version");
  for (const key of ["created_at", "updated_at"]) {
    const value = optional(path, record, key);
    if (value !== undefined) timeValue(`${path}.${key}`, value);
  }
  optionalCount(path, record, "turn_count");
}

function assertPanels(path: string, value: unknown): asserts value is Panels {
  const record = asRecord(path, value);
  closedKeys(path, record, ["governance", "provenance", "publication", "recovery", "qualification"]);

  const governance = asRecord(`${path}.governance`, required(path, record, "governance"));
  closedKeys(`${path}.governance`, governance, ["policy_digest", "allow_count", "deny_count", "unknown_count", "note"]);
  optionalReference(`${path}.governance`, governance, "policy_digest");
  optionalCount(`${path}.governance`, governance, "allow_count");
  optionalCount(`${path}.governance`, governance, "deny_count");
  optionalCount(`${path}.governance`, governance, "unknown_count");
  optionalText(`${path}.governance`, governance, "note", MAX_TEXT_BYTES);

  const provenance = asRecord(`${path}.provenance`, required(path, record, "provenance"));
  closedKeys(`${path}.provenance`, provenance, [
    "available",
    "profile_id",
    "profile_hash",
    "source_digest",
    "containment_profile",
    "containment_digest",
    "provider_profile",
    "runtime_closure",
    "tcb_proof_digest",
    "tcb_proof_sequence",
    "note",
  ]);
  boolValue(`${path}.provenance.available`, required(`${path}.provenance`, provenance, "available"));
  optionalIdentifier(`${path}.provenance`, provenance, "profile_id");
  optionalReference(`${path}.provenance`, provenance, "profile_hash");
  optionalReference(`${path}.provenance`, provenance, "source_digest");
  optionalIdentifier(`${path}.provenance`, provenance, "containment_profile");
  optionalReference(`${path}.provenance`, provenance, "containment_digest");
  optionalIdentifier(`${path}.provenance`, provenance, "provider_profile");
  optionalIdentifier(`${path}.provenance`, provenance, "runtime_closure");
  optionalReference(`${path}.provenance`, provenance, "tcb_proof_digest");
  const tcbSequence = optional(`${path}.provenance`, provenance, "tcb_proof_sequence");
  if (tcbSequence !== undefined) countValue(`${path}.provenance.tcb_proof_sequence`, tcbSequence);
  optionalText(`${path}.provenance`, provenance, "note", MAX_TEXT_BYTES);

  const publication = asRecord(`${path}.publication`, required(path, record, "publication"));
  closedKeys(`${path}.publication`, publication, ["state", "transaction_id", "observed_at", "detail"]);
  oneOf(`${path}.publication.state`, PUBLICATION_STATES, required(`${path}.publication`, publication, "state"));
  optionalReference(`${path}.publication`, publication, "transaction_id");
  const observed = optional(`${path}.publication`, publication, "observed_at");
  if (observed !== undefined) timeValue(`${path}.publication.observed_at`, observed);
  optionalText(`${path}.publication`, publication, "detail", MAX_TEXT_BYTES);

  const recovery = asRecord(`${path}.recovery`, required(path, record, "recovery"));
  closedKeys(`${path}.recovery`, recovery, ["state", "generation", "last_completed_sequence", "detail"]);
  oneOf(`${path}.recovery.state`, RECOVERY_STATES, required(`${path}.recovery`, recovery, "state"));
  optionalIdentifier(`${path}.recovery`, recovery, "generation");
  const completed = optional(`${path}.recovery`, recovery, "last_completed_sequence");
  if (completed !== undefined) countValue(`${path}.recovery.last_completed_sequence`, completed);
  optionalText(`${path}.recovery`, recovery, "detail", MAX_TEXT_BYTES);

  const qualification = asRecord(`${path}.qualification`, required(path, record, "qualification"));
  closedKeys(`${path}.qualification`, qualification, ["g1", "h1", "containment", "claim_bearing", "note"]);
  boolValue(`${path}.qualification.g1`, required(`${path}.qualification`, qualification, "g1"));
  oneOf(`${path}.qualification.h1`, H1_VALUES, required(`${path}.qualification`, qualification, "h1"));
  oneOf(`${path}.qualification.containment`, CONTAINMENT_VALUES, required(`${path}.qualification`, qualification, "containment"));
  boolValue(`${path}.qualification.claim_bearing`, required(`${path}.qualification`, qualification, "claim_bearing"));
  optionalText(`${path}.qualification`, qualification, "note", MAX_TEXT_BYTES);
}

function assertGap(path: string, value: unknown): void {
  const record = asRecord(path, value);
  closedKeys(path, record, ["id", "reason", "first_missing", "last_missing", "recorded_at", "detail"]);
  identifierValue(`${path}.id`, required(path, record, "id"), false);
  reasonCodeValue(`${path}.reason`, required(path, record, "reason"));
  const first = countValue(`${path}.first_missing`, required(path, record, "first_missing"));
  const last = countValue(`${path}.last_missing`, required(path, record, "last_missing"));
  if (last < first) fail(path, "has an inverted range");
  nonZeroTime(`${path}.recorded_at`, required(path, record, "recorded_at"));
  optionalText(path, record, "detail", MAX_TEXT_BYTES);
}

const BUDGET_FIELDS = ["host_calls", "tokens", "duration_seconds"] as const;
const LIVE_VIEW_KEYS = [
  "id",
  "project_id",
  "provider_profile",
  "publication_mode",
  "read_only",
  "revision",
  "blocked",
  "budget",
  "consumed",
  "reserved",
  "total_turns",
  "turns_truncated",
  "turns",
] as const;
const LIVE_TURN_KEYS = [
  "id",
  "ordinal",
  "state",
  "prompt",
  "outcome",
  "terminal_reason",
  "cancel_requested",
  "resolution",
  "candidate_digest",
  "review_timeout_seconds",
] as const;
// The service's own bounds: a prompt is repeated up to 1 KiB, and the view lists at most the recent turns. The prompt and
// the reason are user and service text that the UI draws as plain text, so only their size is checked here.
const MAX_LIVE_TEXT_BYTES = 16 * 1024;
const MAX_LIVE_TURNS = 10000;

function assertBudget(path: string, value: unknown): void {
  const record = asRecord(path, value);
  closedKeys(path, record, BUDGET_FIELDS);
  for (const key of BUDGET_FIELDS) countValue(`${path}.${key}`, required(path, record, key));
}

export function assertLiveTurn(path: string, value: unknown): void {
  const record = asRecord(path, value);
  closedKeys(path, record, LIVE_TURN_KEYS);
  referenceValue(`${path}.id`, required(path, record, "id"), false);
  countValue(`${path}.ordinal`, required(path, record, "ordinal"));
  reasonCodeValue(`${path}.state`, required(path, record, "state"));
  boundedString(`${path}.prompt`, required(path, record, "prompt"));
  for (const key of ["outcome", "resolution"] as const) {
    const text = optional(path, record, key);
    if (text !== undefined) reasonCodeValue(`${path}.${key}`, text);
  }
  const reason = optional(path, record, "terminal_reason");
  if (reason !== undefined) boundedString(`${path}.terminal_reason`, reason);
  const cancel = optional(path, record, "cancel_requested");
  if (cancel !== undefined) boolValue(`${path}.cancel_requested`, cancel);
  const digest = optional(path, record, "candidate_digest");
  if (digest !== undefined) referenceValue(`${path}.candidate_digest`, digest, false);
  const timeout = optional(path, record, "review_timeout_seconds");
  if (timeout !== undefined) countValue(`${path}.review_timeout_seconds`, timeout);
}

function boundedString(path: string, value: unknown, limit: number = MAX_LIVE_TEXT_BYTES): string {
  const text = stringValue(path, value);
  if (utf8Length(text) > limit) fail(path, `exceeds ${limit} bytes`);
  return text;
}

// The service's session view, closed and bounded. The identifiers keep their patterns; prose is size-bounded only.
export function assertLiveView(path: string, value: unknown): asserts value is LiveSessionView {
  const record = asRecord(path, value);
  closedKeys(path, record, LIVE_VIEW_KEYS);
  for (const key of ["id", "project_id", "provider_profile"] as const) {
    boundedIdentity(`${path}.${key}`, required(path, record, key));
  }
  reasonCodeValue(`${path}.publication_mode`, required(path, record, "publication_mode"));
  countValue(`${path}.revision`, required(path, record, "revision"));
  countValue(`${path}.total_turns`, required(path, record, "total_turns"));
  for (const key of ["read_only", "blocked", "turns_truncated"] as const) {
    const flag = optional(path, record, key);
    if (flag !== undefined) boolValue(`${path}.${key}`, flag);
  }
  for (const key of ["budget", "consumed", "reserved"] as const) assertBudget(`${path}.${key}`, required(path, record, key));
  const turns = optional(path, record, "turns");
  if (turns !== undefined) {
    arrayValue(`${path}.turns`, turns, MAX_LIVE_TURNS).forEach((turn, index) => assertLiveTurn(`${path}.turns[${index}]`, turn));
  }
}

// A project or provider name is printable ASCII without spaces, as the service requires (validIdentity).
function boundedIdentity(path: string, value: unknown): string {
  const text = stringValue(path, value);
  if (!/^[\x21-\x7e]{1,128}$/.test(text)) fail(path, "is not a bounded identity");
  return text;
}

// The primitives the live gateway's answers are checked with (live/wire.ts), so that boundary uses these same rules.
export const kit = {
  fail,
  asRecord,
  closedKeys,
  optional,
  required,
  stringValue,
  boolValue,
  countValue,
  identifierValue,
  referenceValue,
  reasonCodeValue,
  arrayValue,
  boundedString,
  boundedIdentity,
} as const;

function cursorParts(cursor: string): { epoch: string; sequence: number } | null {
  const parts = cursor.split(":");
  if (parts.length !== 2) return null;
  const [epoch, digits] = parts;
  if (!epoch || !digits || !/^[0-9]+$/.test(digits)) return null;
  const sequence = Number(digits);
  return Number.isSafeInteger(sequence) ? { epoch, sequence } : null;
}

export type SnapshotForm = "served" | "document";

// Checks a snapshot and narrows it. The item and stream rules run here, not in the
// caller, so a single entry point covers the whole object.
export function assertSnapshot(
  path: string,
  value: unknown,
  form: SnapshotForm,
  options: ValidateOptions,
): asserts value is Snapshot {
  const record = asRecord(path, value);
  closedKeys(path, record, [
    "schema_version",
    "epoch",
    "cursor",
    "source",
    "verification_scope",
    "generated_at",
    "session",
    "panels",
    "streams",
    "items",
    "gaps",
    "warnings",
    "live",
  ]);
  if (record.schema_version !== CONTRACT_VERSION) fail(path, "unsupported snapshot schema version");
  const source = oneOf(`${path}.source`, SOURCE_CLASSES, required(path, record, "source"));
  const live = optional(path, record, "live");
  if (live !== undefined && (form !== "served" || source !== "live")) {
    fail(`${path}.live`, "only a live projection may carry a live session view");
  }
  const scope = oneOf(
    `${path}.verification_scope`,
    VERIFICATION_SCOPES,
    form === "document" ? (optional(path, record, "verification_scope") ?? "unverified-input") : required(path, record, "verification_scope"),
  );
  if (source === "unavailable" && scope !== "unavailable") {
    fail(path, "an unavailable snapshot must use the unavailable verification scope");
  }
  const epoch = stringValue(`${path}.epoch`, required(path, record, "epoch"));
  if (!EPOCH.test(epoch)) fail(`${path}.epoch`, "is invalid");
  const cursor = stringValue(`${path}.cursor`, required(path, record, "cursor"));
  const parsed = cursorParts(cursor);
  if (parsed === null || parsed.epoch !== epoch) fail(`${path}.cursor`, "is invalid");
  if (live !== undefined) {
    assertLiveView(`${path}.live`, live);
    if (live.id !== epoch) fail(`${path}.live.id`, "does not match the snapshot epoch");
  }
  nonZeroTime(`${path}.generated_at`, required(path, record, "generated_at"));
  assertSession(`${path}.session`, required(path, record, "session"));
  assertPanels(`${path}.panels`, required(path, record, "panels"));

  const streams = arrayValue(`${path}.streams`, required(path, record, "streams"), MAX_STREAMS);
  const streamIds = new Set<string>();
  streams.forEach((stream, index) => {
    assertStream(`${path}.streams[${index}]`, stream);
    if (streamIds.has(stream.id)) fail(`${path}.streams[${index}]`, `duplicate stream ${JSON.stringify(stream.id)}`);
    streamIds.add(stream.id);
  });

  const rawItems = arrayValue(`${path}.items`, required(path, record, "items"), MAX_ITEMS);
  const context: ItemContext = {
    source,
    scope,
    nowMs: options.nowMs,
    sequenceMode: form === "served" ? "served" : "document",
  };
  const items: Item[] = [];
  const itemIds = new Set<string>();
  for (const [index, item] of rawItems.entries()) {
    assertItem(`${path}.items[${index}]`, item, context);
    if (itemIds.has(item.id)) fail(`${path}.items[${index}]`, `duplicate item ${JSON.stringify(item.id)}`);
    itemIds.add(item.id);
    items.push(item);
  }
  checkSequences(`${path}.items`, items, form);
  const last = items[items.length - 1];
  if (last !== undefined && sequenceOf(last) !== 0 && parsed.epoch === epoch && parsed.sequence !== sequenceOf(last)) {
    fail(`${path}.cursor`, "does not match the last item sequence");
  }

  const gaps = arrayValue(`${path}.gaps`, optional(path, record, "gaps") ?? [], MAX_GAPS);
  gaps.forEach((gap, index) => assertGap(`${path}.gaps[${index}]`, gap));
  const warnings = arrayValue(`${path}.warnings`, optional(path, record, "warnings") ?? [], MAX_WARNINGS);
  warnings.forEach((warning, index) => textValue(`${path}.warnings[${index}]`, warning, MAX_TEXT_BYTES, false));
}

// The sequence of an item as checked. A document item may omit the key, which reads as 0 (unset).
function sequenceOf(item: Item): number {
  const value: unknown = item.sequence;
  return typeof value === "number" ? value : 0;
}

// A served snapshot's sequences are 1..N in order. A document's are all unset, or
// 1..N in order. Anything else is a discontinuity, which is refused, not renumbered.
function checkSequences(path: string, items: readonly Item[], form: SnapshotForm): void {
  const provided = items.filter((item) => sequenceOf(item) !== 0).length;
  if (form === "served" || provided > 0) {
    if (provided !== items.length) fail(path, "item sequence must be set for every element or for none");
    items.forEach((item, index) => {
      if (sequenceOf(item) !== index + 1) fail(`${path}[${index}]`, "item sequence must be contiguous from 1");
    });
  }
}

// Returns the array, checked for length. Its elements are checked by the caller.
function arrayValue(path: string, value: unknown, limit: number): readonly unknown[] {
  if (!Array.isArray(value)) fail(path, "must be an array");
  if (value.length > limit) fail(path, `exceeds ${limit} elements`);
  return value;
}

// The elements of a value that may be an array, or nothing for any other value.
function listOf(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : [];
}

// A served snapshot, as the gateway serves it: source any of the four, sequences set.
export function parseSnapshot(value: unknown, options: ValidateOptions): Snapshot {
  assertSnapshot("snapshot", value, "served", options);
  return value;
}

// A fixture or replay envelope. The steps follow decodeDocument: the envelope and its
// closed keys, the expected source, the claim checks, the downgrade, then the snapshot
// check on the downgraded value. The result is always unverified input.
export function parseDocument(
  value: unknown,
  expected: "fixture" | "replay",
  options: ValidateOptions,
): Document {
  const envelope = asRecord("document", value);
  closedKeys("document", envelope, ["schema_version", "snapshot"]);
  if (envelope.schema_version !== DOCUMENT_VERSION) fail("document", "unsupported document schema");
  const snapshot = asRecord("document.snapshot", required("document", envelope, "snapshot"));
  if (snapshot.source !== expected) fail("document.snapshot.source", "does not match the requested source");

  const panels = asRecord("document.snapshot.panels", required("document.snapshot", snapshot, "panels"));
  // decodeDocument refuses anything but these two values, and a missing value too.
  const qualification = asRecord("document.snapshot.panels.qualification", required("document.snapshot.panels", panels, "qualification"));
  if (!CONTAINMENT_VALUES.some((allowed) => allowed === qualification.containment)) {
    fail("document.snapshot.panels.qualification.containment", "a document cannot claim containment qualification");
  }
  if (!H1_VALUES.some((allowed) => allowed === qualification.h1)) {
    fail("document.snapshot.panels.qualification.h1", "a document cannot claim an executed research protocol");
  }

  // Refuse unknown authority and scope values before the downgrade replaces them.
  const rawItems = listOf(snapshot.items);
  rawItems.forEach((item, index) => {
    if (!isRecord(item)) return;
    if (item.authority !== undefined) oneOf(`document.snapshot.items[${index}].authority`, AUTHORITIES, item.authority);
    if (item.verification_scope !== undefined) {
      oneOf(`document.snapshot.items[${index}].verification_scope`, VERIFICATION_SCOPES, item.verification_scope);
    }
  });

  const downgraded = downgradeDocument(snapshot, rawItems);
  assertSnapshot("document.snapshot", downgraded, "document", options);
  return { schema_version: DOCUMENT_VERSION, snapshot: downgraded };
}

// The downgrade of decodeDocument. It builds new objects and never mutates the input.
function downgradeDocument(snapshot: Rec, rawItems: readonly unknown[]): Rec {
  const items = rawItems.map((item) => (isRecord(item) ? { ...item, authority: "diagnostic", verification_scope: "unverified-input" } : item));
  const panels = isRecord(snapshot.panels) ? snapshot.panels : {};
  const provenance = isRecord(panels.provenance) ? panels.provenance : {};
  const qualification = isRecord(panels.qualification) ? panels.qualification : {};
  return {
    ...snapshot,
    verification_scope: "unverified-input",
    items,
    panels: {
      ...panels,
      provenance: { ...provenance, available: false },
      qualification: { ...qualification, g1: false, claim_bearing: false },
    },
  };
}

// The options of an SSE payload: the clock, and the projection the stream is bound to. A streamed item is judged in
// that projection, as a snapshot item is judged in its own. The stream supplies it from the snapshot it opened after.
export interface EventOptions extends ValidateOptions {
  readonly projection: ProjectionContext;
}

// An SSE payload: an `item` event or a `session` event (validate.go; the Event struct).
export function parseEvent(value: unknown, options: EventOptions): ItemEvent | SessionEvent {
  const record = asRecord("event", value);
  closedKeys("event", record, ["schema_version", "cursor", "kind", "item", "session", "panels", "streams"]);
  if (record.schema_version !== CONTRACT_VERSION) fail("event", "unsupported schema version");
  const cursor = stringValue("event.cursor", required("event", record, "cursor"));
  if (cursorParts(cursor) === null) fail("event.cursor", "is invalid");
  const kind = required("event", record, "kind");
  if (kind === "item") {
    if (Object.hasOwn(record, "session") || Object.hasOwn(record, "panels") || Object.hasOwn(record, "streams")) {
      fail("event", "an item event carries only an item");
    }
    const itemValue = required("event", record, "item");
    // The event does not carry its snapshot's source or scope. The stream supplies them, so a streamed item is
    // refused by the rule a snapshot item meets (Store.AppendItem checks it against the base snapshot the same way).
    assertItem("event.item", itemValue, {
      source: options.projection.source,
      scope: options.projection.verification,
      nowMs: options.nowMs,
      sequenceMode: "event",
    });
    return { schema_version: CONTRACT_VERSION, cursor, kind: "item", item: itemValue };
  }
  if (kind === "session") {
    if (Object.hasOwn(record, "item")) fail("event", "a session event carries no item");
    let session: SessionHeader | undefined;
    let panels: Panels | undefined;
    let streams: readonly StreamStatus[] | undefined;
    const sessionValue = optional("event", record, "session");
    if (sessionValue !== undefined) {
      assertSession("event.session", sessionValue);
      session = sessionValue;
    }
    const panelsValue = optional("event", record, "panels");
    if (panelsValue !== undefined) {
      assertPanels("event.panels", panelsValue);
      panels = panelsValue;
    }
    const streamsValue = optional("event", record, "streams");
    if (streamsValue !== undefined) {
      const checked: StreamStatus[] = [];
      for (const [index, stream] of arrayValue("event.streams", streamsValue, MAX_STREAMS).entries()) {
        assertStream(`event.streams[${index}]`, stream);
        checked.push(stream);
      }
      streams = checked;
    }
    return { schema_version: CONTRACT_VERSION, cursor, kind: "session", session, panels, streams };
  }
  return fail("event.kind", `unsupported value ${JSON.stringify(kind)}`);
}

// The server's replay gap (the SSE `gap` event): the Go ReplayGap struct, closed.
export function parseReplayGap(value: unknown): ReplayGap {
  const record = asRecord("gap", value);
  closedKeys("gap", record, ["schema_version", "reason", "requested_cursor", "epoch", "earliest", "latest"]);
  if (record.schema_version !== GAP_VERSION) fail("gap", "unsupported gap schema version");
  const reason = reasonCodeValue("gap.reason", required("gap", record, "reason"));
  const requested = optional("gap", record, "requested_cursor");
  const requestedCursor = requested === undefined ? undefined : textValue("gap.requested_cursor", requested, 256, true);
  const epoch = stringValue("gap.epoch", required("gap", record, "epoch"));
  if (!EPOCH.test(epoch)) fail("gap.epoch", "is invalid");
  const earliest = optional("gap", record, "earliest");
  const latest = countValue("gap.latest", required("gap", record, "latest"));
  return {
    schema_version: GAP_VERSION,
    reason,
    requested_cursor: requestedCursor,
    epoch,
    earliest: earliest === undefined ? undefined : countValue("gap.earliest", earliest),
    latest,
  };
}

// The one place JSON text becomes a value. It is not validated here: the caller
// passes the result to parseSnapshot, parseEvent or parseReplayGap, which is.
export function parseJsonText(text: string): unknown {
  const parsed: unknown = JSON.parse(text);
  return parsed;
}
