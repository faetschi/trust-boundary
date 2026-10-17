// Theme choice: dark, light or system. The choice is applied as a data-theme attribute on
// <html>, which tokens.css reads. "system" removes the attribute, so the OS query decides.
// Dark is the default: nothing stored, an unknown stored value, or storage that is refused
// all read as dark. Storage is optional: a browser that refuses storage, or throws on access,
// keeps the choice for the session and falls back to the default on the next load. Nothing
// here throws. public/theme-boot.js repeats this read and apply for the first paint, before
// any module runs; boot.test.ts checks that the two agree for every stored value.

export type ThemeChoice = "system" | "light" | "dark";

export const THEME_CHOICES: readonly ThemeChoice[] = ["system", "light", "dark"];

export const DEFAULT_THEME: ThemeChoice = "dark";

export const THEME_STORAGE_KEY = "tbound-webchat-theme";

export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function parseTheme(value: unknown): ThemeChoice {
  return value === "light" || value === "dark" || value === "system" ? value : DEFAULT_THEME;
}

// The storage object, or null when the browser refuses access. Reading the property
// itself can throw in a sandboxed frame, so the read is guarded.
export function storageOf(win: { readonly localStorage?: StorageLike } | undefined): StorageLike | null {
  try {
    return win?.localStorage ?? null;
  } catch {
    return null;
  }
}

export function readTheme(storage: StorageLike | null): ThemeChoice {
  if (!storage) return DEFAULT_THEME;
  try {
    return parseTheme(storage.getItem(THEME_STORAGE_KEY));
  } catch {
    return DEFAULT_THEME;
  }
}

// Returns whether the choice was persisted. A failed write leaves the applied theme in
// place for this session only.
export function writeTheme(storage: StorageLike | null, choice: ThemeChoice): boolean {
  if (!storage) return false;
  try {
    storage.setItem(THEME_STORAGE_KEY, choice);
    return true;
  } catch {
    return false;
  }
}

export function applyTheme(root: HTMLElement, choice: ThemeChoice): void {
  if (choice === "system") {
    delete root.dataset.theme;
  } else {
    root.dataset.theme = choice;
  }
}
