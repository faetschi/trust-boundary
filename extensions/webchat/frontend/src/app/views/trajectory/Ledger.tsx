import { useId, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import type { RecordGroup } from "../../../core/trajectory";
import { EmptyState, ErrorState, LoadingState } from "../../../ui/StateMessage";
import type { ProjectionPhase } from "../../status";
import { RecordRow } from "./RecordRow";
import styles from "./Trajectory.module.css";

interface LedgerProps {
  readonly phase: ProjectionPhase;
  // Why the projection failed to load, shown in place of the ledger when it did.
  readonly failure: string;
  readonly source: string;
  // Records retained by the projection, before any search.
  readonly total: number;
  // The visible records, in groups. A search has already narrowed these; the rail never sees them.
  readonly groups: readonly RecordGroup[];
  readonly selectedId: string | null;
  readonly ledgerRef: RefObject<HTMLDivElement | null>;
  // The row elements by record id. The owner uses them to return focus to a row.
  readonly rows: Map<string, HTMLButtonElement>;
  readonly onSelect: (id: string, row: HTMLButtonElement) => void;
}

// The record ledger: the visible records in groups, each a button row. One row is in the tab order
// (a roving tab stop), so Tab crosses the ledger in one stop. ArrowUp and ArrowDown move between
// rows, across group boundaries; Home and End go to the ends. Enter or Space selects the focused
// row, as a button does. The container is a named region, not a list: each group names itself.
export function Ledger({ phase, failure, source, total, groups, selectedId, ledgerRef, rows, onSelect }: LedgerProps) {
  const [lastFocused, setLastFocused] = useState<string | null>(null);
  const ids = groups.flatMap((group) => group.records.map((record) => record.id ?? ""));
  const stop = pickStop(ids, lastFocused, selectedId);

  function registerRow(id: string, element: HTMLButtonElement | null) {
    if (element) rows.set(id, element);
    else rows.delete(id);
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const elements = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button[data-record-row]"));
    const index = elements.findIndex((element) => element === event.target);
    if (index < 0) return;
    let next: number;
    switch (event.key) {
      case "ArrowDown":
        next = Math.min(index + 1, elements.length - 1);
        break;
      case "ArrowUp":
        next = Math.max(index - 1, 0);
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = elements.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    const target = elements[next];
    if (!target) return;
    setLastFocused(target.dataset.id ?? null);
    target.focus();
  }

  return (
    <div
      ref={ledgerRef}
      role="region"
      aria-label="Record ledger"
      tabIndex={-1}
      className={styles.ledger}
      onKeyDown={onKeyDown}
    >
      {phase === "loading" ? (
        <LoadingState label="Loading projection…" />
      ) : phase === "failed" ? (
        <ErrorState title="Projection not loaded">{failure}</ErrorState>
      ) : total === 0 ? (
        <EmptyState title="No records">
          <p>
            {source === "unavailable"
              ? "Production observations are unavailable. No live Pi/session source is registered."
              : "No record has been projected yet."}
          </p>
        </EmptyState>
      ) : groups.length === 0 ? (
        <EmptyState title="No matching records">
          <p>No record matches the current search. Clear the search to review the whole trajectory.</p>
        </EmptyState>
      ) : (
        groups.map((group) => (
          <LedgerGroup key={group.id ?? group.title} title={group.title} meta={group.meta}>
            {group.records.map((record) => (
              <RecordRow
                key={record.id}
                record={record}
                selected={record.id === selectedId}
                stop={record.id === stop}
                rowRef={registerRow}
                onSelect={onSelect}
                onFocusRow={setLastFocused}
              />
            ))}
          </LedgerGroup>
        ))
      )}
    </div>
  );
}

// The tab stop: the last row that had focus, else the selected row, else the first row. A stop
// that a search has filtered out is not used.
function pickStop(ids: readonly string[], lastFocused: string | null, selectedId: string | null): string | null {
  if (lastFocused !== null && ids.includes(lastFocused)) return lastFocused;
  if (selectedId !== null && ids.includes(selectedId)) return selectedId;
  return ids[0] ?? null;
}

function LedgerGroup({ title, meta, children }: { readonly title: string; readonly meta: string; readonly children: ReactNode }) {
  const titleId = useId();
  return (
    <div role="group" aria-labelledby={titleId} className={styles.group}>
      <div className={styles.groupHead}>
        <span id={titleId} className={styles.groupTitle}>
          {title}
        </span>
        {meta ? <span className={styles.groupMeta}>{meta}</span> : null}
      </div>
      <div className={styles.groupBody}>{children}</div>
    </div>
  );
}
