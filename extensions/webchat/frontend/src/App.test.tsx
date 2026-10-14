// @vitest-environment happy-dom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { App } from "./App";

// The server render is the shell without its effects: no snapshot, no stream. It is the
// first paint, so it must already carry the claim boundary and must stay CSP-safe.
describe("App: first render", () => {
  it("renders the product heading, the build version and the claim boundary", () => {
    const html = renderToStaticMarkup(<App version="0.1.0" />);
    expect(html).toContain("<h1");
    expect(html).toContain("tbound web chat");
    expect(html).toContain("build 0.1.0");
    expect(html).toContain("Presentation only");
    expect(html).toContain("Display only: these values are copied from the projection and are not evidence.");
    // Before a projection loads, a claim value is unavailable, never a literal copied into the page.
    expect(html).toContain("unavailable (no projection loaded)");
    expect(html).not.toContain("not-established");
  });

  it("has no inline style, no event-handler attribute and no script element", () => {
    const html = renderToStaticMarkup(<App version="0.1.0" />);
    expect(html).not.toMatch(/\sstyle\s*=/);
    expect(html).not.toMatch(/\son[a-z]+\s*=/);
    expect(html).not.toMatch(/<script/i);
  });

  it("names the product, not the upstream brand", () => {
    const html = renderToStaticMarkup(<App version="0.1.0" />);
    expect(html).not.toMatch(/deepseek/i);
  });
});
