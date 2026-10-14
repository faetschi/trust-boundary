// stream.ts — the event-stream lifecycle as a DOM-free state machine (port of stream.mjs).
//
// Every connection is snapshot-first: the stream is closed, a fresh snapshot is applied, and only
// then is the stream opened from that snapshot's cursor. A reconnect never resumes from a cursor
// the client has not just verified, and nothing here retries an effect. Retry timing belongs to
// this module (through backoff.ts), not to the browser's EventSource, so it can be capped,
// jittered, suspended while offline, and reset only after a stable connection.
//
// Every browser facility is injected: the snapshot fetch, the event stream, the online flag, the
// timers and the AbortController. The legacy module defaulted these to globals. The port does not,
// so the core reads no global. The app wiring supplies the browser versions.

import type { ItemEvent, ReplayGap, SessionEvent, Snapshot } from "./contract";
import { createBackoff, createScheduler, type TimerHost } from "./backoff";
import type { AbortSignalLike, EventStreamHandle } from "./api";
import { applyItem, applySession, applySnapshot, noteGap, setConnection, setError, type Workspace } from "./store";

// After this long without an open stream the status shows a warning. The warning never cancels the
// attempt.
export const CONNECT_WARN_MS = 3000;
// A connection must stay open this long before the retry counter resets, so a server that accepts
// and immediately drops does not keep resetting the backoff.
export const STABLE_OPEN_MS = 5000;
// A snapshot request that has not settled by now is abandoned and retried. A browser request has no
// deadline of its own, so a hung response would hold the attempt forever.
export const ATTEMPT_TIMEOUT_MS = 15000;

const SLOW_HANDSHAKE_NOTE = "slow handshake: still connecting";
const TERMINAL_LIMIT_NOTE = "the gateway is at its subscriber limit; this workspace will not retry automatically";

export interface AbortControllerLike {
  readonly signal: AbortSignalLike;
  abort(): void;
}

// The handlers the stream gives its event source. Each carries a validated contract value.
export interface StreamEventHandlers {
  readonly cursor: string;
  onItem(event: ItemEvent): void;
  onSession(event: SessionEvent): void;
  onGap(gap: ReplayGap): void;
  onConnection(value: "open" | "closed"): void;
  onError(error: unknown): void;
}

export interface StreamDeps extends TimerHost {
  readonly state: Workspace;
  // Resolves to a snapshot the caller has validated (the app wiring uses fetchValidatedSnapshot).
  readonly fetchSnapshot: (signal: AbortSignalLike) => Promise<Snapshot>;
  readonly openEventStream: (handlers: StreamEventHandlers) => EventStreamHandle;
  readonly isOnline: () => boolean;
  readonly createAbortController: () => AbortControllerLike;
  readonly onChange?: () => void;
  readonly random?: () => number;
  readonly connectWarnMs?: number;
  readonly stableOpenMs?: number;
  readonly attemptTimeoutMs?: number;
}

export interface StreamController {
  start(): Promise<void>;
  goOnline(): Promise<void> | undefined;
  goOffline(): void;
  readonly attempts: number;
}

interface Attempt {
  readonly controller: AbortControllerLike;
  claimed: boolean;
}

function messageOf(error: unknown): string {
  if (error && typeof error === "object" && "message" in error && error.message) return String(error.message);
  return String(error);
}

