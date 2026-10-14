// format.ts — pure, side-effect-free presentation helpers (port of format.mjs).
// Only allowlisted bounded fields are read here.
//
// The verified gate lives in verification.ts. A success may be shown as `ok` only with a VerifiedEvidence
// value, which verifyIn (verification.ts) is the only function that makes. A caller that holds only a
// state string gets `reported`.

import type { VerifiedEvidence } from "./verification";

export { isVerifiedProjection } from "./verification";

export const SUCCESS_STATES: ReadonlySet<string> = new Set(["succeeded", "completed", "published", "ready"]);

// Lookup tables keyed by a string from the wire. An unknown key reads a prototype
// property (for example "constructor") rather than undefined. The callers fall back
// on a falsy result, so the behaviour matches format.mjs exactly, including that case.
export const TOOL_GLYPH: Readonly<Record<string, string>> = { read: "R", write: "W", edit: "E", bash: "$" };

export function slug(value: unknown): string {
  return (
    String(value ?? "unknown")
      .toLowerCase()
      .replace(/[^a-z0-9-]/g, "-")
      .replace(/-+/g, "-")
      .replace(/^-|-$/g, "") || "unknown"
  );
}

export function humanize(value: unknown): string {
  return String(value ?? "")
    .replace(/[-_]/g, " ")
    .trim();
}

// Chip class for a state value, downgrading unverified success to "reported".
export function stateChipClass(stateValue: unknown, verified: boolean): string {
  const s = slug(stateValue);
  if (SUCCESS_STATES.has(s) && !verified) return "reported";
  return s;
}

// The chip class of a success state. Only a VerifiedEvidence yields the plain class.
export function successChipClass(stateValue: unknown, evidence: VerifiedEvidence | null): string {
  return stateChipClass(stateValue, evidence !== null);
}

export function verdictChipClass(verdict: unknown): "allow" | "deny" | "unknown" {
  const v = slug(verdict);
  if (v === "allow") return "allow";
  if (v === "deny") return "deny";
  return "unknown";
}

export function toolGlyph(tool: string | null | undefined): string {
  return TOOL_GLYPH[String(tool)] || "?";
}

export function shortDigest(value: unknown, head = 10, tail = 6): string {
  if (!value) return "—";
  const text = String(value);
  if (text.length <= head + tail + 1) return text;
  return `${text.slice(0, head)}…${text.slice(-tail)}`;
}

// A value the Date constructor accepts. Anything else is treated as "no time".
export type DateInput = string | number | Date;

function validDate(value: DateInput | null | undefined): Date | null {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime()) || date.getUTCFullYear() < 1970) return null;
  return date;
}

export function formatClock(value: DateInput | null | undefined): string {
  const date = validDate(value);
  if (!date) return "—";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatStamp(value: DateInput | null | undefined): string {
  const date = validDate(value);
  if (!date) return "—";
  return date.toLocaleString([], {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}
