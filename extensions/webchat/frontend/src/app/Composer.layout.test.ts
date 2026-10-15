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

describe("the composer on a phone: one line of reason, so the pinned bar stays compact", () => {
  const phone = mediaBlockOf("max-width: 760px");

  it("collapses the reason to one line with an ellipsis, and its full text stays in the DOM for the description", () => {
    const reason = declarationsIn(phone, ".reason");
    expect(reason).toMatch(/white-space:\s*nowrap;/);
    expect(reason).toMatch(/overflow:\s*hidden;/);
    expect(reason).toMatch(/text-overflow:\s*ellipsis;/);
  });
});

describe("the composer's card does not clip its own focus rings", () => {
  it("leaves the card's overflow visible, so the 2 px outline and its offset draw in full", () => {
    expect(declarationsIn(css, ".card")).not.toMatch(/overflow:\s*hidden;/);
  });
});
