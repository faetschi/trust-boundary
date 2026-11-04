// Grouping consecutive tool blocks into one summary line, and the tone rules that line and a single tool card share. Pure: no
// DOM, no React. The honesty rules of readings.ts carry through: a group is never calmer than its worst record, an ALLOW is
// never the success tone, and a request with no result is never a success.

import type { ChipTone } from "../../../ui/Chip";
import type { ItemView } from "../../../core/contract";
import type { ToolBlock, TurnBlock } from "../../../core/store";
import { stateReading, verdictReading } from "./readings";

// Whether an item is verified in its projection (app verification.ts isVerifiedIn).
type Verifier = (item: ItemView) => boolean;

// A run of this many consecutive tool blocks, or more, is drawn as one group. Two tool cards read fine as they are.
export const GROUP_MIN = 3;

// The outcome of one tool block, in words and tone. The result's state when there is one, else the request's, else the policy
// verdict. A refused record (a DENY verdict, or a result in the deny tone) is deny, whatever else it says.
export interface ToolOutcome {
  readonly tone: ChipTone;
  readonly label: string;
}

export function toolOutcome(block: ToolBlock, verifiedOf: Verifier): ToolOutcome {
  const { activity, decision, result } = block;
  const stateItem = result ?? activity;
  const state = stateItem
    ? stateReading(stateItem.state, verifiedOf(stateItem), stateItem.type === "result" && stateItem.lossy === true)
    : null;
  const verdict = decision ? verdictReading(decision.verdict) : null;
  if (verdict?.tone === "deny" || state?.tone === "deny") {
    return { tone: "deny", label: state?.tone === "deny" ? state.label : (verdict?.label ?? "DENY") };
  }
  const read = state ?? verdict;
  if (read === null) return { tone: "idle", label: "no outcome" };
  // Only a terminal result may draw the success tone: an activity that reports "succeeded" with no result after it is a request.
  if (read.tone === "ok" && result === null) return { tone: "idle", label: "no result" };
  return { tone: read.tone, label: read.label };
}

// The tone of a block's trace line.
export function traceToneOf(block: ToolBlock, verifiedOf: Verifier): ChipTone {
  return toolOutcome(block, verifiedOf).tone;
}

// Worst first. The success tone is last, so a group draws it only when every record in it is a verified success.
const SEVERITY: readonly ChipTone[] = ["deny", "lost", "unknown", "reported", "pending", "allow", "idle", "ok"];

export interface OutcomeCount {
  readonly tone: ChipTone;
  readonly label: string;
  readonly count: number;
}

export interface ToolGroupSummary {
  // The group's tone: the worst of its records.
  readonly tone: ChipTone;
  // The distinct tools, in the order they first appear, with how many times each ran.
  readonly tools: ReadonlyArray<{ readonly name: string; readonly count: number }>;
  // How many records ended in each outcome, worst first.
  readonly outcomes: readonly OutcomeCount[];
}

export function summariseToolGroup(blocks: readonly ToolBlock[], verifiedOf: Verifier): ToolGroupSummary {
  const tools = new Map<string, number>();
  const outcomes = new Map<string, OutcomeCount>();
  for (const block of blocks) {
    const name = (block.result ?? block.activity ?? block.decision)?.tool ?? "tool";
    tools.set(name, (tools.get(name) ?? 0) + 1);
    const outcome = toolOutcome(block, verifiedOf);
    const key = `${outcome.tone}|${outcome.label}`;
    outcomes.set(key, { ...outcome, count: (outcomes.get(key)?.count ?? 0) + 1 });
  }
  const ordered = [...outcomes.values()].sort((a, b) => SEVERITY.indexOf(a.tone) - SEVERITY.indexOf(b.tone) || b.count - a.count);
  const tone = SEVERITY.find((candidate) => ordered.some((entry) => entry.tone === candidate)) ?? "idle";
  return { tone, tools: [...tools].map(([name, count]) => ({ name, count })), outcomes: ordered };
}

// A turn's blocks, with each run of GROUP_MIN or more consecutive tool blocks gathered into one group. A message ends a run.
export interface ToolRun {
  readonly kind: "group";
  readonly key: string;
  readonly blocks: readonly ToolBlock[];
}

export type DrawnBlock = TurnBlock | ToolRun;

export function groupRuns(blocks: readonly TurnBlock[]): DrawnBlock[] {
  const drawn: DrawnBlock[] = [];
  let run: ToolBlock[] = [];
  const flush = (): void => {
    if (run.length >= GROUP_MIN) drawn.push({ kind: "group", key: `group-${run[0]?.key ?? ""}`, blocks: run });
    else drawn.push(...run);
    run = [];
  };
  for (const block of blocks) {
    if (block.kind === "tool") {
      run.push(block);
      continue;
    }
    flush();
    drawn.push(block);
  }
  flush();
  return drawn;
}
