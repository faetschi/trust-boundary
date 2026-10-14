import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { DomElement, DomText, installDom } from "./dom.mjs";

installDom();
const { MAX_BLOCKS, MAX_MARKDOWN_CHARS, createIncrementalRenderer, safeHref, splitBlocks } = await import(
  "../assets/markdown.mjs"
);

const FORBIDDEN_TAGS = new Set(["script", "img", "iframe", "style", "svg", "object", "embed", "form", "input", "link", "meta"]);

function fresh(text) {
  const container = document.createElement("div");
  createIncrementalRenderer(container).update(text);
  return container;
}

function allNodes(root) {
  const out = [];
  const visit = (node) => {
    for (const child of node.children) {
      out.push(child);
      visit(child);
    }
  };
  visit(root);
  return out;
}

function elementsOf(root) {
  return allNodes(root).filter((node) => node instanceof DomElement);
}

function tagsOf(root) {
  return elementsOf(root).map((node) => node.tagName.toLowerCase());
}

// serialize is a structural fingerprint: tags, classes, attributes and text. Two
// containers with equal fingerprints render the same DOM.
function serialize(node) {
  if (node instanceof DomText) return JSON.stringify(node.data);
  const tag = node.tagName.toLowerCase();
  const cls = node.className ? ` class=${JSON.stringify(node.className)}` : "";
  const attrs = Object.keys(node.attributes)
    .sort()
    .map((key) => ` ${key}=${JSON.stringify(node.attributes[key])}`)
    .join("");
  return `<${tag}${cls}${attrs}>${node.children.map(serialize).join("")}</${tag}>`;
}

function serializeChildren(container) {
  return container.children.map(serialize).join("");
}

// The hostile corpus. Every entry must render as literal text (textContent equal
// to the payload) and must never produce a script, image, frame or style element
// or a link to a scheme other than http, https or mailto.
const HOSTILE = [
  ["script tag", "<script>alert(1)</script>"],
  ["img with onerror", '<img src=x onerror="alert(1)">'],
  ["iframe", '<iframe src="https://evil.example"></iframe>'],
  ["style block", "<style>body{display:none}</style>"],
  ["event handler attribute", '<div onmouseover="alert(1)">hover</div>'],
  ["raw anchor with javascript href", '<a href="javascript:alert(1)">x</a>'],
  ["javascript link", "[click](javascript:alert(1))"],
  ["mixed-case javascript scheme", "[click](JaVaScRiPt:alert(1))"],
  ["tab inside the scheme", "[click](java\tscript:alert(1))"],
  ["newline inside the scheme", "[click](java\nscript:alert(1))"],
  ["data url", "[click](data:text/html,<script>alert(1)</script>)"],
  ["vbscript link", "[click](vbscript:msgbox(1))"],
  ["file link", "[click](file:///etc/passwd)"],
  ["protocol-relative link", "[click](//evil.example/x)"],
  ["relative link", "[click](/settings)"],
  ["image with a script source", "![x](javascript:alert(1))"],
  ["markdown image", "![alt](https://example.com/p.png)"],
  ["unclosed link", "[docs](https://exam"],
];

for (const [name, payload] of HOSTILE) {
  test(`hostile payload renders as literal text: ${name}`, () => {
    const root = fresh(payload);
    const tags = tagsOf(root);
    for (const tag of tags) assert.ok(!FORBIDDEN_TAGS.has(tag), `created <${tag}>`);
    assert.equal(tags.filter((tag) => tag === "a").length, 0, "no link may be created");
    assert.equal(root.textContent, payload);
  });
}

test("no script, image, frame or style element is ever created", () => {
  const created = [];
  const original = document.createElement;
  document.createElement = function createElement(tag) {
    created.push(String(tag).toLowerCase());
    return original.call(this, tag);
  };
  try {
    for (const [, payload] of HOSTILE) fresh(payload);
  } finally {
    document.createElement = original;
  }
  assert.ok(created.length > 0);
  for (const tag of created) assert.ok(!FORBIDDEN_TAGS.has(tag), `created <${tag}>`);
});

test("random markup-shaped input never creates a forbidden element or a non-allowlisted link", () => {
  // A deterministic generator keeps the property test reproducible.
  let seed = 20261014;
  const next = () => {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    return seed;
  };
  const alphabet = ["<", ">", "[", "]", "(", ")", "!", "*", "_", "`", "~", "#", "-", "+", "1.", " ", "\n", "\t", "\\", "javascript:", "https://", "mailto:", "a", "b", "img", "script", "\""];
  for (let round = 0; round < 300; round++) {
    let input = "";
    const length = 1 + (next() % 120);
    for (let i = 0; i < length; i++) input += alphabet[next() % alphabet.length];
    const root = fresh(input);
    for (const node of elementsOf(root)) {
      const tag = node.tagName.toLowerCase();
      assert.ok(!FORBIDDEN_TAGS.has(tag), `created <${tag}> for ${JSON.stringify(input)}`);
      if (tag === "a") {
        assert.match(node.getAttribute("href"), /^(https?:|mailto:)/, JSON.stringify(input));
      }
    }
  }
});

