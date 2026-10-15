// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { Kbd } from "./Kbd";
import { VisuallyHidden } from "./VisuallyHidden";
import { mount } from "../test-support/dom";

describe("VisuallyHidden", () => {
  it("keeps its text in the accessibility tree, as a span", () => {
    const view = mount(<VisuallyHidden>Stream connected</VisuallyHidden>);
    const span = view.container.querySelector("span");
    expect(span?.textContent).toBe("Stream connected");
    expect(span?.hasAttribute("aria-hidden")).toBe(false);
    view.unmount();
  });
});

describe("Kbd", () => {
  it("renders a key cap as kbd, with its key name as text", () => {
    const view = mount(<Kbd>Esc</Kbd>);
    const key = view.container.querySelector("kbd");
    expect(key?.textContent).toBe("Esc");
    view.unmount();
  });
});
