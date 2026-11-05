import { describe, expect, it } from "vitest";
import { messageItem } from "../../../core/testing/builders";
import type { Item, ItemView } from "../../../core/contract";
import { groupTurn, type ToolBlock } from "../../../core/store";
import { blocksOf, itemsOf, type ToolCallSpec } from "../../../test-support/toolBlocks";
import { GROUP_MIN, callTitle, groupRuns, outcomeReading, severityRank, summariseToolGroup, toolFindings, traceToneOf } from "./toolGroups";

const unverified = (_item: ItemView): boolean => false;
const verified = (_item: ItemView): boolean => true;

const only = (specs: ToolCallSpec): ToolBlock => blocksOf(specs)[0] as ToolBlock;
const words = (block: ToolBlock, verifiedOf = verified): string[] => toolFindings(block, verifiedOf).map((finding) => `${finding.tone}:${finding.label}`);

describe("the findings of one tool block", () => {
  it("never reads a request with no result as a success or as reported, verified or not, even when the request says succeeded", () => {
    const block = only({ call: "c1", request: "succeeded" });
    expect(words(block, verified)).toEqual(["idle:no result"]);
    expect(words(block, unverified)).toEqual(["idle:no result"]);
    expect(traceToneOf(block, verified)).toBe("idle");
  });

  it("reads a request that says succeeded, with no result, as 'no result' on the card's own reading too", () => {
    const block = only({ call: "c1", request: "succeeded" });
    expect(outcomeReading(block, verified)?.label).toBe("no result");
    expect(outcomeReading(block, unverified)?.label).toBe("no result");
    expect(outcomeReading(only({ call: "c2", request: "pending" }), verified)?.label).toBe("pending");
  });

  it("draws a policy ALLOW with no outcome as the permission tone, never the success tone", () => {
    const block = blocksOf({ call: "c1", verdict: "ALLOW" })[0] as ToolBlock;
    expect(traceToneOf(block, verified)).not.toBe("ok");
    expect(words(block).join()).toContain("pending");
  });

  it("keeps a DENY verdict beside a later success, so the line is deny and the success is still counted", () => {
    const block = only({ call: "c1", verdict: "DENY", result: "succeeded" });
    expect(words(block)).toEqual(["ok:succeeded", "deny:DENY"]);
    expect(traceToneOf(block, verified)).toBe("deny");
  });

  it("keeps an UNKNOWN verdict beside a verified success: the line is unknown, never the success tone", () => {
    const block = only({ call: "c1", verdict: "UNKNOWN", result: "succeeded" });
    expect(words(block)).toEqual(["ok:succeeded", "unknown:UNKNOWN"]);
    expect(traceToneOf(block, verified)).toBe("unknown");
  });

  it("keeps a lost result beside a DENY verdict, so LOST is not hidden behind the refusal", () => {
    const block = only({ call: "c1", verdict: "DENY", result: "lost" });
    expect(words(block).sort()).toEqual(["deny:DENY", "lost:lost"]);
  });

  it("draws a success as ok only when it is verified, and as reported when it is not", () => {
    const block = only({ call: "c1", verdict: "ALLOW", result: "succeeded" });
    expect(traceToneOf(block, verified)).toBe("ok");
    expect(traceToneOf(block, unverified)).toBe("reported");
  });
});

