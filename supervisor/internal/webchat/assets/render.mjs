// render.js — DOM rendering. All text is written with textContent, so projected
// values can never become markup.

import {
  formatClock,
  humanize,
  isVerified,
  isVerifiedProjection,
  shortDigest,
  stateChipClass,
  toolGlyph,
  verdictChipClass,
} from "./format.mjs";
import { groupTurn, turnsOf } from "./store.mjs";

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
  renderConversation(refs, state);
  renderInspector(refs, state);
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
  node.appendChild(el("span", "notice-kind", kind));
  node.appendChild(el("span", "notice-text", text));
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
  const button = el("button", "session-button");
  button.type = "button";
  button.setAttribute("aria-current", "true");
  button.appendChild(el("span", "st", session.title || "Untitled session"));
  button.appendChild(el("span", "sm", `${humanize(session.status)} · ${session.turn_count || 0} turn(s)`));
  li.appendChild(button);
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
  refs.sessionStatus.textContent = humanize(session.status || "unknown");
  refs.sessionStatus.dataset.state = session.status || "unknown";

  const timeline = refs.timeline;
  const openTools = new Set(
    Array.from(timeline.querySelectorAll("details.tool[open]")).map((node) => node.dataset.key),
  );
  clear(timeline);

  if (!state.loaded) {
    timeline.appendChild(emptyState("Loading projection…", "Fetching a bounded snapshot from the gateway."));
    return;
  }
  if (!state.items.length) {
    const message = state.source === "unavailable"
      ? "Production observations are unavailable. No live Pi/session source is registered."
      : "No transcript or activity has been projected yet.";
    timeline.appendChild(emptyState("Nothing to display", message));
    return;
  }

  for (const turn of turnsOf(state.items)) {
    timeline.appendChild(renderTurn(turn));
  }
  for (const node of timeline.querySelectorAll("details.tool")) {
    if (openTools.has(node.dataset.key)) node.open = true;
  }
}

function emptyState(title, message) {
  const node = el("div", "empty-state");
  node.appendChild(el("h3", null, title));
  node.appendChild(el("p", null, message));
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
  kv(publication, "state", chip(pub.state || "unavailable", stateChipClass(pub.state || "unavailable", verified)));
  if (pub.transaction_id) kv(publication, "transaction", monoNode(pub.transaction_id));
  if (pub.observed_at) kv(publication, "observed", formatClock(pub.observed_at));
  if (pub.detail) noteRow(publication, pub.detail);

  const recovery = refs.panelRecovery;
  clear(recovery);
  const rec = panels.recovery || {};
  kv(recovery, "state", chip(rec.state || "unavailable", stateChipClass(rec.state || "unavailable", verified)));
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
