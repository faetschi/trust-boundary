import { useRef, useState, type KeyboardEvent } from "react";
import styles from "./ToggleGroup.module.css";

export interface ToggleItem {
  readonly value: string;
  readonly label: string;
  // Shown after the label and included in the button's accessible name.
  readonly count?: number;
}

interface ToggleGroupProps {
  readonly label: string;
  readonly items: readonly ToggleItem[];
  // The pressed value (aria-pressed). Only a press changes it.
  readonly pressed: string;
  readonly onPress: (value: string) => void;
}

// A labelled toolbar of toggle buttons with one tab stop. Left and Right (and Home and
// End) move focus between buttons and never change aria-pressed. Enter and Space press
// the focused button, which is the native button behaviour. The tab stop is the last
// focused button, or the pressed one at first.
export function ToggleGroup({ label, items, pressed, onPress }: ToggleGroupProps) {
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  const [lastFocused, setLastFocused] = useState<string | null>(null);
  const stop = lastFocused !== null && items.some((item) => item.value === lastFocused) ? lastFocused : pressed;

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const current = items.findIndex((item) => buttons.current.get(item.value) === event.target);
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
    if (target) buttons.current.get(target.value)?.focus();
  }

  return (
    <div role="toolbar" aria-label={label} className={styles.group} onKeyDown={onKeyDown}>
      {items.map((item) => (
        <button
          key={item.value}
          ref={(element) => {
            if (element) buttons.current.set(item.value, element);
            else buttons.current.delete(item.value);
          }}
          type="button"
          aria-pressed={item.value === pressed}
          tabIndex={item.value === stop ? 0 : -1}
          className={styles.toggle}
          onFocus={() => setLastFocused(item.value)}
          onClick={() => {
            setLastFocused(item.value);
            onPress(item.value);
          }}
        >
          {item.label}
          {item.count !== undefined ? <span className={styles.count}> {item.count}</span> : null}
        </button>
      ))}
    </div>
  );
}
