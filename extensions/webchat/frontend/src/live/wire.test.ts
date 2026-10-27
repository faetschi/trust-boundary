import { describe, expect, it } from "vitest";
import atLimit from "../../../../../supervisor/internal/productprotocol/testdata/response-candidate-at-frame-limit.json";
import { parseCandidate } from "./wire";
import { utf8Length } from "../core/validate";

describe("candidate protocol frame bound", () => {
  it("rejects an oversized preview even when each field and the path count fit", () => {
    const changes = Array.from({ length: 17 }, (_, index) => ({ path: `${index}`, op: "modify", diff: "x".repeat(64 * 1024) }));
    expect(() => parseCandidate({ candidate: { turn_id: "psess-t1/turn-1", candidate_digest: "sha256:c", changes } })).toThrow(/frame/);
  });

  it("accepts every path in the supervisor's 1 MiB boundary fixture", () => {
    expect(utf8Length(JSON.stringify(atLimit))).toBe(1024 * 1024);
    expect(parseCandidate({ candidate: atLimit.candidate }).changes).toHaveLength(3200);
  });

  it("refuses the complete candidate at bound plus one byte", () => {
    const over = structuredClone(atLimit);
    const first = over.candidate.changes[0];
    if (first === undefined) throw new Error("missing boundary change");
    first.diff = `${first.diff ?? ""}x`;
    expect(utf8Length(JSON.stringify(over))).toBe(1024 * 1024 + 1);
    expect(() => parseCandidate({ candidate: over.candidate })).toThrow(/frame/);
  });
});
