// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { Notice } from "./Notice";
import { mount } from "../test-support/dom";

describe("Notice", () => {
  it("is a labelled note, not a live region or an alert", () => {
    const view = mount(<Notice label="Claim boundary" tone="claim">Presentation only.</Notice>);
    const note = view.container.querySelector("[role]");
    expect(note?.getAttribute("role")).toBe("note");
    expect(note?.getAttribute("aria-label")).toBe("Claim boundary");
    expect(note?.hasAttribute("aria-live")).toBe(false);
    expect(note?.textContent).toBe("Presentation only.");
    view.unmount();
  });

  it("states its tone in a data attribute for the warning and claim treatments", () => {
    const view = mount(<Notice label="Continuity gap" tone="warning">Recovered.</Notice>);
    expect(view.container.querySelector("[role]")?.getAttribute("data-tone")).toBe("warning");
    view.unmount();
  });
});
