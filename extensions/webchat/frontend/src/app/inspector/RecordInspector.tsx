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
import { isVerifiedIn, type ProjectionState } from "../verification";
import { chipToneOf, sourceRole, stateWord } from "./recordChip";
import styles from "./RecordInspector.module.css";

// The record inspector body: the bounded fields of one projected record. The shell owns the aside,
// its heading and the Close control (shellContext.ts). Every value is the projection's own, and
// the sections are the core's (recordDetail), so the Timing section is always there. Verification is
// judged by the shared gate (verification.ts); the Evidence section states the record's own evidence.
export function RecordInspector({ record, projection }: { readonly record: Item; readonly projection: ProjectionState }) {
  const view = recordOf(record, projection);
  const position = `sequence ${view.sequence}${record.turn_id ? ` · turn ${record.turn_id}` : ""}`;
  const sections = view.detail.map((section) => (section.id === "evidence" ? evidenceSection(record, projection) : section));
  return (
    <div className={styles.body}>
      <div className={styles.head}>
        <Chip source={sourceRole(view.source)}>{sourceLabel(view.source)}</Chip>
        <span className={styles.position}>{position}</span>
      </div>
      <p className={styles.status}>{statusText(record, projection)}</p>
      {sections.map((section) => (
        <Section key={section.id} section={section} view={view} />
      ))}
    </div>
  );
}

// The Evidence rows, read from the record: its authority and scope as it carried them, and whether the
// shared gate verified it. A record that lacks either field is unavailable, never no.
function evidenceSection(record: Item, projection: ProjectionState): DetailSection {
  const declared = record.authority !== undefined && record.verification_scope !== undefined;
  const verdict = isVerifiedIn(projection, record) ? "yes" : "no";
  return {
    id: "evidence",
    title: "Evidence",
    rows: [
      ["authority", record.authority ?? "unavailable", { mono: true }],
      ["verification", record.verification_scope ?? "unavailable", { mono: true }],
      ["independently verified", declared ? verdict : "unavailable — the record carries no authority or verification scope"],
    ],
  };
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
      <span className={styles.digest} title={raw}>
        {shortDigest(raw)}
      </span>
    );
  }
  if (options.mono) return <span className={styles.mono}>{raw}</span>;
  return <span className={styles.text}>{raw}</span>;
}
