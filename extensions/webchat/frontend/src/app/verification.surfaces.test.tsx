// @vitest-environment happy-dom
// One verification gate on every surface (verification.ts). A fixture or replay projection never presents a
// streamed record as verified. An observed, structurally audited record streamed into one is refused at the typed
// boundary (core validate.ts): it never reaches a surface, and the reader is told why. A record the projection
// does admit (diagnostic, unverified) reads as reported on every surface. Only a live structural-audit projection
// may read the observed records as verified. Mounted in the real shell and store.
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { CONTRACT_VERSION, type Item, type Snapshot } from "../core/contract";
import { decisionItem, messageItem, resultItem, snapshotOf } from "../core/testing/builders";
import { click, installMatchMedia, mount, settle, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace, type FakeWorkspace } from "../test-support/fakes";
import { Shell } from "./Shell";
import { WorkspaceProvider } from "./WorkspaceProvider";

let shim: MatchMediaShim | null = null;
let mounted: Mounted | null = null;
let fake: FakeWorkspace;

// The two records a gateway would stream after the snapshot: a policy ALLOW and a succeeded result,
// both observed and structurally audited.
function observedRecords(): Item[] {
  const evidence = { authority: "observed", verification_scope: "structural-audit-only" } as const;
  return [
    decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "ALLOW", ...evidence }),
    resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "succeeded", ...evidence }),
  ];
}

// The same two records as a fixture or replay projection may carry them: diagnostic and unverified.
function diagnosticRecords(): Item[] {
  const evidence = { authority: "diagnostic", verification_scope: "unverified-input" } as const;
  return [
    decisionItem({ id: "d1", sequence: 2, tool: "bash", tool_call_id: "c1", verdict: "ALLOW", ...evidence }),
    resultItem({ id: "r1", sequence: 3, tool: "bash", tool_call_id: "c1", state: "succeeded", ...evidence }),
  ];
}

async function open(snapshot: Snapshot, records: Item[] = observedRecords()): Promise<void> {
  shim = installMatchMedia(1440);
  fake = fakeWorkspace(snapshot);
  mounted = mount(
    <WorkspaceProvider deps={fake.deps}>
      <Shell version="0.1.0" />
    </WorkspaceProvider>,
  );
  await settle();
  act(() => fake.sources[0]?.open());
  for (const item of records) {
    act(() => {
      fake.sources[0]?.emit("item", { schema_version: CONTRACT_VERSION, cursor: `e1:${item.sequence}`, kind: "item", item });
    });
  }
  await settle();
}

function root(): HTMLElement {
  if (!mounted) throw new Error("not mounted");
  return mounted.container;
}

function trajectory(): void {
  const tab = Array.from(root().querySelectorAll<HTMLButtonElement>("[role='tab']")).find((b) => b.textContent === "Trajectory");
  if (!tab) throw new Error("no Trajectory tab");
  click(tab);
}

function rowOf(id: string): HTMLButtonElement {
  const row = root().querySelector<HTMLButtonElement>(`button[data-record-row][data-id='${id}']`);
  if (!row) throw new Error(`no row ${id}`);
  return row;
}

function dotTone(row: Element): string | null | undefined {
  return row.querySelector("span[aria-hidden='true']")?.getAttribute("data-tone");
}

function chatChips(): Array<readonly [string, string | null]> {
  return Array.from(root().querySelectorAll<HTMLElement>("[role='log'] [data-tone]"))
    .filter((element) => element.tagName === "SPAN")
    .map((element) => [element.textContent?.trim() ?? "", element.getAttribute("data-tone")] as const);
}

// The value of one evidence row in the inspector, by its label.
function evidenceValue(label: string): string {
  const aside = root().querySelector("aside");
  const section = aside?.querySelector("[data-section='evidence']");
  const row = Array.from(section?.querySelectorAll("div") ?? []).find((div) => div.querySelector("dt")?.textContent === label);
  return row?.querySelector("dd")?.textContent?.trim() ?? "";
}

// The record ids of the Trajectory ledger's rows, in order. The ledger exists only while its view is selected.
function ledgerIds(): Array<string | null> {
  const ledger = required(root().querySelector("#view-panel-trajectory"), "trajectory panel");
  return Array.from(ledger.querySelectorAll("button[data-record-row]"), (row) => row.getAttribute("data-id"));
}

