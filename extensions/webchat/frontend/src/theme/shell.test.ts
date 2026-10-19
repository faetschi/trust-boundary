// The shell loads the theme script first: a bare classic script in <head>, before the module script, so the parser
// waits for it and the first paint already has the stored choice. These checks read index.html (the source) and the
// committed build (internal/webchat/dist), the two files the browser and the Go gateway actually use. The built files
// are read inside each test, so a build that lacks them fails those tests and nothing else.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const sourceHtml = (): string => readFileSync(new URL("../../index.html", import.meta.url), "utf8");
const builtFile = (name: string): Buffer => readFileSync(new URL(`../../../internal/webchat/dist/${name}`, import.meta.url));

// Every <script ...> start tag in order: its raw attributes and its offset in the document.
function scriptTags(html: string): Array<{ attrs: string; index: number }> {
  return [...html.matchAll(/<script\b([^>]*)>/gi)].map((match) => ({ attrs: match[1] ?? "", index: match.index ?? 0 }));
}

function shellChecks(label: string, html: () => string, bootSrc: RegExp): void {
  describe(`${label}: the theme script is the first script, and it blocks`, () => {
    it("is the first <script>, a bare classic script: no type, defer, async or nomodule", () => {
      const first = scriptTags(html())[0];
      expect(first, "a script tag").toBeDefined();
      expect(first?.attrs).toMatch(bootSrc);
      expect(first?.attrs).not.toMatch(/\btype\s*=/i);
      expect(first?.attrs).not.toMatch(/\b(defer|async|nomodule)\b/i);
    });

    it("sits in <head>, and the module script comes after it", () => {
      const markup = html();
      const scripts = scriptTags(markup);
      const first = scripts[0];
      const module = scripts.find((script) => /\btype="module"/.test(script.attrs));
      expect(module, "a module script").toBeDefined();
      expect(module?.index ?? -1).toBeGreaterThan(first?.index ?? Number.POSITIVE_INFINITY);
      expect(markup.indexOf("<body")).toBeGreaterThan(first?.index ?? Number.POSITIVE_INFINITY);
    });
  });
}

shellChecks("index.html", sourceHtml, /src="\/(?:app\/)?theme-boot\.js"/);
shellChecks("dist/index.html", () => builtFile("index.html").toString("utf8"), /^\s*src="\/app\/theme-boot\.js"\s*$/);

describe("the committed build ships the theme script unchanged", () => {
  it("dist/theme-boot.js is byte-for-byte public/theme-boot.js", () => {
    const source = readFileSync(new URL("../../public/theme-boot.js", import.meta.url));
    expect(builtFile("theme-boot.js").equals(source)).toBe(true);
  });
});
