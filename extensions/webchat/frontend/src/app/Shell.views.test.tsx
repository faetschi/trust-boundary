// @vitest-environment happy-dom
// Behaviour that spans both views of the shell (docs/frontend.md, Phase 3 polish): one copy of each
// notice in the whole app, a new-record count that survives a view switch, a first load that starts at
// the top, and the inspector's name while a record is open. Mounted in the real shell and store.

import { act } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { CONTRACT_VERSION, GAP_VERSION, type Item, type Snapshot } from "../core/contract";
import { decisionItem, messageItem, resultItem, snapshotOf } from "../core/testing/builders";
import { click, installMatchMedia, mount, settle, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../test-support/fakes";
import { Shell } from "./Shell";
import { WorkspaceProvider } from "./WorkspaceProvider";
import { WINDOW_GAP_TEXT, evictedText } from "./status";

let shim: MatchMediaShim | null = null;
let mounted: Mounted | null = null;
let fake: FakeWorkspace | null = null;
let restorePage: (() => void) | null = null;
let restoreTranscript: (() => void) | null = null;

function render(snapshot: Snapshot, width = 1440): FakeWorkspace {
  shim = installMatchMedia(width);
  const source = fakeWorkspace(snapshot);
  fake = source;
  mounted = mount(
    <WorkspaceProvider deps={source.deps}>
      <Shell version="0.1.0" />
    </WorkspaceProvider>,
  );
  return source;
}

function openStream(): void {
  act(() => {
    fake?.sources[0]?.open();
  });
}

function emitItem(item: Item): void {
  act(() => {
    fake?.sources[0]?.emit("item", { schema_version: CONTRACT_VERSION, cursor: `e1:${item.sequence}`, kind: "item", item });
  });
}

function tab(label: string): HTMLButtonElement {
  const match = Array.from(mounted?.container.querySelectorAll<HTMLButtonElement>("[role='tab']") ?? []).find(
    (element) => element.textContent === label,
  );
  if (!match) throw new Error(`no ${label} tab`);
  return match;
}

function occurrences(haystack: string, text: string): number {
  return haystack.split(text).length - 1;
}

// How many times the text occurs in the app's DOM as shown. The live status region is left out: it
// holds the last announcement, which speaks a notice to a screen reader and is not a second copy on screen.
function countOf(text: string): number {
  const container = mounted?.container;
  const live = container?.querySelector("[role='status']")?.textContent ?? "";
  return occurrences(container?.textContent ?? "", text) - occurrences(live, text);
}

function jumpButton(): HTMLButtonElement | undefined {
  return Array.from(mounted?.container.querySelectorAll<HTMLButtonElement>("button") ?? []).find((button) =>
    button.textContent?.includes("new record"),
  );
}

// Stubs the page's scroll metrics. The object is read live, so a test can grow the page after mount.
interface Page {
  scroll: number;
  view: number;
  // The reader's offset, read and written by the transcript's tail logic.
  top?: number;
}

function stubPage(page: Page): void {
  const root = document.documentElement;
  Object.defineProperty(root, "scrollHeight", { configurable: true, get: () => page.scroll });
  Object.defineProperty(root, "clientHeight", { configurable: true, get: () => page.view });
  restorePage = () => {
    delete (root as unknown as Record<string, unknown>).scrollHeight;
    delete (root as unknown as Record<string, unknown>).clientHeight;
    root.scrollTop = 0;
  };
}

function transcriptLog(): HTMLElement {
  const log = mounted?.container.querySelector<HTMLElement>("[role='log'][aria-label='Transcript']");
  if (!log) throw new Error("no transcript log");
  return log;
}

// The scrollers are the chat transcript (a log) and the trajectory ledger (a region named Record ledger): the tail reads
// their metrics, not the page's. The stub is on the prototype and answers for those two only, so the tail's first
// measurement on a mount (which runs before any test code can reach the element) reads it. Returns the restore function.
function stubTranscript(page: Page): () => void {
  const proto = HTMLElement.prototype;
  // The accessor a element gets without the stub (own or inherited): the fallback for every other element.
  const inherited = (name: string): PropertyDescriptor | undefined => {
    for (let level: object | null = proto; level; level = Object.getPrototypeOf(level)) {
      const found = Object.getOwnPropertyDescriptor(level, name);
      if (found) return found;
    }
    return undefined;
  };
  const names = ["scrollHeight", "clientHeight", "scrollTop"];
  const original = new Map(names.map((name) => [name, inherited(name)]));
  const own = new Map(names.map((name) => [name, Object.getOwnPropertyDescriptor(proto, name)]));
  const isScroller = (element: Element) =>
    element.getAttribute("role") === "log" ||
    (element.getAttribute("role") === "region" && element.getAttribute("aria-label") === "Record ledger");
  const fallback = (name: string, element: Element) => original.get(name)?.get?.call(element) ?? 0;
  Object.defineProperty(proto, "scrollHeight", {
    configurable: true,
    get(this: Element) {
      return isScroller(this) ? page.scroll : fallback("scrollHeight", this);
    },
  });
  Object.defineProperty(proto, "clientHeight", {
    configurable: true,
    get(this: Element) {
      return isScroller(this) ? page.view : fallback("clientHeight", this);
    },
  });
  Object.defineProperty(proto, "scrollTop", {
    configurable: true,
    get(this: Element) {
      return isScroller(this) ? (page.top ?? 0) : fallback("scrollTop", this);
    },
    set(this: Element, value: number) {
      if (isScroller(this)) page.top = value;
      else original.get("scrollTop")?.set?.call(this, value);
    },
  });
  return () => {
    for (const name of names) {
      const descriptor = own.get(name);
      if (descriptor) Object.defineProperty(proto, name, descriptor);
      else delete (proto as unknown as Record<string, unknown>)[name];
    }
  };
}

function messages(from: number, to: number): Item[] {
  const out: Item[] = [];
  for (let sequence = from; sequence <= to; sequence++) {
    out.push(messageItem({ id: `m${sequence}`, sequence, role: "user", text: `message ${sequence}` }));
  }
  return out;
}

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  fake = null;
  shim?.restore();
  shim = null;
  restorePage?.();
  restorePage = null;
  restoreTranscript?.();
  restoreTranscript = null;
  document.body.innerHTML = "";
});

