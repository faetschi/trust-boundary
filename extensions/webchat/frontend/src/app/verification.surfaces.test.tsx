// @vitest-environment happy-dom
// One verification gate on every surface (verification.ts). The same observed, structurally audited
// records are streamed into a fixture projection and into a live one. The stream does not refuse an
// observed record against the snapshot's source (only the snapshot is checked), so a fixture
// projection can hold one. In that projection every surface must read it as reported, and only a
// live structural-audit projection may read it as verified. Mounted in the real shell and store.
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

async function open(snapshot: Snapshot): Promise<void> {
  shim = installMatchMedia(1440);
  fake = fakeWorkspace(snapshot);
  mounted = mount(
    <WorkspaceProvider deps={fake.deps}>
      <Shell version="0.1.0" />
    </WorkspaceProvider>,
  );
  await settle();
  act(() => fake.sources[0]?.open());
  for (const item of observedRecords()) {
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

function postureText(): string {
  return root().querySelector("[role='group'][aria-label='Observed posture']")?.textContent ?? "";
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

  it("the chat chips, the posture and the announcer read the streamed success as reported", async () => {
    await open(fixture());
    expect(chatChips()).toContainEqual(["reported", "reported"]);
    expect(chatChips().some(([text]) => text === "succeeded")).toBe(false);
    expect(postureText()).toMatch(/reported 1/);
    expect(postureText()).not.toMatch(/verified 1/);
    expect(root().querySelector("[role='status']")?.textContent ?? "").not.toMatch(/succeeded|verified/);
  });

  it("the trajectory row, its dot and the sequence rail read the streamed success as reported, never ok", async () => {
    await open(fixture());
    trajectory();
    const row = rowOf("r1");
    expect(row.getAttribute("aria-label")).toMatch(/Result: succeeded, reported, not independently verified/);
    expect(row.getAttribute("aria-label")).not.toMatch(/succeeded, verified/);
    expect(dotTone(row)).toBe("reported");
    expect(root().querySelectorAll("#view-panel-trajectory [data-tone='ok']")).toHaveLength(0);
    const tools = root().querySelector("[role='img'][aria-label^='Tools lane']");
    expect(tools?.getAttribute("aria-label")).toMatch(/succeeded, reported, not independently verified/);
    expect(tools?.getAttribute("aria-label")).not.toMatch(/succeeded, verified/);
  });

  it("the record inspector reads independently verified: no, with the state as reported", async () => {
    await open(fixture());
    trajectory();
    click(rowOf("r1"));
    expect(evidenceValue("independently verified")).toBe("no");
    const state = root().querySelector("aside [data-section='result'] [data-tone]");
    expect(state?.textContent).toBe("reported");
  });

  it("a live structural-audit projection reads the same records as verified, on every surface", async () => {
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
}
