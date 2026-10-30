// TEST AND DIAGNOSTIC ONLY. NOT FOR PRODUCTION USE.
//
// A pure serializer from the AST to HTML text. It walks the same tree as toDom.ts
// and writes the markup that mapping would create, with text and attribute values
// escaped and attributes in name order. The parity suite compares its output with
// the serialized DOM, so a mapping that drifts from this one fails the suite.

import { type Block, type ContentBlock, type Inline, assertNever } from "./ast";

export function blocksToHtml(blocks: readonly Block[]): string {
  return blocks.map(blockHtml).join("");
}

type Attribute = readonly [name: string, value: string];

function blockHtml(block: Block): string {
  switch (block.type) {
    case "paragraph":
      return tag("p", [], inlinesHtml(block.children));
    case "heading":
      return tag(`h${block.level}`, [], inlinesHtml(block.children));
    case "list": {
      const attrs: Attribute[] = block.ordered && block.start !== 1 ? [["start", String(block.start)]] : [];
      const items = block.items.map((item) =>
        tag("li", [], item.layout === "inline" ? inlinesHtml(item.children) : contentHtml(item.children)),
      );
      return tag(block.ordered ? "ol" : "ul", attrs, items.join(""));
    }
    case "blockquote":
      return tag("blockquote", [], contentHtml(block.children));
    case "code": {
      const label = block.language ? `Code block, ${block.language}` : "Code block";
      const codeAttrs: Attribute[] = block.language ? [["class", `language-${block.language}`]] : [];
      return tag(
        "pre",
        [
          ["class", "md-code"],
          ["role", "region"],
          ["tabindex", "0"],
          ["aria-label", label],
        ],
        tag("code", codeAttrs, escapeText(block.text)),
      );
    }
    case "notice":
      return tag("p", [["class", "md-truncated"]], escapeText(block.text));
    default:
      return assertNever(block, "blockHtml");
  }
}

function contentHtml(blocks: readonly ContentBlock[]): string {
  return blocks.map(blockHtml).join("");
}

function inlinesHtml(nodes: readonly Inline[]): string {
  return nodes.map(inlineHtml).join("");
}

function inlineHtml(node: Inline): string {
  switch (node.type) {
    case "text":
      return escapeText(node.value);
    case "code":
      return tag("code", [], escapeText(node.value));
    case "em":
      return tag("em", [], inlinesHtml(node.children));
    case "strong":
      return tag("strong", [], inlinesHtml(node.children));
    case "link":
      return tag(
        "a",
        [
          ["href", node.href],
          ["target", "_blank"],
          ["rel", "noopener noreferrer nofollow"],
        ],
        node.children.map((child) => escapeText(child.value)).join(""),
      );
    default:
      return assertNever(node, "inlineHtml");
  }
}

function tag(name: string, attrs: readonly Attribute[], inner: string): string {
  const sorted = [...attrs].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  const rendered = sorted.map(([key, value]) => ` ${key}="${escapeAttribute(value)}"`).join("");
  return `<${name}${rendered}>${inner}</${name}>`;
}

function escapeText(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function escapeAttribute(value: string): string {
  return escapeText(value).replace(/"/g, "&quot;");
}
