// The pure markdown parser: source text to the typed AST in ast.ts. It builds no
// DOM and no markup. It is a port of the legacy renderer (internal/webchat/assets/
// markdown.mjs): the same subset, bounds, look-ahead and output. Only the DOM
// calls are replaced by AST construction, so the differential suite can hold the
// two to the same result, text-node boundaries included.

import type {
  Block,
  CodeBlock,
  ContentBlock,
  HeadingBlock,
  HeadingLevel,
  Inline,
  ListBlock,
  ListItem,
  NoticeBlock,
  ParagraphBlock,
  SafeHref,
  TextInline,
} from "./ast";

export const MAX_MARKDOWN_CHARS = 65536;
export const MAX_BLOCKS = 512;
// Work counter: the positions examined by look-ahead searches, as in the legacy
// renderer. Parsing never reads it; the tests read it to bound adversarial cost.
export const scanWork = { steps: 0 };

const MAX_DEPTH = 4; // blockquote and list nesting
const MAX_INLINE_DEPTH = 2; // emphasis inside emphasis
const MAX_SPAN = 512; // look-ahead for a closing delimiter or a link label
const MAX_URL_CHARS = 2048;
const LINK_SCHEMES = new Set(["http:", "https:", "mailto:"]);

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const FENCE_CLOSE = /^ {0,3}(`{3,}|~{3,})[ \t]*$/;
const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*))?[ \t]*$/;
const QUOTE = /^ {0,3}>/;
const QUOTE_PREFIX = /^ {0,3}> ?/;
const ITEM = /^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*))?$/;
const ASCII_PUNCTUATION = /[!-\/:-@\[-`{-~]/;
const WORD = /[\p{L}\p{N}]/u;
const LANGUAGE = /^[A-Za-z0-9_+#.-]{1,32}$/;

export interface Limits {
  readonly maxChars: number;
  readonly maxBlocks: number;
}

// Set when the source was cut. `chars` is present when the characters were cut;
// `blocks` when the cut text (or the whole text) had more blocks than the bound.
// `ofShownText` says the block count is of the cut text, not of the whole input.
export interface Truncation {
  readonly chars?: { readonly shown: number; readonly total: number };
  readonly blocks?: { readonly shown: number; readonly total: number; readonly ofShownText: boolean };
}

export interface ParseResult {
  // Content blocks in order, then one notice block when the source was cut.
  readonly blocks: readonly Block[];
  readonly truncated: Truncation | null;
}

export interface Segment {
  // The top-level source blocks that are shown, each its lines joined by "\n".
  readonly sources: readonly string[];
  readonly truncated: Truncation | null;
}

/* ---------------------------------------------------------------------------
 * Entry points
 * ------------------------------------------------------------------------- */

export function parseMarkdown(text: string, limits: Partial<Limits> = {}): ParseResult {
  const segmented = segment(text, limits);
  const blocks: Block[] = [];
  for (const source of segmented.sources) {
    for (const node of renderSource(source)) blocks.push(node);
  }
  if (segmented.truncated) blocks.push(noticeBlock(segmented.truncated));
  return { blocks, truncated: segmented.truncated };
}

// segment bounds the text by characters, then divides it into top-level blocks and
// keeps the first maxBlocks of them. Both cuts are recorded in `truncated`.
export function segment(text: string, limits: Partial<Limits> = {}): Segment {
  const { maxChars, maxBlocks } = resolveLimits(limits);
  const bounded = boundText(text, maxChars);
  const all = splitBlocks(bounded.text);
  return { sources: all.slice(0, maxBlocks), truncated: truncationOf(bounded, all.length, maxBlocks) };
}

// renderSource parses one top-level block (as splitBlocks returns it).
export function renderSource(source: string): ContentBlock[] {
  return renderLines(source.split("\n"), 0);
}

export function noticeBlock(truncated: Truncation): NoticeBlock {
  return { type: "notice", kind: "truncated", text: truncationNotice(truncated) };
}

// The notice wording is the legacy wording, byte for byte.
function truncationNotice(truncated: Truncation): string {
  const parts: string[] = [];
  if (truncated.chars) {
    parts.push(`the first ${truncated.chars.shown} of ${truncated.chars.total} characters are shown`);
  }
  if (truncated.blocks) {
    const { shown, total, ofShownText } = truncated.blocks;
    parts.push(
      ofShownText
        ? `the first ${shown} of the ${total} blocks in the shown text are rendered`
        : `the first ${shown} of ${total} blocks are shown`,
    );
  }
  return `Message truncated for display: ${parts.join("; ")}.`;
}

function resolveLimits(limits: Partial<Limits>): Limits {
  const maxChars = limits.maxChars ?? MAX_MARKDOWN_CHARS;
  const maxBlocks = limits.maxBlocks ?? MAX_BLOCKS;
  if (!Number.isInteger(maxChars) || maxChars < 1 || !Number.isInteger(maxBlocks) || maxBlocks < 1) {
    throw new RangeError(`markdown limits must be positive integers (maxChars ${maxChars}, maxBlocks ${maxBlocks})`);
  }
  return { maxChars, maxBlocks };
}

interface BoundedText {
  readonly text: string;
  readonly total: number;
  readonly truncated: boolean;
}

function boundText(value: string, maxChars: number): BoundedText {
  if (value.length <= maxChars) return { text: value, total: value.length, truncated: false };
  let end = maxChars;
  const last = value.charCodeAt(end - 1);
  if (last >= 0xd800 && last <= 0xdbff) end -= 1; // never keep half a surrogate pair
  return { text: value.slice(0, end), total: value.length, truncated: true };
}

function truncationOf(bounded: BoundedText, blockCount: number, maxBlocks: number): Truncation | null {
  if (!bounded.truncated && blockCount <= maxBlocks) return null;
  const out: { chars?: { shown: number; total: number }; blocks?: { shown: number; total: number; ofShownText: boolean } } = {};
  // A count taken from cut text is of that text, not of the whole input, so the
  // notice says which it counts.
  if (bounded.truncated) out.chars = { shown: bounded.text.length, total: bounded.total };
  if (blockCount > maxBlocks) out.blocks = { shown: maxBlocks, total: blockCount, ofShownText: bounded.truncated };
  return out;
}

/* ---------------------------------------------------------------------------
 * Block splitting (streaming frontier)
 * ------------------------------------------------------------------------- */

// splitBlocks returns the top-level blocks of `text`, each as its source lines
// joined by "\n" with no surrounding blank lines. Only a block that grows can
// change when text is appended, so a stream re-parses its tail.
export function splitBlocks(text: string): string[] {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  if (lines[lines.length - 1] === "") lines.pop();

  const blocks: string[] = [];
  let current: string[] = [];
  let fence: Fence | null = null;
  const flush = (): void => {
    if (current.length) blocks.push(current.join("\n"));
    current = [];
  };

  for (const line of lines) {
    if (fence) {
      current.push(line);
      if (closingFence(line, fence)) {
        flush();
        fence = null;
      }
      continue;
    }
    if (isBlank(line)) {
      flush();
      continue;
    }
    const open = fenceOpen(line);
    if (open) {
      flush();
      fence = open;
      current.push(line);
      continue;
    }
    if (HEADING.test(line)) {
      flush();
      current.push(line);
      flush();
      continue;
    }
    current.push(line);
  }
  flush();
  return blocks;
}

/* ---------------------------------------------------------------------------
 * Links
 * ------------------------------------------------------------------------- */

// safeHref returns the normalised URL for an allowed link, or null. Browsers drop
// ASCII tab and newline anywhere in a URL and ignore leading and trailing control
// characters and spaces, so the same cleaning runs before the scheme is read. A
// relative or protocol-relative URL is not a link here.
export function safeHref(raw: string): SafeHref | null {
  if (typeof raw !== "string") return null;
  const cleaned = raw.replace(/[\t\n\r]/g, "").replace(/^[\u0000- ]+|[\u0000- ]+$/g, "");
  if (!cleaned || cleaned.length > MAX_URL_CHARS) return null;
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(cleaned)) return null;
  let parsed: URL;
  try {
    parsed = new URL(cleaned);
  } catch {
    return null;
  }
  // The only place a SafeHref is made. Every other module receives one from here.
  return LINK_SCHEMES.has(parsed.protocol) ? (parsed.href as SafeHref) : null;
}

/* ---------------------------------------------------------------------------
 * Block parsing
 * ------------------------------------------------------------------------- */

// renderLines parses the lines of one block (or of one nested container). Nesting
// is bounded by MAX_DEPTH through nestedBlocks.
function renderLines(lines: readonly string[], depth: number): ContentBlock[] {
  const nodes: ContentBlock[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lineAt(lines, i);
    if (isBlank(line)) {
      i++;
      continue;
    }
    const open = fenceOpen(line);
    if (open) {
      let j = i + 1;
      while (j < lines.length && !closingFence(lineAt(lines, j), open)) j++;
      nodes.push(codeBlock(lines.slice(i + 1, j), open.info));
      i = j + 1;
      continue;
    }
    const heading = HEADING.exec(line);
    if (heading) {
      nodes.push(headingNode(heading));
      i++;
      continue;
    }
    if (QUOTE.test(line)) {
      const inner: string[] = [];
      let j = i;
      while (j < lines.length && QUOTE.test(lineAt(lines, j))) inner.push(lineAt(lines, j++).replace(QUOTE_PREFIX, ""));
      nodes.push({ type: "blockquote", children: nestedBlocks(inner, depth) });
      i = j;
      continue;
    }
    const item = parseItem(line);
    if (item) {
      const list = listNode(lines, i, item, depth);
      nodes.push(list.node);
      i = list.next;
      continue;
    }
    let j = i + 1;
    while (j < lines.length && !interruptsParagraph(lineAt(lines, j))) j++;
    nodes.push(paragraphNode(lines.slice(i, j).join("\n")));
    i = j;
  }
  return nodes;
}

function nestedBlocks(lines: readonly string[], depth: number): ContentBlock[] {
  if (depth < MAX_DEPTH) return renderLines(lines, depth + 1);
  // Past the depth bound the structure is flattened to a paragraph of text.
  const text = lines.join("\n").trim();
  return text ? [paragraphNode(text)] : [];
}

function paragraphNode(text: string): ParagraphBlock {
  return { type: "paragraph", children: inlineNodes(text, 0) };
}

function headingNode(match: RegExpExecArray): HeadingBlock {
  const hashes = (match[1] ?? "").length;
  const text = (match[2] ?? "").replace(/(^|[ \t]+)#+$/, "").trim();
  return { type: "heading", level: demotedLevel(hashes), children: inlineNodes(text, 0) };
}

// Message headings sit below the page's own headings: # is h3. Deeper headings are
// capped at h6, as the legacy min(level + 2, 6) did.
function demotedLevel(hashes: number): HeadingLevel {
  if (hashes <= 1) return 3;
  if (hashes === 2) return 4;
  if (hashes === 3) return 5;
  return 6;
}

interface ItemHead {
  readonly indent: number;
  readonly ordered: boolean;
  readonly start: number;
  readonly text: string;
}

// listNode consumes consecutive items of one kind. An indented or lazy line belongs
// to the item above it, and a nested list is parsed inside that item. The first
// item is always consumed, so the caller always makes progress.
function listNode(lines: readonly string[], start: number, first: ItemHead, depth: number): { node: ListBlock; next: number } {
  const items: ListItem[] = [];
  let i = start;
  let item: ItemHead | null = first;
  while (item && item.ordered === first.ordered) {
    const body = [item.text];
    i++;
    while (i < lines.length && continuesItem(lineAt(lines, i), item.indent)) {
      body.push(lineAt(lines, i++).replace(/^ {1,4}/, ""));
    }
    items.push(
      body.some(isStructural)
        ? { layout: "blocks", children: nestedBlocks(body, depth) }
        : { layout: "inline", children: inlineNodes(body.join("\n"), 0) },
    );
    item = i < lines.length ? parseItem(lineAt(lines, i)) : null;
  }
  return { node: { type: "list", ordered: first.ordered, start: first.start, items }, next: i };
}

function codeBlock(lines: readonly string[], info: string): CodeBlock {
  return { type: "code", language: languageOf(info), text: lines.join("\n") };
}

function languageOf(info: string): string | null {
  const word = info.split(/\s+/)[0] || "";
  return LANGUAGE.test(word) ? word : null;
}

/* ---------------------------------------------------------------------------
 * Line classification
 * ------------------------------------------------------------------------- */

interface Fence {
  readonly marker: string;
  readonly length: number;
  readonly info: string;
}

function lineAt(lines: readonly string[], index: number): string {
  return lines[index] ?? "";
}

function isBlank(line: string): boolean {
  return line.trim() === "";
}

function fenceOpen(line: string): Fence | null {
  const match = FENCE_OPEN.exec(line);
  if (!match) return null;
  const run = match[1] ?? "";
  const info = match[2] ?? "";
  if (run[0] === "`" && info.includes("`")) return null;
  return { marker: run[0] ?? "", length: run.length, info: info.trim() };
}

function closingFence(line: string, fence: Fence): boolean {
  const match = FENCE_CLOSE.exec(line);
  const run = match?.[1] ?? "";
  return Boolean(match) && run[0] === fence.marker && run.length >= fence.length;
}

function parseItem(line: string): ItemHead | null {
  const match = ITEM.exec(line);
  if (!match) return null;
  const indent = match[1] ?? "";
  const marker = match[2] ?? "";
  const text = match[3] ?? "";
  const ordered = /\d/.test(marker);
  return { indent: indent.length, ordered, start: ordered ? parseInt(marker, 10) : 1, text };
}

// A bullet, or an ordered item starting at 1, may interrupt a paragraph; an empty
// item may not. This follows CommonMark for the subset supported here.
function interruptsParagraph(line: string): boolean {
  if (isBlank(line) || fenceOpen(line) || HEADING.test(line) || QUOTE.test(line)) return true;
  const item = parseItem(line);
  return Boolean(item && item.text.trim() !== "" && (!item.ordered || item.start === 1));
}

function isStructural(line: string): boolean {
  return Boolean(fenceOpen(line) || HEADING.test(line) || QUOTE.test(line) || parseItem(line));
}

// A line continues an item when it is lazy text or is indented deeper than the
// item's own marker, so a sibling at the same indent starts a new item.
function continuesItem(line: string, parentIndent: number): boolean {
  if (isBlank(line) || fenceOpen(line) || HEADING.test(line) || QUOTE.test(line)) return false;
  const item = parseItem(line);
  return !item || item.indent > parentIndent + 1;
}

/* ---------------------------------------------------------------------------
 * Inline parsing
 * ------------------------------------------------------------------------- */

interface Link {
  readonly label: string;
  readonly url: string;
  readonly end: number;
}

interface Emphasis {
  readonly strong: boolean;
  readonly inner: string;
  readonly end: number;
}

function inlineNodes(text: string, depth: number): Inline[] {
  const nodes: Inline[] = [];
  let plain = "";
  const flush = (): void => {
    if (plain) nodes.push({ type: "text", value: plain });
    plain = "";
  };
  const look = lookahead(text);

  let i = 0;
  while (i < text.length) {
    const ch = text[i] ?? "";

    if (ch === "\\" && i + 1 < text.length && ASCII_PUNCTUATION.test(text[i + 1] ?? "")) {
      plain += text[i + 1] ?? "";
      i += 2;
      continue;
    }

    if (ch === "`") {
      const run = runLength(text, i, "`");
      const close = look.run(i + run, run);
      if (close !== -1) {
        flush();
        nodes.push({ type: "code", value: codeSpanText(text.slice(i + run, close)) });
        i = close + run;
      } else {
        plain += "`".repeat(run);
        i += run;
      }
      continue;
    }

    if (ch === "!" && text[i + 1] === "[") {
      const image = parseLink(text, i + 1, look);
      if (image) {
        // Images are not rendered; their source stays visible as text.
        plain += text.slice(i, image.end);
        i = image.end;
        continue;
      }
    }

    if (ch === "[") {
      const link = parseLink(text, i, look);
      if (link) {
        flush();
        const href = safeHref(link.url);
        if (href) nodes.push({ type: "link", href, children: [linkText(link.label || link.url)] });
        else plain += text.slice(i, link.end);
        i = link.end;
        continue;
      }
    }

    if ((ch === "*" || ch === "_") && depth < MAX_INLINE_DEPTH) {
      const emphasis = parseEmphasis(text, i, look);
      if (emphasis) {
        flush();
        const children = inlineNodes(emphasis.inner, depth + 1);
        nodes.push(emphasis.strong ? { type: "strong", children } : { type: "em", children });
        i = emphasis.end;
        continue;
      }
    }

    plain += ch;
    i++;
  }
  flush();
  return nodes;
}

// A link label is literal text: it is not parsed for emphasis or code, as in the
// legacy renderer.
function linkText(value: string): TextInline {
  return { type: "text", value };
}

// parseLink reads `[label](url)` starting at the "[" at `open`. A URL may not
// contain whitespace, so a tab or newline inside a scheme never forms a link.
function parseLink(text: string, open: number, look: Lookahead): Link | null {
  const labelEnd = look.next("]", open + 1, MAX_SPAN);
  if (labelEnd === -1 || text[labelEnd + 1] !== "(") return null;
  const urlEnd = look.next(")", labelEnd + 2, MAX_URL_CHARS);
  if (urlEnd === -1) return null;
  const raw = text.slice(labelEnd + 2, urlEnd).trim();
  if (/\s/.test(raw)) return null;
  return { label: text.slice(open + 1, labelEnd), url: raw, end: urlEnd + 1 };
}

function parseEmphasis(text: string, i: number, look: Lookahead): Emphasis | null {
  const mark = text[i] ?? "";
  const strong = text[i + 1] === mark;
  const delimiter = strong ? mark + mark : mark;
  const start = i + delimiter.length;
  if (start >= text.length || isSpace(text[start])) return null;
  if (mark === "_" && i > 0 && WORD.test(text[i - 1] ?? "")) return null;

  const close = look.closer(mark, strong, start);
  if (close === -1) return null;
  return { strong, inner: text.slice(start, close), end: close + delimiter.length };
}

// As in CommonMark, one space is removed from each side when both are present and
// the span is not all spaces, so `` `x` `` shows as `x`.
function codeSpanText(value: string): string {
  const padded = value.length > 2 && value.startsWith(" ") && value.endsWith(" ");
  return padded && value.trim() !== "" ? value.slice(1, -1) : value;
}

function runLength(text: string, i: number, mark: string): number {
  let j = i;
  while (j < text.length && text[j] === mark) j++;
  return j - i;
}

interface Lookahead {
  // next returns the first `mark` at or after `from` and within `span`, or -1.
  next(mark: string, from: number, span: number): number;
  // closer returns the first position after the opener at `start` where its
  // delimiter may close, when that closer ends within MAX_SPAN of the opener, or -1.
  closer(mark: string, strong: boolean, start: number): number;
  // run returns the start of the first backtick run of exactly `length` at or after
  // `from` and within MAX_SPAN, or -1. `from` always follows a whole run.
  run(from: number, length: number): number;
}

// lookahead answers the bounded forward searches of inline parsing. Each kind of
// position is indexed once per text, on first use, and each search is a binary
// search of that index. A window scan repeated at every delimiter cost up to
// MAX_SPAN steps per delimiter, so an unclosed run of delimiters cost the square of
// its length; this costs one linear index plus O(log n) per search. Each answer is
// the one the window scan would give.
function lookahead(text: string): Lookahead {
  const marks = new Map<string, number[]>();
  const closers = new Map<string, number[]>();
  let runs: Map<number, number[]> | null = null;
  return {
    next(mark, from, span) {
      let positions = marks.get(mark);
      if (!positions) {
        positions = positionsOf(text, mark);
        marks.set(mark, positions);
      }
      const j = firstAtOrAfter(positions, from);
      return j !== -1 && j < from + span ? j : -1;
    },
    closer(mark, strong, start) {
      const length = strong ? 2 : 1;
      const key = `${mark}${length}`;
      let positions = closers.get(key);
      if (!positions) {
        positions = closerPositions(text, mark, strong);
        closers.set(key, positions);
      }
      const j = firstAtOrAfter(positions, start + 1);
      return j !== -1 && j + length <= start + MAX_SPAN ? j : -1;
    },
    run(from, length) {
      if (!runs) runs = backtickRuns(text);
      const j = firstAtOrAfter(runs.get(length) ?? [], from);
      return j !== -1 && j < from + MAX_SPAN ? j : -1;
    },
  };
}

// closerPositions lists, in order, the positions where the delimiter of `mark`
// (doubled when strong) may close. Each test depends only on the text at that
// position, never on where a span opened, which is what lets one list serve every
// opener.
function closerPositions(text: string, mark: string, strong: boolean): number[] {
  scanWork.steps += text.length;
  const found: number[] = [];
  for (let j = 0; j < text.length; j++) {
    if (text[j] === mark && closesEmphasis(text, j, mark, strong)) found.push(j);
  }
  return found;
}

// closesEmphasis is called where `mark` occurs at j. It rejects a closer that does
// not end its run: a longer run of the same mark is not a closer.
function closesEmphasis(text: string, j: number, mark: string, strong: boolean): boolean {
  if (strong && text[j + 1] !== mark) return false;
  const before = text[j - 1];
  const after = text[j + (strong ? 2 : 1)];
  if (isSpace(before)) return false;
  if (strong ? after === mark : before === mark || after === mark) return false;
  if (mark === "_" && WORD.test(after ?? "")) return false;
  return true;
}

// positionsOf lists the positions of the character `mark`, in order.
function positionsOf(text: string, mark: string): number[] {
  scanWork.steps += text.length;
  const found: number[] = [];
  for (let j = 0; j < text.length; j++) {
    if (text[j] === mark) found.push(j);
  }
  return found;
}

// isSpace is /\s/ for one character, with a fast path for ASCII.
function isSpace(ch: string | undefined): boolean {
  if (ch === undefined) return false;
  const code = ch.charCodeAt(0);
  return code < 128 ? code === 32 || (code >= 9 && code <= 13) : /\s/.test(ch);
}

// backtickRuns maps each run length to the start of every backtick run of that length.
function backtickRuns(text: string): Map<number, number[]> {
  scanWork.steps += text.length;
  const runs = new Map<number, number[]>();
  for (let j = 0; j < text.length; ) {
    if (text[j] !== "`") {
      j += 1;
      continue;
    }
    const length = runLength(text, j, "`");
    const starts = runs.get(length);
    if (starts) starts.push(j);
    else runs.set(length, [j]);
    j += length;
  }
  return runs;
}

// firstAtOrAfter returns the first entry of a sorted list that is at or after
// `from`, or -1.
function firstAtOrAfter(sorted: readonly number[], from: number): number {
  let low = 0;
  let high = sorted.length;
  while (low < high) {
    scanWork.steps += 1;
    const middle = (low + high) >> 1;
    if ((sorted[middle] ?? 0) < from) low = middle + 1;
    else high = middle;
  }
  return low < sorted.length ? (sorted[low] ?? -1) : -1;
}
