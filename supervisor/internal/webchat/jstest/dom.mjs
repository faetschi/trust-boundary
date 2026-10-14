// dom.mjs — a minimal DOM stub for exercising the renderer under Node.
// It implements only the surface render.mjs uses: createElement, className,
// textContent, dataset, appendChild/removeChild, firstChild, setAttribute,
// querySelector(All) for the three selector shapes the renderer needs, plus the
// `Node` global used by `valueNode instanceof Node`.

class DomNode {}

class DomElement extends DomNode {
  constructor(tagName) {
    super();
    this.tagName = String(tagName).toUpperCase();
    this.children = [];
    this.parentNode = null;
    this._className = "";
    this._text = "";
    this.dataset = {};
    this.attributes = {};
    this.open = false;
    this.title = "";
    this.type = "";
  }

  set className(value) {
    this._className = value || "";
  }

  get className() {
    return this._className;
  }

  set textContent(value) {
    this._text = value === undefined || value === null ? "" : String(value);
    for (const child of this.children) child.parentNode = null;
    this.children = [];
  }

  get textContent() {
    if (this.children.length === 0) return this._text;
    return this.children.map((child) => child.textContent).join("");
  }

  get firstChild() {
    return this.children.length ? this.children[0] : null;
  }

  appendChild(node) {
    node.parentNode = this;
    this.children.push(node);
    return node;
  }

  removeChild(node) {
    const index = this.children.indexOf(node);
    if (index >= 0) {
      this.children.splice(index, 1);
      node.parentNode = null;
    }
    return node;
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }

  getAttribute(name) {
    return Object.prototype.hasOwnProperty.call(this.attributes, name) ? this.attributes[name] : null;
  }

  removeAttribute(name) {
    delete this.attributes[name];
  }

  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }

  querySelectorAll(selector) {
    const matches = [];
    const visit = (node) => {
      for (const child of node.children) {
        if (child instanceof DomElement && matchesSelector(child, selector)) matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
}

function matchesSelector(node, selector) {
  const attribute = /^([a-zA-Z]+)\.([a-zA-Z-]+)\[([a-zA-Z-]+)\]$/.exec(selector);
  if (attribute) {
    const [, tag, className, prop] = attribute;
    return (
      node.tagName === tag.toUpperCase() &&
      node.className.split(/\s+/).includes(className) &&
      node[prop] === true
    );
  }
  const classOnly = /^([a-zA-Z]+)\.([a-zA-Z-]+)$/.exec(selector);
  if (classOnly) {
    const [, tag, className] = classOnly;
    return node.tagName === tag.toUpperCase() && node.className.split(/\s+/).includes(className);
  }
  if (selector.startsWith(".")) {
    return node.className.split(/\s+/).includes(selector.slice(1));
  }
  return node.tagName === selector.toUpperCase();
}

export function installDom() {
  globalThis.Node = DomNode;
  globalThis.document = {
    createElement(tagName) {
      return new DomElement(tagName);
    },
  };
}

export function createRefs(names) {
  const refs = {};
  for (const name of names) {
    refs[name] = new DomElement("div");
  }
  return refs;
}

installDom();
