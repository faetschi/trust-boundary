// A pure incremental parser for a streaming message. update(fullText) keeps the
// AST of every leading top-level block whose source text is unchanged, and returns
// the same node objects for them, so a view can skip them by identity. A text that
// does not extend the previous text is parsed from scratch.
//
// The reuse rule is the legacy renderer's: a block is reused only when the whole
// previous text is a prefix of the new one and the block's source is equal at the
// same position. Parsing a block depends only on its own source, so reuse is exact.

import type { Block, ContentBlock } from "./ast";
import { type Limits, noticeBlock, renderSource, segment } from "./parse";

export interface IncrementalResult {
  readonly blocks: readonly Block[];
  // Leading top-level source blocks reused, and source blocks parsed. Both count
  // source blocks, as the legacy renderer does, not AST nodes.
  readonly reused: number;
  readonly rendered: number;
}

export interface IncrementalParser {
  update(fullText: string): IncrementalResult;
}

interface Entry {
  readonly source: string;
  readonly blocks: readonly ContentBlock[];
}

export function createIncrementalParser(limits: Partial<Limits> = {}): IncrementalParser {
  let previous = "";
  let entries: Entry[] = [];

  return {
    update(fullText: string): IncrementalResult {
      const { sources, truncated } = segment(fullText, limits);

      let keep = 0;
      if (fullText.startsWith(previous)) {
        while (keep < sources.length && keep < entries.length && entries[keep]?.source === sources[keep]) keep++;
      }

      entries = entries.slice(0, keep);
      for (const source of sources.slice(keep)) entries.push({ source, blocks: renderSource(source) });

      const blocks: Block[] = [];
      for (const entry of entries) {
        for (const node of entry.blocks) blocks.push(node);
      }
      if (truncated) blocks.push(noticeBlock(truncated));

      previous = fullText;
      return { blocks, reused: keep, rendered: sources.length - keep };
    },
  };
}