describe("notices: one copy of each in the whole app", () => {
  it("shows a gap, a window gap and an eviction once each, in the chat view and in the trajectory view", async () => {
    const source = render(snapshotOf({ items: messages(1, 5) }));
    await settle();
    openStream();
    await settle();
    // The recovery snapshot keeps messages 1 to 3 and declares that 4 and 5 were evicted: two records are
    // evicted, and the declared history_evicted gap marks the window. A snapshot must start at sequence 1.
    const recovered = snapshotOf({
      items: messages(1, 3),
      gaps: [{ id: "g1", reason: "history_evicted", first_missing: 4, last_missing: 5, recorded_at: "2026-10-13T09:12:04Z" }],
    });
    source.reply = { ok: true, status: 200, body: JSON.parse(JSON.stringify(recovered)) };
    act(() => {
      source.sources[0]?.emit("gap", { schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 9 });
    });
    await settle();
    expect(countOf(WINDOW_GAP_TEXT)).toBe(1);
    expect(countOf(evictedText(2))).toBe(1);
    expect(countOf("Continuity gap (history evicted)")).toBe(1);

    click(tab("Trajectory"));
    await settle();
    expect(countOf(WINDOW_GAP_TEXT)).toBe(1);
    expect(countOf(evictedText(2))).toBe(1);
    expect(countOf("Continuity gap (history evicted)")).toBe(1);
  });
});

describe("notices: each condition has one notice, and each notice has its own name", () => {
  it("names the continuity gap, the window and the eviction as three different notices", async () => {
    const source = render(snapshotOf({ items: messages(1, 5) }));
    await settle();
    openStream();
    await settle();
    const recovered = snapshotOf({
      items: messages(1, 3),
      gaps: [{ id: "g1", reason: "history_evicted", first_missing: 4, last_missing: 5, recorded_at: "2026-10-13T09:12:04Z" }],
    });
    source.reply = { ok: true, status: 200, body: JSON.parse(JSON.stringify(recovered)) };
    act(() => {
      source.sources[0]?.emit("gap", { schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 9 });
    });
    await settle();
    const names = Array.from(mounted?.container.querySelectorAll("main [role='note']") ?? []).map((note) =>
      note.getAttribute("aria-label"),
    );
    expect(names).toEqual(["Continuity gap", "History not loaded", "Records no longer retained"]);
    expect(new Set(names).size).toBe(names.length);
    expect(names).not.toContain("Retained window");
  });
});