function postureText(): string | null {
  return required(root().querySelector("[role='group'][aria-label='Observed posture']"), "observed posture").textContent;
}

// The tone marks inside one surface. The surface is read first, so an empty list means it has no toned mark, not that it is missing.
function toneMarks(surface: Element): Array<string | null> {
  return Array.from(surface.querySelectorAll("[data-tone]"), (element) => element.getAttribute("data-tone"));
}

// The element a read depends on. A missing element fails the read here; it never reads as an empty string.
function required<T extends Element>(element: T | null | undefined, what: string): T {
  if (!element) throw new Error(`no ${what} in the view`);
  return element;
}

function workspace(): HTMLElement {
  return required(root().querySelector("main[aria-label='Workspace']"), "workspace");
}

beforeEach(() => {
  document.body.innerHTML = "";
});

afterEach(() => {
  mounted?.unmount();
  mounted = null;
  shim?.restore();
  shim = null;
});

for (const source of ["fixture", "replay"] as const) {
describe(`one verification gate on every surface: a ${source} projection never reads a streamed record as verified`, () => {
  const fixture = () =>
    snapshotOf({ source, items: [messageItem({ id: "m1", sequence: 1, role: "user", text: "list files" })] });

  it("refuses a streamed observed success at the boundary: no surface shows it, and the reader is told why", async () => {
    // The snapshot declares the records' own scope, so the scope check admits them and only the authority rule
    // (observed outside a live projection) refuses them. Under the default unverified scope the scope rule alone refuses.
    await open(
      snapshotOf({
        source,
        verification_scope: "structural-audit-only",
        items: [messageItem({ id: "m1", sequence: 1, role: "user", text: "list files", verification_scope: "structural-audit-only" })],
      }),
    );
    expect(root().textContent).toContain("Continuity gap (invalid item)");
    const status = required(root().querySelector("[role='status']"), "live status line");
    expect(status.textContent).toContain("Continuity gap (invalid item)");
    expect(status.textContent).not.toMatch(/succeeded|verified/);
    // The chat holds the one admitted record, and its only chip is that record's reading.
    expect(chatChips()).toEqual([["reported, not verified", "reported"]]);
    expect(postureText()).toBe("no decisions or results projected");
    // The ledger is mounted only while its view is selected: switch to it, then read its rows.
    trajectory();
    expect(root().querySelector("#view-panel-trajectory")).not.toBeNull();
    expect(ledgerIds()).toEqual(["m1"]);
  });

  it("a success the projection admits reads as reported on every surface, never verified", async () => {
    await open(fixture(), diagnosticRecords());
    expect(chatChips()).toContainEqual(["reported", "reported"]);
    expect(chatChips().some(([text]) => text === "succeeded")).toBe(false);
    expect(postureText()).toMatch(/reported 1/);
    expect(postureText()).not.toMatch(/verified 1/);
    // The live status line exists and says the stream connected: it is read positively before the negative check.
    const status = required(root().querySelector("[role='status']"), "live status line");
    expect(status.textContent).toContain("Stream connected.");
    expect(status.textContent).not.toMatch(/succeeded|verified/);
    trajectory();
    const row = rowOf("r1");
    expect(row.getAttribute("aria-label")).toMatch(/Result: succeeded, reported, not independently verified/);
    expect(row.getAttribute("aria-label")).not.toMatch(/succeeded, verified/);
    expect(dotTone(row)).toBe("reported");
    expect(root().querySelectorAll("#view-panel-trajectory [data-tone='ok']")).toHaveLength(0);
    const tools = root().querySelector("[role='img'][aria-label^='Tools lane']");
    expect(tools?.getAttribute("aria-label")).toMatch(/succeeded, reported, not independently verified/);
    expect(tools?.getAttribute("aria-label")).not.toMatch(/succeeded, verified/);
    click(rowOf("r1"));
    expect(evidenceValue("independently verified")).toBe("no");
    const state = root().querySelector("aside [data-section='result'] [data-tone]");
    expect(state?.textContent).toBe("reported");
  });
});
}

