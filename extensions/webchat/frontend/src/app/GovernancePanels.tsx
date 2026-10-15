import type { ReactNode } from "react";
import { Chip } from "../ui/Chip";
import { isVerifiedProjection } from "../core/format";
import {
  decisionSummary,
  governanceRows,
  POLICY_NOTE,
  provenanceRows,
  publicationReading,
  qualificationRows,
  recoveryReading,
  verdictCount,
  warningLines,
  type Reading,
  type StateReading,
} from "./panels";
import type { PanelKey } from "./uiPrefs";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Inspector.module.css";

// The session-scoped governance panels. Every value is the snapshot's own. An absent value reads "unavailable"
// with its reason, never zero and never empty. One panel is rendered at a time: the selected tab's.

function ReadingText({ reading }: { readonly reading: Reading }) {
  if (reading.kind === "value") return <>{reading.text}</>;
  return (
    <span className={styles.unavailable}>
      unavailable <span className={styles.reason}>({reading.reason})</span>
    </span>
  );
}

function Rows({ rows }: { readonly rows: ReadonlyArray<readonly [string, Reading]> }) {
  return (
    <dl className={styles.rows}>
      {rows.map(([label, reading]) => (
        <div key={label} className={styles.row}>
          <dt>{label}</dt>
          <dd>
            <ReadingText reading={reading} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

// A state with its chip. An unverified success shows the word "reported", and the trust sentence says that it was
// not independently verified.
function StateLine({ reading }: { readonly reading: StateReading }) {
  return (
    <div className={styles.stateLine}>
      <Chip tone={reading.tone}>{reading.label}</Chip>
      {reading.trust ? <p className={styles.trust}>{reading.trust}</p> : null}
    </div>
  );
}

function PanelSection({ id, title, children }: { readonly id: string; readonly title: string; readonly children: ReactNode }) {
  return (
    <section aria-labelledby={id} className={styles.panel}>
      <h3 id={id} className={styles.panelTitle}>
        {title}
      </h3>
      {children}
    </section>
  );
}

export function GovernancePanels({ model, panel }: { readonly model: WorkspaceModel; readonly panel: PanelKey }) {
  const verified = isVerifiedProjection(model);
  const { panels } = model;
  switch (panel) {
    case "decisions":
      return (
        <PanelSection id="panel-decisions" title="Policy decisions">
          <p className={styles.summary}>{decisionSummary(panels?.governance, verdictCount(model.items))}</p>
          <Rows rows={governanceRows(panels?.governance)} />
          <p className={styles.note}>{POLICY_NOTE}</p>
        </PanelSection>
      );
    case "provenance":
      return (
        <PanelSection id="panel-provenance" title="Provenance">
          <Rows rows={provenanceRows(panels?.provenance, verified)} />
        </PanelSection>
      );
    case "publication":
      return (
        <PanelSection id="panel-publication" title="Publication">
          <StateLine reading={publicationReading(panels?.publication, verified)} />
          {panels?.publication?.detail ? <p className={styles.note}>{panels.publication.detail}</p> : null}
        </PanelSection>
      );
    case "recovery":
      return (
        <PanelSection id="panel-recovery" title="Recovery">
          <StateLine reading={recoveryReading(panels?.recovery, verified)} />
          {panels?.recovery?.detail ? <p className={styles.note}>{panels.recovery.detail}</p> : null}
        </PanelSection>
      );
    case "qualification":
      return (
        <PanelSection id="panel-qualification" title="Qualification">
          <Rows rows={qualificationRows(panels?.qualification)} />
          <p className={styles.note}>These values are fixed for this build. No receipt can change them.</p>
          <section aria-labelledby="panel-warnings" className={styles.warningsSection}>
            <h4 id="panel-warnings" className={styles.warningsTitle}>
              Warnings
            </h4>
            <ul className={styles.warnings}>
              {warningLines(model.warnings, model.loaded).map((line) => (
                <li key={line}>{line}</li>
              ))}
            </ul>
          </section>
        </PanelSection>
      );
  }
}
