import test from "node:test";
import assert from "node:assert/strict";

const {
  RECORD_GROUPS,
  recordChips,
  recordDetail,
  recordDot,
  recordOf,
  recordPayload,
  recordSource,
  searchRecords,
  sequenceLanes,
  sourceLabel,
  statusText,
  timingOf,
  toneText,
  visibleRecords,
  weightClass,
} = await import("../assets/trajectory.mjs");

function item(overrides) {
  return {
    id: "x1",
    sequence: 1,
    type: "activity",
    turn_id: "t1",
    occurred_at: "2026-10-13T10:00:00Z",
    authority: "diagnostic",
    verification_scope: "unverified-input",
    ...overrides,
  };
}

const allowedBash = item({
  id: "a1",
  sequence: 1,
  type: "activity",
  tool: "bash",
  tool_call_id: "c1",
  kind: "proposal",
  summary: "bash: go test ./...",
  state: "pending",
});
const allowDecision = item({
  id: "d1",
  sequence: 2,
  type: "decision",
  tool: "bash",
  tool_call_id: "c1",
  verdict: "ALLOW",
  reason_code: "policy_rule_allow",
  policy_digest: "sha256:aaaa",
});
const succeededResult = item({
  id: "r1",
  sequence: 3,
  type: "result",
  tool: "bash",
  tool_call_id: "c1",
  state: "succeeded",
  summary: "exit 0",
  digest: "sha256:bbbb",
});
const deniedDecision = item({
  id: "d2",
  sequence: 4,
  type: "decision",
  tool: "write",
  tool_call_id: "c2",
  verdict: "DENY",
  reason_code: "argument_digest_mismatch",
});
const deniedResult = item({
  id: "r2",
  sequence: 5,
  type: "result",
  tool: "write",
  tool_call_id: "c2",
  state: "denied",
});
const lostResult = item({
  id: "r3",
  sequence: 6,
  type: "result",
  tool: "edit",
  tool_call_id: "c3",
  state: "lost",
});
const prompt = item({ id: "m1", sequence: 7, type: "message", role: "user", text: "do example tool calls" });
const systemNote = item({ id: "m2", sequence: 8, type: "message", role: "system", text: "runtime context" });

const ALL = [allowedBash, allowDecision, succeededResult, deniedDecision, deniedResult, lostResult, prompt, systemNote];

test("every item type maps to a source chip, and no context chip is invented", () => {
  assert.equal(recordSource(allowedBash), "tool");
  assert.equal(recordSource(allowDecision), "decision");
  assert.equal(recordSource(succeededResult), "result");
  assert.equal(recordSource(prompt), "user");
  assert.equal(recordSource(systemNote), "system");
  // The projection has no context item type; the vocabulary must not imply one.
  const sources = ALL.map(recordSource);
  assert.ok(!sources.includes("context"), "no record may claim a context source");
  assert.equal(sourceLabel("context"), "Unknown");
});

test("an unverified success is reported, never confirmed", () => {
  const chips = recordChips(succeededResult);
  const state = chips.find((entry) => entry.label === "succeeded");
  assert.ok(state, "the result state must be chipped");
  assert.equal(state.variant, "reported", "an unverified success uses the reported variant");
  assert.equal(recordDot(succeededResult), "reported");
  assert.ok(chips.some((entry) => entry.label === "unverified"));
});

test("the status dot never claims more than the chip", () => {
  // A policy ALLOW is a permission, not an outcome, so it is not a confirmed tone.
  const CONFIRMED = new Set(["succeeded", "completed", "published", "ready"]);
  const NEGATIVE = new Set(["deny", "denied", "failed", "cancelled", "lost"]);
  for (const record of ALL) {
    const dot = recordDot(record);
    const chips = recordChips(record);
    const has = (set) => chips.some((entry) => set.has(entry.variant));
    if (dot === "ok") {
      assert.ok(has(CONFIRMED), `${record.id}: an ok dot requires a confirmed chip`);
    }
    if (dot === "reported") {
      assert.ok(
        chips.some((entry) => entry.variant === "reported"),
        `${record.id}: a reported dot requires a reported chip`,
      );
    }
    if (dot === "deny" || dot === "lost") {
      assert.ok(has(NEGATIVE), `${record.id}: a ${dot} dot requires a negative chip`);
    }
    // A policy verdict keeps its verdict tone whether or not the projection
    // verified it, exactly as the timeline renders it; what must never be absent
    // is the disclosure. So an ok tone is only ever accompanied by either
    // verification or an explicit unverified marker.
    if (dot === "ok") {
      const verified =
        record.authority === "observed" &&
        record.verification_scope === "structural-audit-only";
      const disclosed = chips.some((entry) => entry.label === "unverified");
      assert.ok(
        verified || disclosed,
        `${record.id}: an ok dot must be verified or carry an explicit unverified marker`,
      );
    }
  }
});

