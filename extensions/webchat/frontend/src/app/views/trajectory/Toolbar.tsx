import { RECORD_GROUPS } from "../../../core/trajectory";
import { ToggleGroup, type ToggleItem } from "../../../ui/ToggleGroup";
import { VisuallyHidden } from "../../../ui/VisuallyHidden";
import styles from "./Trajectory.module.css";

const GROUP_ITEMS: readonly ToggleItem[] = RECORD_GROUPS.map((group) => ({ value: group.id, label: group.label }));

interface TrajectoryToolbarProps {
  readonly group: string;
  readonly query: string;
  // Records the ledger shows, and records retained. The summary says both when a search narrows.
  readonly shown: number;
  readonly total: number;
  readonly onGroup: (group: string) => void;
  readonly onQuery: (query: string) => void;
}

// The Trajectory toolbar: the grouping as a labelled toggle group (one tab stop, arrows between
// the groups), the record search, and the summary line. Grouping and search are presentation: they
// never change the projection, and the search is over allowlisted bounded fields only.
export function TrajectoryToolbar({ group, query, shown, total, onGroup, onQuery }: TrajectoryToolbarProps) {
  const active = RECORD_GROUPS.find((entry) => entry.id === group) ?? RECORD_GROUPS[0];
  const searching = query.trim() !== "";
  const counted = searching ? `${shown} of ${total} record(s) matching “${query}”` : `${total} record(s)`;
  return (
    <>
      <div className={styles.toolbar}>
        <div className={styles.groupRow}>
          <ToggleGroup
            label="Group records by"
            items={GROUP_ITEMS}
            pressed={active?.id ?? "turn"}
            onPress={onGroup}
          />
          <p className={styles.hint}>{active ? `${active.label}: ${active.hint}.` : ""}</p>
        </div>
        <label className={styles.search}>
          <VisuallyHidden>Search records</VisuallyHidden>
          <input
            type="search"
            className={styles.searchInput}
            placeholder="Search records…"
            value={query}
            onChange={(event) => onQuery(event.currentTarget.value)}
          />
        </label>
      </div>
      <p className={styles.summary}>{`${counted} · grouped by ${active?.label ?? "Turn"}`}</p>
    </>
  );
}
