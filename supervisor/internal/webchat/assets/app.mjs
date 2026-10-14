// app.js — wiring for the tbound Pi chat workspace.
//
// The client is an untrusted presentation layer: it fetches a bounded snapshot,
// follows a cursor-based event stream, and re-renders. It has no control route
// and never invents success; any continuity gap forces recovery from a fresh
// snapshot.

import { fetchSnapshot, openEventStream } from "./api.mjs";
import {
  clearRecord,
  createWorkspace,
  selectRecord,
  setFilter,
  setGroup,
  setQuery,
  setView,
} from "./store.mjs";
import { createStream } from "./stream.mjs";
import { renderAll, setRovingTabStop } from "./render.mjs";
import { humanize } from "./format.mjs";
import {
  isAtTail,
  jumpLabel,
  maxScrollTop,
  pendingSince,
  restoredScrollTop,
  tailAnnouncement,
} from "./anchor.mjs";

const refs = {
  sourceBadge: document.getElementById("source-badge"),
  connection: document.getElementById("connection"),
  claimG1: document.getElementById("claim-g1"),
  claimH1: document.getElementById("claim-h1"),
  claimContainment: document.getElementById("claim-containment"),
  claimBearing: document.getElementById("claim-bearing"),
  notices: document.getElementById("notices"),
  statusAnnouncer: document.getElementById("status-announcer"),
  sessions: document.getElementById("sessions"),
  streams: document.getElementById("streams"),
  sessionTitle: document.getElementById("session-title"),
  sessionSub: document.getElementById("session-sub"),
  sessionStatus: document.getElementById("session-status"),
  timeline: document.getElementById("timeline"),
  statusAnnouncer: document.getElementById("status-announcer"),
  posture: document.getElementById("posture"),
  timelineFilters: document.getElementById("timeline-filters"),
  filterSummary: document.getElementById("filter-summary"),
  panelDecisions: document.getElementById("panel-decisions"),
  panelProvenance: document.getElementById("panel-provenance"),
  panelPublication: document.getElementById("panel-publication"),
  panelRecovery: document.getElementById("panel-recovery"),
  panelQualification: document.getElementById("panel-qualification"),
  panelWarnings: document.getElementById("panel-warnings-list"),
  inspector: document.getElementById("inspector"),
  inspectorToggle: document.getElementById("inspector-toggle"),
  viewTabs: document.getElementById("view-tabs"),
  skipLink: document.getElementById("skip-link"),
  chatView: document.getElementById("chat-view"),
  trajectoryView: document.getElementById("trajectory-view"),
  trajectoryGroups: document.getElementById("trajectory-groups"),
  trajectorySearch: document.getElementById("trajectory-search"),
  trajectorySummary: document.getElementById("trajectory-summary"),
  sequenceRail: document.getElementById("sequence-rail"),
  ledger: document.getElementById("ledger"),
  recordInspector: document.getElementById("record-inspector"),
  footStats: document.getElementById("foot-stats"),
  chatJump: document.getElementById("chat-jump"),
  trajectoryJump: document.getElementById("trajectory-jump"),
};

const state = createWorkspace();

// The tail window. The page is the scroller. A reader at the tail keeps following
// new records. A reader scrolled away keeps their place, and the records applied
// meanwhile are counted on that view's "Jump to latest" control. The arithmetic
// lives in anchor.mjs; this section only reads the page and writes to it.
//
// Each view keeps a pin: the applied-record count when the reader was last at the
// tail, or first saw the view. The count of new records is the distance from it.
const pins = { chat: 0, trajectory: 0 };
// A view's pin starts when the reader first sees it. A return visit keeps the pin,
// so records applied while they were in the other view are still counted here.
const visited = { chat: false, trajectory: false };
// Records in the view at the last render. A view with nothing in it yet is not
// followed, so the first load opens at the top of the transcript.
let renderedRecords = 0;
let announcedRecords = 0;
let announceTimer = null;
// Stream bursts re-render once per record; one announcement covers the burst.
const TAIL_ANNOUNCE_DELAY_MS = 400;

function scrollRoot() {
  return document.scrollingElement || document.documentElement;
}

function scrollMetrics() {
  const root = scrollRoot();
  return { scrollTop: root.scrollTop, clientHeight: root.clientHeight, scrollHeight: root.scrollHeight };
}

function visibleView() {
  return refs.trajectoryView && !refs.trajectoryView.hasAttribute("hidden") ? "trajectory" : "chat";
}

function viewSection(view) {
  return view === "trajectory" ? refs.trajectoryView : refs.chatView;
}

