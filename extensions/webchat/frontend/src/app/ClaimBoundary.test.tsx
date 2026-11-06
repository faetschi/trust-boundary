// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from "vitest";
import type { QualificationPanel, Snapshot } from "../core/contract";
import { panelsDefault, snapshotOf } from "../core/testing/builders";
import { installMatchMedia, mount, type MatchMediaShim, type Mounted } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { ClaimBoundary, DISPLAY_ONLY_TEXT, UNVERIFIED_CLAIM_TEXT } from "./ClaimBoundary";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";

const UNLOADED = "unavailable (no projection loaded)";
const ABSENT = "unavailable (the snapshot carries no qualification panel)";
const MISSING = "unavailable (not projected by the source)";

async function loaded(snapshot: Snapshot): Promise<WorkspaceModel> {
  const store = createWorkspaceStore(fakeWorkspace(snapshot).deps);
  store.start();
  await flush();
  return store.getSnapshot();
}

// The model before the first snapshot: no projection, so no panels.
function unloaded(): WorkspaceModel {
  return createWorkspaceStore(fakeWorkspace(snapshotOf()).deps).getSnapshot();
}

function show(model: WorkspaceModel): Mounted {
  return mount(<ClaimBoundary model={model} />);
}

// The value of each claim, in order: the first element of each definition, before any warning chip.
function valuesOf(view: Mounted): string[] {
  return Array.from(view.container.querySelectorAll("dd")).map((dd) => dd.firstElementChild?.textContent ?? "");
}

// The warning chips, by their text: the chips inside a value cell. The note itself also carries a data-tone.
function warningsOf(view: Mounted): string[] {
  return Array.from(view.container.querySelectorAll("dd [data-tone]")).map((chip) => chip.textContent ?? "");
}

function withQualification(qualification: QualificationPanel): Snapshot {
  return snapshotOf({ panels: { ...panelsDefault(), qualification } });
}

describe("the claim boundary reads the projection, and never a literal", () => {
  it("before a projection loads, every value is unavailable with its reason, and the display-only line is shown", () => {
    const view = show(unloaded());
    expect(valuesOf(view)).toEqual([UNLOADED, UNLOADED, UNLOADED, UNLOADED]);
    expect(warningsOf(view)).toEqual([]);
    expect(view.container.textContent).toContain(DISPLAY_ONLY_TEXT);
    view.unmount();
  });

  it("with a loaded projection, each value is shown as projected, with no warning", async () => {
    const view = show(await loaded(snapshotOf()));
    expect(valuesOf(view)).toEqual(["false", "unexecuted", "not-established", "false"]);
    expect(warningsOf(view)).toEqual([]);
    expect(view.container.textContent).toContain(DISPLAY_ONLY_TEXT);
    view.unmount();
  });

  it("shows a projected true from an unverified source as true, with the warning chip, never as a pass", async () => {
    const view = show(
      await loaded(withQualification({ g1: true, h1: "unexecuted", containment: "not-established", claim_bearing: true })),
    );
    expect(valuesOf(view)).toEqual(["true", "unexecuted", "not-established", "true"]);
    expect(warningsOf(view)).toEqual([UNVERIFIED_CLAIM_TEXT, UNVERIFIED_CLAIM_TEXT]);
    const chips = Array.from(view.container.querySelectorAll("dd [data-tone]"));
    expect(chips.length).toBe(2);
    expect(chips.every((chip) => chip.getAttribute("data-tone") !== "ok")).toBe(true);
    view.unmount();
  });

  it("reads every value as unavailable, with the panel's reason, when a loaded projection carries no qualification panel", async () => {
    const model = await loaded(snapshotOf());
    const view = show({ ...model, panels: { ...panelsDefault(), qualification: undefined as unknown as QualificationPanel } });
    expect(valuesOf(view)).toEqual([ABSENT, ABSENT, ABSENT, ABSENT]);
    expect(warningsOf(view)).toEqual([]);
    view.unmount();
  });

  it("reads only the missing field as unavailable, and keeps the warning on a true g1", async () => {
    const model = await loaded(snapshotOf());
    const partial = { g1: true, h1: "unexecuted" } as unknown as QualificationPanel;
    const view = show({ ...model, panels: { ...panelsDefault(), qualification: partial } });
    expect(valuesOf(view)).toEqual(["true", "unexecuted", MISSING, MISSING]);
    expect(warningsOf(view)).toEqual([UNVERIFIED_CLAIM_TEXT]);
    view.unmount();
  });
});

// The width the viewport reports. The shim is restored after each test.
let shim: MatchMediaShim | null = null;
afterEach(() => {
  shim?.restore();
  shim = null;
});

describe("the claim boundary keeps its two sentences beside its values", () => {
  for (const width of [390, 1440]) {
    it(`at ${width} px, the values and both sentences are in the popover body, with nothing folded`, async () => {
      shim = installMatchMedia(width);
      const view = show(await loaded(snapshotOf({ panels: panelsDefault() })));
      expect(view.container.querySelector("details")).toBeNull();
      expect(view.container.textContent).toContain("Presentation only: the supervisor retains policy, effect and publication authority.");
      expect(view.container.textContent).toContain(DISPLAY_ONLY_TEXT);
      const claims = view.container.querySelector("dl");
      expect(claims?.textContent).toContain("G1");
      expect(claims?.textContent).toContain("Claim-bearing");
      view.unmount();
    });
  }
});