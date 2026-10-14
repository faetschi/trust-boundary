// Differential check: the legacy .mjs modules (internal/webchat/assets) and their TypeScript ports
// receive the same seeded inputs and must return the same outputs. The store is compared in full
// after every step. Nothing here is random without a seed: every corpus comes from Rng(seed).
//
// The legacy files are loaded by path, not by import, so this file needs no declarations for them.
// VITE_WEBCHAT_LEGACY_ASSETS overrides the directory. The mutation receipt in phase2-parity.md uses
// that override to point at a copy with one function changed; the repository copy is never edited.
import { describe, expect, test, vi } from "vitest";
import assert from "node:assert/strict";

// The corpora are large by design. They take 3 to 5 seconds on their own and more when the suite runs in
// parallel, so the default 5-second budget would fail the run without a comparison failing.
vi.setConfig({ testTimeout: 120_000 });

import * as formatTs from "./format";
import * as filtersTs from "./filters";
import * as storeTs from "./store";
import * as trajectoryTs from "./trajectory";
import * as gateTs from "./verification";
import * as anchorTs from "./anchor";
import * as backoffTs from "./backoff";
import * as stopTs from "./stop-sequence";
import { createFakeTimers } from "./testing/fake-timers";
import { activityItem, decisionItem, messageItem, panelsDefault, resultItem, sessionDefault, snapshotOf, streamDefault, TIMESTAMP } from "./testing/builders";
import {
  ITEM_TYPES,
  RESULT_STATES,
  SOURCE_CLASSES,
  TONES,
  type Authority,
  type Item,
  type ItemView,
  type Panels,
  type Role,
  type SessionHeader,
  type Snapshot,
  type StreamStatus,
  type Tool,
  type Verdict,
  type VerificationScope,
} from "./contract";
import type { ConnectionState, StoredGap, Workspace } from "./store";
import type { FilterId } from "./filters";
import type { CancelTarget, ContextEvent, KeyEvent, SequenceState } from "./stop-sequence";

const ASSETS: string =
  import.meta.env.VITE_WEBCHAT_LEGACY_ASSETS || new URL("../../../internal/webchat/assets", import.meta.url).pathname;

// Loads one legacy module and checks each named export at run time. The legacy files are untyped
// JavaScript, so the loaded namespace is typed as its TypeScript port: both sides then take the same
// arguments. This cast is the one boundary cast in the differential suite. The check above it is the
// reason it is safe.
async function legacy<T extends object>(file: string, names: readonly (keyof T & string)[]): Promise<T> {
  const mod: unknown = await import(/* @vite-ignore */ `${ASSETS}/${file}`);
  if (typeof mod !== "object" || mod === null) throw new Error(`legacy ${file} did not load as a module`);
  for (const name of names) {
    if (!(name in mod)) throw new Error(`legacy ${file} has no export ${name}`);
  }
  return mod as T;
}

// mulberry32: a small, seeded PRNG. Every corpus takes its own seed.
class Rng {
  private state: number;

  constructor(seed: number) {
    this.state = seed >>> 0;
  }

  next(): number {
    this.state = (this.state + 0x6d2b79f5) >>> 0;
    let t = this.state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  }

  int(maxExclusive: number): number {
    return Math.floor(this.next() * maxExclusive);
  }

  bool(p = 0.5): boolean {
    return this.next() < p;
  }

  // Picks by position, so a list may legitimately hold undefined or null as an element.
  pick<T>(values: readonly T[]): T {
    if (values.length === 0) throw new Error("pick from an empty list");
    const index = this.int(values.length);
    for (const [position, value] of values.entries()) {
      if (position === index) return value;
    }
    throw new Error("unreachable: pick index outside the list");
  }
}

// Text drawn from characters that exercise the normalizers: case, separators, markup, controls,
// C1 controls, non-ASCII, a surrogate pair, a lone surrogate, and prototype-key words.
const CHARS: readonly string[] = [..."abcdefXYZ0189 -_:./\\<>&\"'$#%\t\n\u0007\u007f\u0085é中😀\ud83d"];
const WORDS: readonly string[] = ["constructor", "__proto__", "toString", "hasOwnProperty", "SUCCEEDED", "Succeeded ", "unknown", "lost"];

function randomText(rng: Rng, maxLength = 18): string {
  if (rng.bool(0.08)) return rng.pick(WORDS);
  const length = rng.int(maxLength + 1);
  let text = "";
  for (let i = 0; i < length; i += 1) text += rng.pick(CHARS);
  return text;
}

// A tally of how many comparisons each corpus made, so the report can state the sizes.
const counts: Record<string, number> = {};
function tally(group: string, n = 1): void {
  counts[group] = (counts[group] ?? 0) + n;
}

// Branches the store corpus reached, so a corpus that never hits a path cannot pass silently.
const coverage: Record<string, number> = {};
function cover(name: string): void {
  coverage[name] = (coverage[name] ?? 0) + 1;
}

// Compare two values with the strict deep equality of node:assert. Functions compare by reference, so a
// prototype lookup that returns a function is compared, not skipped.
function same(label: string, expected: unknown, actual: unknown): void {
  assert.deepEqual(actual, expected, label);
}

