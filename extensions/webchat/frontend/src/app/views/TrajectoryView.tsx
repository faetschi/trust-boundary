import { useEffect, useMemo, useRef, useState } from "react";
import { jumpLabel } from "../../core/anchor";
import { visibleRecords } from "../../core/trajectory";
import { failureText, projectionPhase } from "../status";
import { useShell } from "../shellContext";
import { judgedItem } from "../verification";
import type { WorkspaceModel } from "../workspaceStore";
import { Ledger } from "./trajectory/Ledger";
import { SequenceRail } from "./trajectory/SequenceRail";
import { TrajectoryToolbar } from "./trajectory/Toolbar";
import { useTailWindow } from "./trajectory/useTailWindow";
import styles from "./trajectory/Trajectory.module.css";

// The Trajectory view: a toolbar (grouping and search), the sequence rail, the record ledger and
// the tail control. Every control is presentation: none can change the projection, a verdict or
// the timeline's finding filter. Selecting a record goes through the shell (shellContext.ts), so
// the drawer and the focus contract apply.
export function TrajectoryView({ model }: { readonly model: WorkspaceModel }) {
  const shell = useShell();
  const { actions } = model;
  const ledgerRef = useRef<HTMLDivElement | null>(null);
  const [rows] = useState(() => new Map<string, HTMLButtonElement>());
  const phase = projectionPhase(model);
  // Every record as the shared gate judges it: the rail, the ledger and the inspector all read these.
  const judged = useMemo(
    () => model.items.map((item) => judgedItem(model, item)),
    [model.items, model.source, model.verification],
  );
  const visible = useMemo(
    () => visibleRecords({ items: judged, query: model.query, group: model.group }),
    [judged, model.query, model.group],
  );
  const searching = model.query.trim() !== "";
  const tail = useTailWindow({ actions, pending: model.tailPending, count: model.items.length, ledgerRef });

  // Focus when the selection is cleared by something other than a focused control. If the focus is
  // still on a live element, it is left alone. Otherwise a narrow drawer closes (its own focus rule
  // returns focus to the opener, or to the toggle when the opener is gone), and a wide ledger
  // returns focus to the row that was selected, or to the ledger when that row was evicted.
  const previousSelected = useRef(model.selectedId);
  useEffect(() => {
    const previous = previousSelected.current;
    previousSelected.current = model.selectedId;
    if (previous === null || model.selectedId !== null) return;
    const active = document.activeElement;
    if (active && active !== document.body && active.isConnected) return;
    if (shell.drawerOpen) {
      shell.closeInspector();
      return;
    }
    const row = rows.get(previous);
    (row?.isConnected ? row : ledgerRef.current)?.focus({ preventScroll: true });
  }, [model.selectedId, shell, rows]);

  return (
    <div className={styles.view}>
      <TrajectoryToolbar
        group={model.group}
        query={model.query}
        loaded={phase === "loaded"}
        shown={visible.items.length}
        total={model.items.length}
        onGroup={actions.setGroup}
        onQuery={actions.setQuery}
      />
      <SequenceRail items={judged} phase={phase} searching={searching} />
      <Ledger
        phase={phase}
        failure={failureText(model.error, model.status)}
        source={model.source}
        total={model.items.length}
        groups={visible.groups}
        selectedId={model.selectedId}
        ledgerRef={ledgerRef}
        rows={rows}
        onSelect={(id, row) => shell.selectRecord(id, row)}
      />
      {tail.pending > 0 ? (
        <button type="button" className={styles.jump} onClick={tail.jump}>
          {jumpLabel(tail.pending)}
        </button>
      ) : null}
    </div>
  );
}
