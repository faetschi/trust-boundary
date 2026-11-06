// @vitest-environment happy-dom
// The Chat view. The legacy render.test.mjs cases owned by 3b are ported here (rows 1, 2, 11-14,
// 25-28, 37 and S3-S4 of docs/phase3-parity.md), and the honesty rules of web-chat.md sections 4.1,
// 4.3 and 4.5 are asserted on the DOM. Most cases run under the real store, fed by a fake stream.

import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  activityItem,
  decisionItem,
  messageItem,
  panelsDefault,
  resultItem,
  sessionDefault,
  snapshotOf,
} from "../../core/testing/builders";
import { CONTRACT_VERSION, type Item, type SessionHeader, type Snapshot, type VerificationScope } from "../../core/contract";
import { itemsOf } from "../../test-support/toolBlocks";
import { fakeWorkspace, type FakeWorkspace } from "../../test-support/fakes";
import { click, mount, settle, type Mounted } from "../../test-support/dom";
import { AnnouncerContext, useAnnouncer } from "../../ui/LiveRegion";
import { createAnnouncer, type Announcer } from "../../ui/announcer";
import { WorkspaceProvider, useWorkspace } from "../WorkspaceProvider";
import { createWorkspaceStore, type WorkspaceModel } from "../workspaceStore";
import { WINDOW_GAP_LABEL, WINDOW_GAP_TEXT } from "../status";
import { ChatView, TRANSCRIPT_NOTICE_POINTER } from "./ChatView";
import { HelperBar } from "../HelperBar";
import { TAIL_ANNOUNCE_DELAY_MS } from "./chat/tail";
import { COPIED_MS } from "./chat/chrome";

let latest: WorkspaceModel | null = null;
let liveAnnouncer: Announcer | null = null;

// Renders the view from the live store: the same wiring the shell uses.
function Probe() {
  const model = useWorkspace();
  latest = model;
  liveAnnouncer = useAnnouncer();
  // The identity and the posture counts live in the details card (HelperBar.tsx), so the probe draws it beside the view.
  return (
    <>
      <ChatView model={model} />
      <HelperBar model={model} open version="test" />
    </>
  );
}

let mounted: Mounted | null = null;
let fake: FakeWorkspace | null = null;
let restorePage: (() => void) | null = null;

async function renderLive(snapshot: Snapshot): Promise<FakeWorkspace> {
  const source = fakeWorkspace(snapshot);
  fake = source;
  mounted = mount(
    <WorkspaceProvider deps={source.deps}>
      <Probe />
    </WorkspaceProvider>,
  );
  await settle();
  act(() => {
    source.sources[0]?.open();
  });
  return source;
}

// Renders the view from a fixed model, for states the stream cannot produce on its own.
function renderModel(model: WorkspaceModel): Mounted {
  const announcer = createAnnouncer();
  liveAnnouncer = announcer;
  mounted = mount(
    <AnnouncerContext.Provider value={announcer}>
      <ChatView model={model} />
    </AnnouncerContext.Provider>,
  );
  return mounted;
}

function rerenderModel(model: WorkspaceModel): void {
  const root = mounted?.root;
  if (!root) throw new Error("nothing is mounted");
  act(() => {
    root.render(
      <AnnouncerContext.Provider value={createAnnouncer()}>
        <ChatView model={model} />
      </AnnouncerContext.Provider>,
    );
  });
}

function emitItem(item: Item): void {
  act(() => {
    fake?.sources[0]?.emit("item", { schema_version: CONTRACT_VERSION, cursor: `e1:${item.sequence}`, kind: "item", item });
  });
}

function q<T extends Element = HTMLElement>(selector: string): T {
  const element = mounted?.container.querySelector<T>(selector);
  if (!element) throw new Error(`no element for ${selector}`);
  return element;
}

function qa<T extends Element = HTMLElement>(selector: string): T[] {
  return Array.from(mounted?.container.querySelectorAll<T>(selector) ?? []);
}

// The chip texts and tones in the view, in document order.
function chips(): Array<readonly [string, string | null]> {
  return qa("[data-tone]")
    .filter((element) => element.tagName === "SPAN")
    .map((element) => [element.textContent?.trim() ?? "", element.getAttribute("data-tone")] as const);
}

function button(text: string): HTMLButtonElement {
  const match = qa<HTMLButtonElement>("button").find((element) => element.textContent?.trim().startsWith(text));
  if (!match) throw new Error(`no button starting "${text}"`);
  return match;
}

// The Copy control in one of its states. It is icon-only, so its accessible name, not its text, says the state and the message.
function copyControl(state: string): HTMLButtonElement {
  const match = qa<HTMLButtonElement>("button").find((element) => element.getAttribute("aria-label")?.startsWith(`${state}:`));
  if (!match) throw new Error(`no copy control reading "${state}"`);
  return match;
}

function hasJumpControl(): boolean {
  return qa("button").some((element) => element.textContent?.includes("Jump to latest"));
}

// The line under the filter toolbar: the toolbar's sibling paragraph.
// The count of the retained window, on the head's title row.
function summaryLine(): string {
  return q("[data-summary]").textContent ?? "";
}

