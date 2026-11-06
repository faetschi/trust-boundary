// The session's statistics as separate readings, for the details card's Retention section. A count the projection does not carry reads
// "unavailable", never zero. Tokens and timing are not projected, so they read unavailable too. Before the first snapshot every count
// is unavailable: an empty window is a statement only once a projection has loaded.

import { decisionsState, verdictCount } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";

type StatsModel = Pick<WorkspaceModel, "session" | "panels" | "items" | "evicted" | "loaded">;

// A count the projection carries, or null: an absent or non-integer value is not a number to show.
function countOf(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 ? value : null;
}

// The turn count: a stated count, zero included ("0 turns"), or unavailable when the source did not state one. Presence is the
// only signal, because the gateway omits an absent turn_count and writes a stated one.
function turnsPart(model: StatsModel): string {
  const turns = countOf(model.session?.turn_count);
  if (turns === null) return "turns unavailable";
  return `${turns} ${turns === 1 ? "turn" : "turns"}`;
}

function recordsPart(model: StatsModel): string {
  if (!model.loaded) return "records retained unavailable";
  const count = model.items.length;
  const evicted = model.evicted > 0 ? ` (${model.evicted} earlier evicted)` : "";
  return `${count} ${count === 1 ? "record" : "records"} retained${evicted}`;
}

// The policy verdicts. A stated count is shown, zero included; one the source did not state reads unavailable, and so does a
// set of stated zeros that a loaded verdict contradicts (decisionsState). They are verdicts, not results: the Chat view's
// filters name results, so the two never share a word.
function policyPart(model: StatsModel): string {
  const governance = model.panels?.governance;
  if (!governance) return "policy verdicts unavailable";
  const state = decisionsState(governance, verdictCount(model.items));
  if (state === "absent" || state === "contradicted") return "policy verdicts unavailable";
  const word = (value: unknown) => {
    const count = countOf(value);
    return count === null ? "unavailable" : String(count);
  };
  return `policy verdicts ${word(governance.allow_count)} allow / ${word(governance.deny_count)} deny / ${word(governance.unknown_count)} unknown`;
}

const TOKENS_PART = "tokens unavailable";
const TIMING_PART = "timing unavailable";

// The statistics as separate readings, so an unavailable one can be drawn in the warning ink.
export function statsParts(model: StatsModel): string[] {
  return [turnsPart(model), recordsPart(model), policyPart(model), TOKENS_PART, TIMING_PART];
}

// The statistics as the one line.
export function statsLine(model: StatsModel): string {
  return statsParts(model).join(" · ");
}

// What the closed Retention line says: the records retained, and the policy verdicts when they cannot be read. Tokens and timing are
// never projected, so they are not news and are not in the lead.
export function retentionLead(model: StatsModel): string {
  const policy = policyPart(model);
  return policy.endsWith("unavailable") ? `${recordsPart(model)} · ${policy}` : recordsPart(model);
}
