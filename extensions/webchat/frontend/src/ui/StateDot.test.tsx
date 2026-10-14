// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { StateDot } from "./StateDot";
import { mount } from "../test-support/dom";

// Run from the frontend directory (npm run test), so the path is plain.
const css = readFileSync(resolve(process.cwd(), "src/ui/StateDot.module.css"), "utf8");

// The shape rule for each tone, read from the stylesheet. A tone's shape must differ from
// every other tone's, so the dot carries its state without colour.
function shapeOf(tone: string): string {
  const match = new RegExp(String.raw`\.dot\[data-tone="${tone}"\]\s*\{([^}]*)\}`).exec(css);
  if (!match || match[1] === undefined) throw new Error(`no rule for ${tone}`);
  const body = match[1];
  const border = /border:\s*([^;]+);/.exec(body)?.[1] ?? "";
  const radius = /border-radius:\s*([^;]+);/.exec(body)?.[1] ?? "50%";
  const fill = /background:\s*([^;]+);/.exec(body)?.[1] ?? "";
  return [border, radius, fill.includes("transparent") ? "hollow" : "solid"].join("|");
}

describe("StateDot", () => {
  it("is decorative: the owning element names the state, so the dot is hidden from assistive technology", () => {
    const view = mount(<StateDot tone="ok" />);
    const dot = view.container.querySelector("span");
    expect(dot?.getAttribute("aria-hidden")).toBe("true");
    expect(dot?.hasAttribute("aria-label")).toBe(false);
    expect(dot?.textContent).toBe("");
    view.unmount();
  });

  it("marks its tone in a data attribute", () => {
    const view = mount(<StateDot tone="deny" />);
    expect(view.container.querySelector("span")?.getAttribute("data-tone")).toBe("deny");
    view.unmount();
  });

  it("gives each state a shape of its own, so the state survives without colour", () => {
    // ok is a filled circle; idle is also a filled circle, and differs by its grey colour and
    // its owner's text, which is the documented exception (design-language.md, section 7).
    const tones = ["ok", "allow", "deny", "reported", "pending", "unknown", "lost"];
    const shapes = tones.map(shapeOf);
    expect(new Set(shapes).size).toBe(tones.length);
    expect(shapeOf("idle")).toBe(shapeOf("ok"));
    expect(shapeOf("idle")).toBe("|50%|solid");
  });

  it("keeps the shape under forced colours by drawing borders, with system colours only", () => {
    expect(css).toMatch(/@media \(forced-colors: active\)/);
    expect(css).toMatch(/forced-color-adjust:\s*none/);
    expect(css).toMatch(/CanvasText/);
  });
});
