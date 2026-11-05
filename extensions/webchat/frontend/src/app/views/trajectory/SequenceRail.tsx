import { useMemo } from "react";
import type { ItemView } from "../../../core/contract";
import { weightClass } from "../../../core/trajectory";
import type { ProjectionState } from "../../../core/verification";
import { cx } from "../../../ui/cx";
import { Disclosure } from "../../../ui/Disclosure";
import type { ProjectionPhase } from "../../status";
import { railLanes, railLegend, railToneText } from "./rail";
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
  const { source, verification } = projection;
  const rail = useMemo(() => railLanes(items, { source, verification }), [items, source, verification]);
  if (phase === "loading") return <p className={styles.railNote}>Loading projection…</p>;
  if (phase === "failed") return <p className={styles.railNote}>Projection not loaded; nothing to chart.</p>;
  if (items.length === 0) return <p className={styles.railNote}>No records to chart.</p>;
  const note = searching
    ? `${rail.note} The rail always shows all ${items.length} retained records; search only narrows the ledger.`
    : rail.note;
  const legend = railLegend(rail);
  return (
    <div role="group" aria-label="Record sequence rail" className={styles.rail}>
      {rail.lanes.map((lane) => (
        <div key={lane.id} className={styles.lane}>
          <span className={styles.laneLabel} data-kind={lane.id}>
            {lane.label}
          </span>
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
      {/* The key: colour is the kind of record (the same colours as the kind chips in the ledger), shape is the state of a record. */}
      <div role="group" aria-label="Key to the rail" className={styles.legend}>
        <ul className={styles.legendRow}>
          <li className={styles.legendTitle}>Colour</li>
          {legend.kinds.map((kind) => (
            <li key={kind.lane} className={styles.legendItem}>
              <span className={styles.swatch} data-kind={kind.lane} aria-hidden="true" />
              {`${kind.label}: ${kind.words}`}
            </li>
          ))}
        </ul>
        <ul className={styles.legendRow}>
          <li className={styles.legendTitle}>Shape</li>
          {legend.states.map((state) => (
            <li key={state.tone} className={styles.legendItem}>
              <span className={cx(styles.seg, styles.swatch)} data-tone={state.tone} aria-hidden="true" />
              {state.words}
            </li>
          ))}
        </ul>
      </div>      {/* The note says the axis is the record sequence, not time. It folds under a quiet summary, and it is in the page. */}
      <Disclosure summary="About this axis">
        <p className={styles.axis} data-ordinal="true">
          {note}
        </p>
      </Disclosure>
    </div>
  );
}
