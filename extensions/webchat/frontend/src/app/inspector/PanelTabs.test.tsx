// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { click, mount, type Mounted } from "../../test-support/dom";
import { PanelTabs, type StripTab } from "./PanelTabs";

const TABS: StripTab[] = [
  { id: "decisions", label: "Decisions", badges: [{ text: "1 deny verdict", tone: "danger" }] },
  { id: "provenance", label: "Provenance", badges: [{ text: "unavailable", tone: "neutral" }] },
  {
    id: "qualification",
    label: "Qualification",
    badges: [
      { text: "not established", tone: "neutral" },
      { text: "2 warnings", tone: "warn" },
    ],
  },
];

let view: Mounted | null = null;
let scrollIntoView: ReturnType<typeof vi.fn>;
const original = Element.prototype.scrollIntoView;

interface ShowOptions {
  readonly selected?: string | null;
  readonly stop?: string;
  readonly status?: ReactNode;
}

// The strip, as the inspector shows it. Mounting runs the strip's effects, which keep the selected tab in view.
function show({ selected = "decisions", stop = "decisions", status = null }: ShowOptions = {}): void {
  view = mount(
    <PanelTabs
      tabs={TABS}
      selected={selected}
      stop={stop}
      onSelect={() => undefined}
      status={status}
      labelledBy="governance-tab-decisions"
    >
      <p>panel</p>
    </PanelTabs>,
  );
}

function tabsOf(): HTMLElement[] {
  return Array.from(view?.container.querySelectorAll<HTMLElement>("[role='tab']") ?? []);
}

function tablist(): HTMLElement {
  const list = view?.container.querySelector<HTMLElement>("[role='tablist']");
  if (!list?.parentElement) throw new Error("no tablist");
  return list.parentElement;
}

// The two scroll controls, back then forward. They are the only buttons that are aria-hidden.
function controls(): HTMLButtonElement[] {
  return Array.from(view?.container.querySelectorAll<HTMLButtonElement>("button[aria-hidden='true']") ?? []);
}

// Gives the strip a layout: a scrollable width, a visible width, and a scroll position that the browser would clamp.
function laidOut(scrollWidth: number, clientWidth: number): HTMLElement {
  const strip = tablist();
  let left = 0;
  Object.defineProperty(strip, "scrollWidth", { configurable: true, get: () => scrollWidth });
  Object.defineProperty(strip, "clientWidth", { configurable: true, get: () => clientWidth });
  Object.defineProperty(strip, "scrollLeft", {
    configurable: true,
    get: () => left,
    set: (value: number) => {
      left = Math.max(0, Math.min(value, scrollWidth - clientWidth));
    },
  });
  act(() => {
    strip.dispatchEvent(new Event("scroll"));
  });
  return strip;
}

beforeEach(() => {
  scrollIntoView = vi.fn();
  Element.prototype.scrollIntoView = scrollIntoView as unknown as typeof Element.prototype.scrollIntoView;
});

afterEach(() => {
  view?.unmount();
  view = null;
  Element.prototype.scrollIntoView = original;
});

