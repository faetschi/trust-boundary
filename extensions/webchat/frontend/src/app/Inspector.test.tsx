// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act, createRef, useRef, useState, type RefObject } from "react";
import { describe, expect, it, vi } from "vitest";
import type { Panels } from "../core/contract";
import { decisionItem, messageItem, panelsDefault, snapshotOf } from "../core/testing/builders";
import { click, mount, press } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { Inspector } from "./Inspector";
import { DRAWER_BREAKPOINT_PX } from "./layout";
import type { PanelKey } from "./uiPrefs";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";

// A store with a loaded projection, so the model is what the shell would render. A record is selected when asked.
async function modelWith(selectedId: string | null = null, panels: Panels = panelsDefault()): Promise<WorkspaceModel> {
  const fake = fakeWorkspace(
    snapshotOf({
      items: [
        messageItem({ id: "m1", sequence: 1 }),
        decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "DENY" }),
      ],
      panels,
    }),
  );
  const store = createWorkspaceStore(fake.deps);
  store.start();
  await flush();
  if (selectedId !== null) store.getSnapshot().actions.selectRecord(selectedId);
  return store.getSnapshot();
}

const NO_OP = () => undefined;

interface HostProps {
  readonly model: WorkspaceModel;
  readonly narrow?: boolean;
  readonly open?: boolean;
  readonly collapsed?: boolean;
  readonly initialPanel?: PanelKey;
  readonly initialRecordTab?: boolean;
  readonly onCloseRecord?: () => void;
  readonly onClose?: () => void;
}

// The shell's state for the inspector, as the shell keeps it: one persisted panel, the record tab flag and the column.
function Host({
  model,
  narrow = false,
  open = false,
  collapsed: startCollapsed = false,
  initialPanel = "decisions",
  initialRecordTab = false,
  onCloseRecord = NO_OP,
  onClose = NO_OP,
}: HostProps) {
  const [panel, setPanel] = useState<PanelKey>(initialPanel);
  const [recordTab, setRecordTab] = useState(initialRecordTab);
  const [collapsed, setCollapsed] = useState(startCollapsed);
  const headingRef: RefObject<HTMLHeadingElement | null> = useRef(null);
  return (
    <Inspector
      model={model}
      narrow={narrow}
      open={open}
      headingRef={headingRef}
      collapsed={collapsed}
      panel={panel}
      recordTab={recordTab}
      onClose={onClose}
      onToggleCollapsed={() => setCollapsed((value) => !value)}
      onExpandTo={(next) => {
        setPanel(next);
        setRecordTab(false);
        setCollapsed(false);
      }}
      onSelectPanel={(next) => {
        // The shell clears the record when a governance panel is chosen, so the panel is what the body shows.
        model.actions.clearRecord();
        setPanel(next);
        setRecordTab(false);
      }}
      onCloseRecord={onCloseRecord}
    />
  );
}

const TAB_ORDER = ["Decisions", "Provenance", "Publication", "Recovery", "Qualification"];

function tabsOf(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>("[role='tablist'] [role='tab']"));
}

function tabNamed(container: HTMLElement, start: string): HTMLButtonElement {
  const match = tabsOf(container).find((tab) => (tab.getAttribute("aria-label") ?? "").startsWith(start));
  if (!match) throw new Error(`no tab starting ${start}`);
  return match;
}

function selectedTab(container: HTMLElement): HTMLButtonElement | undefined {
  return tabsOf(container).find((tab) => tab.getAttribute("aria-selected") === "true");
}

