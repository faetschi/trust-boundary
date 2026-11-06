// Tool-call fixtures for the tool group tests: the items of one call (its request, its policy decision, its result), and the
// correlated blocks of several calls. Test-only: non-test code must not import this folder (check-sinks.mjs enforces that).
import { activityItem, decisionItem, resultItem } from "../core/testing/builders";
import type { Item, ResultState, Tool, Verdict } from "../core/contract";
import { groupTurn, type ToolBlock } from "../core/store";

export interface ToolCallSpec {
  readonly call: string;
  readonly tool?: Tool;
  readonly verdict?: Verdict;
  readonly result?: ResultState;
  readonly request?: ResultState;
}

// The items of one tool call: its request, then a policy decision and a result when the spec has them.
export function itemsOf(spec: ToolCallSpec, at: number): Item[] {
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

export function blocksOf(...specs: ToolCallSpec[]): ToolBlock[] {
  const items = specs.flatMap((spec, index) => itemsOf(spec, index * 10 + 1));
  return groupTurn(items).filter((block): block is ToolBlock => block.kind === "tool");
}