const legacyFormat = await legacy<typeof formatTs>("format.mjs", [
  "SUCCESS_STATES",
  "TOOL_GLYPH",
  "slug",
  "humanize",
  "isVerifiedProjection",
  "stateChipClass",
  "verdictChipClass",
  "toolGlyph",
  "shortDigest",
  "formatClock",
  "formatStamp",
]);
const legacyFilters = await legacy<typeof filtersTs>("filters.mjs", [
  "FILTERS",
  "matchesFilter",
  "countFilter",
  "filterItems",
  "governanceCounts",
]);
const legacyStore = await legacy<typeof storeTs>("store.mjs", [
  "MAX_CLIENT_ITEMS",
  "VIEWS",
  "createWorkspace",
  "setView",
  "setGroup",
  "setQuery",
  "selectRecord",
  "clearRecord",
  "applySnapshot",
  "applyItem",
  "applySession",
  "noteGap",
  "setConnection",
  "setError",
  "setFilter",
  "groupTurn",
  "turnsOf",
]);
// The legacy item-only predicate. The port names it isItemEvidenceOnly (verification.ts): the same judgement, renamed so
// that nothing outside the differential suite can mistake it for the verified gate.
const legacyItemOnly = await legacy<{ isVerified: (item: unknown) => boolean }>("format.mjs", ["isVerified"]);

const legacyTrajectory = await legacy<typeof trajectoryTs>("trajectory.mjs", [
  "RECORD_GROUPS",
  "MAX_PREVIEW",
  "recordSource",
  "sourceLabel",
  "preview",
  "recordDot",
  "statusText",
  "recordLabel",
  "recordPayload",
  "recordChips",
  "timingOf",
  "toneText",
  "weightClass",
  "sequenceLanes",
  "searchRecords",
  "groupRecords",
  "recordDetail",
  "recordOf",
  "visibleRecords",
]);
const legacyAnchor = await legacy<typeof anchorTs>("anchor.mjs", [
  "TAIL_SLACK_PX",
  "maxScrollTop",
  "isAtTail",
  "restoredScrollTop",
  "pendingSince",
  "repin",
  "jumpLabel",
  "tailAnnouncement",
]);
const legacyBackoff = await legacy<typeof backoffTs>("backoff.mjs", ["BACKOFF_DEFAULTS", "createBackoff", "createScheduler"]);
const legacyStop = await legacy<typeof stopTs>("stop-sequence.mjs", [
  "DEFAULT_WINDOW_MS",
  "CONFIRMED_TEXT",
  "CLEARED_TEXT",
  "armedText",
  "initialState",
  "step",
]);

// ---------------------------------------------------------------------------------------------
// A. format helpers
// ---------------------------------------------------------------------------------------------
describe("differential: format helpers", () => {
  test("every helper returns the legacy result on a seeded corpus", () => {
    const rng = new Rng(0x5eed0001);
    const states: string[] = [...RESULT_STATES, "completed", "published", "ready", "cancelled", "unavailable", "unverified", "reported", "", ...WORDS];
    const sources: (string | undefined)[] = [...SOURCE_CLASSES, "live", undefined];

    assert.deepEqual(formatTs.SUCCESS_STATES, legacyFormat.SUCCESS_STATES);
    assert.deepEqual(formatTs.TOOL_GLYPH, legacyFormat.TOOL_GLYPH);
    tally("format", 2);

    const values: unknown[] = [undefined, null, 0, 1, -1, 3.5, Number.NaN, Number.POSITIVE_INFINITY, true, false, "", " "];
    for (let i = 0; i < 3000; i += 1) values.push(randomText(rng));
    for (const value of values) {
      same(`slug(${String(value)})`, legacyFormat.slug(value), formatTs.slug(value));
      same(`humanize(${String(value)})`, legacyFormat.humanize(value), formatTs.humanize(value));
      same("verdictChipClass", legacyFormat.verdictChipClass(value), formatTs.verdictChipClass(value));
      same("shortDigest", legacyFormat.shortDigest(value), formatTs.shortDigest(value));
      tally("format", 4);
    }

    const authorities: (string | undefined)[] = ["observed", "diagnostic", "unknown", "authoritative", "", "x", undefined];
    const scopes: (string | undefined)[] = ["structural-audit-only", "unverified-input", "unavailable", "", "x", undefined];
    for (let i = 0; i < 2000; i += 1) {
      const item = { authority: rng.pick(authorities), verification_scope: rng.pick(scopes) };
      same("isItemEvidenceOnly", legacyItemOnly.isVerified(item), gateTs.isItemEvidenceOnly(item));
      tally("format");
    }
    same("isItemEvidenceOnly(null)", legacyItemOnly.isVerified(null), gateTs.isItemEvidenceOnly(null));
    same("isItemEvidenceOnly(undefined)", legacyItemOnly.isVerified(undefined), gateTs.isItemEvidenceOnly(undefined));
    tally("format", 2);

    for (let i = 0; i < 1000; i += 1) {
      const state = { source: rng.pick(sources), verification: rng.pick([...scopes, "live"]) };
      same("isVerifiedProjection", legacyFormat.isVerifiedProjection(state), formatTs.isVerifiedProjection(state));
      tally("format");
    }

    for (let i = 0; i < 3000; i += 1) {
      const state = rng.bool(0.2) ? randomText(rng) : rng.pick(states);
      const verified = rng.bool();
      same("stateChipClass", legacyFormat.stateChipClass(state, verified), formatTs.stateChipClass(state, verified));
      tally("format");
    }

    const tools: (string | null | undefined)[] = ["read", "write", "edit", "bash", "exec", "", null, undefined, "constructor"];
    for (let i = 0; i < 500; i += 1) {
      const tool = rng.bool(0.7) ? rng.pick(tools) : randomText(rng, 6);
      same("toolGlyph", legacyFormat.toolGlyph(tool), formatTs.toolGlyph(tool));
      tally("format");
    }

    // Dates: ISO strings across 1900..2100, epoch milliseconds, invalid text, and the falsy values.
    const dates: (string | number | Date | null | undefined)[] = [undefined, null, 0, "", "not a date", Number.NaN];
    for (let i = 0; i < 800; i += 1) {
      const ms = Date.UTC(1900, 0, 1) + rng.int(200 * 365 * 24 * 3600 * 1000);
      dates.push(new Date(ms).toISOString(), ms, new Date(ms));
    }
    for (const date of dates) {
      same("formatClock", legacyFormat.formatClock(date), formatTs.formatClock(date));
      same("formatStamp", legacyFormat.formatStamp(date), formatTs.formatStamp(date));
      tally("format", 2);
    }
  });
});

