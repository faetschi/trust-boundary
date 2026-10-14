// trajectory.mjs — the Trajectory view's record model.
//
// A pure, DOM-free projection of the bounded observation contract into a
// turn-aware record ledger. The *shape* is borrowed from the DeepSeek Harness
// trajectory view (source chips, a per-record status dot, a bounded payload
// preview, a record inspector, a grouped ledger); the *governance lens* is
// tbound's own, because the harness has no verdict, reason code, policy digest,
// generation or explicit UNKNOWN anywhere in its record vocabulary.
//
// Two invariants are enforced here rather than in the renderer:
//
//   1. Nothing is inferred. Every value comes from an allowlisted contract field
//      that the Go boundary has already validated and bounded.
//   2. No measurement is fabricated. `timingOf` reports availability explicitly
//      and never returns a duration, because the projection records a timestamp
//      and not a duration. The sequence rail is ordinal for the same reason.
//
// This module deliberately has no import of the finding filter: narrowing the
// timeline and searching the trajectory are separate concerns, and a search must
// never be able to change a projection, a verdict, or a filter outcome.

import {
  formatClock,
  humanize,
  shortDigest,
  slug,
  stateChipClass,
  verdictChipClass,
  isVerified,
} from "./format.mjs";
import { turnsOf } from "./store.mjs";

export const RECORD_GROUPS = [
  { id: "turn", label: "Turn", hint: "group records by their recorded turn" },
  { id: "tool", label: "Tool", hint: "group tool records by their tool call" },
  { id: "verdict", label: "Verdict", hint: "group records by their policy verdict" },
];

export const MAX_PREVIEW = 160;

