// @vitest-environment happy-dom
import { act } from "react";
import { describe, expect, it } from "vitest";
import { panelsDefault, snapshotOf } from "../core/testing/builders";
import { mount } from "../test-support/dom";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { StatusPill, pillWords } from "./StatusPill";
import { createWorkspaceStore, type WorkspaceModel } from "./workspaceStore";
import type { Snapshot } from "../core/contract";

async function loaded(snapshot: Snapshot): Promise<WorkspaceModel> {
  const store = createWorkspaceStore(fakeWorkspace(snapshot).deps);
  store.start();
  await flush();
  return store.getSnapshot();
}

describe("the status pill's words", () => {
  it("name the demonstration and its unverified source first, then a connection that is not open", async () => {
    const model = await loaded(snapshotOf());
    expect(pillWords(model, false)).toMatch(/^Demonstration · unverified/);
    expect(pillWords({ ...model, status: { ...model.status, state: "error", label: "offline" } }, false)).toContain("offline");
  });

  it("says a claim the projection makes and nothing verifies, so the warning is not only inside the popover", async () => {
    const model = await loaded(
      snapshotOf({ panels: { ...panelsDefault(), qualification: { g1: true, h1: "unexecuted", containment: "not-established", claim_bearing: true } } }),
    );
    expect(pillWords(model, false)).toContain("unverified claim true");
  });

  it("does not say connecting when no session is chosen", async () => {
    const model = await loaded(snapshotOf());
    expect(pillWords(model, true)).toContain("no session selected");
  });
});

describe("the status pill's popover", () => {
  it("is closed, with the claim boundary in the page and hidden, and opens from the pill", async () => {
    const view = mount(<StatusPill model={await loaded(snapshotOf())} noSession={false} />);
    const pill = view.container.querySelector("button") as HTMLButtonElement;
    const popover = document.getElementById(pill.getAttribute("aria-controls") as string) as HTMLElement;
    expect(pill.getAttribute("aria-expanded")).toBe("false");
    expect(popover.hidden).toBe(true);
    expect(popover.querySelector("[role='note'][aria-label='Claim boundary']")).not.toBeNull();
    act(() => pill.click());
    expect(pill.getAttribute("aria-expanded")).toBe("true");
    expect(popover.hidden).toBe(false);
    expect(popover.textContent).toContain("demonstration · not a live session");
    expect(popover.textContent).toContain("Claim-bearing");
    view.unmount();
  });

  it("closes on Escape, returns focus to the pill, and stops the key there", async () => {
    const view = mount(<StatusPill model={await loaded(snapshotOf())} noSession={false} />);
    const pill = view.container.querySelector("button") as HTMLButtonElement;
    act(() => pill.click());
    let reached = false;
    const seen = () => {
      reached = true;
    };
    document.addEventListener("keydown", seen);
    act(() => {
      pill.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    document.removeEventListener("keydown", seen);
    expect(pill.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(pill);
    expect(reached).toBe(false);
    view.unmount();
  });
});
