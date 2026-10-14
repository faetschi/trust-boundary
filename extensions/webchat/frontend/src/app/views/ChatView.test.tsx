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
  snapshotOf,
} from "../../core/testing/builders";
import { CONTRACT_VERSION, type Item, type Snapshot } from "../../core/contract";
import { fakeWorkspace, type FakeWorkspace } from "../../test-support/fakes";
import { click, mount, settle, type Mounted } from "../../test-support/dom";
import { AnnouncerContext, useAnnouncer } from "../../ui/LiveRegion";
import { createAnnouncer, type Announcer } from "../../ui/announcer";
import { WorkspaceProvider, useWorkspace } from "../WorkspaceProvider";
import { createWorkspaceStore, type WorkspaceModel } from "../workspaceStore";
import { WINDOW_GAP_TEXT } from "../status";
import { ChatView, TRANSCRIPT_NOTICE_POINTER } from "./ChatView";
import { TAIL_ANNOUNCE_DELAY_MS } from "./chat/tail";

let latest: WorkspaceModel | null = null;
let liveAnnouncer: Announcer | null = null;

// Renders the view from the live store: the same wiring the shell uses.
function Probe() {
  const model = useWorkspace();
  latest = model;
  liveAnnouncer = useAnnouncer();
  return <ChatView model={model} />;
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

function hasJumpControl(): boolean {
  return qa("button").some((element) => element.textContent?.includes("Jump to latest"));
}

// The line under the filter toolbar: the toolbar's sibling paragraph.
function summaryLine(): string {
  return q("[role='toolbar']").parentElement?.querySelector("p")?.textContent ?? "";
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

// Stubs the page's scroll metrics. A 5000 px page in a 500 px viewport is away from the tail at the
// top. happy-dom's scrollTo writes the scrollTop it is given, so a jump is observable.
function stubPage(heights: { readonly scroll: number; readonly view: number }): void {
  const root = document.documentElement;
  Object.defineProperty(root, "scrollHeight", { configurable: true, get: () => heights.scroll });
  Object.defineProperty(root, "clientHeight", { configurable: true, get: () => heights.view });
  restorePage = () => {
    delete (root as unknown as Record<string, unknown>).scrollHeight;
    delete (root as unknown as Record<string, unknown>).clientHeight;
    root.scrollTop = 0;
  };
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
    const summaries = qa("summary");
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
    expect(chips()).toContainEqual(["unverified", "idle"]);
    expect(chips()).toContainEqual(["verified", "ok"]);
    expect(chips()).toContainEqual(["succeeded", "ok"]);
  });

  it("states once that a verified projection is verified, with no per-item marker", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" })),
        observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" })),
      ]),
    );
    expect(q("[role='log']").textContent).toContain("All 2 records in this view are verified · source: live");
    expect(chips().some(([text]) => text === "unverified" || text === "verified")).toBe(false);
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
    expect(buttons.map((element) => element.textContent?.trim())).toEqual(["All 7", "Denied 2", "Unknown 0", "Failed 0"]);
    expect(buttons[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(buttons[1]?.getAttribute("aria-pressed")).toBe("false");
    expect(summaryLine()).toBe("7 element(s) in the retained window");
  });

  it("separates policy ALLOW from reported execution in the posture badges", async () => {
    await renderLive(fixtureSnapshot());
    const posture = q("[role='group'][aria-label='Observed posture']").textContent ?? "";
    expect(posture).toMatch(/ALLOW 1/);
    expect(posture).toMatch(/DENY 1/);
    expect(posture).toMatch(/reported 1/);
    expect(posture).toMatch(/denied 1/);
    expect(posture).not.toMatch(/verified 1/);
  });

  it("hides non-matching blocks and messages when the Denied filter is pressed", async () => {
    await renderLive(fixtureSnapshot());
    click(button("Denied"));
    const text = q("[role='log']").textContent ?? "";
    expect(text).toContain("bash: curl");
    expect(text).not.toContain("hello world");
    expect(text).not.toContain("bash: go test");
    expect(button("Denied").getAttribute("aria-pressed")).toBe("true");
    expect(summaryLine()).toBe("2 matches · 3 of 7 element(s) shown for “Denied” (context included)");
  });

  it("renders the empty-filter state when no element matches", async () => {
    await renderLive(fixtureSnapshot());
    click(button("Failed"));
    expect(q("[role='log']").textContent).toContain("No failed findings");
    expect(summaryLine()).toBe("0 matches · 0 of 7 element(s) shown for “Failed” (context included)");
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
    expect(qa("[data-anchor]").map((element) => element.getAttribute("data-anchor"))).toEqual(["m1", "c1", "c2"]);
    expect(qa("div[data-role]").map((element) => element.getAttribute("data-anchor"))).toEqual(["m1"]);
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

  it("never gives a verified lossy success the ok tone: it reads succeeded (output lossy, not confirmed) on the unknown tone", async () => {
    await renderLive(
      verifiedSnapshot([
        observed(decisionItem({ id: "d1", sequence: 1, tool: "bash", tool_call_id: "c1", verdict: "ALLOW" })),
        observed(resultItem({ id: "r1", sequence: 2, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })),
      ]),
    );
    const found = chips();
    expect(found).toContainEqual(["succeeded (output lossy, not confirmed)", "unknown"]);
    expect(found.some(([, tone]) => tone === "ok")).toBe(false);
    const summary = q("[data-anchor='c1'] summary").textContent ?? "";
    expect(summary).toContain("succeeded (output lossy, not confirmed)");
    expect(summary).not.toMatch(/\bsucceeded\b(?! \(output lossy)/);
    expect(q("[role='group'][aria-label='Observed posture']").textContent).not.toMatch(/verified 1/);
  });

  it("states a lossy result in text, and keeps its state, which lossy output does not change", async () => {
    await renderLive(
      fixtureSnapshot([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })]),
    );
    const found = chips();
    expect(found).toContainEqual(["lossy", "unknown"]);
    expect(found).toContainEqual(["reported", "reported"]);
    expect(q("[role='log']").textContent).toContain("lossy output is not terminal evidence");
  });

  it("counts lossy output under the Unknown filter", async () => {
    await renderLive(
      fixtureSnapshot([resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", lossy: true })]),
    );
    expect(button("Unknown").textContent).toBe("Unknown 1");
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
    expect(q("[role='log']").textContent).not.toContain(WINDOW_GAP_TEXT);
    expect(qa("[role='note']")).toHaveLength(0);
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
    expect(qa("[role='note']").some((note) => note.textContent?.includes("Earlier history"))).toBe(false);
    expect(q("[role='log']").textContent).not.toContain(TRANSCRIPT_NOTICE_POINTER);
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

  it("says nothing has been projected for an empty live projection", async () => {
    await renderLive(snapshotOf({ items: [] }));
    expect(q("[role='log']").textContent).toContain("No transcript or activity has been projected yet.");
  });

  it("shows a projection error as text", () => {
    renderModel(modelOf(fixtureItems(), { error: "stream stopped: the gateway closed the connection" }));
    expect(q("[role='log']").textContent).toContain("stream stopped: the gateway closed the connection");
  });
});

describe("the tail window (web-chat.md section 4.5)", () => {
  it("follows new records for a reader at the tail, and counts none of them", async () => {
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "new" }));
    expect(hasJumpControl()).toBe(false);
  });

  it("counts the records applied while a reader is away, and offers Jump to latest", async () => {
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m9", sequence: 9, role: "assistant", text: "two" }));
    expect(button("2 new records").textContent).toBe("2 new records · Jump to latest");
  });

  it("counts only applied records: a duplicate or a gap never adds to the count", async () => {
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    emitItem(messageItem({ id: "m20", sequence: 20, role: "assistant", text: "after a gap" }));
    await settle();
    expect(button("1 new record").textContent).toBe("1 new record · Jump to latest");
  });

  it("holds the reader's place when records arrive below the viewport", async () => {
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    document.documentElement.scrollTop = 1200;
    act(() => {
      window.dispatchEvent(new Event("scroll"));
    });
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "below" }));
    expect(document.documentElement.scrollTop).toBe(1200);
  });

  it("moves the reader to the tail, and focus to the transcript, when Jump is pressed", async () => {
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    emitItem(messageItem({ id: "m8", sequence: 8, role: "assistant", text: "one" }));
    click(button("1 new record"));
    expect(document.documentElement.scrollTop).toBe(4500);
    expect(document.activeElement).toBe(q("[role='log']"));
    expect(hasJumpControl()).toBe(false);
  });

  it("announces the waiting count once, after a debounce, through the shell announcer", async () => {
    stubPage({ scroll: 5000, view: 500 });
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
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    expect(hasJumpControl()).toBe(false);
    expect(latest?.applied).toBe(0);
  });

  it("opens at the top of the transcript with no count", async () => {
    stubPage({ scroll: 5000, view: 500 });
    await renderLive(fixtureSnapshot());
    expect(document.documentElement.scrollTop).toBe(0);
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
