import { describe, expect, it } from "vitest";
import { panelsDefault } from "../core/testing/builders";
import type { GovernancePanel, Panels, ProvenancePanel } from "../core/contract";
import {
  governanceRows,
  NO_PROJECTION,
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
  it("reports independently verified as yes only when the panel says available and the projection is verified", () => {
    expect(valueOf(provenanceRows({ available: true }, true)[0]?.[1])).toBe("yes");
    expect(valueOf(provenanceRows({ available: true }, false)[0]?.[1])).toBe("no");
    expect(valueOf(provenanceRows({ available: false }, true)[0]?.[1])).toBe("no");
  });

  it("reads an absent provenance panel as unavailable with its reason, never as no", () => {
    expect(provenanceRows(undefined, false)[0]?.[1]).toEqual({ kind: "unavailable", reason: "the snapshot carries no provenance panel" });
    expect(provenanceRows(null, true)[0]?.[1].kind).toBe("unavailable");
  });

  it("marks an absent provenance field unavailable, with the panel's note as the reason", () => {
    const rows = provenanceRows({ available: false, note: "not verified here" }, false);
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
    expect(warningLines([], true)).toEqual([NO_WARNINGS]);
  });

  it("reads the warnings as unavailable before a projection is loaded, never as none", () => {
    expect(warningLines([], false)).toEqual([`unavailable (${NO_PROJECTION})`]);
    expect(warningLines(undefined, false)).toEqual([`unavailable (${NO_PROJECTION})`]);
  });

  it("lists each projected warning", () => {
    expect(warningLines(["a", "b"], true)).toEqual(["a", "b"]);
  });
});

// Absent input reads as unavailable with its reason. Absence is never zero, none, no, complete or ok.
describe("absent input reads unavailable, never no, zero, none, complete or ok", () => {
  it("every governance, provenance and qualification row is unavailable when its panel is absent", () => {
    for (const panel of [undefined, null] as const) {
      const rows = [...governanceRows(panel), ...provenanceRows(panel, true), ...qualificationRows(panel)];
      expect(rows.length).toBeGreaterThan(10);
      for (const [label, reading] of rows) expect(reading.kind, label).toBe("unavailable");
    }
  });

  it("a present panel that cannot state a field reads that field as unavailable", () => {
    const provenance = provenanceRows({ available: "yes" } as unknown as ProvenancePanel, true);
    expect(provenance[0]?.[1].kind).toBe("unavailable");
    const governance = governanceRows({ allow_count: "3", deny_count: 0, unknown_count: 0 } as unknown as GovernancePanel);
    expect(governance[0]?.[1].kind).toBe("unavailable");
    expect(governance[1]?.[1]).toEqual({ kind: "value", text: "0" });
  });

  it("an absent publication, recovery or session state reads unavailable, and an empty state is not a label", () => {
    expect(publicationReading(undefined, true).label).toBe("unavailable");
    expect(recoveryReading(null, true).label).toBe("unavailable");
    expect(sessionReading(null, true).label).toBe("unavailable");
    expect(stateReading("", true, {}).label).toBe("unavailable");
  });
});