test("an unverified outcome never takes the ok tone", () => {
  // The success gate applies to execution outcomes, not to verdicts.
  for (const record of ALL) {
    if (record.type !== "result") continue;
    if (record.state !== "succeeded") continue;
    assert.notEqual(recordDot(record), "ok", "an unverified success is reported, never ok");
  }
});

test("a lost result is distinguishable from a denial and from success", () => {
  assert.equal(recordDot(lostResult), "lost");
  assert.notEqual(recordDot(lostResult), "deny");
  assert.notEqual(recordDot(lostResult), "ok");
  assert.equal(recordChips(lostResult).find((entry) => entry.label === "lost").variant, "lost");
});

test("no record can produce a duration, and timing states why", () => {
  for (const record of ALL) {
    const timing = timingOf(record);
    assert.equal(timing.available, false, `${record.id} must not report available timing`);
    assert.equal(timing.duration, null, `${record.id} must not carry a duration`);
    assert.match(timing.reason, /not a duration/);
  }
  const withNoTimestamp = timingOf(item({ occurred_at: undefined }));
  assert.equal(withNoTimestamp.clock, null);
  assert.equal(withNoTimestamp.available, false);
});

test("the record inspector always states that duration is unavailable", () => {
  const sections = recordDetail(deniedDecision);
  const timing = sections.find((section) => section.id === "timing");
  assert.ok(timing, "the Timing section is always present");
  const duration = timing.rows.find((row) => row[0] === "duration");
  assert.match(String(duration[1]), /unavailable/);
  assert.match(timing.note, /no elapsed time/);
});

test("a decision record carries its governance lens", () => {
  const sections = recordDetail(allowDecision);
  const decision = sections.find((section) => section.id === "decision");
  assert.ok(decision, "a decision record has a Decision section");
  const keys = decision.rows.map((row) => row[0]);
  assert.deepEqual(keys, ["verdict", "reason code", "policy digest"]);
  assert.equal(recordPayload(allowDecision), "reason policy_rule_allow · policy sha256:aaaa");
  // A message has no decision or result section.
  const messageSections = recordDetail(prompt).map((section) => section.id);
  assert.ok(!messageSections.includes("decision"));
  assert.ok(!messageSections.includes("result"));
});

test("the sequence rail is ordinal and shares one axis across lanes", () => {
  const rail = sequenceLanes(ALL);
  assert.equal(rail.ordinal, true);
  assert.equal(rail.axis, "record sequence");
  assert.match(rail.note, /not time/);
  assert.deepEqual(rail.lanes.map((lane) => lane.id), ["model", "policy", "tools"]);
  for (const lane of rail.lanes) {
    const total = lane.segments.reduce((sum, segment) => sum + segment.weight, 0);
    assert.equal(total, ALL.length, `${lane.id} must span the whole record sequence`);
  }
  // Adjacent same-tone spans are merged, so the rail never renders redundant cells.
  for (const lane of rail.lanes) {
    for (let index = 1; index < lane.segments.length; index += 1) {
      assert.notEqual(lane.segments[index].tone, lane.segments[index - 1].tone);
    }
  }
});

test("segment weights are bucketed into classes, never inline styles", () => {
  assert.equal(weightClass(1), "seg-w1");
  assert.equal(weightClass(4), "seg-w4");
  assert.equal(weightClass(99), "seg-w8");
  assert.equal(weightClass(0), "seg-w1");
});

