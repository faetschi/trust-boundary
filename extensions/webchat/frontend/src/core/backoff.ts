// backoff.ts — reconnect timing as a pure, injectable policy (port of backoff.mjs).
//
// No DOM and no wall clock: the caller supplies the timer functions, and the random
// source defaults to Math.random but can be injected, so every delay and every fire can
// be driven deterministically.

export const BACKOFF_DEFAULTS = Object.freeze({ baseMs: 500, factor: 2, capMs: 10000 });

// Jitter keeps each delay within [50%, 100%] of the computed value. Clients that lost
// the gateway together do not retry in lockstep, and no delay exceeds the cap.
const JITTER_FLOOR = 0.5;

export interface BackoffOptions {
  readonly baseMs?: number;
  readonly factor?: number;
  readonly capMs?: number;
  readonly random?: () => number;
}

export interface Backoff {
  // next returns the delay before the next retry and counts the attempt.
  next(): number;
  // reset is called only after a connection has stayed open, never on a flap.
  reset(): void;
  readonly attempts: number;
}

export function createBackoff({
  baseMs = BACKOFF_DEFAULTS.baseMs,
  factor = BACKOFF_DEFAULTS.factor,
  capMs = BACKOFF_DEFAULTS.capMs,
  random = Math.random,
}: BackoffOptions = {}): Backoff {
  let attempts = 0;
  return {
    next() {
      const computed = Math.min(capMs, baseMs * factor ** attempts);
      attempts += 1;
      return computed * (JITTER_FLOOR + (1 - JITTER_FLOOR) * unit(random()));
    },
    reset() {
      attempts = 0;
    },
    get attempts() {
      return attempts;
    },
  };
}

// An opaque handle from the injected timer. The core never inspects it.
export type TimerHandle = unknown;

// The timer functions the caller injects. The core reads no global timer.
export interface TimerHost {
  setTimer(callback: () => void, delayMs: number): TimerHandle;
  clearTimer(handle: TimerHandle): void;
}

export interface Scheduler {
  schedule(delayMs: number, callback: () => void): void;
  cancel(): void;
  readonly pending: boolean;
}

// createScheduler holds at most one pending timer. Scheduling replaces the pending
// timer; cancelling is idempotent.
export function createScheduler({ setTimer, clearTimer }: TimerHost): Scheduler {
  let handle: TimerHandle = null;
  let armed = false;

  function cancel(): void {
    if (!armed) return;
    armed = false;
    clearTimer(handle);
    handle = null;
  }

  function schedule(delayMs: number, callback: () => void): void {
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

// A non-finite or out-of-range draw is clamped, so a broken random source can only pick
// a delay inside [50%, 100%] of the computed value.
function unit(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(1, Math.max(0, value));
}