// The fixture projection of the legacy suite: an ALLOW with a reported result, and a DENY with a denied result.
function fixtureItems(): Item[] {
  return [
    messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" }),
    activityItem({ id: "a1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "pending", summary: "bash: go test" }),
    decisionItem({ id: "d1", sequence: 3, tool: "bash", tool_call_id: "c1", verdict: "ALLOW", reason_code: "policy_rule_allow" }),
    resultItem({ id: "r1", sequence: 4, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" }),
    activityItem({ id: "a2", sequence: 5, tool: "bash", tool_call_id: "c2", state: "pending", summary: "bash: curl https://example.com" }),
    decisionItem({ id: "d2", sequence: 6, tool: "bash", tool_call_id: "c2", verdict: "DENY", reason_code: "policy_rule_deny" }),
    resultItem({ id: "r2", sequence: 7, tool: "bash", tool_call_id: "c2", state: "denied", summary: "denial was durable" }),
  ];
}

function fixtureSnapshot(items: Item[] = fixtureItems()): Snapshot {
  return snapshotOf({
    items,
    panels: { ...panelsDefault(), governance: { allow_count: 1, deny_count: 1, unknown_count: 0 } },
  });
}

// A live projection in which the gateway has verified the items. Only these can read as succeeded.
function verifiedSnapshot(items: Item[]): Snapshot {
  return snapshotOf({ source: "live", verification_scope: "structural-audit-only", items });
}

function observed<T extends { authority: string; verification_scope: string }>(item: T): T {
  return { ...item, authority: "observed", verification_scope: "structural-audit-only" };
}

// Stubs the transcript's scroll metrics. The transcript is the scroller (the view fills the column, and the
// log scrolls inside it), so a 5000 px transcript in a 500 px viewport is away from the tail at the top. The
// stub applies to every log element, so it is installed before the view mounts. happy-dom keeps the scrollTop
// it is given, so a jump is observable.
function stubTranscript(heights: { readonly scroll: number; readonly view: number }): void {
  const proto = HTMLElement.prototype;
  Object.defineProperty(proto, "scrollHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "log" ? heights.scroll : 0;
    },
  });
  Object.defineProperty(proto, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("role") === "log" ? heights.view : 0;
    },
  });
  restorePage = () => {
    delete (proto as unknown as Record<string, unknown>).scrollHeight;
    delete (proto as unknown as Record<string, unknown>).clientHeight;
  };
}

function transcript(): HTMLElement {
  return q("[role='log']");
}

// A paragraph of the view whose whole text is the given line.
function hasParagraph(text: string): boolean {
  return qa("p").some((element) => element.textContent === text);
}

// The role line of a message: the first paragraph of its block.
function roleRow(role: string): HTMLElement {
  return q(`div[data-role='${role}'] p`);
}

// A snapshot of the fixture records with the given session header (the Pi identity line is read from it).
function sessionSnapshot(session: Partial<SessionHeader>): Snapshot {
  return snapshotOf({ items: fixtureItems(), session: { ...sessionDefault(), ...session }, panels: panelsDefault() });
}

// A model from a real store with the given items. It is the input for the states that need a fixed
// model, and the store's actions are the real ones.
function modelOf(items: Item[], overrides: Partial<WorkspaceModel> = {}): WorkspaceModel {
  const store = createWorkspaceStore(fakeWorkspace(fixtureSnapshot()).deps);
  return {
    ...store.getSnapshot(),
    items,
    itemsById: new Map(items.map((item) => [item.id, item] as const)),
    loaded: true,
    error: null,
    ...overrides,
  };
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  fake = null;
  restorePage?.();
  restorePage = null;
  vi.useRealTimers();
  latest = null;
  document.body.innerHTML = "";
});

