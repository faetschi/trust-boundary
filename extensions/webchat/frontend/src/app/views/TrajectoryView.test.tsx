// @vitest-environment happy-dom
// The Trajectory view, mounted inside the real shell and its store. The shell supplies the
// drawer, the focus contract and the store's live stream, so these tests drive the same path the
// browser does: a click or a key, then the store, then the render.
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CONTRACT_VERSION } from "../../core/contract";
import {
  activityItem,
  decisionItem,
  messageItem,
  panelsDefault,
  resultItem,
  snapshotOf,
} from "../../core/testing/builders";
import { click, installMatchMedia, mount, press, settle, type MatchMediaShim, type Mounted } from "../../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../../test-support/fakes";
import { THEME_STORAGE_KEY } from "../../theme/theme";
import { Shell } from "../Shell";
import { WorkspaceProvider } from "../WorkspaceProvider";

// The legacy render fixture (internal/webchat/jstest/render.test.mjs): two correlated tool blocks,
// one policy ALLOW with an unverified success, and one policy DENY with a denied result.
function legacyFixture() {
  return [
    messageItem({ id: "m1", sequence: 1, turn_id: "t1", role: "user", text: "hello world" }),
    activityItem({ id: "a1", sequence: 2, turn_id: "t1", tool: "bash", tool_call_id: "c1", state: "pending", summary: "bash: go test" }),
    decisionItem({ id: "d1", sequence: 3, turn_id: "t1", tool: "bash", tool_call_id: "c1", verdict: "ALLOW", reason_code: "policy_rule_allow" }),
    resultItem({ id: "r1", sequence: 4, turn_id: "t1", tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" }),
    activityItem({ id: "a2", sequence: 5, turn_id: "t1", tool: "bash", tool_call_id: "c2", state: "pending", summary: "bash: curl https://example.com" }),
    decisionItem({ id: "d2", sequence: 6, turn_id: "t1", tool: "bash", tool_call_id: "c2", verdict: "DENY", reason_code: "policy_rule_deny", policy_digest: "sha256:cccc" }),
    resultItem({ id: "r2", sequence: 7, turn_id: "t1", tool: "bash", tool_call_id: "c2", state: "denied", summary: "denial was durable" }),
  ];
}

let shim: MatchMediaShim | null = null;
let mounted: Mounted | null = null;
let fake: FakeWorkspace;

// Mounts the shell on the trajectory view. The snapshot is loaded before the view is opened.
// A live snapshot is the only source that may carry a verified (observed) record; the validator
// refuses one from a fixture.
interface SnapshotOptions {
  readonly source?: "fixture" | "live";
  readonly verification_scope?: "unverified-input" | "structural-audit-only";
}

async function openTrajectory(items = legacyFixture(), width = 1440, options: SnapshotOptions = {}) {
  shim = installMatchMedia(width);
  fake = fakeWorkspace(snapshotOf({ items, panels: panelsDefault(), ...options }));
  mounted = mount(
    <WorkspaceProvider deps={fake.deps}>
      <Shell version="0.1.0" />
    </WorkspaceProvider>,
  );
  await settle();
  const tab = Array.from(mounted.container.querySelectorAll<HTMLButtonElement>("[role='tab']")).find(
    (button) => button.textContent === "Trajectory",
  );
  if (!tab) throw new Error("no Trajectory tab");
  click(tab);
  await settle();
  return mounted.container;
}

function root(): HTMLElement {
  const panel = mounted?.container.querySelector<HTMLElement>("#view-panel-trajectory");
  if (!panel) throw new Error("no trajectory panel");
  return panel;
}

function rows(): HTMLButtonElement[] {
  return Array.from(root().querySelectorAll<HTMLButtonElement>("button[data-record-row]"));
}

function rowFor(id: string): HTMLButtonElement {
  const row = rows().find((candidate) => candidate.dataset.id === id);
  if (!row) throw new Error(`no row ${id}`);
  return row;
}

function ledger(): HTMLElement {
  const region = root().querySelector<HTMLElement>("[role='region'][aria-label='Record ledger']");
  if (!region) throw new Error("no ledger");
  return region;
}

function aside(): HTMLElement {
  const element = mounted?.container.querySelector<HTMLElement>("aside");
  if (!element) throw new Error("no aside");
  return element;
}

function buttonIn(container: HTMLElement, text: string): HTMLButtonElement {
  const match = Array.from(container.querySelectorAll<HTMLButtonElement>("button")).find(
    (button) => button.textContent?.trim() === text,
  );
  if (!match) throw new Error(`no button "${text}"`);
  return match;
}

function searchInput(): HTMLInputElement {
  const input = root().querySelector<HTMLInputElement>("input[type='search']");
  if (!input) throw new Error("no search input");
  return input;
}

// A controlled input in React reacts to the native value setter followed by an input event.
function typeInto(input: HTMLInputElement, value: string): void {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

// Focus and scroll are browser events; inside act they flush the same way a user's would.
function focusEl(element: HTMLElement): void {
  act(() => {
    element.focus();
  });
}

function scrollPage(): void {
  act(() => {
    window.dispatchEvent(new Event("scroll"));
  });
}

function pressedGroup(): string | null {
  const pressed = Array.from(root().querySelectorAll("[role='toolbar'] button[aria-pressed='true']"));
  return pressed[0]?.textContent?.trim() ?? null;
}

function groupTitles(): string[] {
  return Array.from(ledger().querySelectorAll("[role='group']")).map((group) => {
    const id = group.getAttribute("aria-labelledby") ?? "";
    return document.getElementById(id)?.textContent ?? "";
  });
}

// The scroll metrics of the document, as the tail window reads them. Happy-dom has no layout.
function setPageMetrics(scrollHeight: number, clientHeight: number, scrollTop: number): void {
  const element = document.documentElement;
  Object.defineProperty(element, "scrollHeight", { configurable: true, value: scrollHeight });
  Object.defineProperty(element, "clientHeight", { configurable: true, value: clientHeight });
  let top = scrollTop;
  Object.defineProperty(element, "scrollTop", {
    configurable: true,
    get: () => top,
    set: (value: number) => {
      top = value;
    },
  });
}

function clearPageMetrics(): void {
  for (const name of ["scrollHeight", "clientHeight", "scrollTop"]) {
    delete (document.documentElement as unknown as Record<string, unknown>)[name];
  }
}

// Sends one live item over the fake stream, as the gateway's SSE `item` event carries it.
function emitItem(item: unknown, cursor: string): void {
  act(() => {
    fake.sources[0]?.emit("item", { schema_version: CONTRACT_VERSION, cursor, kind: "item", item });
  });
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
  clearPageMetrics();
  vi.restoreAllMocks();
  vi.useRealTimers();
  window.localStorage.removeItem(THEME_STORAGE_KEY);
  document.documentElement.removeAttribute("data-theme");
});

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("the ledger: one row per record, a source chip, a dot and its status", () => {
  it("renders one record row per projected element, in sequence order, with a source chip", async () => {
    await openTrajectory();
    expect(rows().map((row) => row.dataset.source)).toEqual(["user", "tool", "decision", "result", "tool", "decision", "result"]);
    for (const row of rows()) {
      expect(row.querySelectorAll("span[aria-hidden='true'][data-tone]")).toHaveLength(1);
    }
    expect(ledger().textContent).toContain("reason policy_rule_deny");
    expect(ledger().textContent).toContain("go test");
  });

  it("never renders an unverified success as confirmed, and gives the reported tone to its dot", async () => {
    await openTrajectory();
    const success = rowFor("r1");
    expect(success.textContent).toContain("reported");
    expect(success.textContent).not.toContain("succeeded");
    expect(success.querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("reported");
    expect(ledger().querySelectorAll("[data-tone='ok']")).toHaveLength(0);
    expect(rowFor("d1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("allow");
  });

  it("states each row's status in words, and never calls an ALLOW or an unverified success a success", async () => {
    await openTrajectory();
    const label = (id: string) => rowFor(id).getAttribute("aria-label") ?? "";
    expect(label("d1")).toMatch(/Policy verdict: ALLOW \(permission, not an outcome\)/);
    expect(label("d2")).toMatch(/Policy verdict: DENY \(refusal, not an outcome\)/);
    expect(label("r1")).toMatch(/Result: succeeded, reported, not independently verified/);
    expect(label("r2")).toMatch(/Result: denied, not success/);
    for (const id of ["d1", "r1"]) {
      expect(label(id)).not.toMatch(/\bsuccess\b/i);
    }
    for (const row of rows()) {
      expect(row.querySelector("span[aria-hidden='true']")?.getAttribute("aria-hidden")).toBe("true");
    }
  });

  it("a policy ALLOW beside a lost result: no green or ok tone anywhere on the rail or in a dot", async () => {
    await openTrajectory([
      decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
      resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "lost" }),
    ]);
    expect(root().querySelectorAll("[data-tone='ok']")).toHaveLength(0);
    expect(rowFor("d1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("allow");
    expect(rowFor("r1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("lost");
    const policy = laneTrack("Policy");
    expect(policy.querySelector("[data-tone='allow']")).not.toBeNull();
    expect(policy.getAttribute("aria-label")).not.toMatch(/succeeded|verified,/);
    expect(laneTrack("Tools").getAttribute("aria-label")).toMatch(/LOST, not success/);
    expect(laneTrack("Tools").querySelector("[data-tone='lost']")).not.toBeNull();
    expect(laneTrack("Tools").querySelector("[data-tone='deny']")).toBeNull();
  });

  it("a LOST result beside a DENY differs by tone and by words, and a LOST outcome never reads as a refusal", async () => {
    await openTrajectory([
      decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "DENY" }),
      resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "lost" }),
      decisionItem({ id: "d2", sequence: 3, tool: "bash", tool_call_id: "c2", verdict: "DENY" }),
      resultItem({ id: "r2", sequence: 4, tool: "bash", tool_call_id: "c2", state: "denied" }),
    ]);
    const dot = (id: string) => rowFor(id).querySelector("span[aria-hidden='true']")?.getAttribute("data-tone");
    expect(dot("r1")).toBe("lost");
    expect(dot("r2")).toBe("deny");
    expect(rowFor("r1").getAttribute("aria-label")).toMatch(/Result: LOST, not success/);
    expect(rowFor("r1").getAttribute("aria-label")).not.toMatch(/\bDENY\b|denied/);
    expect(rowFor("r1").textContent).toContain("lost");
    const tools = laneTrack("Tools");
    expect(tools.querySelectorAll("[data-tone='lost']")).toHaveLength(1);
    expect(tools.querySelectorAll("[data-tone='deny']")).toHaveLength(1);
  });

  it("UNKNOWN verdict and UNKNOWN result stay unknown and are never promoted", async () => {
    await openTrajectory([
      decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "UNKNOWN" }),
      resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "unknown" }),
    ]);
    expect(rowFor("d1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("unknown");
    expect(rowFor("r1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("unknown");
    expect(rowFor("r1").getAttribute("aria-label")).toMatch(/Result: UNKNOWN, not success/);
    expect(root().querySelectorAll("[data-tone='ok'], [data-tone='allow']")).toHaveLength(0);
  });

  it("a verified success reads succeeded, and a lossy one takes the unknown tone and is not counted as confirmed", async () => {
    await openTrajectory(
      [
        resultItem({ id: "ok1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" }),
        resultItem({ id: "lossy1", sequence: 2, tool: "bash", tool_call_id: "c2", state: "succeeded", lossy: true, authority: "observed", verification_scope: "structural-audit-only" }),
      ],
      1440,
      { source: "live", verification_scope: "structural-audit-only" },
    );
    expect(rowFor("ok1").querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("ok");
    expect(rowFor("ok1").getAttribute("aria-label")).toMatch(/Result: succeeded, verified\./);
    const lossy = rowFor("lossy1");
    expect(lossy.querySelector("span[aria-hidden='true']")?.getAttribute("data-tone")).toBe("unknown");
    expect(lossy.getAttribute("aria-label")).toMatch(/succeeded, output lossy, not counted as confirmed/);
    expect(lossy.textContent).toContain("succeeded (output lossy, not confirmed)");
    expect(lossy.textContent).toContain("lossy");
    expect(lossy.querySelector("[data-tone='ok']")).toBeNull();
    expect(lossy.querySelector("[data-tone='unknown']")).not.toBeNull();
  });

  it("marks an unverified verdict with an explicit unverified marker", async () => {
    await openTrajectory();
    expect(rowFor("d1").textContent).toContain("unverified");
  });

  it("carries the anchor key the tail window pins to, on every row", async () => {
    await openTrajectory();
    expect(rows().map((row) => row.dataset.anchor)).toEqual(["m1", "a1", "d1", "r1", "a2", "d2", "r2"]);
    expect(rows()[0]?.dataset.anchor).toBe("m1");
  });

  it("is a named region, not a list, and its rows carry no inline style", async () => {
    await openTrajectory();
    expect(ledger().getAttribute("role")).toBe("region");
    expect(ledger().querySelector("[role='list'], ul, ol")).toBeNull();
    expect(root().querySelectorAll("[style]")).toHaveLength(0);
  });
});

describe("the sequence rail: three lanes over every retained record", () => {
  it("has one lane each for Model, Policy and Tools, with an ordinal axis note", async () => {
    await openTrajectory();
    expect(root().querySelectorAll("[role='img']")).toHaveLength(3);
    expect(root().querySelector("[data-ordinal='true']")?.textContent).toMatch(/The axis is record sequence, not time/);
    expect(root().querySelector("[data-ordinal='true']")?.textContent).toMatch(/no span here is drawn as one/);
  });

  it("draws every segment as a weight class with no inline style (CSP)", async () => {
    await openTrajectory();
    const segments = Array.from(root().querySelectorAll("[role='img'] > span"));
    expect(segments.length).toBeGreaterThan(0);
    for (const segment of segments) {
      expect(segment.getAttribute("style")).toBeNull();
      expect(segment.className).toMatch(/seg-w[1-8]/);
    }
  });

  it("names each lane's segment tones in words", async () => {
    await openTrajectory();
    const tools = laneTrack("Tools");
    expect(tools.getAttribute("aria-label")).toMatch(/pending, no outcome yet/);
    expect(tools.getAttribute("aria-label")).toMatch(/succeeded, reported, not independently verified/);
    expect(tools.getAttribute("aria-label")).toMatch(/DENY, denied or failed, not success/);
    expect(tools.getAttribute("aria-label")).not.toMatch(/lost/);
  });

  it("gives an UNKNOWN result its own unknown segment, named in words, never a lane that did not act", async () => {
    await openTrajectory([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "unknown" })]);
    expect(laneTrack("Tools").querySelector("[data-tone='unknown']")).not.toBeNull();
    expect(laneTrack("Tools").getAttribute("aria-label")).toMatch(/UNKNOWN, not success/);
    expect(laneTrack("Tools").getAttribute("aria-label")).not.toMatch(/no record in this lane/);
  });

  it("draws a message as idle in the Model lane, not as a lane that did not act", async () => {
    await openTrajectory();
    expect(laneTrack("Model").querySelector("[data-tone='idle']")).not.toBeNull();
    expect(laneTrack("Model").getAttribute("aria-label")).toMatch(/message, no verdict or outcome/);
  });
});

function laneTrack(label: string): HTMLElement {
  const track = root().querySelector<HTMLElement>(`[role='img'][aria-label^='${label} lane']`);
  if (!track) throw new Error(`no ${label} lane`);
  return track;
}

describe("toolbar: grouping and search", () => {
  it("groups by Turn, Tool or Verdict over the same records, and never drops one", async () => {
    await openTrajectory();
    expect(pressedGroup()).toBe("Turn");
    expect(groupTitles()).toEqual(["Turn t1"]);
    click(buttonIn(root(), "Verdict"));
    expect(pressedGroup()).toBe("Verdict");
    expect(groupTitles()).toEqual(["No verdict", "ALLOW", "DENY"]);
    expect(rows()).toHaveLength(7);
    click(buttonIn(root(), "Tool"));
    expect(groupTitles()).toEqual(["Messages", "bash", "bash"]);
    expect(rows()).toHaveLength(7);
  });

  it("keeps exactly one group button in the tab order, on the pressed group", async () => {
    await openTrajectory();
    click(buttonIn(root(), "Verdict"));
    const stops = Array.from(root().querySelectorAll<HTMLButtonElement>("[role='toolbar'] button")).map((button) =>
      button.getAttribute("tabindex"),
    );
    expect(stops).toEqual(["-1", "-1", "0"]);
  });

  it("narrows the ledger on search, and says how many of how many match", async () => {
    await openTrajectory();
    typeInto(searchInput(), "curl");
    expect(rows()).toHaveLength(1);
    expect(root().textContent).toContain("1 of 7 record(s) matching “curl”");
  });

  it("searches only allowlisted bounded fields: a policy digest is not searchable", async () => {
    await openTrajectory();
    typeInto(searchInput(), "cccc");
    expect(rows()).toHaveLength(0);
    expect(ledger().textContent).toContain("No matching records");
  });

  it("narrows the ledger but never the rail, which still shows the DENY and every retained record", async () => {
    await openTrajectory();
    typeInto(searchInput(), "curl");
    expect(laneTrack("Policy").querySelector("[data-tone='deny']")).not.toBeNull();
    expect(root().querySelector("[data-ordinal='true']")?.textContent).toContain("always shows all 7 retained records");
  });

  it("labels the search field, and the grouping as a labelled toolbar", async () => {
    await openTrajectory();
    expect(searchInput().closest("label")?.textContent).toContain("Search records");
    expect(root().querySelector("[role='toolbar']")?.getAttribute("aria-label")).toBe("Group records by");
  });
});

describe("the view leaves the projection alone", () => {
  it("switching to the trajectory view and grouping or searching never changes the records", async () => {
    await openTrajectory();
    click(buttonIn(root(), "Verdict"));
    typeInto(searchInput(), "deny");
    typeInto(searchInput(), "");
    expect(rows()).toHaveLength(7);
    expect(root().textContent).toContain("7 record(s) · grouped by Verdict");
  });

  it("shows an empty state when no record is projected, and the rail says there is nothing to chart", async () => {
    await openTrajectory([]);
    expect(ledger().textContent).toContain("No records");
    expect(ledger().textContent).toContain("No record has been projected yet.");
    expect(root().textContent).toContain("No records to chart.");
  });
});

describe("keyboard: a roving tab stop, arrows, Home and End", () => {
  it("has one row in the tab order, and ArrowDown, ArrowUp, Home and End move focus through the ledger", async () => {
    await openTrajectory();
    const stops = rows().filter((row) => row.getAttribute("tabindex") === "0");
    expect(stops.map((row) => row.dataset.id)).toEqual(["m1"]);
    focusEl(rowFor("m1"));
    press(rowFor("m1"), "ArrowDown");
    expect(document.activeElement).toBe(rowFor("a1"));
    expect(rowFor("a1").getAttribute("tabindex")).toBe("0");
    expect(rowFor("m1").getAttribute("tabindex")).toBe("-1");
    press(rowFor("a1"), "End");
    expect(document.activeElement).toBe(rowFor("r2"));
    press(rowFor("r2"), "ArrowDown");
    expect(document.activeElement).toBe(rowFor("r2"));
    press(rowFor("r2"), "Home");
    expect(document.activeElement).toBe(rowFor("m1"));
    press(rowFor("m1"), "ArrowUp");
    expect(document.activeElement).toBe(rowFor("m1"));
  });

  it("moves across group boundaries in the ledger", async () => {
    await openTrajectory([
      messageItem({ id: "m1", sequence: 1, turn_id: "t1", text: "one" }),
      messageItem({ id: "m2", sequence: 2, turn_id: "t2", text: "two" }),
    ]);
    focusEl(rowFor("m1"));
    press(rowFor("m1"), "ArrowDown");
    expect(document.activeElement).toBe(rowFor("m2"));
  });

  it("selects on click, which is what Enter and Space do on a button, and a second click clears the selection", async () => {
    await openTrajectory();
    click(rowFor("d1"));
    expect(rowFor("d1").getAttribute("aria-pressed")).toBe("true");
    click(rowFor("d1"));
    expect(rowFor("d1").getAttribute("aria-pressed")).toBe("false");
  });
});

describe("selecting a record: the inspector, the drawer and the focus contract", () => {
  it("swaps the aside to the record inspector for the selected record, with a duration stated as unavailable", async () => {
    await openTrajectory();
    click(rowFor("d2"));
    expect(aside().querySelector("h2")?.textContent).toBe("Record");
    expect(aside().querySelector("[data-section='decision']")).not.toBeNull();
    expect(aside().textContent).toContain("unavailable — the projection records no duration");
    expect(rowFor("d2").getAttribute("aria-pressed")).toBe("true");
    expect(rows().filter((row) => row.getAttribute("aria-pressed") === "true").map((row) => row.dataset.id)).toEqual(["d2"]);
  });

  it("at wide widths, Close record returns focus to the row that opened the record", async () => {
    await openTrajectory();
    focusEl(rowFor("d2"));
    click(rowFor("d2"));
    const close = buttonIn(aside(), "Close record");
    focusEl(close);
    click(close);
    expect(rowFor("d2").getAttribute("aria-pressed")).toBe("false");
    expect(document.activeElement).toBe(rowFor("d2"));
  });

  it("at wide widths, Escape clears the record and keeps focus on its row", async () => {
    await openTrajectory();
    focusEl(rowFor("d2"));
    click(rowFor("d2"));
    press(rowFor("d2"), "Escape");
    expect(rowFor("d2").getAttribute("aria-pressed")).toBe("false");
    expect(document.activeElement).toBe(rowFor("d2"));
  });

  it("at the drawer breakpoint, selecting a record opens the drawer and moves focus into it", async () => {
    await openTrajectory(legacyFixture(), 1024);
    expect(aside().getAttribute("data-open")).toBe("false");
    click(rowFor("d2"));
    expect(aside().getAttribute("data-open")).toBe("true");
    expect(document.activeElement).toBe(aside().querySelector("h2"));
  });

  it("at the drawer breakpoint, Escape closes the drawer and returns focus to the row", async () => {
    await openTrajectory(legacyFixture(), 1024);
    click(rowFor("d2"));
    press(document.activeElement ?? rowFor("d2"), "Escape");
    expect(aside().getAttribute("data-open")).toBe("false");
    expect(document.activeElement).toBe(rowFor("d2"));
  });

  it("at the drawer breakpoint, Close record closes the drawer and returns focus to the row", async () => {
    await openTrajectory(legacyFixture(), 1024);
    click(rowFor("d2"));
    click(buttonIn(aside(), "Close record"));
    expect(aside().getAttribute("data-open")).toBe("false");
    expect(document.activeElement).toBe(rowFor("d2"));
  });

  it("clears the selection when its record is evicted, and falls back to the ledger", async () => {
    const window512 = Array.from({ length: 512 }, (_, index) => messageItem({ id: `m${index + 1}`, sequence: index + 1, text: `m${index + 1}` }));
    await openTrajectory(window512);
    const oldest = rowFor("m1");
    click(oldest);
    focusEl(oldest);
    expect(aside().querySelector("h2")?.textContent).toBe("Record");
    emitItem(messageItem({ id: "m513", sequence: 513, text: "m513" }), "e1:513");
    expect(aside().querySelector("h2")?.textContent).toBe("Governance");
    expect(mounted?.container.querySelector("[data-id='m1']")).toBeNull();
    expect(document.activeElement).toBe(ledger());
  });
});

describe("the tail window: the count comes only from records the store applied", () => {
  it("shows no count while at the tail, and counts live records once the reader is away", async () => {
    await openTrajectory(legacyFixture().slice(0, 3));
    setPageMetrics(4000, 800, 0);
    scrollPage();
    await settle();
    expect(jumpButton()).toBeUndefined();
    emitItem(messageItem({ id: "m4", sequence: 4, text: "live one" }), "e1:4");
    await settle();
    expect(jumpButton()?.textContent).toBe("1 new record · Jump to latest");
  });

  it("a duplicate of a record already applied adds nothing to the count", async () => {
    await openTrajectory(legacyFixture().slice(0, 3));
    setPageMetrics(4000, 800, 0);
    scrollPage();
    emitItem(messageItem({ id: "m4", sequence: 4, text: "live one" }), "e1:4");
    await settle();
    emitItem(messageItem({ id: "m4", sequence: 4, text: "live one" }), "e1:4");
    await settle();
    expect(jumpButton()?.textContent).toBe("1 new record · Jump to latest");
  });

  it("Jump to latest scrolls to the tail and moves focus to the ledger", async () => {
    await openTrajectory(legacyFixture().slice(0, 3));
    setPageMetrics(4000, 800, 0);
    scrollPage();
    emitItem(messageItem({ id: "m4", sequence: 4, text: "live one" }), "e1:4");
    await settle();
    const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
    click(jumpButton() as HTMLButtonElement);
    expect(scroll).toHaveBeenCalledWith(0, 3200);
    expect(jumpButton()).toBeUndefined();
    expect(document.activeElement).toBe(ledger());
  });

  it("a reader at the tail is followed to the new records", async () => {
    await openTrajectory(legacyFixture().slice(0, 3));
    setPageMetrics(4000, 800, 3200);
    const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
    emitItem(messageItem({ id: "m4", sequence: 4, text: "live one" }), "e1:4");
    await settle();
    expect(scroll).toHaveBeenCalledWith(0, 3200);
  });

  it("announces a settled burst of arrivals once, through the status region", async () => {
    await openTrajectory(legacyFixture().slice(0, 3));
    setPageMetrics(4000, 800, 0);
    scrollPage();
    vi.useFakeTimers();
    emitItem(messageItem({ id: "m4", sequence: 4, text: "a" }), "e1:4");
    emitItem(messageItem({ id: "m5", sequence: 5, text: "b" }), "e1:5");
    act(() => {
      vi.advanceTimersByTime(400);
    });
    expect(mounted?.container.querySelector("[role='status']")?.textContent).toContain("2 new records below");
    vi.useRealTimers();
  });
});

function jumpButton(): HTMLButtonElement | undefined {
  return Array.from(root().querySelectorAll<HTMLButtonElement>("button")).find((button) =>
    button.textContent?.includes("Jump to latest"),
  );
}