test("the markdown module contains no HTML parsing or markup sinks", () => {
  const source = readFileSync(new URL("../assets/markdown.mjs", import.meta.url), "utf8");
  for (const sink of ["innerHTML", "outerHTML", "insertAdjacentHTML", "DOMParser", "createContextualFragment"]) {
    assert.ok(!source.includes(sink), `markdown.mjs must not use ${sink}`);
  }
  assert.ok(!/\beval\s*\(|new Function\s*\(/.test(source), "markdown.mjs must not evaluate strings");
});

/* ---------------------------------------------------------------------------
 * Links
 * ------------------------------------------------------------------------- */

test("safeHref allows only http, https and mailto, after cleaning", () => {
  assert.equal(safeHref("https://example.com/a"), "https://example.com/a");
  assert.equal(safeHref("HTTP://Example.com"), "http://example.com/");
  assert.equal(safeHref("  https://example.com  "), "https://example.com/");
  assert.equal(safeHref("mailto:team@example.com"), "mailto:team@example.com");
  for (const bad of [
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    " java\tscript:alert(1)",
    "java\nscript:x",
    "data:text/html,x",
    "vbscript:x",
    "file:///etc/passwd",
    "ftp://example.com",
    "//evil.example",
    "/relative",
    "relative/path",
    "https://a b",
    "",
  ]) {
    assert.equal(safeHref(bad), null, JSON.stringify(bad));
  }
});

test("an allowed link is an anchor with the hardened attributes", () => {
  const root = fresh("[docs](https://example.com/docs) and [mail](mailto:a@example.com)");
  const anchors = elementsOf(root).filter((node) => node.tagName === "A");
  assert.equal(anchors.length, 2);
  assert.equal(anchors[0].getAttribute("href"), "https://example.com/docs");
  assert.equal(anchors[0].getAttribute("target"), "_blank");
  assert.equal(anchors[0].getAttribute("rel"), "noopener noreferrer nofollow");
  assert.equal(anchors[0].textContent, "docs");
  assert.equal(anchors[1].getAttribute("href"), "mailto:a@example.com");
});

test("an image is literal source text, never an image element", () => {
  const root = fresh("![diagram](https://example.com/d.png)");
  assert.equal(tagsOf(root).includes("img"), false);
  assert.equal(root.textContent, "![diagram](https://example.com/d.png)");
});

/* ---------------------------------------------------------------------------
 * Blocks and fences
 * ------------------------------------------------------------------------- */

test("splitBlocks divides at blank lines, headings and fences", () => {
  assert.deepEqual(splitBlocks("a\n\nb\n# H\nc"), ["a", "b", "# H", "c"]);
  assert.deepEqual(splitBlocks("```\nx\n```\ny"), ["```\nx\n```", "y"]);
  assert.deepEqual(splitBlocks(""), []);
});

test("a blank line inside an unclosed fence does not end the block", () => {
  assert.deepEqual(splitBlocks("```js\nlet a = 1;\n\nlet b"), ["```js\nlet a = 1;\n\nlet b"]);
});

test("a fence closes only on a fence of the same marker and at least the same length", () => {
  assert.deepEqual(splitBlocks("````\n```\nstill code\n````\nafter"), ["````\n```\nstill code\n````", "after"]);
  assert.deepEqual(splitBlocks("~~~\n```\n~~~"), ["~~~\n```\n~~~"]);
});

test("a fenced block keeps its blank lines and its language", () => {
  const root = fresh("```js\nconst a = 1;\n\nconst b = 2;\n```\n\nafter");
  assert.deepEqual(tagsOf(root), ["pre", "code", "p"]);
  const pre = root.children[0];
  const code = pre.children[0];
  assert.equal(code.textContent, "const a = 1;\n\nconst b = 2;");
  assert.equal(code.className, "language-js");
  assert.equal(root.children[1].textContent, "after");
});

test("a long code block is a labelled, focusable scroll region", () => {
  const root = fresh("```js\nconst x = 1;\n```");
  const pre = root.children[0];
  assert.equal(pre.tagName, "PRE");
  assert.equal(pre.getAttribute("tabindex"), "0");
  assert.equal(pre.getAttribute("role"), "region");
  assert.equal(pre.getAttribute("aria-label"), "Code block, js");
  assert.equal(fresh("```\nx\n```").children[0].getAttribute("aria-label"), "Code block");
});

test("an unclosed fence streams as a code block with the content so far", () => {
  const root = fresh("```py\nprint(1)\n\nprint(2)");
  assert.deepEqual(tagsOf(root), ["pre", "code"]);
  assert.equal(root.children[0].textContent, "print(1)\n\nprint(2)");
});

test("markdown and markup inside a fence stay literal", () => {
  const root = fresh("```\n**not bold** [x](https://a.example) <b>tag</b>\n```");
  assert.deepEqual(tagsOf(root), ["pre", "code"]);
  assert.equal(root.textContent, "**not bold** [x](https://a.example) <b>tag</b>");
});

test("headings render at h3 and deeper, never as page-level h1", () => {
  assert.deepEqual(tagsOf(fresh("# One")), ["h3"]);
  assert.deepEqual(tagsOf(fresh("## Two")), ["h4"]);
  assert.equal(fresh("## Two").textContent, "Two");
  assert.equal(tagsOf(fresh("#hashtag")).includes("h3"), false, "no space after the hash is not a heading");
});

/* ---------------------------------------------------------------------------
 * Inline and lists
 * ------------------------------------------------------------------------- */

test("inline emphasis and code spans", () => {
  const root = fresh("Use **bold**, *em*, `a*b*c`, and ~~not strike~~.");
  const tags = tagsOf(root);
  assert.ok(tags.includes("strong"));
  assert.ok(tags.includes("em"));
  assert.ok(tags.includes("code"));
  assert.equal(elementsOf(root).find((node) => node.tagName === "CODE").textContent, "a*b*c");
  assert.equal(root.textContent, "Use bold, em, a*b*c, and ~~not strike~~.");
});

test("a code span drops one padding space from each side, as CommonMark does", () => {
  assert.equal(fresh("`` `x` ``").textContent, "`x`");
  assert.equal(fresh("`  `").textContent, "  ", "an all-space span keeps its spaces");
});

test("intraword underscores and escaped delimiters are literal", () => {
  assert.equal(tagsOf(fresh("snake_case_name stays")).includes("em"), false);
  const escaped = fresh("\\*literal\\*");
  assert.equal(tagsOf(escaped).includes("em"), false);
  assert.equal(escaped.textContent, "*literal*");
});

test("bulleted and ordered lists, with the ordered start preserved", () => {
  const bullets = fresh("- one\n- two");
  assert.deepEqual(tagsOf(bullets), ["ul", "li", "li"]);
  assert.equal(bullets.children[0].children[1].textContent, "two");

  const ordered = fresh("3. three\n4. four");
  assert.equal(ordered.children[0].tagName, "OL");
  assert.equal(ordered.children[0].getAttribute("start"), "3");
  assert.equal(ordered.children[0].children.length, 2);
});

test("a nested list sits inside its item", () => {
  const root = fresh("- a\n  - b\n- c");
  const list = root.children[0];
  assert.equal(list.children.length, 2, "two top-level items");
  assert.ok(list.children[0].children.some((node) => node.tagName === "UL"), "item a holds the nested list");
});

test("a block that starts with an indented item terminates and renders the list", () => {
  const root = fresh("  - indented\n  - second");
  assert.deepEqual(tagsOf(root), ["ul", "li", "li"]);
  assert.equal(root.textContent, "indentedsecond");
});

test("a lazy line after an item continues that item", () => {
  const root = fresh("- first\nlazy text");
  assert.deepEqual(tagsOf(root), ["ul", "li"]);
  assert.equal(root.textContent, "first\nlazy text");
});

test("a blockquote holds its own paragraphs", () => {
  const root = fresh("> quoted\n> more");
  assert.deepEqual(tagsOf(root), ["blockquote", "p"]);
  assert.equal(root.textContent, "quoted\nmore");
});

test("nested blockquotes are bounded by depth", () => {
  const root = fresh(`${"> ".repeat(20)}deep`);
  let depth = 0;
  let node = root.children[0];
  while (node && node.tagName === "BLOCKQUOTE") {
    depth++;
    node = node.children.find((child) => child instanceof DomElement) || null;
  }
  assert.ok(depth >= 1 && depth <= 6, `nesting depth ${depth}`);
  assert.match(root.textContent, /deep/);
});

test("a soft line break inside a paragraph stays part of the paragraph", () => {
  const root = fresh("line one\nline two");
  assert.deepEqual(tagsOf(root), ["p"]);
  assert.equal(root.textContent, "line one\nline two");
});

/* ---------------------------------------------------------------------------
 * Streaming: stable-prefix incremental rendering
 * ------------------------------------------------------------------------- */

const STREAM =
  "# Plan\n\nFirst **point** with `code`.\n\n- one\n- two\n\n```js\nconst a = 1;\n\nconst b = [a];\n```\n\n> quoted line\n> second\n\nTail with [link](https://example.com/x) and *emphasis*.";

test("every streamed prefix renders the same DOM as a fresh render of that prefix", () => {
  const container = document.createElement("div");
  const renderer = createIncrementalRenderer(container);
  for (let end = 1; end <= STREAM.length; end++) {
    renderer.update(STREAM.slice(0, end));
    assert.equal(serializeChildren(container), serializeChildren(fresh(STREAM.slice(0, end))), `prefix ${end}`);
  }
});

test("completed leading blocks keep their DOM node while the tail streams", () => {
  const container = document.createElement("div");
  const renderer = createIncrementalRenderer(container);
  renderer.update("# Title\n\nFirst paragraph.\n\nSecond");
  const [heading, first, second] = container.children;

  const grown = renderer.update("# Title\n\nFirst paragraph.\n\nSecond paragraph grows.");
  const tail = container.children[2];
  assert.equal(container.children[0], heading, "heading node is untouched");
  assert.equal(container.children[1], first, "completed paragraph node is untouched");
  assert.notEqual(tail, second, "the growing tail is re-rendered");
  assert.deepEqual(grown, { reused: 2, rendered: 1 });

  const extended = renderer.update("# Title\n\nFirst paragraph.\n\nSecond paragraph grows.\n\nNew block");
  assert.equal(container.children[0], heading);
  assert.equal(container.children[1], first);
  assert.equal(container.children[2], tail, "a block that has been closed by a blank line is kept");
  assert.equal(container.children.length, 4);
  assert.deepEqual(extended, { reused: 3, rendered: 1 });
});

test("a text that does not extend the previous text fully re-renders", () => {
  const container = document.createElement("div");
  const renderer = createIncrementalRenderer(container);
  renderer.update("# Old\n\nkept?");
  const before = [...container.children];
  const stats = renderer.update("# New\n\nkept?");
  assert.deepEqual(stats, { reused: 0, rendered: 2 });
  for (const node of before) assert.ok(!container.children.includes(node), "no stale node may remain");
  assert.equal(serializeChildren(container), serializeChildren(fresh("# New\n\nkept?")));
});

test("shrinking the text never leaves stale DOM behind", () => {
  const container = document.createElement("div");
  const renderer = createIncrementalRenderer(container);
  renderer.update("alpha\n\nbeta\n\ngamma");
  renderer.update("alpha");
  assert.equal(container.children.length, 1);
  assert.equal(serializeChildren(container), serializeChildren(fresh("alpha")));
});

/* ---------------------------------------------------------------------------
 * Bounds
 * ------------------------------------------------------------------------- */

test("input beyond the character bound is truncated with a visible marker", () => {
  const container = fresh("x".repeat(MAX_MARKDOWN_CHARS + 500));
  assert.equal(container.children[0].textContent.length, MAX_MARKDOWN_CHARS);
  const marker = container.children[container.children.length - 1];
  assert.equal(marker.className, "md-truncated");
  assert.match(marker.textContent, /truncated/i);
});

test("input at the character bound carries no marker", () => {
  const container = fresh("x".repeat(MAX_MARKDOWN_CHARS));
  assert.equal(container.children.length, 1);
  assert.equal(container.children[0].textContent.length, MAX_MARKDOWN_CHARS);
});

test("a surrogate pair is never split at the truncation point", () => {
  const container = fresh(`${"a".repeat(MAX_MARKDOWN_CHARS - 1)}😀b`);
  const text = container.children[0].textContent;
  assert.ok(!/[\uD800-\uDBFF](?![\uDC00-\uDFFF])/.test(text), "no lone high surrogate");
  assert.ok(!/(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(text), "no lone low surrogate");
});

test("block count is bounded with a visible marker", () => {
  const text = Array.from({ length: MAX_BLOCKS + 5 }, (_, index) => `para ${index}`).join("\n\n");
  const container = fresh(text);
  const paragraphs = container.children.filter((node) => node.tagName === "P" && node.className !== "md-truncated");
  assert.equal(paragraphs.length, MAX_BLOCKS);
  assert.match(container.children[container.children.length - 1].textContent, /truncated/i);
});

test("pathological delimiter runs stay fast", () => {
  const started = Date.now();
  fresh(`${"[".repeat(30000)}${"*a ".repeat(10000)}${"`".repeat(5000)}${"_b".repeat(5000)}`);
  assert.ok(Date.now() - started < 2000, "rendering must stay linear-ish");
});
