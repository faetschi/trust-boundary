import { describe, expect, it } from "vitest";
import type { Snapshot } from "./contract";
import { snapshotOf, TIMESTAMP } from "./testing/builders";
import { ContractError, parseSnapshot } from "./validate";

// A count the served snapshot can carry: the turn count, and each governance count. The gateway omits a count it does not
// state and writes a stated one, zero included, so each is checked as the wire gives it.
interface Field {
  readonly object: "session" | "governance";
  readonly key: string;
}

const FIELDS: readonly Field[] = [
  { object: "session", key: "turn_count" },
  { object: "governance", key: "allow_count" },
  { object: "governance", key: "deny_count" },
  { object: "governance", key: "unknown_count" },
];

// Each row: the wire value, and whether the gateway accepts it. `absent` omits the key.
const VALUES: ReadonlyArray<readonly [string, unknown, boolean]> = [
  ["absent", undefined, true],
  ["a stated zero", 0, true],
  ["a stated count", 3, true],
  ["a negative count", -1, false],
  ["a fraction", 1.5, false],
  ["null", null, false],
  ["a numeric string", "3", false],
];

const options = { nowMs: Date.parse(TIMESTAMP) };

// The served snapshot as JSON, with the wire's own keys, so a test can set or omit one.
type Wire = {
  session: Record<string, unknown>;
  panels: { governance: Record<string, unknown> };
};

// The served snapshot as JSON, with one count set to `value`, or omitted when `value` is undefined.
function served(field: Field, value: unknown): Wire {
  const snapshot = JSON.parse(JSON.stringify(snapshotOf())) as Wire;
  const block = field.object === "session" ? snapshot.session : snapshot.panels.governance;
  if (value === undefined) delete block[field.key];
  else block[field.key] = value;
  return snapshot;
}

function countOf(snapshot: Snapshot, field: Field): unknown {
  const block = field.object === "session" ? snapshot.session : snapshot.panels.governance;
  return (block as unknown as Record<string, unknown>)[field.key];
}

describe("a served count is absent or stated: absent is accepted and reads absent, a stated zero reads zero", () => {
  for (const field of FIELDS) {
    describe(field.key, () => {
      it.each(VALUES)("accepts or refuses %s", (_name, value, accepted) => {
        const snapshot = served(field, value);
        if (!accepted) {
          expect(() => parseSnapshot(snapshot, options)).toThrow(ContractError);
          return;
        }
        expect(countOf(parseSnapshot(snapshot, options), field)).toBe(value);
      });
    });
  }

  it("accepts a served snapshot that states no count at all, as a receipt projection does", () => {
    const snapshot = JSON.parse(JSON.stringify(snapshotOf())) as Wire;
    delete snapshot.session.turn_count;
    for (const key of ["allow_count", "deny_count", "unknown_count"]) delete snapshot.panels.governance[key];
    const parsed = parseSnapshot(snapshot, options);
    expect(parsed.session.turn_count).toBeUndefined();
    expect(parsed.panels.governance.allow_count).toBeUndefined();
  });
});
