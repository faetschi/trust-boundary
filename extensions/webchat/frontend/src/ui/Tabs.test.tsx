// @vitest-environment happy-dom
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { TabPanel, Tabs } from "./Tabs";
import { click, mount, press } from "../test-support/dom";

const ITEMS = [
  { id: "chat", label: "Chat" },
  { id: "trajectory", label: "Trajectory" },
  { id: "notes", label: "Notes" },
];

function Harness({ onChange }: { readonly onChange?: (id: string) => void }) {
  const [value, setValue] = useState("chat");
  return (
    <>
      <Tabs
        label="Workspace view"
        idPrefix="view"
        items={ITEMS}
        value={value}
        onValueChange={(id) => {
          setValue(id);
          onChange?.(id);
        }}
      />
      {ITEMS.map((item) => (
        <TabPanel key={item.id} idPrefix="view" id={item.id} value={value}>
          <p>{`${item.label} body`}</p>
        </TabPanel>
      ))}
    </>
  );
}

function tabsOf(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll<HTMLButtonElement>("[role='tab']"));
}

describe("Tabs: WAI-ARIA tabs with a roving tab stop", () => {
  it("is a labelled tablist whose tabs control their panels, with the selected tab in the tab order", () => {
    const view = mount(<Harness />);
    const list = view.container.querySelector("[role='tablist']");
    expect(list?.getAttribute("aria-label")).toBe("Workspace view");
    const [chat, trajectory] = tabsOf(view.container);
    expect(chat?.getAttribute("aria-selected")).toBe("true");
    expect(chat?.getAttribute("tabindex")).toBe("0");
    expect(trajectory?.getAttribute("aria-selected")).toBe("false");
    expect(trajectory?.getAttribute("tabindex")).toBe("-1");
    expect(chat?.getAttribute("aria-controls")).toBe("view-panel-chat");
    expect(view.container.querySelector("#view-panel-chat")?.getAttribute("aria-labelledby")).toBe("view-tab-chat");
    view.unmount();
  });

  it("mounts only the selected panel's children and hides the others", () => {
    const view = mount(<Harness />);
    expect(view.container.textContent).toContain("Chat body");
    expect(view.container.textContent).not.toContain("Trajectory body");
    const hidden = view.container.querySelector<HTMLElement>("#view-panel-trajectory");
    expect(hidden?.hidden).toBe(true);
    view.unmount();
  });

  it("selects on click and moves the tab stop with the selection", () => {
    const onChange = vi.fn();
    const view = mount(<Harness onChange={onChange} />);
    click(tabsOf(view.container)[1] as HTMLElement);
    expect(onChange).toHaveBeenLastCalledWith("trajectory");
    expect(tabsOf(view.container)[1]?.getAttribute("tabindex")).toBe("0");
    expect(view.container.textContent).toContain("Trajectory body");
    view.unmount();
  });

  it("moves with the arrow keys, wrapping at both ends, and selects the tab it lands on", () => {
    const view = mount(<Harness />);
    const [chat] = tabsOf(view.container);
    chat?.focus();
    press(chat as HTMLElement, "ArrowRight");
    expect(document.activeElement).toBe(tabsOf(view.container)[1]);
    expect(tabsOf(view.container)[1]?.getAttribute("aria-selected")).toBe("true");
    press(tabsOf(view.container)[1] as HTMLElement, "ArrowRight");
    press(tabsOf(view.container)[2] as HTMLElement, "ArrowRight");
    expect(document.activeElement).toBe(tabsOf(view.container)[0]);
    press(tabsOf(view.container)[0] as HTMLElement, "ArrowLeft");
    expect(document.activeElement).toBe(tabsOf(view.container)[2]);
    view.unmount();
  });

  it("jumps to the first and last tab with Home and End", () => {
    const view = mount(<Harness />);
    press(tabsOf(view.container)[0] as HTMLElement, "End");
    expect(document.activeElement).toBe(tabsOf(view.container)[2]);
    press(tabsOf(view.container)[2] as HTMLElement, "Home");
    expect(document.activeElement).toBe(tabsOf(view.container)[0]);
    view.unmount();
  });

  it("ignores other keys", () => {
    const view = mount(<Harness />);
    const chat = tabsOf(view.container)[0] as HTMLElement;
    chat.focus();
    press(chat, "a");
    expect(document.activeElement).toBe(chat);
    view.unmount();
  });
});
