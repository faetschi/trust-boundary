// markdown.mjs — a small, dependency-free markdown subset for assistant text.
//
// Output is DOM built only with createElement and createTextNode. Source text is
// never parsed as HTML: raw markup is literal text, and a link is created only
// for an allowed scheme (http, https, mailto). An image renders as its source
// text. Supported: paragraphs, ATX headings, fenced code, inline code, bold and
// italic, bulleted and ordered lists, blockquotes and [text](url) links.
//
// Streaming: splitBlocks divides text into top-level blocks at blank lines,
// headings and fences (a blank line inside an open fence does not end it).
// createIncrementalRenderer keeps the DOM of each leading block whose source is
// unchanged, so a growing message re-renders only its tail.

export const MAX_MARKDOWN_CHARS = 65536;
export const MAX_BLOCKS = 512;

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

/* ---------------------------------------------------------------------------
 * Block splitting (streaming frontier)
 * ------------------------------------------------------------------------- */

// splitBlocks returns the top-level blocks of `text`, each as its source lines
// joined by "\n" with no surrounding blank lines. Only a block that grows can
// change when text is appended, so a stream re-renders its tail.
export function splitBlocks(text) {
  const lines = String(text).replace(/\r\n?/g, "\n").split("\n");
  if (lines[lines.length - 1] === "") lines.pop();

  const blocks = [];
  let current = [];
  let fence = null;
  const flush = () => {
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
 * Incremental renderer
 * ------------------------------------------------------------------------- */

// createIncrementalRenderer owns `container` and fills it with the rendered
// blocks. update(text) keeps the DOM of every leading block whose source is
// unchanged, so a growing message only re-renders its tail. A text that does not
// extend the previous text is rendered from scratch. The returned counts say
// how many leading blocks were reused and how many were rendered.
export function createIncrementalRenderer(container) {
  let source = "";
  let entries = [];
  let notice = null;

  return {
    update(text) {
      const next = text === undefined || text === null ? "" : String(text);
      const bounded = boundText(next);
      const all = splitBlocks(bounded.text);
      const blocks = all.slice(0, MAX_BLOCKS);

      let keep = 0;
      if (next.startsWith(source)) {
        while (keep < blocks.length && keep < entries.length && entries[keep].source === blocks[keep]) keep++;
      }

      if (notice) {
        container.removeChild(notice);
        notice = null;
      }
      for (let i = entries.length - 1; i >= keep; i--) {
        for (const node of entries[i].nodes) container.removeChild(node);
      }
      entries = entries.slice(0, keep);

      for (let i = keep; i < blocks.length; i++) {
        const nodes = renderLines(blocks[i].split("\n"), 0);
        for (const node of nodes) container.appendChild(node);
        entries.push({ source: blocks[i], nodes });
      }

      const message = truncationMessage(bounded, all.length);
      if (message) {
        notice = el("p", "md-truncated", message);
        container.appendChild(notice);
      }
      source = next;
      return { reused: keep, rendered: blocks.length - keep };
    },
  };
}

function boundText(value) {
  if (value.length <= MAX_MARKDOWN_CHARS) return { text: value, total: value.length, truncated: false };
  let end = MAX_MARKDOWN_CHARS;
  const last = value.charCodeAt(end - 1);
  if (last >= 0xd800 && last <= 0xdbff) end -= 1; // never keep half a surrogate pair
  return { text: value.slice(0, end), total: value.length, truncated: true };
}

function truncationMessage(bounded, blockCount) {
  const parts = [];
  if (bounded.truncated) {
    parts.push(`the first ${bounded.text.length} of ${bounded.total} characters are shown`);
  }
  if (blockCount > MAX_BLOCKS) {
    parts.push(`the first ${MAX_BLOCKS} of ${blockCount} blocks are shown`);
  }
  return parts.length ? `Message truncated for display: ${parts.join("; ")}.` : "";
}

/* ---------------------------------------------------------------------------
 * Links
 * ------------------------------------------------------------------------- */

// safeHref returns the normalised URL for an allowed link, or null. Browsers
// drop ASCII tab and newline anywhere in a URL and ignore leading and trailing
// control characters and spaces, so the same cleaning runs before the scheme is
// read. A relative or protocol-relative URL is not a link here.
export function safeHref(raw) {
  if (typeof raw !== "string") return null;
  const cleaned = raw.replace(/[\t\n\r]/g, "").replace(/^[\u0000- ]+|[\u0000- ]+$/g, "");
  if (!cleaned || cleaned.length > MAX_URL_CHARS) return null;
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(cleaned)) return null;
  let parsed;
  try {
    parsed = new URL(cleaned);
  } catch {
    return null;
  }
  return LINK_SCHEMES.has(parsed.protocol) ? parsed.href : null;
}

function anchor(href, label) {
  const node = el("a", null, label);
  node.setAttribute("href", href);
  node.setAttribute("target", "_blank");
  node.setAttribute("rel", "noopener noreferrer nofollow");
  return node;
}

/* ---------------------------------------------------------------------------
 * Block rendering
 * ------------------------------------------------------------------------- */

// renderLines turns the lines of one block (or of one nested container) into
// DOM nodes. Nesting is bounded by MAX_DEPTH through nestedNodes.
function renderLines(lines, depth) {
  const nodes = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (isBlank(line)) {
      i++;
      continue;
    }
    const open = fenceOpen(line);
    if (open) {
      let j = i + 1;
      while (j < lines.length && !closingFence(lines[j], open)) j++;
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
      const inner = [];
      let j = i;
      while (j < lines.length && QUOTE.test(lines[j])) inner.push(lines[j++].replace(QUOTE_PREFIX, ""));
      nodes.push(quoteNode(inner, depth));
      i = j;
      continue;
    }
    if (parseItem(line)) {
      const list = listNode(lines, i, depth);
      nodes.push(list.node);
      i = list.next;
      continue;
    }
    let j = i + 1;
    while (j < lines.length && !interruptsParagraph(lines[j])) j++;
    nodes.push(paragraphNode(lines.slice(i, j).join("\n")));
    i = j;
  }
  return nodes;
}