describe("the transcript: legacy cases from render.test.mjs", () => {
  it("renders the transcript, with the user message and the tool block", async () => {
    await renderLive(fixtureSnapshot());
    const text = q("[role='log']").textContent ?? "";
    expect(text).toContain("hello world");
    expect(text).toContain("bash: go test");
  });

  it("shows an unverified success as reported, never as succeeded or in the success tone", async () => {
    await renderLive(fixtureSnapshot());
    const found = chips();
    expect(found).toContainEqual(["reported", "reported"]);
    expect(found.some(([text]) => text === "succeeded")).toBe(false);
    expect(found.some(([, tone]) => tone === "ok")).toBe(false);
    expect(q("[role='log']").textContent).toContain("reported by the source, not independently verified");
  });

  it("states once, for the whole view, that a uniformly unverified projection is unverified", async () => {
    await renderLive(fixtureSnapshot());
    expect(q("[role='log']").textContent).toContain("All 7 records in this view are unverified · source: fixture");
    expect(qa("[data-tone='idle']").filter((element) => element.textContent === "unverified")).toHaveLength(0);
  });

  it("says the tool name once in a tool card's headline, then the summary without its repeat", async () => {
    await renderLive(fixtureSnapshot());
    const headline = qa("summary")[0]?.textContent ?? "";
    expect(headline.match(/bash/g)).toHaveLength(1);
    expect(headline).toContain("go test");
  });

  it("keeps the unverified wording in each tool card's accessible name, though no chip is drawn", async () => {
    await renderLive(fixtureSnapshot());
    const summaries = qa("[role='log'] summary");
    expect(summaries.length).toBeGreaterThan(0);
    for (const summary of summaries) expect(summary.textContent).toContain("unverified");
  });

  it("marks each item of a mixed projection by its own verification, and states no view-level line", () => {
    // The validator refuses a live projection that mixes unobserved and observed records, so the mixed
    // state is rendered from a model: the view's own branch is what is under test.
    renderModel(
      modelOf(
        [
          messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
          observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" })),
        ],
        { source: "live", verification: "structural-audit-only" },
      ),
    );
    expect(q("[role='log']").textContent).not.toContain("in this view are");
    expect(chips()).toContainEqual(["reported, not verified", "reported"]);
    expect(chips()).toContainEqual(["verified", "ok"]);
    expect(chips()).toContainEqual(["succeeded", "ok"]);
  });

  it("draws no speaker and no reading on a message where the view states its provenance once, and keeps both for assistive technology", async () => {
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "assistant", text: "hi" })]));
    const row = roleRow("assistant");
    const hidden = row.querySelector("[class*='hidden']");
    expect(hidden?.textContent).toBe("Pi, assistant reported, not verified");
    expect(q("[role='log']").textContent).toContain("The one record in this view");
  });

  it("draws the speaker and the reading of each message in a mixed view, where nothing states them once", () => {
    renderModel(
      modelOf(
        [
          messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
          observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" })),
        ],
        { source: "live", verification: "structural-audit-only" },
      ),
    );
    expect(roleRow("user").querySelector("[class*='hidden']")).toBeNull();
    expect(roleRow("user").textContent).toBe("User reported, not verified");
  });
  it("draws the success tone on a trace only when a terminal result exists, never for an activity alone", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(activityItem({ id: "a1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "bash: go test" })),
        observed(activityItem({ id: "a2", sequence: 2, tool: "bash", tool_call_id: "c2", state: "succeeded", summary: "bash: go vet" })),
        observed(resultItem({ id: "r2", sequence: 3, tool: "bash", tool_call_id: "c2", state: "succeeded", summary: "exit 0" })),
      ]),
    );
    expect(q("[data-anchor='c1'] details").getAttribute("data-tone")).toBe("idle");
    expect(q("[data-anchor='c2'] details").getAttribute("data-tone")).toBe("ok");
  });

  it("gives a verified lossy success in a mixed view a neutral evidence marker, never the green verified chip", () => {
    renderModel(
      modelOf(
        [
          messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
          observed(
            resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true, summary: "exit 0" }),
          ),
        ],
        { source: "live", verification: "structural-audit-only" },
      ),
    );
    expect(chips()).toContainEqual(["succeeded (output lossy, not counted as confirmed)", "unknown"]);
    expect(chips()).toContainEqual(["verified evidence, output lossy, not counted as confirmed", "idle"]);
    expect(chips()).not.toContainEqual(["verified", "ok"]);
    expect(chips().some(([, tone]) => tone === "ok")).toBe(false);
    // The card's accessible name carries the same reading, so a screen reader hears the lossy output too.
    expect(qa("summary")[0]?.textContent).toContain("verified evidence, output lossy, not counted as confirmed");
  });

  it("states once that a verified projection is verified, and draws no per-item marker on the tool card", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" })),
        observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" })),
      ]),
    );
    expect(q("[role='log']").textContent).toContain("All 2 records in this view are verified · source: live");
    // The message reads its own verdict of the gate; the tool card, in a uniform view, does not.
    expect(chips()).toContainEqual(["verified", "ok"]);
    expect(q("[data-anchor='c1'] summary").textContent).not.toMatch(/unverified|verified/);
  });

  it("states no provenance line for an empty projection", async () => {
    await renderLive(snapshotOf({ items: [] }));
    expect(q("[role='log']").textContent).not.toContain("in this view are");
  });

  it("renders the filter toolbar with pressed state and counts", async () => {
    await renderLive(fixtureSnapshot());
    const toolbar = q("[role='toolbar']");
    expect(toolbar.getAttribute("aria-label")).toBe("Timeline filters");
    const buttons = Array.from(toolbar.querySelectorAll("button"));
    expect(buttons.map((element) => element.textContent?.trim())).toEqual([
      "All 7",
      "Denied (verdict or result) 2",
      "Unknown or lossy 0",
      "Failed result 0",
    ]);
    expect(buttons[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(buttons[1]?.getAttribute("aria-pressed")).toBe("false");
    expect(buttons[1]?.getAttribute("title")).toMatch(/DENY verdict or a denied result/);
    expect(buttons[2]?.getAttribute("title")).toBe("Records whose verdict or result is unknown, or whose output is lossy");
    expect(summaryLine()).toBe("7 element(s) in the retained window");
  });

  it("words each posture count by what it counts: verdicts for decisions, results for executions", async () => {
    await renderLive(fixtureSnapshot());
    const posture = q("[role='group'][aria-label='Observed posture']").textContent ?? "";
    expect(posture).toMatch(/allow verdict 1/);
    expect(posture).toMatch(/deny verdict 1/);
    expect(posture).toMatch(/reported 1/);
    expect(posture).toMatch(/denied result 1/);
    expect(posture).not.toMatch(/verified 1/);
    expect(posture).not.toMatch(/ALLOW 1|DENY 1|denied 1/);
  });

  it("hides non-matching blocks and messages when the Denied filter is pressed", async () => {
    await renderLive(fixtureSnapshot());
    click(button("Denied (verdict or result)"));
    const text = q("[role='log']").textContent ?? "";
    expect(text).toContain("bash: curl");
    expect(text).not.toContain("hello world");
    expect(text).not.toContain("bash: go test");
    expect(button("Denied (verdict or result)").getAttribute("aria-pressed")).toBe("true");
    expect(summaryLine()).toBe("2 matches · 3 of 7 element(s) shown for “Denied (verdict or result)” (context included)");
  });

  it("renders the empty-filter state when no element matches", async () => {
    await renderLive(fixtureSnapshot());
    click(button("Failed result"));
    expect(q("[role='log']").textContent).toContain("No failed result findings");
    expect(summaryLine()).toBe("0 matches · 0 of 7 element(s) shown for “Failed result” (context included)");
  });

  it("renders assistant text as markdown and the same text from the user as literal", async () => {
    const text = "**Done.** See [docs](https://example.com/docs).";
    await renderLive(
      fixtureSnapshot([
        messageItem({ id: "m1", sequence: 1, role: "assistant", text }),
        messageItem({ id: "m2", sequence: 2, role: "user", text }),
      ]),
    );
    const assistant = q("div[data-role='assistant']");
    expect(assistant.querySelectorAll("strong").length).toBe(1);
    const link = assistant.querySelector("a");
    expect(link?.getAttribute("href")).toBe("https://example.com/docs");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer nofollow");
    const user = q("div[data-role='user']");
    expect(user.querySelectorAll("strong").length).toBe(0);
    expect(user.querySelectorAll("a").length).toBe(0);
    expect(user.textContent).toContain(text);
  });

  it("renders assistant markup from the model as literal text, never as an element", async () => {
    await renderLive(
      fixtureSnapshot([
        messageItem({ id: "m1", sequence: 1, role: "assistant", text: "<script>alert(1)</script> [x](javascript:alert(1))" }),
      ]),
    );
    const assistant = q("div[data-role='assistant']");
    expect(assistant.querySelectorAll("script").length).toBe(0);
    expect(assistant.querySelectorAll("a").length).toBe(0);
    expect(assistant.textContent).toContain("<script>alert(1)</script> [x](javascript:alert(1))");
  });

  it("keeps the completed blocks of a streaming assistant message across renders", () => {
    const first = messageItem({ id: "m2", sequence: 2, role: "assistant", text: "# Plan\n\nFirst paragraph.\n\nSecond" });
    const model = modelOf([first]);
    renderModel(model);
    const body = q("div.md-body");
    const heading = body.children[0];
    const paragraph = body.children[1];

    const grown = messageItem({
      id: "m2",
      sequence: 2,
      role: "assistant",
      text: "# Plan\n\nFirst paragraph.\n\nSecond paragraph grows.",
    });
    rerenderModel({ ...model, items: [grown], itemsById: new Map([["m2", grown]]) });

    expect(q("div.md-body")).toBe(body);
    expect(q("div.md-body").children[0]).toBe(heading);
    expect(q("div.md-body").children[1]).toBe(paragraph);
    expect(q("div.md-body").textContent).toContain("Second paragraph grows.");
  });

  it("prunes the markdown container once its message leaves the retained window", () => {
    const message = messageItem({ id: "m2", sequence: 2, role: "assistant", text: "kept" });
    const model = modelOf([message]);
    renderModel(model);
    expect(qa("div.md-body").length).toBe(1);
    rerenderModel({ ...model, items: [], itemsById: new Map() });
    expect(qa("div.md-body").length).toBe(0);
  });

  it("anchors each message and tool card with the key the tail window pins to", async () => {
    await renderLive(fixtureSnapshot());
    expect(qa("[data-anchor]").map((element) => element.getAttribute("data-anchor"))).toEqual(["m1", "run-t1-0", "c1", "c2"]);
    expect(qa("div[data-role]").map((element) => element.getAttribute("data-anchor"))).toEqual(["m1"]);
  });
});

