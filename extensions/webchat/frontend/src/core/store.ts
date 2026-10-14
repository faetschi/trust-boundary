// store.ts — bounded client-side projection state (port of store.mjs).
//
// The client never invents success. Items are applied only in strictly increasing
// sequence order; duplicates are ignored; any discontinuity or replay gap forces the
// caller to recover from a fresh snapshot instead of assuming continuity.
//
// The store takes contract values. The defensive checks of the legacy module stay, so
// a value the types do not admit is still handled as it was before.

import type {
  ActivityItem,
  DecisionItem,
  GapRecord,
  Item,
  ItemView,
  Panels,
  ReplayGap,
  ResultItem,
  SessionHeader,
  Snapshot,
  SourceClass,
  StreamStatus,
  VerificationScope,
} from "./contract";
import type { FilterId } from "./filters";

export const MAX_CLIENT_ITEMS = 512;

export type ConnectionState = "connecting" | "open" | "reconnecting" | "offline" | "error";
export type ViewId = "chat" | "trajectory";
export const VIEWS: readonly ViewId[] = ["chat", "trajectory"];

// A gap as the store keeps it: a durable record from the snapshot, or a stream-level
// notice (the server's replay gap, or one the client records while recovering).
export type StoredGap = GapRecord | ReplayGap;

// windowGap says the loaded window is not provably complete from `since` onward.
export interface WindowGap {
  readonly kind: "server" | "client";
  readonly reason: string;
  readonly since: number;
}

export interface Workspace {
  source: SourceClass;
  verification: VerificationScope;
  epoch: string;
  cursor: string;
  session: SessionHeader | null;
  panels: Panels | null;
  streams: readonly StreamStatus[];
  warnings: readonly string[];
  gaps: readonly StoredGap[];
  items: Item[];
  itemsById: Map<string, Item>;
  lastSequence: number;
  // Counts records applied through applyItem, in order. The tail window measures "new"
  // records from this count, so gaps, duplicates and snapshot loads never add to it.
  applied: number;
  connection: ConnectionState;
  loaded: boolean;
  error: string | null;
  gapNotice: StoredGap | null;
  windowGap: WindowGap | null;
  // Set only while a connection attempt is slow; never a connection state itself.
  connectionWarning: string;
  filter: FilterId;
  evicted: number;
  // Trajectory view state. Presentation only: none of these fields can alter a
  // projection, a verdict, or a filter outcome.
  view: ViewId;
  group: string;
  query: string;
  selectedId: string | null;
}

export type ApplyOutcome = "applied" | "duplicate" | "gap" | "invalid";

export function createWorkspace(): Workspace {
  return {
    source: "unavailable",
    verification: "unavailable",
    epoch: "",
    cursor: "",
    session: null,
    panels: null,
    streams: [],
    warnings: [],
    gaps: [],
    items: [],
    itemsById: new Map(),
    lastSequence: 0,
    applied: 0,
    connection: "connecting",
    loaded: false,
    error: null,
    gapNotice: null,
    windowGap: null,
    connectionWarning: "",
    filter: "all",
    evicted: 0,
    view: "chat",
    group: "turn",
    query: "",
    selectedId: null,
  };
}

function isView(value: unknown): value is ViewId {
  return VIEWS.some((view) => view === value);
}

export function setView(state: Workspace, value: unknown): Workspace {
  state.view = isView(value) ? value : "chat";
  return state;
}

export function setGroup(state: Workspace, value: unknown): Workspace {
  state.group = typeof value === "string" && value ? value : "turn";
  return state;
}

export function setQuery(state: Workspace, value: unknown): Workspace {
  state.query = value === undefined || value === null ? "" : String(value);
  return state;
}

// Selecting a record is a local inspection action. Selecting the selected record again
// clears the selection.
export function selectRecord(state: Workspace, id: string | null | undefined): Workspace {
  state.selectedId = id && state.selectedId !== id ? id : null;
  return state;
}

export function clearRecord(state: Workspace): Workspace {
  state.selectedId = null;
  return state;
}

