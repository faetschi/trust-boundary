// render.js — DOM rendering. All text is written with textContent, so projected
// values can never become markup.

import {
  SUCCESS_STATES,
  formatClock,
  humanize,
  isVerified,
  isVerifiedProjection,
  shortDigest,
  slug,
  stateChipClass,
  toolGlyph,
  verdictChipClass,
} from "./format.mjs";
import { groupTurn, turnsOf } from "./store.mjs";
import { FILTERS, countFilter, filterItems, governanceCounts } from "./filters.mjs";
import {
  RECORD_GROUPS,
  recordOf,
  sequenceLanes,
  sourceLabel,
  visibleRecords,
  weightClass,
} from "./trajectory.mjs";

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = String(text);
  return node;
}

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

function chip(label, variant) {
  return el("span", `chip chip-${variant}`, label);
}

// trustValue renders a state chip with an explicit, visible trust marker. A
// success the projection has not verified must never read as confirmed success,
// including for assistive technology and copy-paste, so the marker is text, not
// only styling.
function trustValue(value, verified) {
  const variant = stateChipClass(value, verified);
  if (verified || !SUCCESS_STATES.has(slug(value))) {
    return chip(value, variant);
  }
  const wrap = el("span", "trust-value");
  const node = chip(value, variant);
  node.setAttribute("aria-label", `${value}, reported, not verified`);
  wrap.appendChild(node);
  wrap.appendChild(el("span", "trust-marker", "reported, not verified"));
  return wrap;
}

function kv(container, key, valueNode) {
  container.appendChild(el("span", "k", key));
  if (valueNode instanceof Node) {
    const cell = el("span", "v");
    cell.appendChild(valueNode);
    container.appendChild(cell);
    return;
  }
  const text = valueNode === undefined || valueNode === null || valueNode === "" ? "—" : valueNode;
  container.appendChild(el("span", "v", text));
}

function noteRow(container, text) {
  container.appendChild(el("span", "panel-note", text));
}

function digestNode(value) {
  const node = el("span", "digest", shortDigest(value));
  if (value) node.title = String(value);
  return node;
}

function monoNode(value) {
  return el("span", "mono", value);
}

export function renderAll(refs, state) {
  renderHeader(refs, state);
  renderNotices(refs.notices, state);
  renderSessions(refs.sessions, state);
  renderStreams(refs.streams, state);
  applyView(refs, state);
  renderToolbar(refs, state);
  renderConversation(refs, state);
  renderTrajectory(refs, state);
  renderRecordInspector(refs, state);
  renderFootStats(refs, state);
  renderInspector(refs, state);
}

// The view ring is presentation only: switching between Chat and Trajectory
// changes what is shown, never what is held or claimed.
function applyView(refs, state) {
  const view = state.view === "trajectory" ? "trajectory" : "chat";
  if (refs.viewTabs) {
    for (const button of refs.viewTabs.querySelectorAll("button.view-tab")) {
      const active = button.dataset.view === view;
      button.setAttribute("aria-selected", String(active));
      button.className = active ? "view-tab tab is-active" : "view-tab tab";
    }
  }
  if (refs.chatView) toggleHidden(refs.chatView, view !== "chat");
  if (refs.trajectoryView) toggleHidden(refs.trajectoryView, view !== "trajectory");
}

function toggleHidden(node, hidden) {
  if (hidden) node.setAttribute("hidden", "");
  else node.removeAttribute("hidden");
}