describe("a finding filter on a turn with two tool runs", () => {
  it("does not remount the surviving run's card when the filter drops the earlier run and the message", () => {
    const items: Item[] = [
      ...itemsOf({ call: "c1", verdict: "ALLOW" }, 1),
      messageItem({ id: "m1", sequence: 10, role: "assistant", text: "between" }),
      ...itemsOf({ call: "c2", verdict: "DENY", result: "denied" }, 20),
    ];
    const model = modelOf(items);
    renderModel(model);
    const card = q("[data-anchor='c2']");
    rerenderModel({ ...model, filter: "denied" });
    expect(qa("[data-anchor='m1']").length).toBe(0);
    expect(q("[data-anchor='c2']")).toBe(card);
  });
});

describe("honesty rules on the transcript (web-chat.md sections 4.1 and 4.3)", () => {
  it("shows ALLOW beside a lost result as a permission and a lost state, never as a success", async () => {
    await renderLive(
      fixtureSnapshot([
        activityItem({ id: "a1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "pending" }),
        decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" }),
        resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "lost" }),
      ]),
    );
    const found = chips();
    expect(found).toContainEqual(["ALLOW", "allow"]);
    expect(found).toContainEqual(["lost", "lost"]);
    expect(found.some(([, tone]) => tone === "ok")).toBe(false);
    expect(q("[role='log']").textContent).toContain("policy ALLOW: a permission, not an outcome.");
  });

  it("shows UNKNOWN, unknown and lost as distinct, visible states", async () => {
    await renderLive(
      fixtureSnapshot([
        activityItem({ id: "a1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "pending" }),
        decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "UNKNOWN" }),
        resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "unknown" }),
        activityItem({ id: "a2", sequence: 4, tool: "read", tool_call_id: "c2", state: "pending" }),
        resultItem({ id: "r2", sequence: 5, tool: "read", tool_call_id: "c2", state: "lost" }),
      ]),
    );
    const found = chips();
    expect(found).toContainEqual(["UNKNOWN", "unknown"]);
    expect(found).toContainEqual(["unknown", "unknown"]);
    expect(found).toContainEqual(["lost", "lost"]);
    // The bare word unknown names the verdict count only (the footer's "0 unknown"); the unknown result reads unresolved.
    const posture = q("[role='group'][aria-label='Observed posture']").textContent ?? "";
    expect(posture).toMatch(/unknown verdict 1/);
    expect(posture).toMatch(/unresolved result 1/);
    expect(posture).not.toMatch(/unknown result/);
    expect(q("[role='log']").textContent).toContain("not an ALLOW or a DENY");
  });

  it("never shows a success as confirmed in an unverified projection", async () => {
    await renderLive(
      fixtureSnapshot([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded" })]),
    );
    expect(chips()).toContainEqual(["reported", "reported"]);
    expect(chips().some(([, tone]) => tone === "ok")).toBe(false);
  });

  it("shows a verified success as succeeded in the success tone, with no unverified marker", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" })),
        observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded" })),
      ]),
    );
    const card = q("[data-anchor='c1']");
    expect(chips()).toContainEqual(["succeeded", "ok"]);
    expect(card.textContent).not.toContain("unverified");
    expect(card.textContent).not.toContain("reported");
  });

  it("never gives a verified lossy success the ok tone: it reads succeeded (output lossy, not counted as confirmed) on the unknown tone", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" })),
        observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })),
      ]),
    );
    const found = chips();
    expect(found).toContainEqual(["succeeded (output lossy, not counted as confirmed)", "unknown"]);
    expect(found.some(([, tone]) => tone === "ok")).toBe(false);
    const summary = q("[data-anchor='c1'] summary").textContent ?? "";
    expect(summary).toContain("succeeded (output lossy, not counted as confirmed)");
    expect(summary).not.toMatch(/\bsucceeded\b(?! \(output lossy)/);
    expect(q("[role='group'][aria-label='Observed posture']").textContent).not.toMatch(/verified 1/);
  });

  it("states a lossy result in the ledger's words, and keeps its reported tone, which lossy output does not change", async () => {
    await renderLive(
      fixtureSnapshot([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })]),
    );
    const found = chips();
    expect(found).toContainEqual(["lossy", "unknown"]);
    expect(found).toContainEqual(["reported, output lossy, not counted as confirmed", "reported"]);
    expect(q("[role='log']").textContent).toContain("lossy output is not terminal evidence");
  });

  it("counts lossy output under the Unknown or lossy filter", async () => {
    await renderLive(
      fixtureSnapshot([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })]),
    );
    expect(button("Unknown or lossy").textContent).toBe("Unknown or lossy 1");
    expect(button("Unknown or lossy").getAttribute("title")).toBe("Records whose verdict or result is unknown, or whose output is lossy");
    // The filter counts lossy output, which never takes the success tone; its button is not inside any success chip.
    expect(button("Unknown or lossy").closest("[data-tone='ok']")).toBeNull();
  });

  it("shows the policy digest shortened on screen, with the full value in the title and in the text a reader hears", async () => {
    const digest = "sha256:0123456789abcdef0123456789abcdef";
    await renderLive(
      fixtureSnapshot([
        decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW", policy_digest: digest }),
      ]),
    );
    const shortened = qa("[title]").find((element) => element.getAttribute("title") === digest);
    const visible = shortened?.querySelector("[aria-hidden='true']")?.textContent ?? "";
    expect(visible).toContain("…");
    expect(visible).not.toBe(digest);
    expect(shortened?.textContent).toContain(`policy digest ${digest}`);
  });

  it("gives each step a timestamp in a time element", async () => {
    await renderLive(fixtureSnapshot());
    const times = qa("time[datetime]");
    expect(times.length).toBeGreaterThan(0);
    expect(times[0]?.getAttribute("datetime")).toBe("2026-10-13T09:12:04Z");
  });
});

