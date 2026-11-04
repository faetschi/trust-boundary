// @vitest-environment happy-dom
import { act } from "react";
import { describe, expect, it } from "vitest";
import type { ItemView } from "../../../core/contract";
import { mount } from "../../../test-support/dom";
import { blocksOf, type ToolCallSpec } from "../../../test-support/toolBlocks";
import type { ToolRun as ToolRunData } from "./toolGroups";
import { ToolRun } from "./ToolGroup";

const verified = (_item: ItemView): boolean => true;
const unverified = (_item: ItemView): boolean => false;
const noMarker = (): null => null;

const SPECS: ToolCallSpec[] = [
  { call: "c1", tool: "read", verdict: "ALLOW", result: "succeeded" },
  { call: "c2", tool: "edit", verdict: "ALLOW", result: "succeeded" },
  { call: "c3", tool: "bash", verdict: "DENY", result: "denied" },
];

function runOf(count: number, specs: ToolCallSpec[] = SPECS): ToolRunData {
  return { kind: "run", key: "run-c1", blocks: blocksOf(...specs.slice(0, count)) };
}

function draw(run: ToolRunData, verifiedOf = verified, markOf: (item: ItemView) => "verified" | "unverified" | null = noMarker) {
  return <ToolRun run={run} verifiedOf={verifiedOf} markOf={markOf} />;
}

describe("a run of fewer than three tools", () => {
  it("is the tool cards alone: no group line", () => {
    const view = mount(draw(runOf(2)));
    expect(view.container.querySelector("[data-group]")).toBeNull();
    expect(view.container.querySelectorAll("details")).toHaveLength(2);
    view.unmount();
  });
});

describe("a group of three or more", () => {
  it("is a closed line that is a button, naming the count, the tools and each finding, and the worst tone", () => {
    const view = mount(draw(runOf(3), unverified));
    const group = view.container.querySelector("[data-group]") as HTMLElement;
    expect(group.getAttribute("data-tone")).toBe("deny");
    const toggle = group.querySelector("button") as HTMLButtonElement;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain("3 tool actions");
    expect(toggle.textContent).toContain("read · edit · bash");
    expect(toggle.textContent).toContain("1 DENY");
    expect(toggle.textContent).toContain("1 denied");
    expect(toggle.textContent).toContain("2 reported");
    // The body is in the page, hidden, and is the element the button controls.
    const body = document.getElementById(toggle.getAttribute("aria-controls") as string) as HTMLElement;
    expect(body.hidden).toBe(true);
    expect(body.querySelectorAll("details")).toHaveLength(3);
    view.unmount();
  });

  it("opens and closes with its line", () => {
    const view = mount(draw(runOf(3)));
    const toggle = view.container.querySelector("[data-group] button") as HTMLButtonElement;
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect((document.getElementById(toggle.getAttribute("aria-controls") as string) as HTMLElement).hidden).toBe(false);
    act(() => toggle.click());
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    view.unmount();
  });

  it("says the unverified wording on its line: as a chip in a mixed view, for screen readers only in a uniform view", () => {
    const mixed = mount(draw(runOf(3), unverified, () => "unverified"));
    expect(mixed.container.querySelector("[data-group] button")?.textContent).toContain("3 unverified");
    mixed.unmount();
    const uniform = mount(draw(runOf(3), unverified, noMarker));
    const line = uniform.container.querySelector("[data-group] button")?.textContent ?? "";
    // The word is there for a screen reader, but no count chip is drawn: the view states "unverified" once above the transcript.
    expect(line).toContain("unverified");
    expect(line).not.toContain("3 unverified");
    uniform.unmount();
  });
});

describe("a card in an opened group says what the group line says", () => {
  it("reads three requests that say succeeded, with no results, as 'no result' on every card chip", () => {
    const specs: ToolCallSpec[] = [
      { call: "c1", request: "succeeded" },
      { call: "c2", request: "succeeded" },
      { call: "c3", request: "succeeded" },
    ];
    const view = mount(draw(runOf(3, specs), verified));
    expect(view.container.querySelector("[data-group] button")?.textContent).toContain("3 no result");
    const chips = Array.from(view.container.querySelectorAll("details > summary [data-tone]")).map((chip) => chip.textContent);
    expect(chips).toEqual(["no result", "no result", "no result"]);
    view.unmount();
  });
});

describe("a run that changes size does not remount its cards", () => {
  it("keeps an open card when a filter removes the run's first block", () => {
    const specs: ToolCallSpec[] = [
      { call: "c1", verdict: "ALLOW", result: "succeeded" },
      { call: "c2", verdict: "DENY", result: "denied" },
    ];
    const view = mount(draw({ kind: "run", key: "run-0", blocks: blocksOf(...specs) }));
    const second = view.container.querySelectorAll("details")[1] as HTMLDetailsElement;
    act(() => {
      second.open = true;
      second.dispatchEvent(new Event("toggle"));
    });
    const survivor = blocksOf(...specs).filter((block) => block.key === "c2");
    act(() => view.root.render(draw({ kind: "run", key: "run-0", blocks: survivor })));
    const only = view.container.querySelector("details") as HTMLDetailsElement;
    expect(only).toBe(second);
    expect(only.open).toBe(true);
    view.unmount();
  });

  it("keeps an open card, and the group opens, when a third tool arrives", () => {
    const view = mount(draw(runOf(2)));
    const first = view.container.querySelector("details") as HTMLDetailsElement;
    act(() => {
      first.open = true;
      first.dispatchEvent(new Event("toggle"));
    });
    act(() => view.root.render(draw(runOf(3))));
    const group = view.container.querySelector("[data-group]") as HTMLElement;
    const again = group.querySelector("details") as HTMLDetailsElement;
    expect(again).toBe(first);
    expect(again.open).toBe(true);
    expect((group.querySelector("button") as HTMLButtonElement).getAttribute("aria-expanded")).toBe("true");
    view.unmount();
  });

  it("keeps the focused card focused when the group forms, and again when it shrinks", () => {
    const view = mount(draw(runOf(2)));
    const summary = view.container.querySelector("details > summary") as HTMLElement;
    act(() => summary.focus());
    expect(document.activeElement).toBe(summary);
    act(() => view.root.render(draw(runOf(3))));
    expect(document.activeElement).toBe(summary);
    act(() => view.root.render(draw(runOf(2))));
    expect(document.activeElement).toBe(summary);
    expect(view.container.querySelector("[data-group]")).toBeNull();
    view.unmount();
  });
});
