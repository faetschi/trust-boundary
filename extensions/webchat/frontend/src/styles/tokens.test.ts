// The token contract, checked on the shipped CSS:
//   1. the dark static block is the same in the OS-query form and in the attribute form;
//   2. every contrast pair of docs/design/contrast.mjs passes, on the values in tokens.css;
//   3. component CSS reads only alias and geometry tokens, never the static ramp, and never a
//      literal colour (the alias-tier rule the legacy tokens test enforced for its own sheet).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const SRC = fileURLToPath(new URL("..", import.meta.url));
const tokensCss = readFileSync(join(SRC, "styles", "tokens.css"), "utf8");

// The declarations of one rule body, as name -> value.
function declarations(body: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const match of body.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    const [, name, value] = match;
    if (name !== undefined && value !== undefined) map.set(name, value.trim());
  }
  return map;
}

function ruleBody(css: string, header: RegExp): string {
  const match = header.exec(css);
  if (!match || match[1] === undefined) throw new Error(`rule not found: ${header}`);
  return match[1];
}

// Dark is the default: its selector list names :root with no attribute, so it applies when nothing is chosen.
const darkBody = ruleBody(tokensCss, /:root,\s*:root\[data-theme="dark"\]\s*\{([^}]*)\}/);
const lightBody = ruleBody(tokensCss, /:root\[data-theme="light"\]\s*\{([^}]*)\}/);
const osLightBody = ruleBody(tokensCss, /@media \(prefers-color-scheme: light\)\s*\{\s*:root:not\(\[data-theme="dark"\]\)\s*\{([^}]*)\}/);

const LIGHT = declarations(lightBody);
const DARK = declarations(darkBody);

describe("static ramp: the default is dark, and the light block is identical in both forms", () => {
  it("has the same static names and values in the attribute and the OS query for light", () => {
    const osLight = declarations(osLightBody);
    const staticNames = [...LIGHT.keys()].filter((name) => name.startsWith("--tb-s-"));
    expect(staticNames.length).toBeGreaterThan(20);
    expect([...osLight.keys()].filter((name) => name.startsWith("--tb-s-")).sort()).toEqual(staticNames.sort());
    for (const name of staticNames) {
      expect(osLight.get(name), name).toBe(LIGHT.get(name));
    }
  });

  it("declares the same static names in light and dark", () => {
    expect([...DARK.keys()].sort()).toEqual([...LIGHT.keys()].sort());
  });
});

// ---- contrast: the maths of docs/design/contrast.mjs, ported ----
type Rgb = readonly [number, number, number];

interface Oklch {
  readonly L: number;
  readonly C: number;
  readonly h: number;
  readonly a: number;
}

function parseOklch(value: string): Oklch {
  const match = /^oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+)(%?))?\s*\)$/.exec(value);
  if (!match) throw new Error(`not an oklch value: ${value}`);
  const [, L, C, h, alpha, percent] = match;
  return {
    L: Number(L),
    C: Number(C),
    h: Number(h),
    a: alpha === undefined ? 1 : Number(alpha) / (percent === "%" ? 100 : 1),
  };
}

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));
const toEncoded = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

// Linear sRGB of an oklch colour (Ottosson), and whether a channel left the gamut.
function linearOf({ L, C, h }: Oklch): { lin: Rgb; clipped: boolean } {
  const hr = (h * Math.PI) / 180;
  const A = C * Math.cos(hr);
  const B = C * Math.sin(hr);
  const l = (L + 0.3963377774 * A + 0.2158037573 * B) ** 3;
  const m = (L - 0.1055613458 * A - 0.0638541728 * B) ** 3;
  const s = (L - 0.0894841775 * A - 1.291485548 * B) ** 3;
  const lin: Rgb = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  return { lin, clipped: lin.some((v) => v < -1e-4 || v > 1 + 1e-4) };
}

// sRGB of an oklch colour. Out-of-gamut colours lose chroma, keeping lightness and hue (the
// CSS Color 4 approach that contrast.mjs uses).
function rgbOf(colour: Oklch): Rgb {
  let current = colour;
  let attempt = linearOf(current);
  if (attempt.clipped && colour.C > 0) {
    let lo = 0;
    let hi = colour.C;
    for (let i = 0; i < 30; i++) {
      const mid = (lo + hi) / 2;
      if (linearOf({ ...colour, C: mid }).clipped) hi = mid;
      else lo = mid;
    }
    current = { ...colour, C: lo };
    attempt = linearOf(current);
  }
  const [r, g, b] = attempt.lin;
  return [encode(r), encode(g), encode(b)];
}

const encode = (v: number): number => clamp01(toEncoded(clamp01(v)));

