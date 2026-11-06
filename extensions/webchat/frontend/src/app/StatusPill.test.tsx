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

describe("the status pill says where the claim boundary stands for a source that is not a demonstration", () => {
  it("names containment on a live, verified-looking projection, so the boundary's reading is on the pill", async () => {
    const live = await loaded(snapshotOf({ source: "live", verification_scope: "structural-audit-only" }));
    expect(pillWords(live, false)).toContain("containment not established");
    expect(pillWords({ ...live, panels: null }, false)).toContain("claim boundary unavailable");
  });

  it("does not add it to a demonstration, whose own words already say it shows no live claim", async () => {
    expect(pillWords(await loaded(snapshotOf()), false)).not.toContain("containment");
  });
});

describe("the status pill's dot and the order of its words", () => {
  const toneOf = (view: ReturnType<typeof mount>) => view.container.querySelector("button [data-tone]")?.getAttribute("data-tone");

  it("draws a dropped connection as unknown, never the refusal square", async () => {
    const model = await loaded(snapshotOf());
    const view = mount(<StatusPill model={{ ...model, status: { ...model.status, state: "error", label: "offline" } }} noSession={false} />);
    expect(toneOf(view)).toBe("unknown");
    view.unmount();
  });

  it("draws a demonstration and a source whose containment is not established as the reported ring, never quiet", async () => {
    const demo = mount(<StatusPill model={await loaded(snapshotOf())} noSession={false} />);
    expect(toneOf(demo)).toBe("reported");
    demo.unmount();
    const live = mount(<StatusPill model={await loaded(snapshotOf({ source: "live", verification_scope: "structural-audit-only" }))} noSession={false} />);
    expect(toneOf(live)).toBe("reported");
    live.unmount();
  });

  it("puts the claim words before the connection words, so a cut or a wrap never takes the claim away", async () => {
    const live = await loaded(snapshotOf({ source: "live", verification_scope: "structural-audit-only" }));
    const words = pillWords({ ...live, status: { ...live.status, state: "connecting", label: "connecting…" } }, false);
    expect(words.indexOf("containment not established")).toBeLessThan(words.indexOf("connecting"));
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

  it("closes when focus leaves the pill and its popover, so Escape is not left for the shell to read as clearing a record", async () => {
    const view = mount(
      <>
        <StatusPill model={await loaded(snapshotOf())} noSession={false} />
        <button type="button">elsewhere</button>
      </>,
    );
    const pill = view.container.querySelector("button") as HTMLButtonElement;
    act(() => pill.click());
    act(() => pill.focus());
    const elsewhere = view.container.querySelectorAll("button")[view.container.querySelectorAll("button").length - 1] as HTMLButtonElement;
    act(() => elsewhere.focus());
    expect(pill.getAttribute("aria-expanded")).toBe("false");
    view.unmount();
  });

  it("stays open when the reader presses inside it, which moves focus to the popover and not out of the pill", async () => {
    const view = mount(<StatusPill model={await loaded(snapshotOf())} noSession={false} />);
    const pill = view.container.querySelector("button") as HTMLButtonElement;
    const popover = document.getElementById(pill.getAttribute("aria-controls") as string) as HTMLElement;
    expect(popover.tabIndex).toBe(-1);
    act(() => pill.click());
    act(() => pill.focus());
    act(() => popover.focus());
    expect(pill.getAttribute("aria-expanded")).toBe("true");
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
