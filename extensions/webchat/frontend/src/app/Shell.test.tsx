// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Snapshot } from "../core/contract";
import { decisionItem, messageItem, panelsDefault, resultItem, snapshotOf } from "../core/testing/builders";
import { Shell } from "./Shell";
import { WorkspaceProvider } from "./WorkspaceProvider";
import { click, installMatchMedia, mount, press, settle, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../test-support/fakes";
import { THEME_STORAGE_KEY } from "../theme/theme";
import { UI_PREFS_KEY } from "./uiPrefs";

// A fixture projection, unverified, with a reported (unverified) publication.
function fixtureSnapshot() {
  return snapshotOf({
    items: [
      messageItem({ id: "m1", sequence: 1, role: "user", text: "list the files" }),
      messageItem({ id: "m2", sequence: 2, role: "assistant", text: "Listing them." }),
      decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded" }),
    ],
    // The counts agree with the one ALLOW verdict in the window.
    panels: {
      ...panelsDefault(),
      governance: { allow_count: 1, deny_count: 0, unknown_count: 0 },
      publication: { state: "published" },
    },
  });
}

let shim: MatchMediaShim | null = null;
let mounted: Mounted | null = null;
let fake: FakeWorkspace;

function renderShell(width: number, snapshot: Snapshot = fixtureSnapshot()): void {
  shim = installMatchMedia(width);
  fake = fakeWorkspace(snapshot);
  mounted = mount(
    <WorkspaceProvider deps={fake.deps}>
      <Shell version="0.1.0" />
    </WorkspaceProvider>,
  );
}

function q<T extends Element = HTMLElement>(selector: string): T {
  const element = mounted?.container.querySelector<T>(selector);
  if (!element) throw new Error(`no element for ${selector}`);
  return element;
}

function boundary(): HTMLElement {
  return q("[role='note'][aria-label='Claim boundary']");
}

// The value of each claim in the header, in order: the first element of each definition, before any warning chip.
function claimValues(): string[] {
  return Array.from(boundary().querySelectorAll("dd")).map((dd) => dd.firstElementChild?.textContent ?? "");
}

// The text of the first @media block for `query`, brace-matched, or "" when the sheet has none.
function mediaBlock(css: string, query: string): string {
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

// The declarations of the first rule for `selector` in a stylesheet fragment.
function declarationsOf(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
}

function byLabel(text: string): HTMLElement {
  const match = Array.from(mounted?.container.querySelectorAll<HTMLElement>("button") ?? []).find(
    (el) => el.textContent?.trim() === text,
  );
  if (!match) throw new Error(`no button "${text}"`);
  return match;
}

// A governance tab by the start of its accessible name ("Decisions" names "Decisions, 1 deny" too).
function governanceTab(name: string): HTMLElement {
  const match = Array.from(mounted?.container.querySelectorAll<HTMLElement>("aside [role='tab']") ?? []).find((el) =>
    (el.getAttribute("aria-label") ?? "").startsWith(name),
  );
  if (!match) throw new Error(`no governance tab ${name}`);
  return match;
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
  window.localStorage.removeItem(THEME_STORAGE_KEY);
  window.localStorage.removeItem(UI_PREFS_KEY);
  document.documentElement.removeAttribute("data-theme");
});

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("Shell: landmarks and headings", () => {
  it("labels every landmark and has one h1, in a correct heading order", async () => {
    renderShell(1440);
    await settle();
    expect(mounted?.container.querySelectorAll("h1")).toHaveLength(1);
    expect(q("h1").textContent).toBe("tbound web chat");
    expect(q("header").getAttribute("aria-label")).toBe("Workspace header");
    expect(q("nav").getAttribute("aria-label")).toBe("Sessions and streams");
    expect(q("main").getAttribute("aria-label")).toBe("Workspace");
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
    expect(q("[aria-label='Session statistics']").textContent).toContain("turn");
    const levels = Array.from(mounted?.container.querySelectorAll("h1, h2, h3") ?? []).map((h) => h.tagName);
    expect(levels[0]).toBe("H1");
    for (let i = 1; i < levels.length; i++) {
      const previous = Number(levels[i - 1]?.slice(1));
      const current = Number(levels[i]?.slice(1));
      expect(current - previous).toBeLessThanOrEqual(1);
    }
  });

  it("puts a skip link first, and it points at the visible view", async () => {
    renderShell(1440);
    await settle();
    const skip = mounted?.container.querySelector("a") as HTMLAnchorElement;
    expect(skip.getAttribute("href")).toBe("#view-panel-chat");
    expect(skip.textContent).toBe("Skip to the chat transcript");
  });
});

describe("Shell: the claim boundary and the source are always visible", () => {
  it("shows the claim boundary before any snapshot arrives, with each value unavailable and never a literal", () => {
    renderShell(1440);
    expect(boundary().textContent).toContain("Presentation only");
    expect(boundary().textContent).toContain(
      "Display only: these values are copied from the projection and are not evidence.",
    );
    expect(claimValues()).toEqual(Array(4).fill("unavailable (no projection loaded)"));
    expect(boundary().textContent).not.toMatch(/unexecuted|not-established|\bfalse\b/);
  });

  it("reads each claim value from the projection, exactly as projected, with no warning", async () => {
    renderShell(1440);
    await settle();
    expect(claimValues()).toEqual(["false", "unexecuted", "not-established", "false"]);
    expect(boundary().querySelector("[data-tone]")).toBeNull();
  });

  it("never shows a projected true as a pass: an unverified projection that says true carries a warning chip", async () => {
    renderShell(
      1440,
      snapshotOf({
        panels: {
          ...panelsDefault(),
          qualification: { g1: true, h1: "unexecuted", containment: "not-established", claim_bearing: true },
        },
      }),
    );
    await settle();
    expect(claimValues()).toEqual(["true", "unexecuted", "not-established", "true"]);
    const warnings = Array.from(boundary().querySelectorAll("[data-tone]"));
    expect(warnings.map((chip) => chip.textContent)).toEqual([
      "unverified projection claims true",
      "unverified projection claims true",
    ]);
    expect(warnings.some((chip) => chip.getAttribute("data-tone") === "ok")).toBe(false);
  });

  it("shows no session and no rows before a projection is loaded", () => {
    renderShell(1440);
    expect(q("nav").textContent).toContain("No session projection");
    expect(q("nav").textContent).toContain("unavailable (no projection loaded)");
    click(governanceTab("Qualification"));
    expect(q("aside").textContent).toContain("unavailable (no projection loaded)");
    expect(q("aside").textContent).not.toContain("No warnings projected.");
  });

  it("before the first snapshot, every panel and rail value reads unavailable, never no, zero, none or ok", () => {
    renderShell(1440);
    const values: string[] = [];
    for (const name of ["Decisions", "Provenance", "Publication", "Recovery", "Qualification"]) {
      click(governanceTab(name));
      values.push(...Array.from(q("aside [role='tabpanel']").querySelectorAll("dd, li")).map((el) => el.textContent?.trim() ?? ""));
    }
    expect(values.length).toBeGreaterThan(10);
    for (const value of values) expect(value, value).toMatch(/^unavailable/);
    expect(q("nav").textContent).not.toMatch(/No streams projected/);
    expect(q("[role='group'][aria-label='Observed posture']").textContent).not.toContain("no decisions or results projected");
  });

  it("keeps the rail to one control, the collapse toggle, with no link or field", async () => {
    renderShell(1440);
    await settle();
    expect(q("nav").querySelectorAll("button")).toHaveLength(1);
    expect(q("nav").querySelectorAll("a, input, select, textarea")).toHaveLength(0);
  });

  it("labels the source as unverified, and the connection in words", async () => {
    renderShell(1440);
    await settle();
    expect(q("[data-source]").textContent).toBe("source: fixture · unverified");
    expect(q("[data-connection]").textContent).toBe("connecting…");
    act(() => fake.sources[0]?.open());
    expect(q("[data-connection]").textContent).toBe("connected");
    expect(mounted?.container.querySelector("[role='status']")?.textContent?.trim()).toBe("Stream connected.");
  });
});

describe("Shell: views and the skip link", () => {
  it("switches the view with the tab and moves the skip link to the visible view", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Trajectory"));
    expect(q("#view-panel-trajectory").hidden).toBe(false);
    expect(q("#view-panel-chat").hidden).toBe(true);
    expect(q("a").getAttribute("href")).toBe("#view-panel-trajectory");
    expect(q("a").textContent).toBe("Skip to the trajectory ledger");
    expect(q("#view-panel-trajectory [role='region'][aria-label='Record ledger']")).toBeTruthy();
  });

  it("moves between views with the arrow keys", async () => {
    renderShell(1440);
    await settle();
    const chat = q<HTMLButtonElement>("#view-tab-chat");
    chat.focus();
    press(chat, "ArrowRight");
    expect(document.activeElement).toBe(q("#view-tab-trajectory"));
    expect(q("#view-tab-trajectory").getAttribute("aria-selected")).toBe("true");
  });
});

