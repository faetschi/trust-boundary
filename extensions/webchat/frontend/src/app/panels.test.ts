import { describe, expect, it } from "vitest";
import { panelsDefault } from "../core/testing/builders";
import type { Panels } from "../core/contract";
import {
  governanceRows,
  NO_WARNINGS,
  provenanceRows,
  publicationReading,
  qualificationRows,
  recoveryReading,
  sessionReading,
  stateReading,
  UNREPORTED_TRUST,
  warningLines,
  type Reading,
} from "./panels";

// The state chip for a publication panel, under the given projection verification.
function publication(state: Panels["publication"]["state"], verified: boolean) {
  return publicationReading({ state }, verified);
}

function valueOf(reading: Reading | undefined): string {
  if (!reading) throw new Error("missing row");
  if (reading.kind !== "value") throw new Error(`expected a value, got unavailable: ${reading.reason}`);
  return reading.text;
}

describe("publication: a success is reported unless the projection is verified", () => {
  it("shows an unverified publication as reported, never as published", () => {
    const reading = publication("published", false);
    expect(reading.label).toBe("reported");
    expect(reading.label).not.toBe("published");
    expect(reading.tone).toBe("reported");
    expect(reading.trust).toBe(UNREPORTED_TRUST);
  });

  it("shows a verified publication as published, with the gateway as the verifier", () => {
    const reading = publication("published", true);
    expect(reading).toEqual({ label: "published", tone: "ok", trust: "verified by the gateway" });
  });

  it("keeps a non-success state in words, with its own tone, and never calls it a success", () => {
    expect(publication("failed", false)).toMatchObject({ label: "failed", tone: "deny" });
    expect(publication("pending", true)).toMatchObject({ label: "pending", tone: "pending" });
    expect(publication("not-attempted", true)).toMatchObject({ label: "not attempted", tone: "idle" });
  });

  it("reads an absent publication panel as unavailable with a reason, never as a state", () => {
    expect(publicationReading(undefined, true)).toEqual({
      label: "unavailable",
      tone: "idle",
      trust: "the snapshot carries no publication panel",
    });
  });
});

describe("state readings: UNKNOWN stays UNKNOWN", () => {
  it("reads a state the client does not know as UNKNOWN, not as a state it resembles", () => {
    expect(stateReading("some-future-state", true, {})).toMatchObject({ label: "some future state", tone: "unknown" });
  });

  it("does not take an inherited property name as a tone", () => {
    expect(stateReading("constructor", true, {})).toMatchObject({ tone: "unknown" });
  });

  it("reads an unverified recovery 'ready' as reported, the same rule as publication", () => {
    expect(recoveryReading({ state: "ready" }, false)).toMatchObject({ label: "reported", tone: "reported" });
    expect(recoveryReading(undefined, false)).toMatchObject({ label: "unavailable" });
  });

  it("reads a session 'completed' as reported when the projection is not verified", () => {
    expect(sessionReading({ status: "completed", turn_count: 1 }, false)).toMatchObject({
      label: "reported",
      tone: "reported",
    });
    expect(sessionReading(null, false)).toMatchObject({ label: "unavailable", tone: "idle" });
  });
});

describe("governance counts: absent is unavailable, never zero", () => {
  it("shows the counts when the panel is present", () => {
    const rows = governanceRows({ allow_count: 2, deny_count: 1, unknown_count: 0 });
    expect(valueOf(rows[0]?.[1])).toBe("2");
    expect(valueOf(rows[1]?.[1])).toBe("1");
    expect(valueOf(rows[2]?.[1])).toBe("0");
  });

  it("shows every count as unavailable with a reason when the panel is absent", () => {
    const rows = governanceRows(undefined);
    for (const [, reading] of rows.slice(0, 3)) {
      expect(reading).toEqual({ kind: "unavailable", reason: "the snapshot carries no governance panel" });
    }
  });

  it("refuses a count that is not a non-negative integer", () => {
    const rows = governanceRows({ allow_count: -1, deny_count: 1.5, unknown_count: Number.NaN });
    expect(rows.slice(0, 3).every(([, reading]) => reading.kind === "unavailable")).toBe(true);
  });
});

describe("provenance and qualification", () => {
  it("reports independently verified as yes only when the panel says available", () => {
    expect(valueOf(provenanceRows({ available: true })[0]?.[1])).toBe("yes");
    expect(valueOf(provenanceRows({ available: false })[0]?.[1])).toBe("no");
    expect(valueOf(provenanceRows(undefined)[0]?.[1])).toBe("no");
  });

  it("marks an absent provenance field unavailable, with the panel's note as the reason", () => {
    const rows = provenanceRows({ available: false, note: "not verified here" });
    expect(rows[1]?.[1]).toEqual({ kind: "unavailable", reason: "not verified here" });
  });

  it("states the fixed qualification values, and says so when the panel is absent", () => {
    const rows = qualificationRows(panelsDefault().qualification);
    expect(rows.map(([label, reading]) => [label, valueOf(reading)])).toEqual([
      ["G1", "false"],
      ["H1", "unexecuted"],
      ["Containment", "not-established"],
      ["Claim-bearing", "false"],
    ]);
    expect(qualificationRows(undefined).every(([, reading]) => reading.kind === "unavailable")).toBe(true);
  });
});

describe("warnings", () => {
  it("says so when no warnings were projected", () => {
    expect(warningLines([])).toEqual([NO_WARNINGS]);
  });

  it("lists each projected warning", () => {
    expect(warningLines(["a", "b"])).toEqual(["a", "b"]);
  });
});
