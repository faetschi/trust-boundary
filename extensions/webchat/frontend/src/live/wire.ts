// wire.ts — the live gateway's answers (web-chat-contract.md section 9.3), parsed at the boundary. Every answer is
// checked with the contract validator's own primitives (validate.ts `kit`), so an unknown field, a wrong type or a bad
// bound is a ContractError and never a partial value. The routes and their bodies are the gateway's, unchanged.

import type { LiveSessionView, LiveTurn } from "../core/contract";
import { assertLiveTurn, assertLiveView, kit } from "../core/validate";

const { fail, asRecord, closedKeys, optional, required, stringValue, boolValue, countValue, reasonCodeValue, arrayValue } = kit;

export interface Pairing {
  readonly csrfToken: string;
}

export interface Candidate {
  readonly turnId: string;
  readonly candidateDigest: string;
  readonly changes: readonly CandidateChange[];
}

export interface CandidateChange {
  readonly path: string;
  readonly op: string;
  readonly diff: string;
  readonly truncated: boolean;
}

// One product observation. The transcript line for it is `items[i]` of the same answer. The presentation reads only the
// kind (to know when the session view must be read again), so the rest is checked for shape and not kept.
export interface LiveEvent {
  readonly seq: number;
  readonly kind: string;
  readonly turnId: string;
  readonly candidateDigest: string;
}

export interface EventsAnswer {
  readonly cursor: number;
  readonly nextCursor: number;
  readonly gap: boolean;
  readonly events: readonly LiveEvent[];
  // Raw transcript items. The stream judges each one in the projection its snapshot opened (core parseEvent).
  readonly items: readonly unknown[];
}

const MAX_EVENTS = 512;
const MAX_CHANGES = 256;
const MAX_ANSWER_TEXT = 64 * 1024;
const EVENT_KEYS = [
  "seq",
  "kind",
  "turn_id",
  "text",
  "tool",
  "tool_call_id",
  "verdict",
  "reason_code",
  "hint",
  "outcome",
  "reason",
  "candidate_digest",
  "paths",
] as const;

function boundedText(path: string, value: unknown): string {
  return kit.boundedString(path, value, MAX_ANSWER_TEXT);
}

export function parsePairing(value: unknown): Pairing {
  const record = asRecord("pairing", value);
  closedKeys("pairing", record, ["csrf_token", "expires_at"]);
  const token = stringValue("pairing.csrf_token", required("pairing", record, "csrf_token"));
  if (token === "") fail("pairing.csrf_token", "is required");
  return { csrfToken: token };
}

export type GatewayKind = "live" | "demo";

// The descriptor says "live" only on a live gateway. A demonstration gateway has no `mode` key.
export function parseGatewayKind(value: unknown): GatewayKind {
  const record = asRecord("contract", value);
  return record.mode === "live" ? "live" : "demo";
}

export function parseSessions(value: unknown): readonly LiveSessionView[] {
  const record = asRecord("sessions", value);
  closedKeys("sessions", record, ["sessions"]);
  const list = arrayValue("sessions.sessions", required("sessions", record, "sessions"), 2000);
  list.forEach((entry, index) => assertLiveView(`sessions.sessions[${index}]`, entry));
  return list as readonly LiveSessionView[];
}

export function parseCreated(value: unknown): { readonly session: LiveSessionView; readonly replayed: boolean } {
  const record = asRecord("created", value);
  closedKeys("created", record, ["session", "replayed"]);
  const session = required("created", record, "session");
  assertLiveView("created.session", session);
  const replayed = optional("created", record, "replayed");
  return { session, replayed: replayed === undefined ? false : boolValue("created.replayed", replayed) };
}

export function parseTurnAnswer(value: unknown): { readonly turn: LiveTurn; readonly replayed: boolean } {
  const record = asRecord("turn answer", value);
  closedKeys("turn answer", record, ["turn", "replayed"]);
  const turn = required("turn answer", record, "turn");
  assertLiveTurn("turn answer.turn", turn);
  const replayed = optional("turn answer", record, "replayed");
  return { turn: turn as LiveTurn, replayed: replayed === undefined ? false : boolValue("turn answer.replayed", replayed) };
}

