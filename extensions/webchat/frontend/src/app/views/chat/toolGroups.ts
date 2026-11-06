// Grouping consecutive tool blocks into runs, and the findings and tone rules that a tool card and a group line share. Pure: no
// DOM, no React. The honesty rules of readings.ts carry through: a block keeps every finding it has (its policy verdict and its
// outcome are counted separately), a group is never calmer than its worst finding, an ALLOW is never the success tone, and a
// request with no result is never a success.

import type { ChipTone } from "../../../ui/Chip";
import type { ItemView } from "../../../core/contract";
import type { ToolBlock, TurnBlock } from "../../../core/store";
import { headlineSummary } from "./headline";
import { EMPTY_TOOL_SUMMARY, stateReading, verdictReading, type ChipReading } from "./readings";

// Whether an item is verified in its projection (app verification.ts isVerifiedIn).
type Verifier = (item: ItemView) => boolean;

// The first bounded summary of a block, in the order the legacy transcript used: request, policy, result.
export function summaryOf(block: ToolBlock): string | undefined {
  for (const candidate of [block.activity, block.decision, block.result]) {
    if (candidate?.summary) return candidate.summary;
  }
  return undefined;
}

// What one call is called on a folded line: its tool, then what it acted on ("read service/entrypoint.go"). It names the call and says
// nothing about its outcome, so it never claims the call ran or succeeded.
export function callTitle(block: ToolBlock): string {
  const tool = (block.result ?? block.activity ?? block.decision)?.tool ?? "tool";
  const target = headlineSummary(tool, summaryOf(block));
  return target === "" || target === EMPTY_TOOL_SUMMARY ? tool : `${tool} ${target}`;
}

// A run of this many consecutive tool blocks, or more, is drawn as one folded line over its cards. One call stays a plain card.
export const GROUP_MIN = 2;

interface Finding {
  readonly tone: ChipTone;
  readonly label: string;
  // A policy verdict (DENY, UNKNOWN) is not a result (denied, unresolved): the group line names the kind, so "1 DENY verdict" and
  // "1 denied" are never read as one count.
  readonly kind?: "verdict";
}

// A request with no result after it: whatever state the request itself reports, nothing terminal has been recorded. It is not a
// success (verified or not) and it is not running.
const NO_RESULT_READING: ChipReading = {
  label: "no result",
  tone: "idle",
  note: "a request with no result recorded after it: not a success, and not running.",
};

// The reading of a block's outcome, for its chip and for its findings: the result's state, else the request's, else nothing. An
// activity that says "succeeded" with no result after it reads as NO_RESULT_READING, so the card and the group agree.
export function outcomeReading(block: ToolBlock, verifiedOf: Verifier): ChipReading | null {
  const { activity, result } = block;
  const stateItem = result ?? activity;
  if (!stateItem) return null;
  if (result === null && stateItem.state === "succeeded") return NO_RESULT_READING;
  return stateReading(stateItem.state, verifiedOf(stateItem), stateItem.type === "result" && stateItem.lossy === true);
}

// What a block says, as findings. Its outcome comes first: the result's state, else the request's. An activity that says
// "succeeded" with no result after it is a request, so it is "no result" whether or not the projection is verified. Its policy
// verdict follows when it is not an ALLOW, so a DENY or an UNKNOWN is never lost behind a success; an ALLOW is a finding only
// when it is all the block has.
export function toolFindings(block: ToolBlock, verifiedOf: Verifier): Finding[] {
  const { decision } = block;
  const outcome = outcomeReading(block, verifiedOf);
  const findings: Finding[] = outcome ? [{ tone: outcome.tone, label: outcome.label }] : [];
  const verdict = decision ? verdictReading(decision.verdict) : null;
  if (verdict && (verdict.tone !== "allow" || findings.length === 0)) findings.push({ tone: verdict.tone, label: verdict.label, kind: "verdict" });
  if (findings.length === 0) findings.push({ tone: "idle", label: "no outcome" });
  return findings;
}

// Worst first. The success tone is last, so a block or a group draws it only when every finding in it is a verified success.
const SEVERITY: readonly ChipTone[] = ["deny", "lost", "unknown", "reported", "pending", "allow", "idle", "ok"];

// A tone's place in that order: the lower, the worse. A summary that names only a few findings names the worst ones.
export function severityRank(tone: ChipTone): number {
  const index = SEVERITY.indexOf(tone);
  return index < 0 ? SEVERITY.length : index;
}

function worstTone(findings: readonly Finding[]): ChipTone {
  return SEVERITY.find((candidate) => findings.some((finding) => finding.tone === candidate)) ?? "idle";
}

// The tone of a block's trace line.
export function traceToneOf(block: ToolBlock, verifiedOf: Verifier): ChipTone {
  return worstTone(toolFindings(block, verifiedOf));
}

interface FindingCount extends Finding {
  readonly count: number;
}

interface ToolGroupSummary {
  // The group's tone: the worst of its findings.
  readonly tone: ChipTone;
  // The distinct tools, in the order they first appear, with how many times each ran.
  readonly tools: ReadonlyArray<{ readonly name: string; readonly count: number }>;
  // How many findings there are of each kind, worst first. A block with a verdict and an outcome adds to two kinds.
  readonly findings: readonly FindingCount[];
}

export function summariseToolGroup(blocks: readonly ToolBlock[], verifiedOf: Verifier): ToolGroupSummary {
  const tools = new Map<string, number>();
  const counted = new Map<string, FindingCount>();
  for (const block of blocks) {
    const name = (block.result ?? block.activity ?? block.decision)?.tool ?? "tool";
    tools.set(name, (tools.get(name) ?? 0) + 1);
    for (const finding of toolFindings(block, verifiedOf)) {
      const key = `${finding.tone}|${finding.label}`;
      counted.set(key, { ...finding, count: (counted.get(key)?.count ?? 0) + 1 });
    }
  }
  const findings = [...counted.values()].sort((a, b) => SEVERITY.indexOf(a.tone) - SEVERITY.indexOf(b.tone) || b.count - a.count);
  return { tone: worstTone(findings), tools: [...tools].map(([name, count]) => ({ name, count })), findings };
}

// A turn's blocks, with each run of consecutive tool blocks gathered into one run. A message ends a run. Whether a run is
// drawn as a group is its length (GROUP_MIN). A run is keyed by its turn and its place among the turn's runs, not by its first block (so two turns never share an anchor): a run
// that grows, or that loses its first block to a filter, keeps its key, so the cards it holds are never remounted.
export interface ToolRun {
  readonly kind: "run";
  readonly key: string;
  readonly blocks: readonly ToolBlock[];
}

type DrawnBlock = Exclude<TurnBlock, ToolBlock> | ToolRun;

// `whole` is the turn's blocks before any filter. A run's ordinal is its place among the whole turn's runs, so a filter that drops
// an earlier run or a message does not renumber the runs that remain.
export function groupRuns(blocks: readonly TurnBlock[], turnId: string, whole: readonly TurnBlock[] = blocks): DrawnBlock[] {
  const ordinalOf = new Map<string, number>();
  let place = 0;
  let inRun = false;
  for (const block of whole) {
    if (block.kind !== "tool") {
      inRun = false;
      continue;
    }
    if (!inRun) place += 1;
    inRun = true;
    ordinalOf.set(block.key, place - 1);
  }
  const drawn: DrawnBlock[] = [];
  let run: ToolBlock[] = [];
  const flush = (): void => {
    const first = run[0];
    if (first) drawn.push({ kind: "run", key: `run-${turnId}-${ordinalOf.get(first.key) ?? 0}`, blocks: run });
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
