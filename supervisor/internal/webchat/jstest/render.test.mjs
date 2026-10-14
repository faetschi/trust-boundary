import test from "node:test";
import assert from "node:assert/strict";

import { createRefs, installDom } from "./dom.mjs";

installDom();
const { renderAll } = await import("../assets/render.mjs");
const { applySnapshot, createWorkspace, selectRecord, setFilter, setGroup, setQuery, setView, MAX_CLIENT_ITEMS } =
  await import("../assets/store.mjs");

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
  "inspector",
  "viewTabs",
  "chatView",
  "trajectoryView",
  "trajectoryGroups",
  "trajectorySearch",
  "trajectorySummary",
  "sequenceRail",
  "ledger",
  "recordInspector",
  "footStats",
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

/* ---------------------------------------------------------------------------
 * Trajectory view
 * ------------------------------------------------------------------------- */

test("the ledger renders one record row per projected element, with a source chip", () => {
  const { refs } = renderFixture();
  const rows = refs.ledger.querySelectorAll("button.record-row");
  assert.equal(rows.length, 7, "every projected element becomes exactly one record");
  const sources = rows.map((row) => row.dataset.source);
  assert.deepEqual(sources, ["user", "tool", "decision", "result", "tool", "decision", "result"]);
  assert.equal(refs.ledger.querySelectorAll("span.dot").length, 7, "every record carries a status dot");
  assert.match(refs.ledger.textContent, /reason policy_rule_deny/);
  assert.match(refs.ledger.textContent, /go test/);
});

test("the ledger never renders an unverified success as confirmed", () => {
  const { refs } = renderFixture();
  const classes = refs.ledger.querySelectorAll("span.chip").map((chip) => chip.className);
  assert.ok(classes.some((value) => value.includes("chip-reported")), "an unverified success is 'reported'");
  assert.ok(!classes.some((value) => value.includes("chip-succeeded")), "never the confirmed-success chip");
  const dots = refs.ledger.querySelectorAll("span.dot").map((dot) => dot.className);
  assert.ok(dots.some((value) => value.includes("dot-reported")), "the dot matches the chip's trust tone");
  assert.equal(
    dots.filter((value) => value.includes("dot-ok")).length,
    0,
    "no unverified record, and no policy ALLOW, carries the ok tone",
  );
  assert.equal(
    dots.filter((value) => value.includes("dot-allow")).length,
    1,
    "the ALLOW decision has its own permission tone",
  );
});

test("selecting a record swaps the aside to the record inspector for that record", () => {
  const { refs, state } = renderFixture();
  assert.equal(refs.inspector.dataset.mode, "governance", "the governance panels are the default");
  setView(state, "trajectory");
  selectRecord(state, "d2");
  renderAll(refs, state);
  assert.equal(refs.inspector.dataset.mode, "record");
  assert.equal(refs.inspector.getAttribute("aria-label"), "Record inspector");
  assert.match(refs.recordInspector.textContent, /argument|policy_rule_deny/);
  assert.match(refs.recordInspector.textContent, /Decision/);
  // The inspector states the absence of a duration rather than omitting it.
  assert.match(refs.recordInspector.textContent, /unavailable — the projection records no duration/);
  const selected = refs.ledger.querySelectorAll("button.record-row").filter((row) => row.className.includes("is-selected"));
  assert.equal(selected.length, 1);
  assert.equal(selected[0].dataset.id, "d2");
});

test("the sequence rail declares itself ordinal and never draws a duration", () => {
  const { refs } = renderFixture();
  const lanes = refs.sequenceRail.querySelectorAll("div.lane");
  assert.equal(lanes.length, 3, "one lane each for model, policy and tools");
  assert.match(refs.sequenceRail.textContent, /not time/);
  assert.match(refs.sequenceRail.textContent, /no span here is drawn as one/);
  const segments = refs.sequenceRail.querySelectorAll("span.seg");
  assert.ok(segments.length > 0);
  for (const segment of segments) {
    assert.equal(segment.attributes.style, undefined, "segment widths never use an inline style (CSP)");
    assert.match(segment.className, /seg-w[1-8]/);
  }
});

test("the footer marks unrecorded metrics unavailable instead of zero", () => {
  const { refs } = renderFixture();
  assert.match(refs.footStats.textContent, /1 turn\(s\) · 7 record\(s\)/);
  assert.match(refs.footStats.textContent, /policy 1 allow \/ 1 deny \/ 0 unknown/);
  assert.match(refs.footStats.textContent, /token usage unavailable/);
  assert.match(refs.footStats.textContent, /duration unavailable/);
  const unavailable = refs.footStats.children.filter((node) => node.dataset.unavailable === "true");
  assert.equal(unavailable.length, 2);
});

