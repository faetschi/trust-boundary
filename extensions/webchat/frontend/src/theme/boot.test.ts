// The first-paint theme script, public/theme-boot.js, is classic and render-blocking. These tests run the shipped
// file in a fresh VM context over a stub document and storage. It must set the same data-theme as the module path
// (theme.ts: applyTheme(readTheme(storageOf(window)))) for every stored value, and it must never throw.
import { readFileSync } from "node:fs";
import { createContext, runInContext } from "node:vm";
import { describe, expect, it } from "vitest";
import { applyTheme, readTheme, storageOf, THEME_STORAGE_KEY, type StorageLike } from "./theme";

const boot = readFileSync(new URL("../../public/theme-boot.js", import.meta.url), "utf8");

// A localStorage over a map, or one that throws on every read, as a refused or sandboxed browser does.
function storageWith(values: Record<string, string>): StorageLike {
  return { getItem: (key) => values[key] ?? null, setItem: () => undefined };
}
const throwingStorage: StorageLike = {
  getItem: () => {
    throw new Error("storage is disabled");
  },
  setItem: () => undefined,
};

// The window as the script sees it. withheld makes the localStorage property itself throw, as a sandboxed frame does.
function windowWith(storage: StorageLike | undefined, withheld = false): Record<string, unknown> {
  const win: Record<string, unknown> = {};
  if (withheld) {
    Object.defineProperty(win, "localStorage", {
      get() {
        throw new Error("access denied");
      },
    });
  } else if (storage !== undefined) {
    win.localStorage = storage;
  }
  return win;
}

// Runs the script and returns the attributes it set on <html>. A throw from the script fails the test here.
function runBoot(win: Record<string, unknown>): Map<string, string> {
  const attributes = new Map<string, string>();
  const documentElement = {
    setAttribute: (name: string, value: string) => attributes.set(name, value),
    removeAttribute: (name: string) => attributes.delete(name),
  };
  runInContext(boot, createContext({ window: win, document: { documentElement } }));
  return attributes;
}

// What the module path sets for the same window: the data-theme of the root, or undefined for the system choice.
function moduleThemeOf(win: Record<string, unknown>): string | undefined {
  const root = { dataset: {} as Record<string, string | undefined> } as unknown as HTMLElement;
  applyTheme(root, readTheme(storageOf(win as { readonly localStorage?: StorageLike })));
  return root.dataset.theme;
}

describe("theme-boot.js: the first-paint choice", () => {
  it("applies a stored light or dark choice to <html> before the first paint", () => {
    expect(runBoot(windowWith(storageWith({ [THEME_STORAGE_KEY]: "light" }))).get("data-theme")).toBe("light");
    expect(runBoot(windowWith(storageWith({ [THEME_STORAGE_KEY]: "dark" }))).get("data-theme")).toBe("dark");
  });

  it("leaves the system choice alone: no attribute for system, an invalid value, or nothing stored", () => {
    expect(runBoot(windowWith(storageWith({ [THEME_STORAGE_KEY]: "system" }))).size).toBe(0);
    expect(runBoot(windowWith(storageWith({ [THEME_STORAGE_KEY]: "sepia" }))).size).toBe(0);
    expect(runBoot(windowWith(storageWith({ [THEME_STORAGE_KEY]: "" }))).size).toBe(0);
    expect(runBoot(windowWith(storageWith({}))).size).toBe(0);
    expect(runBoot(windowWith(undefined))).toEqual(new Map());
  });

  it("does not throw when storage throws on read, and keeps the system choice", () => {
    expect(runBoot(windowWith(throwingStorage)).size).toBe(0);
  });

  it("does not throw when the localStorage property itself is withheld, and keeps the system choice", () => {
    expect(runBoot(windowWith(undefined, true)).size).toBe(0);
  });

  it("sets the same attribute as the module path (theme.ts) for every stored value, and for blocked storage", () => {
    const cases: Array<Record<string, unknown>> = [
      windowWith(storageWith({ [THEME_STORAGE_KEY]: "light" })),
      windowWith(storageWith({ [THEME_STORAGE_KEY]: "dark" })),
      windowWith(storageWith({ [THEME_STORAGE_KEY]: "system" })),
      windowWith(storageWith({ [THEME_STORAGE_KEY]: "sepia" })),
      windowWith(storageWith({ [THEME_STORAGE_KEY]: "DARK" })),
      windowWith(storageWith({})),
      windowWith(throwingStorage),
      windowWith(undefined, true),
      windowWith(undefined),
    ];
    for (const win of cases) {
      expect(runBoot(win).get("data-theme")).toBe(moduleThemeOf(win));
    }
  });

  it("is a classic script: no import or export, no document.write, no eval, and no style it sets inline", () => {
    expect(boot).not.toMatch(/^\s*(import|export)\b/m);
    expect(boot).not.toMatch(/document\.write|innerHTML|\beval\s*\(|\.style\b|\.cssText\b/);
    expect(boot).toContain(THEME_STORAGE_KEY);
  });
});
