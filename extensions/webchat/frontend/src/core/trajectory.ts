// trajectory.ts — the Trajectory view's record model (port of trajectory.mjs).
//
// A pure, DOM-free projection of the bounded observation contract into a turn-aware
// record ledger. The shape is borrowed from the DeepSeek Harness trajectory view (source
// chips, a per-record status dot, a bounded payload preview, a record inspector, a grouped
// ledger); the governance lens is tbound's own, because the harness has no verdict, reason
// code, policy digest, generation or explicit UNKNOWN anywhere in its record vocabulary.
//
// Two invariants are enforced here rather than in the renderer:
//
//   1. Nothing is inferred. Every value comes from an allowlisted contract field that the
//      Go boundary has already validated and bounded.
//   2. No measurement is fabricated. `timingOf` reports availability explicitly and never
//      returns a duration, because the projection records a timestamp and not a duration.
//      The sequence rail is ordinal for the same reason.
//   3. A record is verified only in its projection. Every judgement takes the projection
//      (verification.ts), so the record alone never decides.
//
// This module deliberately has no import of the finding filter: narrowing the timeline and
// searching the trajectory are separate concerns, and a search must never be able to change
// a projection, a verdict, or a filter outcome.

import type { ItemView, Tone } from "./contract";
import { formatClock, humanize, shortDigest, slug, stateChipClass, verdictChipClass } from "./format";
import { isVerifiedIn, verifyIn, type ProjectionState } from "./verification";
import { turnsOf } from "./store";

export interface RecordGroupDef {
  readonly id: string;
  readonly label: string;
  readonly hint: string;
}

export const RECORD_GROUPS: readonly RecordGroupDef[] = [
  { id: "turn", label: "Turn", hint: "group records by their recorded turn" },
  { id: "tool", label: "Tool", hint: "group tool records by their tool call" },
  { id: "verdict", label: "Verdict", hint: "group records by their policy verdict" },
];

export const MAX_PREVIEW = 160;

export type RecordSource =
  | "decision"
  | "result"
  | "tool"
  | "user"
  | "assistant"
  | "system"
  | "message"
  | "unknown";

// The chip vocabulary. `context` is deliberately absent: the projection has no context
// item type, and rendering one would present a capability the service does not have.
export function recordSource(item: ItemView | null | undefined): RecordSource {
  if (!item) return "unknown";
  switch (item.type) {
    case "decision":
      return "decision";
    case "result":
      return "result";
    case "activity":
      return "tool";
    case "message":
      if (item.role === "user") return "user";
      if (item.role === "assistant") return "assistant";
      if (item.role === "system") return "system";
      return "message";
    default:
      return "unknown";
  }
}

const SOURCE_LABELS: Readonly<Record<string, string>> = {
  system: "System",
  user: "User",
  assistant: "Assistant",
  tool: "Tool",
  decision: "Decision",
  result: "Result",
  message: "Message",
  unknown: "Unknown",
};

export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] || SOURCE_LABELS.unknown || "Unknown";
}

export function preview(text: unknown, max = MAX_PREVIEW): string {
  const value = text === undefined || text === null ? "" : String(text);
  if (value.length <= max) return value;
  return `${value.slice(0, max)}…`;
}

// The status dot mirrors the chip tone exactly, so the two can never disagree about the
// same record. An unverified success is "reported", never "ok". A policy ALLOW is a
// permission, not an outcome, so it never maps to "ok".
const DOT_BY_VARIANT: Readonly<Record<string, Tone>> = {
  allow: "allow",
  succeeded: "ok",
  completed: "ok",
  published: "ok",
  ready: "ok",
  deny: "deny",
  denied: "deny",
  failed: "deny",
  cancelled: "deny",
  lost: "lost",
  pending: "pending",
  reported: "reported",
  unknown: "unknown",
  unavailable: "unknown",
  unverified: "unknown",
};

export function recordDot(item: ItemView | null | undefined, projection: ProjectionState | null | undefined): Tone {
  if (!item) return "unknown";
  if (item.type === "decision") return DOT_BY_VARIANT[verdictChipClass(item.verdict)] || "unknown";
  if (item.type === "result" || item.type === "activity") {
    // The evidence is the only route to "ok": without it a success reads as reported.
    const dot = DOT_BY_VARIANT[successChip(item, projection)] || "unknown";
    // Lossy output is incomplete and is not terminal evidence of success.
    return dot === "ok" && item.lossy ? "unknown" : dot;
  }
  return "idle";
}