describe("Shell: governance panels honour the projection", () => {
  it("shows an unverified publication as reported, never as published", async () => {
    renderShell(1440);
    await settle();
    click(governanceTab("Publication"));
    const section = q("section[aria-labelledby='panel-publication']");
    // The label is the state chip. The trust sentence beside it says "not independently verified" and also contains "reported",
    // so the chip itself is read here.
    expect(Array.from(section.querySelectorAll("[data-tone]"), (chip) => chip.textContent)).toEqual(["reported"]);
    expect(section.textContent).not.toMatch(/\bpublished\b/);
  });

  it("shows one governance panel at a time, in the tab order, with the others not rendered", async () => {
    renderShell(1440);
    await settle();
    expect(q("aside").querySelectorAll("[role='tabpanel']")).toHaveLength(1);
    expect(q("aside").querySelectorAll("section[aria-labelledby^='panel-']")).toHaveLength(1);
    click(governanceTab("Provenance"));
    expect(q("aside").querySelectorAll("section[aria-labelledby^='panel-']")).toHaveLength(1);
    expect(q("section[aria-labelledby='panel-provenance']")).toBeTruthy();
  });

  it("shows the footer's untracked metrics as unavailable, with a reason, not as zero", async () => {
    renderShell(1440);
    await settle();
    const stats = q("[aria-label='Session statistics']");
    expect(stats.textContent).toContain("tokens unavailable");
    expect(stats.textContent).toContain("timing unavailable");
    expect(stats.textContent).not.toMatch(/tokens 0|timing 0|\b0 ms\b/);
  });

  it("shows the policy counts for the retained window, as the projection states them", async () => {
    renderShell(1440);
    await settle();
    expect(q("[aria-label='Session statistics']").textContent).toContain("policy verdicts 1 allow / 0 deny / 0 unknown");
  });
});

