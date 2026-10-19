import { useRef, type KeyboardEvent, type ReactNode } from "react";
import styles from "./Tabs.module.css";

export interface TabItem {
  readonly id: string;
  readonly label: string;
}

interface TabsProps {
  // The accessible name of the tablist.
  readonly label: string;
  // Element ids are `${idPrefix}-tab-${id}` and `${idPrefix}-panel-${id}`.
  readonly idPrefix: string;
  readonly items: readonly TabItem[];
  readonly value: string;
  readonly onValueChange: (id: string) => void;
}

export function tabId(idPrefix: string, id: string): string {
  return `${idPrefix}-tab-${id}`;
}

export function panelId(idPrefix: string, id: string): string {
  return `${idPrefix}-panel-${id}`;
}

// WAI-ARIA tabs with a roving tab stop: only the selected tab is in the tab order.
// Left and Right wrap, Home and End jump to the ends. Arrow keys select the tab they move
// to (automatic activation), because the panels are cheap to switch.
export function Tabs({ label, idPrefix, items, value, onValueChange }: TabsProps) {
  const buttons = useRef(new Map<string, HTMLButtonElement>());

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const current = items.findIndex((item) => buttons.current.get(item.id) === event.target);
    if (current < 0) return;
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = (current + 1) % items.length;
        break;
      case "ArrowLeft":
        next = (current - 1 + items.length) % items.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    const target = items[next];
    if (!target) return;
    buttons.current.get(target.id)?.focus();
    onValueChange(target.id);
  }

  return (
    <div role="tablist" aria-label={label} className={styles.tablist} onKeyDown={onKeyDown}>
      {items.map((item) => {
        const selected = item.id === value;
        return (
          <button
            key={item.id}
            ref={(element) => {
              if (element) buttons.current.set(item.id, element);
              else buttons.current.delete(item.id);
            }}
            id={tabId(idPrefix, item.id)}
            type="button"
            role="tab"
            aria-selected={selected}
            aria-controls={panelId(idPrefix, item.id)}
            tabIndex={selected ? 0 : -1}
            className={styles.tab}
            onClick={() => onValueChange(item.id)}
          >
            {item.label}
          </button>
        );
      })}
    </div>
  );
}

interface TabPanelProps {
  readonly idPrefix: string;
  readonly id: string;
  readonly value: string;
  readonly children: ReactNode;
}

// A tab panel. Only the selected panel mounts its children; the others stay in the DOM,
// hidden, so that aria-controls resolves and the skip link can name the visible view.
// The panel is focusable so that the skip link can move focus into it.
export function TabPanel({ idPrefix, id, value, children }: TabPanelProps) {
  const selected = id === value;
  return (
    <div
      id={panelId(idPrefix, id)}
      role="tabpanel"
      aria-labelledby={tabId(idPrefix, id)}
      tabIndex={-1}
      hidden={!selected}
      className={styles.panel}
    >
      {selected ? children : null}
    </div>
  );
}
