// @vitest-environment happy-dom
// The React mapping of the AST (react.tsx). It must render every node, keep hostile payloads
// inert, skip blocks whose node objects did not change, and serialise to the same HTML as the
// legacy AST serializer (toHtmlString.ts) on the golden, hostile and seeded corpora.

import { act } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Block } from "./ast";
import { createIncrementalParser } from "./incremental";
import { parseMarkdown, safeHref } from "./parse";
import { Markdown, renderBlocks } from "./react";
import { HOSTILE, ORDINARY, randomMarkdown, seededRandom } from "./test-support/corpus";
import { blocksToHtml } from "./toHtmlString";
import { mount, type Mounted } from "../test-support/dom";

const LINK = safeHref("https://example.com/docs");
if (LINK === null) throw new Error("the fixture link must be a SafeHref");

// Hostile payloads beyond the legacy corpus: each targets one sink (markup, handler, URL scheme).
const EXTRA_HOSTILE: readonly (readonly [string, string])[] = [
  ["raw img with a handler", "<img src=x onerror=alert(1)>"],
  ["raw iframe with srcdoc", '<iframe srcdoc="<script>alert(1)</script>"></iframe>'],
  ["raw style block", "<style>body{display:none}</style>"],
  ["raw svg with onload", "<svg onload=alert(1)>"],
  ["raw anchor with javascript href", '<a href="javascript:alert(1)">x</a>'],
  ["javascript link", "[x](javascript:alert(1))"],
  ["mixed-case javascript link", "[x](JaVaScRiPt:alert(1))"],
  ["leading space javascript link", "[x]( javascript:alert(1))"],
  ["tab and newline inside a javascript link", "[x](\tjava\nscript:alert(1))"],
  ["data link", "[x](data:text/html,<script>alert(1)</script>)"],
  ["vbscript link", "[x](vbscript:msgbox(1))"],
  ["protocol-relative link", "[x](//evil.example/)"],
  ["image inside a link", "[![i](https://e.example/i.png)](https://e.example)"],
  ["script in a heading", "# <script>alert(1)</script>"],
  ["javascript link in a list", "- [x](javascript:alert(1))"],
  ["handler in a blockquote", "> <img src=x onerror=1>"],
  ["script inside a code fence", "```html\n<script>alert(1)</script>\n```"],
];

const HOSTILE_ALL: readonly (readonly [string, string])[] = [...HOSTILE, ...EXTRA_HOSTILE];

const ALLOWED_TAGS = new Set(["p", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "a"]);

// The DOM of a rendered fragment, parsed from markup or taken from a mounted container.
function hostOf(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  return host;
}

// Attributes in name order, so two serialisations of the same tree compare equal.
function sortAttributes(element: Element): void {
  const attributes = Array.from(element.attributes).map((attribute) => [attribute.name, attribute.value] as const);
  for (const [name] of attributes) element.removeAttribute(name);
  attributes.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  for (const [name, value] of attributes) element.setAttribute(name, value);
}

function normalised(html: string): string {
  const host = hostOf(html);
  for (const element of Array.from(host.querySelectorAll("*"))) sortAttributes(element);
  return host.innerHTML;
}

function serverMarkup(blocks: readonly Block[]): string {
  return renderToStaticMarkup(<>{renderBlocks(blocks)}</>);
}

// Every element the markup creates must be on the allowlist, carry no handler or style attribute,
// and every link must be an http, https or mailto link with the hardened rel.
function expectInert(container: ParentNode): void {
  for (const element of Array.from(container.querySelectorAll("*"))) {
    expect(ALLOWED_TAGS.has(element.tagName.toLowerCase()), `tag ${element.tagName}`).toBe(true);
    for (const attribute of Array.from(element.attributes)) {
      expect(attribute.name.startsWith("on"), `handler ${attribute.name}`).toBe(false);
      expect(attribute.name, "style attribute").not.toBe("style");
    }
    if (element.tagName === "A") {
      expect(element.getAttribute("href") ?? "", "link target").toMatch(/^(https?:|mailto:)/);
      expect(element.getAttribute("rel")).toBe("noopener noreferrer nofollow");
      expect(element.getAttribute("target")).toBe("_blank");
    }
  }
}