describe("Shell: the drawer at the narrow breakpoint", () => {
  it("is inert and hidden from assistive technology when closed, and opens from the toggle", async () => {
    renderShell(1000);
    await settle();
    const toggle = byLabel("Governance");
    const aside = q("aside");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(aside.hasAttribute("inert")).toBe(true);
    expect(aside.getAttribute("aria-hidden")).toBe("true");
    click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(aside.hasAttribute("inert")).toBe(false);
    expect(aside.hasAttribute("aria-hidden")).toBe(false);
    expect(document.activeElement).toBe(q("aside h2"));
  });

  it("closes on Escape, and returns focus to the toggle", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    press(document.body, "Escape");
    expect(q("aside").hasAttribute("inert")).toBe(true);
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(byLabel("Governance"));
  });

  it("is a column at wide widths: the header control reports it, collapses it, and the choice persists", async () => {
    renderShell(1440);
    await settle();
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("true");
    expect(q("aside").hasAttribute("inert")).toBe(false);
    expect(q("aside").hasAttribute("aria-hidden")).toBe(false);
    click(byLabel("Governance"));
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("false");
    expect(q("aside").getAttribute("data-collapsed")).toBe("true");
    expect(JSON.parse(window.localStorage.getItem(UI_PREFS_KEY) ?? "null")).toMatchObject({ inspectorCollapsed: true });
  });

  it("re-evaluates when the viewport crosses the breakpoint", async () => {
    renderShell(1440);
    await settle();
    shim?.setWidth(900);
    expect(q("aside").hasAttribute("inert")).toBe(true);
    shim?.setWidth(1440);
    expect(q("aside").hasAttribute("inert")).toBe(false);
  });

  it("does not reopen after a widen and a narrow again: leaving the narrow layout closes the drawer", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    expect(q("aside").getAttribute("data-open")).toBe("true");
    shim?.setWidth(1440);
    shim?.setWidth(1000);
    expect(q("aside").getAttribute("data-open")).toBe("false");
    expect(q("aside").hasAttribute("inert")).toBe(true);
    expect(q("aside").getAttribute("aria-hidden")).toBe("true");
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("false");
    expect(q("aside").contains(document.activeElement)).toBe(false);
  });

  it("a record selected at narrow width opens the drawer; after a widen and a narrow the drawer is closed and focus is not in it", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    expect(q("aside").getAttribute("data-open")).toBe("true");
    expect(q("aside").contains(document.activeElement)).toBe(true);
    shim?.setWidth(1440);
    shim?.setWidth(1000);
    expect(q("aside").getAttribute("data-open")).toBe("false");
    expect(q("aside").hasAttribute("inert")).toBe(true);
    expect(q("aside").contains(document.activeElement)).toBe(false);
  });

  it("never opens by itself when the viewport narrows", async () => {
    renderShell(1440);
    await settle();
    shim?.setWidth(1000);
    expect(q("aside").getAttribute("data-open")).toBe("false");
    expect(q("aside").hasAttribute("inert")).toBe(true);
  });
});