// The client keeps a bounded history. The server ring is bounded too, and an unbounded
// client would grow without limit once live events are appended. Returns the elements it
// trimmed, oldest first.
function enforceClientCap(state: Workspace): Item[] {
  if (state.items.length <= MAX_CLIENT_ITEMS) return [];
  const overflow = state.items.length - MAX_CLIENT_ITEMS;
  const removed = state.items.splice(0, overflow);
  for (const item of removed) state.itemsById.delete(item.id);
  state.evicted += removed.length;
  // An evicted record must not stay selected: the inspector would show nothing and the
  // next Escape would be swallowed dismissing a dangling selection.
  if (state.selectedId && !state.itemsById.has(state.selectedId)) state.selectedId = null;
  return removed;
}

// Gap reasons that say the source no longer retains earlier history.
const EVICTION_REASONS: ReadonlySet<string> = new Set(["history_evicted"]);

// The first loss opens the marker and keeps its kind and reason. A later loss can only
// move `since` earlier, because the earliest lost element bounds the history.
function noteWindowLoss(state: Workspace, kind: WindowGap["kind"], reason: string, since: number): void {
  const prior = state.windowGap;
  state.windowGap = prior ? { ...prior, since: Math.min(prior.since, since) } : { kind, reason, since };
}

// The first sequence a gap names, when it names one.
function firstMissingOf(gap: StoredGap): number | undefined {
  return "first_missing" in gap && typeof gap.first_missing === "number" ? gap.first_missing : undefined;
}

// A snapshot proves continuity only when it declares no eviction, drops nothing the client
// held, trims nothing, and restores an element at or before the earliest lost one. Anything
// less leaves the marker in place.
function updateWindowGap(
  state: Workspace,
  { evictions, dropped, trimmed }: { evictions: StoredGap[]; dropped: Item[]; trimmed: Item[] },
): void {
  const [firstEviction] = evictions;
  if (firstEviction) {
    const since = firstMissingOf(firstEviction) ?? firstLoadedSequence(state);
    noteWindowLoss(state, "server", firstEviction.reason, since);
  }
  if (dropped.length) {
    noteWindowLoss(state, "server", "history_evicted", Math.min(...dropped.map((item) => item.sequence)));
  }
  if (trimmed.length) {
    const [oldest] = trimmed;
    if (oldest) noteWindowLoss(state, "client", "retained_window", oldest.sequence);
  }
  if (!state.windowGap || evictions.length || dropped.length || trimmed.length) return;
  if (holdsFrom(state, state.windowGap.since)) state.windowGap = null;
}

// holdsFrom says whether the window holds every element from `since` through the last
// one, with no hole. Only then does a snapshot prove the lost history is back.
function holdsFrom(state: Workspace, since: number): boolean {
  const span = state.lastSequence - since + 1;
  if (!state.items.length || span < 1 || span > state.items.length) return false;
  const present = new Set(state.items.map((item) => item.sequence));
  for (let sequence = since; sequence <= state.lastSequence; sequence++) {
    if (!present.has(sequence)) return false;
  }
  return true;
}

// Where an eviction gap has no numeric range, the loss is taken to start at the oldest
// element the window holds, or at the first sequence when it holds none.
function firstLoadedSequence(state: Workspace): number {
  const [oldest] = state.items;
  return oldest ? oldest.sequence : 1;
}

export function setFilter(state: Workspace, value: FilterId): Workspace {
  state.filter = value;
  return state;
}

// The elements of a list that may be absent. A value that is not an array reads as empty.
function listOrEmpty<T>(value: readonly T[] | null | undefined): readonly T[] {
  return Array.isArray(value) ? value : [];
}

export function applySnapshot(state: Workspace, snapshot: Snapshot): Workspace {
  const priorItems = state.items;
  state.source = snapshot.source;
  state.verification = snapshot.verification_scope;
  state.epoch = snapshot.epoch;
  state.cursor = snapshot.cursor;
  state.session = snapshot.session;
  state.panels = snapshot.panels;
  state.streams = listOrEmpty(snapshot.streams);
  state.warnings = listOrEmpty(snapshot.warnings);
  state.gaps = listOrEmpty(snapshot.gaps);
  state.items = [];
  state.itemsById = new Map();
  state.lastSequence = 0;
  for (const item of listOrEmpty(snapshot.items)) {
    if (state.itemsById.has(item.id)) continue;
    state.items.push(item);
    state.itemsById.set(item.id, item);
    if (item.sequence > state.lastSequence) state.lastSequence = item.sequence;
  }
  // Elements the client retained but the new snapshot omits were dropped by the source
  // window. Count them as evicted so the retained-window disclosure stays truthful instead
  // of silently shrinking the record.
  const dropped = priorItems.filter((item) => !state.itemsById.has(item.id));
  state.evicted += dropped.length;
  const trimmed = enforceClientCap(state);
  // A selection that the new window no longer contains must not survive as a dangling
  // reference: the inspector would keep showing an evicted record.
  if (state.selectedId && !state.itemsById.has(state.selectedId)) state.selectedId = null;
  const evictions = state.gaps.filter((gap) => EVICTION_REASONS.has(gap.reason));
  updateWindowGap(state, { evictions, dropped, trimmed });
  state.loaded = true;
  state.error = null;
  state.gapNotice = null;
  return state;
}

