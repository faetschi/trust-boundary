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

// `evidenced` is true only for a success that the shared gate verified (app verification.ts). A lossy
// success never takes the success tone: its output is incomplete, so it is not confirmed, whatever its
// verification.
export function stateReading(state: ResultState, evidenced: boolean, lossy = false): ChipReading {
  switch (state) {
    case "succeeded":
      if (evidenced && lossy) {
        return {
          label: "succeeded (output lossy, not confirmed)",
          tone: "unknown",
          note: "verified, but its output is lossy: not counted as confirmed; lossy output is not terminal evidence.",
        };
      }
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

// The marker of a verified success whose output is lossy. Its evidence is verified, but the output is
// incomplete, so it is not confirmed: the marker is neutral and never the success tone.
export const LOSSY_EVIDENCE_READING: ChipReading = {
  label: "verified evidence, output lossy",
  tone: "idle",
  note: "verified by the gateway; its output is lossy, so it is not confirmed.",
};

// The reading of a message: the shared gate's verdict in words. A message that the gate does not
// verify reads "reported, not verified". A message whose verification scope the projection does not carry reads
// "verification unavailable", and is never verified.
export const REPORTED_NOT_VERIFIED_READING: ChipReading = {
  label: "reported, not verified",
  tone: "reported",
  note: "reported by the source, not independently verified.",
};

export const VERIFICATION_UNAVAILABLE_READING: ChipReading = {
  label: "verification unavailable",
  tone: "idle",
  note: "the projection carries no verification scope for this record.",
};

const SCOPES_WITH_A_VERDICT: ReadonlySet<string> = new Set(["unverified-input", "structural-audit-only"]);

export function messageReading(scope: string | undefined, verified: boolean): ChipReading {
  if (scope === undefined || !SCOPES_WITH_A_VERDICT.has(scope)) return VERIFICATION_UNAVAILABLE_READING;
  return verified ? VERIFIED_READING : REPORTED_NOT_VERIFIED_READING;
}

export interface PostureEntry {
  readonly key: string;
  readonly text: string;
  readonly tone: ChipTone;
  readonly hint: string;
}

// The posture badges of the legacy toolbar, in the same order. A zero count is not shown. A verdict is a policy decision
// and a result is an execution, and each count says which, so no word names two different counts on the same screen.
export function postureEntries(counts: GovernanceCounts): PostureEntry[] {
  const entries: Array<readonly [string, number, string, ChipTone, string]> = [
    ["allow", counts.allow, "allow verdict", "allow", "policy ALLOW verdicts: a permission, not an outcome"],
    ["deny", counts.deny, "deny verdict", "deny", "policy DENY verdicts: the action was refused by policy"],
    [
      "unknown-verdict",
      counts.unknownDecisions,
      "unknown verdict",
      "unknown",
      "policy verdicts that are UNKNOWN: not decided, and not an ALLOW or a DENY",
    ],
    ["verified", counts.verified, "verified", "ok", "successful executions confirmed by the projection"],
    [
      "reported",
      counts.reported,
      "reported",
      "reported",
      "successes not confirmed: reported but not independently verified, or verified with lossy output",
    ],
    ["denied", counts.denied, "denied result", "deny", "results with the denied outcome: the action did not run"],
    ["failed", counts.failed, "failed result", "deny", "results that failed"],
    ["pending", counts.pendingResults, "pending", "pending", "results still pending"],
    ["unknown-result", counts.unknownResults, "unresolved result", "unknown", "results with no retained terminal outcome"],
    ["lost", counts.lost, "lost", "lost", "results whose outcome was lost; not a success"],
    ["lossy", counts.lossy, "lossy", "unknown", "results flagged lossy; lossy output is not terminal evidence"],
  ];
  return entries
    .filter(([, count]) => count > 0)
    .map(([key, count, word, tone, hint]) => ({ key, text: `${word} ${count}`, tone, hint }));
}

// What the Chat view calls each filter, and what its count counts. A filter counts records, so its name says what it counts:
// "Denied (verdict or result)" counts both, and the posture chips count each on its own. The hint is the full definition,
// shown as the filter's title. The policy verdicts are the footer's and the decisions tab's. The core filter ids and
// labels are the legacy client's (core/filters.ts), and they stay as they are.
export interface FilterName {
  readonly label: string;
  readonly hint: string;
}

const FILTER_NAMES: Readonly<Record<string, FilterName>> = {
  all: { label: "All", hint: "every projected element" },
  denied: { label: "Denied (verdict or result)", hint: "records with a DENY verdict or a denied result" },
  // Unknown or lossy counts a union: an UNKNOWN verdict, an unknown or lost result, or lossy output. A lost result is in the
  // unknown family (its outcome was not recorded). The result chip reads "unresolved", so the bare word unknown on this
  // screen counts verdicts, as the footer's "0 unknown" does.
  unknown: {
    label: "Unknown or lossy",
    hint: "Records whose verdict or result is unknown, or whose output is lossy",
  },
  failed: { label: "Failed result", hint: "records with a failed result" },
};

export function filterNameOf(id: string): FilterName {
  return Object.hasOwn(FILTER_NAMES, id) ? (FILTER_NAMES[id] as FilterName) : { label: id, hint: "" };
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
