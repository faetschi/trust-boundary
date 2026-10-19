// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
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

// The rules that name a tone, read from the stylesheet. A selector list can name several tones, so
// every rule whose selector names this tone is returned.
function rulesFor(css: string, tone: string): string[] {
  const rules: string[] = [];
  for (const match of css.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
    const selector = match[1] ?? "";
    if (selector.includes(`data-tone="${tone}"`)) rules.push(match[2] ?? "");
  }
  return rules;
}

describe("Chip: LOST is in the unknown family, with a shape of its own", () => {
  const css = readFileSync(resolve(process.cwd(), "src/ui/Chip.module.css"), "utf8");

  it("never takes the deny colour, so a LOST chip is not a refusal", () => {
    const rules = rulesFor(css, "lost");
    expect(rules.length).toBeGreaterThan(0);
    for (const body of rules) expect(body).not.toMatch(/--state-deny/);
  });

  it("takes the unknown family's ink and tint, and draws a double border that unknown and reported do not", () => {
    const rules = rulesFor(css, "lost");
    expect(rules.some((body) => /--local-ink:\s*var\(--state-reported-ink\)/.test(body))).toBe(true);
    expect(rules.some((body) => /--local-tint:\s*var\(--state-reported\)/.test(body))).toBe(true);
    expect(rules.some((body) => /border-style:\s*double/.test(body))).toBe(true);
    expect(rulesFor(css, "unknown").some((body) => /border-style:\s*double/.test(body))).toBe(false);
  });
});
