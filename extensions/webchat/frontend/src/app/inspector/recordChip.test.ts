// The chip and status-word rules, as pure functions (no DOM).
import { describe, expect, it } from "vitest";
import { activityItem, resultItem } from "../../core/testing/builders";
import { chipToneOf, sourceRole, stateWord } from "./recordChip";

describe("chipToneOf: a core variant as a chip tone", () => {
  it("maps the outcome states onto the shared tone set", () => {
    expect(chipToneOf("succeeded")).toBe("ok");
    expect(chipToneOf("denied")).toBe("deny");
    expect(chipToneOf("failed")).toBe("deny");
    expect(chipToneOf("reported")).toBe("reported");
    expect(chipToneOf("lost")).toBe("lost");
    expect(chipToneOf("pending")).toBe("pending");
    expect(chipToneOf("allow")).toBe("allow");
  });

  it("takes the conservative unknown tone for a variant it does not know, never a success", () => {
    expect(chipToneOf("ready-ish")).toBe("unknown");
    expect(chipToneOf("")).toBe("unknown");
  });

  it("reads a prototype key as absent, not as a tone", () => {
    expect(chipToneOf("constructor")).toBe("unknown");
    expect(chipToneOf("__proto__")).toBe("unknown");
  });
});

describe("sourceRole", () => {
  it("styles the unknown source as a message, and passes the other sources through", () => {
    expect(sourceRole("unknown")).toBe("message");
    expect(sourceRole("decision")).toBe("decision");
    expect(sourceRole("tool")).toBe("tool");
  });
});

describe("stateWord: the state chip's word", () => {
  it("reads an unverified success as reported, never as the confirmed word", () => {
    const item = resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded" });
    expect(stateWord(item, "reported")).toBe("reported");
  });

  it("reads a lossy success as succeeded (output lossy, not confirmed), on the unknown tone", () => {
    const item = resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true, authority: "observed", verification_scope: "structural-audit-only" });
    expect(stateWord(item, "unknown")).toBe("succeeded (output lossy, not confirmed)");
  });

  it("reads other states as the projection names them", () => {
    expect(stateWord(resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" }), "ok")).toBe("succeeded");
    expect(stateWord(activityItem({ id: "a1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "pending" }), "pending")).toBe("pending");
    expect(stateWord(resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "unknown" }), "unknown")).toBe("unknown");
  });
});