// ---------------------------------------------------------------------------------------------
// B. filters and governance counts
// ---------------------------------------------------------------------------------------------
function randomItemView(rng: Rng, index: number): ItemView {
  const tools: Tool[] = ["read", "write", "edit", "bash"];
  const roles: Role[] = ["user", "assistant", "system"];
  const verdicts: Verdict[] = ["ALLOW", "DENY", "UNKNOWN"];
  const authorities: Authority[] = ["observed", "diagnostic", "unknown"];
  const scopes: VerificationScope[] = ["structural-audit-only", "unverified-input", "unavailable"];
  return {
    id: rng.bool(0.97) ? `i${index}` : undefined,
    type: rng.pick([...ITEM_TYPES, undefined]),
    turn_id: rng.bool(0.8) ? `t${rng.int(3)}` : undefined,
    tool_call_id: rng.bool(0.75) ? `c${rng.int(6)}` : undefined,
    tool: rng.bool(0.8) ? rng.pick(tools) : undefined,
    state: rng.bool(0.7) ? rng.pick(RESULT_STATES) : undefined,
    verdict: rng.bool(0.6) ? rng.pick(verdicts) : undefined,
    lossy: rng.bool(0.2) ? true : rng.bool(0.5) ? false : undefined,
    role: rng.bool(0.5) ? rng.pick(roles) : undefined,
    kind: rng.bool(0.4) ? "proposal" : undefined,
    text: rng.bool(0.4) ? randomText(rng, 12) : undefined,
    summary: rng.bool(0.5) ? randomText(rng, 16) : undefined,
    detail: rng.bool(0.2) ? randomText(rng, 16) : undefined,
    reason_code: rng.bool(0.5) ? "policy_rule_allow" : undefined,
    policy_digest: rng.bool(0.4) ? `sha256:${rng.int(999)}` : undefined,
    digest: rng.bool(0.4) ? `sha256:${rng.int(999)}ab` : undefined,
    authority: rng.bool(0.6) ? rng.pick(authorities) : undefined,
    verification_scope: rng.bool(0.6) ? rng.pick(scopes) : undefined,
    sequence: index + 1,
    occurred_at: TIMESTAMP,
    text_truncated: rng.bool(0.05) ? true : undefined,
    schema_version: "tbound-webchat-observation/v1",
  };
}

describe("differential: filters and governance counts", () => {
  test("matchesFilter, filterItems, countFilter and governanceCounts agree on seeded timelines", () => {
    const rng = new Rng(0x5eed0002);
    const filterIds: string[] = [...legacyFilters.FILTERS.map((entry) => entry.id), "bogus", ""];
    same("FILTERS", legacyFilters.FILTERS, filtersTs.FILTERS);
    tally("filters");

    const predicate = (item: ItemView): boolean => ((item.id?.length ?? 0) + (item.sequence ?? 0)) % 3 === 0;
    for (let round = 0; round < 500; round += 1) {
      const length = rng.int(31);
      const timeline: ItemView[] = [];
      for (let i = 0; i < length; i += 1) timeline.push(randomItemView(rng, i));
      for (const item of timeline) {
        for (const filter of filterIds) {
          same("matchesFilter", legacyFilters.matchesFilter(item, filter), filtersTs.matchesFilter(item, filter));
          tally("filters");
        }
      }
      for (const filter of filterIds) {
        same("filterItems", legacyFilters.filterItems(timeline, filter), filtersTs.filterItems(timeline, filter));
        same("countFilter", legacyFilters.countFilter(timeline, filter), filtersTs.countFilter(timeline, filter));
        tally("filters", 2);
      }
      same("governanceCounts(predicate)", legacyFilters.governanceCounts(timeline, predicate), filtersTs.governanceCounts(timeline, predicate));
      same("governanceCounts(none)", legacyFilters.governanceCounts(timeline), filtersTs.governanceCounts(timeline));
      tally("filters", 2);
    }
    same("matchesFilter(null)", legacyFilters.matchesFilter(null, "denied"), filtersTs.matchesFilter(null, "denied"));
    tally("filters");
  });
});

