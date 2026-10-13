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
} from "./store.mjs";
import { renderAll } from "./render.mjs";

const refs = {
  sourceBadge: document.getElementById("source-badge"),
  connection: document.getElementById("connection"),
  claimG1: document.getElementById("claim-g1"),
  claimH1: document.getElementById("claim-h1"),
  claimContainment: document.getElementById("claim-containment"),
  claimBearing: document.getElementById("claim-bearing"),
  notices: document.getElementById("notices"),
  sessions: document.getElementById("sessions"),
  streams: document.getElementById("streams"),
  sessionTitle: document.getElementById("session-title"),
  sessionSub: document.getElementById("session-sub"),
  sessionStatus: document.getElementById("session-status"),
  timeline: document.getElementById("timeline"),
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

// Rendering must never block data loading. A render error is surfaced without
// discarding the projection the client already holds.
function render() {
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
      applySession(state, event && event.session, event && event.panels);
      render();
    },
    onGap: (gap) => {
      recover({ ...(gap || {}), reason: (gap && gap.reason) || "replay_gap" });
    },
    onConnection: (value) => {
      setConnection(state, value);
      render();
    },
    onError: (error) => {
      setError(state, error && error.message ? error.message : String(error));
      render();
    },
  });
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

// Governance inspector drawer for narrow viewports.
function syncInspector() {
  const open = refs.inspector.dataset.open === "true";
  refs.inspectorToggle.setAttribute("aria-expanded", String(open));
}

refs.inspectorToggle.addEventListener("click", () => {
  const open = refs.inspector.dataset.open === "true";
  refs.inspector.dataset.open = String(!open);
  syncInspector();
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && refs.inspector.dataset.open === "true") {
    refs.inspector.dataset.open = "false";
    refs.inspectorToggle.focus();
    syncInspector();
  }
});

syncInspector();
start();
