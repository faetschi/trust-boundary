import { describe, expect, it } from "vitest";
import {
  applyTheme,
  DEFAULT_THEME,
  parseTheme,
  readTheme,
  storageOf,
  THEME_STORAGE_KEY,
  writeTheme,
  type StorageLike,
} from "./theme";

function memoryStorage(initial: Record<string, string> = {}): StorageLike & { readonly data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => {
      data.set(key, value);
    },
  };
}

const throwingStorage: StorageLike = {
  getItem: () => {
    throw new Error("storage is disabled");
  },
  setItem: () => {
    throw new Error("quota exceeded");
  },
};

describe("theme choice", () => {
  it("accepts only the three choices, and reads anything else as the dark default", () => {
    expect(parseTheme("light")).toBe("light");
    expect(parseTheme("dark")).toBe("dark");
    expect(parseTheme("system")).toBe("system");
    expect(parseTheme("sepia")).toBe("dark");
    expect(parseTheme(null)).toBe("dark");
  });

  it("defaults to dark when nothing is stored, and keeps a stored system choice", () => {
    expect(DEFAULT_THEME).toBe("dark");
    expect(readTheme(memoryStorage())).toBe("dark");
    expect(readTheme(memoryStorage({ [THEME_STORAGE_KEY]: "system" }))).toBe("system");
  });

  it("persists the choice under one key and reads it back", () => {
    const storage = memoryStorage();
    expect(writeTheme(storage, "dark")).toBe(true);
    expect(storage.data.get(THEME_STORAGE_KEY)).toBe("dark");
    expect(readTheme(storage)).toBe("dark");
  });

  it("falls back to the dark default when storage throws on read, and does not throw", () => {
    expect(readTheme(throwingStorage)).toBe("dark");
  });

  it("reports a refused write, and does not throw", () => {
    expect(writeTheme(throwingStorage, "light")).toBe(false);
  });

  it("falls back to the dark default when storage is unavailable at all", () => {
    expect(readTheme(null)).toBe("dark");
    expect(writeTheme(null, "light")).toBe(false);
  });

  it("reads no storage when the browser withholds it, as a throwing getter", () => {
    const withheld = {
      get localStorage(): StorageLike {
        throw new Error("access denied");
      },
    };
    expect(storageOf(withheld)).toBeNull();
    expect(storageOf(undefined)).toBeNull();
    expect(storageOf({ localStorage: memoryStorage() })).not.toBeNull();
  });
});

describe("applying the theme attribute", () => {
  it("sets data-theme for light and dark, and removes it for system", () => {
    const root = { dataset: {} as Record<string, string | undefined> } as unknown as HTMLElement;
    applyTheme(root, "dark");
    expect(root.dataset.theme).toBe("dark");
    applyTheme(root, "light");
    expect(root.dataset.theme).toBe("light");
    applyTheme(root, "system");
    expect(root.dataset.theme).toBeUndefined();
  });
});
