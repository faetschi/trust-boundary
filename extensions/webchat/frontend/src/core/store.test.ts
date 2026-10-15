// Port of internal/webchat/jstest/store.test.mjs. Same test names and assertions.
import { test } from "vitest";
import assert from "node:assert/strict";

import type { Item, MessageItem, Snapshot } from "./contract";
import { activityItem, decisionItem, messageItem, panelsDefault, resultItem, snapshotOf } from "./testing/builders";
import {
  applyItem,
  applySession,
  applySnapshot,
  createWorkspace,
  selectRecord,
  groupTurn,
  noteGap,
  MAX_CLIENT_ITEMS,
  turnsOf,
} from "./store";

// A message item on turn-1 by default. `overrides` may move it to another turn.
function item(id: string, sequence: number, overrides: { readonly turn_id?: string } = {}): MessageItem {
  return messageItem({ id, sequence, turn_id: "turn-1", role: "assistant", text: id, ...overrides });
}

// A fixture snapshot whose cursor ends at its last item, as the gateway serves it.
function snapshot(items: readonly Item[]): Snapshot {
  return snapshotOf({ items });
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
      activityItem({
        id: "act",
        sequence: 1,
        turn_id: "t1",
        tool: "bash",
        tool_call_id: "call-1",
        kind: "proposal",
        summary: "go test",
        state: "pending",
      }),
      decisionItem({
        id: "dec",
        sequence: 2,
        turn_id: "t1",
        tool: "bash",
        tool_call_id: "call-1",
        verdict: "ALLOW",
        reason_code: "policy_rule_allow",
      }),
    ]),
  );
  // The result (sequence 4) never arrives contiguously; sequence 3 is missing.
  assert.equal(
    applyItem(
      state,
      resultItem({ id: "res", sequence: 4, turn_id: "t1", tool: "bash", tool_call_id: "call-1", state: "succeeded" }),
    ),
    "gap",
  );
  const result = state.items.find((entry) => entry.type === "result");
  assert.equal(result, undefined, "no result element may appear after a gap");
});

test("a snapshot that omits retained items counts them as evicted", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1), item("b", 2), item("c", 3)]));
  assert.equal(state.evicted, 0);
  applySnapshot(state, snapshot([item("c", 3)]));
  assert.equal(state.items.length, 1);
  assert.equal(state.evicted, 2, "items dropped by the new snapshot must be disclosed");
});

test("applySession updates session and panels", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([]));
  applySession(
    state,
    { session_id: "s1", status: "unknown", turn_count: 1 },
    { ...panelsDefault(), publication: { state: "quarantined" } },
  );
  assert.equal(state.session?.status, "unknown");
  assert.equal(state.panels?.publication.state, "quarantined");
});

test("noteGap records a durable notice", () => {
  const state = createWorkspace();
  noteGap(state, { reason: "history_evicted", requested_cursor: "e1:5" });
  assert.equal(state.gapNotice?.reason, "history_evicted");
  assert.equal(state.gaps.length, 1);
});

test("groupTurn correlates activity, decision and result by tool_call_id", () => {
  const blocks = groupTurn([
    messageItem({ id: "m1", sequence: 1, role: "user", text: "hi" }),
    activityItem({
      id: "act",
      sequence: 2,
      tool: "read",
      tool_call_id: "call-9",
      kind: "proposal",
      summary: "read",
      state: "pending",
    }),
    decisionItem({ id: "dec", sequence: 3, tool: "read", tool_call_id: "call-9", verdict: "ALLOW", reason_code: "policy_rule_allow" }),
    resultItem({ id: "res", sequence: 4, tool: "read", tool_call_id: "call-9", state: "succeeded" }),
  ]);
  assert.equal(blocks.length, 2);
  assert.equal(blocks[0]?.kind, "message");
  assert.equal(blocks[1]?.kind, "tool");
  const tool = blocks[1];
  assert.ok(tool?.kind === "tool", "the second block is a tool block");
  assert.equal(tool.decision?.verdict, "ALLOW");
  assert.equal(tool.result?.state, "succeeded");
});

