// TEST-ONLY loader for the legacy renderer, the reference for every parity check.
// The legacy module is loaded unchanged from its file. It builds DOM through the
// global `document`, so withGlobalDocument installs the shim for the duration of
// each call and restores whatever was there before.

import { type ShimDocument, type ShimElement, childrenFingerprint, createShimDocument } from "./dom-shim";

const LEGACY_URL = new URL("../../../../internal/webchat/assets/markdown.mjs", import.meta.url);

export interface LegacyStream {
  update(text: string): { reused: number; rendered: number };
}

export interface LegacyMarkdown {
  readonly MAX_MARKDOWN_CHARS: number;
  readonly MAX_BLOCKS: number;
  readonly scanWork: { steps: number };
  safeHref(raw: string): string | null;
  splitBlocks(text: string): string[];
  createIncrementalRenderer(container: ShimElement): LegacyStream;
}

function isLegacyModule(value: unknown): value is LegacyMarkdown {
  return (
    typeof value === "object" &&
    value !== null &&
    "MAX_MARKDOWN_CHARS" in value &&
    "MAX_BLOCKS" in value &&
    "scanWork" in value &&
    "safeHref" in value &&
    typeof value.safeHref === "function" &&
    "splitBlocks" in value &&
    typeof value.splitBlocks === "function" &&
    "createIncrementalRenderer" in value &&
    typeof value.createIncrementalRenderer === "function"
  );
}

let loaded: Promise<LegacyMarkdown> | null = null;

export function loadLegacy(): Promise<LegacyMarkdown> {
  loaded ??= import(LEGACY_URL.href).then((module: unknown) => {
    if (!isLegacyModule(module)) throw new Error(`legacy markdown module has an unexpected shape: ${LEGACY_URL.href}`);
    return module;
  });
  return loaded;
}

export function withGlobalDocument<T>(doc: ShimDocument, run: () => T): T {
  const previous = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", { value: doc, configurable: true, writable: true });
  try {
    return run();
  } finally {
    if (previous) Object.defineProperty(globalThis, "document", previous);
    else Reflect.deleteProperty(globalThis, "document");
  }
}

export interface LegacyRender {
  readonly fingerprint: string;
  readonly reused: number;
  readonly rendered: number;
  // Positions the look-ahead examined for this render (the legacy scanWork delta).
  readonly work: number;
}

// renderLegacy renders `text` once into a fresh container, as a non-streaming view would.
export function renderLegacy(legacy: LegacyMarkdown, text: string): LegacyRender {
  const doc = createShimDocument();
  const container = doc.createElement("div");
  const before = legacy.scanWork.steps;
  const stats = withGlobalDocument(doc, () => legacy.createIncrementalRenderer(container).update(text));
  return {
    fingerprint: childrenFingerprint(container),
    reused: stats.reused,
    rendered: stats.rendered,
    work: legacy.scanWork.steps - before,
  };
}

// legacyStream owns one container and one renderer, so updates reuse DOM as the
// message grows.
export function legacyStream(legacy: LegacyMarkdown): { container: ShimElement; update(text: string): { reused: number; rendered: number } } {
  const doc = createShimDocument();
  const container = doc.createElement("div");
  const renderer = legacy.createIncrementalRenderer(container);
  return {
    container,
    update: (text: string) => withGlobalDocument(doc, () => renderer.update(text)),
  };
}
