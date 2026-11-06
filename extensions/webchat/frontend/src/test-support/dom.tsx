// DOM helpers for the component tests (happy-dom). Test-only: non-test code must not import
// this folder (check-sinks.mjs enforces that). Keyboard and click events go through the real
// DOM dispatch, so React's event delegation is exercised as the browser exercises it.
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

export interface Mounted {
  readonly container: HTMLElement;
  readonly root: Root;
  unmount(): void;
}

export function mount(ui: ReactElement): Mounted {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(ui);
  });
  return {
    container,
    root,
    unmount() {
      act(() => {
        root.unmount();
      });
      container.remove();
    },
  };
}

export function press(target: Element, key: string, init: KeyboardEventInit = {}): void {
  act(() => {
    target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
  });
}

export function click(target: Element): void {
  act(() => {
    (target as HTMLElement).click();
  });
}

// Lets pending promises and their state updates run.
export async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

export interface MatchMediaShim {
  setWidth(width: number): void;
  restore(): void;
}

// Replaces window.matchMedia with a width-only query for the drawer breakpoint. The shim
// answers "(max-width: Npx)" and fires `change` listeners when the width moves.
export function installMatchMedia(initialWidth: number): MatchMediaShim {
  const original = window.matchMedia;
  let width = initialWidth;
  const listeners = new Set<(event: { matches: boolean }) => void>();
  const matches = (query: string): boolean => {
    const match = /\(max-width:\s*(\d+)px\)/.exec(query);
    return match ? width <= Number(match[1]) : false;
  };
  window.matchMedia = (query: string) => {
    const list = {
      media: query,
      get matches() {
        return matches(query);
      },
      addEventListener(_type: string, listener: (event: { matches: boolean }) => void) {
        listeners.add(listener);
      },
      removeEventListener(_type: string, listener: (event: { matches: boolean }) => void) {
        listeners.delete(listener);
      },
    };
    return list as unknown as MediaQueryList;
  };
  return {
    setWidth(next: number) {
      width = next;
      act(() => {
        for (const listener of listeners) listener({ matches: width <= 1180 });
      });
    },
    restore() {
      window.matchMedia = original;
    },
  };
}
