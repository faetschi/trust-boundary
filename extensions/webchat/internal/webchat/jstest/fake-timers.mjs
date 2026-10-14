// fake-timers.mjs — a deterministic clock for timer-driven tests. Callbacks run
// only when a test advances time, so no test sleeps on the wall clock. Timers due
// at the same instant fire in the order they were scheduled.

export function createFakeTimers() {
  let now = 0;
  let nextId = 1;
  const pending = new Map();

  function nextDue(limit) {
    let dueId = null;
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
      pending.delete(id);
    },
    advance(ms) {
      const target = now + ms;
      let due = nextDue(target);
      while (due !== null) {
        const timer = pending.get(due);
        pending.delete(due);
        now = timer.at;
        timer.callback();
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