// renderToolbar draws the observed posture badges and the timeline filter
// toggle group (coss Toggle Group convention: aria-pressed segmented buttons).
function renderToolbar(refs, state) {
  if (refs.posture) {
    clear(refs.posture);
    const counts = governanceCounts(state.items, isVerified);
    const badges = [
      [counts.allow, `ALLOW ${counts.allow}`, "allow", "policy ALLOW decisions"],
      [counts.deny, `DENY ${counts.deny}`, "deny", "policy DENY decisions"],
      [counts.unknownDecisions, `unknown verdict ${counts.unknownDecisions}`, "unknown", "decisions with an unknown verdict"],
      [counts.verified, `verified ${counts.verified}`, "allow", "successful executions confirmed by the projection"],
      [counts.reported, `reported ${counts.reported}`, "reported", "successes reported but not independently verified"],
      [counts.denied, `denied ${counts.denied}`, "deny", "denied results"],
      [counts.failed, `failed ${counts.failed}`, "deny", "failed results"],
      [counts.pendingResults, `pending ${counts.pendingResults}`, "pending", "results still pending"],
      [counts.unknownResults, `unknown result ${counts.unknownResults}`, "unknown", "results with no retained terminal outcome"],
      [counts.lost, `lost ${counts.lost}`, "unknown", "results whose state is LOST"],
      [counts.lossy, `lossy ${counts.lossy}`, "unknown", "results flagged LOSSY; lossy output is not terminal evidence"],
    ];
    for (const entry of badges) {
      const [value, label, variant, hint] = entry;
      if (!value) continue;
      const badge = el("span", `chip chip-${variant}`, label);
      badge.title = hint;
      refs.posture.appendChild(badge);
    }
    if (!refs.posture.children.length) {
      refs.posture.appendChild(el("span", "posture-empty", "no decisions or results projected"));
    }
  }

  if (refs.timelineFilters) {
    clear(refs.timelineFilters);
    for (const filter of FILTERS) {
      const button = el("button", "toggle-item");
      button.type = "button";
      button.dataset.filter = filter.id;
      button.setAttribute("aria-pressed", String(state.filter === filter.id));
      button.setAttribute("aria-label", `${filter.label} (${filter.hint})`);
      button.appendChild(el("span", null, filter.label));
      const count = countFilter(state.items, filter.id);
      button.appendChild(el("span", "toggle-count", String(count)));
      refs.timelineFilters.appendChild(button);
    }
  }

  if (refs.filterSummary) {
    const total = state.items.length;
    const shown = filterItems(state.items, state.filter).length;
    const matches = countFilter(state.items, state.filter);
    const active = FILTERS.find((entry) => entry.id === state.filter);
    const label = active ? active.label : state.filter;
    const evicted = state.evicted ? ` · ${state.evicted} older element(s) evicted` : "";
    // Distinguish the number of matching elements from the number of elements
    // shown, because a kept tool block also displays its context.
    refs.filterSummary.textContent = state.filter === "all"
      ? `${total} element(s) in the retained window${evicted}`
      : `${matches} match${matches === 1 ? "" : "es"} · ${shown} of ${total} element(s) shown for “${label}” (context included)${evicted}`;
  }
}

function renderHeader(refs, state) {
  refs.sourceBadge.textContent = state.source;
  refs.sourceBadge.dataset.source = state.source;

  const connectionLabels = {
    open: "connected",
    connecting: "connecting…",
    reconnecting: "reconnecting…",
    closed: "stream closed",
    error: "stream error",
  };
  refs.connection.textContent = connectionLabels[state.connection] || state.connection;
  refs.connection.dataset.state = state.connection;

  const qualification = (state.panels && state.panels.qualification) || {};
  refs.claimG1.textContent = String(qualification.g1 === true);
  refs.claimH1.textContent = qualification.h1 || "unexecuted";
  refs.claimContainment.textContent = qualification.containment || "not-established";
  refs.claimBearing.textContent = String(qualification.claim_bearing === true);
}

function renderNotices(container, state) {
  clear(container);
  if (state.error) {
    container.appendChild(notice("error", "projection error", state.error));
  }
  // Retained-window disclosure: eviction must never be silent, or a finding could
  // disappear from the review without notice.
  if (state.evicted > 0) {
    container.appendChild(notice(
      "info",
      "retained window",
      `${state.evicted} older element(s) were evicted from the bounded client window. Counts and filters describe the retained window only.`,
    ));
  }
  for (const gap of state.gaps) {
    const isRange = typeof gap.first_missing === "number";
    const detail = isRange
      ? `elements ${gap.first_missing}–${gap.last_missing} · ${humanize(gap.reason)}. No success is inferred for missing elements.`
      : `cursor ${gap.requested_cursor || "—"} · ${humanize(gap.reason)}. Recovered from a snapshot; no success is inferred for missing elements.`;
    container.appendChild(notice("gap", "continuity gap", detail));
  }
}

