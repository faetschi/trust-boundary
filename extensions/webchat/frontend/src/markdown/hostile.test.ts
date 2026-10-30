// Ported from the hostile-payload group of internal/webchat/jstest/markdown.test.mjs.
// The payloads come from legacy-fixtures.json, extracted from the legacy file.

import { describe, expect, it } from "vitest";
import type { Block, LinkInline } from "./ast";
import { parseMarkdown } from "./parse";
import { blocksToDom } from "./toDom";
import { type ShimChild, ShimElement, createShimDocument } from "./test-support/dom-shim";
import { HOSTILE } from "./test-support/corpus";
import { astText, domOfBlocks, walk } from "./test-support/render";

const FORBIDDEN_TAGS = new Set(["script", "img", "iframe", "style", "svg", "object", "embed", "form", "input", "link", "meta"]);

// The only elements the mapping may create.
const ALLOWED_TAGS = new Set(["p", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "pre", "code", "em", "strong", "a"]);

function linksOf(blocks: readonly Block[]): LinkInline[] {
  return walk(blocks).filter((node): node is LinkInline => "type" in node && node.type === "link");
}

function tagsOfDom(children: readonly ShimChild[]): string[] {
  const tags: string[] = [];
  const visit = (node: ShimChild): void => {
    if (!(node instanceof ShimElement)) return;
    tags.push(node.tagName);
    for (const child of node.children) visit(child);
  };
  for (const node of children) visit(node);
  return tags;
}

describe("hostile payloads", () => {
  for (const [name, payload] of HOSTILE) {
    it(`hostile payload renders as literal text: ${name}`, () => {
      const blocks = parseMarkdown(payload).blocks;
      expect(linksOf(blocks), "no link may be created").toHaveLength(0);
      expect(astText(blocks)).toBe(payload);

      const container = domOfBlocks(blocks);
      for (const tag of tagsOfDom(container.children)) {
        expect(FORBIDDEN_TAGS.has(tag), `created <${tag}>`).toBe(false);
        expect(ALLOWED_TAGS.has(tag), `created <${tag}>`).toBe(true);
      }
      expect(container.textContent).toBe(payload);
    });
  }
});

it("no script, image, frame or style element is ever created", () => {
  const doc = createShimDocument();
  const created: string[] = [];
  const original = doc.createElement;
  doc.createElement = (tag: string) => {
    created.push(tag.toLowerCase());
    return original(tag);
  };
  for (const [, payload] of HOSTILE) blocksToDom(doc, parseMarkdown(payload).blocks);

  expect(created.length).toBeGreaterThan(0);
  for (const tag of created) {
    expect(FORBIDDEN_TAGS.has(tag), `created <${tag}>`).toBe(false);
    expect(ALLOWED_TAGS.has(tag), `created <${tag}>`).toBe(true);
  }
});

it("random markup-shaped input never creates a forbidden element or a non-allowlisted link", () => {
  // The same deterministic generator and alphabet as the legacy property test.
  let seed = 20261014;
  const next = (): number => {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed;
  };
  const alphabet = ["<", ">", "[", "]", "(", ")", "!", "*", "_", "`", "~", "#", "-", "+", "1.", " ", "\n", "\t", "\\", "javascript:", "https://", "mailto:", "a", "b", "img", "script", "\""];
  for (let round = 0; round < 300; round++) {
    let input = "";
    const length = 1 + (next() % 120);
    for (let i = 0; i < length; i++) input += alphabet[next() % alphabet.length];
    const blocks = parseMarkdown(input).blocks;
    for (const link of linksOf(blocks)) {
      expect(link.href, JSON.stringify(input)).toMatch(/^(https?:|mailto:)/);
    }
    for (const tag of tagsOfDom(domOfBlocks(blocks).children)) {
      expect(FORBIDDEN_TAGS.has(tag), `created <${tag}> for ${JSON.stringify(input)}`).toBe(false);
    }
  }
});
