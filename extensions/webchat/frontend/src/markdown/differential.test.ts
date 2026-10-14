// Differential suite. The legacy renderer (internal/webchat/assets/markdown.mjs, loaded
// unchanged) is run against the new parser on the same inputs. An input agrees when:
//   - the DOM fingerprint of the legacy output equals that of the AST's DOM mapping
//     (tags, classes, attributes, and text-node boundaries);
//   - the AST's HTML serialization equals the serialized DOM mapping;
//   - the look-ahead work counter is identical, so the two take the same search path.
// Streams also compare the reuse and render counts of every update.

import { beforeAll, describe, expect, it } from "vitest";
import { createIncrementalParser } from "./incremental";
import { parseMarkdown, safeHref, scanWork, splitBlocks } from "./parse";
import {
  ADVERSARIAL,
  HOSTILE,
  ORDINARY,
  STREAM,
  randomInt,
  randomLinkShaped,
  randomMarkdown,
  randomUrlText,
  seededRandom,
} from "./test-support/corpus";
import { childrenFingerprint } from "./test-support/dom-shim";
import { type LegacyMarkdown, legacyStream, loadLegacy, renderLegacy } from "./test-support/legacy";
import { domOfBlocks, renderNew } from "./test-support/render";

interface Tally {
  checked: number;
  mismatched: number;
  examples: string[];
}

function newTally(): Tally {
  return { checked: 0, mismatched: 0, examples: [] };
}

function compare(legacy: LegacyMarkdown, text: string, tally: Tally): void {
  tally.checked++;
  const old = renderLegacy(legacy, text);
  const mine = renderNew(text);
  const problems: string[] = [];
  if (old.fingerprint !== mine.fingerprint) problems.push("dom");
  if (mine.html !== mine.htmlOfString) problems.push("html");
  if (old.work !== mine.work) problems.push(`work ${old.work} vs ${mine.work}`);
  if (problems.length > 0) {
    tally.mismatched++;
    if (tally.examples.length < 5) tally.examples.push(`${problems.join(", ")}: ${JSON.stringify(text)}`);
  }
}

function report(name: string, tally: Tally, minimum: number): void {
  console.info(`[differential] ${name}: ${tally.checked} inputs, ${tally.mismatched} mismatches`);
  expect(tally.checked, `${name}: corpus size`).toBeGreaterThanOrEqual(minimum);
  expect(tally.examples, name).toStrictEqual([]);
  expect(tally.mismatched, name).toBe(0);
}

// The stream of one message: every prefix at a random cut, with a few rewrites of an
// earlier character so that some updates do not extend the previous text.
function streamSteps(rng: () => number, full: string): string[] {
  const steps: string[] = [];
  let cut = 0;
  while (cut < full.length) {
    cut = Math.min(full.length, cut + 1 + randomInt(rng, 6));
    const prefix = full.slice(0, cut);
    steps.push(rng() < 0.05 && prefix.length > 0 ? `${prefix.slice(0, -1)}!` : prefix);
  }
  return steps;
}