export function createStream({
  state,
  fetchSnapshot,
  openEventStream,
  onChange = () => {},
  isOnline,
  setTimer,
  clearTimer,
  createAbortController,
  random = Math.random,
  connectWarnMs = CONNECT_WARN_MS,
  stableOpenMs = STABLE_OPEN_MS,
  attemptTimeoutMs = ATTEMPT_TIMEOUT_MS,
}: StreamDeps): StreamController {
  const backoff = createBackoff({ random });
  const timers = { setTimer, clearTimer };
  const retryTimer = createScheduler(timers);
  const warnTimer = createScheduler(timers);
  const stableTimer = createScheduler(timers);
  const attemptTimer = createScheduler(timers);

  let source: EventStreamHandle | null = null;
  // Each opened stream takes a token. A callback from a stream that has since been closed or
  // replaced is ignored, so a late event cannot touch current state.
  let token = 0;
  // The snapshot request in flight, if any. Only the attempt still in flight may settle, so a
  // request that was superseded, timed out or abandoned is ignored.
  let inflight: Attempt | null = null;
  let open = false;
  let terminal = false;
  // Set by goOffline and cleared by goOnline. It is checked after every await, so an attempt that
  // lands after the app went offline cannot open a stream, even while the browser still reports
  // online.
  let offlineRequested = false;
  // The first gap after a stable open recovers at once. The allowance is spent until the connection
  // has stayed open, so a server that answers every open with a gap cannot drive a snapshot request
  // per event. Later gaps wait under the backoff policy.
  let immediateGapAvailable = true;
  // A gap whose recovery has not yet landed. It is noted once a snapshot is applied.
  let pendingGap: ReplayGap | null = null;

  function changed(): void {
    onChange();
  }

  function closeSource(): void {
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

  // abandon ends the attempt in flight, if there is one: its timeout is disarmed and its request
  // aborted. Whatever that request later does is then ignored.
  function abandon(): void {
    attemptTimer.cancel();
    if (!inflight) return;
    const { controller } = inflight;
    inflight = null;
    controller.abort();
  }

  // claim takes the outcome of an attempt, but only while that attempt is the one in flight. A
  // superseded, timed-out or abandoned request has no outcome to take.
  function claim(attempt: Attempt): boolean {
    if (inflight !== attempt) return false;
    inflight = null;
    attemptTimer.cancel();
    attempt.claimed = true;
    return true;
  }

  function suspend(): void {
    retryTimer.cancel();
    abandon();
    closeSource();
    setConnection(state, "offline");
    changed();
  }

  // scheduleRetry arms the next attempt under the backoff policy, or suspends while the browser is
  // offline so no timer runs against a dead network.
  function scheduleRetry(): void {
    if (terminal) return;
    if (!isOnline()) {
      suspend();
      return;
    }
    setConnection(state, "reconnecting");
    retryTimer.schedule(backoff.next(), () => {
      void connect();
    });
  }

  function fail(error: unknown): void {
    closeSource();
    setError(state, messageOf(error));
    scheduleRetry();
    changed();
  }

  // drop handles a stream that closed on its own. The error is not shown: the status says
  // reconnecting, and the next attempt either clears or replaces it.
  function drop(): void {
    closeSource();
    scheduleRetry();
    changed();
  }

  // recover handles a gap. The gapped stream is closed at once, so no later event from it is
  // applied, and the client then recovers from a fresh snapshot.
  function recover(gap: ReplayGap): void {
    pendingGap = gap;
    closeSource();
    if (immediateGapAvailable) {
      immediateGapAvailable = false;
      void connect();
    } else {
      scheduleRetry();
    }
    changed();
  }

  function openStream(): void {
    const current = ++token;
    const guard = <A>(handler: (value: A) => void) => (value: A) => {
      if (current === token) handler(value);
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
          recover({ reason: "sequence_gap", requested_cursor: state.cursor, latest: item.sequence });
        } else if (outcome === "invalid") {
          // An item with no usable id or sequence cannot be placed, so it is recovered like a gap.
          recover({ reason: "invalid_item", requested_cursor: state.cursor });
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
          abandon();
          closeSource();
          setError(state, TERMINAL_LIMIT_NOTE);
          setConnection(state, "error");
          changed();
          return;
        }
        recover({ ...(gap || {}), reason: (gap && gap.reason) || "replay_gap" });
      }),
      onConnection: guard((value) => {
        if (value === "open") {
          open = true;
          warnTimer.cancel();
          state.connectionWarning = "";
          setConnection(state, "open");
          // Only a connection that stays open resets the retry counter, and with it the immediate
          // gap recovery.
          stableTimer.schedule(stableOpenMs, () => {
            backoff.reset();
            immediateGapAvailable = true;
          });
          changed();
          return;
        }
        if (value === "closed") drop();
      }),
      onError: guard((error) => {
        setError(state, messageOf(error));
        changed();
      }),
    });
  }

  async function connect(): Promise<void> {
    if (terminal) return;
    retryTimer.cancel();
    // A new attempt supersedes one still in flight, so a hung request cannot hold the state.
    abandon();
    if (offlineRequested || !isOnline()) {
      suspend();
      return;
    }
    const attempt: Attempt = { controller: createAbortController(), claimed: false };
    inflight = attempt;
    closeSource();
    setConnection(state, "connecting");
    // The warning describes the attempt only. It never cancels it.
    warnTimer.schedule(connectWarnMs, () => {
      state.connectionWarning = SLOW_HANDSHAKE_NOTE;
      changed();
    });
    attemptTimer.schedule(attemptTimeoutMs, () => {
      abandon();
      fail(new Error("snapshot request timed out"));
    });
    changed();
    try {
      const snapshot = await fetchSnapshot(attempt.controller.signal);
      if (!claim(attempt)) return;
      applySnapshot(state, snapshot);
      if (pendingGap) {
        noteGap(state, pendingGap);
        pendingGap = null;
      }
      if (offlineRequested || !isOnline()) {
        suspend();
        return;
      }
      openStream();
    } catch (error) {
      // An error after the attempt was claimed is still this attempt's failure.
      if (attempt.claimed || claim(attempt)) fail(error);
    }
  }

  return {
    start() {
      return connect();
    },
    // Coming back online resumes at once: the retry delay was for a network that is now known to be
    // back, and an attempt still in flight is superseded. An open stream is left alone.
    goOnline() {
      offlineRequested = false;
      if (terminal || open) return undefined;
      backoff.reset();
      return connect();
    },
    goOffline() {
      if (terminal) return;
      offlineRequested = true;
      suspend();
    },
    get attempts() {
      return backoff.attempts;
    },
  };
}