// ---------------------------------------------------------------------------------------------
// C. store reducers: the full state after every step
// ---------------------------------------------------------------------------------------------
type StoreEvent =
  | { readonly kind: "snapshot"; readonly snapshot: Snapshot }
  | { readonly kind: "item"; readonly item: Item }
  | { readonly kind: "session"; readonly session?: SessionHeader; readonly panels?: Panels; readonly streams?: readonly StreamStatus[] | null }
  | { readonly kind: "gap"; readonly gap: StoredGap | null | undefined }
  | { readonly kind: "connection"; readonly value: ConnectionState }
  | { readonly kind: "error"; readonly message: string | null }
  | { readonly kind: "view"; readonly value: unknown }
  | { readonly kind: "group"; readonly value: unknown }
  | { readonly kind: "query"; readonly value: unknown }
  | { readonly kind: "select"; readonly id: string | null | undefined }
  | { readonly kind: "clear" }
  | { readonly kind: "filter"; readonly value: FilterId };

// A snapshot sets the store's sequence to its highest item, so `last` is that item's sequence, not a max with
// the previous value (applySnapshot does not keep the old sequence).
function snapshotEvent(rng: Rng): { event: StoreEvent; last: number } {
  const big = rng.bool(0.03);
  const count = big ? 520 + rng.int(60) : rng.int(24);
  const contiguous = rng.bool(0.7);
  const items: Item[] = [];
  let sequence = 0;
  for (let i = 0; i < count; i += 1) {
    sequence = contiguous ? i + 1 : sequence + 1 + rng.int(3);
    // Occasionally repeat an id, so the duplicate-id branch runs.
    const id = rng.bool(0.04) && i > 0 ? `s${i}` : `s${sequence}`;
    items.push(messageItem({ id, sequence, text: `m${sequence}`, turn_id: "t1" }));
  }
  const gaps = rng.bool(0.3)
    ? [{ id: "g1", reason: rng.pick(["history_evicted", "cursor_ahead"]), first_missing: 1 + rng.int(5), last_missing: 6 + rng.int(5), recorded_at: TIMESTAMP }]
    : [];
  const streams = rng.bool(0.3) ? [streamDefault({ id: "fixture-session" })] : [];
  const snapshot = snapshotOf({ epoch: "e1", items, gaps, streams, warnings: rng.bool(0.3) ? ["w"] : [] });
  return { event: { kind: "snapshot", snapshot }, last: sequence };
}

function storeEvent(rng: Rng, state: { last: number }, allowSnapshots = true): StoreEvent {
  const roll = rng.next();
  if (allowSnapshots && roll < 0.02) {
    const { event, last } = snapshotEvent(rng);
    state.last = last;
    return event;
  }
  if (roll < 0.62) {
    // Items: mostly the next sequence, sometimes a duplicate, a gap, or an invalid sequence.
    const shape = rng.next();
    let sequence = state.last + 1;
    if (shape < 0.1) sequence = Math.max(1, state.last - rng.int(3));
    else if (shape < 0.2) sequence = state.last + 2 + rng.int(4);
    else if (shape < 0.22) sequence = Number.NaN;
    else if (shape < 0.24) sequence = 1.5;
    // Advance the model only on an item the store will accept. A rejected gap must not move it, or every later item would be a gap too.
    if (sequence === state.last + 1) state.last = sequence;
    // Ids are unique by sequence, so only the deliberate duplicate branch produces a duplicate.
    const id = `e${sequence}`;
    const type = rng.next();
    if (type < 0.3) return { kind: "item", item: messageItem({ id, sequence, text: `m${sequence}`, role: "assistant" }) };
    if (type < 0.5) return { kind: "item", item: activityItem({ id, sequence, tool: "bash", tool_call_id: `c${sequence}`, state: rng.pick(RESULT_STATES) }) };
    if (type < 0.7) return { kind: "item", item: decisionItem({ id, sequence, tool: "read", tool_call_id: `c${sequence}`, verdict: rng.pick<Verdict>(["ALLOW", "DENY", "UNKNOWN"]) }) };
    return {
      kind: "item",
      item: resultItem({
        id,
        sequence,
        tool: "edit",
        tool_call_id: `c${sequence}`,
        state: rng.pick(RESULT_STATES),
        lossy: rng.bool(0.2) ? true : undefined,
        authority: rng.bool(0.3) ? "observed" : "diagnostic",
        verification_scope: rng.bool(0.3) ? "structural-audit-only" : "unverified-input",
      }),
    };
  }
  if (roll < 0.7) {
    const session = rng.bool(0.6) ? sessionDefault() : undefined;
    const panels = rng.bool(0.4) ? panelsDefault() : undefined;
    const streams = rng.bool(0.4) ? [streamDefault({ id: "s" })] : rng.bool(0.5) ? null : undefined;
    return { kind: "session", session, panels, streams };
  }
  if (roll < 0.76) {
    const gap: StoredGap | null | undefined = rng.bool(0.1)
      ? (rng.bool() ? null : undefined)
      : rng.bool(0.5)
        ? { reason: rng.pick(["sequence_gap", "history_evicted", "replay_gap"]), requested_cursor: "e1:3", latest: rng.int(40) }
        : { id: "g", reason: "history_evicted", first_missing: rng.int(20), last_missing: 30 + rng.int(5), recorded_at: TIMESTAMP };
    return { kind: "gap", gap };
  }
  if (roll < 0.8) return { kind: "connection", value: rng.pick<ConnectionState>(["connecting", "open", "reconnecting", "offline", "error"]) };
  if (roll < 0.82) return { kind: "error", message: rng.bool(0.3) ? null : randomText(rng, 10) };
  if (roll < 0.86) return { kind: "view", value: rng.pick<unknown>(["chat", "trajectory", "other", "", null, undefined, 3]) };
  if (roll < 0.9) return { kind: "group", value: rng.pick<unknown>(["turn", "tool", "verdict", "", null, undefined, "x"]) };
  if (roll < 0.93) return { kind: "query", value: rng.pick<unknown>(["", "read", null, undefined, 42, "  EDIT "]) };
  if (roll < 0.97) return { kind: "select", id: rng.pick<string | null | undefined>(["e1", "e2", "s3", "", null, undefined]) };
  if (roll < 0.98) return { kind: "clear" };
  return { kind: "filter", value: rng.pick<FilterId>(["all", "denied", "unknown", "failed"]) };
}