describe("a run of tool activity", () => {
  const run = [
    activityItem({ id: "a1", sequence: 1, tool: "read", tool_call_id: "c1", state: "pending", summary: "read: a.go" }),
    resultItem({ id: "r1", sequence: 2, tool: "read", tool_call_id: "c1", state: "succeeded", summary: "ok" }),
    activityItem({ id: "a2", sequence: 3, tool: "bash", tool_call_id: "c2", state: "pending", summary: "bash: curl x" }),
    decisionItem({ id: "d2", sequence: 4, tool: "bash", tool_call_id: "c2", verdict: "DENY" }),
    resultItem({ id: "r2", sequence: 5, tool: "bash", tool_call_id: "c2", state: "denied", summary: "denied" }),
    activityItem({ id: "a3", sequence: 6, tool: "bash", tool_call_id: "c3", state: "pending", summary: "bash: go test" }),
  ];

  it("is one group line that counts each outcome in words and is never calmer than its worst record, open while nothing follows it", async () => {
    await renderLive(verifiedSnapshot(run.map((item) => observed(item))));
    const group = q("[data-group]");
    const toggle = group.querySelector("button") as HTMLButtonElement;
    // Nothing follows the run in its turn, so it starts open: the page cannot say the turn is finished.
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(group.getAttribute("data-tone")).toBe("deny");
    const line = toggle.textContent ?? "";
    expect(line).toContain("3 tool actions");
    // The closed line names the first calls by tool and target, never by outcome.
    expect(line).toContain("read a.go · bash curl x · bash go test");
    expect(line).toContain("1 denied");
    expect(line).toContain("1 DENY");
    // The request with no result is a request, not a success and not running.
    expect(line).toContain("1 pending");
    expect(line).toContain("1 succeeded");
  });

  it("starts closed once a message follows the run", async () => {
    await renderLive(verifiedSnapshot([...run.map((item) => observed(item)), observed(messageItem({ id: "m9", sequence: 7, role: "assistant", text: "done" }))]));
    expect(q("[data-group] > button").getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps every tool card inside the group, in order, for the reader who opens it", async () => {
    await renderLive(verifiedSnapshot(run.map((item) => observed(item))));
    const cards = Array.from(document.querySelectorAll("[data-group] [data-anchor]")).map((card) => card.getAttribute("data-anchor"));
    expect(cards).toEqual(["c1", "c2", "c3"]);
  });
});

describe("tool disclosures", () => {
  it("is a native details element that starts closed and names the tool", async () => {
    await renderLive(fixtureSnapshot());
    const details = q<HTMLDetailsElement>("details");
    expect(details.open).toBe(false);
    expect(q("summary").textContent).toContain("bash");
  });

  it("keeps the same disclosure element and its open state across a re-render", async () => {
    await renderLive(fixtureSnapshot());
    const details = q<HTMLDetailsElement>("details");
    act(() => {
      details.open = true;
      details.dispatchEvent(new Event("toggle"));
    });
    emitItem(messageItem({ id: "m9", sequence: 8, role: "assistant", text: "later" }));
    expect(q("details")).toBe(details);
    expect(details.open).toBe(true);
  });
});

describe("notices and states", () => {
  it("does not repeat the window notice in the transcript: the shell shows it above the tabs", () => {
    renderModel(modelOf(fixtureItems(), { windowGap: { kind: "server", reason: "cursor_ahead", since: 1 } }));
    // The loaded records are rendered, and the view has read the gap (its pointer to the shell's notice is shown).
    // Only then is the absence of the notice read: an empty transcript would pass the absence for nothing.
    expect(q("[role='log']").textContent).toContain("hello world");
    expect(qa("[role='log'] p").some((element) => element.textContent === TRANSCRIPT_NOTICE_POINTER)).toBe(true);
    expect(q("[role='log']").textContent).not.toContain(WINDOW_GAP_LABEL);
    expect(q("[role='log']").textContent).not.toContain(WINDOW_GAP_TEXT);
  });

  it("does not repeat the eviction notice in the transcript, but the summary line still counts it", () => {
    renderModel(modelOf(fixtureItems(), { evicted: 3 }));
    expect(q("[role='log']").textContent).not.toMatch(/earlier records? no longer retained/);
    expect(summaryLine()).toMatch(/ · 3 older element\(s\) evicted$/);
  });

  it("marks the top of the transcript with a pointer when a notice applies, and only then", () => {
    renderModel(modelOf(fixtureItems(), { evicted: 3 }));
    expect(qa("[role='log'] p").some((element) => element.textContent === TRANSCRIPT_NOTICE_POINTER)).toBe(true);
    mounted?.unmount();
    renderModel(modelOf(fixtureItems()));
    expect(qa("[role='log'] p").some((element) => element.textContent === TRANSCRIPT_NOTICE_POINTER)).toBe(false);
  });

  it("shows no earlier-history notice for a loaded window with continuity", async () => {
    await renderLive(fixtureSnapshot());
    // The transcript's own records are shown first, so the absences below are read against a rendered transcript.
    expect(q("[role='log']").textContent).toContain("hello world");
    // The notice pointer is the one notice marker the view renders (role="note" is the shell's, not this view's).
    expect(qa("[role='log'] p").some((element) => element.textContent === TRANSCRIPT_NOTICE_POINTER)).toBe(false);
    expect(q("[role='log']").textContent).not.toContain(WINDOW_GAP_TEXT);
  });

  it("shows a loading state until the projection is loaded", async () => {
    mounted = mount(
      <WorkspaceProvider deps={fakeWorkspace(fixtureSnapshot()).deps}>
        <Probe />
      </WorkspaceProvider>,
    );
    expect(q("[aria-busy='true']").textContent).toBe("Loading projection…");
    await settle();
    expect(qa("[aria-busy='true']")).toHaveLength(0);
  });

  it("says the source is unavailable when there is no live source and no items", async () => {
    await renderLive(snapshotOf({ source: "unavailable", verification_scope: "unavailable", items: [] }));
    expect(q("[role='log']").textContent).toContain("Production observations are unavailable");
  });

  it("says no Pi turns are observed yet, and that nothing can be sent from this read-only page", async () => {
    await renderLive(snapshotOf({ items: [] }));
    expect(q("[role='log']").textContent).toContain("No Pi turns observed yet.");
    expect(q("[role='log']").textContent).toContain("This page is a read-only view; nothing can be sent from here.");
  });

  it("shows a projection error as text", () => {
    renderModel(modelOf(fixtureItems(), { error: "stream stopped: the gateway closed the connection" }));
    expect(q("[role='log']").textContent).toContain("stream stopped: the gateway closed the connection");
  });
});

describe("the tail window (web-chat.md section 4.5)", () => {
  it("follows new records for a reader at the tail, and counts none of them", async () => {
    // The transcript grows by the new record's height when it arrives. A reader at the tail is carried to the new bottom.
    const heights = { scroll: 5000, view: 500 };
    stubTranscript(heights);
    await renderLive(fixtureSnapshot());
    transcript().scrollTop = 4500;
    act(() => {
      transcript().dispatchEvent(new Event("scroll"));
    });
    heights.scroll = 5600;
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "arrived at the tail" }));
    expect(transcript().textContent).toContain("arrived at the tail");
    expect(transcript().scrollTop).toBe(5100);
    expect(hasJumpControl()).toBe(false);
  });

  it("counts the records applied while a reader is away, and offers Jump to latest", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m9", sequence: 9, role: "assistant", text: "two" }));
    expect(button("2 new records").textContent).toBe("2 new records · Jump to latest");
  });

  it("counts only applied records: a duplicate or a gap never adds to the count", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m20", sequence: 20, role: "assistant", text: "after a gap" }));
    await settle();
    expect(button("1 new record").textContent).toBe("1 new record · Jump to latest");
  });

  it("holds the reader's place when records arrive below the viewport", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    transcript().scrollTop = 1200;
    act(() => {
      transcript().dispatchEvent(new Event("scroll"));
    });
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "below" }));
    expect(transcript().scrollTop).toBe(1200);
  });

  it("moves the reader to the tail, and focus to the transcript, when Jump is pressed", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    click(button("1 new record"));
    expect(transcript().scrollTop).toBe(4500);
    expect(document.activeElement).toBe(q("[role='log']"));
    expect(hasJumpControl()).toBe(false);
  });

  it("announces the waiting count once, after a debounce, through the shell announcer", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    vi.useFakeTimers();
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m9", sequence: 9, role: "assistant", text: "two" }));
    expect(liveAnnouncer?.getSnapshot().text).not.toMatch(/new record/);
    act(() => {
      vi.advanceTimersByTime(TAIL_ANNOUNCE_DELAY_MS);
    });
    expect(liveAnnouncer?.getSnapshot().text).toBe("2 new records below. Jump to latest is available.");
    emitItem(messageItem({ id: "m10", sequence: 10, role: "assistant", text: "three" }));
    act(() => {
      vi.advanceTimersByTime(TAIL_ANNOUNCE_DELAY_MS);
    });
    expect(liveAnnouncer?.getSnapshot().text).toBe("3 new records below. Jump to latest is available.");
  });

  it("a snapshot load never adds to the count, even for a reader away from the tail", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    expect(hasJumpControl()).toBe(false);
    expect(latest?.applied).toBe(0);
  });

  it("opens at the top of the transcript with no count", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    expect(transcript().scrollTop).toBe(0);
    expect(hasJumpControl()).toBe(false);
  });
});

