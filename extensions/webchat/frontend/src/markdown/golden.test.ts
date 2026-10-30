// Ported from the "ordinary markdown is unchanged" test of
// internal/webchat/jstest/markdown.test.mjs. The digests are the legacy ones, computed
// over the legacy DOM serialization. The same serialization of the AST's DOM mapping
// must hash to the same 16 hex digits.

import { describe, expect, it } from "vitest";
import { parseMarkdown } from "./parse";
import { ORDINARY } from "./test-support/corpus";
import { childrenFingerprint } from "./test-support/dom-shim";
import { domOfBlocks } from "./test-support/render";

async function digest(text: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(bytes), (byte) => byte.toString(16).padStart(2, "0"))
    .join("")
    .slice(0, 16);
}

describe("ordinary markdown", () => {
  it("ordinary markdown renders the same DOM as before the look-ahead rewrite", async () => {
    expect(ORDINARY).toHaveLength(31);
    for (const [text, expected] of ORDINARY) {
      const actual = await digest(childrenFingerprint(domOfBlocks(parseMarkdown(text).blocks)));
      expect(actual, `digest of ${JSON.stringify(text)}`).toBe(expected);
    }
  });
});