test("turnsOf groups by turn in first-seen order", () => {
  const turns = turnsOf([item("a", 1), item("b", 2, { turn_id: "turn-2" }), item("c", 3)]);
  assert.equal(turns.length, 2);
  assert.equal(turns[0]?.id, "turn-1");
  assert.deepEqual(turns[0]?.items.map((entry) => entry.id), ["a", "c"]);
  assert.equal(turns[1]?.id, "turn-2");
  assert.deepEqual(turns[1]?.items.map((entry) => entry.id), ["b"]);
});

test("evicting the selected record clears the selection", () => {
  const state = createWorkspace();
  const items: Item[] = [];
  for (let i = 1; i <= MAX_CLIENT_ITEMS; i += 1) items.push(item(`i${i}`, i));
  applySnapshot(state, snapshot(items));
  selectRecord(state, "i1");
  assert.equal(state.selectedId, "i1");
  assert.equal(applyItem(state, item("next", MAX_CLIENT_ITEMS + 1)), "applied");
  assert.equal(state.itemsById.has("i1"), false);
  assert.equal(state.selectedId, null, "a dangling selection must not survive eviction");
});

// windowGap records that the loaded window is not provably complete from `since`
// onward. Only a snapshot that restores the lost elements clears it.
function windowedSnapshot(sequences: readonly number[], overrides: Partial<Snapshot> = {}): Snapshot {
  return { ...snapshot(sequences.map((sequence) => item(`e${sequence}`, sequence))), ...overrides };
}

test("a snapshot that drops retained elements sets a server windowGap at the first lost element", () => {
  const state = createWorkspace();
  applySnapshot(state, windowedSnapshot([1, 2, 3]));
  assert.equal(state.windowGap, null, "a complete first load claims no missing history");
  applySnapshot(state, windowedSnapshot([3]));
  assert.deepEqual(state.windowGap, { kind: "server", reason: "history_evicted", since: 1 });
});

test("a snapshot-declared history_evicted gap sets a windowGap from its first missing element", () => {
  const state = createWorkspace();
  applySnapshot(
    state,
    windowedSnapshot([4, 5], {
      gaps: [{ id: "g1", reason: "history_evicted", first_missing: 1, last_missing: 3, recorded_at: "2026-10-14T10:00:00Z" }],
    }),
  );
  assert.deepEqual(state.windowGap, { kind: "server", reason: "history_evicted", since: 1 });
});

test("a client-side trim sets a client windowGap at the oldest trimmed element", () => {
  const state = createWorkspace();
  const sequences = Array.from({ length: MAX_CLIENT_ITEMS }, (_, index) => index + 1);
  applySnapshot(state, windowedSnapshot(sequences));
  assert.equal(state.windowGap, null);
  assert.equal(applyItem(state, item("next", MAX_CLIENT_ITEMS + 1)), "applied");
  assert.deepEqual(state.windowGap, { kind: "client", reason: "retained_window", since: 1 });
});

test("a live item never clears a windowGap", () => {
  const state = createWorkspace();
  applySnapshot(state, windowedSnapshot([1, 2, 3]));
  applySnapshot(state, windowedSnapshot([3]));
  assert.ok(state.windowGap);
  assert.equal(applyItem(state, item("e4", 4)), "applied");
  assert.ok(state.windowGap, "appending a live element does not prove the lost history is back");
});

test("a windowGap is cleared only by a snapshot that restores the lost elements", () => {
  const state = createWorkspace();
  applySnapshot(state, windowedSnapshot([1, 2, 3]));
  applySnapshot(state, windowedSnapshot([3]));
  applySnapshot(state, windowedSnapshot([3, 4]));
  assert.deepEqual(state.windowGap, { kind: "server", reason: "history_evicted", since: 1 }, "a snapshot that still starts after the loss keeps the gap");
  applySnapshot(state, windowedSnapshot([1, 2, 3, 4]));
  assert.equal(state.windowGap, null, "a snapshot restoring the lost elements proves continuity");
});

