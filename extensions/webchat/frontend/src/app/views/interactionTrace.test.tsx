// @vitest-environment happy-dom
import { act } from "react";
import { expect, it } from "vitest";
import { messageItem, snapshotOf } from "../../core/testing/builders";
import { fakeWorkspace } from "../../test-support/fakes";
import { click, installMatchMedia, mount } from "../../test-support/dom";
import { WorkspaceProvider } from "../WorkspaceProvider";
import { Shell } from "../Shell";

it("preserves selection, search order and keyboard focus across a full retained window", async () => {
  const media = installMatchMedia(1440);
  const fake = fakeWorkspace(snapshotOf({ items: Array.from({ length: 512 }, (_, n) => messageItem({ id: `m${n}`, sequence: n + 1, turn_id: "turn", text: n % 2 ? "odd evidence" : "even evidence" })) }));
  const mounted = mount(<WorkspaceProvider deps={fake.deps}><Shell version="test" /></WorkspaceProvider>);
  try {
    await act(async () => {});
    const tab = Array.from(mounted.container.querySelectorAll<HTMLButtonElement>("[role=tab]")).find(button => button.textContent === "Trajectory");
    if (!tab) throw new Error("trajectory tab absent");
    click(tab);
    const rows = () => Array.from(mounted.container.querySelectorAll<HTMLButtonElement>("button[data-record-row]"));
    expect(rows()).toHaveLength(512);
    const selected = rows()[254]; if (!selected) throw new Error("selection absent");
    click(selected);
    expect(selected.getAttribute("aria-pressed")).toBe("true");
    const inspector = mounted.container.querySelector('[aria-label="Record inspector"]');
    expect(inspector?.textContent).toContain("m254");
    expect(inspector?.textContent).toContain("even evidence");
    expect(inspector?.textContent).toContain("unverified-input");
    const input = mounted.container.querySelector<HTMLInputElement>("input[type=search]");
    if (!input) throw new Error("search absent");
    act(() => { Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, "even"); input.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(rows().map(row => row.dataset.id)).toEqual(Array.from({ length: 256 }, (_, n) => `m${n * 2}`));
    const first = rows()[0]; if (!first) throw new Error("row absent");
    act(() => { first.focus(); first.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true, cancelable: true })); });
    expect(document.activeElement).toBe(rows().at(-1));
    expect(rows().filter(row => row.tabIndex === 0)).toHaveLength(1);
    expect(fake.fetchUrls).toHaveLength(1);
  } finally { mounted.unmount(); media.restore(); }
});