function notice(variant, kind, text) {
  const node = el("div", "notice");
  node.dataset.variant = variant;
  const icon = el("span", "notice-icon", variant === "error" ? "!" : variant === "info" ? "i" : "!");
  icon.setAttribute("aria-hidden", "true");
  node.appendChild(icon);
  const body = el("div");
  body.appendChild(el("span", "notice-kind", kind));
  body.appendChild(el("span", "notice-text", text));
  node.appendChild(body);
  return node;
}

function renderSessions(container, state) {
  clear(container);
  const session = state.session;
  if (!session) {
    container.appendChild(el("li", "session-item", "No session projection"));
    return;
  }
  const li = el("li", "session-item");
  // The current session is shown as a non-interactive row: selecting another
  // session is not implemented, so an enabled button would imply a capability
  // the product does not have.
  const row = el("div", "session-row");
  row.setAttribute("aria-current", "true");
  row.appendChild(el("span", "st", session.title || "Untitled session"));
  // The rail must carry the same trust marker as the main status badge: without
  // it a replay's "completed" would appear unqualified.
  const verified = isVerifiedProjection(state);
  row.appendChild(el("span", "sm", `${humanize(session.status)}${verified ? "" : " · reported"} · ${session.turn_count || 0} turn(s)`));
  li.appendChild(row);
  li.appendChild(el("p", "session-note", "Current session. Switching sessions requires the supervisor session service (not implemented)."));
  container.appendChild(li);
}

function renderStreams(container, state) {
  clear(container);
  if (!state.streams.length) {
    container.appendChild(el("li", "stream-item", "No observation stream registered"));
    return;
  }
  for (const stream of state.streams) {
    const li = el("li", "stream-item");
    const health = el("span", "stream-health", stream.health);
    health.dataset.health = stream.health;
    li.appendChild(health);
    const body = el("span");
    body.appendChild(el("span", "stream-label", stream.label || stream.id));
    if (stream.reason) body.appendChild(el("span", "stream-reason", stream.reason));
    li.appendChild(body);
    container.appendChild(li);
  }
}

function renderConversation(refs, state) {
  const session = state.session || {};
  refs.sessionTitle.textContent = session.title || "No session";
  refs.sessionSub.textContent = [session.session_id, session.workflow_id, session.pi_version ? `Pi ${session.pi_version}` : null]
    .filter(Boolean)
    .join(" · ") || "—";
  const verified = isVerifiedProjection(state);
  refs.sessionStatus.textContent = verified
    ? humanize(session.status || "unknown")
    : `${humanize(session.status || "unknown")} · reported`;
  refs.sessionStatus.dataset.state = session.status || "unknown";

  const timeline = refs.timeline;
  const openTools = new Set(
    Array.from(timeline.querySelectorAll("details.tool[open]")).map((node) => node.dataset.key),
  );
  clear(timeline);

  if (!state.loaded) {
    timeline.appendChild(loadingState());
    return;
  }
  if (!state.items.length) {
    const message = state.source === "unavailable"
      ? "Production observations are unavailable. No live Pi/session source is registered."
      : "No transcript or activity has been projected yet.";
    timeline.appendChild(emptyState("Nothing to display", message));
    return;
  }

  const visible = filterItems(state.items, state.filter);
  if (!visible.length) {
    const active = FILTERS.find((entry) => entry.id === state.filter);
    timeline.appendChild(emptyState(
      `No ${active ? active.label.toLowerCase() : state.filter} findings`,
      "No projected element matches this filter. Adjust the filter to review the whole timeline.",
    ));
    return;
  }

  for (const turn of turnsOf(visible)) {
    timeline.appendChild(renderTurn(turn));
  }
  for (const node of timeline.querySelectorAll("details.tool")) {
    if (openTools.has(node.dataset.key)) node.open = true;
  }
}

function emptyState(title, message, variant) {
  const node = el("div", variant === "error" ? "error-state" : "empty-state");
  const icon = el("span", "empty-icon", variant === "error" ? "!" : "∅");
  icon.setAttribute("aria-hidden", "true");
  node.appendChild(icon);
  node.appendChild(el("h3", null, title));
  node.appendChild(el("p", null, message));
  return node;
}

