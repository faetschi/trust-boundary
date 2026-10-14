// stop-sequence.mjs — pure, DOM-free two-press Escape cancel state machine.
//
// NOT WIRED. The web chat is read-only: there is no cancel API, composer or
// submit route, so nothing here may be bound to a control or shown as working.
// Cancellation is milestone 3 and needs a supervisor service API owned by the
// core thread. No module imports this file, the shell and app do not reference
// it, and handler.go does not allowlist it.
//
// The pattern comes from the harness client (extensions/webchat/docs/
// web-chat-ui-plan.md, Appendix B); no code is copied. Cancel is a request. The
// wording below never claims that a turn was cancelled, stopped or settled: the
// settled result comes
// from the supervisor, and an uncertain effect stays UNKNOWN.
//
// The module creates no timers and reads no clock. The caller stamps every event
// with `at` from its own clock. step() returns data only: the next state, a
// view-state, an aria-live status string and, at most once per confirmation, a
// cancel-request intent for the caller to send. Nothing is re-emitted on a
// reconnect or resync, and nothing is retried.

export const DEFAULT_WINDOW_MS = 2000;
export const CONFIRMED_TEXT = "Cancel requested; waiting for the supervisor to settle the turn";
export const CLEARED_TEXT = "Cancel request cleared; no request was sent";

/**
 * @param {string} turnId
 * @returns {string}
 */
export function armedText(turnId) {
  return `Press Escape again to request cancel of turn ${turnId}`;
}

/**
 * @typedef {{ sessionId: string, turnId: string, generation?: string | number }} CancelTarget
 * @typedef {{ type: "cancel-request", sessionId: string, turnId: string, generation?: string | number }} CancelRequest
 * @typedef {{ phase: "idle", armed: null }
 *   | { phase: "armed", armed: { target: CancelTarget, armedAt: number } }} SequenceState
 *
 * `key` events may arm or confirm. `context` events only invalidate or expire.
 * Both carry the target the UI currently shows (null when no turn is active),
 * whether focus is inside the declared region, and the caller's clock reading.
 * @typedef {{ type: "key", at: number, key: string, target: CancelTarget | null, inRegion: boolean,
 *   isComposing?: boolean, keyCode?: number, repeat?: boolean,
 *   ctrlKey?: boolean, altKey?: boolean, metaKey?: boolean, shiftKey?: boolean }} KeyEvent
 * @typedef {{ type: "context", at: number, target: CancelTarget | null, inRegion: boolean }} ContextEvent
 * @typedef {{ state: SequenceState, view: "idle" | "armed" | "confirmed", status: string,
 *   intent: CancelRequest | null, reason: string | null }} Outcome
 */

const IDLE = Object.freeze({ phase: "idle", armed: null });

/** @returns {SequenceState} */
export function initialState() {
  return IDLE;
}

/**
 * Advances the sequence by one event. Pure: the input state is never mutated.
 * `isModalActive` is an injected predicate; a modal that declares itself active
 * blocks arming and confirming and clears an armed request.
 *
 * `view` is "confirmed" only for the step that emits an intent; the returned
 * `state` is already idle, so the next step reports "idle".
 *
 * @param {SequenceState} state
 * @param {KeyEvent | ContextEvent} event
 * @param {{ windowMs?: number, isModalActive?: () => boolean }} [options]
 * @returns {Outcome}
 */
export function step(state, event, options = {}) {
  const windowMs = options.windowMs ?? DEFAULT_WINDOW_MS;
  const isModalActive = options.isModalActive ?? (() => false);
  const modal = Boolean(isModalActive());
  const target = normalizeTarget(event.target);
  const inRegion = event.inRegion === true;

  // Context checks run on every event, so a change clears an armed request
  // without waiting for the next key press.
  const cleared = invalidation(state, { at: event.at, target, inRegion, modal, windowMs });
  let next = cleared ? IDLE : state;
  let reason = cleared;
  let status = cleared ? CLEARED_TEXT : "";
  let intent = null;

  if (event.type === "key") {
    const ignored = ignoredKey(event, { inRegion, modal });
    if (ignored) {
      reason = reason ?? ignored;
    } else if (!isEscape(event.key)) {
      if (next.phase === "armed") {
        next = IDLE;
        status = CLEARED_TEXT;
        reason = "other-key";
      } else {
        reason = reason ?? "not-escape";
      }
    } else if (next.phase === "armed") {
      intent = { type: "cancel-request", ...next.armed.target };
      next = IDLE;
      status = CONFIRMED_TEXT;
      reason = "confirmed";
    } else if (target) {
      next = { phase: "armed", armed: { target, armedAt: event.at } };
      status = armedText(target.turnId);
      reason = "armed";
    } else {
      reason = reason ?? "no-target";
    }
  }

  return { state: next, view: intent ? "confirmed" : next.phase, status, intent, reason };
}

// Returns why an armed request must be cleared, or null if it still stands.
function invalidation(state, { at, target, inRegion, modal, windowMs }) {
  if (state.phase !== "armed") return null;
  const elapsed = at - state.armed.armedAt;
  // Fail closed: a missing or backwards clock reading counts as expired.
  if (!(elapsed >= 0 && elapsed < windowMs)) return "expired";
  if (modal) return "modal-active";
  if (!inRegion) return "focus-left";
  if (!sameTarget(state.armed.target, target)) return "target-changed";
  return null;
}

// Keys the sequence does not count as presses. They leave the state untouched.
function ignoredKey(event, { inRegion, modal }) {
  if (event.isComposing === true || event.keyCode === 229) return "ime-composition";
  if (event.repeat === true) return "auto-repeat";
  if (event.ctrlKey === true || event.altKey === true || event.metaKey === true || event.shiftKey === true) {
    return "modified-key";
  }
  if (modal) return "modal-active";
  if (!inRegion) return "outside-region";
  return null;
}

function isEscape(key) {
  return key === "Escape" || key === "Esc";
}

// Returns a canonical copy of a valid target, or null. generation is optional
// and is omitted from the copy when absent.
function normalizeTarget(target) {
  if (!target || !isText(target.sessionId) || !isText(target.turnId)) return null;
  const generation = target.generation ?? undefined;
  if (generation === undefined) return { sessionId: target.sessionId, turnId: target.turnId };
  if (!isText(generation) && !Number.isFinite(generation)) return null;
  return { sessionId: target.sessionId, turnId: target.turnId, generation };
}

function sameTarget(armed, current) {
  return (
    current !== null &&
    armed.sessionId === current.sessionId &&
    armed.turnId === current.turnId &&
    armed.generation === current.generation
  );
}

function isText(value) {
  return typeof value === "string" && value.length > 0;
}