describe("Inspector: the drawer at narrow widths", () => {
  it("is inert and hidden from assistive technology while the narrow drawer is closed", async () => {
    const view = mount(<Host model={await modelWith()} narrow open={false} />);
    const aside = view.container.querySelector("aside");
    expect(aside?.hasAttribute("inert")).toBe(true);
    expect(aside?.getAttribute("aria-hidden")).toBe("true");
    expect(aside?.getAttribute("data-open")).toBe("false");
    view.unmount();
  });

  it("is reachable when open, and at wide widths whatever the drawer state", async () => {
    const model = await modelWith();
    const open = mount(<Host model={model} narrow open />);
    expect(open.container.querySelector("aside")?.hasAttribute("inert")).toBe(false);
    open.unmount();
    const wide = mount(<Host model={model} narrow={false} open={false} />);
    expect(wide.container.querySelector("aside")?.hasAttribute("inert")).toBe(false);
    wide.unmount();
  });

  it("is a modal dialog while the narrow drawer is open, and a plain landmark at wide widths", async () => {
    const model = await modelWith();
    const open = mount(<Host model={model} narrow open />);
    expect(open.container.querySelector("aside")?.getAttribute("role")).toBe("dialog");
    expect(open.container.querySelector("aside")?.getAttribute("aria-modal")).toBe("true");
    open.unmount();
    const wide = mount(<Host model={model} narrow={false} open={false} />);
    expect(wide.container.querySelector("aside")?.hasAttribute("role")).toBe(false);
    expect(wide.container.querySelector("aside")?.hasAttribute("aria-modal")).toBe(false);
    wide.unmount();
  });

  it("has no record tab: the strip holds the five governance tabs, and no tab is pinned", async () => {
    const view = mount(<Host model={await modelWith("d1")} narrow={false} open={false} initialRecordTab />);
    const tabs = Array.from(view.container.querySelectorAll<HTMLElement>("aside [role='tab']"));
    expect(tabs).toHaveLength(5);
    expect(tabs.map((tab) => tab.dataset.pinned ?? null)).toEqual([null, null, null, null, null]);
    view.unmount();
  });

  it("puts the tab strip inside the open drawer, and its close control calls back", async () => {
    const onClose = vi.fn();
    const view = mount(<Host model={await modelWith()} narrow open onClose={onClose} />);
    expect(view.container.querySelector("aside [role='tablist']")).not.toBeNull();
    const close = Array.from(view.container.querySelectorAll("button")).find((b) => b.textContent === "Close governance");
    if (!close) throw new Error("no close control");
    click(close);
    expect(onClose).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("has no collapse control and no icon rail at narrow widths", async () => {
    const view = mount(<Host model={await modelWith()} narrow open />);
    expect(view.container.querySelector("[aria-label='Collapse governance panel']")).toBeNull();
    expect(view.container.querySelector("[role='toolbar']")).toBeNull();
    view.unmount();
  });

  it("moves focus to its heading when the focus ref is set by the shell", async () => {
    const model = await modelWith();
    const headingRef = createRef<HTMLHeadingElement>();
    const view = mount(
      <Inspector
        model={model}
        narrow
        open
        headingRef={headingRef}
        collapsed={false}
        panel="decisions"
        recordTab={false}
        onClose={NO_OP}
        onToggleCollapsed={NO_OP}
        onExpandTo={NO_OP}
        onSelectPanel={NO_OP}
        onCloseRecord={NO_OP}
      />,
    );
    headingRef.current?.focus();
    expect(document.activeElement).toBe(view.container.querySelector("h2"));
    view.unmount();
  });
});

describe("Inspector: the five governance tabs", () => {
  it("shows the five tabs in the fixed order, and exactly one tabpanel, labelled by the selected tab", async () => {
    const view = mount(<Host model={await modelWith()} />);
    const tabs = tabsOf(view.container);
    expect(tabs.map((tab) => (tab.getAttribute("aria-label") ?? "").split(",")[0])).toEqual(TAB_ORDER);
    const panels = view.container.querySelectorAll("[role='tabpanel']");
    expect(panels).toHaveLength(1);
    expect(panels[0]?.getAttribute("aria-labelledby")).toBe(selectedTab(view.container)?.id);
    expect(selectedTab(view.container)?.textContent).toContain("Decisions");
    view.unmount();
  });

  it("renders only the selected panel's body", async () => {
    const view = mount(<Host model={await modelWith()} initialPanel="provenance" />);
    const panel = view.container.querySelector("[role='tabpanel']") as HTMLElement;
    expect(panel.querySelector("section[aria-labelledby='panel-provenance']")).not.toBeNull();
    expect(panel.querySelector("section[aria-labelledby='panel-decisions']")).toBeNull();
    view.unmount();
  });

  it("keeps only the selected tab in the tab order (a roving tab stop)", async () => {
    const view = mount(<Host model={await modelWith()} initialPanel="recovery" />);
    const stops = tabsOf(view.container).map((tab) => tab.tabIndex);
    expect(stops).toEqual([-1, -1, -1, 0, -1]);
    view.unmount();
  });

  it("moves focus and selection with the arrow keys, wrapping at the ends, and with Home and End", async () => {
    const view = mount(<Host model={await modelWith()} />);
    const first = tabNamed(view.container, "Decisions");
    first.focus();
    press(first, "ArrowLeft");
    expect(document.activeElement).toBe(tabNamed(view.container, "Qualification"));
    expect(tabNamed(view.container, "Qualification").getAttribute("aria-selected")).toBe("true");
    press(document.activeElement as Element, "ArrowRight");
    expect(document.activeElement).toBe(tabNamed(view.container, "Decisions"));
    expect(selectedTab(view.container)?.getAttribute("aria-label")?.startsWith("Decisions")).toBe(true);
    press(document.activeElement as Element, "End");
    expect(document.activeElement).toBe(tabNamed(view.container, "Qualification"));
    press(document.activeElement as Element, "Home");
    expect(document.activeElement).toBe(tabNamed(view.container, "Decisions"));
    press(document.activeElement as Element, "ArrowRight");
    expect(document.activeElement).toBe(tabNamed(view.container, "Provenance"));
    view.unmount();
  });

  it("names each tab with its badge text, so the badge is part of the accessible name", async () => {
    const model = await modelWith(null, { ...panelsDefault(), governance: { allow_count: 1, deny_count: 1, unknown_count: 0 } });
    const view = mount(<Host model={model} />);
    expect(tabNamed(view.container, "Decisions").getAttribute("aria-label")).toBe("Decisions, 1 deny verdict");
    expect(tabNamed(view.container, "Provenance").getAttribute("aria-label")).toBe("Provenance, unavailable");
    expect(tabNamed(view.container, "Qualification").getAttribute("aria-label")).toBe("Qualification, not established");
    view.unmount();
  });

  it("shows the warnings at the bottom of the qualification body, and counts them on its tab", async () => {
    const fake = fakeWorkspace(snapshotOf({ items: [messageItem({ id: "m1", sequence: 1 })], warnings: ["first warning", "second warning"] }));
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    const view = mount(<Host model={store.getSnapshot()} initialPanel="qualification" />);
    expect(tabNamed(view.container, "Qualification").getAttribute("aria-label")).toBe(
      "Qualification, not established, 2 warnings",
    );
    const section = view.container.querySelector("section[aria-labelledby='panel-qualification']") as HTMLElement;
    const warnings = section.querySelector("#panel-warnings") as HTMLElement;
    expect(warnings).not.toBeNull();
    expect(section.querySelector("[role='tabpanel']")).toBeNull();
    expect(Array.from(section.querySelectorAll("li")).map((li) => li.textContent)).toEqual(["first warning", "second warning"]);
    const note = section.querySelector("p") as HTMLElement;
    expect(note.compareDocumentPosition(warnings) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    view.unmount();
  });
});

describe("Inspector: the record bar", () => {
  it("shows the record in the panel body, under a bar that names it, and keeps the five governance tabs", async () => {
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab />);
    const tabs = tabsOf(view.container);
    expect(tabs).toHaveLength(5);
    expect(tabs.map((tab) => (tab.getAttribute("aria-label") ?? "").split(",")[0])).toEqual(TAB_ORDER);
    expect(view.container.querySelector("[role='tabpanel']")?.textContent).toContain("sequence 2");
    expect(view.container.querySelector("h2")?.textContent).toBe("Record");
    view.unmount();
  });

  it("puts the bar between the strip and the body, with the record's name and a visible Close record control", async () => {
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab />);
    const close = view.container.querySelector<HTMLButtonElement>("button[aria-label='Close record 2']");
    expect(close?.textContent).toBe("Close record");
    expect(close?.parentElement?.textContent).toContain("Record 2");
    const strip = view.container.querySelector("[role='tablist']");
    const panel = view.container.querySelector("[role='tabpanel']");
    expect(strip?.compareDocumentPosition(close as Node)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(close?.compareDocumentPosition(panel as Node)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    view.unmount();
  });

  it("selects no governance tab while the record shows, and keeps the tab stop on the last panel", async () => {
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab initialPanel="qualification" />);
    expect(tabsOf(view.container).map((tab) => tab.getAttribute("aria-selected"))).toEqual([
      "false",
      "false",
      "false",
      "false",
      "false",
    ]);
    expect(tabsOf(view.container).map((tab) => tab.tabIndex)).toEqual([-1, -1, -1, -1, 0]);
    expect(view.container.querySelector("[role='tabpanel']")?.getAttribute("aria-labelledby")).toBe("governance-record-label");
    view.unmount();
  });

  it("closes the record when a governance tab is chosen, and shows that panel", async () => {
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab />);
    click(tabNamed(view.container, "Publication"));
    expect(view.container.querySelector("button[aria-label^='Close record']")).toBeNull();
    expect(view.container.querySelector("h2")?.textContent).toBe("Governance");
    expect(tabNamed(view.container, "Publication").getAttribute("aria-selected")).toBe("true");
    view.unmount();
  });

  it("closes with its own control, named for the record, which calls back", async () => {
    const onCloseRecord = vi.fn();
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab onCloseRecord={onCloseRecord} />);
    const close = view.container.querySelector("button[aria-label='Close record 2']") as HTMLButtonElement;
    expect(close).not.toBeNull();
    click(close);
    expect(onCloseRecord).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("when the record goes while focus is in the inspector, focus moves to the active governance tab", async () => {
    const view = mount(<Host model={await modelWith("d1")} initialRecordTab />);
    const close = view.container.querySelector<HTMLButtonElement>("button[aria-label='Close record 2']");
    close?.focus();
    const withoutRecord = await modelWith();
    act(() => view.root.render(<Host model={withoutRecord} initialRecordTab />));
    expect(tabsOf(view.container)).toHaveLength(5);
    expect(document.activeElement).toBe(tabNamed(view.container, "Decisions"));
    view.unmount();
  });

  it("at the drawer width, when the record goes while the drawer stays open, focus moves to the active tab inside the drawer", async () => {
    const view = mount(<Host model={await modelWith("d1")} narrow open initialRecordTab />);
    const close = view.container.querySelector<HTMLButtonElement>("button[aria-label='Close record 2']");
    close?.focus();
    const withoutRecord = await modelWith();
    act(() => view.root.render(<Host model={withoutRecord} narrow open initialRecordTab />));
    expect(view.container.querySelector("aside")?.getAttribute("data-open")).toBe("true");
    expect(document.activeElement).toBe(tabNamed(view.container, "Decisions"));
    view.unmount();
  });

  it("is absent when no record is selected, and the status line names the selected tab's words instead", async () => {
    const view = mount(<Host model={await modelWith()} initialRecordTab />);
    expect(tabsOf(view.container)).toHaveLength(5);
    expect(view.container.querySelector("button[aria-label^='Close record']")).toBeNull();
    expect(view.container.querySelector("p")?.textContent).toBe("Decisions · unavailable");
    view.unmount();
  });
});

describe("Inspector: the collapsed column at wide widths", () => {
  it("shows an icon rail of five named buttons, and renders no tablist and no tabpanel", async () => {
    const view = mount(<Host model={await modelWith()} collapsed />);
    const toolbar = view.container.querySelector("[role='toolbar']") as HTMLElement;
    expect(toolbar.getAttribute("aria-label")).toBe("Governance panels (collapsed)");
    const icons = Array.from(toolbar.querySelectorAll("button"));
    expect(icons.map((button) => button.getAttribute("aria-label"))).toEqual([
      "Open Decisions panel, unavailable",
      "Open Provenance panel, unavailable",
      "Open Publication panel, not attempted",
      "Open Recovery panel, not required",
      "Open Qualification panel, not established",
    ]);
    expect(view.container.querySelector("[role='tablist']")).toBeNull();
    expect(view.container.querySelector("[role='tabpanel']")).toBeNull();
    expect(view.container.querySelector("aside")?.getAttribute("aria-label")).toBe("Governance inspector");
    view.unmount();
  });

  it("draws each icon's badge dot as the shape of its tone, and a denied decision as the deny square, never the success tone", async () => {
    const denied: Panels = { ...panelsDefault(), governance: { allow_count: 0, deny_count: 1, unknown_count: 0 } };
    const view = mount(<Host model={await modelWith(null, denied)} collapsed />);
    const dots = Array.from(view.container.querySelectorAll("[role='toolbar'] button")).map((button) =>
      button.querySelector("[data-tone]")?.getAttribute("data-tone"),
    );
    expect(dots).toHaveLength(5);
    expect(dots[0]).toBe("deny");
    expect(dots).not.toContain("ok");
    expect(dots).not.toContain("allow");
    view.unmount();
  });

  it("expands and selects the panel of the icon pressed, and moves focus to its tab", async () => {
    const view = mount(<Host model={await modelWith()} collapsed />);
    const publication = view.container.querySelector("button[aria-label^='Open Publication panel']") as HTMLButtonElement;
    click(publication);
    expect(view.container.querySelector("[role='toolbar']")).toBeNull();
    expect(selectedTab(view.container)?.getAttribute("aria-label")).toBe("Publication, not attempted");
    expect(document.activeElement).toBe(tabNamed(view.container, "Publication"));
    view.unmount();
  });

  it("has a header control, named for the action, that reports the column as expanded when it is", async () => {
    const view = mount(<Host model={await modelWith()} />);
    const collapse = view.container.querySelector("button[aria-label='Collapse governance panel']") as HTMLButtonElement;
    expect(collapse.getAttribute("aria-expanded")).toBe("true");
    expect(collapse.getAttribute("aria-controls")).toBe("governance-inspector");
    click(collapse);
    expect(view.container.querySelector("[role='toolbar']")).not.toBeNull();
    const expand = view.container.querySelector("button[aria-label='Expand governance panel']") as HTMLButtonElement;
    expect(expand.getAttribute("aria-expanded")).toBe("false");
    view.unmount();
  });
});

describe("the drawer's width fits the five governance tabs", () => {
  it("is the inspector column's width, or the viewport less a gutter, whichever is narrower", () => {
    const css = readFileSync(resolve(process.cwd(), "src/app/Inspector.module.css"), "utf8");
    expect(css).toMatch(/width:\s*min\(var\(--inspector-width\),\s*100vw\s*-\s*2rem\);/);
  });
});

// The text inside the first `@media (<query>)` block of a stylesheet, or "" when there is none.
function mediaBlockOf(css: string, query: string): string {
  const start = css.indexOf(query);
  if (start < 0) return "";
  const open = css.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  return "";
}

// The declarations of the first rule for `selector` in a stylesheet fragment, or "" when there is none.
function declarationsOf(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
}

describe("the closed drawer is off the canvas and out of focus, at every narrow width", () => {
  const css = readFileSync(resolve(process.cwd(), "src/app/Inspector.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const narrow = mediaBlockOf(css, `@media (max-width: ${DRAWER_BREAKPOINT_PX}px)`);

  it("is translated past its own width and a gutter, so no edge or shadow of it reaches the viewport", () => {
    expect(declarationsOf(narrow, ".inspector")).toMatch(/transform:\s*translateX\(calc\(100% \+ var\(--space-4\)\)\);/);
  });

  it("is hidden while closed, which also takes it out of the focus order and the accessibility tree", () => {
    expect(declarationsOf(narrow, ".inspector")).toMatch(/visibility:\s*hidden;/);
    expect(declarationsOf(narrow, '.inspector[data-open="true"]')).toMatch(/visibility:\s*visible;/);
  });

  it("stays visible while it slides out and is hidden when the slide ends; an open drawer shows at once", () => {
    expect(declarationsOf(narrow, ".inspector")).toMatch(/visibility 0s linear var\(--dur-slow\)/);
    expect(declarationsOf(narrow, '.inspector[data-open="true"]')).toMatch(/visibility 0s;/);
  });
});

describe("the drawer breakpoint is one value, in layout.ts and in the CSS", () => {
  it("the inspector and shell stylesheets use the same breakpoint as the hook", () => {
    expect(DRAWER_BREAKPOINT_PX).toBe(1180);
    for (const name of ["Inspector.module.css", "Shell.module.css"]) {
      const css = readFileSync(resolve(process.cwd(), "src/app", name), "utf8");
      expect(css).toContain(`@media (max-width: ${DRAWER_BREAKPOINT_PX}px)`);
    }
  });
});