describe("one verification gate on every surface: a live structural-audit projection", () => {
  it("reads the same observed records as verified, on every surface", async () => {
    const live = snapshotOf({
      source: "live",
      verification_scope: "structural-audit-only",
      items: [
        messageItem({ id: "m1", sequence: 1, role: "user", text: "list files", authority: "observed", verification_scope: "structural-audit-only" }),
      ],
    });
    await open(live);
    expect(chatChips()).toContainEqual(["succeeded", "ok"]);
    expect(postureText()).toMatch(/verified 1/);
    trajectory();
    expect(rowOf("r1").getAttribute("aria-label")).toMatch(/Result: succeeded, verified\./);
    expect(dotTone(rowOf("r1"))).toBe("ok");
    click(rowOf("r1"));
    expect(evidenceValue("independently verified")).toBe("yes");
  });
});

// The chat head and the message marks: a session header carries no evidence, so its status
// is always read as reported, not verified; a message reads its own verdict of the gate.
describe("the conversation head and the message marks: reported, not verified unless the gate verifies", () => {
  const session = { session_id: "session-demo-01", workflow_id: "wf-demo-01", title: "Demo", pi_version: "0.87.1", status: "completed", turn_count: 1 } as const;

  it("in a fixture projection, the head and the message read reported, not verified, and no tone is success", async () => {
    await open(
      snapshotOf({ source: "fixture", session, items: [messageItem({ id: "m1", sequence: 1, role: "user", text: "list files" })] }),
      [],
    );
    expect(root().textContent).toContain("completed · reported, not verified");
    expect(root().querySelector("[role='log'] div[data-role='user'] p")?.textContent).toBe("User reported, not verified");
    expect(chatChips()).toContainEqual(["reported, not verified", "reported"]);
    expect(chatChips().some(([text, tone]) => tone === "ok" || text === "verified")).toBe(false);
    // The head: the session status, read in the view's own status line, as reported, not verified.
    const head = required(
      Array.from(workspace().querySelectorAll("p")).find((p) => p.textContent === "completed · reported, not verified"),
      "conversation head status",
    );
    expect(head.textContent).toBe("completed · reported, not verified");
    expect(toneMarks(head)).not.toContain("ok");
    // The posture: the observed posture group, read as its one empty line, with no success mark.
    const posture = required(root().querySelector("[role='group'][aria-label='Observed posture']"), "observed posture");
    expect(posture.textContent).toBe("no decisions or results projected");
    expect(toneMarks(posture)).not.toContain("ok");
    // The rail: the session row's status dot, reported, and no success mark anywhere in the rail.
    const rail = required(root().querySelector("nav[aria-label='Sessions and streams']"), "sessions rail");
    expect(required(rail.querySelector("li[aria-current='true'] [data-tone]"), "session status dot").getAttribute("data-tone")).toBe(
      "reported",
    );
    expect(toneMarks(rail)).not.toContain("ok");
    // The inspector: the governance panel's tabs carry marks, and none is a success mark.
    const inspector = required(root().querySelector("aside[aria-label='Governance inspector']"), "governance inspector");
    expect(inspector.textContent).toContain("Decisions · no decisions");
    expect(toneMarks(inspector).length).toBeGreaterThan(0);
    expect(toneMarks(inspector)).not.toContain("ok");
  });

  it("in a live projection, the head still reads reported, not verified, and a verified message reads verified", async () => {
    const live = snapshotOf({
      source: "live",
      verification_scope: "structural-audit-only",
      session,
      items: [
        messageItem({ id: "m1", sequence: 1, role: "user", text: "list files", authority: "observed", verification_scope: "structural-audit-only" }),
      ],
    });
    await open(live, []);
    expect(root().textContent).toContain("completed · reported, not verified");
    expect(chatChips()).toContainEqual(["verified", "ok"]);
  });

  it("the footer reads the statistics as one line, and no statistic is drawn in a success tone", async () => {
    // No governance counts are stated, so the verdicts read unavailable. A stated zero would read "0 allow / 0 deny / 0 unknown".
    await open(snapshotOf({ source: "fixture", session, items: [], panels: { ...snapshotOf().panels, governance: {} } }), []);
    const line = root().querySelector("[role='group'][aria-label='Session statistics']");
    expect(line?.textContent).toMatch(/ · tokens unavailable · timing unavailable$/);
    expect(line?.textContent).toContain(" · policy verdicts unavailable · ");
    expect(line?.querySelector("[data-tone='ok']")).toBeNull();
  });
});
