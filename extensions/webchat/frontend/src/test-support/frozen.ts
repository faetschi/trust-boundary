// TEST-ONLY. Replays the legacy vanilla client as a frozen oracle. The legacy modules are gone; before
// they were deleted, the differential suites ran every seeded corpus through them and recorded a
// rolling SHA-256 over the legacy output of each comparison, in order (legacy-oracle.json). A suite
// now feeds the same corpus to the React-era port and calls check() with the port's output. A chunk
// of CHUNK comparisons passes when the same digest comes out, so any change to a port's observable
// output on these corpora fails here. Provenance and the mapping table: docs/phase2-parity.md.
import { createHash } from "node:crypto";
import { appendFileSync } from "node:fs";
import { afterEach, expect } from "vitest";
import oracle from "./legacy-oracle.json" with { type: "json" };

const CHUNK = 250;

type Oracle = Record<string, { n: number; chunks: string[] }>;

// Canonical text of a value: stable for undefined, NaN, infinities, -0, Map, Set and key order.
export function canon(value: unknown): string {
  if (value === undefined) return "u";
  if (value === null) return "n";
  if (typeof value === "number") return Object.is(value, -0) ? "-0" : `d${String(value)}`;
  if (typeof value === "string") return JSON.stringify(value);
  if (typeof value === "boolean" || typeof value === "bigint") return `${typeof value}${String(value)}`;
  if (typeof value === "function") return "fn";
  if (Array.isArray(value)) return `[${value.map(canon).join(",")}]`;
  if (value instanceof Map) return `M[${[...value.entries()].map(([k, v]) => `${canon(k)}=>${canon(v)}`).join(",")}]`;
  if (value instanceof Set) return `S[${[...value].map(canon).join(",")}]`;
  if (typeof value === "object") {
    const entries = Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${canon(v)}`).join(",")}}`;
  }
  return String(value);
}

export interface Frozen {
  check(label: string, value: unknown): void;
  done(): void;
}

// With LEGACY_ORACLE_RECORD=<file> set, check() records instead of comparing. Only the
// pre-deletion recorder used that mode.
export function frozen(group: string): Frozen {
  const record = process.env["LEGACY_ORACLE_RECORD"];
  const expected = (oracle as Oracle)[group];
  const chunks: string[] = [];
  let chain = "";
  let count = 0;
  let labels: string[] = [];
  const seal = (): void => {
    const digest = createHash("sha256").update(chain).digest("hex").slice(0, 16);
    const index = chunks.length;
    chunks.push(digest);
    if (!record) {
      if (!expected) throw new Error(`no frozen legacy oracle for group ${group}`);
      if (expected.chunks[index] !== digest) {
        throw new Error(
          `${group}: comparisons ${index * CHUNK}..${index * CHUNK + labels.length - 1} differ from the frozen legacy oracle (first labels: ${labels.slice(0, 5).join(" | ")})`,
        );
      }
    }
    chain = digest;
    labels = [];
  };
  return {
    check(label, value) {
      chain += canon(value) + "|";
      labels.push(label);
      count++;
      if (labels.length === CHUNK) seal();
    },
    done() {
      if (labels.length > 0) seal();
      if (record) {
        appendFileSync(record, `${JSON.stringify({ group, n: count, chunks })}\n`);
        return;
      }
      if (!expected) throw new Error(`no frozen legacy oracle for group ${group}`);
      if (expected.n !== count || expected.chunks.length !== chunks.length) {
        throw new Error(`${group}: ${count} comparisons made, the frozen legacy oracle holds ${expected.n}`);
      }
    },
  };
}

// One frozen group per test, named by the test. Call once at module level; it registers the
// afterEach that closes the group. Returns the check function for the suite.
export function frozenSuite(): (label: string, value: unknown) => void {
  let current: { name: string; group: Frozen } | null = null;
  afterEach(() => {
    current?.group.done();
    current = null;
  });
  return (label, value) => {
    const name = expect.getState().currentTestName ?? "";
    if (current?.name !== name) current = { name, group: frozen(name) };
    current.group.check(label, value);
  };
}
