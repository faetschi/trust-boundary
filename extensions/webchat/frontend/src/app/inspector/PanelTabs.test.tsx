// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, type Mounted } from "../../test-support/dom";
import { nextTabIndex, PanelTabs, type StripTab } from "./PanelTabs";

const TABS: StripTab[] = [
  { id: "decisions", label: "Decisions", badges: [{ text: "1 deny verdict", tone: "danger" }], icon: <svg data-mark="decisions" /> },
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

interface ShowOptions {
  readonly selected?: string | null;
  readonly stop?: string;
  readonly status?: ReactNode;
  readonly onSelect?: (id: string) => void;
}

function show({ selected = "decisions", stop = "decisions", status = null, onSelect = () => undefined }: ShowOptions = {}): void {
  view = mount(
    <PanelTabs tabs={TABS} selected={selected} stop={stop} onSelect={onSelect} status={status} labelledBy="governance-tab-decisions">
      <p>panel</p>
    </PanelTabs>,
  );
}

function tabsOf(): HTMLElement[] {
  return Array.from(view?.container.querySelectorAll<HTMLElement>("[role='tab']") ?? []);
}

function press(element: HTMLElement, key: string): void {
  act(() => {
    element.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
  });
}

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("PanelTabs: a vertical list of single-line tabs", () => {
  it("is a vertical tab list, with one tab per governance tab, in order", () => {
    show();
    expect(view?.container.querySelector("[role='tablist']")?.getAttribute("aria-orientation")).toBe("vertical");
    expect(tabsOf().map((tab) => tab.querySelector("span:nth-of-type(2)")?.textContent)).toEqual(["Decisions", "Provenance", "Qualification"]);
  });

  it("draws each row's mark, its label and the words of its first badge, and keeps the mark and the words out of the name", () => {
    show();
    const first = tabsOf()[0] as HTMLElement;
    expect(first.querySelector("[data-mark='decisions']")).not.toBeNull();
    expect(first.querySelector("span[aria-hidden='true']:not([data-tone])")?.getAttribute("aria-hidden")).toBe("true");
    expect(tabsOf().map((tab) => tab.textContent)).toEqual(["Decisions1 deny verdict", "Provenanceunavailable", "Qualificationnot established"]);
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

  it("draws the status line beneath the list and above the panel", () => {
    show({ status: <span data-testid="status">Record 2</span> });
    const status = view?.container.querySelector("[data-testid='status']");
    const list = view?.container.querySelector("[role='tablist']");
    const panel = view?.container.querySelector("[role='tabpanel']");
    expect(status && list && panel).toBeTruthy();
    expect(list?.compareDocumentPosition(status as Node) ?? 0).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(status?.compareDocumentPosition(panel as Node) ?? 0).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });
});

describe("PanelTabs: keys", () => {
  it("moves with Up and Down as well as Left and Right, wrapping at both ends, and selects the tab it moves to", () => {
    const onSelect = vi.fn();
    show({ onSelect });
    const [first, second, third] = tabsOf() as [HTMLElement, HTMLElement, HTMLElement];
    press(first, "ArrowDown");
    expect(onSelect).toHaveBeenLastCalledWith("provenance");
    expect(document.activeElement).toBe(second);
    press(second, "ArrowRight");
    expect(onSelect).toHaveBeenLastCalledWith("qualification");
    press(third, "ArrowDown");
    expect(onSelect).toHaveBeenLastCalledWith("decisions");
    press(first, "ArrowUp");
    expect(onSelect).toHaveBeenLastCalledWith("qualification");
    press(third, "Home");
    expect(onSelect).toHaveBeenLastCalledWith("decisions");
    press(first, "End");
    expect(onSelect).toHaveBeenLastCalledWith("qualification");
  });

  it("maps keys to indexes the same way for the icon rail", () => {
    expect([nextTabIndex("ArrowDown", 0, 5), nextTabIndex("ArrowUp", 0, 5), nextTabIndex("x", 0, 5)]).toEqual([1, 4, null]);
  });
});

describe("PanelTabs: the list's CSS", () => {
  const css = readFileSync(resolve(process.cwd(), "src/app/inspector/PanelTabs.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  function declarationsOf(selector: string): string {
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
  }

  it("stacks the tabs in a column, so no control is needed to reach one", () => {
    expect(declarationsOf(".tablist")).toMatch(/flex-direction:\s*column;/);
    expect(css).not.toMatch(/overflow-x/);
  });

  it("keeps the words on one line, cut with an ellipsis, since the full words are in the name and the panel", () => {
    const words = declarationsOf(".words");
    expect(words).toMatch(/white-space:\s*nowrap;/);
    expect(words).toMatch(/text-overflow:\s*ellipsis;/);
  });
});
