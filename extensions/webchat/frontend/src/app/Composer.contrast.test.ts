// The composer's contrast rules, read from its stylesheet. The colours are alias tokens, and their contrast is checked against the
// shipped values in tokens.test.ts; this file pins the two rules that set them: the placeholder's token, and the send glyph's
// opacity. The ratios in the comments were computed from the token values in tokens.css (light and dark).
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(resolve(process.cwd(), "src/app/Composer.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The declarations of the first rule for `selector`, or "" when the sheet has none.
function declarationsOf(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";
}

describe("composer contrast: the placeholder and the send glyph", () => {
  it("sets the placeholder to the muted text token, at full opacity, in both themes (8.5:1 light, 9.3:1 dark)", () => {
    const placeholder = declarationsOf(".input::placeholder");
    expect(placeholder).toMatch(/color:\s*var\(--muted-foreground\);/);
    expect(placeholder).toMatch(/opacity:\s*1;/);
  });

  it("keeps the send glyph at 70% opacity or more: 3.6:1 on the light fill, 5.0:1 on the dark one", () => {
    const opacity = Number(/opacity:\s*([\d.]+);/.exec(declarationsOf(".send > span"))?.[1]);
    expect(opacity).toBeGreaterThanOrEqual(0.7);
    expect(opacity).toBeLessThan(1);
  });

  it("keeps the disabled send affordance a dashed outline and a not-allowed cursor", () => {
    const send = declarationsOf(".send");
    expect(send).toMatch(/border:\s*1px dashed var\(--border\);/);
    expect(send).toMatch(/cursor:\s*not-allowed;/);
  });
});