// The store's own state with its Map flattened, so node:assert compares every field, Map contents
// included, in insertion order.
function normalize(state: Workspace): Record<string, unknown> {
  return { ...state, itemsById: [...state.itemsById.entries()] };
}

function observeStore(state: Workspace, outcome: unknown): void {
  if (typeof outcome === "string") cover(`item:${outcome}`);
  if (state.windowGap) cover(`windowGap:${state.windowGap.kind}`);
  if (state.evicted > 0) cover("evicted");
  if (state.items.length >= storeTs.MAX_CLIENT_ITEMS) cover("window-at-cap");
}

function applyStore(api: typeof storeTs, state: Workspace, event: StoreEvent): unknown {
  switch (event.kind) {
    case "snapshot":
      api.applySnapshot(state, event.snapshot);
      return undefined;
    case "item":
      return api.applyItem(state, event.item);
    case "session":
      api.applySession(state, event.session, event.panels, event.streams);
      return undefined;
    case "gap":
      api.noteGap(state, event.gap);
      return undefined;
    case "connection":
      api.setConnection(state, event.value);
      return undefined;
    case "error":
      api.setError(state, event.message);
      return undefined;
    case "view":
      api.setView(state, event.value);
      return undefined;
    case "group":
      api.setGroup(state, event.value);
      return undefined;
    case "query":
      api.setQuery(state, event.value);
      return undefined;
    case "select":
      api.selectRecord(state, event.id);
      return undefined;
    case "clear":
      api.clearRecord(state);
      return undefined;
    case "filter":
      api.setFilter(state, event.value);
      return undefined;
  }
}

describe("differential: store reducers", () => {
  test("the full store state matches the legacy state after every step of seeded runs", () => {
    const rng = new Rng(0x5eed0003);
    for (let run = 0; run < 120; run += 1) {
      const legacyState = legacyStore.createWorkspace();
      const tsState = storeTs.createWorkspace();
      const generator = { last: 0 };
      for (let step = 0; step < 250; step += 1) {
        const event = storeEvent(rng, generator);
        const expected = applyStore(legacyStore, legacyState, event);
        const actual = applyStore(storeTs, tsState, event);
        same(`run ${run} step ${step} outcome`, expected, actual);
        same(`run ${run} step ${step} state`, normalize(legacyState), normalize(tsState));
        tally("store");
        observeStore(tsState, actual);
        if (step % 10 === 0) {
          // Both grouping helpers on the store's own item list, as the view calls them.
          same("groupTurn", legacyStore.groupTurn(tsState.items), storeTs.groupTurn(tsState.items));
          same("turnsOf", legacyStore.turnsOf(tsState.items), storeTs.turnsOf(tsState.items));
          tally("store", 2);
        }
      }
    }
  });

  test("the eviction and window-gap paths match over long runs of 512-plus items", () => {
    const rng = new Rng(0x5eed0004);
    for (let run = 0; run < 3; run += 1) {
      const legacyState = legacyStore.createWorkspace();
      const tsState = storeTs.createWorkspace();
      const generator = { last: 0 };
      // Run 0 has no snapshots, so the client cap trims with no earlier server loss (the client window gap).
      // Runs 1 and 2 take a snapshot every 300 steps, which sets server losses and drops elements.
      const allowSnapshots = run !== 0;
      for (let step = 0; step < 1400; step += 1) {
        let event: StoreEvent;
        if (allowSnapshots && step % 300 === 299) {
          const snapshot = snapshotEvent(rng);
          generator.last = snapshot.last;
          event = snapshot.event;
        } else {
          event = storeEvent(rng, generator, allowSnapshots);
        }
        const expected = applyStore(legacyStore, legacyState, event);
        const actual = applyStore(storeTs, tsState, event);
        same(`long run ${run} step ${step} outcome`, expected, actual);
        same(`long run ${run} step ${step} state`, normalize(legacyState), normalize(tsState));
        tally("store");
        observeStore(tsState, actual);
      }
      expect(tsState.evicted).toBe(legacyState.evicted);
    }
  });
});

// ---------------------------------------------------------------------------------------------
// D. trajectory projections
// ---------------------------------------------------------------------------------------------
// The projection the corpus is judged in. Inside it the legacy item-only judgement and the port agree exactly; outside it
// the port is stricter, and section F holds that difference (D1). The legacy modules take no projection: they receive the
// same argument, ignore it, and judge the item alone, which is the judgement the live projection makes identical.
const LIVE = { source: "live", verification: "structural-audit-only" } as const;