describe("PanelTabs: five single-line tabs", () => {
  it("renders one tab per governance tab, in order, and no tab for anything else", () => {
    show();
    expect(tabsOf().map((tab) => tab.textContent)).toEqual(["Decisions", "Provenance", "Qualification"]);
  });

  it("gives each tab a status dot of the worst tone of its badges, drawn as a shape as well as a colour", () => {
    show();
    // StateDot draws deny as a square, unknown as a dotted ring and idle as a solid dot: three shapes, and never the success tone.
    const dots = tabsOf().map((tab) => tab.querySelector("[data-tone]"));
    expect(dots.map((dot) => dot?.getAttribute("data-tone"))).toEqual(["deny", "idle", "unknown"]);
    expect(dots.every((dot) => dot?.getAttribute("aria-hidden") === "true")).toBe(true);
    expect(dots.map((dot) => dot?.textContent)).toEqual(["", "", ""]);
  });

  it("keeps each tab's full words in its accessible name and in its title", () => {
    show();
    const names = ["Decisions, 1 deny verdict", "Provenance, unavailable", "Qualification, not established, 2 warnings"];
    expect(tabsOf().map((tab) => tab.getAttribute("aria-label"))).toEqual(names);
    expect(tabsOf().map((tab) => tab.getAttribute("title"))).toEqual(names);
  });

  it("marks the selected tab, and no tab while the panel shows a record (selected is null)", () => {
    show({ selected: "provenance" });
    expect(tabsOf().map((tab) => tab.getAttribute("aria-selected"))).toEqual(["false", "true", "false"]);
    view?.unmount();
    show({ selected: null });
    expect(tabsOf().map((tab) => tab.getAttribute("aria-selected"))).toEqual(["false", "false", "false"]);
  });

  it("keeps the tab stop on the given tab, selected or not", () => {
    show({ selected: null, stop: "qualification" });
    expect(tabsOf().map((tab) => tab.tabIndex)).toEqual([-1, -1, 0]);
  });

  it("scrolls the selected tab into view, and only when a tab is selected", () => {
    show();
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
    expect(scrollIntoView).toHaveBeenCalledWith({ inline: "nearest", block: "nearest" });
    view?.unmount();
    view = null;
    scrollIntoView.mockClear();
    show({ selected: null });
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("draws the status line beneath the strip and above the panel", () => {
    show({ status: <span data-testid="status">Record 2</span> });
    const status = view?.container.querySelector("[data-testid='status']");
    const list = view?.container.querySelector("[role='tablist']");
    const panel = view?.container.querySelector("[role='tabpanel']");
    expect(status && list && panel).toBeTruthy();
    expect(list?.compareDocumentPosition(status as Node) ?? 0).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(status?.compareDocumentPosition(panel as Node) ?? 0).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });
});

describe("PanelTabs: the scroll controls", () => {
  it("shows no control while the strip fits, and both controls once it overflows", () => {
    show();
    laidOut(300, 300);
    expect(controls().map((button) => button.hidden)).toEqual([true, true]);
    laidOut(500, 300);
    expect(controls().map((button) => button.hidden)).toEqual([false, false]);
  });

  it("disables the back control at the start and the forward control at the end, and steps the strip between them", () => {
    show();
    const strip = laidOut(500, 300);
    const [back, forward] = controls();
    expect(back?.disabled).toBe(true);
    expect(forward?.disabled).toBe(false);
    click(forward as HTMLButtonElement);
    expect(strip.scrollLeft).toBe(200);
    expect(back?.disabled).toBe(false);
    expect(forward?.disabled).toBe(true);
    click(back as HTMLButtonElement);
    expect(strip.scrollLeft).toBe(0);
    expect(back?.disabled).toBe(true);
  });

  it("keeps the controls out of the tab order and hidden from assistive technology, since the arrow keys already move between tabs", () => {
    show();
    laidOut(500, 300);
    for (const button of controls()) {
      expect(button.tabIndex).toBe(-1);
      expect(button.getAttribute("aria-hidden")).toBe("true");
    }
  });
});

describe("PanelTabs: the strip's CSS", () => {
  const css = readFileSync(resolve(process.cwd(), "src/app/inspector/PanelTabs.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  // The declarations of the first rule for `selector`, or "" when the sheet has none.
  function declarationsOf(selector: string): string {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
  }

  it("keeps each tab on one line: the label never wraps and the dot sits beside it", () => {
    const tab = declarationsOf(".tab");
    expect(tab).toMatch(/white-space:\s*nowrap;/);
    expect(tab).not.toMatch(/flex-direction:\s*column;/);
  });

  it("draws each step control at least 36 px wide, the button token, so it is a touch-sized target", () => {
    expect(declarationsOf(".step")).toMatch(/width:\s*var\(--row-h-button\);/);
  });

  it("hides the strip's scrollbar while it still scrolls, since the controls and the trackpad move it", () => {
    const strip = declarationsOf(".strip");
    expect(strip).toMatch(/overflow-x:\s*auto;/);
    expect(strip).toMatch(/scrollbar-width:\s*none;/);
  });

  it("eases the strip's scroll only when motion is allowed", () => {
    expect(css).toMatch(/@media \(prefers-reduced-motion: no-preference\)\s*\{[^}]*\.strip\s*\{[^}]*scroll-behavior:\s*smooth;/);
  });
});
