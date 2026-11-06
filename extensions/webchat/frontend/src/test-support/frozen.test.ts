import { describe, expect, it } from "vitest";
import { frozen } from "./frozen";

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
});