describe("Shell: the open drawer never covers the header controls", () => {
  // Why this is a contract and not a hit-test: happy-dom has no layout engine, so it cannot say which element
  // is at a point. The layering is therefore asserted by geometry-independent facts: the header precedes the
  // workspace body in the DOM, the drawer lives in that body, and the stylesheet positions the open drawer at
  // the top of the body (not the viewport) at the narrow breakpoint. The browser hit-test is recorded in
  // docs/phase3-parity.md (A3).
  it("places the drawer in the body that follows the header, positioned at the top of that body", async () => {
    renderShell(1024);
    await settle();
    click(byLabel("Governance"));
    const header = q("header");
    const aside = q("aside");
    const body = aside.parentElement as HTMLElement;
    expect(header.compareDocumentPosition(aside) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(body.previousElementSibling).toBe(header);
    const shellCss = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    const inspectorCss = readFileSync(resolve(process.cwd(), "src/app/Inspector.module.css"), "utf8");
    const narrowBody = declarationsOf(mediaBlock(shellCss, "@media (max-width: 1180px)"), ".body");
    expect(narrowBody).toMatch(/position:\s*relative;/);
    // The closed drawer is translated past the body's right edge; clipping keeps it from widening the page.
    expect(narrowBody).toMatch(/overflow-x:\s*clip;/);
    const drawer = declarationsOf(mediaBlock(inspectorCss, "@media (max-width: 1180px)"), ".inspector");
    expect(drawer).toMatch(/position:\s*absolute;/);
    expect(drawer).toMatch(/top:\s*0;/);
    expect(drawer).not.toMatch(/position:\s*fixed/);
  });

  it("keeps the toggle and the theme control in the header, ahead of the drawer, and the toggle announced with aria-expanded", async () => {
    renderShell(1024);
    await settle();
    const toggle = byLabel("Governance");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.compareDocumentPosition(q("aside")) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(q("header").contains(toggle)).toBe(true);
    expect(q("header").contains(byLabel("Dark"))).toBe(true);
    click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("Shell: the theme control", () => {
  it("applies the theme attribute, persists it, and returns to the system choice", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Dark"));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");
    expect(byLabel("Dark").getAttribute("aria-pressed")).toBe("true");
    click(byLabel("System"));
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("system");
  });
});

// Unmounts the shell and mounts it again at another width, as a reload does.
function remount(width: number): void {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
  renderShell(width);
}

describe("Shell: the sessions rail", () => {
  it("keeps its collapse choice across a reload, in the one versioned key", async () => {
    renderShell(1440);
    await settle();
    click(q("nav button"));
    expect(q("nav button").getAttribute("aria-expanded")).toBe("false");
    expect(JSON.parse(window.localStorage.getItem(UI_PREFS_KEY) ?? "null")).toMatchObject({ railCollapsed: true });
    remount(1440);
    await settle();
    expect(q("nav button").getAttribute("aria-expanded")).toBe("false");
  });

  it("clears a stored choice that is not valid, and shows the defaults", async () => {
    window.localStorage.setItem(UI_PREFS_KEY, JSON.stringify({ railCollapsed: "yes" }));
    renderShell(1440);
    await settle();
    expect(window.localStorage.getItem(UI_PREFS_KEY)).toBeNull();
    expect(q("nav button").getAttribute("aria-expanded")).toBe("true");
  });

  it("is a disclosure strip on a phone, collapsed by default, and its state is not persisted", async () => {
    renderShell(390);
    await settle();
    const strip = q("nav h2 button");
    expect(strip.getAttribute("aria-expanded")).toBe("false");
    expect(strip.textContent).toContain("Sessions · ");
    click(strip);
    expect(strip.getAttribute("aria-expanded")).toBe("true");
    expect(window.localStorage.getItem(UI_PREFS_KEY)).toBeNull();
  });
});

describe("Shell: the governance inspector's record bar", () => {
  it("shows the record under a bar with a close control, and closing it returns focus to the row that opened it", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    expect(q("aside").querySelectorAll("[role='tab']")).toHaveLength(5);
    expect(q("aside").querySelectorAll("[role='tab'][aria-selected='true']")).toHaveLength(0);
    expect(q("aside button[aria-label='Close record 3']").textContent).toBe("Close record");
    click(q("button[aria-label='Close record 3']"));
    expect(q("aside").querySelectorAll("[role='tab']")).toHaveLength(5);
    expect(document.activeElement).toBe(q("button[data-record-row][data-id='d1']"));
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
  });

  it("Escape clears the record first, and leaves a wide inspector open", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    expect(q("aside button[aria-label^='Close record']")).toBeTruthy();
    press(document.body, "Escape");
    // The record is cleared: the governance inspector is back, the row is not pressed, and no Close record control is left.
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
    expect(q("button[data-record-row][data-id='d1']").getAttribute("aria-pressed")).toBe("false");
    expect(document.querySelector("aside button[aria-label^='Close record']")).toBeNull();
    expect(q("aside").querySelectorAll("[role='tab']")).toHaveLength(5);
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("true");
    expect(q("aside").getAttribute("data-collapsed")).toBe("false");
  });

  it("choosing a governance tab while a record shows closes the record, and selects that tab", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    click(governanceTab("Publication"));
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
    expect(governanceTab("Publication").getAttribute("aria-selected")).toBe("true");
    expect(q("button[data-record-row][data-id='d1']").getAttribute("aria-pressed")).toBe("false");
    expect(document.querySelector("aside button[aria-label^='Close record']")).toBeNull();
  });

  it("a new selection shows the record again, after a governance tab was chosen", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    click(governanceTab("Publication"));
    click(q("button[data-record-row][data-id='r1']"));
    expect(q("aside").getAttribute("aria-label")).toBe("Record inspector");
    expect(q("aside [role='tabpanel']").getAttribute("aria-labelledby")).toBe("governance-record-label");
  });

  it("selecting a record while the wide inspector is collapsed expands it", async () => {
    renderShell(1440);
    await settle();
    click(byLabel("Governance"));
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    expect(q("aside").getAttribute("data-collapsed")).toBe("false");
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("true");
  });

  it("at narrow width, a record opens the drawer; the first Escape clears only the record, and the second closes the drawer", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    expect(q("aside").getAttribute("data-open")).toBe("true");
    press(document.body, "Escape");
    expect(q("aside").getAttribute("data-open")).toBe("true");
    expect(q("aside").querySelectorAll("[role='tab']")).toHaveLength(5);
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
    expect(document.activeElement).toBe(q("aside [role='tab'][aria-selected='true']"));
    press(document.body, "Escape");
    expect(q("aside").getAttribute("data-open")).toBe("false");
    expect(q("aside").hasAttribute("inert")).toBe(true);
    expect(document.activeElement).toBe(q("button[data-record-row][data-id='d1']"));
  });

  it("at narrow width, Close record clears only the record: the drawer stays open on its governance tab, and the next Escape closes it", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Trajectory"));
    click(q("button[data-record-row][data-id='d1']"));
    click(q("button[aria-label='Close record 3']"));
    expect(q("aside").getAttribute("data-open")).toBe("true");
    expect(q("aside").hasAttribute("inert")).toBe(false);
    expect(q("aside").querySelectorAll("[role='tab']")).toHaveLength(5);
    expect(q("aside").getAttribute("aria-label")).toBe("Governance inspector");
    expect(document.activeElement).toBe(q("aside [role='tab'][aria-selected='true']"));
    press(document.body, "Escape");
    expect(q("aside").getAttribute("data-open")).toBe("false");
    expect(document.activeElement).toBe(q("button[data-record-row][data-id='d1']"));
  });
});

