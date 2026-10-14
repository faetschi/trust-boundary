// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { Chip } from "./Chip";
import { mount } from "../test-support/dom";

describe("Chip", () => {
  it("carries its meaning in text, and the tone in a data attribute", () => {
    const view = mount(<Chip tone="reported">reported</Chip>);
    const chip = view.container.querySelector("span");
    expect(chip?.textContent).toBe("reported");
    expect(chip?.getAttribute("data-tone")).toBe("reported");
    expect(chip?.hasAttribute("data-source")).toBe(false);
    view.unmount();
  });

  it("renders a source chip with its provenance role as the data attribute", () => {
    const view = mount(<Chip source="assistant">assistant</Chip>);
    const chip = view.container.querySelector("span");
    expect(chip?.getAttribute("data-source")).toBe("assistant");
    expect(chip?.hasAttribute("data-tone")).toBe(false);
    view.unmount();
  });

  it("gives every state tone its own data value, so colour is never the only cue", () => {
    const tones = ["ok", "allow", "deny", "reported", "pending", "unknown", "idle", "lost"] as const;
    const view = mount(
      <div>
        {tones.map((tone) => (
          <Chip key={tone} tone={tone}>
            {tone}
          </Chip>
        ))}
      </div>,
    );
    const values = Array.from(view.container.querySelectorAll("span")).map((span) => span.getAttribute("data-tone"));
    expect(new Set(values).size).toBe(tones.length);
    view.unmount();
  });
});