// Returns "applied", "duplicate", "gap", or "invalid". An item with no string id or no
// integer sequence cannot be placed, so it is reported rather than dropped as a duplicate:
// the caller recovers from it and discloses that it did.
export function applyItem(state: Workspace, item: Item): ApplyOutcome {
  if (!item || typeof item.id !== "string" || !Number.isInteger(item.sequence)) return "invalid";
  if (state.itemsById.has(item.id)) return "duplicate";
  if (item.sequence <= state.lastSequence) return "duplicate";
  if (item.sequence !== state.lastSequence + 1) return "gap";
  state.items.push(item);
  state.itemsById.set(item.id, item);
  state.lastSequence = item.sequence;
  state.cursor = `${state.epoch}:${item.sequence}`;
  state.applied += 1;
  const trimmed = enforceClientCap(state);
  const [oldest] = trimmed;
  if (oldest) noteWindowLoss(state, "client", "retained_window", oldest.sequence);
  return "applied";
}

export function applySession(
  state: Workspace,
  session?: SessionHeader | null,
  panels?: Panels | null,
  streams?: readonly StreamStatus[] | null,
): void {
  if (session) state.session = session;
  if (panels) state.panels = panels;
  if (Array.isArray(streams)) state.streams = streams;
}

export function noteGap(state: Workspace, gap: StoredGap | null | undefined): void {
  if (gap && typeof gap === "object") {
    state.gapNotice = gap;
    state.gaps = [...state.gaps, gap];
  }
}

export function setConnection(state: Workspace, value: ConnectionState): void {
  state.connection = value;
}

export function setError(state: Workspace, message: string | null): void {
  state.error = message;
}

// A message block, or a tool block that correlates its activity, decision and result.
export interface MessageBlock {
  readonly kind: "message";
  readonly item: Item;
}

export interface ToolBlock {
  readonly kind: "tool";
  readonly key: string;
  activity: ActivityItem | null;
  decision: DecisionItem | null;
  result: ResultItem | null;
}

export type TurnBlock = MessageBlock | ToolBlock;

// Groups a turn's items into ordered message and tool blocks. Tool blocks correlate their
// activity, decision, and result by tool_call_id.
export function groupTurn(items: readonly Item[]): TurnBlock[] {
  const blocks: TurnBlock[] = [];
  const byTool = new Map<string, ToolBlock>();
  for (const item of items) {
    if (item.type === "message") {
      blocks.push({ kind: "message", item });
      continue;
    }
    const key = item.tool_call_id || item.id;
    let block = byTool.get(key);
    if (!block) {
      block = { kind: "tool", key, activity: null, decision: null, result: null };
      byTool.set(key, block);
      blocks.push(block);
    }
    if (item.type === "activity") block.activity = item;
    else if (item.type === "decision") block.decision = item;
    else if (item.type === "result") block.result = item;
  }
  // Attach in-flight tool blocks to the position of their first item so the transcript
  // order is preserved.
  return blocks;
}

export interface Turn<T extends ItemView = Item> {
  readonly id: string;
  readonly items: T[];
}

export function turnsOf<T extends ItemView>(items: readonly T[]): Turn<T>[] {
  const order: Turn<T>[] = [];
  const map = new Map<string, Turn<T>>();
  for (const item of items) {
    const key = item.turn_id || "__untagged";
    let turn = map.get(key);
    if (!turn) {
      turn = { id: key, items: [] };
      map.set(key, turn);
      order.push(turn);
    }
    turn.items.push(item);
  }
  return order;
}
