// TEST-ONLY deterministic corpora for the parity and property suites. Every input is
// derived from a seed, so a failure reproduces from its seed and index. The legacy
// strings come from legacy-fixtures.json, which was extracted mechanically from the
// legacy test file (see docs/phase2-markdown.md), not retyped.

import fixtures from "./legacy-fixtures.json" with { type: "json" };

export type Pair = readonly [name: string, value: string];

export function seededRandom(seed: number): () => number {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function randomInt(rng: () => number, bound: number): number {
  return Math.floor(rng() * bound);
}

function pick<T>(rng: () => number, items: readonly T[]): T {
  const item = items[randomInt(rng, items.length)];
  if (item === undefined) throw new RangeError("pick from an empty list");
  return item;
}

// Each token is a delimiter, a block marker, a link or image part, a fence, markup
// or a word. The tokens are short on purpose: a random sequence of them reaches
// every branch of the parser, and markup is never written as one source line that a
// source gate could match (the gate scans this file).
const MARKDOWN_TOKENS: readonly string[] = [
  "*", "**", "***", "_", "__", "`", "``", "```", "~~~", "~~", "[", "]", "[x]", "(", ")", "](", "![", "!",
  "\\", "\\*", "\\_", "\\[", "\\`", "#", "##", "###", "# ", "## ", "#######",
  " ", "  ", "   ", "    ", "\t", "\n", "\n", "\n\n", "\r\n", "\r",
  ">", "> ", "> > ", "- ", "+ ", "* ", "1. ", "2) ", "0. ", "10. ", "-", "1.",
  "a", "b", "word", "é", "😀", " ", "x y", "c++", "js",
  "https://a.example/x", "http://b.example", "mailto:c@d.example", "javascript:", "JavaScript:",
  "data:", "vbscript:", "file:///x", "//e.example", "/rel",
  "<", ">", "&", "\"", "'", "script", "img", "src=x", "style", "onclick=", "/",
  "```js\n", "```py ", "```\n", "\n```", "~~~\n", "\n~~~", "````",
];

export function randomMarkdown(rng: () => number, maxTokens: number): string {
  const count = 1 + randomInt(rng, maxTokens);
  let out = "";
  for (let i = 0; i < count; i++) out += pick(rng, MARKDOWN_TOKENS);
  return out;
}

const URL_PIECES: readonly string[] = [
  "java", "script", "http", "https", "mailto", "data", "file", ":", "//", "/", "\t", "\n", "\r", " ",
  "\u0000", "\u001f", "\u0085", " ", " ", "a", "A", "example.com", "%20", "@", "?", "#", "x", "+", "-", ".",
];

export function randomUrlText(rng: () => number): string {
  const count = 1 + randomInt(rng, 8);
  let out = "";
  for (let i = 0; i < count; i++) out += pick(rng, URL_PIECES);
  return out;
}

const LINK_SCHEMES: readonly string[] = [
  "https://", "http://", "HTTPS://", "mailto:", "javascript:", "data:", "ftp://", "//", "/", "https:\t//", "  https://",
];
const LINK_HOSTS: readonly string[] = ["a.example", "b.example/x?y=1", "c.example#frag", "user@d.example", "e"];

// A link-shaped input: [label](url), where the URL is mostly well formed and is
// sometimes followed by noise. Random tokens almost never form a link, so the link
// paths need this generator to be exercised at all.
export function randomLinkShaped(rng: () => number): string {
  const noise = rng() < 0.2 ? randomUrlText(rng) : "";
  return `[${randomMarkdown(rng, 3)}](${pick(rng, LINK_SCHEMES)}${pick(rng, LINK_HOSTS)}${noise})`;
}

function pairs(rows: readonly (readonly string[])[]): readonly Pair[] {
  return rows.map((row) => {
    const [name, value] = row;
    if (row.length !== 2 || name === undefined || value === undefined) throw new Error("fixture row is not a pair");
    return [name, value] as const;
  });
}

// The 18 hostile payloads of the legacy suite: [name, payload].
export const HOSTILE: readonly Pair[] = pairs(fixtures.hostile);

// The 31 golden ordinary samples of the legacy suite: [text, legacy DOM digest].
export const ORDINARY: readonly Pair[] = pairs(fixtures.ordinary);

// The streamed message of the legacy streaming suite.
export const STREAM: string = fixtures.stream;

// The adversarial 4096-character messages of the legacy performance suite.
export const ADVERSARIAL: readonly string[] = [
  "*".repeat(4096),
  "_".repeat(4096),
  "[".repeat(4096),
  "[a](".repeat(1024),
  "*_[`".repeat(1024),
  "`a".repeat(2048),
];