describe("the filter toolbar and the log", () => {
  it("is one tab stop, on the pressed filter, and a press changes the filter in the store", async () => {
    await renderLive(fixtureSnapshot());
    const buttons = Array.from(q("[role='toolbar']").querySelectorAll<HTMLButtonElement>("button"));
    expect(buttons.filter((element) => element.tabIndex === 0)).toHaveLength(1);
    click(buttons[2] as HTMLButtonElement);
    expect(latest?.filter).toBe("unknown");
  });

  it("is a log region with live announcements turned off, since the shell announces", async () => {
    await renderLive(fixtureSnapshot());
    const log = q("[role='log']");
    expect(log.getAttribute("aria-live")).toBe("off");
    expect(log.getAttribute("aria-label")).toBe("Transcript");
  });
});

describe("the conversation head", () => {
  it("names the session, and states its Pi version, session id, workflow id, and status with its reading", async () => {
    await renderLive(
      sessionSnapshot({
        title: "Demo session",
        pi_version: "0.87.1",
        session_id: "session-demo-01",
        workflow_id: "wf-demo-01",
        status: "completed",
      }),
    );
    // The title is the shell header's heading, tested in Shell.test.tsx ("titles the session in the header row").
    expect(hasParagraph("Pi 0.87.1 · session-demo-01 · wf-demo-01")).toBe(true);
    // The Pi version is drawn on its own: its text is exactly the projected session.pi_version.
    expect(qa("p span").some((element) => element.textContent === "0.87.1")).toBe(true);
    expect(hasParagraph("completed · reported, not verified")).toBe(true);
  });

  it("before the first snapshot, every value reads unavailable (the title is tested in Shell.test.tsx)", () => {
    mounted = mount(
      <WorkspaceProvider deps={fakeWorkspace(fixtureSnapshot()).deps}>
        <Probe />
      </WorkspaceProvider>,
    );
    expect(hasParagraph("Pi version unavailable · session id unavailable · workflow id unavailable")).toBe(true);
    expect(hasParagraph("status unavailable")).toBe(true);
  });
});

