// Theme choice: system, light or dark. The choice is applied as a data-theme attribute on
// <html>, which tokens.css reads. "system" removes the attribute, so the OS query decides.
// Storage is optional: a browser that refuses storage, or throws on access, keeps the
// choice for the session and falls back to "system" on the next load. Nothing here
// throws.

export type ThemeChoice = "system" | "light" | "dark";

export const THEME_CHOICES: readonly ThemeChoice[] = ["system", "light", "dark"];

export const THEME_STORAGE_KEY = "tbound-webchat-theme";

export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export function parseTheme(value: unknown): ThemeChoice {
  return value === "light" || value === "dark" ? value : "system";
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
  if (!storage) return "system";
  try {
    return parseTheme(storage.getItem(THEME_STORAGE_KEY));
  } catch {
    return "system";
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