// Taken before a re-render. A reader away from the tail is pinned to the first
// record visible at the top of the viewport, and to its offset from that top.
function captureTail(view) {
  const metrics = scrollMetrics();
  const capture = { atTail: renderedRecords > 0 && isAtTail(metrics), capturedScrollTop: metrics.scrollTop };
  if (capture.atTail) return capture;
  for (const node of viewSection(view).querySelectorAll("[data-anchor]")) {
    const rect = node.getBoundingClientRect();
    if (rect.bottom > 0) {
      capture.anchor = node.dataset.anchor;
      capture.capturedOffset = rect.top;
      break;
    }
  }
  return capture;
}

// Taken after a re-render, for a view that was already visible. A reader at the
// tail is moved to the new bottom; a reader away is moved so the pinned record
// stays where it was.
function settleScroll(view, capture) {
  if (capture.atTail) {
    window.scrollTo(0, maxScrollTop(scrollMetrics()));
    return;
  }
  let nextOffset;
  if (capture.anchor) {
    const node = Array.from(viewSection(view).querySelectorAll("[data-anchor]"))
      .find((candidate) => candidate.dataset.anchor === capture.anchor);
    if (node) nextOffset = node.getBoundingClientRect().top;
  }
  const metrics = scrollMetrics();
  window.scrollTo(0, restoredScrollTop({
    capturedScrollTop: capture.capturedScrollTop,
    capturedOffset: capture.capturedOffset,
    currentScrollTop: metrics.scrollTop,
    nextOffset,
  }, metrics));
}

// Shows the view's control only while the reader is away and records are waiting.
function updateTail(view) {
  const atTail = isAtTail(scrollMetrics());
  if (atTail) pins[view] = state.applied;
  const pending = pendingSince(state.applied, pins[view]);
  const controls = { chat: refs.chatJump, trajectory: refs.trajectoryJump };
  for (const name of Object.keys(controls)) {
    const control = controls[name];
    if (!control) continue;
    if (name === view && pending > 0 && !atTail) {
      // The page scrolls often; rewrite the label only when the count changes.
      const label = jumpLabel(pending);
      if (control.textContent !== label) control.textContent = label;
      control.removeAttribute("hidden");
    } else {
      control.setAttribute("hidden", "");
    }
  }
}

// Announces the waiting count once a burst of arrivals settles. Nothing is said
// while the reader is at the tail, and the count is reset when they return to it.
function announceTail(view) {
  const pending = pendingSince(state.applied, pins[view]);
  if (pending === 0) {
    announcedRecords = 0;
    return;
  }
  if (pending <= announcedRecords) return;
  clearTimeout(announceTimer);
  announceTimer = setTimeout(() => {
    const now = pendingSince(state.applied, pins[visibleView()]);
    const text = tailAnnouncement(announcedRecords, now);
    if (!text) return;
    announcedRecords = now;
    refs.statusAnnouncer.textContent = text;
  }, TAIL_ANNOUNCE_DELAY_MS);
}

function jumpToLatest() {
  const view = visibleView();
  window.scrollTo(0, maxScrollTop(scrollMetrics()));
  pins[view] = state.applied;
  announcedRecords = 0;
  updateTail(view);
  // The control is about to disappear, so focus moves to the list it was for.
  const list = view === "trajectory" ? refs.ledger : refs.timeline;
  list.focus({ preventScroll: true });
}

// Rendering must never block data loading. A render error is surfaced without
// discarding the projection the client already holds.
function render() {
  const focusKey = captureFocus();
  const before = visibleView();
  const capture = captureTail(before);
  try {
    renderAll(refs, state);
  } catch (error) {
    const detail = error && error.message ? error.message : String(error);
    console.error("webchat: render failed", error);
    state.error = `render error: ${detail}`;
    try {
      renderAll(refs, state);
    } catch {
      // A second failure is reported through the first notice only.
    }
  }
  const view = visibleView();
  renderedRecords = state.items.length;
  restoreFocus(focusKey);
  if (!visited[view]) {
    visited[view] = true;
    pins[view] = state.applied;
  }
  // A view switch leaves the browser's own scroll position alone.
  if (view === before) settleScroll(view, capture);
  updateTail(view);
  announce(state);
  announceTail(view);
}

// The notices list is rebuilt on every render, so it is not a live region. A
// separate status region announces each gap exactly once, keyed by identity
// rather than by array length, so a recovered snapshot cannot silently drop a
// pending announcement or repeat one.
const announcedGaps = new Set();
let announcedConnection = "";

function gapKey(gap) {
  return gap.id || `${gap.reason || "unknown"}:${gap.requested_cursor || ""}`;
}

function windowKey(windowGap) {
  return `window:${windowGap.kind}:${windowGap.reason}:${windowGap.since}`;
}