function successChip(item: ItemView, projection: ProjectionState | null | undefined): string {
  return stateChipClass(item.state, verifyIn(projection, item) !== null);
}

// The status as words. The dot is decorative for assistive technology, so the same state is
// carried on the record's accessible name, once. The words match the dot's tone: a policy
// ALLOW is a permission, an unverified success is "reported", and nothing is called a
// success that the projection did not confirm.
const NOT_VERIFIED = ", not independently verified";

const VERDICT_WORDS: Readonly<Record<string, string>> = {
  allow: "ALLOW (permission, not an outcome)",
  deny: "DENY (refusal, not an outcome)",
  unknown: "UNKNOWN (neither permission nor refusal)",
};

const STATE_WORDS: Readonly<Record<string, string>> = { unknown: "UNKNOWN", lost: "LOST" };

function outcomeWords(item: ItemView, tone: Tone): string {
  const word = STATE_WORDS[slug(item.state)] || humanize(item.state);
  const lossy = item.lossy ? "; output lossy" : "";
  switch (tone) {
    case "ok":
      return `${word}, verified${lossy}`;
    case "reported":
      return `${word}, reported${NOT_VERIFIED}${lossy}`;
    case "deny":
    case "lost":
      return `${word}, not success${lossy}`;
    case "pending":
      return `${word}, no outcome yet${lossy}`;
    default:
      // Lossy output takes the unknown tone, so the words must not call it confirmed.
      if (item.lossy && slug(item.state) !== "unknown") return `${word}, output lossy, not counted as confirmed`;
      return `${word}, not success${lossy}`;
  }
}

export function statusText(item: ItemView | null | undefined, projection: ProjectionState | null | undefined): string {
  if (!item) return "Status not recorded";
  if (item.type === "message") return "Message: no verdict or outcome recorded";
  if (item.type === "decision") {
    const verdict = VERDICT_WORDS[verdictChipClass(item.verdict)];
    return `Policy verdict: ${verdict}${isVerifiedIn(projection, item) ? "" : NOT_VERIFIED}`;
  }
  const label = item.type === "result" ? "Result" : "Activity";
  return `${label}: ${outcomeWords(item, recordDot(item, projection))}`;
}

export function recordLabel(item: ItemView | null | undefined): string {
  if (!item) return "record";
  switch (item.type) {
    case "activity":
      return item.kind || "activity";
    case "decision":
      return `${item.tool || "tool"} · reviewed`;
    case "result":
      return item.tool || "tool";
    case "message":
      if (item.role === "user") return "prompt";
      if (item.role === "system") return "system note";
      return "message";
    default:
      return "record";
  }
}

// A one-line preview built only from bounded contract fields. Digests are shown shortened
// and never in full here; the inspector carries the shorter form plus the untruncated value
// in the field's title.
export function recordPayload(item: ItemView | null | undefined): string {
  if (!item) return "";
  switch (item.type) {
    case "message":
      return preview(item.text);
    case "activity":
      return preview(item.summary);
    case "decision": {
      const parts: string[] = [];
      if (item.reason_code) parts.push(`reason ${item.reason_code}`);
      if (item.policy_digest) parts.push(`policy ${shortDigest(item.policy_digest)}`);
      if (!parts.length && item.summary) parts.push(preview(item.summary));
      return parts.join(" · ") || "no bounded detail projected";
    }
    case "result": {
      const parts: string[] = [];
      if (item.summary) parts.push(preview(item.summary));
      if (item.digest) parts.push(`digest ${shortDigest(item.digest)}`);
      if (item.lossy) parts.push("output lossy");
      return parts.join(" · ") || "no bounded detail projected";
    }
    default:
      return "";
  }
}

export interface RecordChip {
  readonly label: string;
  readonly variant: string;
  readonly hint: string;
}

export function recordChips(item: ItemView | null | undefined, projection: ProjectionState | null | undefined): RecordChip[] {
  if (!item) return [];
  const chips: RecordChip[] = [];
  if (item.type === "decision") {
    chips.push({
      label: item.verdict || "UNKNOWN",
      variant: verdictChipClass(item.verdict),
      hint: "policy verdict as reported by the source",
    });
  }
  if ((item.type === "result" || item.type === "activity") && item.state) {
    const verified = isVerifiedIn(projection, item);
    chips.push({
      label: item.state,
      variant: stateChipClass(item.state, verified),
      hint: verified ? "verified by the projection" : "reported by the source, not independently verified",
    });
  }
  if (item.lossy) {
    chips.push({
      label: "lossy",
      variant: "unknown",
      hint: "output is incomplete; lossy output is not terminal evidence",
    });
  }
  if (!isVerifiedIn(projection, item)) {
    chips.push({
      label: "unverified",
      variant: "unverified",
      hint: "the projection does not independently verify this record",
    });
  }
  return chips;
}

