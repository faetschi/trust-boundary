// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
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
