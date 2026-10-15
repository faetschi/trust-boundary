// The honesty rules of the Chat view, on the pure readings (readings.ts). The DOM-level checks are
// in ../ChatView.test.tsx; these pin the words and tones that every view shows.

import { describe, expect, it } from "vitest";
import type { GovernanceCounts } from "../../../core/filters";
import {
  EMPTY_TOOL_SUMMARY,
  LOSSY_EVIDENCE_READING,
  LOSSY_READING,
  REPORTED_NOT_VERIFIED_READING,
  UNVERIFIED_READING,
  VERIFICATION_UNAVAILABLE_READING,
  VERIFIED_READING,
  filterSummaryText,
  firstSummary,
  messageReading,
  filterNameOf,
  postureEntries,
  stateReading,
  turnLabel,
  verdictReading,
} from "./readings";

function counts(overrides: Partial<GovernanceCounts> = {}): GovernanceCounts {
  return {
    allow: 0,
    deny: 0,
    unknownDecisions: 0,
    verified: 0,
    reported: 0,
    denied: 0,
    failed: 0,
    pendingResults: 0,
    unknownResults: 0,
    lost: 0,
    lossy: 0,
    ...overrides,
  };
}

describe("verdicts: a policy ALLOW is a permission, never an outcome", () => {
  it("draws ALLOW in the permission tone, not the success tone", () => {
    const allow = verdictReading("ALLOW");
    expect(allow.label).toBe("ALLOW");
    expect(allow.tone).toBe("allow");
    expect(allow.tone).not.toBe("ok");
    expect(allow.note).toMatch(/a permission, not an outcome/);
  });

  it("keeps DENY and UNKNOWN distinct from each other and from ALLOW", () => {
    expect(verdictReading("DENY").tone).toBe("deny");
    expect(verdictReading("UNKNOWN").label).toBe("UNKNOWN");
    expect(verdictReading("UNKNOWN").tone).toBe("unknown");
    expect(verdictReading("UNKNOWN").note).toMatch(/not an ALLOW or a DENY/);
  });

  it("reads a verdict outside the closed set as UNKNOWN, never as a permission", () => {
    const outside = verdictReading("MAYBE" as "ALLOW");
    expect(outside.label).toBe("UNKNOWN");
    expect(outside.tone).not.toBe("allow");
  });
});

