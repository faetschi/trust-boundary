// The DOM mapping contract (toDom.ts). The legacy suite asserted these attributes and
// tag sequences on the DOM it built. They now hold for the diagnostic mapping, and the
// Phase 3 React mapper must reproduce the same attributes and elements.

import { describe, expect, it } from "vitest";
import { parseMarkdown } from "./parse";
import { type ShimChild, ShimElement } from "./test-support/dom-shim";
import { domOfBlocks } from "./test-support/render";

function domOf(text: string): ShimElement {
  return domOfBlocks(parseMarkdown(text).blocks);
}

function childElements(node: ShimElement): ShimElement[] {
  return node.children.filter((child: ShimChild): child is ShimElement => child instanceof ShimElement);
}

function descendants(node: ShimElement): ShimElement[] {
  return childElements(node).flatMap((child) => [child, ...descendants(child)]);
}

function tagsOf(root: ShimElement): string[] {
  return descendants(root).map((node) => node.tagName);
}

describe("DOM mapping contract", () => {
  it("an allowed link is an anchor with the hardened attributes", () => {
    const root = domOf("[docs](https://example.com/docs) and [mail](mailto:a@example.com)");
    const anchors = descendants(root).filter((node) => node.tagName === "a");
    expect(anchors).toHaveLength(2);
    expect(anchors[0]?.attributes).toStrictEqual({
      href: "https://example.com/docs",
      rel: "noopener noreferrer nofollow",
      target: "_blank",
    });
    expect(anchors[0]?.textContent).toBe("docs");
    expect(anchors[1]?.attributes.href).toBe("mailto:a@example.com");
  });

  it("a long code block is a labelled, focusable scroll region", () => {
    const pre = childElements(domOf("```js\nconst x = 1;\n```"))[0];
    expect(pre).toMatchObject({ tagName: "pre", className: "md-code" });
    expect(pre?.attributes).toMatchObject({ tabindex: "0", role: "region", "aria-label": "Code block, js" });
    expect(childElements(domOf("```\nx\n```"))[0]?.attributes["aria-label"]).toBe("Code block");
  });

  it("a fenced block keeps its language as a class on its code element", () => {
    const root = domOf("```js\nconst a = 1;\n\nconst b = 2;\n```\n\nafter");
    const pre = childElements(root)[0];
    const code = pre ? childElements(pre)[0] : undefined;
    expect(code).toMatchObject({ tagName: "code", className: "language-js" });
    expect(code?.textContent).toBe("const a = 1;\n\nconst b = 2;");
    expect(childElements(root)[1]?.textContent).toBe("after");
  });

  it("the element sequence follows the AST: pre and code, lists, quotes and headings", () => {
    const cases: [string, string[]][] = [
      ["```js\nconst a = 1;\n\nconst b = 2;\n```\n\nafter", ["pre", "code", "p"]],
      ["```py\nprint(1)\n\nprint(2)", ["pre", "code"]],
      ["```\n**not bold** [x](https://a.example) <b>tag</b>\n```", ["pre", "code"]],
      ["# One", ["h3"]],
      ["## Two", ["h4"]],
      ["- one\n- two", ["ul", "li", "li"]],
      ["  - indented\n  - second", ["ul", "li", "li"]],
      ["> quoted\n> more", ["blockquote", "p"]],
      ["line one\nline two", ["p"]],
      ["1. first *item*", ["ol", "li", "em"]],
    ];
    for (const [text, tags] of cases) {
      expect(tagsOf(domOf(text)), JSON.stringify(text)).toStrictEqual(tags);
    }
  });

  it("an ordered list starts at its first number, and only then carries start", () => {
    expect(childElements(domOf("3. three\n4. four"))[0]?.attributes.start).toBe("3");
    expect(childElements(domOf("1. one\n2. two"))[0]?.attributes.start).toBeUndefined();
    expect(childElements(domOf("- one"))[0]?.attributes.start).toBeUndefined();
  });

  it("a truncation notice is a paragraph with the truncated class", () => {
    const notice = childElements(domOf("x".repeat(65537))).at(-1);
    expect(notice).toMatchObject({ tagName: "p", className: "md-truncated" });
    expect(notice?.textContent).toMatch(/truncated/i);
  });
});