describe("the new-record count across a view switch", () => {
  it("carries the count into the trajectory view and back, and clears it at the tail", async () => {
    const page: Page = { scroll: 5000, view: 500, top: 0 };
    restoreTranscript = stubTranscript(page);
    render(snapshotOf({ items: messages(1, 3) }));
    await settle();
    openStream();
    await settle();
    emitItem(messageItem({ id: "m4", sequence: 4, role: "user", text: "live 4" }));
    emitItem(messageItem({ id: "m5", sequence: 5, role: "user", text: "live 5" }));
    expect(jumpButton()?.textContent).toBe("2 new records · Jump to latest");

    click(tab("Trajectory"));
    await settle();
    expect(jumpButton()?.textContent).toBe("2 new records · Jump to latest");
    emitItem(messageItem({ id: "m6", sequence: 6, role: "user", text: "live 6" }));
    expect(jumpButton()?.textContent).toBe("3 new records · Jump to latest");

    click(tab("Chat"));
    await settle();
    expect(jumpButton()?.textContent).toBe("3 new records · Jump to latest");

    act(() => {
      transcriptLog().scrollTop = 4500;
      transcriptLog().dispatchEvent(new Event("scroll"));
    });
    expect(jumpButton()).toBeUndefined();
  });
});

describe("the new-record count at phone widths", () => {
  it("counts from the page, because the page is the scroller when the layout stacks", async () => {
    stubPage({ scroll: 5000, view: 500 });
    render(snapshotOf({ items: messages(1, 3) }), 390);
    await settle();
    openStream();
    await settle();
    document.documentElement.scrollTop = 0;
    emitItem(messageItem({ id: "m4", sequence: 4, role: "user", text: "live 4" }));
    emitItem(messageItem({ id: "m5", sequence: 5, role: "user", text: "live 5" }));
    expect(jumpButton()?.textContent).toBe("2 new records · Jump to latest");
  });
});

describe("the first load", () => {
  it("starts at the top when the page grows on the first load, instead of following it to the bottom", async () => {
    const page: Page = { scroll: 500, view: 500 };
    stubPage(page);
    render(snapshotOf({ items: messages(1, 7) }), 390);
    // The page is short while the projection is loading, and tall once the records are shown.
    page.scroll = 5000;
    await settle();
    openStream();
    await settle();
    expect(countOf("message 7")).toBe(1);
    expect(document.documentElement.scrollTop).toBe(0);
  });
});

describe("the inspector's name", () => {
  it("is 'Record inspector' while a record is shown, and 'Governance inspector' otherwise", async () => {
    render(
      snapshotOf({
        items: [
          messageItem({ id: "m1", sequence: 1, role: "user", text: "hello" }),
          decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "DENY" }),
          resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "denied" }),
        ],
      }),
    );
    await settle();
    openStream();
    await settle();
    click(tab("Trajectory"));
    await settle();
    const aside = (): HTMLElement => {
      const element = mounted?.container.querySelector<HTMLElement>("aside");
      if (!element) throw new Error("no aside");
      return element;
    };
    expect(aside().getAttribute("aria-label")).toBe("Governance inspector");
    const row = mounted?.container.querySelector<HTMLButtonElement>("button[data-record-row][data-id='d1']");
    if (!row) throw new Error("no row d1");
    click(row);
    await settle();
    expect(aside().getAttribute("aria-label")).toBe("Record inspector");
    click(row);
    await settle();
    expect(aside().getAttribute("aria-label")).toBe("Governance inspector");
  });
});