test("the ledger ignores the timeline finding filter but honours the search", () => {
  const { refs, state } = renderFixture();
  setFilter(state, "denied");
  renderAll(refs, state);
  assert.equal(
    refs.ledger.querySelectorAll("button.record-row").length,
    7,
    "the audit ledger is not narrowed by the transcript filter",
  );
  setQuery(state, "curl");
  renderAll(refs, state);
  assert.equal(refs.ledger.querySelectorAll("button.record-row").length, 1);
  assert.match(refs.trajectorySummary.textContent, /1 of 7 record\(s\) matching “curl”/);
});

test("grouping by tool and verdict reorganises the same records", () => {
  const { refs, state } = renderFixture();
  setGroup(state, "verdict");
  renderAll(refs, state);
  assert.match(refs.ledger.textContent, /ALLOW/);
  assert.match(refs.ledger.textContent, /DENY/);
  assert.equal(refs.ledger.querySelectorAll("button.record-row").length, 7, "grouping never drops a record");

  const buttons = refs.trajectoryGroups.children;
  assert.equal(buttons.length, 3);
  assert.equal(buttons[0].dataset.group, "turn");
  assert.equal(buttons[0].getAttribute("aria-pressed"), "false");
  assert.equal(buttons[2].getAttribute("aria-pressed"), "true");

  setGroup(state, "tool");
  renderAll(refs, state);
  assert.match(refs.ledger.textContent, /Messages/);
  assert.equal(refs.ledger.querySelectorAll("button.record-row").length, 7);
});

test("switching to the trajectory view leaves the projection untouched", () => {
  const { state } = renderFixture();
  const before = state.items.length;
  setView(state, "trajectory");
  setGroup(state, "verdict");
  setQuery(state, "deny");
  assert.equal(state.items.length, before);
  assert.equal(state.filter, "all", "view and search never change the finding filter");
});

test("search narrows the ledger but never the governance rail", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  setView(state, "trajectory");
  setQuery(state, "curl");
  renderAll(refs, state);
  const policy = refs.sequenceRail.querySelectorAll("div.lane").find((lane) => lane.className.includes("lane-policy"));
  const tones = policy.querySelectorAll("span.seg").map((seg) => seg.className);
  assert.ok(tones.some((value) => value.includes("seg-deny")), "the DENY stays on the rail while a search narrows the ledger");
  assert.match(refs.sequenceRail.textContent, /always shows all 7 retained records/);
});

test("the skip link follows the visible view", () => {
  const refs = createRefs([...REF_NAMES, "skipLink"]);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  renderAll(refs, state);
  assert.equal(refs.skipLink.getAttribute("href"), "#chat-view");
  setView(state, "trajectory");
  renderAll(refs, state);
  assert.equal(refs.skipLink.getAttribute("href"), "#trajectory-view");
});

/* ---------------------------------------------------------------------------
 * Assistant markdown
 * ------------------------------------------------------------------------- */

// markdownSnapshot adds one assistant and one user message with the same text.
function markdownSnapshot(assistantText = "**Done.** See [docs](https://example.com/docs).") {
  const snapshot = fixtureSnapshot();
  snapshot.cursor = "e1:9";
  snapshot.items.push(
    { id: "m2", sequence: 8, type: "message", turn_id: "t1", occurred_at: "2026-10-13T10:00:04Z", authority: "diagnostic", verification_scope: "unverified-input", role: "assistant", text: assistantText },
    { id: "m3", sequence: 9, type: "message", turn_id: "t1", occurred_at: "2026-10-13T10:00:05Z", authority: "diagnostic", verification_scope: "unverified-input", role: "user", text: "**Done.** See [docs](https://example.com/docs)." },
  );
  return snapshot;
}

// messageNode finds a rendered message by role and, when given, by a snippet of
// its text, because the fixture also holds a user message of its own.
function messageNode(refs, role, snippet = "") {
  return refs.timeline
    .querySelectorAll("div.msg")
    .find((node) => node.dataset.role === role && node.textContent.includes(snippet));
}

