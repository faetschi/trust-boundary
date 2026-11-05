import { useEffect, useRef, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { GovernancePanels } from "./GovernancePanels";
import { RecordInspector } from "./inspector/RecordInspector";
import { nextTabIndex, PanelTabs, stripTabId, type StripTab } from "./inspector/PanelTabs";
import { chipToneOf, panelBadge, warningBadge, worstToneOf, type PanelBadge } from "./inspector/panelBadge";
import { StateDot } from "../ui/StateDot";
import { DecisionsIcon, ProvenanceIcon, PublicationIcon, QualificationIcon, RecoveryIcon } from "../ui/Icon";
import { isPanelKey, PANEL_KEYS, type PanelKey } from "./uiPrefs";
import type { WorkspaceModel } from "./workspaceStore";
import styles from "./Inspector.module.css";

export const INSPECTOR_ID = "governance-inspector";

// Names the panel while a record is shown: no governance tab is selected then, so the record bar's label stands in for one.
const RECORD_LABEL_ID = "governance-record-label";

const PANEL_LABELS: Readonly<Record<PanelKey, string>> = {
  decisions: "Decisions",
  provenance: "Provenance",
  publication: "Publication",
  recovery: "Recovery",
  qualification: "Qualification",
};

// The mark on each panel: on the collapsed rail it is the whole control (the button's name carries the words), and on the expanded
// list it sits beside the label.
const PANEL_ICONS: Readonly<Record<PanelKey, ReactNode>> = {
  decisions: <DecisionsIcon />,
  provenance: <ProvenanceIcon />,
  publication: <PublicationIcon />,
  recovery: <RecoveryIcon />,
  qualification: <QualificationIcon />,
};

// The badges of one governance tab. The qualification tab also counts the warnings.
function badgesOf(key: PanelKey, model: WorkspaceModel): PanelBadge[] {
  const badge = panelBadge(key, model);
  const warnings = key === "qualification" ? warningBadge(model) : null;
  return warnings ? [badge, warnings] : [badge];
}

// A tab's words in one line, for the status line under the strip: its label, then its badges.
function wordsOf(tab: StripTab): string {
  return [tab.label, ...tab.badges.map((badge) => badge.text)].join(" · ");
}

interface InspectorProps {
  readonly model: WorkspaceModel;
  // The viewport is at or below the drawer breakpoint.
  readonly narrow: boolean;
  // The drawer is open. Always false at wide widths.
  readonly open: boolean;
  // Focus goes here when the drawer opens (the focus contract in shellContext.ts).
  readonly headingRef: RefObject<HTMLHeadingElement | null>;
  // The wide column is collapsed to the icon rail. Never true at narrow widths.
  readonly collapsed: boolean;
  // The selected governance panel (the persisted choice). It is the tab stop while a record is shown.
  readonly panel: PanelKey;
  // The record is shown in the panel body, in place of the governance panel. A new selection sets it; a governance tab clears it.
  readonly recordTab: boolean;
  // Closes the drawer (narrow widths).
  readonly onClose: () => void;
  // Collapses or expands the column (wide widths).
  readonly onToggleCollapsed: () => void;
  // The icon rail: expands the column and selects the panel.
  readonly onExpandTo: (panel: PanelKey) => void;
  // Selects a governance panel. The shell clears the record, so the panel is shown.
  readonly onSelectPanel: (panel: PanelKey) => void;
  // Clears the selected record.
  readonly onCloseRecord: () => void;
}

// The governance inspector. At wide widths it is a column that collapses to an icon rail. At narrow widths it is an
// off-canvas drawer, and while it is closed it is inert and hidden from assistive technology, so focus cannot reach its
// content. One panel is rendered at a time, under a tab strip of the five governance tabs. A selected record is shown in
// the panel body, under a bar that names it and closes it; the five tabs stay visible and clickable beside it.
export function Inspector({
  model,
  narrow,
  open,
  headingRef,
  collapsed,
  panel,
  recordTab,
  onClose,
  onToggleCollapsed,
  onExpandTo,
  onSelectPanel,
  onCloseRecord,
}: InspectorProps) {
  const closed = narrow && !open;
  const record = model.selectedId !== null ? model.itemsById.get(model.selectedId) : undefined;
  const recordShown = record !== undefined && recordTab;
  const asideRef = useRef<HTMLElement | null>(null);
  // The panel an icon asked for. Its tab takes focus once the column has expanded.
  const pendingPanel = useRef<PanelKey | null>(null);
  const hadRecord = useRef(record !== undefined);

  // When the record goes while the inspector is shown, focus that was in it (or lost with it) moves to the selected
  // governance tab. A closed drawer has no focus to keep: its opener takes focus (the shell's return rule).
  useEffect(() => {
    const had = hadRecord.current;
    const has = record !== undefined;
    hadRecord.current = has;
    if (!had || has || (narrow && !open)) return;
    const active = document.activeElement;
    if (active === null || active === document.body || (asideRef.current?.contains(active) ?? false)) {
      document.getElementById(stripTabId(panel))?.focus();
    }
  }, [record, narrow, open, panel]);

  useEffect(() => {
    const key = pendingPanel.current;
    if (key === null || collapsed) return;
    pendingPanel.current = null;
    document.getElementById(stripTabId(key))?.focus();
  }, [collapsed]);

  function expandTo(key: PanelKey) {
    pendingPanel.current = key;
    onExpandTo(key);
  }

  function select(id: string) {
    if (isPanelKey(id)) onSelectPanel(id);
  }

  if (collapsed && !narrow) {
    return (
      <aside
        id={INSPECTOR_ID}
        ref={asideRef}
        aria-label="Governance inspector"
        className={styles.inspector}
        data-open={open}
        data-collapsed="true"
      >
        <button
          type="button"
          className={styles.collapse}
          aria-expanded="false"
          aria-controls={INSPECTOR_ID}
          aria-label="Expand governance panel"
          title="Expand governance panel"
          onClick={onToggleCollapsed}
        >
          <span aria-hidden="true">›</span>
        </button>
        <div role="toolbar" aria-orientation="vertical" aria-label="Governance panels (collapsed)" className={styles.iconRail} onKeyDown={onIconKey}>
          {PANEL_KEYS.map((key) => {
            const badges = badgesOf(key, model);
            const name = `Open ${PANEL_LABELS[key]} panel, ${badges.map((badge) => badge.text).join(", ")}`;
            return (
              <button
                key={key}
                type="button"
                className={styles.iconButton}
                aria-label={name}
                title={name}
                tabIndex={key === panel ? 0 : -1}
                data-selected={key === panel}
                onClick={() => expandTo(key)}
              >
                <span aria-hidden="true" className={styles.glyph}>
                  {PANEL_ICONS[key]}
                </span>
                <span className={styles.badgeMark}>
                  <StateDot tone={chipToneOf(worstToneOf(badges))} />
                </span>
              </button>
            );
          })}
        </div>
      </aside>
    );
  }

  const tabs: StripTab[] = PANEL_KEYS.map((key) => ({ id: key, label: PANEL_LABELS[key], badges: badgesOf(key, model), icon: PANEL_ICONS[key] }));
  const selectedTab = tabs.find((tab) => tab.id === panel);
  const status =
    record && recordShown ? (
      <div className={styles.recordBar}>
        <span id={RECORD_LABEL_ID} className={styles.recordLabel}>
          {`Record ${record.sequence}`}
        </span>
        <button type="button" className={styles.recordClose} aria-label={`Close record ${record.sequence}`} onClick={onCloseRecord}>
          Close record
        </button>
      </div>
    ) : selectedTab && selectedTab.badges.length > 1 ? (
      // The row draws the first badge's words; a tab with more than one badge says them all, here.
      <p className={styles.tabWords}>{wordsOf(selectedTab)}</p>
    ) : null;

  // At narrow widths the inspector is the drawer, and while it is open it is a modal dialog (the page behind it is inert).
  return (
    <aside
      id={INSPECTOR_ID}
      ref={asideRef}
      aria-label={recordShown ? "Record inspector" : "Governance inspector"}
      role={narrow ? "dialog" : undefined}
      aria-modal={narrow && open ? true : undefined}
      className={styles.inspector}
      data-open={open}
      data-collapsed="false"
      inert={closed}
      aria-hidden={closed || undefined}
    >
      <div className={styles.head}>
        <div className={styles.titleRow}>
          <h2 ref={headingRef} tabIndex={-1} className={styles.heading}>
            {recordShown ? "Record" : "Governance"}
          </h2>
          {recordShown ? null : <span className={styles.scope}>session-scoped</span>}
        </div>
        {narrow ? (
          <button type="button" className={styles.close} onClick={onClose}>
            Close governance
          </button>
        ) : (
          <button
            type="button"
            className={styles.collapse}
            aria-expanded="true"
            aria-controls={INSPECTOR_ID}
            aria-label="Collapse governance panel"
            title="Collapse governance panel"
            onClick={onToggleCollapsed}
          >
            <span aria-hidden="true">‹</span>
          </button>
        )}
      </div>
      <PanelTabs
        tabs={tabs}
        selected={recordShown ? null : panel}
        stop={panel}
        onSelect={select}
        status={status}
        labelledBy={recordShown ? RECORD_LABEL_ID : stripTabId(panel)}
      >
        {recordShown && record ? (
          <RecordInspector record={record} projection={model} />
        ) : (
          <GovernancePanels model={model} panel={panel} />
        )}
      </PanelTabs>
    </aside>
  );
}

// Arrow keys move focus around the icon rail, as in a vertical toolbar. They do not expand the column.
function onIconKey(event: KeyboardEvent<HTMLDivElement>) {
  const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button"));
  const current = buttons.indexOf(event.target as HTMLButtonElement);
  if (current < 0) return;
  const key = event.key === "ArrowDown" ? "ArrowRight" : event.key === "ArrowUp" ? "ArrowLeft" : event.key;
  const next = nextTabIndex(key, current, buttons.length);
  if (next === null) return;
  event.preventDefault();
  buttons[next]?.focus();
}