describe("a group's summary", () => {
  it("takes the worst tone of its findings, and counts each kind in words, worst first", () => {
    const blocks = blocksOf(
      { call: "c1", tool: "read", verdict: "ALLOW", result: "succeeded" },
      { call: "c2", tool: "edit", verdict: "ALLOW", result: "succeeded" },
      { call: "c3", tool: "bash", verdict: "DENY", result: "denied" },
    );
    const summary = summariseToolGroup(blocks, unverified);
    expect(summary.tone).toBe("deny");
    expect(summary.findings.map((entry) => `${entry.count} ${entry.label}`)).toEqual(["1 denied", "1 DENY", "2 reported"]);
    expect(summary.tools).toEqual([
      { name: "read", count: 1 },
      { name: "edit", count: 1 },
      { name: "bash", count: 1 },
    ]);
  });

  it("is not green when three UNKNOWN decisions each have a verified success", () => {
    const blocks = blocksOf(
      { call: "c1", verdict: "UNKNOWN", result: "succeeded" },
      { call: "c2", verdict: "UNKNOWN", result: "succeeded" },
      { call: "c3", verdict: "UNKNOWN", result: "succeeded" },
    );
    const summary = summariseToolGroup(blocks, verified);
    expect(summary.tone).toBe("unknown");
    expect(summary.findings.map((entry) => `${entry.count} ${entry.label}`)).toEqual(["3 UNKNOWN", "3 succeeded"]);
  });

  it("does not count three request-only blocks as reported successes", () => {
    const blocks = blocksOf({ call: "c1", request: "succeeded" }, { call: "c2", request: "succeeded" }, { call: "c3", request: "succeeded" });
    const summary = summariseToolGroup(blocks, unverified);
    expect(summary.findings.map((entry) => `${entry.count} ${entry.label}`)).toEqual(["3 no result"]);
    expect(summary.tone).toBe("idle");
  });

  it("is the success tone only when every finding is a verified success", () => {
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

describe("what a call is called on a folded line", () => {
  it("is its tool and what it acted on, with the tool's own repeat trimmed, and never says whether it ran", () => {
    // The helper's summary is "<tool> call": the repeated tool name is trimmed from the target.
    expect(callTitle(only({ call: "c1", tool: "read", request: "succeeded" }))).toBe("read call");
    expect(callTitle(only({ call: "c2", tool: "bash", verdict: "DENY", result: "denied" }))).toBe("bash call");
  });
});

describe("severity order, for summaries that name only a few findings", () => {
  it("ranks a refusal, then LOST, then unknown, then reported, before a permission, so the worst is named first", () => {
    const ranked = (["allow", "reported", "unknown", "lost", "deny"] as const).slice().sort((a, b) => severityRank(a) - severityRank(b));
    expect(ranked).toEqual(["deny", "lost", "unknown", "reported", "allow"]);
  });
});

describe("runs of tool blocks", () => {
  const tool = (call: string): Item[] => itemsOf({ call }, Number(call.slice(1)) * 10);
  const message = (id: string, sequence: number): Item => messageItem({ id, sequence, role: "assistant" });
  const lengths = (items: Item[]): Array<number | string> =>
    groupRuns(groupTurn(items), "t1").map((block) => (block.kind === "run" ? block.blocks.length : block.kind));

  it("gathers every consecutive tool block into one run, grouped or not by its length", () => {
    expect(lengths([...tool("c1")])).toEqual([1]);
    expect(lengths([...tool("c1"), ...tool("c2")])).toEqual([2]);
    expect(lengths([...tool("c1"), ...tool("c2"), ...tool("c3")])).toEqual([3]);
    expect(GROUP_MIN).toBe(2);
  });

  it("ends a run at a message, so a message is never inside a run", () => {
    expect(lengths([...tool("c1"), ...tool("c2"), message("m1", 100), ...tool("c3"), ...tool("c4"), ...tool("c5")])).toEqual([2, "message", 3]);
  });

  it("keys a run by its turn and its place among the turn's runs, so it keeps its key when it grows or loses its first block, and two turns never share an anchor", () => {
    const keysOf = (items: Item[]): string[] => groupRuns(groupTurn(items), "t1").flatMap((block) => (block.kind === "run" ? [block.key] : []));
    expect(keysOf([...tool("c1"), ...tool("c2")])).toEqual(["run-t1-0"]);
    expect(keysOf([...tool("c1"), ...tool("c2"), ...tool("c3")])).toEqual(["run-t1-0"]);
    // A filter that drops the first block leaves the run where it was.
    expect(keysOf([...tool("c2")])).toEqual(["run-t1-0"]);
    expect(keysOf([...tool("c1"), message("m1", 100), ...tool("c2")])).toEqual(["run-t1-0", "run-t1-1"]);
    expect(groupRuns(groupTurn([...tool("c1")]), "t2").map((block) => (block.kind === "run" ? block.key : ""))).toEqual(["run-t2-0"]);
  });
});