describe("differential: trajectory projections", () => {
  test("every projection returns the legacy result on seeded item lists", () => {
    const rng = new Rng(0x5eed0005);
    same("RECORD_GROUPS", legacyTrajectory.RECORD_GROUPS, trajectoryTs.RECORD_GROUPS);
    same("MAX_PREVIEW", legacyTrajectory.MAX_PREVIEW, trajectoryTs.MAX_PREVIEW);
    tally("trajectory", 2);

    const sources: string[] = ["decision", "result", "tool", "user", "assistant", "system", "message", "unknown", "context", "constructor", "", "x"];
    const tones: string[] = [...TONES, "muted", "", "x", "constructor"];
    const weights = [Number.NaN, 0, 0.4, 1, 2.5, 4.5, 7.9, 9.6, -3, Number.POSITIVE_INFINITY];
    const queries: (string | null | undefined)[] = ["", " ", "read", "c1", "EDIT", "sha256", null, undefined, "t2", "constructor"];

    for (let round = 0; round < 260; round += 1) {
      const length = rng.int(26);
      const items: ItemView[] = [];
      for (let i = 0; i < length; i += 1) items.push(randomItemView(rng, i));
      const mode = rng.pick(["turn", "tool", "verdict", "other"]);
      const query = rng.pick(queries);

      for (const item of items) {
        same("recordSource", legacyTrajectory.recordSource(item), trajectoryTs.recordSource(item));
        same("recordDot", legacyTrajectory.recordDot(item, LIVE), trajectoryTs.recordDot(item, LIVE));
        same("statusText", legacyTrajectory.statusText(item, LIVE), trajectoryTs.statusText(item, LIVE));
        same("recordLabel", legacyTrajectory.recordLabel(item), trajectoryTs.recordLabel(item));
        same("recordPayload", legacyTrajectory.recordPayload(item), trajectoryTs.recordPayload(item));
        same("recordChips", legacyTrajectory.recordChips(item, LIVE), trajectoryTs.recordChips(item, LIVE));
        same("timingOf", legacyTrajectory.timingOf(item), trajectoryTs.timingOf(item));
        same("recordDetail", legacyTrajectory.recordDetail(item, LIVE), trajectoryTs.recordDetail(item, LIVE));
        same("recordOf", legacyTrajectory.recordOf(item, LIVE), trajectoryTs.recordOf(item, LIVE));
        tally("trajectory", 9);
      }
      for (const source of sources) {
        same("sourceLabel", legacyTrajectory.sourceLabel(source), trajectoryTs.sourceLabel(source));
        tally("trajectory");
      }
      for (let i = 0; i < 40; i += 1) {
        const text = rng.bool(0.5) ? randomText(rng, 200) : undefined;
        const max = rng.pick([0, 1, 5, 40, 160, 300]);
        same("preview", legacyTrajectory.preview(text, max), trajectoryTs.preview(text, max));
        tally("trajectory");
      }
      for (const tone of tones) {
        same("toneText", legacyTrajectory.toneText(tone), trajectoryTs.toneText(tone));
        tally("trajectory");
      }
      for (const weight of weights) {
        same("weightClass", legacyTrajectory.weightClass(weight), trajectoryTs.weightClass(weight));
        tally("trajectory");
      }
      same("sequenceLanes", legacyTrajectory.sequenceLanes(items, LIVE), trajectoryTs.sequenceLanes(items, LIVE));
      same("searchRecords", legacyTrajectory.searchRecords(items, query), trajectoryTs.searchRecords(items, query));
      tally("trajectory", 2);
      for (const groupMode of ["turn", "tool", "verdict", "other"]) {
        same(`groupRecords ${groupMode}`, legacyTrajectory.groupRecords(items, groupMode, LIVE), trajectoryTs.groupRecords(items, groupMode, LIVE));
        tally("trajectory");
      }
      // The view's query is a string or absent; null is covered through searchRecords above.
      const view = { items, query: typeof query === "string" ? query : undefined, group: mode };
      const judged = { ...view, projection: LIVE };
      same("visibleRecords", legacyTrajectory.visibleRecords(judged), trajectoryTs.visibleRecords(judged));
      tally("trajectory");
    }
  });
});