function announce(state) {
  const node = refs.statusAnnouncer;
  if (!node) return;
  const fresh = state.gaps.filter((gap) => !announcedGaps.has(gapKey(gap)));
  const parts = [];
  if (fresh.length) {
    for (const gap of fresh) announcedGaps.add(gapKey(gap));
    const gap = fresh[fresh.length - 1];
    parts.push(`Continuity gap (${humanize(gap.reason || "unknown")}). Recovered from a snapshot; no success is inferred.`);
  }
  if (state.windowGap && !announcedGaps.has(windowKey(state.windowGap))) {
    announcedGaps.add(windowKey(state.windowGap));
    parts.push("Earlier history not loaded. No success is inferred for it.");
  }
  if (parts.length) {
    node.textContent = parts.join(" ");
    return;
  }
  if (state.connection !== announcedConnection) {
    announcedConnection = state.connection;
    node.textContent = state.connection === "open" ? "Stream connected." : `Stream ${state.connection}.`;
  }
}

// The timeline and toolbar are rebuilt on every render (content is
// append-only). Keep keyboard focus on the control the user was operating:
// a tool disclosure or a timeline filter button.
function captureFocus() {
  const active = document.activeElement;
  if (!active) return null;
  if (active.tagName === "SUMMARY") {
    const details = active.closest("details.tool");
    return details ? { kind: "tool", value: details.dataset.key } : null;
  }
  if (active.tagName === "BUTTON" && active.dataset && active.dataset.filter) {
    return { kind: "filter", value: active.dataset.filter };
  }
  if (active.tagName === "BUTTON" && active.dataset && active.dataset.group) {
    return { kind: "group", value: active.dataset.group };
  }
  if (active.tagName === "BUTTON" && active.dataset && active.dataset.id) {
    return { kind: "record", value: active.dataset.id };
  }
  return null;
}

// A toolbar is one tab stop. Focusing a button moves the stop onto it.
function focusToolbarButton(container, selector, matches) {
  const buttons = Array.from(container.querySelectorAll(selector));
  const index = buttons.findIndex(matches);
  if (index < 0) return;
  setRovingTabStop(buttons, index);
  buttons[index].focus();
}

// Left and Right move focus along a toolbar, wrapping at either end. Selecting a
// button stays a separate action (Enter or Space), so arrowing never changes state.
function moveToolbarFocus(event, container, selector) {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  const buttons = Array.from(container.querySelectorAll(selector));
  const index = buttons.indexOf(document.activeElement);
  if (index < 0) return;
  const next = event.key === "ArrowRight"
    ? (index + 1) % buttons.length
    : (index - 1 + buttons.length) % buttons.length;
  event.preventDefault();
  setRovingTabStop(buttons, next);
  buttons[next].focus();
}

function restoreFocus(target) {
  if (!target) return;
  // Skip when the DOM already holds focus, or when the user moved it elsewhere.
  if (document.activeElement && document.activeElement !== document.body) return;
  if (target.kind === "group") {
    focusToolbarButton(refs.trajectoryGroups, "button.group-tab", (button) => button.dataset.group === target.value);
    return;
  }
  if (target.kind === "record") {
    for (const row of refs.ledger.querySelectorAll("button.record-row")) {
      if (row.dataset.id === target.value) {
        row.focus();
        return;
      }
    }
    return;
  }
  if (target.kind === "filter") {
    focusToolbarButton(refs.timelineFilters, "button[data-filter]", (button) => button.dataset.filter === target.value);
    return;
  }
  for (const details of refs.timeline.querySelectorAll("details.tool")) {
    if (details.dataset.key === target.value) {
      const summary = details.querySelector("summary");
      if (summary) summary.focus();
      return;
    }
  }
}

// The stream lifecycle lives in stream.mjs. Every connection is snapshot-first,
// and retries follow the backoff policy. This module only wires it to the page.
const stream = createStream({
  state,
  fetchSnapshot,
  openEventStream,
  onChange: render,
});

window.addEventListener("online", () => stream.goOnline());
window.addEventListener("offline", () => stream.goOffline());

// Governance inspector drawer for narrow viewports. When the drawer is off
// canvas it is made inert so keyboard focus cannot reach hidden content.
const narrowViewport = window.matchMedia("(max-width: 1180px)");

function syncInspector() {
  const narrow = narrowViewport.matches;
  const open = refs.inspector.dataset.open === "true";
  const hidden = narrow && !open;
  // Never hide the panel out from under keyboard focus.
  if (hidden && refs.inspector.contains(document.activeElement)) {
    refs.inspectorToggle.focus();
  }
  if (hidden) {
    refs.inspector.setAttribute("inert", "");
    refs.inspector.setAttribute("aria-hidden", "true");
  } else {
    refs.inspector.removeAttribute("inert");
    refs.inspector.removeAttribute("aria-hidden");
  }
  // aria-expanded only means something while the panel is collapsible. Above the
  // breakpoint the panel is always visible, so the toggle is hidden rather than
  // left as a control with no effect.
  if (narrow) {
    refs.inspectorToggle.removeAttribute("hidden");
    refs.inspectorToggle.setAttribute("aria-expanded", String(open));
  } else {
    refs.inspectorToggle.setAttribute("hidden", "");
    refs.inspectorToggle.removeAttribute("aria-expanded");
  }
}

