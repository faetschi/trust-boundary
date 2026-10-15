// Security properties of the parser and its AST. The types carry the first three
// (SafeHref, text-only link children, the closed node kinds); the runtime checks
// confirm them on the parsed corpora, and the source scans keep the modules free of
// markup sinks.

import { describe, expect, it } from "vitest";
import type { Block, Inline, LinkInline, SafeHref, TextInline } from "./ast";
import { parseMarkdown, safeHref } from "./parse";
import { ADVERSARIAL, HOSTILE, ORDINARY, STREAM, randomLinkShaped, randomMarkdown, seededRandom } from "./test-support/corpus";
import { walk, type AnyNode } from "./test-support/render";
import astSource from "./ast.ts?raw";
import parseSource from "./parse.ts?raw";
import incrementalSource from "./incremental.ts?raw";
import toDomSource from "./toDom.ts?raw";
import toHtmlSource from "./toHtmlString.ts?raw";

type IsEqual<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;

// Compile-time checks. If one of these stops holding, tsc fails before any test runs.
const hrefIsSafeHref: IsEqual<LinkInline["href"], SafeHref> = true;
const linkChildrenAreTextOnly: IsEqual<LinkInline["children"][number], TextInline> = true;
const plainStringIsNotSafeHref: IsEqual<string extends SafeHref ? true : false, false> = true;
const inlineKindsAreClosed: IsEqual<Inline["type"], "text" | "code" | "em" | "strong" | "link"> = true;
const blockKindsAreClosed: IsEqual<Block["type"], "paragraph" | "heading" | "list" | "blockquote" | "code" | "notice"> = true;

const ALLOWED_KEYS = new Set([
  "type", "value", "children", "href", "level", "ordered", "start", "items", "layout", "language", "text", "kind",
]);
const LANGUAGE = /^[A-Za-z0-9_+#.-]{1,32}$/;

const SINKS = ["innerHTML", "outerHTML", "insertAdjacentHTML", "DOMParser", "createContextualFragment", "dangerouslySetInnerHTML"];

function corpus(): string[] {
  const rng = seededRandom(0xc0ffee);
  const random = Array.from({ length: 3000 }, () => randomMarkdown(rng, 40));
  // Random tokens rarely form a link, so a link-shaped set is added, safe and unsafe alike.
  const links = Array.from({ length: 1000 }, () => randomLinkShaped(rng));
  return [...HOSTILE.map(([, payload]) => payload), ...ORDINARY.map(([text]) => text), STREAM, ...ADVERSARIAL, ...random, ...links];
}

function nodesOf(text: string): AnyNode[] {
  return walk(parseMarkdown(text).blocks);
}

describe("security properties", () => {
  it("the markdown modules contain no HTML parsing or markup sinks", () => {
    for (const source of [astSource, parseSource, incrementalSource, toDomSource, toHtmlSource]) {
      for (const sink of SINKS) expect(source.includes(sink), `must not use ${sink}`).toBe(false);
      expect(/\beval\s*\(|new Function\s*\(/.test(source), "must not evaluate strings").toBe(false);
    }
  });

  it("SafeHref is produced only by safeHref, the one cast in parse.ts", () => {
    expect(hrefIsSafeHref && linkChildrenAreTextOnly && plainStringIsNotSafeHref).toBe(true);
    expect(inlineKindsAreClosed && blockKindsAreClosed).toBe(true);
    const castsIn = (source: string): number => source.split("as SafeHref").length - 1;
    expect(castsIn(parseSource), "parse.ts makes the only SafeHref").toBe(1);
    expect(castsIn(astSource), "ast.ts").toBe(0);
    expect(castsIn(incrementalSource), "incremental.ts").toBe(0);
    expect(castsIn(toDomSource), "toDom.ts").toBe(0);
    expect(castsIn(toHtmlSource), "toHtmlString.ts").toBe(0);
  });

  it("every link href in the AST is a fixed point of safeHref with an allowed scheme", () => {
    let links = 0;
    for (const text of corpus()) {
      for (const node of nodesOf(text)) {
        if (!("type" in node) || node.type !== "link") continue;
        links++;
        expect(safeHref(node.href), JSON.stringify(text)).toBe(node.href);
        expect(["http:", "https:", "mailto:"]).toContain(new URL(node.href).protocol);
      }
    }
    expect(links, "the corpus must contain links for this to mean anything").toBeGreaterThan(100);
  });

  it("no node carries a style or event-handler concept: every key is in the closed set", () => {
    for (const text of corpus()) {
      for (const node of nodesOf(text)) {
        for (const key of Object.keys(node)) {
          expect(ALLOWED_KEYS.has(key), `key ${key} in ${JSON.stringify(text)}`).toBe(true);
          expect(/style|^on|attribute|html/i.test(key), `key ${key}`).toBe(false);
        }
      }
    }
  });

  it("link children never contain a link", () => {
    for (const text of corpus()) {
      for (const node of nodesOf(text)) {
        if (!("type" in node) || node.type !== "link") continue;
        for (const child of node.children) expect(child.type, JSON.stringify(text)).toBe("text");
      }
    }
  });

  it("the code language token is restricted to the legacy character set", () => {
    const infos = ["js", "c++", "x_y", "a.b-c", "#lang", "ünï", "a<img src=x onerror=alert(1)>", "js<b>", "a".repeat(33), ""];
    for (const info of infos) {
      const [block] = parseMarkdown(`\`\`\`${info}\ncode\n\`\`\``).blocks;
      expect(block?.type === "code" && (block.language === null || LANGUAGE.test(block.language)), JSON.stringify(info)).toBe(true);
    }
    expect(parseMarkdown("```c++ extra\nx\n```").blocks).toMatchObject([{ type: "code", language: "c++" }]);
    expect(parseMarkdown("```js<b>\nx\n```").blocks).toMatchObject([{ type: "code", language: null }]);
    for (const text of corpus()) {
      for (const node of nodesOf(text)) {
        if ("type" in node && node.type === "code" && "language" in node) {
          expect(node.language === null || LANGUAGE.test(node.language), JSON.stringify(text)).toBe(true);
        }
      }
    }
  });

  it("inline text is never empty, so no text node is ever an empty string", () => {
    for (const text of corpus()) {
      for (const node of nodesOf(text)) {
        if ("type" in node && node.type === "text") expect(node.value.length, JSON.stringify(text)).toBeGreaterThan(0);
      }
    }
  });
});
