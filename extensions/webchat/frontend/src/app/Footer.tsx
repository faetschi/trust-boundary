// The footer: the session's statistics as one wrapping line. A count the projection does not carry reads
// "unavailable", never zero. Tokens and timing are not projected, so they read unavailable too. Before the first snapshot
// every count is unavailable: an empty window is a statement only once a projection has loaded.

import { Fragment } from "react";
import { decisionsState, verdictCount } from "./panels";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Footer.module.css";

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

// The statistics as separate readings. Each one is a part the footer can draw on its own, so an unavailable one can be drawn in
// the warning ink.
export function statsParts(model: StatsModel): string[] {
  return [turnsPart(model), recordsPart(model), policyPart(model), "tokens unavailable", "timing unavailable"];
}

// The statistics as the one line the footer shows.
export function statsLine(model: StatsModel): string {
  return statsParts(model).join(" · ");
}

export function Footer({ model, version }: { readonly model: WorkspaceModel; readonly version: string }) {
  return (
    <div className={styles.footer}>
      <div role="group" aria-label="Session statistics" className={styles.line}>
        {statsParts(model).map((part, index) => (
          <Fragment key={part}>
            {index > 0 ? <span className={styles.sep}> · </span> : null}
            <span className={part.includes("unavailable") ? styles.un : undefined}>{part}</span>
          </Fragment>
        ))}
      </div>
      <p className={styles.build}>build {version}</p>
    </div>
  );
}
