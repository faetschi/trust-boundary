// Assertions that read DOM structure now read the AST. The attribute-level
// assertions moved to render.test.ts, the DOM mapping contract.

import { describe, expect, it } from "vitest";
import type { Block, LinkInline } from "./ast";
import { parseMarkdown, safeHref, splitBlocks } from "./parse";
import { astText, walk } from "./test-support/render";

function blocksOf(text: string): readonly Block[] {
  return parseMarkdown(text).blocks;
}

function linksOf(blocks: readonly Block[]): LinkInline[] {
  return walk(blocks).filter((node): node is LinkInline => "type" in node && node.type === "link");
}

describe("links", () => {
  it("safeHref allows only http, https and mailto, after cleaning", () => {
    expect(safeHref("https://example.com/a")).toBe("https://example.com/a");
    expect(safeHref("HTTP://Example.com")).toBe("http://example.com/");
    expect(safeHref("  https://example.com  ")).toBe("https://example.com/");
    expect(safeHref("mailto:team@example.com")).toBe("mailto:team@example.com");
    for (const bad of [
      "javascript:alert(1)",
      "JaVaScRiPt:alert(1)",
      " java\tscript:alert(1)",
      "java\nscript:x",
      "data:text/html,x",
      "vbscript:x",
      "file:///etc/passwd",
      "ftp://example.com",
      "//evil.example",
      "/relative",
      "relative/path",
      "https://a b",
      "",
    ]) {
      expect(safeHref(bad), JSON.stringify(bad)).toBeNull();
    }
  });

  it("an allowed link is an anchor with the hardened attributes", () => {
    // The AST side: two links, each with its normalised href and its label as text.
    // target and rel are checked on the DOM mapping in render.test.ts.
    const links = linksOf(blocksOf("[docs](https://example.com/docs) and [mail](mailto:a@example.com)"));
    expect(links).toStrictEqual([
      { type: "link", href: "https://example.com/docs", children: [{ type: "text", value: "docs" }] },
      { type: "link", href: "mailto:a@example.com", children: [{ type: "text", value: "mail" }] },
    ]);
  });

  it("an image is literal source text, never an image element", () => {
    const source = "![diagram](https://example.com/d.png)";
    const blocks = blocksOf(source);
    expect(linksOf(blocks)).toHaveLength(0);
    expect(blocks).toMatchObject([{ type: "paragraph" }]);
    expect(astText(blocks)).toBe(source);
  });

  it("an unsafe link is its source text, and the text before it is a separate text node", () => {
    // The legacy renderer flushes text before any link, safe or not, so an unsafe
    // link leaves two adjacent text nodes. The AST keeps that boundary.
    expect(blocksOf("a [x](javascript:1) b")).toMatchObject([
      { type: "paragraph", children: [{ type: "text", value: "a " }, { type: "text", value: "[x](javascript:1) b" }] },
    ]);
  });

  it("a link label is literal text, not parsed for emphasis", () => {
    expect(blocksOf("[a *b* c](https://example.com/)")).toMatchObject([
      { type: "paragraph", children: [{ type: "link", children: [{ type: "text", value: "a *b* c" }] }] },
    ]);
  });
});

describe("blocks and fences", () => {
  it("splitBlocks divides at blank lines, headings and fences", () => {
    expect(splitBlocks("a\n\nb\n# H\nc")).toEqual(["a", "b", "# H", "c"]);
    expect(splitBlocks("```\nx\n```\ny")).toEqual(["```\nx\n```", "y"]);
    expect(splitBlocks("")).toEqual([]);
  });

  it("a blank line inside an unclosed fence does not end the block", () => {
    expect(splitBlocks("```js\nlet a = 1;\n\nlet b")).toEqual(["```js\nlet a = 1;\n\nlet b"]);
  });

  it("a fence closes only on a fence of the same marker and at least the same length", () => {
    expect(splitBlocks("````\n```\nstill code\n````\nafter")).toEqual(["````\n```\nstill code\n````", "after"]);
    expect(splitBlocks("~~~\n```\n~~~")).toEqual(["~~~\n```\n~~~"]);
  });

  it("a fenced block keeps its blank lines and its language", () => {
    const blocks = blocksOf("```js\nconst a = 1;\n\nconst b = 2;\n```\n\nafter");
    expect(blocks).toStrictEqual([
      { type: "code", language: "js", text: "const a = 1;\n\nconst b = 2;" },
      { type: "paragraph", children: [{ type: "text", value: "after" }] },
    ]);
  });

  it("an unclosed fence streams as a code block with the content so far", () => {
    expect(blocksOf("```py\nprint(1)\n\nprint(2)")).toStrictEqual([
      { type: "code", language: "py", text: "print(1)\n\nprint(2)" },
    ]);
  });

  it("markdown and markup inside a fence stay literal", () => {
    const source = "```\n**not bold** [x](https://a.example) <b>tag</b>\n```";
    const blocks = blocksOf(source);
    expect(blocks).toStrictEqual([{ type: "code", language: null, text: "**not bold** [x](https://a.example) <b>tag</b>" }]);
    expect(linksOf(blocks)).toHaveLength(0);
  });

  it("headings render at h3 and deeper, never as page-level h1", () => {
    expect(blocksOf("# One")).toStrictEqual([{ type: "heading", level: 3, children: [{ type: "text", value: "One" }] }]);
    expect(blocksOf("## Two")).toStrictEqual([{ type: "heading", level: 4, children: [{ type: "text", value: "Two" }] }]);
    expect(blocksOf("#### Four")).toMatchObject([{ type: "heading", level: 6 }]);
    expect(blocksOf("###### Six")).toMatchObject([{ type: "heading", level: 6 }]);
    expect(blocksOf("#hashtag"), "no space after the hash is not a heading").toMatchObject([{ type: "paragraph" }]);
  });
});

