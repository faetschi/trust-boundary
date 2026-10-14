// store.js — bounded client-side projection state.
//
// The client never invents success. Items are applied only in strictly
// increasing sequence order; duplicates are ignored; any discontinuity or
// replay gap forces the caller to recover from a fresh snapshot instead of
// assuming continuity.

export const MAX_CLIENT_ITEMS = 512;

export function createWorkspace() {
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
    connection: "connecting",
    loaded: false,
    error: null,
    gapNotice: null,
    // windowGap says the loaded window is not provably complete from `since` onward:
    // { kind: "server" | "client", reason, since }. It is null when continuity holds.
    windowGap: null,
    // Set only while a connection attempt is slow; never a connection state itself.
    connectionWarning: "",
    filter: "all",
    evicted: 0,
    // Trajectory view state. Presentation only: none of these fields can alter a
    // projection, a verdict, or a filter outcome.
    view: "chat",
    group: "turn",
    query: "",
    selectedId: null,
  };
}

export const VIEWS = ["chat", "trajectory"];

export function setView(state, value) {
  state.view = VIEWS.includes(value) ? value : "chat";
  return state;
}

export function setGroup(state, value) {
  state.group = typeof value === "string" && value ? value : "turn";
  return state;
}

export function setQuery(state, value) {
  state.query = value === undefined || value === null ? "" : String(value);
  return state;
}

// Selecting a record is a local inspection action. Selecting the selected record
// again clears the selection.
export function selectRecord(state, id) {
  state.selectedId = id && state.selectedId !== id ? id : null;
  return state;
}

export function clearRecord(state) {
  state.selectedId = null;
  return state;
}

// The client keeps a bounded history. The server ring is bounded too, and an
// unbounded client would grow without limit once live events are appended.
// Returns the elements it trimmed, oldest first.
function enforceClientCap(state) {
  if (state.items.length <= MAX_CLIENT_ITEMS) return [];
  const overflow = state.items.length - MAX_CLIENT_ITEMS;
  const removed = state.items.splice(0, overflow);
  for (const item of removed) state.itemsById.delete(item.id);
  state.evicted += removed.length;
  // An evicted record must not stay selected: the inspector would show nothing
  // and the next Escape would be swallowed dismissing a dangling selection.
  if (state.selectedId && !state.itemsById.has(state.selectedId)) state.selectedId = null;
  return removed;
}

// Gap reasons that say the source no longer retains earlier history.
const EVICTION_REASONS = new Set(["history_evicted"]);

// The first loss opens the marker and keeps its kind and reason. A later loss can
// only move `since` earlier, because the earliest lost element bounds the history.
function noteWindowLoss(state, kind, reason, since) {
  const prior = state.windowGap;
  state.windowGap = prior
    ? { ...prior, since: Math.min(prior.since, since) }
    : { kind, reason, since };
}

// A snapshot proves continuity only when it declares no eviction, drops nothing
// the client held, trims nothing, and restores an element at or before the
// earliest lost one. Anything less leaves the marker in place.
function updateWindowGap(state, { evictions, dropped, trimmed }) {
  if (evictions.length) {
    const gap = evictions[0];
    const since = typeof gap.first_missing === "number" ? gap.first_missing : firstLoadedSequence(state);
    noteWindowLoss(state, "server", gap.reason, since);
  }
  if (dropped.length) {
    noteWindowLoss(state, "server", "history_evicted", Math.min(...dropped.map((item) => item.sequence)));
  }
  if (trimmed.length) {
    noteWindowLoss(state, "client", "retained_window", trimmed[0].sequence);
  }
  if (!state.windowGap || evictions.length || dropped.length || trimmed.length) return;
  const oldest = state.items.length ? state.items[0].sequence : Infinity;
  if (oldest <= state.windowGap.since) state.windowGap = null;
}

// Where an eviction gap has no numeric range, the loss is taken to start at the
// oldest element the window holds, or at the first sequence when it holds none.
function firstLoadedSequence(state) {
  return state.items.length ? state.items[0].sequence : 1;
}

export function setFilter(state, value) {
  state.filter = value;
  return state;
}

export function applySnapshot(state, snapshot) {
  const priorItems = state.items;
  state.source = snapshot.source;
  state.verification = snapshot.verification_scope;
  state.epoch = snapshot.epoch;
  state.cursor = snapshot.cursor;
  state.session = snapshot.session;
  state.panels = snapshot.panels;
  state.streams = Array.isArray(snapshot.streams) ? snapshot.streams : [];
  state.warnings = Array.isArray(snapshot.warnings) ? snapshot.warnings : [];
  state.gaps = Array.isArray(snapshot.gaps) ? snapshot.gaps : [];
  state.items = [];
  state.itemsById = new Map();
  state.lastSequence = 0;
  for (const item of snapshot.items || []) {
    if (state.itemsById.has(item.id)) continue;
    state.items.push(item);
    state.itemsById.set(item.id, item);
    if (item.sequence > state.lastSequence) state.lastSequence = item.sequence;
  }
  // Elements the client retained but the new snapshot omits were dropped by the
  // source window. Count them as evicted so the retained-window disclosure stays
  // truthful instead of silently shrinking the record.
  const dropped = priorItems.filter((item) => !state.itemsById.has(item.id));
  state.evicted += dropped.length;
  const trimmed = enforceClientCap(state);
  // A selection that the new window no longer contains must not survive as a
  // dangling reference: the inspector would keep showing an evicted record.
  if (state.selectedId && !state.itemsById.has(state.selectedId)) state.selectedId = null;
  const evictions = state.gaps.filter((gap) => EVICTION_REASONS.has(gap.reason));
  updateWindowGap(state, { evictions, dropped, trimmed });
  state.loaded = true;
  state.error = null;
  state.gapNotice = null;
  return state;
}

// Returns "applied", "duplicate", or "gap".
export function applyItem(state, item) {
  if (!item || typeof item.id !== "string") return "duplicate";
  if (state.itemsById.has(item.id)) return "duplicate";
  if (typeof item.sequence !== "number" || item.sequence <= state.lastSequence) {
    return "duplicate";
  }
  if (item.sequence !== state.lastSequence + 1) {
    return "gap";
  }
  state.items.push(item);
  state.itemsById.set(item.id, item);
  state.lastSequence = item.sequence;
  state.cursor = `${state.epoch}:${item.sequence}`;
  const trimmed = enforceClientCap(state);
  if (trimmed.length) noteWindowLoss(state, "client", "retained_window", trimmed[0].sequence);
  return "applied";
}

export function applySession(state, session, panels, streams) {
  if (session) state.session = session;
  if (panels) state.panels = panels;
  if (Array.isArray(streams)) state.streams = streams;
}

export function noteGap(state, gap) {
  if (gap && typeof gap === "object") {
    state.gapNotice = gap;
    state.gaps = [...state.gaps, gap];
  }
}

export function setConnection(state, value) {
  state.connection = value;
}

export function setError(state, message) {
  state.error = message;
}

// Groups a turn's items into ordered message and tool blocks. Tool blocks
// correlate their activity, decision, and result by tool_call_id.
export function groupTurn(items) {
  const blocks = [];
  const byTool = new Map();
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
  // Attach in-flight tool blocks to the position of their first item so the
  // transcript order is preserved.
  return blocks;
}

export function turnsOf(items) {
  const order = [];
  const map = new Map();
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