// Composites a colour with alpha over a backdrop, in gamma-encoded sRGB, as the browser does.
function over(fg: Oklch, backdrop: Rgb): Rgb {
  const [r, g, b] = rgbOf(fg);
  const [br, bg, bb] = backdrop;
  const mix = (c: number, under: number) => c * fg.a + under * (1 - fg.a);
  return [mix(r, br), mix(g, bg), mix(b, bb)];
}

function luminance(rgb: Rgb): number {
  const lin = rgb.map((c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * (lin[0] ?? 0) + 0.7152 * (lin[1] ?? 0) + 0.0722 * (lin[2] ?? 0);
}

function ratio(a: Rgb, b: Rgb): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

// The contrast.mjs rows, expressed over the shipped token names (see tokens.css).
type Surface = "canvas" | "sidebar" | "raised" | "bubble" | "hover";
const SURF: readonly Surface[] = ["canvas", "sidebar", "raised", "bubble", "hover"];
const CHIP_SURF: readonly Surface[] = ["canvas", "raised", "bubble", "hover"];

interface Row {
  readonly label: string;
  readonly kind: "text" | "chip" | "fill" | "nontext";
  readonly fg: string;
  readonly tint?: string;
  readonly bg?: string;
  readonly on: readonly Surface[];
}

const ROWS: readonly Row[] = [
  { label: "text primary", kind: "text", fg: "ink", on: SURF },
  { label: "text secondary", kind: "text", fg: "ink-2", on: SURF },
  { label: "text tertiary", kind: "text", fg: "ink-3", on: SURF },
  { label: "chip ink ok (verified)", kind: "chip", fg: "ok-ink", tint: "ok-line", on: CHIP_SURF },
  { label: "chip ink deny", kind: "chip", fg: "deny-ink", tint: "deny-line", on: CHIP_SURF },
  { label: "chip ink allow", kind: "chip", fg: "allow", tint: "allow", on: CHIP_SURF },
  { label: "chip ink reported/unknown", kind: "chip", fg: "warn-ink", tint: "warn-line", on: CHIP_SURF },
  { label: "chip ink pending (=text2)", kind: "chip", fg: "ink-2", tint: "ink-2", on: CHIP_SURF },
  { label: "src chip user", kind: "chip", fg: "src-user", tint: "src-user", on: CHIP_SURF },
  { label: "src chip assistant", kind: "chip", fg: "src-assistant", tint: "src-assistant", on: CHIP_SURF },
  { label: "src chip tool", kind: "chip", fg: "warn-ink", tint: "warn-ink", on: CHIP_SURF },
  { label: "src chip result/message", kind: "chip", fg: "src-result", tint: "src-result", on: CHIP_SURF },
  { label: "src chip decision", kind: "chip", fg: "src-decision", tint: "src-decision", on: CHIP_SURF },
  { label: "selection text", kind: "fill", fg: "ink", bg: "selection", on: ["canvas"] },
  { label: "primary button label", kind: "fill", fg: "on-primary", bg: "primary", on: ["canvas"] },
  { label: "dot ok fill", kind: "nontext", fg: "ok-line", on: SURF },
  { label: "dot deny fill", kind: "nontext", fg: "deny-line", on: SURF },
  { label: "dot allow ring", kind: "nontext", fg: "allow", on: SURF },
  { label: "dot allow bullseye centre", kind: "nontext", fg: "allow", on: SURF },
  { label: "pending hollow ring", kind: "nontext", fg: "pending", on: SURF },
  { label: "reported dashed border", kind: "nontext", fg: "warn-line", on: SURF },
  { label: "unknown dotted border", kind: "nontext", fg: "warn-line", on: SURF },
  // LOST is in the unknown family: a chip with the unknown ink and tint (double border), and a
  // solid diamond on the unknown line. Neither uses the deny pair.
  { label: "chip ink lost (unknown family, double border)", kind: "chip", fg: "warn-ink", tint: "warn-line", on: CHIP_SURF },
  { label: "dot lost diamond fill", kind: "nontext", fg: "warn-line", on: SURF },
  { label: "idle filled dot", kind: "nontext", fg: "idle", on: SURF },
  { label: "focus ring", kind: "nontext", fg: "focus", on: SURF },
  { label: "control boundary (input)", kind: "nontext", fg: "edge", on: ["canvas", "sidebar", "raised"] },
];

// The static names the rows read, for each theme. A row's surface is the Tier 1 name.
function staticOf(theme: Map<string, string>, name: string): Oklch {
  const value = theme.get(`--tb-s-${name}`);
  if (value === undefined) throw new Error(`no static token --tb-s-${name}`);
  return parseOklch(value);
}

function worstRatio(theme: Map<string, string>, row: Row): number {
  let worst = Infinity;
  for (const surface of row.on) {
    const surf = rgbOf(staticOf(theme, surface));
    let fg: Rgb;
    let bg: Rgb;
    if (row.kind === "chip" && row.tint) {
      bg = over({ ...staticOf(theme, row.tint), a: 0.12 }, surf);
      fg = over(staticOf(theme, row.fg), bg);
    } else if (row.kind === "fill" && row.bg) {
      bg = rgbOf(staticOf(theme, row.bg));
      fg = over(staticOf(theme, row.fg), bg);
    } else {
      bg = surf;
      fg = over(staticOf(theme, row.fg), surf);
    }
    worst = Math.min(worst, ratio(fg, bg));
  }
  return worst;
}

describe("contrast: every contrast.mjs pair passes on the shipped values", () => {
  for (const [theme, palette] of [
    ["light", LIGHT],
    ["dark", DARK],
  ] as const) {
    for (const row of ROWS) {
      const need = row.kind === "nontext" ? 3 : 4.5;
      it(`${theme}: ${row.label} >= ${need}:1`, () => {
        expect(worstRatio(palette, row)).toBeGreaterThanOrEqual(need);
      });
    }
  }
});

// ---- the alias tier: component CSS reads aliases and geometry, never the ramp ----

// The Tier 2 names: declared between the Tier 2 and Tier 3 markers.
const tier2Block = tokensCss.slice(tokensCss.indexOf("── Tier 2"), tokensCss.indexOf("── Tier 3"));
const TIER2 = new Set([...tier2Block.matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1] ?? ""));
const TIER3 = new Set(
  [...tokensCss.slice(tokensCss.indexOf("── Tier 3")).matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1] ?? ""),
);

