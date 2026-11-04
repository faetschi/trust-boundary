// @vitest-environment happy-dom
import { act } from "react";
import { describe, expect, it } from "vitest";
import { Trace, TraceIcon, TraceLine, TraceMeta, TraceTitle } from "./Trace";
import { mount } from "../test-support/dom";

function sample(defaultOpen = false) {
  return (
    <Trace
      tone="reported"
      defaultOpen={defaultOpen}
      line={
        <TraceLine>
          <TraceIcon>R</TraceIcon>
          <TraceTitle title="service/entrypoint.go" detail="read record 5" />
          <TraceMeta>reported</TraceMeta>
        </TraceLine>
      }
    >
      <p>request</p>
    </Trace>
  );
}

describe("Trace", () => {
  it("is a native details element, closed by default, with the state tone as a data attribute", () => {
    const view = mount(sample());
    const details = view.container.querySelector("details") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(details.getAttribute("data-tone")).toBe("reported");
    expect(view.container.querySelector("summary")?.textContent).toContain("service/entrypoint.go");
    view.unmount();
  });

  it("hides its icon from assistive technology and keeps the full title in the title attribute", () => {
    const view = mount(sample());
    expect(view.container.querySelector("[aria-hidden='true']")?.textContent).toBe("R");
    expect(view.container.querySelector("[title='service/entrypoint.go']")).not.toBeNull();
    view.unmount();
  });

  it("keeps its open state across a toggle and a re-render", () => {
    const view = mount(sample());
    const details = view.container.querySelector("details") as HTMLDetailsElement;
    act(() => {
      details.open = true;
      details.dispatchEvent(new Event("toggle"));
    });
    act(() => view.root.render(sample()));
    expect((view.container.querySelector("details") as HTMLDetailsElement).open).toBe(true);
    view.unmount();
  });
});