// Loading state: a spinner plus skeleton rows, matching the coss Empty/Skeleton
// convention without any framework.
function loadingState() {
  const node = el("div", "empty-state");
  const spinner = el("span", "spinner");
  spinner.setAttribute("aria-hidden", "true");
  node.appendChild(spinner);
  node.appendChild(el("h3", null, "Loading projection"));
  node.appendChild(el("p", null, "Fetching a bounded snapshot from the gateway."));
  const first = el("div", "skeleton");
  const second = el("div", "skeleton skeleton-sm");
  const row = el("div", "skeleton-row");
  row.appendChild(first);
  row.appendChild(second);
  node.appendChild(row);
  return node;
}

function renderTurn(turn) {
  const section = el("section", "turn");
  const head = el("div", "turn-head");
  const label = turn.id === "__untagged" ? "turn" : turn.id.startsWith("turn") ? turn.id : `turn ${turn.id}`;
  head.appendChild(el("span", null, label));
  head.appendChild(el("span", null, `${turn.items.length} element(s)`));
  section.appendChild(head);

  const body = el("div", "turn-body");
  for (const block of groupTurn(turn.items)) {
    if (block.kind === "message") body.appendChild(renderMessage(block.item));
    else body.appendChild(renderTool(block));
  }
  section.appendChild(body);
  return section;
}

function renderMessage(item) {
  const node = el("div", "msg");
  node.dataset.role = item.role;
  const roleLine = el("div", "msg-role");
  roleLine.appendChild(el("span", null, item.role || "message"));
  roleLine.appendChild(el("span", null, " · "));
  roleLine.appendChild(el("span", null, formatClock(item.occurred_at)));
  if (!isVerified(item)) {
    roleLine.appendChild(el("span", null, " · "));
    roleLine.appendChild(chip("unverified", "unverified"));
  }
  node.appendChild(roleLine);
  node.appendChild(el("p", "msg-text", item.text || "(empty)"));
  if (item.text_truncated) node.appendChild(el("p", "step-detail", "Transcript text truncated by the projection."));
  return node;
}

function renderTool(block) {
  const details = el("details", "tool");
  details.dataset.key = block.key;
  const activity = block.activity;
  const decision = block.decision;
  const result = block.result;

  const summary = el("summary");
  summary.appendChild(el("span", "tool-glyph", toolGlyph((activity && activity.tool) || (decision && decision.tool) || (result && result.tool))));
  summary.appendChild(el("span", "tool-name", (activity && activity.tool) || (decision && decision.tool) || (result && result.tool) || "tool"));
  summary.appendChild(el("span", "tool-summary", summaryText(block)));

  const chips = el("span", "tool-chips");
  if (decision) chips.appendChild(chip(decision.verdict, verdictChipClass(decision.verdict)));
  const stateItem = result || activity;
  if (stateItem && stateItem.state) {
    chips.appendChild(chip(stateItem.state, stateChipClass(stateItem.state, isVerified(stateItem))));
  }
  if (!isVerified(result || activity || decision)) chips.appendChild(chip("unverified", "unverified"));
  summary.appendChild(chips);
  details.appendChild(summary);

  const steps = el("div", "tool-steps");
  if (activity) {
    steps.appendChild(step("request", [monoNode(activity.summary || "—"), el("span", null, `  ${formatClock(activity.occurred_at)}`)]));
  }
  if (decision) {
    const body = [chip(decision.verdict, verdictChipClass(decision.verdict)), el("span", null, " "), monoNode(decision.reason_code || "unknown"), el("span", null, " "), digestNode(decision.policy_digest)];
    const row = step("policy", body);
    if (decision.summary) row.querySelector(".step-body").appendChild(el("div", "step-detail", decision.summary));
    steps.appendChild(row);
  }
  if (result) {
    const body = [chip(result.state, stateChipClass(result.state, isVerified(result)))];
    if (result.lossy) body.push(el("span", null, " "), chip("lossy", "unknown"));
    if (result.digest) body.push(el("span", null, " "), digestNode(result.digest));
    const row = step("result", body);
    const cell = row.querySelector(".step-body");
    if (result.summary) cell.appendChild(el("div", "step-detail", result.summary));
    if (result.detail) cell.appendChild(el("div", "step-detail", result.detail));
    steps.appendChild(row);
  }
  if (activity && activity.detail) steps.appendChild(el("div", "step-detail", activity.detail));
  details.appendChild(steps);
  return details;
}