describe("differential: legacy renderer vs AST parser", () => {
  let legacy: LegacyMarkdown;
  beforeAll(async () => {
    legacy = await loadLegacy();
  });

  it("the legacy module is the one under test", () => {
    expect(legacy.MAX_MARKDOWN_CHARS).toBe(65536);
    expect(legacy.MAX_BLOCKS).toBe(512);
    expect(renderLegacy(legacy, "**bold** [x](https://a.example)").fingerprint).toContain("<strong>");
  });

  it("(a) the 31 golden ordinary samples agree", () => {
    const tally = newTally();
    for (const [text] of ORDINARY) compare(legacy, text, tally);
    report("golden samples", tally, 31);
  });

  it("(b) the 18 hostile payloads agree", () => {
    const tally = newTally();
    for (const [, payload] of HOSTILE) compare(legacy, payload, tally);
    report("hostile payloads", tally, 18);
  });

  it("(b) the adversarial 4096-character messages agree, look-ahead work included", () => {
    const tally = newTally();
    for (const text of ADVERSARIAL) compare(legacy, text, tally);
    report("adversarial messages", tally, ADVERSARIAL.length);
  });

  it("(c) a seeded fuzz corpus of 100,000 markdown-token inputs agrees", () => {
    const rng = seededRandom(0x5eed0001);
    const tally = newTally();
    for (let index = 0; index < 100_000; index++) compare(legacy, randomMarkdown(rng, 40), tally);
    report("fuzz corpus (seed 0x5eed0001)", tally, 100_000);
  }, 600_000);

  it("(c) 20,000 link-shaped inputs agree, so the safe and unsafe link paths are compared", () => {
    const rng = seededRandom(0x5eed0005);
    const tally = newTally();
    for (let index = 0; index < 20_000; index++) compare(legacy, randomLinkShaped(rng), tally);
    report("link-shaped inputs (seed 0x5eed0005)", tally, 20_000);
  }, 600_000);

  it("(c) look-ahead window boundaries agree: closers and labels either side of 512 and 2048", () => {
    // The golden sample named "after the window" closes at distance 319, inside the
    // 512 window, so it never crosses the bound. These do, on both sides of it.
    const tally = newTally();
    const around = [1, 2, 3, 255, 256, 257, 510, 511, 512, 513, 514, 1023, 1024, 1025];
    for (const distance of around) {
      const run = "x".repeat(Math.max(0, distance - 1));
      compare(legacy, `*${run}*`, tally);
      compare(legacy, `_${run}_`, tally);
      compare(legacy, `**${"x".repeat(Math.max(0, distance - 2))}**`, tally);
      compare(legacy, `\`${run}\``, tally);
      compare(legacy, `[${run}](https://a.example)`, tally);
    }
    for (const distance of [2046, 2047, 2048, 2049, 2050]) {
      compare(legacy, `[a](https://a.example/${"x".repeat(Math.max(0, distance - 20))})`, tally);
      compare(legacy, `[a](${"x".repeat(distance)})`, tally);
    }
    report("look-ahead window boundaries", tally, around.length * 5 + 10);
  });

  it("(c) block-heavy inputs over the 512-block bound agree", () => {
    const rng = seededRandom(0x5eed0002);
    const tally = newTally();
    for (let index = 0; index < 300; index++) {
      const pieces = Array.from({ length: 510 + randomInt(rng, 30) }, () => randomMarkdown(rng, 4));
      compare(legacy, pieces.join("\n\n"), tally);
    }
    report("block-heavy inputs (seed 0x5eed0002)", tally, 300);
  }, 600_000);

  it("(c) splitBlocks and safeHref agree on 100,000 and 50,000 seeded inputs", () => {
    const rng = seededRandom(0x5eed0003);
    let blocksChecked = 0;
    for (let index = 0; index < 100_000; index++) {
      const text = randomMarkdown(rng, 40);
      expect(splitBlocks(text), JSON.stringify(text)).toStrictEqual(legacy.splitBlocks(text));
      blocksChecked++;
    }
    let linksChecked = 0;
    for (let index = 0; index < 50_000; index++) {
      const raw = randomUrlText(rng);
      expect(safeHref(raw), JSON.stringify(raw)).toBe(legacy.safeHref(raw));
      linksChecked++;
    }
    console.info(`[differential] splitBlocks: ${blocksChecked} inputs; safeHref: ${linksChecked} inputs; 0 mismatches`);
    expect(blocksChecked).toBeGreaterThanOrEqual(100_000);
    expect(linksChecked).toBeGreaterThanOrEqual(50_000);
  }, 600_000);

  it("(d) 2,500 streamed messages agree at every update (reuse counts) and at the end (result)", () => {
    const rng = seededRandom(0x5eed0004);
    let messages = 0;
    let updates = 0;
    for (let index = 0; index < 2500; index++) {
      const full = randomMarkdown(rng, 60);
      const stream = legacyStream(legacy);
      const parser = createIncrementalParser();
      stream.update("");
      let last = parser.update("");
      let lastText = "";
      for (const text of streamSteps(rng, full)) {
        const old = stream.update(text);
        last = parser.update(text);
        lastText = text;
        expect(last.reused, `reused for ${JSON.stringify(text)}`).toBe(old.reused);
        expect(last.rendered, `rendered for ${JSON.stringify(text)}`).toBe(old.rendered);
        updates++;
      }
      expect(childrenFingerprint(domOfBlocks(last.blocks)), `final DOM of ${JSON.stringify(lastText)}`).toBe(
        childrenFingerprint(stream.container),
      );
      expect(last.blocks, `final AST of ${JSON.stringify(lastText)}`).toStrictEqual(parseMarkdown(lastText).blocks);
      messages++;
    }
    console.info(`[differential] streams: ${messages} messages, ${updates} updates; 0 mismatches`);
    expect(messages).toBeGreaterThanOrEqual(2000);
  }, 600_000);

  it("(d) the legacy 170-character stream agrees at every prefix, DOM and counts included", () => {
    const stream = legacyStream(legacy);
    const parser = createIncrementalParser();
    for (let end = 1; end <= STREAM.length; end++) {
      const prefix = STREAM.slice(0, end);
      const old = stream.update(prefix);
      const mine = parser.update(prefix);
      expect({ reused: mine.reused, rendered: mine.rendered }, `prefix ${end}`).toStrictEqual({ reused: old.reused, rendered: old.rendered });
      expect(childrenFingerprint(domOfBlocks(mine.blocks)), `prefix ${end}`).toBe(childrenFingerprint(stream.container));
    }
  });

  it("(d) the per-update look-ahead work of a 4096-character streamed message agrees", () => {
    const text = "*".repeat(4096);
    const stream = legacyStream(legacy);
    const parser = createIncrementalParser();
    const legacyWork: number[] = [];
    const newWork: number[] = [];
    for (let end = 1; end <= text.length; end++) {
      const prefix = text.slice(0, end);
      const before = legacy.scanWork.steps;
      stream.update(prefix);
      legacyWork.push(legacy.scanWork.steps - before);
      const mine = scanWork.steps;
      parser.update(prefix);
      newWork.push(scanWork.steps - mine);
    }
    expect(newWork).toStrictEqual(legacyWork);
    console.info(`[differential] adversarial stream: ${legacyWork.length} updates, total work ${legacyWork.reduce((a, b) => a + b, 0)}; 0 mismatches`);
  });

  it("(e) the bounds agree: the 64 KiB cut, the 512-block cut, depth 4 and the notice", () => {
    const MAX = 65536;
    const nested = (levels: number): string =>
      Array.from({ length: levels }, (_, index) => `${"  ".repeat(index)}- level ${index}`).join("\n");
    const bounds = [
      "x".repeat(MAX + 500),
      "x".repeat(MAX),
      "x".repeat(MAX + 1),
      `${"a".repeat(MAX - 1)}😀b`,
      `${"a".repeat(MAX - 2)}😀b`,
      "a\n\n".repeat(40000),
      Array.from({ length: 517 }, (_, index) => `para ${index}`).join("\n\n"),
      Array.from({ length: 512 }, (_, index) => `para ${index}`).join("\n\n"),
      "x\n".repeat(70000),
      `\`\`\`\n${"x".repeat(70000)}`,
      "[".repeat(70000),
      "> ".repeat(12) + "deep",
      "> ".repeat(40) + "deep",
      nested(8),
      nested(30),
      "> - a\n> > - b\n> > > - c\n> > > > - d\n> > > > > - e",
    ];
    const tally = newTally();
    for (const text of bounds) {
      compare(legacy, text, tally);
      const { truncated, blocks } = parseMarkdown(text);
      const notice = blocks[blocks.length - 1];
      if (truncated && notice?.type === "notice") {
        expect(renderLegacy(legacy, text).fingerprint, "the legacy DOM carries the same notice text").toContain(
          JSON.stringify(notice.text),
        );
      }
    }
    report("bounds", tally, bounds.length);
  });

  it("(e) a stream beyond the 64 KiB bound agrees at each update", () => {
    const text = "word ".repeat(14000);
    const stream = legacyStream(legacy);
    const parser = createIncrementalParser();
    const texts = [...Array.from({ length: Math.floor(text.length / 1000) }, (_, index) => text.slice(0, (index + 1) * 1000)), text];
    for (const prefix of texts) {
      const old = stream.update(prefix);
      const mine = parser.update(prefix);
      expect({ reused: mine.reused, rendered: mine.rendered }, `length ${prefix.length}`).toStrictEqual({
        reused: old.reused,
        rendered: old.rendered,
      });
      expect(childrenFingerprint(domOfBlocks(mine.blocks)), `length ${prefix.length}`).toBe(childrenFingerprint(stream.container));
    }
  });
});
