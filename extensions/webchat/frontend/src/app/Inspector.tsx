import type { RefObject } from "react";
import { GovernancePanels } from "./GovernancePanels";
import { RecordInspector } from "./inspector/RecordInspector";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Inspector.module.css";

export const INSPECTOR_ID = "governance-inspector";

interface InspectorProps {
  readonly model: WorkspaceModel;
  // The viewport is at or below the drawer breakpoint.
  readonly narrow: boolean;
  // The drawer is open. Always false at wide widths.
  readonly open: boolean;
  // Focus goes here when the drawer opens (the focus contract in shellContext.ts).
  readonly headingRef: RefObject<HTMLHeadingElement | null>;
  // Closes the selected record when there is one, otherwise the drawer.
  readonly onClose: () => void;
}

// The right-hand inspector. At wide widths it is a column. At narrow widths it is an
// off-canvas drawer, and while it is closed it is inert and hidden from assistive technology,
// so focus cannot reach its content.
export function Inspector({ model, narrow, open, headingRef, onClose }: InspectorProps) {
  const closed = narrow && !open;
  const record = model.selectedId !== null ? model.itemsById.get(model.selectedId) : undefined;
  return (
    <aside
      id={INSPECTOR_ID}
      aria-label="Governance inspector"
      className={styles.inspector}
      data-open={open}
      inert={closed}
      aria-hidden={closed || undefined}
    >
      <div className={styles.head}>
        <h2 ref={headingRef} tabIndex={-1} className={styles.heading}>
          {record ? "Record" : "Governance"}
        </h2>
        {record || narrow ? (
          <button type="button" className={styles.close} onClick={onClose}>
            {record ? "Close record" : "Close governance"}
          </button>
        ) : null}
      </div>
      {record ? <RecordInspector record={record} /> : <GovernancePanels model={model} />}
    </aside>
  );
}