test("search reads only allowlisted bounded fields", () => {
  const withRaw = item({ id: "raw1", summary: "read go.mod", path: "/etc/passwd", body: "secret-token" });
  assert.equal(searchRecords([withRaw], "go.mod").length, 1);
  assert.equal(searchRecords([withRaw], "policy_rule_allow").length, 0);
  assert.equal(searchRecords([withRaw], "/etc/passwd").length, 0, "a raw path is not searchable");
  assert.equal(searchRecords([withRaw], "secret-token").length, 0, "raw body bytes are not searchable");
  assert.equal(searchRecords(ALL, "argument_digest_mismatch").length, 1);
  assert.equal(searchRecords(ALL, "  ").length, ALL.length, "a blank query does not narrow");
  assert.equal(searchRecords(ALL, "DENY").length, 1, "search is case-insensitive");
});

test("grouping covers turn, tool and verdict", () => {
  assert.deepEqual(RECORD_GROUPS.map((entry) => entry.id), ["turn", "tool", "verdict"]);

  const byTurn = visibleRecords({ items: ALL, group: "turn", query: "" });
  assert.equal(byTurn.groups.length, 1);
  assert.equal(byTurn.groups[0].title, "Turn t1");
  assert.equal(byTurn.groups[0].records.length, ALL.length);

  const byTool = visibleRecords({ items: ALL, group: "tool", query: "" });
  const toolIds = byTool.groups.map((group) => group.id);
  assert.ok(toolIds.includes("c1") && toolIds.includes("c2") && toolIds.includes("__messages"));
  const c1 = byTool.groups.find((group) => group.id === "c1");
  assert.equal(c1.records.length, 3, "a tool call's activity, decision and result group together");

  const byVerdict = visibleRecords({ items: ALL, group: "verdict", query: "" });
  const allow = byVerdict.groups.find((group) => group.title === "ALLOW");
  const deny = byVerdict.groups.find((group) => group.title === "DENY");
  const none = byVerdict.groups.find((group) => group.title === "No verdict");
  assert.equal(allow.records.length, 3, "activity, decision and result follow their verdict");
  assert.equal(deny.records.length, 2);
  assert.equal(
    none.records.length,
    3,
    "the two messages and the lost result with no recorded decision carry no verdict",
  );
  assert.ok(
    none.records.some((record) => record.item.state === "lost"),
    "a lost result is never filed under a verdict it does not have",
  );
});

test("the trajectory ignores the timeline finding filter", () => {
  // The filter narrows the transcript; it must not silently narrow the audit
  // ledger, and a search must never change a filter outcome.
  const state = { items: ALL, group: "turn", query: "", filter: "denied" };
  assert.equal(visibleRecords(state).items.length, ALL.length);
  assert.equal(visibleRecords({ ...state, query: "go test" }).items.length, 1);
});

test("a record exposes only the fields the ledger and inspector need", () => {
  const record = recordOf(allowDecision);
  assert.deepEqual(Object.keys(record).sort(), [
    "chips",
    "detail",
    "dot",
    "id",
    "item",
    "label",
    "payload",
    "sequence",
    "source",
  ]);
  assert.equal(record.label, "bash · reviewed");
  assert.equal(recordOf(prompt).label, "prompt");
  assert.equal(recordOf(systemNote).label, "system note");
});

