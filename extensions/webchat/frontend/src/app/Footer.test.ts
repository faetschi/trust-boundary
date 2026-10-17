import { describe, expect, it } from "vitest";
import { decisionItem, messageItem, panelsDefault, sessionDefault } from "../core/testing/builders";
import type { GovernancePanel, Panels, SessionHeader } from "../core/contract";
import { statsLine, statsParts } from "./Footer";

type StatsInput = Parameters<typeof statsLine>[0];

// The demo fixture's shape: one turn, four retained records, and a governance panel of one ALLOW and one DENY.
function fixture(overrides: Partial<StatsInput> = {}): StatsInput {
  const governance: Panels = {
    ...panelsDefault(),
    governance: { allow_count: 1, deny_count: 1, unknown_count: 0 },
  };
  return {
    session: sessionDefault(),
    panels: governance,
    items: [
      messageItem({ id: "m1", sequence: 1, role: "user", text: "list files" }),
      decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
      decisionItem({ id: "d2", sequence: 3, tool: "bash", tool_call_id: "c2", verdict: "DENY" }),
      messageItem({ id: "m2", sequence: 4, role: "assistant", text: "done" }),
    ],
    evicted: 0,
    loaded: true,
    ...overrides,
  };
}

function sessionWith(overrides: Partial<SessionHeader>): SessionHeader {
  return { ...sessionDefault(), ...overrides };
}

describe("the one-line statistics", () => {
  it("reads the demo fixture as one line, with tokens and timing unavailable", () => {
    expect(statsLine(fixture())).toBe(
      "1 turn · 4 records retained · policy verdicts 1 allow / 1 deny / 0 unknown · tokens unavailable · timing unavailable",
    );
  });

  it("uses the singular for one turn and one record", () => {
    const line = statsLine(fixture({ items: [messageItem({ id: "m1", sequence: 1, role: "user", text: "hi" })] }));
    expect(line.startsWith("1 turn · 1 record retained")).toBe(true);
  });

  it("uses the plural for other counts", () => {
    expect(statsLine(fixture({ session: sessionWith({ turn_count: 3 }) })).startsWith("3 turns · ")).toBe(true);
  });

  // A stated zero is a count: a session with no turns reads 0 turns, with or without loaded records.
  it("reads a stated zero turn count as 0 turns, with or without loaded records", () => {
    expect(statsLine(fixture({ session: sessionWith({ turn_count: 0 }) })).startsWith("0 turns · ")).toBe(true);
    expect(statsLine(fixture({ session: sessionWith({ turn_count: 0 }), items: [] })).startsWith("0 turns · ")).toBe(true);
  });

  it("reads stated zero policy counts as zero verdicts, with or without a policy digest", () => {
    const zero = { allow_count: 0, deny_count: 0, unknown_count: 0 };
    // No decision among the records, so that the zero counts are not contradicted by a verdict.
    const records = [messageItem({ id: "m1", sequence: 1, role: "user", text: "list files" })];
    const bare: Panels = { ...panelsDefault(), governance: zero };
    expect(statsLine(fixture({ panels: bare, items: records }))).toContain(" · policy verdicts 0 allow / 0 deny / 0 unknown · ");
    const observed: Panels = { ...panelsDefault(), governance: { ...zero, policy_digest: "sha256:ab" } };
    expect(statsLine(fixture({ panels: observed, items: records }))).toContain(" · policy verdicts 0 allow / 0 deny / 0 unknown · ");
  });

  it("reads zero counts as unavailable when a loaded decision carries a verdict, since the counts contradict it", () => {
    const zero = { allow_count: 0, deny_count: 0, unknown_count: 0, policy_digest: "sha256:ab" };
    const panels: Panels = { ...panelsDefault(), governance: zero };
    expect(statsLine(fixture({ panels }))).toContain(" · policy verdicts unavailable · ");
  });

  it("keeps the demo fixture's counts, including a real zero unknown, unchanged", () => {
    expect(statsLine(fixture())).toContain(" · policy verdicts 1 allow / 1 deny / 0 unknown · ");
  });

  it("reads an absent turn count as unavailable, never as zero", () => {
    const line = statsLine(fixture({ session: sessionWith({ turn_count: undefined as unknown as number }) }));
    expect(line.startsWith("turns unavailable · ")).toBe(true);
    expect(line).not.toMatch(/^0 turns/);
  });

  it("reads an absent policy count as unavailable, never as zero", () => {
    const panels: Panels = {
      ...panelsDefault(),
      governance: { allow_count: 1, deny_count: 1, unknown_count: undefined as unknown as number },
    };
    expect(statsLine(fixture({ panels }))).toContain("policy verdicts 1 allow / 1 deny / unavailable unknown");
  });

  it("reads the whole policy as unavailable when the snapshot carries no governance panel", () => {
    expect(statsLine(fixture({ panels: null }))).toContain(" · policy verdicts unavailable · ");
  });

  it("reads every count as unavailable before the first snapshot, never as zero", () => {
    expect(statsLine({ session: null, panels: null, items: [], evicted: 0, loaded: false })).toBe(
      "turns unavailable · records retained unavailable · policy verdicts unavailable · tokens unavailable · timing unavailable",
    );
  });

  it("reads an empty loaded window as zero records, because the projection did load and retained none", () => {
    expect(statsLine({ session: null, panels: panelsDefault(), items: [], evicted: 0, loaded: true })).toContain(
      " · 0 records retained · ",
    );
  });

  it("notes the earlier records no longer retained, inside the records segment", () => {
    expect(statsLine(fixture({ evicted: 3 }))).toContain("4 records retained (3 earlier evicted) · policy verdicts ");
  });

  it("never gives tokens or timing a number", () => {
    expect(statsLine(fixture())).toMatch(/ · tokens unavailable · timing unavailable$/);
  });
});

