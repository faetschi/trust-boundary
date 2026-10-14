// format.js — pure, side-effect-free presentation helpers.
// Only allowlisted bounded fields are ever read here.

export const SUCCESS_STATES = new Set(["succeeded", "completed", "published", "ready"]);

export const TOOL_GLYPH = { read: "R", write: "W", edit: "E", bash: "$" };

export function slug(value) {
  return String(value ?? "unknown")
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "") || "unknown";
}

export function humanize(value) {
  return String(value ?? "")
    .replace(/[-_]/g, " ")
    .trim();
}

// A projection element is only "verified" when it was observed from the
// supervisor's durable transition under a claim-sufficient scope. An
// `authoritative` value is never produced by the gateway, an `observed` value is
// refused for fixture/replay/receipt sources by the Go boundary, and an
// `unavailable` or unverified scope never counts. This gate fails closed: a
// missing or unexpected value is not verified.
export function isVerified(item) {
  if (!item) return false;
  return item.authority === "observed" && item.verification_scope === "structural-audit-only";
}

export function isVerifiedProjection(state) {
  if (!state) return false;
  return state.source === "live" && state.verification === "structural-audit-only";
}

// Chip class for a state value, downgrading unverified success to "reported".
export function stateChipClass(stateValue, verified) {
  const s = slug(stateValue);
  if (SUCCESS_STATES.has(s) && !verified) return "reported";
  return s;
}

export function verdictChipClass(verdict) {
  const v = slug(verdict);
  if (v === "allow") return "allow";
  if (v === "deny") return "deny";
  return "unknown";
}

export function toolGlyph(tool) {
  return TOOL_GLYPH[tool] || "?";
}

export function shortDigest(value, head = 10, tail = 6) {
  if (!value) return "—";
  const text = String(value);
  if (text.length <= head + tail + 1) return text;
  return `${text.slice(0, head)}…${text.slice(-tail)}`;
}

export function formatClock(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 1970) return "—";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatStamp(value) {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 1970) return "—";
  return date.toLocaleString([], {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}
