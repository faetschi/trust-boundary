// Ported from the bounds group of internal/webchat/jstest/markdown.test.mjs, plus the
// nesting-depth and limits checks the parity plan calls for. The bounds are the
// legacy values: 64 KiB of source, 512 top-level blocks, depth 4.

import { describe, expect, it } from "vitest";
import type { Block } from "./ast";
import { createIncrementalParser } from "./incremental";
import { MAX_BLOCKS, MAX_MARKDOWN_CHARS, parseMarkdown, splitBlocks } from "./parse";
import { astText } from "./test-support/render";

function contentOf(blocks: readonly Block[]): readonly Block[] {
  return blocks.filter((block) => block.type !== "notice");
}

function noticeOf(blocks: readonly Block[]): string | undefined {
  const last = blocks[blocks.length - 1];
  return last?.type === "notice" ? last.text : undefined;
}

// containerDepth is the deepest chain of blockquotes and lists, counting each level.
function containerDepth(blocks: readonly Block[]): number {
  let deepest = 0;
  const visit = (block: Block, depth: number): void => {
    if (block.type === "blockquote") {
      deepest = Math.max(deepest, depth + 1);
      for (const child of block.children) visit(child, depth + 1);
    } else if (block.type === "list") {
      deepest = Math.max(deepest, depth + 1);
      for (const item of block.items) {
        if (item.layout === "blocks") for (const child of item.children) visit(child, depth + 1);
      }
    }
  };
  for (const block of blocks) visit(block, 0);
  return deepest;
}

describe("bounds", () => {
  it("input beyond the character bound is truncated with a visible marker", () => {
    const { blocks, truncated } = parseMarkdown("x".repeat(MAX_MARKDOWN_CHARS + 500));
    const [paragraph] = blocks;
    expect(paragraph).toMatchObject({ type: "paragraph" });
    expect(astText(contentOf(blocks))).toHaveLength(MAX_MARKDOWN_CHARS);
    expect(truncated).toStrictEqual({ chars: { shown: MAX_MARKDOWN_CHARS, total: MAX_MARKDOWN_CHARS + 500 } });
    const marker = blocks[blocks.length - 1];
    expect(marker).toMatchObject({ type: "notice", kind: "truncated" });
    expect(marker?.type === "notice" && /truncated/i.test(marker.text)).toBe(true);
  });

  it("input at the character bound carries no marker", () => {
    const { blocks, truncated } = parseMarkdown("x".repeat(MAX_MARKDOWN_CHARS));
    expect(blocks).toHaveLength(1);
    expect(astText(blocks)).toHaveLength(MAX_MARKDOWN_CHARS);
    expect(truncated).toBeNull();
  });

  it("a surrogate pair is never split at the truncation point", () => {
    const { blocks } = parseMarkdown(`${"a".repeat(MAX_MARKDOWN_CHARS - 1)}😀b`);
    const text = astText(contentOf(blocks));
    expect(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/.test(text), "no lone high surrogate").toBe(false);
    expect(/(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(text), "no lone low surrogate").toBe(false);
    expect(text).toHaveLength(MAX_MARKDOWN_CHARS - 1);
  });

  it("block count is bounded with a visible marker", () => {
    const text = Array.from({ length: MAX_BLOCKS + 5 }, (_, index) => `para ${index}`).join("\n\n");
    const { blocks, truncated } = parseMarkdown(text);
    expect(contentOf(blocks).filter((block) => block.type === "paragraph")).toHaveLength(MAX_BLOCKS);
    expect(noticeOf(blocks)).toMatch(/truncated/i);
    expect(truncated).toStrictEqual({ blocks: { shown: MAX_BLOCKS, total: MAX_BLOCKS + 5, ofShownText: false } });
  });

  it("a block count in the notice is the count of the original input when only blocks are cut", () => {
    const text = Array.from({ length: MAX_BLOCKS + 5 }, (_, index) => `para ${index}`).join("\n\n");
    expect(noticeOf(parseMarkdown(text).blocks)).toBe(
      `Message truncated for display: the first ${MAX_BLOCKS} of ${MAX_BLOCKS + 5} blocks are shown.`,
    );
  });

  it("a block count in the notice says which text it counts when the characters are cut too", () => {
    const text = "a\n\n".repeat(40000);
    const shown = splitBlocks(text.slice(0, MAX_MARKDOWN_CHARS)).length;
    const parsed = parseMarkdown(text);
    expect(shown > MAX_BLOCKS, "the shown text must itself exceed the block bound for this test to mean anything").toBe(true);
    expect(noticeOf(parsed.blocks)).toBe(
      `Message truncated for display: the first ${MAX_MARKDOWN_CHARS} of ${text.length} characters are shown; ` +
        `the first ${MAX_BLOCKS} of the ${shown} blocks in the shown text are rendered.`,
    );
    expect(parsed.truncated).toStrictEqual({
      chars: { shown: MAX_MARKDOWN_CHARS, total: text.length },
      blocks: { shown: MAX_BLOCKS, total: shown, ofShownText: true },
    });
  });

  it("the notice wording for a character cut is exact", () => {
    expect(noticeOf(parseMarkdown("x".repeat(MAX_MARKDOWN_CHARS + 1)).blocks)).toBe(
      "Message truncated for display: the first 65536 of 65537 characters are shown.",
    );
  });

  it("custom limits cut and word the notice the same way", () => {
    const parsed = parseMarkdown("a\n\nb\n\nc", { maxBlocks: 2 });
    expect(contentOf(parsed.blocks)).toHaveLength(2);
    expect(noticeOf(parsed.blocks)).toBe("Message truncated for display: the first 2 of 3 blocks are shown.");
  });

  it("the incremental parser applies the same limits as parseMarkdown", () => {
    const text = "a\n\nb\n\nc\n\nd";
    const parser = createIncrementalParser({ maxBlocks: 2 });
    expect(parser.update(text).blocks).toStrictEqual(parseMarkdown(text, { maxBlocks: 2 }).blocks);
  });

  it("limits must be positive integers", () => {
    expect(() => parseMarkdown("a", { maxChars: 0 })).toThrow(RangeError);
    expect(() => parseMarkdown("a", { maxBlocks: 1.5 })).toThrow(RangeError);
  });

  it("nested blockquotes and lists are bounded at five levels, the innermost flattened", () => {
    // The legacy bound (MAX_DEPTH 4) admits container levels 0 to 4. Content deeper
    // than that is flattened to paragraph text. The parity suite pins these inputs
    // against the legacy DOM.
    const quotes = `${"> ".repeat(12)}deep`;
    expect(containerDepth(parseMarkdown(quotes).blocks)).toBe(5);
    const list = Array.from({ length: 8 }, (_, index) => `${"  ".repeat(index)}- level ${index}`).join("\n");
    expect(containerDepth(parseMarkdown(list).blocks)).toBe(5);
  });
});

describe("splitBlocks", () => {
  it("normalises CRLF and lone CR before it splits", () => {
    expect(splitBlocks("a\r\nb\rc")).toStrictEqual(["a\nb\nc"]);
  });
});