let view: Mounted | null = null;

function render(blocks: readonly Block[]): HTMLElement {
  view?.unmount();
  view = mount(<Markdown blocks={blocks} />);
  return view.container;
}

describe("every AST node renders to its element", () => {
  const everyNode: Block[] = [
    { type: "heading", level: 3, children: [{ type: "text", value: "Heading three" }] },
    { type: "heading", level: 4, children: [{ type: "text", value: "Heading four" }] },
    { type: "heading", level: 5, children: [{ type: "text", value: "Heading five" }] },
    { type: "heading", level: 6, children: [{ type: "text", value: "Heading six" }] },
    {
      type: "paragraph",
      children: [
        { type: "text", value: "a " },
        { type: "em", children: [{ type: "text", value: "b" }] },
        { type: "strong", children: [{ type: "text", value: "c" }] },
        { type: "text", value: " " },
        { type: "code", value: "d" },
        { type: "text", value: " " },
        { type: "link", href: LINK, children: [{ type: "text", value: "e" }] },
      ],
    },
    {
      type: "list",
      ordered: true,
      start: 3,
      items: [
        { layout: "inline", children: [{ type: "text", value: "one" }] },
        { layout: "blocks", children: [{ type: "paragraph", children: [{ type: "text", value: "two" }] }] },
      ],
    },
    { type: "list", ordered: false, start: 1, items: [{ layout: "inline", children: [{ type: "text", value: "bullet" }] }] },
    { type: "blockquote", children: [{ type: "paragraph", children: [{ type: "text", value: "quoted" }] }] },
    { type: "code", language: "ts", text: "const a = 1;" },
    { type: "code", language: null, text: "plain" },
    { type: "notice", kind: "truncated", text: "Message truncated for display: the first 10 of 20 characters are shown." },
  ];

  it("maps each node to the element the legacy serializer writes", () => {
    const container = render(everyNode);
    expect(container.querySelectorAll("h3, h4, h5, h6").length).toBe(4);
    expect(container.querySelector("h3")?.textContent).toBe("Heading three");
    expect(container.querySelector("em")?.textContent).toBe("b");
    expect(container.querySelector("strong")?.textContent).toBe("c");
    expect(container.querySelector("p > code")?.textContent).toBe("d");
    expect(container.querySelector("a")?.getAttribute("href")).toBe(LINK);
    const ordered = container.querySelector("ol");
    expect(ordered?.getAttribute("start")).toBe("3");
    expect(ordered?.querySelectorAll("li").length).toBe(2);
    expect(container.querySelector("ul")?.hasAttribute("start")).toBe(false);
    expect(container.querySelector("blockquote p")?.textContent).toBe("quoted");
  });

  it("keeps code scrollable and keyboard-reachable, with a language class when one is named", () => {
    const container = render(everyNode);
    const regions = container.querySelectorAll("pre");
    expect(regions.length).toBe(2);
    const named = regions[0];
    expect(named?.getAttribute("role")).toBe("region");
    expect(named?.getAttribute("tabindex")).toBe("0");
    expect(named?.getAttribute("aria-label")).toBe("Code block, ts");
    expect(named?.querySelector("code")?.className).toBe("language-ts");
    expect(regions[1]?.getAttribute("aria-label")).toBe("Code block");
    expect(regions[1]?.querySelector("code")?.hasAttribute("class")).toBe(false);
  });

  it("shows the truncation notice as visible text", () => {
    const container = render(everyNode);
    const notice = container.querySelector("p.md-truncated");
    expect(notice?.textContent).toBe("Message truncated for display: the first 10 of 20 characters are shown.");
  });

  it("maps a link text-only, so a link never contains an element", () => {
    const container = render(everyNode);
    expect(container.querySelector("a")?.children.length).toBe(0);
  });

  it("writes headings at their demoted level, never a level-1 or level-2 heading", () => {
    const html = serverMarkup(everyNode);
    expect(html).not.toMatch(/<h[12][\s>]/);
  });
});

