// @vitest-environment happy-dom
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { decisionItem, messageItem, panelsDefault, resultItem, snapshotOf } from "../core/testing/builders";
import { Shell } from "./Shell";
import { WorkspaceProvider } from "./WorkspaceProvider";
import { click, installMatchMedia, mount, press, settle, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../test-support/fakes";
import { THEME_STORAGE_KEY } from "../theme/theme";

// A fixture projection, unverified, with a reported (unverified) publication.
function fixtureSnapshot() {
  return snapshotOf({
    items: [
      messageItem({ id: "m1", sequence: 1, role: "user", text: "list the files" }),
      messageItem({ id: "m2", sequence: 2, role: "assistant", text: "Listing them." }),
      decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
      resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded" }),
    ],
    panels: { ...panelsDefault(), publication: { state: "published" } },
  });
}

let shim: MatchMediaShim | null = null;
let mounted: Mounted | null = null;
let fake: FakeWorkspace;

function renderShell(width: number): void {
  shim = installMatchMedia(width);
  fake = fakeWorkspace(fixtureSnapshot());
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

function byLabel(text: string): HTMLElement {
  const match = Array.from(mounted?.container.querySelectorAll<HTMLElement>("button") ?? []).find(
    (el) => el.textContent?.trim() === text,
  );
  if (!match) throw new Error(`no button "${text}"`);
  return match;
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
  window.localStorage.removeItem(THEME_STORAGE_KEY);
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
    expect(q("footer").getAttribute("aria-label")).toBe("Observed session metrics");
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
  it("shows the claim boundary before any snapshot arrives", () => {
    renderShell(1440);
    const boundary = q("[role='note'][aria-label='Claim boundary']");
    expect(boundary.textContent).toContain("Presentation only");
    expect(boundary.textContent).toContain("G1");
    expect(boundary.textContent).toContain("unexecuted");
    expect(boundary.textContent).toContain("not-established");
    expect(boundary.textContent).toContain("claim-bearing");
  });

  it("shows no session and no rows before a projection is loaded", () => {
    renderShell(1440);
    expect(q("nav").textContent).toContain("No session projection");
    expect(q("nav").textContent).toContain("No streams projected.");
    expect(q("aside").textContent).toContain("No warnings projected.");
  });

  it("keeps the rail read-only: it has no buttons, links or controls", async () => {
    renderShell(1440);
    await settle();
    expect(q("nav").querySelectorAll("button, a, input, select, textarea").length).toBe(0);
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
    expect(q("#view-panel-trajectory").textContent).toContain("Phase 3c");
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
    const section = q("section[aria-labelledby='panel-publication']");
    expect(section.textContent).toContain("reported");
    expect(section.textContent).not.toMatch(/\bpublished\b/);
  });

  it("shows the footer's untracked metrics as unavailable, with a reason, not as zero", async () => {
    renderShell(1440);
    await settle();
    const footer = q("footer");
    expect(footer.textContent).toContain("Token usage");
    expect(footer.textContent).toContain("unavailable");
    expect(footer.textContent).toContain("the projection records no duration");
  });

  it("shows the policy counts for the retained window", async () => {
    renderShell(1440);
    await settle();
    expect(q("footer").textContent).toContain("ALLOW 1 · DENY 0 · UNKNOWN 0");
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

  it("is a column with no toggle state at wide widths", async () => {
    renderShell(1440);
    await settle();
    expect(byLabel("Governance").getAttribute("aria-expanded")).toBeNull();
    expect(q("aside").hasAttribute("inert")).toBe(false);
    expect(q("aside").hasAttribute("aria-hidden")).toBe(false);
  });

  it("re-evaluates when the viewport crosses the breakpoint", async () => {
    renderShell(1440);
    await settle();
    shim?.setWidth(900);
    expect(q("aside").hasAttribute("inert")).toBe(true);
    shim?.setWidth(1440);
    expect(q("aside").hasAttribute("inert")).toBe(false);
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

describe("Shell: markup", () => {
  it("renders no inline style attribute and no event-handler attribute", async () => {
    renderShell(1440);
    await settle();
    const html = mounted?.container.innerHTML ?? "";
    expect(html).not.toMatch(/\sstyle\s*=/);
    expect(html).not.toMatch(/\son[a-z]+\s*=/);
    expect(html).not.toMatch(/<script/i);
  });
});