const verifiedSuccess = item({ id: "v1", sequence: 9, type: "result", tool: "bash", tool_call_id: "c9", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" });
const unknownDecision = item({ id: "u1", sequence: 10, type: "decision", tool: "bash", tool_call_id: "c10", verdict: "UNKNOWN", reason_code: "decision_lost" });
const unknownResult = item({ id: "u2", sequence: 11, type: "result", tool: "bash", tool_call_id: "c10", state: "unknown" });

test("a policy ALLOW is never styled as a successful execution", () => {
  assert.equal(recordDot(allowDecision), "allow");
  for (const record of ALL) {
    if (record.type === "decision") assert.notEqual(recordDot(record), "ok", `${record.id}: a verdict is not an outcome`);
  }
  // ALLOW next to a lost result: the policy lane must not be green beside a red tools lane.
  const rail = sequenceLanes([allowDecision, lostResult]);
  const policy = rail.lanes.find((lane) => lane.id === "policy");
  assert.ok(policy.segments.some((segment) => segment.tone === "allow"));
  assert.ok(!rail.lanes.some((lane) => lane.segments.some((segment) => segment.tone === "ok")));
});

test("only a verified, complete success takes the ok tone", () => {
  assert.equal(recordDot(verifiedSuccess), "ok");
  assert.equal(recordDot({ ...verifiedSuccess, lossy: true }), "unknown", "lossy output is not terminal evidence");
  assert.equal(recordDot(succeededResult), "reported");
});

test("UNKNOWN verdicts and results stay unknown and are never promoted", () => {
  assert.equal(recordDot(unknownDecision), "unknown");
  assert.equal(recordDot(unknownResult), "unknown");
  assert.ok(recordChips(unknownDecision).some((chip) => chip.label === "UNKNOWN" && chip.variant === "unknown"));
  const rail = sequenceLanes([unknownDecision, unknownResult]);
  for (const lane of rail.lanes) {
    for (const segment of lane.segments) assert.ok(!["ok", "allow"].includes(segment.tone), "UNKNOWN is not drawn as success or permission");
  }
});

test("a message is drawn distinctly from a lane that did not act", () => {
  const rail = sequenceLanes([prompt, allowDecision]);
  const model = rail.lanes.find((lane) => lane.id === "model");
  assert.deepEqual(model.segments.map((segment) => segment.tone), ["idle", "muted"]);
});

/* ---------------------------------------------------------------------------
 * Status as text: the colour dot is decorative, so its state is also stated in
 * words on the record. Wording never calls a policy ALLOW or an unverified
 * success a success.
 * ------------------------------------------------------------------------- */

const verifiedItem = { authority: "observed", verification_scope: "structural-audit-only" };

test("a policy ALLOW is stated as a permission, never as an outcome", () => {
  const text = statusText(allowDecision);
  assert.equal(text, "Policy verdict: ALLOW (permission, not an outcome), not independently verified");
  assert.doesNotMatch(text, /\bsuccess\b/i);
});

test("an unverified success is stated as reported, and only a verified one reads as succeeded, verified", () => {
  assert.equal(statusText(succeededResult), "Result: succeeded, reported, not independently verified");
  assert.equal(statusText(verifiedSuccess), "Result: succeeded, verified");
  assert.equal(statusText({ ...verifiedSuccess, ...verifiedItem, state: "succeeded" }), "Result: succeeded, verified");
});

test("UNKNOWN, LOST and denied results say they are not success, and lossy output is named", () => {
  assert.equal(statusText(unknownResult), "Result: UNKNOWN, not success");
  assert.equal(statusText(item({ id: "l", type: "result", tool: "bash", state: "lost" })), "Result: LOST, not success");
  assert.equal(statusText(item({ id: "d", type: "result", tool: "bash", state: "denied" })), "Result: denied, not success");
  assert.equal(
    statusText({ ...verifiedSuccess, lossy: true }),
    "Result: succeeded, output lossy, not counted as confirmed",
    "a lossy success is not drawn with the ok tone, and its text says so",
  );
});

test("a pending activity and a message state their status in words too", () => {
  assert.equal(statusText(item({ id: "p", type: "activity", tool: "bash", state: "pending" })), "Activity: pending, no outcome yet");
  assert.equal(statusText(prompt), "Message: no verdict or outcome recorded");
});

test("the rail names every tone in words, and an UNKNOWN record is not drawn as a lane that did not act", () => {
  const rail = sequenceLanes([unknownDecision, unknownResult]);
  const policy = rail.lanes.find((lane) => lane.id === "policy");
  const tools = rail.lanes.find((lane) => lane.id === "tools");
  assert.deepEqual(policy.segments.map((segment) => segment.tone), ["unknown", "muted"]);
  assert.deepEqual(tools.segments.map((segment) => segment.tone), ["muted", "unknown"]);
  assert.equal(toneText("unknown"), "UNKNOWN, not success");
  assert.equal(toneText("allow"), "ALLOW, a permission not an outcome");
  assert.equal(toneText("reported"), "succeeded, reported, not independently verified");
  assert.doesNotMatch(toneText("allow"), /\bsuccess\b/i);
});