export interface Timing {
  readonly available: false;
  readonly recordedAt: string | null;
  readonly clock: string | null;
  readonly duration: null;
  readonly reason: string;
}

// Timing is reported as a recorded timestamp plus an explicit unavailability. Returning a
// duration here would fabricate a measurement the projection never took, which is the one
// thing the harness's own trajectory view also refuses to do ("in-flight records show a
// start marker without inventing elapsed time").
export function timingOf(item: ItemView | null | undefined): Timing {
  const value = item && item.occurred_at ? item.occurred_at : null;
  const clock = formatClock(value);
  return {
    available: false,
    recordedAt: value,
    clock: clock === "—" ? null : clock,
    duration: null,
    reason: "the projection records a timestamp, not a duration; no elapsed time is shown because none was recorded",
  };
}

interface LaneDef {
  readonly id: string;
  readonly label: string;
  readonly match: (item: ItemView) => boolean;
}

const LANES: readonly LaneDef[] = [
  { id: "model", label: "Model", match: (item) => item.type === "message" },
  { id: "policy", label: "Policy", match: (item) => item.type === "decision" },
  {
    id: "tools",
    label: "Tools",
    match: (item) => item.type === "activity" || item.type === "result",
  },
];

// A rail segment's tone: a record tone, or `muted` for a lane that did not act here.
export type RailTone = Tone | "muted";

function toneOf(item: ItemView, projection: ProjectionState | null | undefined): RailTone {
  const dot = recordDot(item, projection);
  if (dot === "deny" || dot === "lost") return "deny";
  if (dot === "ok") return "ok";
  if (dot === "allow") return "allow";
  if (dot === "reported") return "reported";
  if (dot === "pending") return "pending";
  // An UNKNOWN outcome is drawn as itself: it is not a lane that did not act.
  if (dot === "unknown") return "unknown";
  // A message is recorded but carries no verdict or outcome: it is drawn as "idle", distinct
  // from the "muted" span of a lane that did not act at all.
  if (dot === "idle") return "idle";
  return "muted";
}

// The rail's accessible name states each tone in words, because segments are colour only.
// Wording matches statusText and never calls a verdict or an unverified outcome a success.
const TONE_WORDS: Readonly<Record<string, string>> = {
  allow: "ALLOW, a permission not an outcome",
  ok: "succeeded, verified",
  reported: "succeeded, reported, not independently verified",
  deny: "DENY, denied, failed or lost, not success",
  pending: "pending, no outcome yet",
  unknown: "UNKNOWN, not success",
  idle: "message, no verdict or outcome",
  muted: "no record in this lane",
};

export function toneText(tone: string): string {
  return TONE_WORDS[tone] || TONE_WORDS.muted || "no record in this lane";
}

// Segment weights are bucketed into classes rather than inline styles: the gateway's CSP
// allows no inline style, and the rail is ordinal anyway.
export function weightClass(weight: number): string {
  return `seg-w${Math.max(1, Math.min(8, Math.round(weight)))}`;
}

export interface RailSegment {
  readonly tone: RailTone;
  weight: number;
}

export interface RailLane {
  readonly id: string;
  readonly label: string;
  readonly segments: RailSegment[];
}

export interface RailModel {
  readonly lanes: RailLane[];
  readonly records: number;
  readonly axis: string;
  readonly ordinal: true;
  readonly note: string;
}

// sequenceLanes builds the ordinal rail. Every lane walks the same record order, so the
// three lanes share one x-axis (the record sequence) and a lane's muted spans show what it
// did *not* touch at that position. No span is a duration.
export function sequenceLanes(items: readonly ItemView[], projection: ProjectionState | null | undefined): RailModel {
  const lanes: RailLane[] = [];
  for (const lane of LANES) {
    const segments: RailSegment[] = [];
    for (const item of items) {
      const tone: RailTone = lane.match(item) ? toneOf(item, projection) : "muted";
      const last = segments[segments.length - 1];
      if (last && last.tone === tone) last.weight += 1;
      else segments.push({ tone, weight: 1 });
    }
    lanes.push({ id: lane.id, label: lane.label, segments });
  }
  return {
    lanes,
    records: items.length,
    axis: "record sequence",
    ordinal: true,
    note: "The axis is record sequence, not time: the projection records no duration, so no span here is drawn as one.",
  };
}