// ---------------------------------------------------------------------------------------------
// E. anchor arithmetic
// ---------------------------------------------------------------------------------------------
describe("differential: anchor arithmetic", () => {
  test("the tail, scroll and announcement functions agree on seeded metrics", () => {
    const rng = new Rng(0x5eed0006);
    same("TAIL_SLACK_PX", legacyAnchor.TAIL_SLACK_PX, anchorTs.TAIL_SLACK_PX);
    tally("anchor");
    const numbers = [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, -1, 0, 0.5];
    const real = (): number => (rng.bool(0.1) ? rng.pick(numbers) : rng.int(5000) - 500);
    const optional = (): number | null | undefined => (rng.bool(0.1) ? rng.pick<number | null | undefined>([null, undefined]) : real());
    for (let i = 0; i < 20000; i += 1) {
      const metrics = { scrollTop: real(), clientHeight: real(), scrollHeight: real() };
      same("maxScrollTop", legacyAnchor.maxScrollTop(metrics), anchorTs.maxScrollTop(metrics));
      same("isAtTail", legacyAnchor.isAtTail(metrics), anchorTs.isAtTail(metrics));
      const slack = rng.bool(0.2) ? rng.int(100) : undefined;
      same("isAtTail(slack)", legacyAnchor.isAtTail(metrics, slack), anchorTs.isAtTail(metrics, slack));
      const capture = { capturedScrollTop: real(), capturedOffset: optional(), currentScrollTop: real(), nextOffset: optional() };
      same("restoredScrollTop", legacyAnchor.restoredScrollTop(capture, metrics), anchorTs.restoredScrollTop(capture, metrics));
      const applied = real();
      const pinned = real();
      same("pendingSince", legacyAnchor.pendingSince(applied, pinned), anchorTs.pendingSince(applied, pinned));
      const atTail = rng.bool();
      same("repin", legacyAnchor.repin(applied, atTail, pinned), anchorTs.repin(applied, atTail, pinned));
      // Draw each argument once, so both implementations receive the same values.
      const labelCount = rng.int(4);
      same("jumpLabel", legacyAnchor.jumpLabel(labelCount), anchorTs.jumpLabel(labelCount));
      const announced = rng.int(5);
      const pending = rng.int(6);
      same("tailAnnouncement", legacyAnchor.tailAnnouncement(announced, pending), anchorTs.tailAnnouncement(announced, pending));
      tally("anchor", 7);
    }
  });
});

// ---------------------------------------------------------------------------------------------
// F. backoff delays and the scheduler's fire order
// ---------------------------------------------------------------------------------------------
describe("differential: backoff delays and scheduler", () => {
  test("delays with an injected random source, and timers, agree on seeded sequences", () => {
    const rng = new Rng(0x5eed0007);
    same("BACKOFF_DEFAULTS", legacyBackoff.BACKOFF_DEFAULTS, backoffTs.BACKOFF_DEFAULTS);
    tally("backoff");

    const draws = [Number.NaN, -2, 0, 1, 1.5, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY];
    for (let run = 0; run < 600; run += 1) {
      const options = {
        baseMs: rng.pick([0, 1, 500, 1000.5, Number.NaN]),
        factor: rng.pick([0, 0.5, 2, 3]),
        capMs: rng.pick([0, 250, 10000]),
      };
      const script: number[] = [];
      for (let i = 0; i < 60; i += 1) script.push(rng.bool(0.1) ? rng.pick(draws) : rng.next());
      // Each implementation gets its own replay of the same script, so their draws never interleave.
      const replay = (): (() => number) => {
        let cursor = 0;
        return () => {
          const value = script[cursor % script.length];
          cursor += 1;
          return value ?? 0;
        };
      };
      const legacyBo = legacyBackoff.createBackoff({ ...options, random: replay() });
      const tsBo = backoffTs.createBackoff({ ...options, random: replay() });
      for (let i = 0; i < 14; i += 1) {
        if (rng.bool(0.15)) {
          legacyBo.reset();
          tsBo.reset();
        }
        same("backoff.next", legacyBo.next(), tsBo.next());
        same("backoff.attempts", legacyBo.attempts, tsBo.attempts);
        tally("backoff", 2);
      }
    }

    for (let run = 0; run < 300; run += 1) {
      const legacyClock = createFakeTimers();
      const tsClock = createFakeTimers();
      const legacyScheduler = legacyBackoff.createScheduler({ setTimer: legacyClock.setTimeout, clearTimer: legacyClock.clearTimeout });
      const tsScheduler = backoffTs.createScheduler({ setTimer: tsClock.setTimeout, clearTimer: tsClock.clearTimeout });
      const legacyFired: string[] = [];
      const tsFired: string[] = [];
      for (let op = 0; op < 80; op += 1) {
        const roll = rng.next();
        if (roll < 0.45) {
          const delay = rng.int(900) - 50;
          const label = `t${op}`;
          legacyScheduler.schedule(delay, () => legacyFired.push(label));
          tsScheduler.schedule(delay, () => tsFired.push(label));
        } else if (roll < 0.6) {
          legacyScheduler.cancel();
          tsScheduler.cancel();
        } else {
          const ms = rng.int(400);
          legacyClock.advance(ms);
          tsClock.advance(ms);
        }
        same("scheduler.fired", legacyFired, tsFired);
        same("scheduler.pending", legacyScheduler.pending, tsScheduler.pending);
        same("clock.now", legacyClock.now, tsClock.now);
        same("clock.pendingCount", legacyClock.pendingCount, tsClock.pendingCount);
        tally("backoff", 4);
      }
    }
  });
});