// Timeline filter: a coss-style toggle group where each button carries its own
// count and pressed state. Clicking, or pressing Enter/Space on a focused item,
// selects a filter; Left/Right arrows move between items (roving focus).
// Selecting a filter only narrows what is displayed; it never changes the
// projection, an observation, or a governance decision.
function selectFilter(value) {
  if (!value) return;
  setFilter(state, value);
  render();
}

refs.timelineFilters.addEventListener("click", (event) => {
  const target = event.target;
  const button = target && target.closest ? target.closest("button[data-filter]") : null;
  if (button) selectFilter(button.dataset.filter);
});

refs.timelineFilters.addEventListener("keydown", (event) => {
  moveToolbarFocus(event, refs.timelineFilters, "button[data-filter]");
});

// View ring: Chat and Trajectory are two projections of the same bounded window.
// Switching views is presentation only.
function selectView(value) {
  setView(state, value);
  render();
}

refs.viewTabs.addEventListener("click", (event) => {
  const target = event.target;
  const button = target && target.closest ? target.closest("button.view-tab") : null;
  if (button) selectView(button.dataset.view);
});

refs.viewTabs.addEventListener("keydown", (event) => {
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  const buttons = Array.from(refs.viewTabs.querySelectorAll("button.view-tab"));
  const index = buttons.indexOf(document.activeElement);
  if (index < 0) return;
  const next = event.key === "ArrowRight"
    ? (index + 1) % buttons.length
    : (index - 1 + buttons.length) % buttons.length;
  event.preventDefault();
  buttons[next].focus();
  selectView(buttons[next].dataset.view);
});

// Trajectory grouping is a presentation choice, exactly like the timeline filter.
refs.trajectoryGroups.addEventListener("click", (event) => {
  const target = event.target;
  const button = target && target.closest ? target.closest("button.group-tab") : null;
  if (!button) return;
  setGroup(state, button.dataset.group);
  render();
});

refs.trajectoryGroups.addEventListener("keydown", (event) => {
  moveToolbarFocus(event, refs.trajectoryGroups, "button.group-tab");
});

// Search narrows what the ledger shows. It reads only allowlisted bounded fields
// and can never change a projection, a verdict, or a filter outcome.
refs.trajectorySearch.addEventListener("input", () => {
  setQuery(state, refs.trajectorySearch.value);
  render();
});

// Selecting a record opens the record inspector for that record only.
refs.ledger.addEventListener("click", (event) => {
  const target = event.target;
  const row = target && target.closest ? target.closest("button.record-row") : null;
  if (!row) return;
  selectRecord(state, row.dataset.id);
  // Below the breakpoint the inspector is an off-canvas drawer that stays inert
  // until opened, so selecting a record must reveal it or the fields are
  // unreachable.
  if (narrowViewport.matches) refs.inspector.dataset.open = "true";
  render();
  syncInspector();
});

// Dismiss the record inspector and hand focus back to the row that opened it,
// so keyboard users do not lose their place in the ledger.
function dismissRecord() {
  const id = state.selectedId;
  clearRecord(state);
  if (narrowViewport.matches) refs.inspector.dataset.open = "false";
  render();
  syncInspector();
  if (!id) return;
  for (const row of refs.ledger.querySelectorAll("button.record-row")) {
    if (row.dataset.id === id) {
      row.focus();
      return;
    }
  }
}

refs.recordInspector.addEventListener("click", (event) => {
  const target = event.target;
  if (target && target.dataset && target.dataset.action === "clear-record") dismissRecord();
});

refs.inspectorToggle.addEventListener("click", () => {
  const open = refs.inspector.dataset.open === "true";
  refs.inspector.dataset.open = String(!open);
  syncInspector();
});

if (typeof narrowViewport.addEventListener === "function") {
  narrowViewport.addEventListener("change", syncInspector);
}

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  // A record selection is the innermost dismissable state.
  if (state.selectedId) {
    dismissRecord();
    return;
  }
  if (refs.inspector.dataset.open === "true") {
    refs.inspector.dataset.open = "false";
    refs.inspectorToggle.focus();
    syncInspector();
  }
});

// Reaching the tail by scrolling clears the waiting count, as the jump control does.
refs.chatJump.addEventListener("click", jumpToLatest);
refs.trajectoryJump.addEventListener("click", jumpToLatest);
window.addEventListener("scroll", () => updateTail(visibleView()), { passive: true });

syncInspector();
render();
stream.start();