// The keyboard Tab is dispatched by hand, so its default action can be read: a trapped Tab is prevented.
function tabKey(target: Element, shift = false): KeyboardEvent {
  const event = new KeyboardEvent("keydown", { key: "Tab", shiftKey: shift, bubbles: true, cancelable: true });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

describe("Shell: the open drawer's scrim closes it by a press, as Escape and Close do by key", () => {
  it("is drawn only while the drawer is open", async () => {
    renderShell(1000);
    await settle();
    expect(document.querySelector("[data-testid='scrim']")).toBeNull();
    click(byLabel("Governance"));
    expect(q("[data-testid='scrim']").getAttribute("aria-hidden")).toBe("true");
    click(byLabel("Close governance"));
    expect(document.querySelector("[data-testid='scrim']")).toBeNull();
  });

  it("closes the drawer on a press, and returns focus to the toggle", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    click(q("[data-testid='scrim']"));
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBe("false");
    expect(q("aside").hasAttribute("inert")).toBe(true);
    expect(document.querySelector("[data-testid='scrim']")).toBeNull();
    expect(document.activeElement).toBe(byLabel("Governance"));
  });

  it("is stacked between the page and the drawer: above the page's layers, under the drawer's", () => {
    const tokens = readFileSync(resolve(process.cwd(), "src/styles/tokens.css"), "utf8");
    const z = (name: string) => Number(new RegExp(`--${name}:\\s*(\\d+);`).exec(tokens)?.[1]);
    expect(z("z-scrim")).toBeGreaterThan(z("z-sticky"));
    expect(z("z-scrim")).toBeLessThan(z("z-overlay"));
    const shell = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    expect(declarationsOf(shell, ".scrim")).toMatch(/z-index:\s*var\(--z-scrim\);/);
    expect(declarationsOf(shell, ".scrim")).toMatch(/background:\s*var\(--scrim\);/);
    // The drawer's own rule, at the breakpoint where it is a drawer, is the token that sits above the scrim.
    const inspector = readFileSync(resolve(process.cwd(), "src/app/Inspector.module.css"), "utf8");
    expect(declarationsOf(mediaBlock(inspector, "@media (max-width: 1180px)"), ".inspector")).toMatch(/z-index:\s*var\(--z-overlay\);/);
  });
});

