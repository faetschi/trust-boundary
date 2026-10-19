// @vitest-environment happy-dom
import { act, createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, type Mounted } from "../test-support/dom";
import {
  DEFAULT_UI_PREFS,
  UI_PREFS_KEY,
  parseUiPrefs,
  prefsStorageOf,
  readUiPrefs,
  useUiPrefs,
  writeUiPrefs,
  type PrefsStorage,
  type UiPrefs,
} from "./uiPrefs";

const VALID: UiPrefs = { railCollapsed: true, inspectorCollapsed: false, panel: "publication" };
// The defaults, pinned as literals. The read cases compare to these, not to DEFAULT_UI_PREFS, so a changed default fails here.
const PINNED_DEFAULTS: UiPrefs = { railCollapsed: false, inspectorCollapsed: false, panel: "decisions" };

function memoryStorage(initial: Record<string, string> = {}): PrefsStorage & { readonly data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => {
      data.set(key, value);
    },
    removeItem: (key) => {
      data.delete(key);
    },
  };
}

const refusing: PrefsStorage = {
  getItem: () => {
    throw new Error("storage is disabled");
  },
  setItem: () => {
    throw new Error("quota exceeded");
  },
  removeItem: () => {
    throw new Error("storage is disabled");
  },
};

// Makes window.localStorage throw on access, as a sandboxed frame does. Returns the restore function.
function refuseWindowStorage(): () => void {
  const original = Object.getOwnPropertyDescriptor(window, "localStorage");
  Object.defineProperty(window, "localStorage", {
    configurable: true,
    get() {
      throw new DOMException("storage is disabled", "SecurityError");
    },
  });
  return () => {
    if (original) Object.defineProperty(window, "localStorage", original);
    else delete (window as unknown as { localStorage?: unknown }).localStorage;
  };
}

describe("ui preferences: the stored shape is strict", () => {
  it("accepts exactly the three fields, with boolean flags and a known panel", () => {
    expect(parseUiPrefs(VALID)).toEqual(VALID);
    for (const panel of ["decisions", "provenance", "publication", "recovery", "qualification"]) {
      expect(parseUiPrefs({ ...VALID, panel })).toEqual({ ...VALID, panel });
    }
  });

  it("rejects every other value, so that the stored value is cleared", () => {
    const rejected: unknown[] = [
      null,
      "x",
      1,
      [],
      [VALID],
      { railCollapsed: true, inspectorCollapsed: false },
      { ...VALID, extra: 1 },
      { ...VALID, railCollapsed: "true" },
      { ...VALID, inspectorCollapsed: 0 },
      { ...VALID, panel: "sepia" },
      { ...VALID, panel: "Decisions" },
      { ...VALID, panel: 2 },
      { ...VALID, panel: "record" },
    ];
    for (const value of rejected) expect(parseUiPrefs(value), JSON.stringify(value)).toBeNull();
  });
});

describe("reading ui preferences", () => {
  it("exports the pinned defaults, so the read cases below check the values the product ships", () => {
    expect(DEFAULT_UI_PREFS).toEqual(PINNED_DEFAULTS);
  });

  it("returns the defaults when nothing is stored, and leaves storage alone", () => {
    const storage = memoryStorage();
    const remove = vi.spyOn(storage, "removeItem");
    expect(readUiPrefs(storage)).toEqual(PINNED_DEFAULTS);
    expect(remove).not.toHaveBeenCalled();
  });

  it("returns a stored value that is valid", () => {
    expect(readUiPrefs(memoryStorage({ [UI_PREFS_KEY]: JSON.stringify(VALID) }))).toEqual(VALID);
  });

  it("clears an invalid value and falls back to the defaults", () => {
    const invalid = ["{not json", JSON.stringify({ ...VALID, panel: "sepia" }), "null", "42", JSON.stringify([VALID])];
    for (const raw of invalid) {
      const storage = memoryStorage({ [UI_PREFS_KEY]: raw });
      expect(readUiPrefs(storage), raw).toEqual(PINNED_DEFAULTS);
      expect(storage.data.has(UI_PREFS_KEY), raw).toBe(false);
    }
  });

  it("reads the defaults, without throwing, when storage is missing or refuses access", () => {
    expect(readUiPrefs(null)).toEqual(PINNED_DEFAULTS);
    expect(readUiPrefs(refusing)).toEqual(PINNED_DEFAULTS);
  });

  it("still reads the defaults when clearing an invalid value is refused", () => {
    const storage: PrefsStorage = {
      ...memoryStorage({ [UI_PREFS_KEY]: "{bad" }),
      removeItem: () => {
        throw new Error("denied");
      },
    };
    expect(readUiPrefs(storage)).toEqual(PINNED_DEFAULTS);
  });
});