// Only allowlisted bounded fields are searchable. Raw paths, arguments and result bytes are
// not in the projection and can never be matched here.
const SEARCH_FIELDS = [
  "id",
  "text",
  "summary",
  "detail",
  "tool",
  "kind",
  "state",
  "verdict",
  "reason_code",
  "tool_call_id",
  "turn_id",
] as const satisfies readonly (keyof ItemView)[];

export function searchRecords<T extends ItemView>(items: readonly T[], query: unknown): T[] {
  const needle = String(query === undefined || query === null ? "" : query)
    .trim()
    .toLowerCase();
  if (!needle) return items.slice();
  return items.filter((item) =>
    SEARCH_FIELDS.some((field) => {
      const value = item[field];
      return typeof value === "string" && value.toLowerCase().includes(needle);
    }),
  );
}

export interface RecordView {
  readonly id: string | undefined;
  readonly sequence: number | undefined;
  readonly source: RecordSource;
  readonly label: string;
  readonly payload: string;
  readonly dot: Tone;
  readonly chips: RecordChip[];
  readonly detail: DetailSection[];
  readonly item: ItemView;
}

export interface RecordGroup {
  readonly id: string | undefined;
  readonly title: string;
  meta: string;
  readonly appends?: boolean;
  readonly records: RecordView[];
}

export function groupRecords(items: readonly ItemView[], mode: string, projection: ProjectionState | null | undefined): RecordGroup[] {
  if (mode === "tool") return groupByTool(items, projection);
  if (mode === "verdict") return groupByVerdict(items, projection);
  return groupByTurn(items, projection);
}

function groupByTurn(items: readonly ItemView[], projection: ProjectionState | null | undefined): RecordGroup[] {
  return turnsOf(items).map((turn) => ({
    id: turn.id,
    title: turn.id === "__untagged" ? "Turn (not tagged)" : `Turn ${turn.id}`,
    meta: `${turn.items.length} record(s)`,
    records: turn.items.map((item) => recordOf(item, projection)),
  }));
}

function groupByTool(items: readonly ItemView[], projection: ProjectionState | null | undefined): RecordGroup[] {
  const groups: RecordGroup[] = [];
  const byKey = new Map<string | undefined, RecordGroup>();
  for (const item of items) {
    if (item.type === "message") {
      let messages = groups.find((group) => group.id === "__messages");
      if (!messages) {
        messages = { id: "__messages", title: "Messages", meta: "", appends: true, records: [] };
        groups.push(messages);
      }
      messages.records.push(recordOf(item, projection));
      continue;
    }
    const key = item.tool_call_id || item.id;
    let group = byKey.get(key);
    if (!group) {
      group = { id: key, title: item.tool || "tool", meta: "", records: [] };
      byKey.set(key, group);
      groups.push(group);
    }
    group.records.push(recordOf(item, projection));
  }
  for (const group of groups) {
    group.meta =
      group.id === "__messages" ? `${group.records.length} record(s)` : `${group.records.length} record(s) · ${group.id}`;
  }
  return groups;
}

function groupByVerdict(items: readonly ItemView[], projection: ProjectionState | null | undefined): RecordGroup[] {
  const verdictByCall = new Map<string | undefined, string>();
  for (const item of items) {
    if (item.type === "decision") {
      verdictByCall.set(item.tool_call_id || item.id, item.verdict || "UNKNOWN");
    }
  }
  const order: RecordGroup[] = [];
  const byId = new Map<string | undefined, RecordGroup>();
  const push = (id: string | undefined, title: string, item: ItemView): void => {
    let group = byId.get(id);
    if (!group) {
      group = { id, title, meta: "", records: [] };
      byId.set(id, group);
      order.push(group);
    }
    group.records.push(recordOf(item, projection));
  };
  for (const item of items) {
    if (item.type === "message") {
      push("no-verdict", "No verdict", item);
      continue;
    }
    const verdict = (item.type === "decision" ? item.verdict : verdictByCall.get(item.tool_call_id || item.id)) || null;
    if (!verdict) push("no-verdict", "No verdict", item);
    else push(slug(verdict), humanize(verdict).toUpperCase(), item);
  }
  for (const group of order) group.meta = `${group.records.length} record(s)`;
  return order;
}

