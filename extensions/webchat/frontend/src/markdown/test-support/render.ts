// TEST-ONLY helpers over the new parser: map an AST into the shim, measure its work,
// and walk every node. Nothing here is used by the app.

import type { Block, Inline, ListItem } from "../ast";
import { parseMarkdown, scanWork } from "../parse";
import { blocksToDom } from "../toDom";
import { blocksToHtml } from "../toHtmlString";
import { type ShimElement, childrenFingerprint, createShimDocument, toHtml } from "./dom-shim";

export function domOfBlocks(blocks: readonly Block[]): ShimElement {
  const doc = createShimDocument();
  const container = doc.createElement("div");
  for (const node of blocksToDom(doc, blocks)) container.appendChild(node);
  return container;
}

export interface NewRender {
  readonly fingerprint: string;
  readonly html: string;
  readonly htmlOfString: string;
  readonly work: number;
}

// renderNew parses `text` once and reports what the parity suites compare: the DOM
// fingerprint, the DOM serialized as HTML, the AST serialized as HTML, and the work.
export function renderNew(text: string): NewRender {
  const before = scanWork.steps;
  const { blocks } = parseMarkdown(text);
  const work = scanWork.steps - before;
  const container = domOfBlocks(blocks);
  return {
    fingerprint: childrenFingerprint(container),
    html: container.children.map(toHtml).join(""),
    htmlOfString: blocksToHtml(blocks),
    work,
  };
}

// astText is the text a reader sees: the AST's counterpart of textContent. The DOM
// mapping must produce the same string, which the differential suite checks.
export function astText(blocks: readonly Block[]): string {
  return blocks.map(blockText).join("");
}

function blockText(block: Block): string {
  switch (block.type) {
    case "paragraph":
    case "heading":
      return inlinesText(block.children);
    case "list":
      return block.items.map(itemText).join("");
    case "blockquote":
      return block.children.map(blockText).join("");
    case "code":
      return block.text;
    case "notice":
      return block.text;
  }
}

function itemText(item: ListItem): string {
  return item.layout === "inline" ? inlinesText(item.children) : item.children.map(blockText).join("");
}

function inlinesText(nodes: readonly Inline[]): string {
  return nodes.map(inlineText).join("");
}

function inlineText(node: Inline): string {
  switch (node.type) {
    case "text":
    case "code":
      return node.value;
    case "em":
    case "strong":
      return inlinesText(node.children);
    case "link":
      return node.children.map((child) => child.value).join("");
  }
}

// Every block and inline node, in document order, depth first.
export type AnyNode = Block | Inline | ListItem;

export function walk(blocks: readonly Block[]): AnyNode[] {
  const out: AnyNode[] = [];
  const visitBlock = (block: Block): void => {
    out.push(block);
    switch (block.type) {
      case "paragraph":
      case "heading":
        visitInlines(block.children);
        return;
      case "list":
        for (const item of block.items) {
          out.push(item);
          if (item.layout === "inline") visitInlines(item.children);
          else for (const child of item.children) visitBlock(child);
        }
        return;
      case "blockquote":
        for (const child of block.children) visitBlock(child);
        return;
      case "code":
      case "notice":
        return;
    }
  };
  const visitInlines = (nodes: readonly Inline[]): void => {
    for (const node of nodes) {
      out.push(node);
      if (node.type === "em" || node.type === "strong") visitInlines(node.children);
      if (node.type === "link") for (const child of node.children) out.push(child);
    }
  };
  for (const block of blocks) visitBlock(block);
  return out;
}
