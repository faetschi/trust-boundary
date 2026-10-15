// Every class a component reads from its CSS module is defined by that module. A missing rule fails silently in the
// browser: the element renders unstyled, with its parts running together (the tool card header read `styles.head`
// with no rule behind it). This reads the source, so it needs no DOM.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const SRC = fileURLToPath(new URL("..", import.meta.url));

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) out.push(...sourceFiles(path));
    else if (entry.endsWith(".tsx") && !/\.test\.tsx$/.test(entry)) out.push(path);
  }
  return out;
}

// The CSS with its comments removed, so a class named in a comment does not count as defined.
function rulesOf(path: string): string {
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

function definesClass(css: string, name: string): boolean {
  return new RegExp(`\\.${name}(?![\\w-])`).test(css);
}

const components = sourceFiles(SRC);

describe("CSS modules: every styles.<name> a component reads is defined in its stylesheet", () => {
  it("found the components that use CSS modules", () => {
    expect(components.filter((file) => /import styles from/.test(readFileSync(file, "utf8"))).length).toBeGreaterThan(20);
  });

  for (const file of components) {
    const source = readFileSync(file, "utf8");
    const sheet = /import styles from\s+["']([^"']+\.module\.css)["']/.exec(source)?.[1];
    if (sheet === undefined) continue;
    const name = relative(SRC, file).replace(/\\/g, "/");

    it(`${name} reads only classes its module defines`, () => {
      const css = rulesOf(resolve(dirname(file), sheet));
      const read = new Set([...source.matchAll(/\bstyles\.([A-Za-z_][\w-]*)/g)].map((match) => match[1] ?? ""));
      const missing = [...read].filter((className) => !definesClass(css, className));
      expect(missing, `${name} reads classes that ${sheet} does not define`).toEqual([]);
    });
  }
});
