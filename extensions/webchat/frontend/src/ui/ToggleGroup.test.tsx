// @vitest-environment happy-dom
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ToggleGroup } from "./ToggleGroup";
import { click, mount, press } from "../test-support/dom";

const ITEMS = [
  { value: "all", label: "All", count: 7 },
  { value: "denied", label: "Denied", count: 2 },
  { value: "unknown", label: "Unknown", count: 0 },
];

function Harness({ onPress }: { readonly onPress?: (value: string) => void }) {
  const [pressed, setPressed] = useState("all");
  return (
    <ToggleGroup
      label="Filter timeline"
      items={ITEMS}
      pressed={pressed}
      onPress={(value) => {
        setPressed(value);
        onPress?.(value);
      }}
    />
  );
}

function buttonsOf(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>("button"));
}

describe("ToggleGroup: a labelled toolbar with one tab stop", () => {
  it("is a labelled toolbar of buttons that state their pressed state and counts", () => {
    const view = mount(<Harness />);
    expect(view.container.querySelector("[role='toolbar']")?.getAttribute("aria-label")).toBe("Filter timeline");
    const [all, denied] = buttonsOf(view.container);
    expect(all?.getAttribute("aria-pressed")).toBe("true");
    expect(denied?.getAttribute("aria-pressed")).toBe("false");
    expect(all?.textContent?.trim()).toBe("All 7");
    view.unmount();
  });

  it("gives each button its hint as the title, so what its count counts is stated", () => {
    const view = mount(
      <ToggleGroup
        label="Filter timeline"
        items={[{ value: "denied", label: "Denied outcome", count: 2, hint: "records with a DENY verdict or a denied result" }]}
        pressed="denied"
        onPress={() => undefined}
      />,
    );
    expect(buttonsOf(view.container)[0]?.getAttribute("title")).toBe("records with a DENY verdict or a denied result");
    view.unmount();
  });

  it("is a button group, not a switch: no role=switch, and every control is a button", () => {
    const view = mount(<Harness />);
    expect(view.container.querySelector("[role='switch']")).toBeNull();
    expect(view.container.querySelectorAll("[role='toolbar'] > *").length).toBe(buttonsOf(view.container).length);
    view.unmount();
  });

  it("keeps exactly one button in the tab order", () => {
    const view = mount(<Harness />);
    const stops = buttonsOf(view.container).filter((button) => button.tabIndex === 0);
    expect(stops).toHaveLength(1);
    expect(stops[0]?.getAttribute("aria-pressed")).toBe("true");
    view.unmount();
  });

  it("moves focus with the arrow keys without changing aria-pressed", () => {
    const view = mount(<Harness />);
    const [all] = buttonsOf(view.container);
    all?.focus();
    press(all as HTMLElement, "ArrowRight");
    const [, denied] = buttonsOf(view.container);
    expect(document.activeElement).toBe(denied);
    expect(denied?.getAttribute("aria-pressed")).toBe("false");
    expect(all?.getAttribute("aria-pressed")).toBe("true");
    view.unmount();
  });

  it("wraps with the arrow keys and jumps with Home and End", () => {
    const view = mount(<Harness />);
    const buttons = buttonsOf(view.container);
    press(buttons[0] as HTMLElement, "ArrowLeft");
    expect(document.activeElement).toBe(buttons[2]);
    press(buttons[2] as HTMLElement, "ArrowRight");
    expect(document.activeElement).toBe(buttons[0]);
    press(buttons[0] as HTMLElement, "End");
    expect(document.activeElement).toBe(buttons[2]);
    press(buttons[2] as HTMLElement, "Home");
    expect(document.activeElement).toBe(buttons[0]);
    view.unmount();
  });

  it("presses on click, and the tab stop follows the press", () => {
    const onPress = vi.fn();
    const view = mount(<Harness onPress={onPress} />);
    click(buttonsOf(view.container)[1] as HTMLElement);
    expect(onPress).toHaveBeenCalledWith("denied");
    const pressed = buttonsOf(view.container).find((button) => button.getAttribute("aria-pressed") === "true");
    expect(pressed?.textContent?.trim()).toBe("Denied 2");
    expect(pressed?.tabIndex).toBe(0);
    view.unmount();
  });
});
