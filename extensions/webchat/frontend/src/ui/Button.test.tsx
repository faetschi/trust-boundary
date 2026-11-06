// @vitest-environment happy-dom
import { act } from "react";
import { describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { mount } from "../test-support/dom";

describe("Button", () => {
  it("is a native button that never submits a form, with variant and size as data attributes", () => {
    const view = mount(<Button>Jump to latest</Button>);
    const button = view.container.querySelector("button");
    expect(button?.type).toBe("button");
    expect(button?.getAttribute("data-variant")).toBe("default");
    expect(button?.getAttribute("data-size")).toBe("default");
    expect(button?.textContent).toBe("Jump to latest");
    view.unmount();
  });

  it("takes a variant and a size, and passes the accessible name and the click through", () => {
    const onClick = vi.fn();
    const view = mount(
      <Button variant="ghost" size="icon" aria-label="Copy message" onClick={onClick}>
        c
      </Button>,
    );
    const button = view.container.querySelector("button") as HTMLButtonElement;
    expect(button.getAttribute("data-variant")).toBe("ghost");
    expect(button.getAttribute("data-size")).toBe("icon");
    expect(button.getAttribute("aria-label")).toBe("Copy message");
    act(() => button.click());
    expect(onClick).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it("keeps the caller's className beside its own, for placement", () => {
    const view = mount(<Button className="placed">Go</Button>);
    expect(view.container.querySelector("button")?.className).toMatch(/placed/);
    view.unmount();
  });
});