function summaryText(block) {
  const candidates = [block.activity, block.decision, block.result];
  for (const candidate of candidates) {
    if (candidate && candidate.summary) return candidate.summary;
  }
  return "tool call (no bounded summary projected)";
}

function step(label, bodyNodes) {
  const row = el("div", "step");
  row.appendChild(el("span", "step-label", label));
  const body = el("div", "step-body");
  for (const node of bodyNodes) body.appendChild(node);
  row.appendChild(body);
  return row;
}

function renderInspector(refs, state) {
  const panels = state.panels || {};
  const verified = isVerifiedProjection(state);

  const decisions = refs.panelDecisions;
  clear(decisions);
  const governance = panels.governance || {};
  kv(decisions, "allow", String(governance.allow_count ?? 0));
  kv(decisions, "deny", String(governance.deny_count ?? 0));
  kv(decisions, "unknown", String(governance.unknown_count ?? 0));
  kv(decisions, "policy", digestNode(governance.policy_digest));
  if (governance.note) noteRow(decisions, governance.note);

  const provenance = refs.panelProvenance;
  clear(provenance);
  const prov = panels.provenance || {};
  kv(provenance, "independently verified", prov.available === true ? "yes" : "no");
  kv(provenance, "profile", monoNode(prov.profile_id || "—"));
  kv(provenance, "profile hash", digestNode(prov.profile_hash));
  kv(provenance, "source digest", digestNode(prov.source_digest));
  kv(provenance, "containment", monoNode(prov.containment_profile || "—"));
  kv(provenance, "containment hash", digestNode(prov.containment_digest));
  kv(provenance, "provider", monoNode(prov.provider_profile || "—"));
  kv(provenance, "runtime closure", monoNode(prov.runtime_closure || "—"));
  if (prov.tcb_proof_digest) kv(provenance, "launch proof", digestNode(prov.tcb_proof_digest));
  noteRow(provenance, prov.note || "Provenance references are not independently verified here.");

  const publication = refs.panelPublication;
  clear(publication);
  const pub = panels.publication || {};
  kv(publication, "state", trustValue(pub.state || "unavailable", verified));
  kv(publication, "trust", verified ? "verified by the gateway" : "reported by the source, not independently verified");
  if (pub.transaction_id) kv(publication, "transaction", monoNode(pub.transaction_id));
  if (pub.observed_at) kv(publication, "observed", formatClock(pub.observed_at));
  if (pub.detail) noteRow(publication, pub.detail);

  const recovery = refs.panelRecovery;
  clear(recovery);
  const rec = panels.recovery || {};
  kv(recovery, "state", trustValue(rec.state || "unavailable", verified));
  kv(recovery, "trust", verified ? "verified by the gateway" : "reported by the source, not independently verified");
  if (rec.generation) kv(recovery, "generation", monoNode(rec.generation));
  if (typeof rec.last_completed_sequence === "number") kv(recovery, "last completed seq", String(rec.last_completed_sequence));
  if (rec.detail) noteRow(recovery, rec.detail);

  const qualification = refs.panelQualification;
  clear(qualification);
  const qual = panels.qualification || {};
  kv(qualification, "G1", String(qual.g1 === true));
  kv(qualification, "H1", qual.h1 || "unexecuted");
  kv(qualification, "containment", qual.containment || "not-established");
  kv(qualification, "claim-bearing", String(qual.claim_bearing === true));
  if (qual.note) noteRow(qualification, qual.note);

  const warnings = refs.panelWarnings;
  clear(warnings);
  const list = state.warnings.length ? state.warnings : ["No warnings projected."];
  for (const warning of list) warnings.appendChild(el("li", null, warning));
}

/* ---------------------------------------------------------------------------
 * Trajectory view
 *
 * The ledger is a second projection of the same bounded items: a turn-aware
 * record list with a governance lens. It cannot alter a projection, a verdict,
 * or a filter outcome, and it never renders a measurement the projection did not
 * record.
 * ------------------------------------------------------------------------- */

