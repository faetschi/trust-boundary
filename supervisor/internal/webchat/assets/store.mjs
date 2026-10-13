// store.js — bounded client-side projection state.
//
// The client never invents success. Items are applied only in strictly
// increasing sequence order; duplicates are ignored; any discontinuity or
// replay gap forces the caller to recover from a fresh snapshot instead of
// assuming continuity.

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
  };
}

export function applySnapshot(state, snapshot) {
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
  return "applied";
}

export function applySession(state, session, panels) {
  if (session) state.session = session;
  if (panels) state.panels = panels;
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
