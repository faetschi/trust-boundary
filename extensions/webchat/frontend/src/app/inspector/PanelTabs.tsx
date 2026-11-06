import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { StateDot } from "../../ui/StateDot";
import { chipToneOf, worstBadgeOf, worstToneOf, type PanelBadge } from "./panelBadge";
import styles from "./PanelTabs.module.css";

// The governance panels and the one panel that is shown. A vertical WAI-ARIA tab list with a roving tab stop: only the tab stop is
// in the tab order. Up and Left go to the previous tab, Down and Right to the next (both wrap), Home and End jump to the ends, and
// the arrow keys select the tab they move to (automatic activation). Each tab is one row: its mark, its label, the words of its
// worst badge, and a status dot. The dot is a shape as well as a colour (StateDot), so the tone does not rely on colour alone, and
// the badge words are drawn in the row and are in the tab's name and title.

export interface StripTab {
  readonly id: string;
  readonly label: string;
  readonly badges: readonly PanelBadge[];
  // The tab's mark. Decorative: the label beside it names the tab.
  readonly icon?: ReactNode;
}

const PANEL_ID = "governance-panel";

export function stripTabId(id: string): string {
  return `governance-tab-${id}`;
}

// The accessible name and the title of a tab: its label, then the words of each badge, so a badge is part of the name.
function tabName(tab: StripTab): string {
  return [tab.label, ...tab.badges.map((badge) => badge.text)].join(", ");
}

// The index that a key moves to, or null when the key does not move between tabs.
export function nextTabIndex(key: string, current: number, count: number): number | null {
  switch (key) {
    case "ArrowRight":
    case "ArrowDown":
      return (current + 1) % count;
    case "ArrowLeft":
    case "ArrowUp":
      return (current - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
}

interface PanelTabsProps {
  readonly tabs: readonly StripTab[];
  // The selected tab (aria-selected). Null while the panel shows something that is not a tab, such as a record.
  readonly selected: string | null;
  // The tab in the tab order. It is a tab whether or not it is selected, so the keyboard always reaches the list.
  readonly stop: string;
  readonly onSelect: (id: string) => void;
  // The line beneath the list, or null for none.
  readonly status: ReactNode;
  // The id of the element that names the panel.
  readonly labelledBy: string;
  readonly children: ReactNode;
}

export function PanelTabs({ tabs, selected, stop, onSelect, status, labelledBy, children }: PanelTabsProps) {
  const buttons = useRef(new Map<string, HTMLButtonElement>());

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const current = tabs.findIndex((tab) => buttons.current.get(tab.id) === event.target);
    if (current < 0) return;
    const next = nextTabIndex(event.key, current, tabs.length);
    const target = next === null ? undefined : tabs[next];
    if (!target) return;
    event.preventDefault();
    buttons.current.get(target.id)?.focus();
    onSelect(target.id);
  }

  return (
    <>
      <div role="tablist" aria-label="Governance panels" aria-orientation="vertical" className={styles.tablist} onKeyDown={onKeyDown}>
        {tabs.map((tab) => {
          const name = tabName(tab);
          const words = worstBadgeOf(tab.badges)?.text ?? "";
          return (
            <button
              key={tab.id}
              ref={(element) => {
                if (element) buttons.current.set(tab.id, element);
                else buttons.current.delete(tab.id);
              }}
              id={stripTabId(tab.id)}
              type="button"
              role="tab"
              aria-selected={tab.id === selected}
              aria-controls={PANEL_ID}
              aria-label={name}
              title={name}
              tabIndex={tab.id === stop ? 0 : -1}
              className={styles.tab}
              onClick={() => onSelect(tab.id)}
            >
              <span className={styles.mark} aria-hidden="true">
                {tab.icon}
              </span>
              <span className={styles.label}>{tab.label}</span>
              <span className={styles.words} aria-hidden="true">
                {words}
              </span>
              <StateDot tone={chipToneOf(worstToneOf(tab.badges))} />
            </button>
          );
        })}
      </div>
      {status !== null ? <div className={styles.status}>{status}</div> : null}
      <div id={PANEL_ID} role="tabpanel" aria-labelledby={labelledBy} tabIndex={-1} className={styles.panel}>
        {children}
      </div>
    </>
  );
}