function dotNode(tone) {
  const node = el("span", `dot dot-${tone}`);
  node.setAttribute("aria-hidden", "true");
  node.title = `status: ${tone}`;
  return node;
}

function sourceChip(source) {
  return chip(sourceLabel(source), `source-${source}`);
}

function renderTrajectory(refs, state) {
  const view = visibleRecords(state);
  const group = RECORD_GROUPS.find((entry) => entry.id === (state.group || "turn"));

  if (refs.trajectoryGroups) {
    clear(refs.trajectoryGroups);
    for (const entry of RECORD_GROUPS) {
      const button = el("button", "toggle-item group-tab");
      button.type = "button";
      button.dataset.group = entry.id;
      button.setAttribute("aria-pressed", String((state.group || "turn") === entry.id));
      button.setAttribute("aria-label", `Group records by ${entry.label} (${entry.hint})`);
      button.appendChild(el("span", null, entry.label));
      refs.trajectoryGroups.appendChild(button);
    }
  }

  // Never overwrite the search box while the reader is typing in it.
  if (
    refs.trajectorySearch &&
    refs.trajectorySearch.value !== state.query &&
    document.activeElement !== refs.trajectorySearch
  ) {
    refs.trajectorySearch.value = state.query || "";
  }

  if (refs.trajectorySummary) {
    const searching = Boolean(String(state.query || "").trim());
    const narrowed = searching ? `${view.items.length} of ${state.items.length} record(s) matching “${state.query}”` : `${view.items.length} record(s)`;
    refs.trajectorySummary.textContent = `${narrowed} · grouped by ${group ? group.label : "Turn"}`;
  }

  if (refs.sequenceRail) {
    clear(refs.sequenceRail);
    if (!state.loaded) {
      refs.sequenceRail.appendChild(el("p", "rail-note", "Loading projection…"));
    } else if (!view.items.length) {
      refs.sequenceRail.appendChild(el("p", "rail-note", "No records to chart."));
    } else {
      const rail = sequenceLanes(view.items);
      for (const lane of rail.lanes) {
        const row = el("div", `lane lane-${lane.id}`);
        row.appendChild(el("span", "lane-label", lane.label));
        const track = el("span", "lane-track");
        for (const segment of lane.segments) {
          const node = el("span", `seg seg-${segment.tone} ${weightClass(segment.weight)}`);
          node.title = `${lane.label}: ${segment.weight} record(s), ${segment.tone}`;
          track.appendChild(node);
        }
        row.appendChild(track);
        refs.sequenceRail.appendChild(row);
      }
      const axis = el("p", "rail-axis", rail.note);
      axis.dataset.ordinal = "true";
      refs.sequenceRail.appendChild(axis);
    }
  }

  const ledger = refs.ledger;
  if (!ledger) return;
  clear(ledger);
  if (!state.loaded) {
    ledger.appendChild(loadingState());
    return;
  }
  if (!state.items.length) {
    ledger.appendChild(emptyState(
      "No records",
      state.source === "unavailable"
        ? "Production observations are unavailable. No live Pi/session source is registered."
        : "No record has been projected yet.",
    ));
    return;
  }
  if (!view.items.length) {
    ledger.appendChild(emptyState(
      "No matching records",
      "No record matches the current search. Clear the search to review the whole trajectory.",
    ));
    return;
  }
  for (const entry of view.groups) {
    ledger.appendChild(renderRecordGroup(state, entry));
  }
}

function renderRecordGroup(state, group) {
  const node = el("div", "record-group");
  node.setAttribute("role", "group");
  node.setAttribute("aria-label", group.title);
  const head = el("div", "group-head");
  head.appendChild(el("span", "group-title", group.title));
  if (group.meta) head.appendChild(el("span", "group-meta", group.meta));
  node.appendChild(head);
  const body = el("div", "group-body");
  for (const record of group.records) body.appendChild(renderRecordRow(state, record));
  node.appendChild(body);
  return node;
}

