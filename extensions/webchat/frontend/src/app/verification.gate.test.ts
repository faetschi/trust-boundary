// The gate's boundary inside the app (a grep-style gate, like core check-purity). The item-only predicate
// (core isItemEvidenceOnly, the legacy isVerified) is blind to the projection, and judgedItem was the app's
// compensation for that. No file under src/app may name either. A view reaches verification only through
// verification.ts, which re-exports the core gate, so the projection is always part of the decision.
import { describe, expect, test } from "vitest";
import * as appGate from "./verification";
import * as coreGate from "../core/verification";

// Every app file, read as text at build time. The self-test below proves the detector fires.
const files: Record<string, unknown> = import.meta.glob("./**/*.{ts,tsx}", {
  query: "?raw",
  import: "default",
  eager: true,
});

const FORBIDDEN = /\b(?:isItemEvidenceOnly|isVerified|judgedItem)\b/;

// Blank comments but keep their newlines, so a reported line number matches the file.
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, (match) => match.replace(/[^\n]/g, " "));
}

function forbiddenLines(source: string): string[] {
  return stripComments(source)
    .split("\n")
    .map((line, index) => ({ line, number: index + 1 }))
    .filter(({ line }) => FORBIDDEN.test(line))
    .map(({ line, number }) => `${number}: ${line.trim()}`);
}

describe("app verification boundary (gate)", () => {
  test("no file under src/app names the item-only predicate or the retired judgedItem", () => {
    const failures: string[] = [];
    let scanned = 0;
    for (const [path, content] of Object.entries(files)) {
      if (typeof content !== "string") throw new Error(`verification gate: ${path} was not read as text`);
      // This file holds the forbidden names on purpose, in its patterns and self-test.
      if (path === "./verification.gate.test.ts") continue;
      scanned += 1;
      for (const hit of forbiddenLines(content)) failures.push(`${path}:${hit}`);
    }
    expect(scanned).toBeGreaterThan(20);
    expect(failures).toEqual([]);
  });

  test("the app gate is the core gate: verification.ts re-exports it rather than keeping a copy", () => {
    expect(appGate.isVerifiedIn).toBe(coreGate.isVerifiedIn);
  });

  test("self-test: the detector fires on each name and stays quiet on the verified predicate and on comments", () => {
    expect(forbiddenLines("import { isVerified } from '../core/format';")).toHaveLength(1);
    expect(forbiddenLines("import { isItemEvidenceOnly } from '../core/verification';")).toHaveLength(1);
    expect(forbiddenLines("const x = judgedItem(p, item);")).toHaveLength(1);
    expect(forbiddenLines("import { isVerifiedIn, isVerifiedProjection } from '../core/verification';")).toEqual([]);
    expect(forbiddenLines("// judgedItem was retired; isVerified is item-only")).toEqual([]);
  });
});
