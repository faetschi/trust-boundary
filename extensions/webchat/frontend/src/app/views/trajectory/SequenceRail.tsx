import { useMemo } from "react";
import type { ItemView } from "../../../core/contract";
import { weightClass } from "../../../core/trajectory";
import type { ProjectionState } from "../../../core/verification";
import { cx } from "../../../ui/cx";
import type { ProjectionPhase } from "../../status";
import { railLanes, railToneText } from "./rail";
import styles from "./Trajectory.module.css";

interface SequenceRailProps {
  // Every retained record, as the shared gate judges it. The rail is never built from the search result.
  readonly items: readonly ItemView[];
  readonly phase: ProjectionPhase;
  readonly searching: boolean;
  // The projection the records are judged in. The rail draws only what that projection verifies.
  readonly projection: ProjectionState;
}

// The sequence rail: three lanes (Model, Policy, Tools) over every retained record. Its axis is the
// record sequence, not time, and the note says so. Each lane is an image whose name states the
// tone of each segment in words, because the segments are colour and shape only.
export function SequenceRail({ items, phase, searching, projection }: SequenceRailProps) {
  const rail = useMemo(() => railLanes(items, projection), [items, projection]);
  if (phase === "loading") return <p className={styles.railNote}>Loading projection…</p>;
  if (phase === "failed") return <p className={styles.railNote}>Projection not loaded; nothing to chart.</p>;
  if (items.length === 0) return <p className={styles.railNote}>No records to chart.</p>;
  const note = searching
    ? `${rail.note} The rail always shows all ${items.length} retained records; search only narrows the ledger.`
    : rail.note;
  return (
    <div role="group" aria-label="Record sequence rail" className={styles.rail}>
      {rail.lanes.map((lane) => (
        <div key={lane.id} className={styles.lane}>
          <span className={styles.laneLabel}>{lane.label}</span>
          <span
            role="img"
            className={styles.track}
            data-lane={lane.id}
            aria-label={`${lane.label} lane: ${lane.segments
              .map((segment) => `${segment.weight} record(s), ${railToneText(segment.tone)}`)
              .join("; ")}`}
          >
            {lane.segments.map((segment, index) => (
              <span
                key={index}
                className={cx(styles.seg, styles[weightClass(segment.weight)])}
                data-tone={segment.tone}
              />
            ))}
          </span>
        </div>
      ))}
      <p className={styles.axis} data-ordinal="true">
        {note}
      </p>
    </div>
  );
}
