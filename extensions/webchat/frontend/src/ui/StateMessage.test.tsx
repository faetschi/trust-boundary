// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { EmptyState, ErrorState, LoadingState } from "./StateMessage";
import { mount } from "../test-support/dom";

describe("state messages", () => {
  it("renders an empty state with its title and body, and no live role", () => {
    const view = mount(
      <EmptyState title="Chat transcript: Phase 3b">
        <p>Renders later.</p>
      </EmptyState>,
    );
    const state = view.container.querySelector("[data-state]");
    expect(state?.getAttribute("data-state")).toBe("empty");
    expect(view.container.textContent).toContain("Chat transcript: Phase 3b");
    expect(view.container.querySelector("[role='status'], [role='alert'], [aria-live]")).toBeNull();
    view.unmount();
  });

  it("renders an error state as text, without an alert role, since the announcer carries the error", () => {
    const view = mount(<ErrorState title="Snapshot failed">The gateway did not answer.</ErrorState>);
    expect(view.container.querySelector("[data-state='error']")?.textContent).toContain("Snapshot failed");
    expect(view.container.querySelector("[role='alert']")).toBeNull();
    view.unmount();
  });

  it("marks a loading state busy and names what it waits for", () => {
    const view = mount(<LoadingState label="Loading projection" />);
    const loading = view.container.querySelector("[data-state='loading']");
    expect(loading?.getAttribute("aria-busy")).toBe("true");
    expect(loading?.textContent).toBe("Loading projection");
    view.unmount();
  });
});
