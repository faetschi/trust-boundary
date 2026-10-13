import test from "node:test";
import assert from "node:assert/strict";

import { createRefs, installDom } from "./dom.mjs";

installDom();
const { renderAll } = await import("../assets/render.mjs");
const { applySnapshot, createWorkspace, setFilter, MAX_CLIENT_ITEMS } = await import("../assets/store.mjs");

const REF_NAMES = [
  "sourceBadge",
  "connection",
  "claimG1",
  "claimH1",
  "claimContainment",
  "claimBearing",
  "notices",
  "sessions",
  "streams",
  "sessionTitle",
  "sessionSub",
  "sessionStatus",
  "posture",
  "timelineFilters",
  "filterSummary",
  "statusAnnouncer",
  "timeline",
  "panelDecisions",
  "panelProvenance",
  "panelPublication",
  "panelRecovery",
  "panelQualification",
  "panelWarnings",
];

// fixtureSnapshot carries two correlated tool blocks: one policy ALLOW with a
// succeeded (unverified) result, and one policy DENY with a denied result.
function fixtureSnapshot() {
  return {
    schema_version: "tbound-webchat-observation/v1",
    epoch: "e1",
    cursor: "e1:7",
    source: "fixture",
    verification_scope: "unverified-input",
    generated_at: "2026-10-13T10:00:00Z",
    session: { session_id: "s1", title: "Demo session", status: "completed", turn_count: 1 },
    panels: {
      governance: { allow_count: 1, deny_count: 1, unknown_count: 0, policy_digest: "sha256:aaaa" },
      provenance: { available: false, profile_id: "dev" },
      publication: { state: "published", detail: "illustrative" },
      recovery: { state: "quarantined" },
      qualification: { g1: false, h1: "unexecuted", containment: "not-established", claim_bearing: false },
    },
    streams: [{ id: "fixture", label: "fixture", health: "complete", available: true }],
    warnings: ["fixture warning"],
    items: [
      { id: "m1", sequence: 1, type: "message", turn_id: "t1", occurred_at: "2026-10-13T10:00:00Z", authority: "diagnostic", verification_scope: "unverified-input", role: "user", text: "hello world" },
      { id: "a1", sequence: 2, type: "activity", turn_id: "t1", occurred_at: "2026-10-13T10:00:01Z", authority: "diagnostic", verification_scope: "unverified-input", kind: "proposal", tool: "bash", tool_call_id: "c1", summary: "bash: go test", state: "pending" },
      { id: "d1", sequence: 3, type: "decision", turn_id: "t1", occurred_at: "2026-10-13T10:00:01Z", authority: "diagnostic", verification_scope: "unverified-input", tool: "bash", tool_call_id: "c1", verdict: "ALLOW", reason_code: "policy_rule_allow" },
      { id: "r1", sequence: 4, type: "result", turn_id: "t1", occurred_at: "2026-10-13T10:00:02Z", authority: "diagnostic", verification_scope: "unverified-input", tool: "bash", tool_call_id: "c1", state: "succeeded", summary: "exit 0" },
      { id: "a2", sequence: 5, type: "activity", turn_id: "t1", occurred_at: "2026-10-13T10:00:03Z", authority: "diagnostic", verification_scope: "unverified-input", kind: "proposal", tool: "bash", tool_call_id: "c2", summary: "bash: curl https://example.com", state: "pending" },
      { id: "d2", sequence: 6, type: "decision", turn_id: "t1", occurred_at: "2026-10-13T10:00:03Z", authority: "diagnostic", verification_scope: "unverified-input", tool: "bash", tool_call_id: "c2", verdict: "DENY", reason_code: "policy_rule_deny" },
      { id: "r2", sequence: 7, type: "result", turn_id: "t1", occurred_at: "2026-10-13T10:00:03Z", authority: "diagnostic", verification_scope: "unverified-input", tool: "bash", tool_call_id: "c2", state: "denied", summary: "denial was durable" },
    ],
  };
}

function renderFixture() {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  renderAll(refs, state);
  return { refs, state };
}

test("renderAll completes without throwing and shows the transcript", () => {
  const { refs } = renderFixture();
  assert.match(refs.timeline.textContent, /hello world/);
  assert.match(refs.timeline.textContent, /go test/);
  assert.equal(refs.sessionTitle.textContent, "Demo session");
});

test("unverified success never renders with the confirmed-success chip", () => {
  const { refs } = renderFixture();
  const classes = refs.timeline.querySelectorAll("span.chip").map((chip) => chip.className);
  assert.ok(classes.some((value) => value.includes("chip-reported")), `expected a reported chip, got ${classes.join(",")}`);
  assert.ok(!classes.some((value) => value.includes("chip-succeeded")), "unverified success must not use the success chip");
});

