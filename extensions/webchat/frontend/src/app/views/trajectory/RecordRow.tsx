import type { ReactNode } from "react";
import { timingOf, sourceLabel, statusText, type RecordView } from "../../../core/trajectory";
import { Chip } from "../../../ui/Chip";
import { StateDot } from "../../../ui/StateDot";
import { cx } from "../../../ui/cx";
import type { ProjectionState } from "../../../core/verification";
import { chipToneOf, sourceRole, stateWord } from "../../inspector/recordChip";
import styles from "./Trajectory.module.css";

interface RecordRowProps {
  readonly record: RecordView;
  // The selected record (aria-pressed).
  readonly selected: boolean;
  // The roving tab stop: the one row of the ledger that Tab reaches.
  readonly stop: boolean;
  readonly rowRef: (id: string, element: HTMLButtonElement | null) => void;
  readonly onSelect: (id: string, row: HTMLButtonElement) => void;
  readonly onFocusRow: (id: string) => void;
  // The projection the record is judged in: its status words say what that projection confirms.
  readonly projection: ProjectionState;
}

// One projected record, as a button. Its accessible name states the status in words: the status
// dot and the chips are decorative for assistive technology, so the name carries the state once.
export function RecordRow({ record, selected, stop, rowRef, onSelect, onFocusRow, projection }: RecordRowProps) {
  const id = record.id ?? "";
  const name = [
    `${sourceLabel(record.source)} record ${record.sequence}: ${record.label}.`,
    `${statusText(record.item, projection)}.`,
    record.payload,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button
      ref={(element) => rowRef(id, element)}
      type="button"
      className={cx(styles.row, selected && styles.selected)}
      data-record-row=""
      data-id={id}
      data-anchor={id}
      data-source={record.source}
      aria-pressed={selected}
      aria-label={name}
      tabIndex={stop ? 0 : -1}
      onClick={(event) => onSelect(id, event.currentTarget)}
      onFocus={() => onFocusRow(id)}
    >
      <StateDot tone={record.dot} />
      <span className={styles.kind}>
        <Chip source={sourceRole(record.source)}>{sourceLabel(record.source)}</Chip>
      </span>
      <span className={styles.label}>{record.label}</span>
      <span className={styles.payload} title={record.payload}>
        {record.payload}
      </span>
      <span className={styles.clock}>{timingOf(record.item).clock ?? "unavailable"}</span>
      <span className={styles.chips}>
        <RecordChips record={record} />
      </span>
    </button>
  );
}

// The record's trust chips. The state chip is drawn from the dot's tone and the wording of
// stateWord, so an unverified success is "reported" and never the confirmed word. The rest of the
// core's chips (verdict, lossy, unverified) are shown as the core gives them.
function RecordChips({ record }: { readonly record: RecordView }) {
  const { item } = record;
  const outcome = item.type === "result" || item.type === "activity";
  const stateHint = record.chips.find((entry) => entry.label === item.state)?.hint;
  const rest = record.chips.filter((entry) => !(outcome && entry.label === item.state));
  const nodes: ReactNode[] = [];
  if (outcome) {
    nodes.push(
      <span key="state" className={styles.chipWrap} title={stateHint}>
        <Chip tone={record.dot}>{stateWord(item, record.dot)}</Chip>
      </span>,
    );
  }
  for (const [index, entry] of rest.entries()) {
    if (entry.variant === "unverified") {
      nodes.push(
        <span key={`mark-${index}`} className={styles.unverified} title={entry.hint}>
          {entry.label}
        </span>,
      );
      continue;
    }
    nodes.push(
      <span key={`chip-${index}`} className={styles.chipWrap} title={entry.hint}>
        <Chip tone={chipToneOf(entry.variant)}>{entry.label}</Chip>
      </span>,
    );
  }
  return <>{nodes}</>;
}
