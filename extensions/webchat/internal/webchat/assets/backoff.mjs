// backoff.mjs — reconnect timing as a pure, injectable policy.
//
// No DOM and no wall clock: the caller supplies the random source and the timer
// functions, so every delay and every fire can be driven deterministically.

export const BACKOFF_DEFAULTS = Object.freeze({ baseMs: 500, factor: 2, capMs: 10000 });

// Jitter keeps each delay within [50%, 100%] of the computed value. Clients that
// lost the gateway together do not retry in lockstep, and no delay exceeds the cap.
const JITTER_FLOOR = 0.5;

export function createBackoff({
  baseMs = BACKOFF_DEFAULTS.baseMs,
  factor = BACKOFF_DEFAULTS.factor,
  capMs = BACKOFF_DEFAULTS.capMs,
  random = Math.random,
} = {}) {
  let attempts = 0;
  return {
    // next returns the delay before the next retry and counts the attempt.
    next() {
      const computed = Math.min(capMs, baseMs * factor ** attempts);
      attempts += 1;
      return computed * (JITTER_FLOOR + (1 - JITTER_FLOOR) * unit(random()));
    },
    // reset is called only after a connection has stayed open, never on a flap.
    reset() {
      attempts = 0;
    },
    get attempts() {
      return attempts;
    },
  };
}

// createScheduler holds at most one pending timer. Scheduling replaces the
// pending timer; cancelling is idempotent.
export function createScheduler({ setTimer, clearTimer }) {
  let handle = null;
  let armed = false;

  function cancel() {
    if (!armed) return;
    armed = false;
    clearTimer(handle);
    handle = null;
  }

  function schedule(delayMs, callback) {
    cancel();
    armed = true;
    handle = setTimer(() => {
      armed = false;
      handle = null;
      callback();
    }, delayMs);
  }

  return {
    schedule,
    cancel,
    get pending() {
      return armed;
    },
  };
}

// A non-finite or out-of-range draw is clamped, so a broken random source can
// only pick a delay inside [50%, 100%] of the computed value.
function unit(value) {
  if (!Number.isFinite(value)) return 0;
  return Math.min(1, Math.max(0, value));
}
