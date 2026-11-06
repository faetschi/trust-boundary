import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { StateDot } from "../../ui/StateDot";
import { chipToneOf, worstToneOf, type PanelBadge } from "./panelBadge";
import styles from "./PanelTabs.module.css";

// The governance tab strip and its one tab panel. WAI-ARIA tabs with a roving tab stop: only the tab stop is in the tab
// order. Left and Right wrap, Home and End jump to the ends, and the arrow keys select the tab they move to (automatic
// activation). Each tab is one line: its label and a status dot. The dot is a shape as well as a colour (StateDot), so
// the tone does not rely on colour alone. The badge words are in the tab's name and title, and the status line beneath
// the strip carries the selected tab's words. When the tabs do not fit, the ‹ and › controls step the strip, for a
// pointer; they are hidden from assistive technology, because the arrow keys already do the same job.

export interface StripTab {
  readonly id: string;
  readonly label: string;
  readonly badges: readonly PanelBadge[];
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
      return (current + 1) % count;
    case "ArrowLeft":
      return (current - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
}

// How far the strip can scroll: whether its tabs overflow it, and whether it is at either end.
interface Reach {
  readonly overflow: boolean;
  readonly atStart: boolean;
  readonly atEnd: boolean;
}

const NO_OVERFLOW: Reach = { overflow: false, atStart: true, atEnd: true };

function reachOf(element: HTMLElement): Reach {
  const max = element.scrollWidth - element.clientWidth;
  return { overflow: max > 1, atStart: element.scrollLeft <= 1, atEnd: element.scrollLeft >= max - 1 };
}

function sameReach(a: Reach, b: Reach): boolean {
  return a.overflow === b.overflow && a.atStart === b.atStart && a.atEnd === b.atEnd;
}

// A step is most of the visible width, so the tab at the edge is still in view on the far side of the step.
const STEP_SHARE = 0.8;
const MIN_STEP_PX = 48;

interface PanelTabsProps {
  readonly tabs: readonly StripTab[];
  // The selected tab (aria-selected). Null while the panel shows something that is not a tab, such as a record.
  readonly selected: string | null;
  // The tab in the tab order. It is a tab whether or not it is selected, so the keyboard always reaches the strip.
  readonly stop: string;
  readonly onSelect: (id: string) => void;
  // The line beneath the strip, or null for none.
  readonly status: ReactNode;
  // The id of the element that names the panel.
  readonly labelledBy: string;
  readonly children: ReactNode;
}

export function PanelTabs({ tabs, selected, stop, onSelect, status, labelledBy, children }: PanelTabsProps) {
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  const strip = useRef<HTMLDivElement | null>(null);
  const [reach, setReach] = useState<Reach>(NO_OVERFLOW);

  // Reads the strip's reach, and keeps the state only when it changed, so a measure after a render does not render again.
  const measure = useCallback(() => {
    const element = strip.current;
    if (!element) return;
    const next = reachOf(element);
    setReach((previous) => (sameReach(previous, next) ? previous : next));
  }, []);

  // The reach changes with the tabs, the badges and the window, so it is read after every render, and on each scroll and resize.
  useLayoutEffect(() => {
    measure();
  });

  useEffect(() => {
    const element = strip.current;
    const observer = element && typeof ResizeObserver === "function" ? new ResizeObserver(measure) : null;
    element?.addEventListener("scroll", measure, { passive: true });
    window.addEventListener("resize", measure);
    if (element) observer?.observe(element);
    return () => {
      element?.removeEventListener("scroll", measure);
      window.removeEventListener("resize", measure);
      observer?.disconnect();
    };
  }, [measure]);

  // Keeps the selected tab in view within the strip. Layout-less test environments have no scrollIntoView.
  useEffect(() => {
    if (selected === null) return;
    const element = buttons.current.get(selected);
    if (element && typeof element.scrollIntoView === "function") {
      element.scrollIntoView({ inline: "nearest", block: "nearest" });
    }
  }, [selected]);

  function step(direction: 1 | -1) {
    const element = strip.current;
    if (!element) return;
    element.scrollLeft += direction * Math.max(MIN_STEP_PX, Math.round(element.clientWidth * STEP_SHARE));
    measure();
  }

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
      <div className={styles.row}>
        <button
          type="button"
          tabIndex={-1}
          aria-hidden="true"
          title="Scroll the governance tabs back"
          className={styles.step}
          hidden={!reach.overflow}
          disabled={reach.atStart}
          onClick={() => step(-1)}
        >
          ‹
        </button>
        <div ref={strip} className={styles.strip}>
          <div role="tablist" aria-label="Governance panels" className={styles.tablist} onKeyDown={onKeyDown}>
            {tabs.map((tab) => {
              const name = tabName(tab);
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
                  <span>{tab.label}</span>
                  <StateDot tone={chipToneOf(worstToneOf(tab.badges))} />
                </button>
              );
            })}
          </div>
        </div>
        <button
          type="button"
          tabIndex={-1}
          aria-hidden="true"
          title="Scroll the governance tabs forward"
          className={styles.step}
          hidden={!reach.overflow}
          disabled={reach.atEnd}
          onClick={() => step(1)}
        >
          ›
        </button>
      </div>
      {status !== null ? <div className={styles.status}>{status}</div> : null}
      <div id={PANEL_ID} role="tabpanel" aria-labelledby={labelledBy} tabIndex={-1} className={styles.panel}>
        {children}
      </div>
    </>
  );
}
