// The honesty rules of the Chat view, on the pure readings (readings.ts). The DOM-level checks are
// in ../ChatView.test.tsx; these pin the words and tones that every view shows.

import { describe, expect, it } from "vitest";
import type { GovernanceCounts } from "../../../core/filters";
import {
  EMPTY_TOOL_SUMMARY,
  LOSSY_READING,
  UNVERIFIED_READING,
  filterSummaryText,
  firstSummary,
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

  it("shows a verified lossy success on the unknown tone, never the success tone", () => {
    const reading = stateReading("succeeded", true, true);
    expect(reading.label).toBe("succeeded (output lossy, not confirmed)");
    expect(reading.tone).toBe("unknown");
    expect(reading.note).toMatch(/not counted as confirmed/);
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
});

describe("posture badges", () => {
  it("separates policy ALLOW from reported execution", () => {
    const entries = postureEntries(counts({ allow: 1, reported: 1 }));
    const texts = entries.map((entry) => entry.text);
    expect(texts).toEqual(["ALLOW 1", "reported 1"]);
    expect(entries.find((entry) => entry.key === "allow")?.tone).toBe("allow");
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
