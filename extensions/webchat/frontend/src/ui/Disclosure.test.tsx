// @vitest-environment happy-dom
import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { Disclosure } from "./Disclosure";
import { mount } from "../test-support/dom";

describe("Disclosure", () => {
  it("is a native details element, so the browser provides its keyboard and expanded state", () => {
    const view = mount(
      <Disclosure summary="Provenance">
        <p>Not independently verified here.</p>
      </Disclosure>,
    );
    const details = view.container.querySelector("details");
    expect(details?.open).toBe(false);
    expect(view.container.querySelector("summary")?.textContent).toBe("Provenance");
    view.unmount();
  });

  it("opens when defaultOpen is set, and reports each toggle", () => {
    const onToggle = vi.fn();
    const view = mount(
      <Disclosure summary="Warnings" defaultOpen onToggle={onToggle}>
        <p>None.</p>
      </Disclosure>,
    );
    const details = view.container.querySelector("details") as HTMLDetailsElement;
    expect(details.open).toBe(true);
    act(() => {
      details.open = false;
      details.dispatchEvent(new Event("toggle"));
    });
    expect(onToggle).toHaveBeenLastCalledWith(false);
    view.unmount();
  });
});
