// Ported from the performance group of internal/webchat/jstest/markdown.test.mjs.
// Cost is judged by scanWork, the count of positions the look-ahead examined, so the
// bound cannot flake on a slow machine. The clock is a generous backstop only. The
// constants are the legacy ones.

import { describe, expect, it } from "vitest";
import { createIncrementalParser } from "./incremental";
import { MAX_MARKDOWN_CHARS, parseMarkdown, scanWork } from "./parse";
import { ADVERSARIAL } from "./test-support/corpus";

const MAX_WORK_PER_CHARACTER = 64;
const TIME_BACKSTOP_MS = 1500;

describe("performance: linear-time look-ahead", () => {
  it("pathological delimiter runs stay within the work and time bounds", () => {
    const text = `${"[".repeat(30000)}${"*a ".repeat(10000)}${"`".repeat(5000)}${"_b".repeat(5000)}`;
    scanWork.steps = 0;
    const started = performance.now();
    parseMarkdown(text);
    const elapsed = performance.now() - started;
    const rendered = Math.min(text.length, MAX_MARKDOWN_CHARS);
    expect(scanWork.steps, `examined ${scanWork.steps} positions for ${rendered} characters`).toBeLessThanOrEqual(
      rendered * MAX_WORK_PER_CHARACTER,
    );
    expect(elapsed, `took ${elapsed.toFixed(0)} ms`).toBeLessThan(TIME_BACKSTOP_MS);
  });

  it("512 adversarial 4096-character messages stay within the work and time bounds", () => {
    scanWork.steps = 0;
    const started = performance.now();
    for (let index = 0; index < 512; index++) parseMarkdown(ADVERSARIAL[index % ADVERSARIAL.length] ?? "");
    const elapsed = performance.now() - started;
    const characters = 512 * 4096;
    console.info(`[perf] 512 adversarial messages: ${scanWork.steps} positions, ${(scanWork.steps / characters).toFixed(2)} per character`);
    expect(scanWork.steps, `examined ${scanWork.steps} positions for ${characters} characters`).toBeLessThanOrEqual(
      characters * MAX_WORK_PER_CHARACTER,
    );
    expect(elapsed, `took ${elapsed.toFixed(0)} ms`).toBeLessThan(TIME_BACKSTOP_MS);
  });

  it("a 4096-character adversarial message streamed one character at a time stays within the time bound", () => {
    const text = "*".repeat(4096);
    const parser = createIncrementalParser();
    const started = performance.now();
    let last = parser.update("");
    for (let end = 1; end <= text.length; end++) last = parser.update(text.slice(0, end));
    const elapsed = performance.now() - started;
    console.info(`[perf] 4096-character stream: ${elapsed.toFixed(0)} ms`);
    expect(last.blocks, "the final stream equals a fresh parse of the same text").toStrictEqual(parseMarkdown(text).blocks);
    expect(elapsed, `streaming took ${elapsed.toFixed(0)} ms`).toBeLessThan(TIME_BACKSTOP_MS);
  });

  it("one parse of each adversarial message grows linearly with its length", () => {
    // A message and its double, same pattern. The work of the double is at most
    // 2.5 times the work of the single, so the look-ahead is not quadratic.
    for (const unit of ["*", "_", "[", "[a](", "*_[`", "`a"]) {
      const single = unit.repeat(2048);
      const double = unit.repeat(4096);
      scanWork.steps = 0;
      parseMarkdown(single);
      const singleWork = scanWork.steps;
      scanWork.steps = 0;
      parseMarkdown(double);
      const doubleWork = scanWork.steps;
      expect(doubleWork, `unit ${JSON.stringify(unit)}: ${singleWork} then ${doubleWork}`).toBeLessThanOrEqual(singleWork * 2.5);
    }
  });
});