function listStyleSheets(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) out.push(...listStyleSheets(path));
    else if (entry.endsWith(".css") && entry !== "tokens.css") out.push(path);
  }
  return out;
}

const componentSheets = listStyleSheets(SRC);
const COLOUR_PROPERTY = /^(color|background|background-color|border|border-color|border-[a-z]+-color|outline|outline-color|box-shadow|fill|stroke|caret-color|accent-color)$/;
const LITERAL_COLOUR = /#[0-9a-fA-F]{3,8}\b|\b(rgba?|hsla?|oklch|oklab|lab|lch|hwb|color)\(/;

describe("alias tier: component and base CSS", () => {
  it("found the component stylesheets it must check", () => {
    expect(componentSheets.length).toBeGreaterThanOrEqual(10);
  });

  for (const path of componentSheets) {
    const name = relative(SRC, path).replace(/\\/g, "/");
    const css = readFileSync(path, "utf8");

    it(`${name} reads no static ramp token and no literal colour`, () => {
      expect(css).not.toMatch(/--tb-s-/);
      expect(css).not.toMatch(LITERAL_COLOUR);
    });

    it(`${name} declares every var() it reads`, () => {
      const local = new Set([...css.matchAll(/(--local-[a-z-]+)\s*:/g)].map((m) => m[1] ?? ""));
      for (const match of css.matchAll(/var\((--[a-z0-9-]+)/g)) {
        const token = match[1] ?? "";
        const known = TIER2.has(token) || TIER3.has(token) || local.has(token);
        expect(known, `${name} reads ${token}`).toBe(true);
      }
    });

    it(`${name} takes colour only from alias tokens`, () => {
      const local = new Set([...css.matchAll(/(--local-[a-z-]+)\s*:/g)].map((m) => m[1] ?? ""));
      for (const declaration of css.matchAll(/([a-z-]+)\s*:\s*([^;]+);/g)) {
        const property = declaration[1] ?? "";
        const value = declaration[2] ?? "";
        if (!COLOUR_PROPERTY.test(property) && !property.startsWith("--local-")) continue;
        for (const match of value.matchAll(/var\((--[a-z0-9-]+)/g)) {
          const token = match[1] ?? "";
          expect(TIER2.has(token) || local.has(token), `${name}: ${property} reads ${token}`).toBe(true);
        }
      }
    });
  }
});

describe("tokens.css and base.css: the static contract", () => {
  it("has no @import, no url(), no webfont in its rules (comments are not rules)", () => {
    const rules = tokensCss.replace(/\/\*[\s\S]*?\*\//g, "");
    expect(rules).not.toMatch(/@import/);
    expect(rules).not.toMatch(/url\(/);
    expect(rules).not.toMatch(/@font-face/);
  });

  it("keeps the focus ring and the reduced-motion kill switch in the base sheet", () => {
    const base = readFileSync(join(SRC, "styles", "base.css"), "utf8");
    expect(base).toMatch(/:focus-visible\s*\{[^}]*var\(--ring\)/);
    expect(base).toMatch(/@media \(prefers-reduced-motion: reduce\)/);
    expect(base).toMatch(/\[hidden\]\s*\{\s*display:\s*none\s*!important/);
  });
});
