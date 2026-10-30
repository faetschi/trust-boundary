// Port of internal/webchat/jstest/filters.test.mjs. Same test names and assertions.
import { test } from "vitest";
import assert from "node:assert/strict";

import { FILTERS, countFilter, filterItems, governanceCounts, matchesFilter } from "./filters";
import type { ItemView } from "./contract";

function item(overrides: ItemView): ItemView {
  return { id: "i", type: "result", turn_id: "t1", ...overrides };
}

const timeline: ItemView[] = [
  item({ id: "m1", type: "message", role: "user", text: "do the thing" }),
  item({ id: "a1", type: "activity", tool: "read", kind: "proposal", tool_call_id: "c1", summary: "read", state: "pending" }),
  item({ id: "d1", type: "decision", tool: "read", tool_call_id: "c1", verdict: "ALLOW", reason_code: "policy_rule_allow" }),
  item({ id: "r1", type: "result", tool: "read", tool_call_id: "c1", state: "succeeded" }),
  item({ id: "a2", type: "activity", tool: "bash", kind: "proposal", tool_call_id: "c2", summary: "curl", state: "pending" }),
  item({ id: "d2", type: "decision", tool: "bash", tool_call_id: "c2", verdict: "DENY", reason_code: "policy_rule_deny" }),
  item({ id: "r2", type: "result", tool: "bash", tool_call_id: "c2", state: "denied" }),
  item({ id: "a3", type: "activity", tool: "bash", kind: "proposal", tool_call_id: "c3", summary: "race", state: "pending" }),
  item({ id: "d3", type: "decision", tool: "bash", tool_call_id: "c3", verdict: "ALLOW", reason_code: "policy_rule_allow" }),
  item({ id: "r3", type: "result", tool: "bash", tool_call_id: "c3", state: "unknown" }),
  item({ id: "a4", type: "activity", tool: "write", kind: "proposal", tool_call_id: "c4", summary: "write", state: "pending" }),
  item({ id: "r4", type: "result", tool: "write", tool_call_id: "c4", state: "failed" }),
];

test("filter list exposes the documented governance filters", () => {
  assert.deepEqual(
    FILTERS.map((entry) => entry.id),
    ["all", "denied", "unknown", "failed"],
  );
});

test("matchesFilter recognises denied decisions and results", () => {
  assert.equal(matchesFilter(item({ id: "d", type: "decision", verdict: "DENY" }), "denied"), true);
  assert.equal(matchesFilter(item({ id: "r", state: "denied" }), "denied"), true);
  assert.equal(matchesFilter(item({ id: "r", state: "succeeded" }), "denied"), false);
});

test("matchesFilter treats UNKNOWN and LOST as unknown, never success", () => {
  assert.equal(matchesFilter(item({ id: "r", state: "unknown" }), "unknown"), true);
  assert.equal(matchesFilter(item({ id: "r", state: "lost" }), "unknown"), true);
  assert.equal(matchesFilter(item({ id: "d", type: "decision", verdict: "UNKNOWN" }), "unknown"), true);
  assert.equal(matchesFilter(item({ id: "r", state: "succeeded" }), "unknown"), false);
});

test("the all filter is the identity", () => {
  assert.equal(filterItems(timeline, "all").length, timeline.length);
  assert.equal(countFilter(timeline, "all"), timeline.length);
});

test("filtering keeps the correlated tool block but drops transcript messages", () => {
  const denied = filterItems(timeline, "denied");
  const ids = denied.map((entry) => entry.id);
  assert.deepEqual(ids, ["a2", "d2", "r2"], "the whole denied tool block is kept for context");
  assert.ok(!ids.includes("m1"), "transcript messages are omitted while filtering");
});

test("unknown filtering surfaces the UNKNOWN effect with its context", () => {
  const ids = filterItems(timeline, "unknown").map((entry) => entry.id);
  assert.deepEqual(ids, ["a3", "d3", "r3"]);
});

test("failed filtering surfaces only failed results and their context", () => {
  const ids = filterItems(timeline, "failed").map((entry) => entry.id);
  assert.deepEqual(ids, ["a4", "r4"]);
});

test("counts match the number of matching elements", () => {
  assert.equal(countFilter(timeline, "denied"), 2);
  assert.equal(countFilter(timeline, "unknown"), 1);
  assert.equal(countFilter(timeline, "failed"), 1);
});

test("a LOSSY result is surfaced under Unknown and counted independently", () => {
  const lossy = item({ id: "r-lossy", state: "succeeded", lossy: true });
  assert.equal(matchesFilter(lossy, "unknown"), true, "lossy output is not terminal evidence and must be reviewable");
  const counts = governanceCounts([lossy], () => false);
  assert.equal(counts.reported, 1);
  assert.equal(counts.lossy, 1);
});

test("a pending result is neither unknown nor a finding", () => {
  const pending = item({ id: "r-pending", state: "pending" });
  assert.equal(matchesFilter(pending, "unknown"), false);
  const counts = governanceCounts([pending], () => false);
  assert.equal(counts.pendingResults, 1);
  assert.equal(counts.unknownResults, 0);
});

test("a verified but lossy result counts as both verified and lossy", () => {
  const counts = governanceCounts([item({ id: "r", state: "succeeded", lossy: true })], () => true);
  assert.equal(counts.verified, 1);
  assert.equal(counts.lossy, 1);
});

test("governanceCounts splits verified from reported successes", () => {
  const counts = governanceCounts(timeline, () => false);
  assert.equal(counts.allow, 2);
  assert.equal(counts.deny, 1);
  assert.equal(counts.verified, 0);
  assert.equal(counts.reported, 1);
  assert.equal(counts.denied, 1);
  assert.equal(counts.failed, 1);
  assert.equal(counts.unknownResults, 1);

  const verified = governanceCounts(timeline, () => true);
  assert.equal(verified.verified, 1);
  assert.equal(verified.reported, 0);
});
