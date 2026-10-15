// The composer's phone layout, read from Composer.module.css. Geometry is not measurable in happy-dom, so these pin the rules
// the browser depends on: the reason line takes one line on a phone, and the card never clips a focus ring.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const css = readFileSync(resolve(process.cwd(), "src/app/Composer.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// The declarations of the first rule for `selector` in `fragment`, or "" when there is none.
function declarationsIn(fragment: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(fragment)?.[1] ?? "";
}

// The rules inside the first `@media (<query>)` block, or "" when there is none.
function mediaBlockOf(query: string): string {
  const start = css.indexOf(`@media (${query})`);
  if (start < 0) return "";
  const open = css.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  return "";
}

describe("the composer's reason and placeholder read in full on a phone: they wrap, and the dock grows to hold them", () => {
  const phone = mediaBlockOf("max-width: 760px");

  it("never clips the reason: no nowrap, no ellipsis and no hidden overflow, at any width", () => {
    // Every rule that styles the reason, at any width. It is the explanation of why submitting is unavailable, so it reads in full.
    const reasonRules = [...css.matchAll(/([^{}]*\.reason[^{}]*)\{([^}]*)\}/g)].map((m) => m[2]);
    expect(reasonRules.length).toBeGreaterThan(0);
    for (const decls of reasonRules) {
      expect(decls).not.toMatch(/white-space:\s*nowrap;/);
      expect(decls).not.toMatch(/text-overflow:\s*ellipsis;/);
      expect(decls).not.toMatch(/overflow:\s*hidden;/);
    }
  });

  it("makes the field two lines tall on a phone, so the placeholder's wrapped second line is not cut off", () => {
    // The textarea has one row (Composer.tsx), so its height comes from here: two lines of body text plus the field's padding.
    expect(declarationsIn(phone, ".input")).toMatch(/min-height:\s*calc\(2 \* var\(--lh-body\) \+ 2 \* var\(--space-1\)\);/);
  });
});

describe("the composer's card does not clip its own focus rings", () => {
  it("leaves the card's overflow visible, so the 2 px outline and its offset draw in full", () => {
    // The rule must exist first: a missing rule has no declarations, and so would pass the clip check below.
    expect(css).toMatch(/(?:^|[\s},])\.card\s*\{[^}]*\}/);
    expect(declarationsIn(css, ".card")).not.toMatch(/overflow(?:-[xy])?:\s*(?:hidden|clip);/);
  });
});