// A cell of the inspector: a label, a value, and how to draw the value.
export type DetailRow = readonly [label: string, value: string | undefined, options?: DetailRowOptions];

export interface DetailRowOptions {
  readonly chip?: string;
  readonly mono?: boolean;
  readonly digest?: boolean;
}

export interface DetailSection {
  readonly id: string;
  readonly title: string;
  readonly rows: DetailRow[];
  readonly note?: string;
}

// The record inspector's sections. Only the sections a record type can actually carry are
// produced, and the Timing section is always present so the absence of a duration is always
// stated rather than left to inference.
export function recordDetail(item: ItemView | null | undefined, projection: ProjectionState | null | undefined): DetailSection[] {
  if (!item) return [];
  const sections: DetailSection[] = [];

  const identity: DetailRow[] = [
    ["source", sourceLabel(recordSource(item))],
    ["record id", item.id],
    ["sequence", String(item.sequence)],
    ["turn", item.turn_id || "not tagged"],
  ];
  if (item.tool) identity.push(["tool", item.tool]);
  if (item.tool_call_id) identity.push(["tool call", item.tool_call_id]);
  sections.push({ id: "summary", title: "Summary", rows: identity });

  const payload: DetailRow[] = [];
  if (item.type === "message") {
    payload.push(["text", item.text || "(empty)"]);
    if (item.text_truncated) payload.push(["truncation", "the projection truncated this text"]);
  } else if (item.type === "activity") {
    payload.push(["kind", item.kind || "—"]);
    payload.push(["summary", item.summary || "—"]);
    if (item.detail) payload.push(["detail", item.detail]);
  } else if (item.type === "result") {
    if (item.summary) payload.push(["summary", item.summary]);
    if (item.detail) payload.push(["detail", item.detail]);
  } else if (item.summary) {
    payload.push(["summary", item.summary]);
  }
  if (payload.length) sections.push({ id: "payload", title: "Payload", rows: payload });

  if (item.type === "decision") {
    sections.push({
      id: "decision",
      title: "Decision",
      rows: [
        ["verdict", item.verdict || "UNKNOWN", { chip: verdictChipClass(item.verdict) }],
        ["reason code", item.reason_code || "—", { mono: true }],
        ["policy digest", item.policy_digest || "—", { digest: true }],
      ],
    });
  }

  if (item.type === "result") {
    const rows: DetailRow[] = [["state", item.state || "unknown", { chip: stateChipClass(item.state, isVerifiedIn(projection, item)) }]];
    if (item.lossy) rows.push(["lossy", "yes — output completeness is not a terminal outcome"]);
    rows.push(["digest", item.digest || "—", { digest: true }]);
    sections.push({ id: "result", title: "Result", rows });
  }

  sections.push({
    id: "evidence",
    title: "Evidence",
    rows: [
      ["authority", item.authority || "unknown", { mono: true }],
      ["verification", item.verification_scope || "unavailable", { mono: true }],
      ["independently verified", isVerifiedIn(projection, item) ? "yes" : "no"],
    ],
  });

  const timing = timingOf(item);
  const timingRows: DetailRow[] = [
    ["recorded at", timing.clock ? `${timing.clock} — a clock reading, not a duration` : "unavailable"],
    ["duration", "unavailable — the projection records no duration"],
  ];
  if (item.occurred_at) timingRows.push(["recorded timestamp", item.occurred_at, { mono: true }]);
  sections.push({ id: "timing", title: "Timing", rows: timingRows, note: timing.reason });

  return sections;
}

export function recordOf(item: ItemView, projection: ProjectionState | null | undefined): RecordView {
  return {
    id: item.id,
    sequence: item.sequence,
    source: recordSource(item),
    label: recordLabel(item),
    payload: recordPayload(item),
    dot: recordDot(item, projection),
    chips: recordChips(item, projection),
    detail: recordDetail(item, projection),
    item,
  };
}

export interface TrajectoryInput {
  readonly items?: readonly ItemView[];
  readonly query?: string;
  readonly group?: string;
  // The projection the records are judged in. Required: a record is verified only in its projection.
  readonly projection: ProjectionState | null | undefined;
}

// The ledger's visible records and their groups. The timeline's finding filter is
// intentionally not applied: the trajectory is the whole retained window, and the search is
// the only narrowing control here.
export function visibleRecords(state: TrajectoryInput): { items: ItemView[]; groups: RecordGroup[] } {
  const items = searchRecords(state.items || [], state.query || "");
  return { items, groups: groupRecords(items, state.group || "turn", state.projection) };
}
