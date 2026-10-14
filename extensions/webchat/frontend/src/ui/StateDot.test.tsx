// @vitest-environment happy-dom
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { StateDot } from "./StateDot";
import { mount } from "../test-support/dom";

// Run from the frontend directory (npm run test), so the path is plain.
const css = readFileSync(resolve(process.cwd(), "src/ui/StateDot.module.css"), "utf8");
const FORCED = css.slice(css.indexOf("@media (forced-colors: active)"));

// The rule body for one tone, read from the stylesheet. part is "" for the dot itself, or "::after" for its
// centred mark. A tone can share a rule with others, so the first rule whose selector names the tone is the
// one that carries its own shape.
function ruleOf(tone: string, part = ""): string {
  const match = new RegExp(String.raw`\.dot\[data-tone="${tone}"\]${part}\s*\{([^}]*)\}`).exec(css);
  if (!match || match[1] === undefined) throw new Error(`no ${part || "dot"} rule for ${tone}`);
  return match[1];
}

// The declarations of a rule body, as property -> value.
function declarations(body: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const match of body.matchAll(/([a-z-]+)\s*:\s*([^;]+);/g)) {
    const [, name, value] = match;
    if (name !== undefined && value !== undefined) map.set(name, value.trim());
  }
  return map;
}

// The rule in the forced-colours block whose selector names the tone, or "" when there is none.
function forcedRuleOf(tone: string, part = ""): string {
  const match = new RegExp(String.raw`\.dot\[data-tone="${tone}"\]${part}(?![:\w])[^{]*\{([^}]*)\}`).exec(FORCED);
  return match?.[1] ?? "";
}

// The shape rule for each tone, read from the stylesheet. A tone's shape must differ from
// every other tone's, so the dot carries its state without colour.
function shapeOf(tone: string): string {
  const body = ruleOf(tone);
  const border = /border:\s*([^;]+);/.exec(body)?.[1] ?? "";
  const radius = /border-radius:\s*([^;]+);/.exec(body)?.[1] ?? "50%";
  const fill = /background:\s*([^;]+);/.exec(body)?.[1] ?? "";
  const clip = /clip-path:\s*([^;]+);/.exec(body)?.[1] ?? "";
  return [border, radius, fill.includes("transparent") ? "hollow" : "solid", clip].join("|");
}

// The centred mark a tone draws (its ::after rule): "centred", or "" when the tone has none.
function markOf(tone: string): string {
  return new RegExp(String.raw`\.dot\[data-tone="${tone}"\]::after\s*\{`).test(css) ? "centred" : "";
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
    const shapes = tones.map((tone) => shapeOf(tone) + markOf(tone));
    expect(new Set(shapes).size).toBe(tones.length);
    expect(shapeOf("idle")).toBe(shapeOf("ok"));
    expect(shapeOf("idle")).toBe("|50%|solid|");
  });

  it("draws ALLOW as a bullseye, a ring with a centred dot, and PENDING as a plain thin hollow ring", () => {
    expect(markOf("allow")).toBe("centred");
    expect(markOf("pending")).toBe("");
    expect(ruleOf("allow")).toMatch(/position:\s*relative/);
    expect(ruleOf("allow")).toMatch(/border:\s*2px solid var\(--state-allow\)/);
    expect(ruleOf("allow", "::after")).toMatch(/position:\s*absolute/);
    expect(ruleOf("allow", "::after")).toMatch(/inset:\s*0/);
    expect(ruleOf("allow", "::after")).toMatch(/margin:\s*auto/);
    expect(ruleOf("allow", "::after")).toMatch(/background:\s*var\(--state-allow\)/);
    expect(ruleOf("pending")).toMatch(/border:\s*1px solid var\(--state-pending-mark\)/);
    expect(ruleOf("pending")).toMatch(/background:\s*transparent/);
  });

  it("separates ALLOW from PENDING by the centred mark, not by border width", () => {
    // Without the border width and the colour, the two outlines are the same hollow ring. Only the mark differs.
    const outline = (tone: string) => shapeOf(tone).replace(/\b\d+px\b/, "").replace(/var\(--state-[a-z-]+\)/, "");
    expect(outline("allow")).toBe(outline("pending"));
    expect(markOf("allow")).not.toBe(markOf("pending"));
  });

  it("draws LOST in the unknown family with a diamond of its own, never in the deny colour or shape", () => {
    expect(ruleOf("lost")).toMatch(/var\(--state-unknown\)/);
    expect(ruleOf("lost")).not.toMatch(/--state-deny/);
    expect(ruleOf("lost")).toMatch(/clip-path:\s*polygon\(/);
    expect(shapeOf("lost")).not.toBe(shapeOf("deny"));
    expect(shapeOf("lost")).not.toBe(shapeOf("unknown"));
  });

  it("keeps the shape under forced colours by drawing borders, with system colours only", () => {
    expect(css).toMatch(/@media \(forced-colors: active\)/);
    expect(css).toMatch(/forced-color-adjust:\s*none/);
    expect(css).toMatch(/CanvasText/);
  });

  it("keeps the bullseye's centre under forced colours, as a system colour that forced colours do not adjust away", () => {
    const centre = declarations(forcedRuleOf("allow", "::after"));
    expect(centre.get("forced-color-adjust")).toBe("none");
    expect(centre.get("background-color")).toBe("CanvasText");
  });

  it("changes only colour under forced colours, so no tone's outline or mark is overridden there", () => {
    // A forced-colours override of an outline, radius, clip or size could make two shapes the normal sheet keeps
    // apart look alike. The forced block sets colour properties only.
    const colourOnly = new Set(["forced-color-adjust", "background-color", "border-color"]);
    for (const tone of ["ok", "allow", "deny", "reported", "pending", "unknown", "lost", "idle"]) {
      for (const part of ["", "::after"]) {
        for (const name of declarations(forcedRuleOf(tone, part)).keys()) {
          expect(colourOnly.has(name), `${tone}${part} sets ${name} under forced colours`).toBe(true);
        }
      }
    }
  });
});