describe("hostile payloads stay inert", () => {
  for (const [name, payload] of HOSTILE_ALL) {
    it(`renders as inert DOM: ${name}`, () => {
      const container = render(parseMarkdown(payload).blocks);
      expectInert(container);
    });

    it(`renders as inert server markup: ${name}`, () => {
      const html = serverMarkup(parseMarkdown(payload).blocks);
      expectInert(hostOf(html));
      expect(html).not.toMatch(/<(script|img|iframe|style|svg|object|embed|form|input|link|meta|base)[\s/>]/i);
    });
  }

  it("never produces a javascript: href from any payload", () => {
    for (const [, payload] of HOSTILE_ALL) {
      const html = serverMarkup(parseMarkdown(payload).blocks);
      expect(html).not.toMatch(/href="\s*(javascript|vbscript|data):/i);
    }
  });
});

describe("unchanged blocks are not rendered again", () => {
  it("skips leading blocks whose node objects are unchanged, and renders the changed one", () => {
    const counts = new Map<Block, number>();
    const onBlockRender = (block: Block) => counts.set(block, (counts.get(block) ?? 0) + 1);
    const parser = createIncrementalParser();

    const first = parser.update("# Plan\n\nFirst paragraph.\n\nSecond");
    const shown = mount(<Markdown blocks={first.blocks} onBlockRender={onBlockRender} />);
    const heading = shown.container.querySelector("h3");

    const second = parser.update("# Plan\n\nFirst paragraph.\n\nSecond paragraph grows.");
    expect(second.reused).toBe(2);
    act(() => {
      shown.root.render(<Markdown blocks={second.blocks} onBlockRender={onBlockRender} />);
    });

    expect(counts.get(first.blocks[0] as Block)).toBe(1);
    expect(counts.get(first.blocks[1] as Block)).toBe(1);
    expect(counts.get(second.blocks[2] as Block)).toBe(1);
    expect(shown.container.querySelector("h3")).toBe(heading);
    expect(shown.container.textContent).toContain("Second paragraph grows.");
    shown.unmount();
    view = null;
  });

  it("re-renders every block when the text does not extend the previous text", () => {
    const counts = new Map<Block, number>();
    const onBlockRender = (block: Block) => counts.set(block, (counts.get(block) ?? 0) + 1);
    const parser = createIncrementalParser();
    const first = parser.update("alpha");
    const shown = mount(<Markdown blocks={first.blocks} onBlockRender={onBlockRender} />);
    const second = parser.update("beta");
    expect(second.reused).toBe(0);
    act(() => {
      shown.root.render(<Markdown blocks={second.blocks} onBlockRender={onBlockRender} />);
    });
    expect(counts.get(second.blocks[0] as Block)).toBe(1);
    shown.unmount();
  });
});

describe("parity with the legacy AST serializer (toHtmlString.ts)", () => {
  const random = seededRandom(7);
  const seeded = Array.from({ length: 150 }, () => randomMarkdown(random, 40));

  const corpus: readonly (readonly [string, string])[] = [
    ...ORDINARY.map(([name, text]) => [`golden ${name}`, text] as const),
    ...HOSTILE_ALL.map(([name, text]) => [`hostile ${name}`, text] as const),
    ...seeded.map((text, index) => [`seeded ${index}`, text] as const),
  ];

  it("serialises to the same normalised HTML on the golden, hostile and seeded corpora", () => {
    expect(corpus.length).toBeGreaterThan(200);
    for (const [name, text] of corpus) {
      const blocks = parseMarkdown(text).blocks;
      const expected = normalised(blocksToHtml(blocks));
      const actual = normalised(serverMarkup(blocks));
      expect(actual, name).toBe(expected);
    }
  });

  it("the mounted DOM matches the same serializer on the golden corpus", () => {
    for (const [name, text] of ORDINARY) {
      const blocks = parseMarkdown(text).blocks;
      const container = render(blocks);
      const actual = normalised(container.innerHTML);
      expect(actual, name).toBe(normalised(blocksToHtml(blocks)));
    }
    view?.unmount();
    view = null;
  });
});