// The chip vocabulary. `context` is deliberately absent: the projection has no
// context item type, and rendering one would present a capability the service
// does not have.
export function recordSource(item) {
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

const SOURCE_LABELS = {
  system: "System",
  user: "User",
  assistant: "Assistant",
  tool: "Tool",
  decision: "Decision",
  result: "Result",
  message: "Message",
  unknown: "Unknown",
};

export function sourceLabel(source) {
  return SOURCE_LABELS[source] || SOURCE_LABELS.unknown;
}

export function preview(text, max = MAX_PREVIEW) {
  const value = text === undefined || text === null ? "" : String(text);
  if (value.length <= max) return value;
  return `${value.slice(0, max)}…`;
}

// The status dot mirrors the chip tone exactly, so the two can never disagree
// about the same record. An unverified success is "reported", never "ok".
// A policy ALLOW is a permission, not an outcome, so it never maps to "ok".
const DOT_BY_VARIANT = {
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

export function recordDot(item) {
  if (!item) return "unknown";
  if (item.type === "decision") return DOT_BY_VARIANT[verdictChipClass(item.verdict)] || "unknown";
  if (item.type === "result" || item.type === "activity") {
    const dot = DOT_BY_VARIANT[stateChipClass(item.state, isVerified(item))] || "unknown";
    // Lossy output is incomplete and is not terminal evidence of success.
    return dot === "ok" && item.lossy ? "unknown" : dot;
  }
  return "idle";
}

export function recordLabel(item) {
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

// A one-line preview built only from bounded contract fields. Digests are shown
// shortened and never in full here; the inspector carries the shorter form plus
// the untruncated value in the field's title.
export function recordPayload(item) {
  if (!item) return "";
  switch (item.type) {
    case "message":
      return preview(item.text);
    case "activity":
      return preview(item.summary);
    case "decision": {
      const parts = [];
      if (item.reason_code) parts.push(`reason ${item.reason_code}`);
      if (item.policy_digest) parts.push(`policy ${shortDigest(item.policy_digest)}`);
      if (!parts.length && item.summary) parts.push(preview(item.summary));
      return parts.join(" · ") || "no bounded detail projected";
    }
    case "result": {
      const parts = [];
      if (item.summary) parts.push(preview(item.summary));
      if (item.digest) parts.push(`digest ${shortDigest(item.digest)}`);
      if (item.lossy) parts.push("output lossy");
      return parts.join(" · ") || "no bounded detail projected";
    }
    default:
      return "";
  }
}

export function recordChips(item) {
  if (!item) return [];
  const chips = [];
  if (item.type === "decision") {
    chips.push({
      label: item.verdict || "UNKNOWN",
      variant: verdictChipClass(item.verdict),
      hint: "policy verdict as reported by the source",
    });
  }
  if ((item.type === "result" || item.type === "activity") && item.state) {
    chips.push({
      label: item.state,
      variant: stateChipClass(item.state, isVerified(item)),
      hint: isVerified(item)
        ? "verified by the projection"
        : "reported by the source, not independently verified",
    });
  }
  if (item.lossy) {
    chips.push({
      label: "lossy",
      variant: "unknown",
      hint: "output is incomplete; lossy output is not terminal evidence",
    });
  }
  if (!isVerified(item)) {
    chips.push({
      label: "unverified",
      variant: "unverified",
      hint: "the projection does not independently verify this record",
    });
  }
  return chips;
}

// Timing is reported as a recorded timestamp plus an explicit unavailability.
// Returning a duration here would fabricate a measurement the projection never
// took, which is the one thing the harness's own trajectory view also refuses to
// do ("in-flight records show a start marker without inventing elapsed time").
export function timingOf(item) {
  const value = item && item.occurred_at ? item.occurred_at : null;
  const clock = formatClock(value);
  return {
    available: false,
    recordedAt: value,
    clock: clock === "—" ? null : clock,
    duration: null,
    reason:
      "the projection records a timestamp, not a duration; no elapsed time is shown because none was recorded",
  };
}

const LANES = [
  { id: "model", label: "Model", match: (item) => item.type === "message" },
  { id: "policy", label: "Policy", match: (item) => item.type === "decision" },
  {
    id: "tools",
    label: "Tools",
    match: (item) => item.type === "activity" || item.type === "result",
  },
];

function toneOf(item) {
  const dot = recordDot(item);
  if (dot === "deny" || dot === "lost") return "deny";
  if (dot === "ok") return "ok";
  if (dot === "allow") return "allow";
  if (dot === "reported") return "reported";
  if (dot === "pending") return "pending";
  // A message is recorded but carries no verdict or outcome: it is drawn as
  // "idle", distinct from the "muted" span of a lane that did not act at all.
  if (dot === "idle") return "idle";
  return "muted";
}

// Segment weights are bucketed into classes rather than inline styles: the
// gateway's CSP allows no inline style, and the rail is ordinal anyway.
export function weightClass(weight) {
  return `seg-w${Math.max(1, Math.min(8, Math.round(weight)))}`;
}

// sequenceLanes builds the ordinal rail. Every lane walks the same record order,
// so the three lanes share one x-axis (the record sequence) and a lane's muted
// spans show what it did *not* touch at that position. No span is a duration.
export function sequenceLanes(items) {
  const lanes = [];
  for (const lane of LANES) {
    const segments = [];
    for (const item of items) {
      const tone = lane.match(item) ? toneOf(item) : "muted";
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
    note:
      "The axis is record sequence, not time: the projection records no duration, so no span here is drawn as one.",
  };
}

// Only allowlisted bounded fields are searchable. Raw paths, arguments and
// result bytes are not in the projection and can never be matched here.
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
];

export function searchRecords(items, query) {
  const needle = String(query === undefined || query === null ? "" : query).trim().toLowerCase();
  if (!needle) return items.slice();
  return items.filter((item) =>
    SEARCH_FIELDS.some((field) => {
      const value = item[field];
      return typeof value === "string" && value.toLowerCase().includes(needle);
    }),
  );
}

export function groupRecords(items, mode) {
  if (mode === "tool") return groupByTool(items);
  if (mode === "verdict") return groupByVerdict(items);
  return groupByTurn(items);
}

function groupByTurn(items) {
  return turnsOf(items).map((turn) => ({
    id: turn.id,
    title: turn.id === "__untagged" ? "Turn (not tagged)" : `Turn ${turn.id}`,
    meta: `${turn.items.length} record(s)`,
    records: turn.items.map(recordOf),
  }));
}

function groupByTool(items) {
  const groups = [];
  const byKey = new Map();
  for (const item of items) {
    if (item.type === "message") {
      let messages = groups.find((group) => group.id === "__messages");
      if (!messages) {
        messages = { id: "__messages", title: "Messages", meta: "", appends: true, records: [] };
        groups.push(messages);
      }
      messages.records.push(recordOf(item));
      continue;
    }
    const key = item.tool_call_id || item.id;
    let group = byKey.get(key);
    if (!group) {
      group = { id: key, title: item.tool || "tool", meta: "", records: [] };
      byKey.set(key, group);
      groups.push(group);
    }
    group.records.push(recordOf(item));
  }
  for (const group of groups) {
    group.meta = group.id === "__messages" ? `${group.records.length} record(s)` : `${group.records.length} record(s) · ${group.id}`;
  }
  return groups;
}

function groupByVerdict(items) {
  const verdictByCall = new Map();
  for (const item of items) {
    if (item.type === "decision") {
      verdictByCall.set(item.tool_call_id || item.id, item.verdict || "UNKNOWN");
    }
  }
  const order = [];
  const byId = new Map();
  const push = (id, title, item) => {
    let group = byId.get(id);
    if (!group) {
      group = { id, title, meta: "", records: [] };
      byId.set(id, group);
      order.push(group);
    }
    group.records.push(recordOf(item));
  };
  for (const item of items) {
    if (item.type === "message") {
      push("no-verdict", "No verdict", item);
      continue;
    }
    const verdict =
      (item.type === "decision" ? item.verdict : verdictByCall.get(item.tool_call_id || item.id)) || null;
    if (!verdict) push("no-verdict", "No verdict", item);
    else push(slug(verdict), humanize(verdict).toUpperCase(), item);
  }
  for (const group of order) group.meta = `${group.records.length} record(s)`;
  return order;
}

// The record inspector's sections. Only the sections a record type can actually
// carry are produced, and the Timing section is always present so the absence of
// a duration is always stated rather than left to inference.
export function recordDetail(item) {
  if (!item) return [];
  const sections = [];

  const identity = [
    ["source", sourceLabel(recordSource(item))],
    ["record id", item.id],
    ["sequence", String(item.sequence)],
    ["turn", item.turn_id || "not tagged"],
  ];
  if (item.tool) identity.push(["tool", item.tool]);
  if (item.tool_call_id) identity.push(["tool call", item.tool_call_id]);
  sections.push({ id: "summary", title: "Summary", rows: identity });

  const payload = [];
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
    const rows = [["state", item.state || "unknown", { chip: stateChipClass(item.state, isVerified(item)) }]];
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
      ["independently verified", isVerified(item) ? "yes" : "no"],
    ],
  });

  const timing = timingOf(item);
  const timingRows = [
    ["recorded at", timing.clock ? `${timing.clock} — a clock reading, not a duration` : "unavailable"],
    ["duration", "unavailable — the projection records no duration"],
  ];
  if (item.occurred_at) timingRows.push(["recorded timestamp", item.occurred_at, { mono: true }]);
  sections.push({ id: "timing", title: "Timing", rows: timingRows, note: timing.reason });

  return sections;
}

export function recordOf(item) {
  return {
    id: item.id,
    sequence: item.sequence,
    source: recordSource(item),
    label: recordLabel(item),
    payload: recordPayload(item),
    dot: recordDot(item),
    chips: recordChips(item),
    detail: recordDetail(item),
    item,
  };
}

// The ledger's visible records and their groups. The timeline's finding filter is
// intentionally not applied: the trajectory is the whole retained window, and the
// search is the only narrowing control here.
export function visibleRecords(state) {
  const items = searchRecords(state.items || [], state.query || "");
  return { items, groups: groupRecords(items, state.group || "turn") };
}
