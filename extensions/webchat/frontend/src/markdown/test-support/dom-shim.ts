// TEST-ONLY DOM SHIM. Not imported by the app. It implements the subset that the
// diagnostic mapping (toDom.ts) and the legacy renderer use: createElement,
// createTextNode, className, setAttribute, appendChild and removeChild.
//
// Markup writes are refused. Every element and text node is made non-extensible, so
// assigning a property the shim does not define throws a TypeError in strict mode.
// That covers the markup-string properties a renderer must never write, and calling
// a method the shim does not implement throws too. dom-shim.test.ts pins this.

import type { DomChild, DomDocument } from "../toDom";

export class ShimText {
  readonly nodeType = 3 as const;
  readonly data: string;

  constructor(data: string) {
    this.data = data;
    Object.preventExtensions(this);
  }
}

export class ShimElement {
  readonly nodeType = 1 as const;
  readonly tagName: string;
  className = "";
  readonly attributes: Record<string, string> = {};
  readonly children: ShimChild[] = [];

  constructor(tagName: string) {
    this.tagName = tagName.toLowerCase();
    Object.preventExtensions(this);
  }

  setAttribute(name: string, value: string): void {
    this.attributes[name] = String(value);
  }

  appendChild(child: DomChild): DomChild {
    const shim = asShim(child);
    this.children.push(shim);
    return shim;
  }

  removeChild(child: ShimChild): ShimChild {
    const index = this.children.indexOf(child);
    if (index >= 0) this.children.splice(index, 1);
    return child;
  }

  get textContent(): string {
    return this.children.map((child) => textOf(child)).join("");
  }
}

export type ShimChild = ShimElement | ShimText;

export interface ShimDocument extends DomDocument {
  createElement(tagName: string): ShimElement;
  createTextNode(data: string): ShimText;
}

export function createShimDocument(): ShimDocument {
  return {
    createElement: (tagName) => new ShimElement(tagName),
    createTextNode: (data) => new ShimText(data),
  };
}

function asShim(child: DomChild): ShimChild {
  if (child instanceof ShimElement || child instanceof ShimText) return child;
  throw new TypeError("the shim only holds nodes it created");
}

function textOf(node: ShimChild): string {
  return node instanceof ShimText ? node.data : node.textContent;
}

// fingerprint is the structural form the legacy test suite compared: tag, class,
// attributes in name order, and each text node as JSON. Two trees with equal
// fingerprints render the same DOM, text-node boundaries included.
export function fingerprint(node: ShimChild): string {
  if (node instanceof ShimText) return JSON.stringify(node.data);
  const cls = node.className ? ` class=${JSON.stringify(node.className)}` : "";
  const attrs = Object.keys(node.attributes)
    .sort()
    .map((key) => ` ${key}=${JSON.stringify(node.attributes[key])}`)
    .join("");
  return `<${node.tagName}${cls}${attrs}>${node.children.map(fingerprint).join("")}</${node.tagName}>`;
}

export function childrenFingerprint(container: ShimElement): string {
  return container.children.map(fingerprint).join("");
}

// toHtml writes the tree as escaped HTML with every attribute, class included, in
// name order. It is compared with toHtmlString.ts.
export function toHtml(node: ShimChild): string {
  if (node instanceof ShimText) return escapeText(node.data);
  const attributes: Record<string, string> = { ...node.attributes };
  if (node.className) attributes.class = node.className;
  const rendered = Object.keys(attributes)
    .sort()
    .map((key) => ` ${key}="${escapeText(attributes[key] ?? "").replace(/"/g, "&quot;")}"`)
    .join("");
  return `<${node.tagName}${rendered}>${node.children.map(toHtml).join("")}</${node.tagName}>`;
}

function escapeText(value: string): string {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
