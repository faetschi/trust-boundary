// check-purity — a grep-style gate for the framework-free core (src/core).
//
// It fails when:
//   - any file in src/core imports react or react-dom (type-only imports included);
//   - any file in src/core references document, window, navigator or storage as a value
//     (a type position such as `Document` or `Window` is not a value reference);
//   - a module (not a test, not testing/ support) calls a browser or timer global directly
//     (setTimeout, fetch, EventSource, AbortController, ...). Those come in through the
//     injected interfaces in api.ts, stream.ts and backoff.ts.
//
// Comments and string contents are blanked before the value checks, so a word in a comment or
// a message does not count. The self-test at the bottom proves the detector fires on each rule.
// Run it with `npm run test` (no separate script, so the project's package.json is unchanged).

import { describe, expect, test } from "vitest";

// Every file under src/core, read as text at build time. A value that is not a string would be
// a bug in the glob, so it is refused rather than skipped.
const files: Record<string, unknown> = import.meta.glob("./**/*.ts", {
  query: "?raw",
  import: "default",
  eager: true,
});

export type FileKind = "module" | "test" | "testing";

export interface Violation {
  readonly rule: "react-import" | "browser-global" | "timer-or-network-global";
  readonly line: number;
  readonly text: string;
}

const REACT_IMPORT = /(?:\bfrom\s+|\bimport\s*\(\s*|\brequire\s*\(\s*)["'](?:react|react-dom)(?:\/[^"']*)?["']/;
const BROWSER_VALUE = /(?<![.\w$])(?:document|window|navigator|localStorage|sessionStorage|location)(?![\w$])/;
const TIMER_OR_NETWORK = /(?<![.\w$])(?:setTimeout|clearTimeout|setInterval|clearInterval|requestAnimationFrame|fetch|EventSource|AbortController|WebSocket|XMLHttpRequest)(?![\w$])/;

// Blank comments but keep their newlines, so reported line numbers match the file.
export function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, (match) => match.replace(/[^\n]/g, " "));
}

// Blank the contents of single-line string and template literals, keeping the quotes.
export function stripStrings(source: string): string {
  return source.replace(/"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|`(?:\\.|[^`\\])*`/g, (match) => {
    const quote = match.charAt(0);
    return quote + "_".repeat(Math.max(0, match.length - 2)) + quote;
  });
}

export function violationsIn(source: string, kind: FileKind): Violation[] {
  const found: Violation[] = [];
  const withStrings = stripComments(source).split("\n");
  const valuesOnly = stripStrings(stripComments(source)).split("\n");
  valuesOnly.forEach((line, index) => {
    const original = withStrings[index] ?? "";
    if (REACT_IMPORT.test(original)) found.push({ rule: "react-import", line: index + 1, text: original.trim() });
    if (BROWSER_VALUE.test(line)) found.push({ rule: "browser-global", line: index + 1, text: line.trim() });
    if (kind === "module" && TIMER_OR_NETWORK.test(line)) {
      found.push({ rule: "timer-or-network-global", line: index + 1, text: line.trim() });
    }
  });
  return found;
}

describe("src/core purity (check-purity)", () => {
  test("no file in src/core imports react, and no value reference reaches a browser global", () => {
    const failures: string[] = [];
    let scanned = 0;
    for (const [path, content] of Object.entries(files)) {
      if (typeof content !== "string") throw new Error(`check-purity: ${path} was not read as text`);
      // This file holds the forbidden words on purpose, in strings and in the self-test below.
      if (path === "./check-purity.test.ts") continue;
      const kind: FileKind = path.includes("/testing/") ? "testing" : /\.test\.ts$/.test(path) ? "test" : "module";
      scanned += 1;
      for (const violation of violationsIn(content, kind)) {
        failures.push(`${path}:${violation.line}: ${violation.rule}: ${violation.text}`);
      }
    }
    expect(scanned).toBeGreaterThan(20);
    expect(failures).toEqual([]);
  });

  test("self-test: the detector fires on each rule and stays quiet on type positions and text", () => {
    expect(violationsIn('import React from "react";', "module").map((v) => v.rule)).toEqual(["react-import"]);
    expect(violationsIn('import type { X } from "react-dom";', "test").map((v) => v.rule)).toEqual(["react-import"]);
    expect(violationsIn("const n = window.innerWidth;", "test").map((v) => v.rule)).toEqual(["browser-global"]);
    expect(violationsIn("const d = document;", "module").map((v) => v.rule)).toEqual(["browser-global"]);
    expect(violationsIn("setTimeout(() => {}, 1);", "module").map((v) => v.rule)).toEqual(["timer-or-network-global"]);
    expect(violationsIn("await fetch('/x');", "module").map((v) => v.rule)).toEqual(["timer-or-network-global"]);
    expect(violationsIn("const c = new AbortController();", "module").map((v) => v.rule)).toEqual([
      "timer-or-network-global",
    ]);

    // Type positions and words inside comments or strings are not value references.
    expect(violationsIn("function f(d: Document, w: Window): void {}", "module")).toEqual([]);
    expect(violationsIn("// document.write is a sink, see check-sinks\nconst a = 1;", "module")).toEqual([]);
    expect(violationsIn('const msg = "window and document are not used";', "module")).toEqual([]);
    expect(violationsIn("const x = obj.window + obj.document;", "module")).toEqual([]);
    expect(violationsIn("const t = createAbortController(); setTimer(cb, 1);", "module")).toEqual([]);

    // Node globals are allowed in a test file, not in a module.
    expect(violationsIn("const c = new AbortController();", "test")).toEqual([]);
    expect(violationsIn("setTimeout(() => {}, 1);", "testing")).toEqual([]);
  });
});