test("assistant text renders markdown while the same text from the user stays literal", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, markdownSnapshot());
  renderAll(refs, state);

  const assistant = messageNode(refs, "assistant");
  const links = assistant.querySelectorAll("a");
  assert.equal(assistant.querySelectorAll("strong").length, 1);
  assert.equal(links.length, 1);
  assert.equal(links[0].getAttribute("href"), "https://example.com/docs");
  assert.equal(links[0].getAttribute("rel"), "noopener noreferrer nofollow");

  const user = messageNode(refs, "user", "**Done.**");
  assert.ok(user, "the user message with the same text is rendered");
  assert.equal(user.querySelectorAll("strong").length, 0, "user text is never formatted");
  assert.equal(user.querySelectorAll("a").length, 0, "user text never becomes a link");
  assert.match(user.textContent, /\*\*Done\.\*\* See \[docs\]\(https:\/\/example\.com\/docs\)\./);
});

test("assistant markup from the model is literal, not a link or element", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, markdownSnapshot("<script>alert(1)</script> [x](javascript:alert(1))"));
  renderAll(refs, state);

  const assistant = messageNode(refs, "assistant");
  assert.equal(assistant.querySelectorAll("script").length, 0);
  assert.equal(assistant.querySelectorAll("a").length, 0);
  assert.match(assistant.textContent, /<script>alert\(1\)<\/script> \[x\]\(javascript:alert\(1\)\)/);
});

test("a streaming assistant message keeps its completed blocks across conversation renders", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, markdownSnapshot("# Plan\n\nFirst paragraph.\n\nSecond"));
  renderAll(refs, state);

  const body = refs.timeline.querySelectorAll("div.md-body")[0];
  const [heading, first] = body.children;

  // The retained item grows as tokens arrive; the timeline is rebuilt on each pass.
  state.items.find((item) => item.id === "m2").text = "# Plan\n\nFirst paragraph.\n\nSecond paragraph grows.";
  renderAll(refs, state);

  const grown = refs.timeline.querySelectorAll("div.md-body")[0];
  assert.equal(grown, body, "the message container is reused across renders");
  assert.equal(grown.children[0], heading, "the heading node is untouched");
  assert.equal(grown.children[1], first, "the completed paragraph node is untouched");
  assert.match(grown.textContent, /Second paragraph grows\./);
});

test("the markdown container is pruned once its message leaves the retained window", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, markdownSnapshot());
  renderAll(refs, state);
  assert.equal(refs.timeline.querySelectorAll("div.md-body").length, 1);

  state.items = state.items.filter((item) => item.id !== "m2");
  renderAll(refs, state);
  assert.equal(refs.timeline.querySelectorAll("div.md-body").length, 0);
});

test("a window replaced without provable continuity shows an explicit history-not-loaded notice", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  applySnapshot(state, { ...fixtureSnapshot(), items: [] });
  assert.ok(state.windowGap, "the replacement dropped retained elements");
  renderAll(refs, state);
  assert.match(refs.notices.textContent, /Earlier history not loaded/);
  assert.match(refs.notices.textContent, /No success is inferred/);
  const labels = refs.notices.querySelectorAll("span.notice-kind").map((node) => node.textContent);
  assert.ok(labels.includes("history not loaded"), "the marker is labelled in text, not only by its position");
});

test("a history_evicted gap states the earlier-history fact in its own notice, without a duplicate", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  const snapshot = fixtureSnapshot();
  snapshot.gaps = [{ id: "gap-1", reason: "history_evicted", first_missing: 1, last_missing: 1, recorded_at: "2026-10-14T10:00:00Z" }];
  applySnapshot(state, snapshot);
  renderAll(refs, state);
  assert.match(refs.notices.textContent, /Earlier history not loaded/);
  assert.equal(refs.notices.querySelectorAll("span.notice-icon").length, 1, "one eviction gap is one notice");
});

test("a loaded window with continuity shows no history-not-loaded notice", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  renderAll(refs, state);
  assert.doesNotMatch(refs.notices.textContent, /Earlier history not loaded/);
});

test("the connection status reads connected only when open, and the slow note is text", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  state.connection = "connecting";
  state.connectionWarning = "slow handshake: still connecting";
  renderAll(refs, state);
  assert.equal(refs.connection.textContent, "connecting… · slow handshake: still connecting");
  assert.doesNotMatch(refs.connection.textContent, /connected/);

  state.connection = "open";
  state.connectionWarning = "";
  renderAll(refs, state);
  assert.equal(refs.connection.textContent, "connected");
});

test("an offline stream is labelled offline in text", () => {
  const refs = createRefs(REF_NAMES);
  const state = createWorkspace();
  applySnapshot(state, fixtureSnapshot());
  state.connection = "offline";
  renderAll(refs, state);
  assert.equal(refs.connection.textContent, "offline");
  assert.equal(refs.connection.dataset.state, "offline");
});