function nestedNodes(lines, depth) {
  if (depth < MAX_DEPTH) return renderLines(lines, depth + 1);
  // Past the depth bound the structure is flattened to a paragraph of text.
  const text = lines.join("\n").trim();
  return text ? [paragraphNode(text)] : [];
}

function paragraphNode(text) {
  const node = el("p");
  appendAll(node, inlineNodes(text, 0));
  return node;
}

function headingNode(match) {
  const level = match[1].length;
  const text = (match[2] || "").replace(/(^|[ \t]+)#+$/, "").trim();
  // Message headings sit below the page's own headings: # becomes h3.
  const node = el(`h${Math.min(level + 2, 6)}`);
  appendAll(node, inlineNodes(text, 0));
  return node;
}

function quoteNode(inner, depth) {
  const node = el("blockquote");
  appendAll(node, nestedNodes(inner, depth));
  return node;
}

// listNode consumes consecutive items of one kind. An indented or lazy line
// belongs to the item above it, and a nested list is rendered inside that item.
// The first item is always consumed, so the caller always makes progress.
function listNode(lines, start, depth) {
  const first = parseItem(lines[start]);
  const list = el(first.ordered ? "ol" : "ul");
  if (first.ordered && first.start !== 1) list.setAttribute("start", String(first.start));

  let i = start;
  let item = first;
  while (item && item.ordered === first.ordered) {
    const body = [item.text];
    i++;
    while (i < lines.length && continuesItem(lines[i], item.indent)) body.push(lines[i++].replace(/^ {1,4}/, ""));
    const li = el("li");
    // A tight item holds its text directly; only nested structure gets blocks.
    if (body.some(isStructural)) appendAll(li, nestedNodes(body, depth));
    else appendAll(li, inlineNodes(body.join("\n"), 0));
    list.appendChild(li);
    item = i < lines.length ? parseItem(lines[i]) : null;
  }
  return { node: list, next: i };
}

function codeBlock(lines, info) {
  const language = languageOf(info);
  const pre = el("pre", "md-code");
  // The scroll region is keyboard-reachable and labelled, so a long line can be
  // read without a pointer.
  pre.setAttribute("role", "region");
  pre.setAttribute("tabindex", "0");
  pre.setAttribute("aria-label", language ? `Code block, ${language}` : "Code block");
  pre.appendChild(el("code", language ? `language-${language}` : null, lines.join("\n")));
  return pre;
}

function languageOf(info) {
  const word = info.split(/\s+/)[0] || "";
  return LANGUAGE.test(word) ? word : "";
}

/* ---------------------------------------------------------------------------
 * Line classification
 * ------------------------------------------------------------------------- */

function isBlank(line) {
  return line.trim() === "";
}

function fenceOpen(line) {
  const match = FENCE_OPEN.exec(line);
  if (!match) return null;
  const [, run, info] = match;
  if (run[0] === "`" && info.includes("`")) return null;
  return { marker: run[0], length: run.length, info: info.trim() };
}

function closingFence(line, fence) {
  const match = FENCE_CLOSE.exec(line);
  return Boolean(match) && match[1][0] === fence.marker && match[1].length >= fence.length;
}

function parseItem(line) {
  const match = ITEM.exec(line);
  if (!match) return null;
  const [, indent, marker, text = ""] = match;
  const ordered = /\d/.test(marker);
  return { indent: indent.length, ordered, start: ordered ? parseInt(marker, 10) : 1, text };
}

// A bullet, or an ordered item starting at 1, may interrupt a paragraph; an
// empty item may not. This follows CommonMark for the subset supported here.
function interruptsParagraph(line) {
  if (isBlank(line) || fenceOpen(line) || HEADING.test(line) || QUOTE.test(line)) return true;
  const item = parseItem(line);
  return Boolean(item && item.text.trim() !== "" && (!item.ordered || item.start === 1));
}

function isStructural(line) {
  return Boolean(fenceOpen(line) || HEADING.test(line) || QUOTE.test(line) || parseItem(line));
}

// A line continues an item when it is lazy text or is indented deeper than the
// item's own marker, so a sibling at the same indent starts a new item.
function continuesItem(line, parentIndent) {
  if (isBlank(line) || fenceOpen(line) || HEADING.test(line) || QUOTE.test(line)) return false;
  const item = parseItem(line);
  return !item || item.indent > parentIndent + 1;
}

/* ---------------------------------------------------------------------------
 * Inline rendering
 * ------------------------------------------------------------------------- */

function inlineNodes(text, depth) {
  const nodes = [];
  let plain = "";
  const flush = () => {
    if (plain) nodes.push(document.createTextNode(plain));
    plain = "";
  };

  let i = 0;
  while (i < text.length) {
    const ch = text[i];

    if (ch === "\\" && i + 1 < text.length && ASCII_PUNCTUATION.test(text[i + 1])) {
      plain += text[i + 1];
      i += 2;
      continue;
    }

    if (ch === "`") {
      const run = runLength(text, i, "`");
      const close = findRun(text, i + run, "`", run);
      if (close !== -1) {
        flush();
        nodes.push(el("code", null, codeSpanText(text.slice(i + run, close))));
        i = close + run;
      } else {
        plain += "`".repeat(run);
        i += run;
      }
      continue;
    }

    if (ch === "!" && text[i + 1] === "[") {
      const image = parseLink(text, i + 1);
      if (image) {
        // Images are not rendered; their source stays visible as text.
        plain += text.slice(i, image.end);
        i = image.end;
        continue;
      }
    }

    if (ch === "[") {
      const link = parseLink(text, i);
      if (link) {
        flush();
        const href = safeHref(link.url);
        if (href) nodes.push(anchor(href, link.label || link.url));
        else plain += text.slice(i, link.end);
        i = link.end;
        continue;
      }
    }

    if ((ch === "*" || ch === "_") && depth < MAX_INLINE_DEPTH) {
      const emphasis = parseEmphasis(text, i);
      if (emphasis) {
        flush();
        const node = el(emphasis.strong ? "strong" : "em");
        appendAll(node, inlineNodes(emphasis.inner, depth + 1));
        nodes.push(node);
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

// parseLink reads `[label](url)` starting at the "[" at `open`. A URL may not
// contain whitespace, so a tab or newline inside a scheme never forms a link.
function parseLink(text, open) {
  const labelEnd = findChar(text, "]", open + 1, MAX_SPAN);
  if (labelEnd === -1 || text[labelEnd + 1] !== "(") return null;
  const urlEnd = findChar(text, ")", labelEnd + 2, MAX_URL_CHARS);
  if (urlEnd === -1) return null;
  const raw = text.slice(labelEnd + 2, urlEnd).trim();
  if (/\s/.test(raw)) return null;
  return { label: text.slice(open + 1, labelEnd), url: raw, end: urlEnd + 1 };
}

function parseEmphasis(text, i) {
  const mark = text[i];
  const strong = text[i + 1] === mark;
  const delimiter = strong ? mark + mark : mark;
  const start = i + delimiter.length;
  if (start >= text.length || /\s/.test(text[start])) return null;
  if (mark === "_" && i > 0 && WORD.test(text[i - 1])) return null;

  const limit = Math.min(text.length, start + MAX_SPAN);
  for (let j = start + 1; j + delimiter.length <= limit; j++) {
    if (!text.startsWith(delimiter, j)) continue;
    const before = text[j - 1];
    const after = text[j + delimiter.length];
    if (/\s/.test(before)) continue;
    // The closer must end its run: a longer run of the same mark is not a closer.
    if (strong ? after === mark : before === mark || after === mark) continue;
    if (mark === "_" && WORD.test(after || "")) continue;
    return { strong, inner: text.slice(start, j), end: j + delimiter.length };
  }
  return null;
}

// As in CommonMark, one space is removed from each side when both are present
// and the span is not all spaces, so `` `x` `` shows as `x`.
function codeSpanText(value) {
  const padded = value.length > 2 && value.startsWith(" ") && value.endsWith(" ");
  return padded && value.trim() !== "" ? value.slice(1, -1) : value;
}

function runLength(text, i, mark) {
  let j = i;
  while (j < text.length && text[j] === mark) j++;
  return j - i;
}

// findRun returns the index of the next run of exactly `length` marks at or
// after `from`, within the look-ahead window, or -1.
function findRun(text, from, mark, length) {
  const limit = Math.min(text.length, from + MAX_SPAN);
  for (let j = from; j < limit; j++) {
    if (text[j] !== mark) continue;
    const run = runLength(text, j, mark);
    if (run === length) return j;
    j += run - 1;
  }
  return -1;
}

function findChar(text, mark, from, span) {
  const limit = Math.min(text.length, from + span);
  for (let j = from; j < limit; j++) {
    if (text[j] === mark) return j;
  }
  return -1;
}

/* ---------------------------------------------------------------------------
 * DOM helpers
 * ------------------------------------------------------------------------- */

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text) node.appendChild(document.createTextNode(text));
  return node;
}

function appendAll(parent, nodes) {
  for (const node of nodes) parent.appendChild(node);
}
