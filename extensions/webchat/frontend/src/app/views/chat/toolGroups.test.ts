import { describe, expect, it } from "vitest";
import { activityItem, decisionItem, messageItem, resultItem } from "../../../core/testing/builders";
import type { Item, ItemView, ResultState, Tool, Verdict } from "../../../core/contract";
import { groupTurn, type ToolBlock } from "../../../core/store";
import { GROUP_MIN, groupRuns, summariseToolGroup, toolOutcome, traceToneOf } from "./toolGroups";

const unverified = (_item: ItemView): boolean => false;
const verified = (_item: ItemView): boolean => true;

interface Spec {
  readonly call: string;
  readonly tool?: Tool;
  readonly verdict?: Verdict;
  readonly result?: ResultState;
  readonly request?: ResultState;
}

// The items of one tool call: its request, then a policy decision and a result when the spec has them.
function itemsOf(spec: Spec, at: number): Item[] {
  const tool = spec.tool ?? "bash";
  const items: Item[] = [activityItem({ id: `a-${spec.call}`, sequence: at, tool, tool_call_id: spec.call, state: spec.request ?? "pending" })];
  if (spec.verdict) {
    items.push(decisionItem({ id: `d-${spec.call}`, sequence: at + 1, tool, tool_call_id: spec.call, verdict: spec.verdict }));
  }
  if (spec.result) {
    items.push(resultItem({ id: `r-${spec.call}`, sequence: at + 2, tool, tool_call_id: spec.call, state: spec.result }));
  }
  return items;
}

function blocksOf(...specs: Spec[]): ToolBlock[] {
  const items = specs.flatMap((spec, index) => itemsOf(spec, index * 10 + 1));
  return groupTurn(items).filter((block): block is ToolBlock => block.kind === "tool");
}

describe("the outcome of one tool block", () => {
  it("never draws a success for a request with no result, even when the request itself says succeeded", () => {
    const [block] = blocksOf({ call: "c1", request: "succeeded" });
    expect(toolOutcome(block as ToolBlock, verified)).toEqual({ tone: "idle", label: "no result" });
    expect(traceToneOf(block as ToolBlock, verified)).toBe("idle");
  });

  it("draws a policy ALLOW with no result as the permission tone, never the success tone", () => {
    const [block] = blocksOf({ call: "c1", verdict: "ALLOW" });
    expect(traceToneOf(block as ToolBlock, verified)).not.toBe("ok");
  });

  it("draws a DENY verdict as deny even when a later result claims success", () => {
    const [block] = blocksOf({ call: "c1", verdict: "DENY", result: "succeeded" });
    expect(traceToneOf(block as ToolBlock, verified)).toBe("deny");
  });

  it("draws a success as ok only when it is verified, and as reported when it is not", () => {
    const [block] = blocksOf({ call: "c1", verdict: "ALLOW", result: "succeeded" });
    expect(traceToneOf(block as ToolBlock, verified)).toBe("ok");
    expect(traceToneOf(block as ToolBlock, unverified)).toBe("reported");
  });
});

describe("a group's summary", () => {
  it("takes the worst tone of its records, and counts each outcome in words, worst first", () => {
    const blocks = blocksOf(
      { call: "c1", tool: "read", verdict: "ALLOW", result: "succeeded" },
      { call: "c2", tool: "edit", verdict: "ALLOW", result: "succeeded" },
      { call: "c3", tool: "bash", verdict: "DENY", result: "denied" },
    );
    const summary = summariseToolGroup(blocks, unverified);
    expect(summary.tone).toBe("deny");
    expect(summary.outcomes.map((entry) => `${entry.count} ${entry.label}`)).toEqual(["1 denied", "2 reported"]);
    expect(summary.tools).toEqual([
      { name: "read", count: 1 },
      { name: "edit", count: 1 },
      { name: "bash", count: 1 },
    ]);
  });

  it("is the success tone only when every record is a verified success", () => {
    const allOk = blocksOf(
      { call: "c1", verdict: "ALLOW", result: "succeeded" },
      { call: "c2", verdict: "ALLOW", result: "succeeded" },
      { call: "c3", verdict: "ALLOW", result: "succeeded" },
    );
    expect(summariseToolGroup(allOk, verified).tone).toBe("ok");
    const oneOpen = blocksOf(
      { call: "c1", verdict: "ALLOW", result: "succeeded" },
      { call: "c2", verdict: "ALLOW", result: "succeeded" },
      { call: "c3", verdict: "ALLOW" },
    );
    expect(summariseToolGroup(oneOpen, verified).tone).not.toBe("ok");
  });

  it("counts repeated tools once, with how many times they ran", () => {
    const blocks = blocksOf({ call: "c1", tool: "bash" }, { call: "c2", tool: "bash" }, { call: "c3", tool: "read" });
    expect(summariseToolGroup(blocks, verified).tools).toEqual([
      { name: "bash", count: 2 },
      { name: "read", count: 1 },
    ]);
  });
});

describe("which runs are grouped", () => {
  const tool = (call: string): Item[] => itemsOf({ call }, Number(call.slice(1)) * 10);
  const message = (id: string, sequence: number): Item => messageItem({ id, sequence, role: "assistant" });

  it(`leaves fewer than ${GROUP_MIN} consecutive tool blocks as they are`, () => {
    const drawn = groupRuns(groupTurn([...tool("c1"), ...tool("c2")]));
    expect(drawn.map((block) => block.kind)).toEqual(["tool", "tool"]);
  });

  it(`gathers ${GROUP_MIN} or more consecutive tool blocks into one group`, () => {
    const drawn = groupRuns(groupTurn([...tool("c1"), ...tool("c2"), ...tool("c3")]));
    expect(drawn.map((block) => block.kind)).toEqual(["group"]);
    expect(drawn[0]?.kind === "group" ? drawn[0].blocks.length : 0).toBe(3);
  });

  it("ends a run at a message, so a message is never inside a group", () => {
    const drawn = groupRuns(groupTurn([...tool("c1"), ...tool("c2"), message("m1", 100), ...tool("c3"), ...tool("c4"), ...tool("c5")]));
    expect(drawn.map((block) => block.kind)).toEqual(["tool", "tool", "message", "group"]);
  });

  it("keeps a stable key for a group, from its first block", () => {
    const drawn = groupRuns(groupTurn([...tool("c1"), ...tool("c2"), ...tool("c3")]));
    expect(drawn[0]?.kind === "group" ? drawn[0].key : "").toBe("group-c1");
  });
});