test("a snapshot that still declares an eviction gap does not clear the windowGap", () => {
  const state = createWorkspace();
  const declared = [{ id: "g1", reason: "history_evicted", first_missing: 1, last_missing: 1, recorded_at: "2026-10-14T10:00:00Z" }];
  applySnapshot(state, windowedSnapshot([1, 2], { gaps: declared }));
  assert.deepEqual(state.windowGap, { kind: "server", reason: "history_evicted", since: 1 });
  applySnapshot(state, windowedSnapshot([1, 2], { gaps: declared }));
  assert.ok(state.windowGap, "a snapshot that still declares the eviction cannot prove continuity");
});

test("a client trim that recurs on each snapshot keeps the windowGap set", () => {
  const state = createWorkspace();
  const sequences = Array.from({ length: MAX_CLIENT_ITEMS + 10 }, (_, index) => index + 1);
  applySnapshot(state, windowedSnapshot(sequences));
  assert.equal(state.windowGap?.kind, "client");
  applySnapshot(state, windowedSnapshot(sequences));
  assert.equal(state.windowGap?.kind, "client", "the same over-limit window cannot prove continuity");
});

test("a resync that loses nothing claims no missing history", () => {
  const state = createWorkspace();
  applySnapshot(state, windowedSnapshot([1, 2, 3]));
  applySnapshot(state, windowedSnapshot([1, 2, 3, 4, 5]));
  assert.equal(state.windowGap, null, "a complete replacement must not display history as not loaded");
});

test("applied counts only records applyItem applied in order, never gaps, duplicates or snapshot loads", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1)]));
  assert.equal(state.applied, 0, "a snapshot replaces the window; it is not a live application");
  assert.equal(applyItem(state, item("b", 2)), "applied");
  assert.equal(applyItem(state, item("b", 2)), "duplicate");
  assert.equal(applyItem(state, item("d", 4)), "gap");
  assert.equal(state.applied, 1);
});

test("an item without a string id is invalid, not a duplicate, so it is never dropped silently", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1)]));
  // @ts-expect-error: an item with no id is malformed on purpose
  assert.equal(applyItem(state, { sequence: 2, type: "message" }), "invalid");
  // @ts-expect-error: a numeric id is malformed on purpose
  assert.equal(applyItem(state, { id: 7, sequence: 2, type: "message" }), "invalid", "a numeric id is not a string id");
  // @ts-expect-error: a null item is malformed on purpose
  assert.equal(applyItem(state, null), "invalid");
  assert.equal(state.lastSequence, 1, "an invalid item never advances the sequence");
  assert.equal(state.items.length, 1);
});

test("an item without a numeric sequence is invalid", () => {
  const state = createWorkspace();
  applySnapshot(state, snapshot([item("a", 1)]));
  // @ts-expect-error: an item with no sequence is malformed on purpose
  assert.equal(applyItem(state, { id: "b", type: "message" }), "invalid");
  // @ts-expect-error: a string sequence is malformed on purpose
  assert.equal(applyItem(state, item("c", "3")), "invalid", "a string sequence is not a sequence");
  assert.equal(state.lastSequence, 1);
});

test("a windowGap clears only when the snapshot holds every element from the lost point on", () => {
  const state = createWorkspace();
  applySnapshot(
    state,
    windowedSnapshot([3, 4], {
      gaps: [{ id: "g1", reason: "history_evicted", first_missing: 5, last_missing: 9, recorded_at: "2026-10-14T10:00:00Z" }],
    }),
  );
  assert.deepEqual(state.windowGap, { kind: "server", reason: "history_evicted", since: 5 });

  applySnapshot(state, windowedSnapshot([3, 4, 10, 11, 12, 20]));
  assert.ok(state.windowGap, "the oldest element is before the loss, but 5 through 9 are missing, so continuity is not proven");

  applySnapshot(state, windowedSnapshot(Array.from({ length: 18 }, (_, index) => index + 3)));
  assert.equal(state.windowGap, null, "a snapshot holding 3 through 20 with no hole proves continuity");
});