// The footer draws each reading as its own part, so an unavailable one can be drawn in the warning ink. The parts joined are the line.
describe("the readings the footer draws apart", () => {
  it("keeps each reading as one part, and the parts joined are the statistics line", () => {
    expect(statsParts(fixture())).toEqual([
      "1 turn",
      "4 records retained",
      "policy verdicts 1 allow / 1 deny / 0 unknown",
      "tokens unavailable",
      "timing unavailable",
    ]);
    expect(statsParts(fixture()).join(" · ")).toBe(statsLine(fixture()));
  });

  it("gives every unavailable reading its own part before the first snapshot", () => {
    const parts = statsParts({ session: null, panels: null, items: [], evicted: 0, loaded: false });
    expect(parts.filter((part) => part.includes("unavailable"))).toEqual([
      "turns unavailable",
      "records retained unavailable",
      "policy verdicts unavailable",
      "tokens unavailable",
      "timing unavailable",
    ]);
  });
});

// Absent is unavailable, a stated zero is zero, and a stated count is its number, for the turn count and each policy count.
describe("absent versus zero, in the footer", () => {
  const TURNS: ReadonlyArray<readonly [number | undefined, string]> = [
    [undefined, "turns unavailable"],
    [0, "0 turns"],
    [1, "1 turn"],
    [3, "3 turns"],
  ];
  it.each(TURNS)("reads turn_count %s as '%s'", (turns, text) => {
    expect(statsLine(fixture({ session: sessionWith({ turn_count: turns }) })).startsWith(`${text} · `)).toBe(true);
  });

  const POLICY: ReadonlyArray<readonly [number | undefined, number | undefined, number | undefined, string]> = [
    [undefined, undefined, undefined, "policy verdicts unavailable"],
    [0, 0, 0, "policy verdicts 0 allow / 0 deny / 0 unknown"],
    [2, undefined, 0, "policy verdicts 2 allow / unavailable deny / 0 unknown"],
    [0, 2, 0, "policy verdicts 0 allow / 2 deny / 0 unknown"],
    [0, 0, 2, "policy verdicts 0 allow / 0 deny / 2 unknown"],
  ];
  // The records carry no verdict, so a stated zero is not contradicted. The digest changes no row.
  it.each(POLICY)("reads allow %s, deny %s, unknown %s as '%s', with and without a policy digest", (allow, deny, unknown, text) => {
    const records = [messageItem({ id: "m1", sequence: 1, role: "user", text: "list files" })];
    for (const digest of [undefined, "sha256:ab"]) {
      const panels: Panels = { ...panelsDefault(), governance: governance(allow, deny, unknown, digest) };
      expect(statsLine(fixture({ panels, items: records }))).toContain(` · ${text} · `);
    }
  });
});

// A governance panel with the given counts. An undefined count is omitted, as the gateway omits a count it does not state.
function governance(allow?: number, deny?: number, unknown?: number, digest?: string): GovernancePanel {
  return {
    ...(allow === undefined ? {} : { allow_count: allow }),
    ...(deny === undefined ? {} : { deny_count: deny }),
    ...(unknown === undefined ? {} : { unknown_count: unknown }),
    ...(digest === undefined ? {} : { policy_digest: digest }),
  };
}