export function parseCandidate(value: unknown): Candidate {
  const outer = asRecord("candidate answer", value);
  closedKeys("candidate answer", outer, ["candidate"]);
  const record = asRecord("candidate", required("candidate answer", outer, "candidate"));
  closedKeys("candidate", record, ["turn_id", "candidate_digest", "changes"]);
  const turnId = kit.referenceValue("candidate.turn_id", required("candidate", record, "turn_id"), false);
  const digest = kit.referenceValue("candidate.candidate_digest", required("candidate", record, "candidate_digest"), false);
  const changes = arrayValue("candidate.changes", required("candidate", record, "changes"), MAX_CHANGES).map((entry, index) => {
    const path = `candidate.changes[${index}]`;
    const change = asRecord(path, entry);
    closedKeys(path, change, ["path", "op", "diff", "truncated"]);
    const diff = optional(path, change, "diff");
    const truncated = optional(path, change, "truncated");
    return {
      path: boundedText(`${path}.path`, required(path, change, "path")),
      op: boundedText(`${path}.op`, required(path, change, "op")),
      diff: diff === undefined ? "" : boundedText(`${path}.diff`, diff),
      truncated: truncated === undefined ? false : boolValue(`${path}.truncated`, truncated),
    };
  });
  return { turnId, candidateDigest: digest, changes };
}

function parseEvent(path: string, value: unknown): LiveEvent {
  const record = asRecord(path, value);
  closedKeys(path, record, EVENT_KEYS);
  const seq = countValue(`${path}.seq`, required(path, record, "seq"));
  const kind = reasonCodeValue(`${path}.kind`, required(path, record, "kind"));
  const turnId = stringValue(`${path}.turn_id`, required(path, record, "turn_id"));
  const digest = optional(path, record, "candidate_digest");
  for (const key of ["text", "tool", "tool_call_id", "verdict", "reason_code", "hint", "outcome", "reason"] as const) {
    const text = optional(path, record, key);
    if (text !== undefined) boundedText(`${path}.${key}`, text);
  }
  const paths = optional(path, record, "paths");
  if (paths !== undefined) arrayValue(`${path}.paths`, paths, 256).forEach((entry, index) => boundedText(`${path}.paths[${index}]`, entry));
  return { seq, kind, turnId, candidateDigest: digest === undefined ? "" : stringValue(`${path}.candidate_digest`, digest) };
}

// The long-poll answer. The window must continue the cursor the client asked for, and events[i] and items[i] must pair up,
// because the poller reads the lifecycle kind from one and the transcript line from the other.
export function parseEventsAnswer(value: unknown, sessionId: string, requested: number): EventsAnswer {
  const record = asRecord("events", value);
  closedKeys("events", record, ["schema_version", "session_id", "cursor", "next_cursor", "gap", "events", "items"]);
  if (stringValue("events.session_id", required("events", record, "session_id")) !== sessionId) {
    fail("events.session_id", "is not the requested session");
  }
  const cursor = countValue("events.cursor", required("events", record, "cursor"));
  const nextCursor = countValue("events.next_cursor", required("events", record, "next_cursor"));
  const gap = boolValue("events.gap", required("events", record, "gap"));
  const events = arrayValue("events.events", required("events", record, "events"), MAX_EVENTS).map((entry, index) =>
    parseEvent(`events.events[${index}]`, entry),
  );
  const items = arrayValue("events.items", required("events", record, "items"), MAX_EVENTS);
  if (cursor !== requested) fail("events.cursor", "is not the requested cursor");
  if (items.length !== events.length) fail("events.items", "does not pair with events");
  if (nextCursor !== cursor + events.length) fail("events.next_cursor", "does not continue the cursor");
  events.forEach((event, index) => {
    if (event.seq !== cursor + index) fail(`events.events[${index}].seq`, "is not the next sequence number");
  });
  return { cursor, nextCursor, gap, events, items };
}

export interface ErrorBody {
  readonly code: string;
  readonly detail: string;
  readonly outcomeUnknown: boolean;
}

// The gateway's error body. Anything else (a proxy's page, a truncated body) is not an error body: the caller treats it as
// a failure whose cause it cannot read.
export function parseErrorBody(value: unknown): ErrorBody | null {
  try {
    const outer = asRecord("error", value);
    closedKeys("error", outer, ["error"]);
    const record = asRecord("error.error", required("error", outer, "error"));
    closedKeys("error.error", record, ["code", "detail", "outcome_unknown"]);
    const code = stringValue("error.error.code", required("error.error", record, "code"));
    if (!/^[A-Z][A-Z0-9_]{0,63}$/.test(code)) return null;
    const detail = optional("error.error", record, "detail");
    const unknown = optional("error.error", record, "outcome_unknown");
    return {
      code,
      detail: detail === undefined ? "" : stringValue("error.error.detail", detail),
      outcomeUnknown: unknown === true,
    };
  } catch {
    return null;
  }
}
