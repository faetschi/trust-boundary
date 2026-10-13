import test from "node:test";
import assert from "node:assert/strict";

import {
  applyItem,
  applySession,
  applySnapshot,
  createWorkspace,
  groupTurn,
  noteGap,
  turnsOf,
} from "../assets/store.mjs";

function item(id, sequence, overrides = {}) {
  return { id, sequence, type: "message", turn_id: "turn-1", role: "assistant", text: id, ...overrides };
}

function snapshot(items) {
  return {
    schema_version: "tbound-webchat-observation/v1",
    epoch: "e1",
    cursor: `e1:${items.length ? items[items.length - 1].sequence : 0}`,
    source: "fixture",
    verification_scope: "unverified-input",
    session: { session_id: "s1", status: "pending", turn_count: 1 },
    panels: { publication: { state: "not-attempted" }, recovery: { state: "not-required" }, qualification: { g1: false } },
    streams: [],
    warnings: [],
    items,
  };
}

test("applySnapshot loads items and advances the cursor", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1), item("b", 2)]));
  assert.equal(state.loaded, true);
  assert.equal(state.items.length, 2);
  assert.equal(state.lastSequence, 2);
  assert.equal(state.cursor, "e1:2");
});

test("duplicate item delivery is idempotent", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1)]));
  assert.equal(applyItem(state, item("a", 1)), "duplicate");
  assert.equal(state.items.length, 1);
});

test("a gap is reported and the missing element is never invented", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1)]));
  // Sequence 3 arrives without 2: continuity is broken.
  assert.equal(applyItem(state, item("c", 3)), "gap");
  assert.equal(state.items.length, 1, "a gapped element must not be applied");
  assert.equal(state.itemsById.has("c"), false);
});

test("an out-of-order element is ignored rather than applied", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1), item("b", 2)]));
  assert.equal(applyItem(state, item("old", 1)), "duplicate");
  assert.equal(state.items.length, 2);
});

test("a missing terminal result cannot become a success display", () => {
  const state = createWorkspace();
  applySnapshot(
    state,
    snapshot([
      { id: "act", sequence: 1, type: "activity", turn_id: "t1", tool: "bash", kind: "proposal", summary: "go test", state: "pending" },
      { id: "dec", sequence: 2, type: "decision", turn_id: "t1", tool: "bash", tool_call_id: "call-1", verdict: "ALLOW", reason_code: "policy_rule_allow" },
    ]),
  );
  // The result (sequence 4) never arrives contiguously; sequence 3 is missing.
  assert.equal(
    applyItem(state, { id: "res", sequence: 4, type: "result", turn_id: "t1", tool: "bash", tool_call_id: "call-1", state: "succeeded" }),
    "gap",
  );
  const result = state.items.find((entry) => entry.type === "result");
  assert.equal(result, undefined, "no result element may appear after a gap");
});

test("applySession updates session and panels", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([]));
  applySession(state, { session_id: "s1", status: "unknown", turn_count: 1 }, { publication: { state: "quarantined" } });
  assert.equal(state.session.status, "unknown");
  assert.equal(state.panels.publication.state, "quarantined");
});

test("noteGap records a durable notice", () => {
  const state = createWorkspace();
  noteGap(state, { reason: "history_evicted", requested_cursor: "e1:5" });
  assert.equal(state.gapNotice.reason, "history_evicted");
  assert.equal(state.gaps.length, 1);
});

test("groupTurn correlates activity, decision and result by tool_call_id", () => {
  const blocks = groupTurn([
    { id: "m1", sequence: 1, type: "message", role: "user", text: "hi" },
    { id: "act", sequence: 2, type: "activity", tool: "read", tool_call_id: "call-9", kind: "proposal", summary: "read", state: "pending" },
    { id: "dec", sequence: 3, type: "decision", tool: "read", tool_call_id: "call-9", verdict: "ALLOW", reason_code: "policy_rule_allow" },
    { id: "res", sequence: 4, type: "result", tool: "read", tool_call_id: "call-9", state: "succeeded" },
  ]);
  assert.equal(blocks.length, 2);
  assert.equal(blocks[0].kind, "message");
  assert.equal(blocks[1].kind, "tool");
  assert.equal(blocks[1].decision.verdict, "ALLOW");
  assert.equal(blocks[1].result.state, "succeeded");
});

test("turnsOf groups by turn in first-seen order", () => {
  const turns = turnsOf([item("a", 1), item("b", 2, { turn_id: "turn-2" }), item("c", 3)]);
  assert.equal(turns.length, 2);
  assert.equal(turns[0].id, "turn-1");
  assert.deepEqual(turns[0].items.map((entry) => entry.id), ["a", "c"]);
  assert.equal(turns[1].id, "turn-2");
  assert.deepEqual(turns[1].items.map((entry) => entry.id), ["b"]);
});
