// fake-timers.ts — a deterministic clock for timer-driven tests (port of jstest/fake-timers.mjs).
// Callbacks run only when a test advances time, so no test sleeps on the wall clock. Timers due at
// the same instant fire in the order they were scheduled.

export interface FakeTimers {
  setTimeout(callback: () => void, ms: number): number;
  clearTimeout(id: number | null | undefined): void;
  advance(ms: number): void;
  readonly now: number;
  readonly pendingCount: number;
}

interface PendingTimer {
  readonly at: number;
  readonly callback: () => void;
}

export function createFakeTimers(): FakeTimers {
  let now = 0;
  let nextId = 1;
  const pending = new Map<number, PendingTimer>();

  function nextDue(limit: number): number | null {
    let dueId: number | null = null;
    let dueAt = Infinity;
    for (const [id, timer] of pending) {
      if (timer.at <= limit && timer.at < dueAt) {
        dueId = id;
        dueAt = timer.at;
      }
    }
    return dueId;
  }

  return {
    setTimeout(callback, ms) {
      const id = nextId;
      nextId += 1;
      pending.set(id, { at: now + Math.max(0, ms), callback });
      return id;
    },
    clearTimeout(id) {
      if (id !== null && id !== undefined) pending.delete(id);
    },
    advance(ms) {
      const target = now + ms;
      let due = nextDue(target);
      while (due !== null) {
        const timer = pending.get(due);
        pending.delete(due);
        if (timer) {
          now = timer.at;
          timer.callback();
        }
        due = nextDue(target);
      }
      now = target;
    },
    get now() {
      return now;
    },
    get pendingCount() {
      return pending.size;
    },
  };
}