describe("states: a success is reported unless it is verified", () => {
  it("shows an unverified success as reported, never succeeded and never the success tone", () => {
    const reading = stateReading("succeeded", false);
    expect(reading.label).toBe("reported");
    expect(reading.tone).toBe("reported");
    expect(reading.note).toMatch(/not independently verified/);
  });

  it("shows an unverified lossy success as reported, in the ledger's words, and never as succeeded", () => {
    const reading = stateReading("succeeded", false, true);
    expect(reading.label).toBe("reported, output lossy, not counted as confirmed");
    expect(reading.tone).toBe("reported");
    expect(reading.label).not.toMatch(/^succeeded/);
  });

  it("shows a verified lossy success on the unknown tone, never the success tone, in the ledger's words", () => {
    const reading = stateReading("succeeded", true, true);
    expect(reading.label).toBe("succeeded (output lossy, not counted as confirmed)");
    expect(reading.tone).toBe("unknown");
    expect(reading.note).toMatch(/output lossy, not counted as confirmed/);
  });

  it("shows a verified success as succeeded in the success tone", () => {
    const reading = stateReading("succeeded", true);
    expect(reading.label).toBe("succeeded");
    expect(reading.tone).toBe("ok");
  });

  it("names UNKNOWN, lost, pending, failed and denied, and never calls them a success", () => {
    const states = ["unknown", "lost", "pending", "failed", "denied"] as const;
    for (const state of states) {
      for (const evidenced of [false, true]) {
        const reading = stateReading(state, evidenced);
        expect(reading.label).toBe(state);
        expect(reading.tone).not.toBe("ok");
        expect(reading.label).not.toBe("succeeded");
      }
    }
  });

  it("keeps lost and unknown in different tones, so they are not one state", () => {
    expect(stateReading("lost", false).tone).toBe("lost");
    expect(stateReading("unknown", false).tone).toBe("unknown");
  });

  it("reads a state outside the closed set as unknown, not as a success", () => {
    const reading = stateReading("finished" as "succeeded", true);
    expect(reading.label).toBe("unknown");
    expect(reading.tone).toBe("unknown");
  });

  it("names lossy output and keeps it out of the success tone", () => {
    expect(LOSSY_READING.label).toBe("lossy");
    expect(LOSSY_READING.tone).toBe("unknown");
    expect(LOSSY_READING.note).toMatch(/not terminal evidence/);
  });

  it("marks an unverified item with the explicit unverified label", () => {
    expect(UNVERIFIED_READING.label).toBe("unverified");
    expect(UNVERIFIED_READING.tone).not.toBe("ok");
  });

  it("marks a verified lossy success as verified evidence with lossy output, on the neutral tone and never the success tone", () => {
    expect(LOSSY_EVIDENCE_READING.label).toBe("verified evidence, output lossy, not counted as confirmed");
    expect(LOSSY_EVIDENCE_READING.tone).toBe("idle");
    expect(LOSSY_EVIDENCE_READING.tone).not.toBe("ok");
    expect(LOSSY_EVIDENCE_READING.note).toMatch(/not confirmed/);
    // A complete verified success keeps the success marker: only the lossy case changes.
    expect(VERIFIED_READING.tone).toBe("ok");
  });
});

describe("posture badges", () => {
  it("separates policy ALLOW from reported execution", () => {
    const entries = postureEntries(counts({ allow: 1, reported: 1 }));
    const texts = entries.map((entry) => entry.text);
    expect(texts).toEqual(["allow verdict 1", "reported 1"]);
    expect(entries.find((entry) => entry.key === "allow")?.tone).toBe("allow");
  });

  // A count and the word for it must agree across the screen: a verdict count is a decision, a result count is an execution.
  it("words the decision counts as verdicts and the result counts as results, so no word names two different counts", () => {
    const entries = postureEntries(counts({ allow: 1, deny: 2, unknownDecisions: 1, denied: 3, unknownResults: 4, failed: 5 }));
    expect(entries.map((entry) => entry.text)).toEqual([
      "allow verdict 1",
      "deny verdict 2",
      "unknown verdict 1",
      "denied result 3",
      "failed result 5",
      "unresolved result 4",
    ]);
    for (const entry of entries) expect(entry.hint.length).toBeGreaterThan(0);
  });

  // The footer's "0 unknown" counts UNKNOWN verdicts, so on this screen the bare word unknown names a verdict only.
  it("keeps the bare word unknown for verdicts: an unknown result reads unresolved", () => {
    const texts = postureEntries(counts({ unknownDecisions: 1, unknownResults: 4 })).map((entry) => entry.text);
    expect(texts).toEqual(["unknown verdict 1", "unresolved result 4"]);
    expect(texts.filter((text) => /\bunknown\b/.test(text))).toEqual(["unknown verdict 1"]);
  });

  it("never draws a member of the Unknown or lossy filter in the success tone", () => {
    const members = postureEntries(counts({ unknownDecisions: 1, unknownResults: 1, lost: 1, lossy: 1 }));
    expect(members.map((entry) => entry.tone)).toEqual(["unknown", "unknown", "lost", "unknown"]);
  });

  it("names each filter for what its count counts, and keeps the legacy id for anything else", () => {
    expect(filterNameOf("denied").label).toBe("Denied (verdict or result)");
    expect(filterNameOf("unknown").label).toBe("Unknown or lossy");
    expect(filterNameOf("failed").label).toBe("Failed result");
    expect(filterNameOf("denied").hint).toMatch(/DENY verdict or a denied result/);
    expect(filterNameOf("unknown").hint).toBe("Records whose verdict or result is unknown, or whose output is lossy");
    expect(filterNameOf("failed").hint).toBe("records with a failed result");
    expect(filterNameOf("bogus")).toEqual({ label: "bogus", hint: "" });
  });

  it("uses the success tone for verified only when there is a verified count", () => {
    expect(postureEntries(counts({ reported: 2 })).some((entry) => entry.key === "verified")).toBe(false);
    const verified = postureEntries(counts({ verified: 1 })).find((entry) => entry.key === "verified");
    expect(verified?.tone).toBe("ok");
  });

  it("hides every zero count, and shows lost and lossy in their own tones", () => {
    expect(postureEntries(counts())).toEqual([]);
    const entries = postureEntries(counts({ lost: 1, lossy: 2 }));
    expect(entries.map((entry) => [entry.text, entry.tone])).toEqual([
      ["lost 1", "lost"],
      ["lossy 2", "unknown"],
    ]);
  });
});