describe("message chrome", () => {
  it("labels the assistant Pi, with its screen-reader text, and the user User, each with its reading", async () => {
    await renderLive(
      fixtureSnapshot([
        messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
        messageItem({ id: "m2", sequence: 2, role: "assistant", text: "hi" }),
      ]),
    );
    expect(roleRow("user").textContent).toBe("User reported, not verified");
    expect(roleRow("assistant").textContent).toBe("Pi, assistant reported, not verified");
  });

  it("reads a message whose verification scope is absent as verification unavailable, never as verified", async () => {
    // A snapshot item without its scope is refused by validation, so the view is rendered from a model that carries it.
    const orphan = { ...messageItem({ id: "m1", sequence: 1, role: "assistant", text: "hi" }), verification_scope: undefined as unknown as VerificationScope };
    renderModel(modelOf([orphan]));
    expect(roleRow("assistant").textContent).toBe("Pi, assistant verification unavailable");
  });

  it("gives each message a time, a Copy button and a record button named for its sequence", async () => {
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" })]));
    const time = q("div[data-role='user'] time");
    expect(time.getAttribute("datetime")).toBe("2026-10-13T09:12:04Z");
    expect(time.textContent).toMatch(/^\d{2}:\d{2}$/);
    expect(copyControl("Copy message").getAttribute("aria-label")).toBe("Copy message: your message, record 1");
    expect(button("record 1").textContent).toBe("record 1");
  });

  it("draws the Copy control as an icon alone: no text, and a name that says what it copies", async () => {
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" })]));
    const control = copyControl("Copy message");
    expect(control.textContent?.trim()).toBe("");
    expect(control.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("puts the record button on the speaker's label row", async () => {
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "assistant", text: "hi" })]));
    const row = roleRow("assistant").parentElement;
    expect(row?.querySelector("button")?.textContent).toBe("record 1");
  });

  it("draws each turn heading as a label, a hairline rule and the element count", async () => {
    await renderLive(fixtureSnapshot());
    const heading = q("section").firstElementChild;
    expect(heading?.children).toHaveLength(3);
    expect(heading?.children[0]?.textContent).toBe("turn t1");
    expect(heading?.children[1]?.getAttribute("aria-hidden")).toBe("true");
    expect(heading?.children[2]?.textContent).toBe("7 element(s)");
  });

  it("names every record label by an item's sequence, on messages and on tool cards", async () => {
    await renderLive(fixtureSnapshot());
    const sequences = fixtureItems().map((item) => item.sequence);
    const labels = [...(mounted?.container.textContent ?? "").matchAll(/record (\d+)/g)].map((match) => Number(match[1]));
    expect(labels.length).toBeGreaterThan(0);
    for (const label of labels) expect(sequences).toContain(label);
    expect(qa("[data-anchor='c1'] summary")[0]?.textContent).toContain("record 4");
  });

  it("opens the record inspector from a record button, so the selection moves to that record", async () => {
    await renderLive(fixtureSnapshot());
    click(button("record 1"));
    expect(latest?.selectedId).toBe("m1");
  });

  it("draws no diff statistic, no group summary and no MATCHED label", async () => {
    await renderLive(fixtureSnapshot());
    expect(q("[role='log']").textContent).not.toMatch(/\+\d+\s*[−-]\d+|MATCHED/);
  });

  it("never names the closed tool vocabulary as this session's tools", async () => {
    await renderLive(fixtureSnapshot());
    expect(mounted?.container.textContent).not.toContain("read, write, edit, bash");
  });
});

