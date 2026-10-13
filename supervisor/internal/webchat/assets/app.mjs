// app.js — wiring for the tbound Pi chat workspace.
//
// The client is an untrusted presentation layer: it fetches a bounded snapshot,
// follows a cursor-based event stream, and re-renders. It has no control route
// and never invents success; any continuity gap forces recovery from a fresh
// snapshot.

import { fetchSnapshot, openEventStream } from "./api.mjs";
import {
  applyItem,
  applySession,
  applySnapshot,
  createWorkspace,
  noteGap,
  setConnection,
  setError,
  setFilter,
} from "./store.mjs";
import { renderAll } from "./render.mjs";
import { humanize } from "./format.mjs";

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
};

const state = createWorkspace();
let stream = null;
let recovering = false;
let streamOpens = 0;

// Rendering must never block data loading. A render error is surfaced without
// discarding the projection the client already holds.
function render() {
  const focusKey = captureFocus();
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
  restoreFocus(focusKey);
  announce(state);
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

function announce(state) {
  const node = refs.statusAnnouncer;
  if (!node) return;
  const fresh = state.gaps.filter((gap) => !announcedGaps.has(gapKey(gap)));
  if (fresh.length) {
    for (const gap of fresh) announcedGaps.add(gapKey(gap));
    const gap = fresh[fresh.length - 1];
    node.textContent = `Continuity gap (${humanize(gap.reason || "unknown")}). Recovered from a snapshot; no success is inferred.`;
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
  return null;
}

function restoreFocus(target) {
  if (!target) return;
  // Skip when the DOM already holds focus, or when the user moved it elsewhere.
  if (document.activeElement && document.activeElement !== document.body) return;
  if (target.kind === "filter") {
    for (const button of refs.timelineFilters.querySelectorAll("button[data-filter]")) {
      if (button.dataset.filter === target.value) {
        button.focus();
        return;
      }
    }
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

function closeStream() {
  if (stream) {
    stream.close();
    stream = null;
  }
}

function subscribe() {
  closeStream();
  stream = openEventStream({
    cursor: state.cursor,
    onItem: (event) => {
      const item = event && event.item;
      const outcome = applyItem(state, item);
      if (outcome === "applied") {
        render();
        return;
      }
      if (outcome === "gap") {
        recover({ reason: "sequence_gap", requested_cursor: state.cursor, latest: item && item.sequence });
      }
    },
    onSession: (event) => {
      applySession(state, event && event.session, event && event.panels, event && event.streams);
      render();
    },
    onGap: (gap) => {
      // The subscriber limit is terminal, not a continuity gap to retry: with
      // every slot occupied an immediate resubscribe would loop.
      if (gap && gap.reason === "subscriber_limit") {
        setError(state, "the gateway is at its subscriber limit; this workspace will not retry automatically");
        setConnection(state, "error");
        render();
        return;
      }
      recover({ ...(gap || {}), reason: (gap && gap.reason) || "replay_gap" });
    },
    onConnection: (value) => {
      setConnection(state, value);
      render();
      if (value === "open") {
        streamOpens += 1;
        // Recover from a fresh snapshot on a *re*open, so a session/panel/stream
        // update that happened while disconnected cannot be missed. recover()
        // closes the stream first, so its snapshot cannot race live events.
        if (streamOpens > 1) recover({ reason: "reconnect" });
      }
    },
    onError: (error) => {
      setError(state, error && error.message ? error.message : String(error));
      render();
    },
  });
}

// refreshSnapshot re-fetches the bounded snapshot without resubscribing. It is
// used by recover() (which closes the stream first), so a snapshot response can
// never race live events.
async function refreshSnapshot() {
  const snapshot = await fetchSnapshot();
  applySnapshot(state, snapshot);
  render();
}

async function recover(gap) {
  if (recovering) return;
  recovering = true;
  try {
    closeStream();
    const snapshot = await fetchSnapshot();
    applySnapshot(state, snapshot);
    noteGap(state, gap);
    setConnection(state, "reconnecting");
    render();
    subscribe();
  } catch (error) {
    setError(state, error && error.message ? error.message : String(error));
    setConnection(state, "error");
    render();
  } finally {
    recovering = false;
  }
}

async function start() {
  render();
  try {
    closeStream();
    const snapshot = await fetchSnapshot();
    applySnapshot(state, snapshot);
    setConnection(state, "connecting");
    render();
    subscribe();
  } catch (error) {
    setError(state, error && error.message ? error.message : String(error));
    setConnection(state, "error");
    render();
  }
}

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
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
  const buttons = Array.from(refs.timelineFilters.querySelectorAll("button[data-filter]"));
  const index = buttons.indexOf(document.activeElement);
  if (index < 0) return;
  const next = event.key === "ArrowRight"
    ? (index + 1) % buttons.length
    : (index - 1 + buttons.length) % buttons.length;
  event.preventDefault();
  buttons[next].focus();
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
  if (event.key === "Escape" && refs.inspector.dataset.open === "true") {
    refs.inspector.dataset.open = "false";
    refs.inspectorToggle.focus();
    syncInspector();
  }
});

syncInspector();
start();
