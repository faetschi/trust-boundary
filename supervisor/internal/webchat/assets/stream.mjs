// stream.mjs — the event-stream lifecycle as a DOM-free state machine.
//
// Every connection is snapshot-first: the stream is closed, a fresh snapshot is
// applied, and only then is the stream opened from that snapshot's cursor. A
// reconnect never resumes from a cursor the client has not just verified, and
// nothing here retries an effect. Retry timing belongs to this module (through
// backoff.mjs), not to the browser's EventSource, so it can be capped, jittered,
// suspended while offline, and reset only after a stable connection.

import { createBackoff, createScheduler } from "./backoff.mjs";
import { applyItem, applySession, applySnapshot, noteGap, setConnection, setError } from "./store.mjs";

// After this long without an open stream the status shows a warning. The warning
// never cancels the attempt.
export const CONNECT_WARN_MS = 3000;
// A connection must stay open this long before the retry counter resets, so a
// server that accepts and immediately drops does not keep resetting the backoff.
export const STABLE_OPEN_MS = 5000;

const SLOW_HANDSHAKE_NOTE = "slow handshake: still connecting";
const TERMINAL_LIMIT_NOTE = "the gateway is at its subscriber limit; this workspace will not retry automatically";

export function createStream({
  state,
  fetchSnapshot,
  openEventStream,
  onChange = () => {},
  isOnline = () => typeof navigator === "undefined" || navigator.onLine !== false,
  setTimer = (callback, ms) => setTimeout(callback, ms),
  clearTimer = (handle) => clearTimeout(handle),
  random = Math.random,
  connectWarnMs = CONNECT_WARN_MS,
  stableOpenMs = STABLE_OPEN_MS,
}) {
  const backoff = createBackoff({ random });
  const retryTimer = createScheduler({ setTimer, clearTimer });
  const warnTimer = createScheduler({ setTimer, clearTimer });
  const stableTimer = createScheduler({ setTimer, clearTimer });

  let source = null;
  // Each opened stream takes a token. A callback from a stream that has since
  // been closed or replaced is ignored, so a late event cannot touch current state.
  let token = 0;
  let attempting = false;
  let open = false;
  let terminal = false;
  // A gap whose recovery has not yet landed. It is noted once a snapshot is applied.
  let pendingGap = null;

  function changed() {
    onChange();
  }

  function closeSource() {
    token += 1;
    open = false;
    warnTimer.cancel();
    stableTimer.cancel();
    state.connectionWarning = "";
    if (source) {
      source.close();
      source = null;
    }
  }

  function suspend() {
    retryTimer.cancel();
    closeSource();
    setConnection(state, "offline");
    changed();
  }

  // scheduleRetry arms the next attempt under the backoff policy, or suspends
  // while the browser is offline so no timer runs against a dead network.
  function scheduleRetry() {
    if (terminal) return;
    if (!isOnline()) {
      suspend();
      return;
    }
    setConnection(state, "reconnecting");
    retryTimer.schedule(backoff.next(), () => connect());
  }

  function fail(error) {
    closeSource();
    setError(state, error && error.message ? error.message : String(error));
    scheduleRetry();
    changed();
  }

  // drop handles a stream that closed on its own. The error is not shown: the
  // status says reconnecting, and the next attempt either clears or replaces it.
  function drop() {
    closeSource();
    scheduleRetry();
    changed();
  }

  function openStream() {
    const current = ++token;
    const guard = (handler) => (event) => {
      if (current === token) handler(event);
    };
    source = openEventStream({
      cursor: state.cursor,
      onItem: guard((event) => {
        const item = event && event.item;
        const outcome = applyItem(state, item);
        if (outcome === "applied") {
          changed();
          return;
        }
        if (outcome === "gap") {
          connect({ reason: "sequence_gap", requested_cursor: state.cursor, latest: item && item.sequence });
        }
      }),
      onSession: guard((event) => {
        applySession(state, event && event.session, event && event.panels, event && event.streams);
        changed();
      }),
      onGap: guard((gap) => {
        // The subscriber limit is terminal: every slot is taken, so a retry would only churn.
        if (gap && gap.reason === "subscriber_limit") {
          terminal = true;
          retryTimer.cancel();
          closeSource();
          setError(state, TERMINAL_LIMIT_NOTE);
          setConnection(state, "error");
          changed();
          return;
        }
        connect({ ...(gap || {}), reason: (gap && gap.reason) || "replay_gap" });
      }),
      onConnection: guard((value) => {
        if (value === "open") {
          open = true;
          warnTimer.cancel();
          state.connectionWarning = "";
          setConnection(state, "open");
          // Only a connection that stays open resets the retry counter.
          stableTimer.schedule(stableOpenMs, () => backoff.reset());
          changed();
          return;
        }
        if (value === "closed") drop();
      }),
      onError: guard((error) => {
        setError(state, error && error.message ? error.message : String(error));
        changed();
      }),
    });
  }

  async function connect(gap) {
    if (terminal || attempting) return;
    if (gap) pendingGap = gap;
    retryTimer.cancel();
    if (!isOnline()) {
      suspend();
      return;
    }
    attempting = true;
    closeSource();
    setConnection(state, "connecting");
    // The warning describes the attempt only. It never cancels it.
    warnTimer.schedule(connectWarnMs, () => {
      state.connectionWarning = SLOW_HANDSHAKE_NOTE;
      changed();
    });
    changed();
    try {
      const snapshot = await fetchSnapshot();
      applySnapshot(state, snapshot);
      if (pendingGap) {
        noteGap(state, pendingGap);
        pendingGap = null;
      }
      if (!isOnline()) {
        suspend();
        return;
      }
      openStream();
    } catch (error) {
      fail(error);
    } finally {
      attempting = false;
    }
  }

  return {
    start() {
      return connect();
    },
    // Coming back online resumes at once: the retry delay was for a network that
    // is now known to be back. An open stream is left alone.
    goOnline() {
      if (terminal || open) return undefined;
      backoff.reset();
      return connect();
    },
    goOffline() {
      if (terminal) return;
      suspend();
    },
    get attempts() {
      return backoff.attempts;
    },
  };
}