// The projection states. Loading and failure are one state at a time: a first-load failure replaces
// the loading skeleton, a failure after data keeps the data, and a recovery shows the skeleton only
// while its fetch is in flight with no data.
describe("projection states: loading, failure and recovery are never shown together", () => {
  const failing = { ok: false, status: 500, body: null };
  const loaded = () =>
    snapshotOf({ items: [messageItem({ id: "m1", sequence: 1, role: "user", text: "list the files" })] });

  function renderWith(reply?: { ok: boolean; status: number; body: unknown }): FakeWorkspace {
    shim = installMatchMedia(1440);
    const source = fakeWorkspace(loaded());
    if (reply) source.reply = reply;
    fake = source;
    mounted = mount(
      <WorkspaceProvider deps={source.deps}>
        <Shell version="0.1.0" />
      </WorkspaceProvider>,
    );
    return source;
  }

  function dataStates(): string[] {
    return Array.from(mounted?.container.querySelectorAll("[data-state]") ?? []).map((el) => el.getAttribute("data-state") ?? "");
  }

  function logText(): string {
    return mounted?.container.querySelector("[role='log']")?.textContent ?? "";
  }

  it("a first-load failure shows the error in place of the loading skeleton, with the reconnect status", async () => {
    renderWith(failing);
    await settle();
    expect(dataStates()).toEqual(["error"]);
    expect(mounted?.container.querySelector("[data-state='error']")?.textContent).toMatch(/reconnecting…/);
    expect(mounted?.container.querySelector("[aria-busy='true']")).toBeNull();
  });

  it("a failure after data keeps the data, and shows the error without a loading skeleton", async () => {
    const source = renderWith();
    await settle();
    act(() => source.sources[0]?.open());
    // A gap recovers from a fresh snapshot; that snapshot fails, so the data stays and the error shows.
    source.reply = failing;
    act(() => {
      source.sources[0]?.emit("gap", { schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 9 });
    });
    await settle();
    expect(dataStates()).not.toContain("loading");
    expect(dataStates()).toEqual(["error"]);
    expect(logText()).toContain("list the files");
  });

  it("a recovery shows the skeleton while its fetch is in flight with no data, then the data", async () => {
    const source = renderWith(failing);
    await settle();
    expect(dataStates()).toEqual(["error"]);
    source.reply = { ok: true, status: 200, body: loaded() };
    act(() => {
      source.fireTimer();
    });
    expect(dataStates()).toEqual(["loading"]);
    expect(dataStates()).not.toContain("error");
    await settle();
    expect(dataStates()).toEqual([]);
    expect(logText()).toContain("list the files");
  });

  it("the trajectory shows its first-load failure in place of the loading text, not beside it", async () => {
    renderWith(failing);
    await settle();
    const tab = Array.from(mounted?.container.querySelectorAll<HTMLButtonElement>("[role='tab']") ?? []).find(
      (button) => button.textContent === "Trajectory",
    );
    if (!tab) throw new Error("no Trajectory tab");
    click(tab);
    await settle();
    const panel = mounted?.container.querySelector("#view-panel-trajectory");
    expect(panel?.textContent ?? "").not.toContain("Loading projection…");
    expect(Array.from(panel?.querySelectorAll("[data-state]") ?? []).map((el) => el.getAttribute("data-state"))).toEqual(["error"]);
  });
});

describe("before the first snapshot, no count reads as zero", () => {
  it("the chat and trajectory summaries say unavailable, never 0 elements or 0 records", async () => {
    shim = installMatchMedia(1440);
    fake = fakeWorkspace(snapshotOf({ items: [messageItem({ id: "m1", sequence: 1 })] }));
    mounted = mount(
      <WorkspaceProvider deps={fake.deps}>
        <Shell version="0.1.0" />
      </WorkspaceProvider>,
    );
    const chat = mounted.container.querySelector("#view-panel-chat")?.textContent ?? "";
    expect(chat).not.toContain("0 element(s)");
    expect(chat).toContain("unavailable (no projection loaded)");
    const tab = Array.from(mounted.container.querySelectorAll<HTMLButtonElement>("[role='tab']")).find((b) => b.textContent === "Trajectory");
    if (!tab) throw new Error("no Trajectory tab");
    click(tab);
    const trajectory = mounted.container.querySelector("#view-panel-trajectory")?.textContent ?? "";
    expect(trajectory).not.toContain("0 record(s)");
    expect(trajectory).toContain("unavailable (no projection loaded)");
  });
});
