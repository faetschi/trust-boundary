import { describe, expect, it } from "vitest";
import { frozen, orphanedGroups } from "./frozen";

// A group the oracle holds must fail when it is closed without its comparisons. Without this, an emptied
// corpus (STREAM = "", ADVERSARIAL = []) would pass silently.
describe("frozen oracle guards", () => {
  it("a recorded group closed with zero comparisons fails", () => {
    const group = frozen("frozen legacy oracle: markdown parser > (b) the 18 hostile payloads agree");
    expect(() => group.done()).toThrow(/0 comparisons made, the frozen legacy oracle holds 18/);
  });

  it("a group with no recorded oracle fails", () => {
    expect(() => frozen("no such group").done()).toThrow(/no frozen legacy oracle/);
  });

  it("a recorded group whose test was deleted or renamed is reported, and a present one is not", () => {
    const name = "frozen legacy oracle: markdown parser > (b) the 18 hostile payloads agree";
    const file = "C:/repo/frontend/src/markdown/frozen-oracle.test.ts";
    expect(orphanedGroups(file, [name])).not.toContain(name);
    expect(orphanedGroups(file, [])).toContain(name);
    expect(orphanedGroups(file, ["renamed"])).toContain(name);
    // Groups of the other file are not this file's to carry.
    expect(orphanedGroups(file, [])).not.toContain("frozen legacy oracle: anchor arithmetic > the tail, scroll and announcement functions agree on seeded metrics");
  });
});
