// The words and tones the Chat view shows for a record. Pure: no DOM, no React. The honesty rules
// of web-chat.md section 4 live here, so they are tested without a browser:
//   - a policy ALLOW is a permission, drawn in the permission tone and never the success tone;
//   - a success is "succeeded" only with verified evidence, and "reported" otherwise;
//   - UNKNOWN, lost, unknown, pending and failed are each named and never called a success;
//   - lossy output is named, and it never takes the success tone.

import type { Verdict, ResultState } from "../../../core/contract";
import type { ChipTone } from "../../../ui/Chip";
import type { GovernanceCounts } from "../../../core/filters";

export interface ChipReading {
  // The text the chip shows. It carries the meaning; the tone is the colour, never the only cue.
  readonly label: string;
  readonly tone: ChipTone;
  // A plain sentence for the step that owns the chip, so the claim is stated in text too.
  readonly note: string;
}

export function verdictReading(verdict: Verdict): ChipReading {
  switch (verdict) {
    case "ALLOW":
      return { label: "ALLOW", tone: "allow", note: "policy ALLOW: a permission, not an outcome." };
    case "DENY":
      return { label: "DENY", tone: "deny", note: "policy DENY: the action was refused by policy." };
    case "UNKNOWN":
      return { label: "UNKNOWN", tone: "unknown", note: "policy verdict UNKNOWN: not decided, and not an ALLOW or a DENY." };
    default:
      // A verdict outside the closed set is not a permission. It reads as UNKNOWN.
      return { label: "UNKNOWN", tone: "unknown", note: "policy verdict UNKNOWN: not decided, and not an ALLOW or a DENY." };
  }
}

// `evidenced` is true only for a success with VerifiedEvidence (core format.ts verifyItem).
export function stateReading(state: ResultState, evidenced: boolean): ChipReading {
  switch (state) {
    case "succeeded":
      if (evidenced) return { label: "succeeded", tone: "ok", note: "verified by the gateway." };
      return { label: "reported", tone: "reported", note: "reported by the source, not independently verified." };
    case "denied":
      return { label: "denied", tone: "deny", note: "denied: the action did not run; not a success." };
    case "failed":
      return { label: "failed", tone: "deny", note: "failed: not a success." };
    case "pending":
      return { label: "pending", tone: "pending", note: "pending: no terminal outcome yet." };
    case "lost":
      return { label: "lost", tone: "lost", note: "lost: the terminal outcome was not recorded; not a success." };
    case "unknown":
      return { label: "unknown", tone: "unknown", note: "unknown: no terminal outcome retained; not a success." };
    default:
      return { label: "unknown", tone: "unknown", note: "unknown: no terminal outcome retained; not a success." };
  }
}

export const LOSSY_READING: ChipReading = {
  label: "lossy",
  tone: "unknown",
  note: "output is incomplete; lossy output is not terminal evidence.",
};

export const UNVERIFIED_READING: ChipReading = {
  label: "unverified",
  tone: "idle",
  note: "not independently verified.",
};

// The marker of a verified record in a mixed view. It has the success tone, as the verified posture does.
export const VERIFIED_READING: ChipReading = {
  label: "verified",
  tone: "ok",
  note: "verified by the gateway.",
};

export interface PostureEntry {
  readonly key: string;
  readonly text: string;
  readonly tone: ChipTone;
  readonly hint: string;
}

// The posture badges of the legacy toolbar, in the same order. A zero count is not shown.
export function postureEntries(counts: GovernanceCounts): PostureEntry[] {
  const entries: Array<readonly [string, number, string, ChipTone, string]> = [
    ["allow", counts.allow, "ALLOW", "allow", "policy ALLOW decisions: a permission, not an outcome"],
    ["deny", counts.deny, "DENY", "deny", "policy DENY decisions"],
    ["unknown-verdict", counts.unknownDecisions, "unknown verdict", "unknown", "decisions with an UNKNOWN verdict"],
    ["verified", counts.verified, "verified", "ok", "successful executions confirmed by the projection"],
    ["reported", counts.reported, "reported", "reported", "successes reported but not independently verified"],
    ["denied", counts.denied, "denied", "deny", "denied results"],
    ["failed", counts.failed, "failed", "deny", "failed results"],
    ["pending", counts.pendingResults, "pending", "pending", "results still pending"],
    ["unknown-result", counts.unknownResults, "unknown result", "unknown", "results with no retained terminal outcome"],
    ["lost", counts.lost, "lost", "lost", "results whose outcome was lost; not a success"],
    ["lossy", counts.lossy, "lossy", "unknown", "results flagged lossy; lossy output is not terminal evidence"],
  ];
  return entries
    .filter(([, count]) => count > 0)
    .map(([key, count, word, tone, hint]) => ({ key, text: `${word} ${count}`, tone, hint }));
}

// The line under the toolbar. It separates the matches from the elements shown, because a kept
// tool block also shows its context.
export function filterSummaryText(input: {
  readonly filter: string;
  readonly label: string;
  readonly total: number;
  readonly matches: number;
  readonly shown: number;
  readonly evicted: number;
}): string {
  const evicted = input.evicted > 0 ? ` · ${input.evicted} older element(s) evicted` : "";
  if (input.filter === "all") return `${input.total} element(s) in the retained window${evicted}`;
  const matches = `${input.matches} match${input.matches === 1 ? "" : "es"}`;
  return `${matches} · ${input.shown} of ${input.total} element(s) shown for “${input.label}” (context included)${evicted}`;
}

// The turn heading. The untagged turn has no id to show.
export function turnLabel(id: string): string {
  if (id === "__untagged") return "turn";
  return id.startsWith("turn") ? id : `turn ${id}`;
}

export const EMPTY_TOOL_SUMMARY = "tool call (no bounded summary projected)";

// The first bounded summary of a tool block, in the order the legacy transcript used.
export function firstSummary(candidates: ReadonlyArray<{ readonly summary?: string | undefined } | null>): string {
  for (const candidate of candidates) {
    if (candidate?.summary) return candidate.summary;
  }
  return EMPTY_TOOL_SUMMARY;
}
