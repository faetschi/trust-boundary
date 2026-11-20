// DOM node identity becomes AST object identity: a view keys its memoised rows on it.

import { describe, expect, it } from "vitest";
import type { Block } from "./ast";
import { createIncrementalParser } from "./incremental";
import { parseMarkdown } from "./parse";
import { STREAM } from "./test-support/corpus";
import { childrenFingerprint } from "./test-support/dom-shim";
import { domOfBlocks } from "./test-support/render";

function fingerprintOf(blocks: readonly Block[]): string {
  return childrenFingerprint(domOfBlocks(blocks));
}

describe("streaming: stable-prefix incremental parsing", () => {
  it("every streamed prefix renders the same DOM as a fresh render of that prefix", () => {
    const parser = createIncrementalParser();
    for (let end = 1; end <= STREAM.length; end++) {
      const prefix = STREAM.slice(0, end);
      const streamed = parser.update(prefix);
      const fresh = parseMarkdown(prefix).blocks;
      expect(streamed.blocks, `prefix ${end}`).toStrictEqual(fresh);
      expect(fingerprintOf(streamed.blocks), `prefix ${end}`).toBe(fingerprintOf(fresh));
    }
  });

  it("completed leading blocks keep their node while the tail streams", () => {
    const parser = createIncrementalParser();
    const first = parser.update("# Title\n\nFirst paragraph.\n\nSecond");
    const [heading, paragraph, second] = first.blocks;

    const grown = parser.update("# Title\n\nFirst paragraph.\n\nSecond paragraph grows.");
    const tail = grown.blocks[2];
    expect(grown.blocks[0], "heading node is untouched").toBe(heading);
    expect(grown.blocks[1], "completed paragraph node is untouched").toBe(paragraph);
    expect(tail, "the growing tail is re-rendered").not.toBe(second);
    expect(grown).toMatchObject({ reused: 2, rendered: 1 });

    const extended = parser.update("# Title\n\nFirst paragraph.\n\nSecond paragraph grows.\n\nNew block");
    expect(extended.blocks[0]).toBe(heading);
    expect(extended.blocks[1]).toBe(paragraph);
    expect(extended.blocks[2], "a block that has been closed by a blank line is kept").toBe(tail);
    expect(extended.blocks).toHaveLength(4);
    expect(extended).toMatchObject({ reused: 3, rendered: 1 });
  });

  it("a text that does not extend the previous text fully re-renders", () => {
    const parser = createIncrementalParser();
    const before = parser.update("# Old\n\nkept?").blocks;
    const stats = parser.update("# New\n\nkept?");
    expect(stats).toMatchObject({ reused: 0, rendered: 2 });
    for (const node of before) expect(stats.blocks.includes(node), "no stale node may remain").toBe(false);
    expect(stats.blocks).toStrictEqual(parseMarkdown("# New\n\nkept?").blocks);
  });

  it("shrinking the text never leaves stale DOM behind", () => {
    const parser = createIncrementalParser();
    parser.update("alpha\n\nbeta\n\ngamma");
    const shrunk = parser.update("alpha");
    expect(shrunk.blocks).toHaveLength(1);
    expect(fingerprintOf(shrunk.blocks)).toBe(fingerprintOf(parseMarkdown("alpha").blocks));
  });

  it("reuse counts source blocks, so one source block that parses to two nodes is one unit", () => {
    // "a" and "- b" share a source block (no blank line between them), which parses
    // to a paragraph and a list. The reuse count is of source blocks, as in legacy.
    const parser = createIncrementalParser();
    const first = parser.update("a\n- b\n\nc");
    expect(first.blocks).toHaveLength(3);
    const second = parser.update("a\n- b\n\nc more");
    expect(second).toMatchObject({ reused: 1, rendered: 1 });
    expect(second.blocks[0]).toBe(first.blocks[0]);
    expect(second.blocks[1]).toBe(first.blocks[1]);
  });
});