function renderRecordRow(state, record) {
  const selected = state.selectedId === record.id;
  const row = el("button", selected ? "record-row is-selected" : "record-row");
  row.type = "button";
  row.dataset.id = record.id;
  row.dataset.source = record.source;
  row.setAttribute("aria-pressed", String(selected));
  const chips = record.chips.map((entry) => entry.label).join(", ");
  row.setAttribute(
    "aria-label",
    `${sourceLabel(record.source)} record ${record.sequence}: ${record.label}${chips ? `. ${chips}` : ""}`,
  );
  row.appendChild(dotNode(record.dot));
  row.appendChild(sourceChip(record.source));
  row.appendChild(el("span", "record-label", record.label));
  const payload = el("span", "record-payload mono", record.payload);
  if (record.payload) payload.title = record.payload;
  row.appendChild(payload);
  const tail = el("span", "record-tail");
  for (const entry of record.chips) {
    const node = chip(entry.label, entry.variant);
    if (entry.hint) node.title = entry.hint;
    tail.appendChild(node);
  }
  row.appendChild(tail);
  return row;
}

// The record inspector shares the aside with the session-scoped governance
// panels. It is only shown for a selected record in the Trajectory view; the
// panels remain the default so the claim boundary is never hidden.
function renderRecordInspector(refs, state) {
  const item = state.selectedId && state.itemsById ? state.itemsById.get(state.selectedId) : null;
  const record = item ? recordOf(item) : null;
  const mode = state.view === "trajectory" && record ? "record" : "governance";
  if (refs.inspector) {
    refs.inspector.dataset.mode = mode;
    refs.inspector.setAttribute(
      "aria-label",
      mode === "record" ? "Record inspector" : "Governance inspector",
    );
  }
  const container = refs.recordInspector;
  if (!container) return;
  clear(container);
  if (!record) {
    if (state.view === "trajectory") {
      container.appendChild(el(
        "p",
        "rail-note",
        "Select a record in the ledger to inspect its bounded fields. The session-scoped governance panels are shown below.",
      ));
    }
    return;
  }

  const head = el("div", "record-inspector-head");
  head.appendChild(sourceChip(record.source));
  head.appendChild(el(
    "span",
    "record-inspector-pos",
    `sequence ${record.sequence}${item.turn_id ? ` · turn ${item.turn_id}` : ""}`,
  ));
  const close = el("button", "ghost-button record-close", "Close");
  close.type = "button";
  close.dataset.action = "clear-record";
  head.appendChild(close);
  container.appendChild(head);

  for (const section of record.detail) {
    const block = el("section", "record-section");
    block.appendChild(el("h3", null, section.title));
    const grid = el("div", "kv");
    for (const row of section.rows) {
      const key = row[0];
      const value = row[1];
      const options = row[2] || {};
      grid.appendChild(el("span", "k", key));
      const cell = el("span", "v");
      if (options.chip) cell.appendChild(chip(String(value), options.chip));
      else if (options.digest) cell.appendChild(digestNode(value === "—" ? null : value));
      else if (options.mono) cell.appendChild(monoNode(String(value)));
      else cell.textContent = String(value);
      grid.appendChild(cell);
    }
    block.appendChild(grid);
    if (section.note) block.appendChild(el("p", "record-note", section.note));
    container.appendChild(block);
  }
}

// The footer states what was observed and marks every metric the projection does
// not carry as unavailable. It never renders a zero where the value is unknown.
function renderFootStats(refs, state) {
  const container = refs.footStats;
  if (!container) return;
  clear(container);
  const counts = governanceCounts(state.items, isVerified);
  const session = state.session || {};
  const stats = [
    `${session.turn_count || 0} turn(s) · ${state.items.length} record(s)`,
    `policy ${counts.allow} allow / ${counts.deny} deny / ${counts.unknownDecisions} unknown`,
    `${counts.reported} reported${counts.verified ? ` · ${counts.verified} verified` : ""}`,
    "token usage unavailable",
    "duration unavailable",
  ];
  for (const text of stats) {
    if (container.children.length) {
      const separator = el("span", "foot-sep", "·");
      separator.setAttribute("aria-hidden", "true");
      container.appendChild(separator);
    }
    const node = el("span", "foot-stat", text);
    if (/unavailable/.test(text)) node.dataset.unavailable = "true";
    container.appendChild(node);
  }
}