describe("writing ui preferences", () => {
  it("stores the JSON under the one versioned key and reports success", () => {
    const storage = memoryStorage();
    expect(writeUiPrefs(storage, VALID)).toBe(true);
    expect(storage.data.get(UI_PREFS_KEY)).toBe(JSON.stringify(VALID));
  });

  it("reports a refused write as not persisted, without throwing", () => {
    expect(writeUiPrefs(refusing, VALID)).toBe(false);
    expect(writeUiPrefs(null, VALID)).toBe(false);
  });

  it("returns no storage when the window refuses to expose it", () => {
    const hostile = {};
    Object.defineProperty(hostile, "localStorage", {
      get() {
        throw new Error("sandboxed frame");
      },
    });
    expect(prefsStorageOf(hostile as { localStorage?: PrefsStorage })).toBeNull();
    expect(prefsStorageOf(undefined)).toBeNull();
  });
});

describe("useUiPrefs", () => {
  let mounted: Mounted | null = null;
  let restore: (() => void) | null = null;

  afterEach(() => {
    mounted?.unmount();
    mounted = null;
    restore?.();
    restore = null;
    window.localStorage.removeItem(UI_PREFS_KEY);
  });

  type Latest = { prefs: UiPrefs; update: (patch: Partial<UiPrefs>) => void };

  // A component that exposes the hook's current value, so a test can call its update.
  function renderHook(): { latest(): Latest } {
    const seen: { current: Latest | null } = { current: null };
    function Probe() {
      const [prefs, update] = useUiPrefs();
      seen.current = { prefs, update };
      return null;
    }
    mounted = mount(createElement(Probe));
    return {
      latest() {
        if (!seen.current) throw new Error("the probe did not render");
        return seen.current;
      },
    };
  }

  it("starts from the stored value, and persists each update", () => {
    window.localStorage.setItem(UI_PREFS_KEY, JSON.stringify(VALID));
    const hook = renderHook();
    expect(hook.latest().prefs).toEqual(VALID);
    act(() => hook.latest().update({ railCollapsed: false }));
    expect(hook.latest().prefs).toEqual({ ...VALID, railCollapsed: false });
    expect(JSON.parse(window.localStorage.getItem(UI_PREFS_KEY) ?? "null")).toEqual({ ...VALID, railCollapsed: false });
  });

  it("keeps each update for the session when storage refuses access, and never throws", () => {
    restore = refuseWindowStorage();
    const hook = renderHook();
    expect(hook.latest().prefs).toEqual(PINNED_DEFAULTS);
    expect(() => act(() => hook.latest().update({ inspectorCollapsed: true, panel: "recovery" }))).not.toThrow();
    expect(hook.latest().prefs).toEqual({ ...PINNED_DEFAULTS, inspectorCollapsed: true, panel: "recovery" });
  });

  it("applies two updates in one batch, from the latest value of each", () => {
    const hook = renderHook();
    act(() => {
      hook.latest().update({ railCollapsed: true });
      hook.latest().update({ inspectorCollapsed: true });
    });
    expect(hook.latest().prefs).toEqual({ ...PINNED_DEFAULTS, railCollapsed: true, inspectorCollapsed: true });
  });
});