describe("the filter summary", () => {
  it("counts the retained window when every element is shown", () => {
    expect(
      filterSummaryText({ filter: "all", label: "All", total: 7, matches: 7, shown: 7, evicted: 0 }),
    ).toBe("7 element(s) in the retained window");
  });

  it("separates matches from the elements shown, because a kept block shows its context", () => {
    expect(
      filterSummaryText({ filter: "denied", label: "Denied", total: 7, matches: 2, shown: 3, evicted: 0 }),
    ).toBe("2 matches · 3 of 7 element(s) shown for “Denied” (context included)");
  });

  it("says singular for one match and names the evicted count", () => {
    expect(
      filterSummaryText({ filter: "failed", label: "Failed", total: 7, matches: 1, shown: 2, evicted: 3 }),
    ).toMatch(/^1 match · 2 of 7 element\(s\) shown .* · 3 older element\(s\) evicted$/);
  });
});

describe("turns and summaries", () => {
  it("labels the untagged turn as a plain turn and tags others by id", () => {
    expect(turnLabel("__untagged")).toBe("turn");
    expect(turnLabel("t1")).toBe("turn t1");
    expect(turnLabel("turn-4")).toBe("turn-4");
  });

  it("takes the first bounded summary, in request, policy, result order", () => {
    expect(firstSummary([{ summary: "request" }, { summary: "policy" }, { summary: "result" }])).toBe("request");
    expect(firstSummary([null, { summary: undefined }, { summary: "result" }])).toBe("result");
  });

  it("says when no bounded summary was projected", () => {
    expect(firstSummary([null, null])).toBe(EMPTY_TOOL_SUMMARY);
  });
});

describe("the reading of a message (the gate's verdict, in words)", () => {
  it("reads a verified message verified, and every other message reported, not verified", () => {
    expect(messageReading("structural-audit-only", true)).toEqual(VERIFIED_READING);
    expect(messageReading("structural-audit-only", false)).toEqual(REPORTED_NOT_VERIFIED_READING);
    expect(messageReading("unverified-input", false)).toEqual(REPORTED_NOT_VERIFIED_READING);
  });

  it("reads an absent or unavailable verification scope as verification unavailable, never as verified", () => {
    expect(messageReading(undefined, false)).toEqual(VERIFICATION_UNAVAILABLE_READING);
    expect(messageReading("unavailable", true)).toEqual(VERIFICATION_UNAVAILABLE_READING);
  });

  it("never gives a message that is not verified the success tone", () => {
    expect(REPORTED_NOT_VERIFIED_READING.label).toBe("reported, not verified");
    expect(REPORTED_NOT_VERIFIED_READING.tone).toBe("reported");
    expect(messageReading("structural-audit-only", false).tone).not.toBe("ok");
    expect(messageReading(undefined, false).tone).not.toBe("ok");
  });
});
