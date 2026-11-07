import { useCallback, useRef, useState } from "react";

// The shell's presentation choices: which sidebar is collapsed, and which governance panel is selected. One
// versioned key. Nothing here is claim or verification state. A stored value that is not exactly the shape this
// version writes is cleared, and the defaults apply. Storage is optional: a browser that refuses access keeps the
// choice for this session (it is React state) and falls back to the defaults on the next load. Nothing here throws.

export const UI_PREFS_KEY = "tbound.webchat.ui.v1";

// The governance panels, in tab order. The record tab is not a panel, and it is never stored.
export const PANEL_KEYS = ["decisions", "provenance", "publication", "recovery", "qualification"] as const;
export type PanelKey = (typeof PANEL_KEYS)[number];

export interface UiPrefs {
  readonly railCollapsed: boolean;
  readonly inspectorCollapsed: boolean;
  readonly panel: PanelKey;
}

// The governance column starts as its icon column: the header's Governance control carries the decisions badge, and opening it
// is one press. A choice made by the reader is stored and kept.
export const DEFAULT_UI_PREFS: UiPrefs = { railCollapsed: false, inspectorCollapsed: true, panel: "decisions" };

export interface PrefsStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export function isPanelKey(value: unknown): value is PanelKey {
  return typeof value === "string" && (PANEL_KEYS as readonly string[]).includes(value);
}

// The storage object, or null when the browser refuses access. Reading the property itself can throw in a
// sandboxed frame, so the read is guarded.
export function prefsStorageOf(win: { readonly localStorage?: PrefsStorage } | undefined): PrefsStorage | null {
  try {
    return win?.localStorage ?? null;
  } catch {
    return null;
  }
}

const FIELDS = ["inspectorCollapsed", "panel", "railCollapsed"];

// The preferences a parsed value holds, or null when it is not exactly this version's shape: an object with
// these three fields and no others, two boolean flags, and a known panel.
export function parseUiPrefs(value: unknown): UiPrefs | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  if (Object.keys(record).sort().join() !== FIELDS.join()) return null;
  const { railCollapsed, inspectorCollapsed, panel } = record;
  if (typeof railCollapsed !== "boolean" || typeof inspectorCollapsed !== "boolean") return null;
  if (!isPanelKey(panel)) return null;
  return { railCollapsed, inspectorCollapsed, panel };
}

function parseStored(raw: string): UiPrefs | null {
  try {
    return parseUiPrefs(JSON.parse(raw));
  } catch {
    return null;
  }
}

// The stored preferences, or the defaults. A value that does not parse, or does not match, is cleared.
export function readUiPrefs(storage: PrefsStorage | null): UiPrefs {
  if (!storage) return DEFAULT_UI_PREFS;
  let raw: string | null;
  try {
    raw = storage.getItem(UI_PREFS_KEY);
  } catch {
    return DEFAULT_UI_PREFS;
  }
  if (raw === null) return DEFAULT_UI_PREFS;
  const prefs = parseStored(raw);
  if (prefs) return prefs;
  try {
    storage.removeItem(UI_PREFS_KEY);
  } catch {
    // A refused clear leaves the stored value in place. The defaults apply for this session either way.
  }
  return DEFAULT_UI_PREFS;
}

// Returns whether the preferences were persisted. A refused write keeps them for this session only.
export function writeUiPrefs(storage: PrefsStorage | null, prefs: UiPrefs): boolean {
  if (!storage) return false;
  try {
    storage.setItem(UI_PREFS_KEY, JSON.stringify(prefs));
    return true;
  } catch {
    return false;
  }
}

// The preferences for the shell, and an update that applies a partial change. The change takes effect at once
// and is persisted when storage allows it. Two updates in one batch compose, because the update reads the latest value.
export function useUiPrefs(): readonly [UiPrefs, (patch: Partial<UiPrefs>) => void] {
  const [prefs, setPrefs] = useState<UiPrefs>(() => readUiPrefs(prefsStorageOf(window)));
  const latest = useRef(prefs);
  const update = useCallback((patch: Partial<UiPrefs>) => {
    const next: UiPrefs = { ...latest.current, ...patch };
    latest.current = next;
    writeUiPrefs(prefsStorageOf(window), next);
    setPrefs(next);
  }, []);
  return [prefs, update];
}
