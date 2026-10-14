import { useId } from "react";
import type { Item } from "../../core/contract";
import { shortDigest } from "../../core/format";
import {
  recordOf,
  sourceLabel,
  statusText,
  type DetailRow,
  type DetailRowOptions,
  type DetailSection,
  type RecordView,
} from "../../core/trajectory";
import { Chip } from "../../ui/Chip";
import { chipToneOf, sourceRole, stateWord } from "./recordChip";
import styles from "./RecordInspector.module.css";

// The record inspector body: the bounded fields of one projected record. The shell owns the aside,
// its heading and the Close control (shellContext.ts). Every value is the projection's own, and
// the sections are the core's (recordDetail), so the Timing section is always there.
export function RecordInspector({ record }: { readonly record: Item }) {
  const view = recordOf(record);
  const position = `sequence ${view.sequence}${record.turn_id ? ` · turn ${record.turn_id}` : ""}`;
  return (
    <div className={styles.body}>
      <div className={styles.head}>
        <Chip source={sourceRole(view.source)}>{sourceLabel(view.source)}</Chip>
        <span className={styles.position}>{position}</span>
      </div>
      <p className={styles.status}>{statusText(record)}</p>
      {view.detail.map((section) => (
        <Section key={section.id} section={section} view={view} />
      ))}
    </div>
  );
}

function Section({ section, view }: { readonly section: DetailSection; readonly view: RecordView }) {
  const titleId = useId();
  return (
    <section aria-labelledby={titleId} className={styles.section} data-section={section.id}>
      <h3 id={titleId} className={styles.title}>
        {section.title}
      </h3>
      <dl className={styles.rows}>
        {section.rows.map((row, index) => (
          <div key={`${row[0]}-${index}`} className={styles.row}>
            <dt>{row[0]}</dt>
            <dd>
              <Value row={row} section={section} view={view} />
            </dd>
          </div>
        ))}
      </dl>
      {section.note ? <p className={styles.note}>{section.note}</p> : null}
    </section>
  );
}

// One value cell. The row's options say how to draw it; the state row is drawn from the same
// tone as the ledger's dot, so the inspector and the ledger cannot disagree about a record.
function Value({
  row,
  section,
  view,
}: {
  readonly row: DetailRow;
  readonly section: DetailSection;
  readonly view: RecordView;
}) {
  const [label, raw = "—"] = row;
  const options: DetailRowOptions = row[2] ?? {};
  if (section.id === "result" && label === "state") {
    return <Chip tone={view.dot}>{stateWord(view.item, view.dot)}</Chip>;
  }
  if (options.chip) return <Chip tone={chipToneOf(options.chip)}>{raw}</Chip>;
  if (options.digest) {
    if (raw === "—") return <>{raw}</>;
    return (
      <span className={styles.mono} title={raw}>
        {shortDigest(raw)}
      </span>
    );
  }
  if (options.mono) return <span className={styles.mono}>{raw}</span>;
  return <span className={styles.text}>{raw}</span>;
}
