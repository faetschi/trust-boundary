// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { focusableIn, trapTarget } from "./focusTrap";

// A small tree built with the DOM API, so no markup is parsed from a string.
function tree(...children: Element[]): HTMLElement {
  const root = document.createElement("div");
  root.append(...children);
  return root;
}

function element(tag: string, attributes: Record<string, string> = {}, text = ""): HTMLElement {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attributes)) node.setAttribute(name, value);
  node.textContent = text;
  return node;
}

describe("focusableIn: the tab stops inside a dialog", () => {
  it("lists buttons, links, fields and tabindex 0 in document order", () => {
    const first = element("button", {}, "one");
    const link = element("a", { href: "#x" }, "two");
    const field = element("input");
    const custom = element("span", { tabindex: "0" }, "four");
    const root = tree(first, link, field, custom);
    expect(focusableIn(root)).toEqual([first, link, field, custom]);
  });

  it("skips disabled controls, roving tabindex -1, and anything inside a hidden or inert subtree", () => {
    const disabled = element("button", { disabled: "" });
    const roving = element("button", { tabindex: "-1" });
    const inert = element("div", { inert: "" });
    inert.append(element("button"));
    const hidden = element("div", { hidden: "" });
    hidden.append(element("button"));
    const kept = element("button");
    expect(focusableIn(tree(disabled, roving, inert, hidden, kept))).toEqual([kept]);
  });
});

describe("trapTarget: where Tab goes when it would leave the dialog", () => {
  const first = element("button");
  const middle = element("button");
  const last = element("button");
  const stops = [first, middle, last];

  it("wraps Tab from the last stop to the first", () => {
    expect(trapTarget(stops, last, false)).toBe(first);
  });

  it("wraps Shift+Tab from the first stop to the last", () => {
    expect(trapTarget(stops, first, true)).toBe(last);
  });

  it("leaves Tab and Shift+Tab between stops to the browser", () => {
    expect(trapTarget(stops, middle, false)).toBeNull();
    expect(trapTarget(stops, middle, true)).toBeNull();
    expect(trapTarget(stops, first, false)).toBeNull();
    expect(trapTarget(stops, last, true)).toBeNull();
  });

  it("brings focus that is outside the stops (the body, a heading) back in at the matching end", () => {
    const heading = element("h2");
    expect(trapTarget(stops, heading, false)).toBe(first);
    expect(trapTarget(stops, heading, true)).toBe(last);
    expect(trapTarget(stops, null, false)).toBe(first);
  });

  it("has no target when the dialog has no stops", () => {
    expect(trapTarget([], null, false)).toBeNull();
  });
});
