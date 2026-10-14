// The test DOM refuses markup writes. The parity suites rely on this: a renderer that
// reached for a markup string would fail here instead of passing by accident.

import { describe, expect, it } from "vitest";
import { ShimElement, ShimText, createShimDocument, fingerprint, toHtml } from "./dom-shim";

describe("test DOM shim", () => {
  it("markup-string writes throw, as the legacy shim's markup sinks did", () => {
    const element = createShimDocument().createElement("div");
    const writable = element as unknown as Record<string, unknown>;
    expect(() => {
      writable.innerHTML = "<b>x</b>";
    }).toThrow(TypeError);
    expect(() => {
      writable.outerHTML = "<b>x</b>";
    }).toThrow(TypeError);
    const callable = element as unknown as { insertAdjacentHTML: (position: string, text: string) => void };
    expect(() => callable.insertAdjacentHTML("beforeend", "<b>x</b>")).toThrow(TypeError);
    expect(element.children).toHaveLength(0);
  });

  it("elements and text nodes are non-extensible, so no unknown property can be added", () => {
    const doc = createShimDocument();
    expect(Object.isExtensible(doc.createElement("p"))).toBe(false);
    expect(Object.isExtensible(doc.createTextNode("x"))).toBe(false);
  });

  it("the fingerprint is the legacy serialization: tag, class, sorted attributes, JSON text", () => {
    const doc = createShimDocument();
    const pre = doc.createElement("pre");
    pre.className = "md-code";
    pre.setAttribute("tabindex", "0");
    pre.setAttribute("role", "region");
    pre.setAttribute("aria-label", "Code block");
    pre.appendChild(doc.createTextNode('say "hi"'));
    expect(fingerprint(pre)).toBe('<pre class="md-code" aria-label="Code block" role="region" tabindex="0">"say \\"hi\\""</pre>');
  });

  it("toHtml escapes text and attribute values and writes class among the sorted attributes", () => {
    const doc = createShimDocument();
    const anchor = doc.createElement("a");
    anchor.setAttribute("href", 'https://e.example/?a=1&b="2"');
    anchor.appendChild(doc.createTextNode("<&>"));
    expect(toHtml(anchor)).toBe('<a href="https://e.example/?a=1&amp;b=&quot;2&quot;">&lt;&amp;&gt;</a>');
  });

  it("removeChild takes out only the given node", () => {
    const doc = createShimDocument();
    const container = doc.createElement("div");
    const first = doc.createTextNode("a");
    const second = doc.createTextNode("b");
    container.appendChild(first);
    container.appendChild(second);
    container.removeChild(first);
    expect(container.children).toStrictEqual([second]);
    expect(container.textContent).toBe("b");
  });

  it("the created nodes are the shim's own classes", () => {
    const doc = createShimDocument();
    expect(doc.createElement("p")).toBeInstanceOf(ShimElement);
    expect(doc.createTextNode("p")).toBeInstanceOf(ShimText);
  });
});