describe("inline and lists", () => {
  it("inline emphasis and code spans", () => {
    const blocks = blocksOf("Use **bold**, *em*, `a*b*c`, and ~~not strike~~.");
    expect(blocks).toMatchObject([
      {
        type: "paragraph",
        children: [
          { type: "text", value: "Use " },
          { type: "strong", children: [{ type: "text", value: "bold" }] },
          { type: "text", value: ", " },
          { type: "em", children: [{ type: "text", value: "em" }] },
          { type: "text", value: ", " },
          { type: "code", value: "a*b*c" },
          { type: "text", value: ", and ~~not strike~~." },
        ],
      },
    ]);
    expect(astText(blocks)).toBe("Use bold, em, a*b*c, and ~~not strike~~.");
  });

  it("a code span drops one padding space from each side, as CommonMark does", () => {
    expect(astText(blocksOf("`` `x` ``"))).toBe("`x`");
    expect(astText(blocksOf("`  `")), "an all-space span keeps its spaces").toBe("  ");
  });

  it("intraword underscores and escaped delimiters are literal", () => {
    expect(JSON.stringify(blocksOf("snake_case_name stays"))).not.toContain('"em"');
    const escaped = blocksOf("\\*literal\\*");
    expect(JSON.stringify(escaped)).not.toContain('"em"');
    expect(astText(escaped)).toBe("*literal*");
  });

  it("bulleted and ordered lists, with the ordered start preserved", () => {
    expect(blocksOf("- one\n- two")).toStrictEqual([
      {
        type: "list",
        ordered: false,
        start: 1,
        items: [
          { layout: "inline", children: [{ type: "text", value: "one" }] },
          { layout: "inline", children: [{ type: "text", value: "two" }] },
        ],
      },
    ]);

    const ordered = blocksOf("3. three\n4. four");
    expect(ordered).toMatchObject([{ type: "list", ordered: true, start: 3, items: [{}, {}] }]);
  });

  it("a nested list sits inside its item", () => {
    const blocks = blocksOf("- a\n  - b\n- c");
    expect(blocks).toMatchObject([
      {
        type: "list",
        items: [
          { layout: "blocks", children: [{ type: "paragraph" }, { type: "list", items: [{ layout: "inline" }] }] },
          { layout: "inline" },
        ],
      },
    ]);
  });

  it("a block that starts with an indented item terminates and renders the list", () => {
    const blocks = blocksOf("  - indented\n  - second");
    expect(blocks).toMatchObject([{ type: "list", items: [{ layout: "inline" }, { layout: "inline" }] }]);
    expect(astText(blocks)).toBe("indentedsecond");
  });

  it("a lazy line after an item continues that item", () => {
    const blocks = blocksOf("- first\nlazy text");
    expect(blocks).toStrictEqual([
      { type: "list", ordered: false, start: 1, items: [{ layout: "inline", children: [{ type: "text", value: "first\nlazy text" }] }] },
    ]);
    expect(astText(blocks)).toBe("first\nlazy text");
  });

  it("a blockquote holds its own paragraphs", () => {
    const blocks = blocksOf("> quoted\n> more");
    expect(blocks).toStrictEqual([
      { type: "blockquote", children: [{ type: "paragraph", children: [{ type: "text", value: "quoted\nmore" }] }] },
    ]);
    expect(astText(blocks)).toBe("quoted\nmore");
  });

  it("nested blockquotes are bounded by depth", () => {
    const blocks = blocksOf(`${"> ".repeat(20)}deep`);
    let depth = 0;
    let node: Block | undefined = blocks[0];
    while (node && node.type === "blockquote") {
      depth++;
      node = node.children[0];
    }
    expect(depth >= 1 && depth <= 6, `nesting depth ${depth}`).toBe(true);
    expect(astText(blocks)).toMatch(/deep/);
  });

  it("a soft line break inside a paragraph stays part of the paragraph", () => {
    const blocks = blocksOf("line one\nline two");
    expect(blocks).toStrictEqual([{ type: "paragraph", children: [{ type: "text", value: "line one\nline two" }] }]);
    expect(astText(blocks)).toBe("line one\nline two");
  });
});
