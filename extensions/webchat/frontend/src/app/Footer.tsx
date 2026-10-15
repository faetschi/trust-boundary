// The footer: the session's statistics as one wrapping line. A count the projection does not carry reads
// "unavailable", never zero. Tokens and timing are not projected, so they read unavailable too. Before the first snapshot
// every count is unavailable: an empty window is a statement only once a projection has loaded.

import { decisionsState, verdictCount } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Footer.module.css";

type StatsModel = Pick<WorkspaceModel, "session" | "panels" | "items" | "evicted" | "loaded">;

// A count the projection carries, or null: an absent or non-integer value is not a number to show.
function countOf(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 ? value : null;
}

// The turn count. Zero reads unavailable, not "0 turns": the gateway writes an absent turn_count as zero, and a session with
// records has at least one turn, so a zero here is the absent value. A real session with no turns reads unavailable too; the
// projection cannot tell the two apart.
function turnsPart(model: StatsModel): string {
  const turns = countOf(model.session?.turn_count);
  if (turns === null || turns === 0) return "turns unavailable";
  return `${turns} ${turns === 1 ? "turn" : "turns"}`;
}

function recordsPart(model: StatsModel): string {
  if (!model.loaded) return "records retained unavailable";
  const count = model.items.length;
  const evicted = model.evicted > 0 ? ` (${model.evicted} earlier evicted)` : "";
  return `${count} ${count === 1 ? "record" : "records"} retained${evicted}`;
}

// The policy verdicts. An all-zero panel with no digest is the gateway's zero for an absent panel, so it reads unavailable
// (decisionsState); the counts are shown only when the projection states them. They are verdicts, not results: the
// Chat view's filters name results, so the two never share a word.
function policyPart(model: StatsModel): string {
  const governance = model.panels?.governance;
  if (!governance || decisionsState(governance, verdictCount(model.items)) === "absent") return "policy verdicts unavailable";
  const word = (value: unknown) => {
    const count = countOf(value);
    return count === null ? "unavailable" : String(count);
  };
  return `policy verdicts ${word(governance.allow_count)} allow / ${word(governance.deny_count)} deny / ${word(governance.unknown_count)} unknown`;
}

// The statistics as the one line the footer shows.
export function statsLine(model: StatsModel): string {
  return [
    turnsPart(model),
    recordsPart(model),
    policyPart(model),
    "tokens unavailable",
    "timing unavailable",
  ].join(" · ");
}

export function Footer({ model, version }: { readonly model: WorkspaceModel; readonly version: string }) {
  return (
    <div className={styles.footer}>
      <div role="group" aria-label="Session statistics" className={styles.line}>
        {statsLine(model)}
      </div>
      <p className={styles.build}>build {version}</p>
    </div>
  );
}
