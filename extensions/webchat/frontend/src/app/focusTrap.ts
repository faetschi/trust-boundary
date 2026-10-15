// The focus trap of the narrow drawer, as two pure rules. While the drawer is open it is a modal dialog: Tab and Shift+Tab
// move between its tab stops, and wrap from the last stop to the first and back. The browser's own order handles every
// other move, so the trap only intervenes where Tab would leave the dialog.

// What takes focus from the keyboard by default, and is not taken out of the order: not disabled, and not tabindex -1
// (the strip's roving tabs, and the headings the shell focuses by script).
const STOP = ["a[href]", "button", "input", "select", "textarea", "summary", "[tabindex]"]
  .map((selector) => `${selector}:not([disabled]):not([tabindex='-1'])`)
  .join(", ");

// The tab stops inside a dialog, in document order. Anything inside a hidden or inert subtree is not a stop.
export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(STOP)).filter((element) => !element.closest("[hidden], [inert]"));
}

// Where Tab (or Shift+Tab) goes when it would leave the dialog: the first stop going forward, the last going back. Focus
// that is not on a stop (the body, or the dialog's heading, which is focusable but not in the order) is brought in at
// the end that the key starts from. Null means the browser keeps the key, and focus stays inside.
export function trapTarget(stops: readonly HTMLElement[], active: Element | null, backwards: boolean): HTMLElement | null {
  if (stops.length === 0) return null;
  const index = active === null ? -1 : stops.indexOf(active as HTMLElement);
  if (backwards) return index <= 0 ? (stops[stops.length - 1] ?? null) : null;
  return index === -1 || index === stops.length - 1 ? (stops[0] ?? null) : null;
}
