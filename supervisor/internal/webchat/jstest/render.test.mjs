import test from "node:test";
import assert from "node:assert/strict";

import { createRefs, installDom } from "./dom.mjs";

installDom();
const { renderAll } = await import("../assets/render.mjs");
const { applySnapshot, createWorkspace } = await import("../assets/store.mjs");

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
  "timeline",
  "panelDecisions",
  "panelProvenance",
  "panelPublication",
  "panelRecovery",
  "panelQualification",
  "panelWarnings",
];

function fixtureSnapshot() {
  return {
    schema_version: "tbound-webchat-observation/v1",
    epoch: "e1",
    cursor: "e1:5",
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
  const chips = refs.timeline.querySelectorAll("span.chip");
  const classes = chips.map((chip) => chip.className);
  assert.ok(classes.some((value) => value.includes("chip-reported")), `expected a reported chip, got ${classes.join(",")}`);
  assert.ok(!classes.some((value) => value.includes("chip-succeeded")), "unverified success must not use the success chip");
});

test("unverified publication is presented as reported, not published", () => {
  const { refs } = renderFixture();
  const chips = refs.panelPublication.querySelectorAll("span.chip");
  const classes = chips.map((chip) => chip.className);
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

test("unavailable source renders an explicit empty state, not fabricated rows", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  renderAll(refs, state);
  assert.match(refs.timeline.textContent, /Loading projection/);
  assert.ok(refs.timeline.querySelectorAll("span.spinner").length > 0, "loading state should show a spinner");
  assert.match(refs.sessions.textContent, /No session projection/);
});