test("unverified publication is presented as reported, not published", () => {
  const { refs } = renderFixture();
  const classes = refs.panelPublication.querySelectorAll("span.chip").map((chip) => chip.className);
  assert.ok(classes.some((value) => value.includes("chip-reported")));
  assert.ok(!classes.some((value) => value.includes("chip-published")));
});

test("inspector renders bounded key/value rows and the claim boundary", () => {
  const { refs } = renderFixture();
  assert.match(refs.panelDecisions.textContent, /policy/);
  assert.match(refs.panelQualification.textContent, /unexecuted/);
  assert.equal(refs.claimG1.textContent, "false");
  assert.equal(refs.claimBearing.textContent, "false");
  assert.match(refs.panelWarnings.textContent, /fixture warning/);
});

test("unverified panel and session claims carry an explicit textual trust label", () => {
  const { refs } = renderFixture();
  // Publication/recovery must state their trust explicitly, not only via colour.
  assert.match(refs.panelPublication.textContent, /reported by the source, not independently verified/);
  assert.match(refs.panelRecovery.textContent, /reported by the source, not independently verified/);
  assert.match(refs.panelPublication.textContent, /reported, not verified/);
  assert.match(refs.sessionStatus.textContent, /reported/);
});

test("the sessions rail carries the unverified marker", () => {
  const { refs } = renderFixture();
  // The rail must not show a replay's "completed" unqualified.
  assert.match(refs.sessions.textContent, /reported/);
});

test("the current session is a non-interactive row, not a dead control", () => {
  const { refs } = renderFixture();
  assert.equal(refs.sessions.querySelectorAll("button").length, 0, "no dead session button may be rendered");
  assert.match(refs.sessions.textContent, /Current session/);
});

test("snapshot continuity gaps are surfaced as explicit notices", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  const snapshot = fixtureSnapshot();
  snapshot.gaps = [{ id: "gap-1", reason: "history_evicted", first_missing: 2, last_missing: 6, recorded_at: "2026-10-13T10:00:00Z" }];
  applySnapshot(state, snapshot);
  renderAll(refs, state);
  assert.match(refs.notices.textContent, /continuity gap/);
  assert.match(refs.notices.textContent, /No success is inferred/);
  assert.equal(refs.notices.querySelectorAll("span.notice-icon").length, 1, "a notice must render its alert icon");
});

test("loading state renders a spinner and skeleton rows", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  renderAll(refs, state);
  assert.match(refs.timeline.textContent, /Loading projection/);
  assert.equal(refs.timeline.querySelectorAll("span.spinner").length, 1);
  assert.equal(refs.timeline.querySelectorAll("div.skeleton").length, 2);
});

test("an unloaded projection shows no session and no fabricated rows", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  renderAll(refs, state);
  assert.match(refs.sessions.textContent, /No session projection/);
  assert.match(refs.posture.textContent, /no decisions or results projected/);
});

test("toolbar renders the filter toggle group with pressed state and counts", () => {
  const { refs } = renderFixture();
  const buttons = refs.timelineFilters.children;
  assert.equal(buttons.length, 4);
  assert.equal(buttons[0].dataset.filter, "all");
  assert.equal(buttons[0].getAttribute("aria-pressed"), "true");
  assert.equal(buttons[1].getAttribute("aria-pressed"), "false");
  assert.equal(buttons[0].children[1].textContent, "7", "the All count is the element count");
  assert.equal(buttons[1].children[1].textContent, "2", "the Denied count counts DENY decisions and denied results");
  assert.match(refs.filterSummary.textContent, /in the retained window/);
});

test("toolbar posture separates policy ALLOW from reported execution", () => {
  const { refs } = renderFixture();
  const text = refs.posture.textContent;
  assert.match(text, /ALLOW 1/);
  assert.match(text, /DENY 1/);
  assert.match(text, /reported 1/);
  assert.match(text, /denied 1/);
  assert.ok(!/verified 1/.test(text), "an unverified success must not be counted as verified");
});

test("selecting the denied filter hides non-matching blocks and messages", () => {
  const { refs, state } = renderFixture();
  setFilter(state, "denied");
  renderAll(refs, state);
  assert.match(refs.timeline.textContent, /curl/);
  assert.ok(!refs.timeline.textContent.includes("hello world"), "transcript messages are hidden while filtering");
  assert.ok(!refs.timeline.textContent.includes("go test"), "non-matching tool blocks are hidden");
  const active = refs.timelineFilters.children.find((button) => button.getAttribute("aria-pressed") === "true");
  assert.equal(active.dataset.filter, "denied");
  // The summary must distinguish matches from shown elements (context included).
  assert.match(refs.filterSummary.textContent, /2 matches · 3 of 7 element/);
});

test("a filter with no matches renders the empty-filter state", () => {
  const { refs, state } = renderFixture();
  setFilter(state, "failed");
  renderAll(refs, state);
  assert.match(refs.timeline.textContent, /No failed findings/);
  assert.match(refs.filterSummary.textContent, /0 matches · 0 of 7 element/);
});