describe("Shell: the open drawer is a modal dialog", () => {
  it("makes the page behind it inert while it is open: the header, the skip link, the rail and the workspace", async () => {
    renderShell(1000);
    await settle();
    expect(q("main").hasAttribute("inert")).toBe(false);
    // The skip link is picked by its own class, not as the first anchor, so the check cannot land on another link.
    const skip = q("a[class*='_skip_']");
    click(byLabel("Governance"));
    expect(q("header").hasAttribute("inert")).toBe(true);
    expect(q("nav").hasAttribute("inert")).toBe(true);
    expect(q("main").hasAttribute("inert")).toBe(true);
    expect(skip.hasAttribute("inert")).toBe(true);
    expect(q("aside").hasAttribute("inert")).toBe(false);
    press(document.body, "Escape");
    expect(q("header").hasAttribute("inert")).toBe(false);
    expect(q("nav").hasAttribute("inert")).toBe(false);
    expect(q("main").hasAttribute("inert")).toBe(false);
    expect(skip.hasAttribute("inert")).toBe(false);
  });

  it("keeps the wide layout's page live: nothing is inert at a wide width, even with the column open", async () => {
    renderShell(1440);
    await settle();
    expect(q("main").hasAttribute("inert")).toBe(false);
    expect(q("nav").hasAttribute("inert")).toBe(false);
  });

  it("wraps Tab from the last stop of the drawer to the first, and Shift+Tab from the first to the last", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    // The tab stops of the drawer: its controls, and the one selected tab (the others are roving, tabindex -1).
    const stops = Array.from(q("aside").querySelectorAll<HTMLElement>("button:not([disabled]):not([tabindex='-1'])"));
    const first = stops[0];
    const last = stops[stops.length - 1];
    if (!first || !last || first === last) throw new Error("the drawer needs two stops to wrap");
    last.focus();
    expect(tabKey(last).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(first);
    expect(tabKey(first, true).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(last);
  });

  it("leaves a Tab from one stop of the drawer to the next to the browser", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    const stops = Array.from(q("aside").querySelectorAll<HTMLElement>("button:not([disabled]):not([tabindex='-1'])"));
    const first = stops[0];
    if (!first || stops.length < 2) throw new Error("the drawer needs two stops");
    first.focus();
    expect(tabKey(first).defaultPrevented).toBe(false);
  });

  it("is a dialog while it is open, and reaches the focus into it on open", async () => {
    renderShell(1000);
    await settle();
    click(byLabel("Governance"));
    expect(q("aside").getAttribute("role")).toBe("dialog");
    expect(q("aside").getAttribute("aria-modal")).toBe("true");
    expect(document.activeElement).toBe(q("aside h2"));
  });
});

