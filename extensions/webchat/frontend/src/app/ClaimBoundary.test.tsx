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

// The phone breakpoint is the width at which the explanation folds. The shim is restored after each test.
let shim: MatchMediaShim | null = null;
afterEach(() => {
  shim?.restore();
  shim = null;
});

describe("the claim boundary folds its explanation at a phone width, and keeps every value", () => {
  it("at a phone width, the two sentences are a closed disclosure, and every claim value stays outside it", async () => {
    shim = installMatchMedia(390);
    const view = show(await loaded(snapshotOf({ panels: panelsDefault() })));
    const details = view.container.querySelector("details");
    expect(details?.querySelector("summary")?.textContent).toBe("Presentation only · display only");
    expect(details?.hasAttribute("open")).toBe(false);
    expect(details?.textContent).toContain(DISPLAY_ONLY_TEXT);
    const claims = view.container.querySelector("dl");
    expect(claims?.closest("details")).toBeNull();
    expect(claims?.textContent).toContain("G1");
    expect(claims?.textContent).toContain("Claim-bearing");
    view.unmount();
  });

  it("at a wide width, the two sentences are plain text, with no disclosure", async () => {
    shim = installMatchMedia(1440);
    const view = show(await loaded(snapshotOf({ panels: panelsDefault() })));
    expect(view.container.querySelector("details")).toBeNull();
    expect(view.container.textContent).toContain(DISPLAY_ONLY_TEXT);
    view.unmount();
  });
});
