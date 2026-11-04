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

describe("composer contrast: the placeholder, and a card that does not read as a field", () => {
  it("sets the placeholder to the muted text token, at full opacity, in both themes (8.5:1 light, 9.3:1 dark)", () => {
    const placeholder = declarationsOf(".input::placeholder");
    expect(placeholder).toMatch(/color:\s*var\(--muted-foreground\);/);
    expect(placeholder).toMatch(/opacity:\s*1;/);
  });

  it("draws the card dashed, and the field with a not-allowed cursor, so the notice does not read as a working field", () => {
    expect(declarationsOf(".card")).toMatch(/border:\s*1px dashed var\(--input\);/);
    expect(declarationsOf(".input")).toMatch(/cursor:\s*not-allowed;/);
  });
});