describe("Shell: the header control and the workspace column", () => {
  it("shows the Governance header control at every width, with aria-expanded that says whether its panel is open", async () => {
    for (const width of [1440, 1180, 1024, 390]) {
      remount(width);
      await settle();
      // Wide (above the 1180 px drawer breakpoint): the inspector column is open at first. Narrow: the drawer starts closed.
      const wide = width > 1180;
      expect(byLabel("Governance").getAttribute("aria-expanded"), String(width)).toBe(wide ? "true" : "false");
      click(byLabel("Governance"));
      expect(byLabel("Governance").getAttribute("aria-expanded"), String(width)).toBe(wide ? "false" : "true");
    }
  });

  it("holds the footer inside the workspace column, after the views, as the column's last box", async () => {
    renderShell(1440);
    await settle();
    const main = q("main[aria-label='Workspace']");
    const footer = q("main [role='group'][aria-label='Session statistics']").parentElement;
    expect(footer?.parentElement).toBe(main);
    expect(main.lastElementChild).toBe(footer);
    expect(q("[data-view]").compareDocumentPosition(footer as HTMLElement) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("keeps the governance tab strip in its own scroll box, so the page never scrolls sideways", () => {
    const css = readFileSync(resolve(process.cwd(), "src/app/inspector/PanelTabs.module.css"), "utf8");
    expect(declarationsOf(css, ".strip")).toMatch(/overflow-x:\s*auto;/);
  });
});

describe("Shell: the phone header is compact", () => {
  it("puts Governance and the theme control on one full-width row, below the badges", () => {
    const css = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    const phone = mediaBlock(css, "@media (max-width: 760px)");
    const controls = declarationsOf(phone, ".controls");
    expect(controls).toMatch(/flex:\s*1 1 100%;/);
    expect(controls).toMatch(/justify-content:\s*space-between;/);
    expect(controls).toMatch(/margin-left:\s*0;/);
  });
});

describe("Shell: the phone stack keeps one 4 px step between its rows (QA4 check J)", () => {
  it("sets the header's rows, the header's padding and the workspace's gaps to the 4 px step", () => {
    const shell = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    const phone = mediaBlock(shell, "@media (max-width: 760px)");
    expect(declarationsOf(phone, ".header")).toMatch(/gap:\s*var\(--space-1\);/);
    expect(declarationsOf(phone, ".header")).toMatch(/padding:\s*var\(--space-1\) var\(--space-3\);/);
    expect(declarationsOf(phone, ".main")).toMatch(/gap:\s*var\(--space-1\);/);
    expect(declarationsOf(phone, ".main")).toMatch(/padding-top:\s*var\(--space-1\);/);
  });

  it("sets the footer's padding to the 4 px step on a phone, and leaves its text as it is", () => {
    const footer = readFileSync(resolve(process.cwd(), "src/app/Footer.module.css"), "utf8");
    expect(declarationsOf(mediaBlock(footer, "@media (max-width: 760px)"), ".footer")).toMatch(/padding:\s*var\(--space-1\) var\(--space-2\);/);
  });
});

describe("Shell: the reading order is the visual order (WCAG 1.3.2)", () => {
  it("places the footer after the views in the DOM, at every width, which is where it is drawn", async () => {
    for (const width of [1440, 1024, 390, 360]) {
      remount(width);
      await settle();
      const stats = q('[role="group"][aria-label="Session statistics"]');
      const views = q("[data-view]");
      expect(views.compareDocumentPosition(stats) & Node.DOCUMENT_POSITION_FOLLOWING, String(width)).toBeTruthy();
      expect(views.contains(stats), String(width)).toBe(false);
    }
  });

  it("moves no element with CSS order, so the DOM order is the visual order at every width", () => {
    const footer = readFileSync(resolve(process.cwd(), "src/app/Footer.module.css"), "utf8");
    const shell = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    expect(footer).not.toMatch(/\border:/);
    expect(shell).not.toMatch(/\border:/);
  });
});

describe("Shell: on a phone the pinned composer is the page's last box", () => {

  it("adds no bottom padding to the column on a phone, so the composer's bottom edge is the page's end", () => {
    const shell = readFileSync(resolve(process.cwd(), "src/app/Shell.module.css"), "utf8");
    expect(declarationsOf(mediaBlock(shell, "@media (max-width: 760px)"), ".main")).toMatch(/padding-bottom:\s*0;/);
  });
});

describe("Shell: markup", () => {
  it("renders no inline style attribute and no event-handler attribute, after the markup is rendered", async () => {
    renderShell(1440);
    await settle();
    const html = mounted?.container.innerHTML;
    // The markup must be there before the absence of attributes means anything.
    expect(html).toMatch(/aria-label="Workspace header"/);
    expect(html).toMatch(/aria-label="Session statistics"/);
    expect(html).toMatch(/data-view="chat"/);
    expect(html).not.toMatch(/\sstyle\s*=/);
    expect(html).not.toMatch(/\son[a-z]+\s*=/);
    expect(html).not.toMatch(/<script/i);
  });
});
