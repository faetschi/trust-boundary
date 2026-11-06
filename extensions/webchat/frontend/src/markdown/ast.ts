// The typed AST of an assistant message. Nodes are plain data: they name no
// element, no attribute and no markup string. A renderer maps each `type` to one
// element in an exhaustive switch that ends in assertNever. toDom.ts is the
// diagnostic mapping used by the parity suite; the Phase 3 React views map the same
// nodes to React elements.
//
// Invariants. The types enforce the first three; the tests in security.test.ts
// also check them on parsed corpora.
// - There is no raw-HTML node. Source markup is text.
// - A link's href is a SafeHref, which only safeHref in parse.ts produces.
// - A link's children are text only, so a link never contains a link.
// - A code block's language is null or a token from the legacy character set.
// - Inline text is never empty. Adjacent text nodes can occur, as they do in the
//   legacy DOM, so renderers must not depend on text-node boundaries.

declare const safeHrefBrand: unique symbol;

// An allowed link target: http, https or mailto, normalised by the URL parser.
// A string cast can still forge one, so the sources are scanned for casts in tests.
export type SafeHref = string & { readonly [safeHrefBrand]: true };

export type Inline = TextInline | CodeInline | EmInline | StrongInline | LinkInline;

export interface TextInline {
  readonly type: "text";
  readonly value: string;
}

interface CodeInline {
  readonly type: "code";
  readonly value: string;
}

interface EmInline {
  readonly type: "em";
  readonly children: readonly Inline[];
}

interface StrongInline {
  readonly type: "strong";
  readonly children: readonly Inline[];
}

export interface LinkInline {
  readonly type: "link";
  readonly href: SafeHref;
  readonly children: readonly TextInline[];
}

// Message headings sit below the page's own headings, so # is level 3.
export type HeadingLevel = 3 | 4 | 5 | 6;

export type Block = ContentBlock | NoticeBlock;

export type ContentBlock = ParagraphBlock | HeadingBlock | ListBlock | BlockquoteBlock | CodeBlock;

export interface ParagraphBlock {
  readonly type: "paragraph";
  readonly children: readonly Inline[];
}

export interface HeadingBlock {
  readonly type: "heading";
  readonly level: HeadingLevel;
  readonly children: readonly Inline[];
}

// `start` is the number an ordered list begins at; it is 1 for a bullet list.
export interface ListBlock {
  readonly type: "list";
  readonly ordered: boolean;
  readonly start: number;
  readonly items: readonly ListItem[];
}

// A tight item holds its inline text directly. Only nested structure gets blocks.
export type ListItem =
  | { readonly layout: "inline"; readonly children: readonly Inline[] }
  | { readonly layout: "blocks"; readonly children: readonly ContentBlock[] };

interface BlockquoteBlock {
  readonly type: "blockquote";
  readonly children: readonly ContentBlock[];
}

export interface CodeBlock {
  readonly type: "code";
  readonly language: string | null;
  readonly text: string;
}

// Appended after the content when the source was cut. `text` is the message shown.
export interface NoticeBlock {
  readonly type: "notice";
  readonly kind: "truncated";
  readonly text: string;
}

// Exhaustiveness helper: a switch over a union ends with `return assertNever(x, ...)`,
// so adding a node kind is a compile error until every mapping handles it.
export function assertNever(value: never, where: string): never {
  throw new Error(`${where}: unhandled node ${JSON.stringify(value)}`);
}
