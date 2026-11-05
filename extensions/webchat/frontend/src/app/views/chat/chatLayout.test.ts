// The chat's layout rules, read from Chat.module.css (and, for the page's dock clearance, base.css and tokens.css). Geometry is
// not measurable in happy-dom, so these pin the rules the browser depends on: the transcript scrolls, the tool card header is
// a row, the message meta is never hidden at rest, and the page keeps the dock's clearance under it on a phone.
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function stripped(path: string): string {
  return readFileSync(resolve(process.cwd(), path), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

const css = stripped("src/app/views/chat/Chat.module.css");
// The tool card is a Trace (ui/Trace.module.css); Copy and the record label are Buttons (ui/Button.module.css).
const traceCss = stripped("src/ui/Trace.module.css");
const buttonCss = stripped("src/ui/Button.module.css");

// The declarations of the first rule, in `fragment`, whose selector list starts with `selector`, or "" when there is none.
function declarationsIn(fragment: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[\\s},])${escaped}\\s*\\{([^}]*)\\}`).exec(fragment)?.[1] ?? "";
}

function declarationsOf(selector: string): string {
  return declarationsIn(css, selector);
}

// The rules inside the first `@media (<query>)` block of `source`, or "" when there is none.
function mediaBlockOf(query: string, source: string = css): string {
  const start = source.indexOf(`@media (${query})`);
  if (start < 0) return "";
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") depth++;
    if (source[i] === "}" && --depth === 0) return source.slice(open + 1, i);
  }
  return "";
}

describe("the transcript is the scroller at wide widths", () => {
  it("keeps each turn at its natural height, so the log scrolls rather than the turn shrinking to fit", () => {
    expect(declarationsOf(".turn")).toMatch(/flex:\s*none;|flex-shrink:\s*0;/);
  });

  it("makes the log the scroll container, and lets it shrink inside the column", () => {
    const log = declarationsOf(".log");
    expect(log).toMatch(/overflow-y:\s*auto;/);
    expect(log).toMatch(/min-height:\s*0;/);
  });

  it("keeps the transcript region the containing block of its absolutely positioned labels, so they cannot widen the page", () => {
    // The visually hidden labels in the transcript are absolutely positioned. Their overflow stays inside this region only
    // while it is positioned; without it they overflow the column's scroll box and scroll the whole page.
    expect(declarationsOf(".logWrap")).toMatch(/position:\s*relative;/);
  });

  it("makes the log the containing block of the labels inside it, so their overflow scrolls in the log and never widens the workspace", () => {
    // The labels sit inside the log, so the wrapper is not their containing block: a positioned wrapper that does not clip
    // lets them overflow the workspace's scroll box. Round 3 (check K at 844 by 390) measured the workspace's scroll range
    // growing by 205 px for that reason. The log, a scroll container, is the containing block, so they scroll with it.
    expect(declarationsOf(".log")).toMatch(/position:\s*relative;/);
  });
});

describe("the tool card header is a row of parts with gaps", () => {
  it("lays the glyph, name, summary, record label and chips out as a wrapping flex row", () => {
    const head = declarationsIn(traceCss, ".lineContent");
    expect(head).toMatch(/display:\s*flex;/);
    expect(head).toMatch(/gap:/);
    expect(head).toMatch(/min-width:\s*0;/);
  });

  it("ends the summary in an ellipsis rather than running into the record label", () => {
    const summary = declarationsIn(traceCss, ".title");
    expect(summary).toMatch(/overflow:\s*hidden;/);
    expect(summary).toMatch(/text-overflow:\s*ellipsis;/);
    expect(summary).toMatch(/white-space:\s*nowrap;/);
  });
});

describe("the message meta is visible at rest on every device", () => {
  it("is never hidden with opacity or visibility, except the Copy icon and the element count, which fade only where a pointer can hover", () => {
    const hoverBlocks = [...css.matchAll(/@media \(hover: hover\) \{([\s\S]*?)\n\}\n/g)].map((match) => match[1] ?? "");
    const withoutHoverOnly = css.replace(/@media \(hover: hover\) \{[\s\S]*?\n\}\n/g, "");
    expect(withoutHoverOnly).not.toMatch(/opacity:\s*0\b/);
    expect(withoutHoverOnly).not.toMatch(/visibility:\s*hidden/);
    // Inside the hover-only blocks, exactly these two selectors may be faded; nothing else may be.
    const faded = hoverBlocks.flatMap((block) => [...block.matchAll(/([^{}]+)\{([^}]*opacity:\s*0\b[^}]*)\}/g)].map((rule) => (rule[1] ?? "").trim()));
    expect(faded.sort()).toEqual([".meta button", ".turnCount"]);
  });
  it("draws the time at full emphasis, not in the muted ink", () => {
    expect(declarationsOf(".time")).toMatch(/color:\s*var\(--foreground\);/);
  });

  it("keeps Copy and the record label readable in the muted ink", () => {
    expect(declarationsIn(buttonCss, '.button[data-variant="ghost"]')).toMatch(/color:\s*var\(--muted-foreground\);/);
  });
});

describe("the transcript reads as a flow of quiet parts, not boxes", () => {
  it("draws no box around a turn: no border and no ground", () => {
    const turn = declarationsOf(".turn");
    expect(turn).not.toMatch(/\bborder\s*:/);
    expect(turn).not.toMatch(/\bbackground\s*:/);
  });

  it("right-aligns the user bubble, capped at 80% of the message column", () => {
    expect(declarationsOf('.msg[data-role="user"] .msgText')).toMatch(/max-width:\s*80%;/);
  });

  it("gives a refused trace the destructive tint, in its rule and its ground", () => {
    const refused = declarationsIn(traceCss, '.trace[data-tone="deny"]');
    expect(refused).toMatch(/var\(--state-deny\)/);
    expect(refused).toMatch(/var\(--destructive\)/);
  });
});

describe("the composer dock: pinned on a phone, and the jump sits above it", () => {
  const phone = mediaBlockOf("max-width: 760px");

  it("is a positioned box at every width, so the jump can sit on it", () => {
    expect(declarationsOf(".dock")).toMatch(/position:\s*relative;/);
  });

  it("sticks to the bottom of the viewport on a phone, on an opaque ground, over the transcript and under the drawer's scrim", () => {
    const dock = declarationsIn(phone, ".dock");
    expect(dock).toMatch(/position:\s*sticky;/);
    expect(dock).toMatch(/bottom:\s*0;/);
    expect(dock).toMatch(/background:\s*var\(--background\);/);
    expect(dock).toMatch(/z-index:\s*var\(--z-sticky\);/);
  });

  it("draws the jump over the foot of the transcript, above the dock, so the composer never covers it", () => {
    const jump = declarationsOf(".jump");
    expect(jump).toMatch(/position:\s*absolute;/);
    expect(jump).toMatch(/bottom:\s*calc\(100% \+ var\(--space-2\) \+ var\(--space-3\)\);/);
  });

  it("keeps the jump on one line on a phone, where its half-width containing block would otherwise wrap its label", () => {
    expect(declarationsIn(buttonCss, ".button")).toMatch(/white-space:\s*nowrap;/);
  });

  it("does not make the jump sticky on a phone: it rides with the dock", () => {
    expect(declarationsIn(phone, ".jump")).toBe("");
  });
});

describe("the page reserves the dock's clearance on a phone, so keyboard focus never lands under the dock", () => {
  const base = readFileSync(resolve(process.cwd(), "src/styles/base.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const tokens = readFileSync(resolve(process.cwd(), "src/styles/tokens.css"), "utf8");

  it("scrolls the page's viewport to the clearance at the bottom, at phone widths only", () => {
    // The page is the scroller on a phone. Focus and anchor scrolls stop above the dock because the root reserves its clearance.
    const phone = mediaBlockOf("max-width: 760px", base);
    expect(phone).toMatch(/html\s*\{\s*scroll-padding-bottom:\s*var\(--dock-clearance\);\s*\}/);
    expect(base.replace(phone, "")).not.toMatch(/scroll-padding/);
  });

  it("declares the clearance in the theme-free geometry tier as the dock's tallest measured state plus one gap", () => {
    // 268 px is the dock with its status line shown, its reason and placeholder wrapped and its chips wrapped: the tallest state
    // from 200 px up (250 at 240, 232 at 280, 214 at 320 to 360, 196 at 390, 172 at 412). The status line appears on the first
    // focus of the composer, and the dock grows upward when it does, so the gap keeps the last stop above the grown dock.
    const geometry = tokens.slice(tokens.indexOf("── Tier 3"));
    expect(geometry).toMatch(/--dock-clearance:\s*calc\(268px \+ var\(--space-2\)\);/);
  });
});