// ---------------------------------------------------------------------------------------------
// G. stop-sequence step() over random event streams
// ---------------------------------------------------------------------------------------------
describe("differential: stop-sequence step()", () => {
  test("every outcome and state matches the legacy step() on seeded event streams", () => {
    const rng = new Rng(0x5eed0008);
    same("CONFIRMED_TEXT", legacyStop.CONFIRMED_TEXT, stopTs.CONFIRMED_TEXT);
    same("CLEARED_TEXT", legacyStop.CLEARED_TEXT, stopTs.CLEARED_TEXT);
    same("initialState", legacyStop.initialState(), stopTs.initialState());
    same("DEFAULT_WINDOW_MS", legacyStop.DEFAULT_WINDOW_MS, stopTs.DEFAULT_WINDOW_MS);
    for (const turnId of ["t1", "", "turn with spaces", "constructor"]) {
      same("armedText", legacyStop.armedText(turnId), stopTs.armedText(turnId));
    }
    tally("stop", 7);

    const keys = ["Escape", "Esc", "a", "Enter", "Tab", "", "escape"];
    const generations: (string | number | null | undefined)[] = [undefined, "g1", "", 1, 2, Number.NaN, Number.POSITIVE_INFINITY, null];
    const turnIds = ["t1", "t2", ""];
    const windows = [0, 1, 500, 2000, 10000];

    for (let stream = 0; stream < 300; stream += 1) {
      const windowMs = rng.pick(windows);
      let legacyState: SequenceState = legacyStop.initialState();
      let tsState: SequenceState = stopTs.initialState();
      let clock = 0;
      for (let step = 0; step < 150; step += 1) {
        clock += rng.int(3000) - 300;
        const target = rng.bool(0.15)
          ? rng.pick<CancelTarget | null | undefined>([null, undefined])
          : { sessionId: rng.bool(0.95) ? "s1" : "", turnId: rng.pick(turnIds), generation: rng.pick(generations) };
        const modal = rng.bool(0.1);
        const isModalActive = (): boolean => modal;
        const options = { windowMs, isModalActive };
        const event: KeyEvent | ContextEvent =
          rng.bool(0.8)
            ? {
                type: "key",
                at: clock,
                key: rng.pick(keys),
                target,
                inRegion: rng.bool(0.85),
                isComposing: rng.bool(0.03),
                keyCode: rng.pick<number | undefined>([undefined, 229, 27]),
                repeat: rng.bool(0.05),
                ctrlKey: rng.bool(0.03),
                altKey: rng.bool(0.02),
                metaKey: rng.bool(0.02),
                shiftKey: rng.bool(0.02),
              }
            : { type: "context", at: clock, target, inRegion: rng.bool(0.85) };
        const expected = legacyStop.step(legacyState, event, options);
        const actual = stopTs.step(tsState, event, options);
        same(`stream ${stream} step ${step} outcome`, expected, actual);
        legacyState = expected.state;
        tsState = actual.state;
        same(`stream ${stream} step ${step} state`, legacyState, tsState);
        tally("stop", 2);
      }
    }
  });
});

// ---------------------------------------------------------------------------------------------
// Corpus sizes. The totals are the comparisons made above; they are printed for the report.
// ---------------------------------------------------------------------------------------------
describe("differential: corpus", () => {
  test("the corpus is large, and every group compared something", () => {
    const groups = Object.keys(counts);
    const total = Object.values(counts).reduce((sum, value) => sum + value, 0);
    console.log(`differential corpus comparisons: ${JSON.stringify(counts)} total=${total}`);
    expect(groups.sort()).toEqual(["anchor", "backoff", "filters", "format", "stop", "store", "trajectory"]);
    expect(total).toBeGreaterThan(100000);
    console.log(`differential store coverage: ${JSON.stringify(coverage)}`);
    for (const branch of ["item:applied", "item:duplicate", "item:gap", "item:invalid", "windowGap:server", "windowGap:client", "evicted", "window-at-cap"]) {
      expect(coverage[branch] ?? 0, branch).toBeGreaterThan(0);
    }
  });
});

// ---------------------------------------------------------------------------------------------
// F. deliberate deviations: where the TypeScript port is stricter than the legacy module
// ---------------------------------------------------------------------------------------------
// Each deviation is listed in phase2-parity.md ("Deliberate deviations") with the same name. The corpus above
// is constrained to the live structural projection, where the legacy item-only judgement and the port agree.
// Everywhere else the port is stricter, and this block holds that difference to account.
describe("differential: deliberate deviations (stricter than legacy)", () => {
  const NON_LIVE_PROJECTIONS = [
    { source: "fixture", verification: "unverified-input" },
    { source: "fixture", verification: "structural-audit-only" },
    { source: "replay", verification: "structural-audit-only" },
    { source: "unavailable", verification: "unavailable" },
  ] as const;

  test("D1: a record the legacy item-only judgement reads as verified is not verified outside a live structural projection", () => {
    // Legacy trajectory.mjs judges a record from the record alone, so an observed structural success is "ok" wherever
    // it appears. The port asks the projection too. Evidence: trajectory.test.ts ("outside a live structural projection").
    const record = {
      id: "r1",
      sequence: 1,
      type: "result" as const,
      turn_id: "t1",
      occurred_at: "2026-10-13T10:00:00Z",
      tool: "bash" as const,
      tool_call_id: "c1",
      state: "succeeded" as const,
      authority: "observed" as const,
      verification_scope: "structural-audit-only" as const,
    };
    assert.equal(legacyTrajectory.recordDot(record, LIVE), "ok", "legacy: the record alone decides");
    assert.equal(trajectoryTs.recordDot(record, { source: "live", verification: "structural-audit-only" }), "ok", "agreement inside a live structural projection");
    for (const projection of NON_LIVE_PROJECTIONS) {
      assert.notEqual(trajectoryTs.recordDot(record, projection), "ok", `port, ${projection.source}/${projection.verification}`);
    }
  });
});