describe("the Copy action", () => {
  afterEach(() => {
    delete (navigator as unknown as Record<string, unknown>).clipboard;
  });

  function setClipboard(value: unknown): void {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value });
  }

  // Lets the copy promise settle, without moving the timers.
  async function settleCopy(): Promise<void> {
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it("copies the projected text, reads Copied for one second, and announces the copy", async () => {
    const writeText = vi.fn(async (_text: string) => undefined);
    setClipboard({ writeText });
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" })]));
    vi.useFakeTimers();
    click(copyControl("Copy message"));
    await settleCopy();
    expect(writeText).toHaveBeenCalledWith("hello world");
    expect(copyControl("Copied").getAttribute("aria-label")).toBe("Copied: your message, record 1");
    expect(liveAnnouncer?.getSnapshot().text).toBe("Message copied.");
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(copyControl("Copy message").getAttribute("aria-label")).toBe("Copy message: your message, record 1");
  });

  it("reads Copy unavailable when the clipboard refuses, and does not throw", async () => {
    setClipboard({ writeText: () => Promise.reject(new Error("permission denied")) });
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" })]));
    click(copyControl("Copy message"));
    await settleCopy();
    expect(copyControl("Copy unavailable").getAttribute("aria-label")).toBe("Copy unavailable: your message, record 1");
    expect(liveAnnouncer?.getSnapshot().text).toBe("Copy unavailable.");
  });

  it("reads Copy unavailable when the page has no clipboard, and does not throw", async () => {
    setClipboard(undefined);
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" })]));
    click(copyControl("Copy message"));
    await settleCopy();
    expect(copyControl("Copy unavailable").getAttribute("aria-label")).toBe("Copy unavailable: your message, record 1");
  });

  it("names the copy of a truncated message as the displayed text", async () => {
    await renderLive(
      fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "assistant", text: "part", text_truncated: true })]),
    );
    expect(copyControl("Copy displayed text (truncated)").getAttribute("aria-label")).toBe(
      "Copy displayed text (truncated): Pi, record 1",
    );
  });

  it("names each Copy control for the message it copies, so no two controls share a name", async () => {
    await renderLive(
      fixtureSnapshot([
        messageItem({ id: "m1", sequence: 1, role: "user", text: "first" }),
        messageItem({ id: "m2", sequence: 2, role: "assistant", text: "second" }),
        messageItem({ id: "m3", sequence: 3, role: "system", text: "third" }),
      ]),
    );
    const names = qa<HTMLButtonElement>("button")
      .filter((element) => element.getAttribute("aria-label")?.startsWith("Copy message:"))
      .map((element) => element.getAttribute("aria-label"));
    expect(names).toEqual([
      "Copy message: your message, record 1",
      "Copy message: Pi, record 2",
      "Copy message: system note, record 3",
    ]);
  });

  it("keeps the record in the name while the label reads Copied", async () => {
    setClipboard({ writeText: async () => undefined });
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "assistant", text: "hi" })]));
    click(copyControl("Copy message"));
    await settleCopy();
    expect(copyControl("Copied").getAttribute("aria-label")).toBe("Copied: Pi, record 1");
  });

  it("reads Copy unavailable for the same second as Copied, announces it once, and then returns to Copy message", async () => {
    setClipboard({ writeText: () => Promise.reject(new Error("permission denied")) });
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" })]));
    const before = liveAnnouncer?.getSnapshot().serial ?? -1;
    vi.useFakeTimers();
    click(copyControl("Copy message"));
    await settleCopy();
    expect(copyControl("Copy unavailable").getAttribute("aria-label")).toBe("Copy unavailable: your message, record 1");
    expect(liveAnnouncer?.getSnapshot().serial).toBe(before + 1);
    expect(liveAnnouncer?.getSnapshot().text).toBe("Copy unavailable.");
    act(() => {
      vi.advanceTimersByTime(COPIED_MS - 1);
    });
    expect(copyControl("Copy unavailable").getAttribute("aria-label")).toBe("Copy unavailable: your message, record 1");
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(copyControl("Copy message").getAttribute("aria-label")).toBe("Copy message: your message, record 1");
    expect(liveAnnouncer?.getSnapshot().serial).toBe(before + 1);
  });

  it("restarts the one-second reading on each copy, so a second copy is not cut short by the first timer", async () => {
    setClipboard({ writeText: async () => undefined });
    await renderLive(fixtureSnapshot([messageItem({ id: "m1", sequence: 1, role: "user", text: "hello world" })]));
    vi.useFakeTimers();
    click(copyControl("Copy message"));
    await settleCopy();
    act(() => {
      vi.advanceTimersByTime(COPIED_MS - 400);
    });
    click(copyControl("Copied"));
    await settleCopy();
    act(() => {
      vi.advanceTimersByTime(COPIED_MS - 400);
    });
    expect(copyControl("Copied").getAttribute("aria-label")).toBe("Copied: your message, record 1");
    act(() => {
      vi.advanceTimersByTime(400);
    });
    expect(copyControl("Copy message").getAttribute("aria-label")).toBe("Copy message: your message, record 1");
  });
});

describe("the composer in the Chat view", () => {
  it("renders the composer below the transcript, as a read-only, labelled region", async () => {
    await renderLive(fixtureSnapshot());
    const log = q("[role='log']");
    const composer = q("[data-testid='composer']");
    // The composer is not inside the transcript. FOLLOWING alone also holds for a descendant, so the containment is asserted.
    expect(log.contains(composer)).toBe(false);
    // The composer's dock is the transcript wrapper's next sibling: the composer follows the log, and is not a child of it.
    const dock = composer.parentElement;
    expect(dock?.previousElementSibling).toBe(log.parentElement);
    expect(dock?.parentElement).toBe(log.parentElement?.parentElement);
    expect(log.compareDocumentPosition(composer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(q<HTMLTextAreaElement>("[data-testid='composer'] textarea").readOnly).toBe(true);
  });

  it("is rendered before the first snapshot too, so the screen still reads as a chat", () => {
    mounted = mount(
      <WorkspaceProvider deps={fakeWorkspace(fixtureSnapshot()).deps}>
        <Probe />
      </WorkspaceProvider>,
    );
    expect(q("[data-testid='composer']")).toBeTruthy();
  });
});

describe("the composer dock: the chat view's last box, which holds the jump", () => {
  it("is a box of its own, and the last child of the chat view, so a phone pins it at the page's end", async () => {
    await renderLive(fixtureSnapshot());
    const dock = q("[data-testid='composer']").parentElement;
    expect(dock?.querySelector("[role='log']")).toBeNull();
    expect(dock?.parentElement?.lastElementChild).toBe(dock);
  });

  it("holds the jump in that dock, beside the composer and not inside the transcript", async () => {
    stubTranscript({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    expect(button("1 new record").parentElement).toBe(q("[data-testid='composer']").parentElement);
  });
});
