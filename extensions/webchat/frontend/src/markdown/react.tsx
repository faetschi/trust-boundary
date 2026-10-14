// The React mapping of the markdown AST (ast.ts). Each node type maps to one element in an
// exhaustive switch that ends in assertNever, so a new node kind is a compile error here until
// it is mapped. The markup is the same as toHtmlString.ts for every corpus (react.test.tsx).
//
// Safety. No markup string is built and no raw-HTML property is set. Every node becomes a React
// element or a text child, and no style value is written. A link is an anchor only for a SafeHref, and the scheme is checked
// again here, so a forged brand becomes literal text instead of a live link.
//
// Identity. Each top-level block is a memoised component keyed by its position. The incremental
// parser returns the same node objects for an unchanged leading prefix, so a streaming update
// skips those blocks.

import { Fragment, memo, type ReactNode } from "react";
import {
  assertNever,
  type Block,
  type CodeBlock,
  type ContentBlock,
  type HeadingLevel,
  type Inline,
  type LinkInline,
  type ListBlock,
  type ListItem,
} from "./ast";
import "./markdown.css";

const LINK_TARGET = /^(?:https?|mailto):/i;

export interface MarkdownProps {
  readonly blocks: readonly Block[];
  // Diagnostic hook for the render-count test: called once each time a top-level block renders.
  readonly onBlockRender?: (block: Block) => void;
}

export function Markdown({ blocks, onBlockRender }: MarkdownProps): ReactNode {
  return <>{renderBlocks(blocks, onBlockRender)}</>;
}

export function renderBlocks(blocks: readonly Block[], onBlockRender?: (block: Block) => void): ReactNode[] {
  return blocks.map((block, index) => <BlockView key={index} block={block} onBlockRender={onBlockRender} />);
}

interface BlockViewProps {
  readonly block: Block;
  readonly onBlockRender?: (block: Block) => void;
}

const BlockView = memo(function BlockView({ block, onBlockRender }: BlockViewProps): ReactNode {
  onBlockRender?.(block);
  return blockNode(block);
});

function blockNode(block: Block): ReactNode {
  switch (block.type) {
    case "paragraph":
      return <p>{inlineNodes(block.children)}</p>;
    case "heading":
      return headingNode(block.level, inlineNodes(block.children));
    case "list":
      return listNode(block);
    case "blockquote":
      return <blockquote>{blockNodes(block.children)}</blockquote>;
    case "code":
      return codeNode(block);
    case "notice":
      return <p className="md-truncated">{block.text}</p>;
    default:
      return assertNever(block, "blockNode");
  }
}

function blockNodes(blocks: readonly ContentBlock[]): ReactNode[] {
  return blocks.map((block, index) => <Fragment key={index}>{blockNode(block)}</Fragment>);
}

// Message headings sit at level 3 and below (ast.ts), so the page's own headings stay first.
function headingNode(level: HeadingLevel, children: ReactNode): ReactNode {
  switch (level) {
    case 3:
      return <h3>{children}</h3>;
    case 4:
      return <h4>{children}</h4>;
    case 5:
      return <h5>{children}</h5>;
    case 6:
      return <h6>{children}</h6>;
    default:
      return assertNever(level, "headingNode");
  }
}

function listNode(block: ListBlock): ReactNode {
  const items = block.items.map((item, index) => <li key={index}>{listItemNodes(item)}</li>);
  if (block.ordered) return <ol start={block.start !== 1 ? block.start : undefined}>{items}</ol>;
  return <ul>{items}</ul>;
}

function listItemNodes(item: ListItem): ReactNode {
  switch (item.layout) {
    case "inline":
      return inlineNodes(item.children);
    case "blocks":
      return blockNodes(item.children);
    default:
      return assertNever(item, "listItemNodes");
  }
}

// The scroll region is keyboard-reachable and labelled, so a long line can be read without a pointer.
function codeNode(block: CodeBlock): ReactNode {
  const label = block.language ? `Code block, ${block.language}` : "Code block";
  return (
    <pre className="md-code" role="region" tabIndex={0} aria-label={label}>
      <code className={block.language ? `language-${block.language}` : undefined}>{block.text}</code>
    </pre>
  );
}

function inlineNodes(nodes: readonly Inline[]): ReactNode[] {
  return nodes.map((node, index) => <Fragment key={index}>{inlineNode(node)}</Fragment>);
}

function inlineNode(node: Inline): ReactNode {
  switch (node.type) {
    case "text":
      return node.value;
    case "code":
      return <code>{node.value}</code>;
    case "em":
      return <em>{inlineNodes(node.children)}</em>;
    case "strong":
      return <strong>{inlineNodes(node.children)}</strong>;
    case "link":
      return linkNode(node);
    default:
      return assertNever(node, "inlineNode");
  }
}

// A link's children are text only (ast.ts), so its text is joined and never nests an element.
function linkNode(node: LinkInline): ReactNode {
  const text = node.children.map((child) => child.value).join("");
  if (!LINK_TARGET.test(node.href)) return text;
  return (
    <a href={node.href} target="_blank" rel="noopener noreferrer nofollow">
      {text}
    </a>
  );
}
