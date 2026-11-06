// TEST AND DIAGNOSTIC ONLY. NOT FOR PRODUCTION USE.
//
// The app never builds the DOM from the AST. The Phase 3 views map each node to a
// React element. This module is the diagnostic mapping that the parity suite uses to
// compare the AST with the legacy DOM. It creates nodes with createElement and
// createTextNode, sets attributes with setAttribute, and never parses markup.

import { type Block, type ContentBlock, type Inline, assertNever } from "./ast";

interface DomText {
  readonly nodeType: 3;
  readonly data: string;
}

interface DomElement {
  readonly nodeType: 1;
  className: string;
  setAttribute(name: string, value: string): void;
  appendChild(child: DomChild): DomChild;
}

export type DomChild = DomElement | DomText;

export interface DomDocument {
  createElement(tagName: string): DomElement;
  createTextNode(data: string): DomText;
}

export function blocksToDom(doc: DomDocument, blocks: readonly Block[]): DomChild[] {
  return blocks.map((block) => blockToDom(doc, block));
}

function blockToDom(doc: DomDocument, block: Block): DomElement {
  switch (block.type) {
    case "paragraph":
      return element(doc, "p", null, inlinesToDom(doc, block.children));
    case "heading":
      return element(doc, `h${block.level}`, null, inlinesToDom(doc, block.children));
    case "list": {
      const list = element(doc, block.ordered ? "ol" : "ul", null, []);
      if (block.ordered && block.start !== 1) list.setAttribute("start", String(block.start));
      for (const item of block.items) {
        const children = item.layout === "inline" ? inlinesToDom(doc, item.children) : contentToDom(doc, item.children);
        list.appendChild(element(doc, "li", null, children));
      }
      return list;
    }
    case "blockquote":
      return element(doc, "blockquote", null, contentToDom(doc, block.children));
    case "code": {
      // The scroll region is keyboard-reachable and labelled, so a long line can be
      // read without a pointer.
      const pre = element(doc, "pre", "md-code", []);
      pre.setAttribute("role", "region");
      pre.setAttribute("tabindex", "0");
      pre.setAttribute("aria-label", block.language ? `Code block, ${block.language}` : "Code block");
      const code = element(doc, "code", block.language ? `language-${block.language}` : null, textChildren(doc, block.text));
      pre.appendChild(code);
      return pre;
    }
    case "notice":
      return element(doc, "p", "md-truncated", textChildren(doc, block.text));
    default:
      return assertNever(block, "blockToDom");
  }
}

function contentToDom(doc: DomDocument, blocks: readonly ContentBlock[]): DomChild[] {
  return blocks.map((block) => blockToDom(doc, block));
}

function inlinesToDom(doc: DomDocument, nodes: readonly Inline[]): DomChild[] {
  return nodes.map((node) => inlineToDom(doc, node));
}

function inlineToDom(doc: DomDocument, node: Inline): DomChild {
  switch (node.type) {
    case "text":
      return doc.createTextNode(node.value);
    case "code":
      return element(doc, "code", null, textChildren(doc, node.value));
    case "em":
      return element(doc, "em", null, inlinesToDom(doc, node.children));
    case "strong":
      return element(doc, "strong", null, inlinesToDom(doc, node.children));
    case "link": {
      const anchor = element(doc, "a", null, node.children.flatMap((child) => textChildren(doc, child.value)));
      anchor.setAttribute("href", node.href);
      anchor.setAttribute("target", "_blank");
      anchor.setAttribute("rel", "noopener noreferrer nofollow");
      return anchor;
    }
    default:
      return assertNever(node, "inlineToDom");
  }
}

function element(doc: DomDocument, tag: string, className: string | null, children: readonly DomChild[]): DomElement {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  for (const child of children) node.appendChild(child);
  return node;
}

// An empty string creates no text node, as the legacy element helper did.
function textChildren(doc: DomDocument, value: string): DomChild[] {
  return value ? [doc.createTextNode(value)] : [];
}
