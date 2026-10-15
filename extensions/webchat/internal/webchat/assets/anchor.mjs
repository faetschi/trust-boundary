// anchor.mjs — the tail window as DOM-free arithmetic.
//
// The page scrolls as a whole. A reader at the tail keeps following new records; a
// reader who has scrolled away keeps their place, and the records that arrive
// meanwhile are counted, not hidden. The count is the number of records the store
// applied through applyItem since the reader's pin. Gaps, duplicates and snapshot
// loads never add to it, because none of them is a new record.

export const TAIL_SLACK_PX = 40;

// metrics: { scrollTop, clientHeight, scrollHeight }

export function maxScrollTop(metrics) {
  return Math.max(0, metrics.scrollHeight - metrics.clientHeight);
}

export function isAtTail(metrics, slack = TAIL_SLACK_PX) {
  return metrics.scrollTop + metrics.clientHeight >= metrics.scrollHeight - slack;
}

// The scroll position that keeps the anchor at the offset the reader last saw it.
// The anchor is the first record that was visible at the top of the viewport; its
// offset is measured from the viewport top before and after the re-render. If the
// anchor is gone, the captured position is used instead.
export function restoredScrollTop(
  { capturedScrollTop, capturedOffset, currentScrollTop, nextOffset },
  metrics,
) {
  const anchored = Number.isFinite(capturedOffset) && Number.isFinite(nextOffset);
  const base = anchored ? currentScrollTop + (nextOffset - capturedOffset) : capturedScrollTop;
  return Math.min(Math.max(base, 0), maxScrollTop(metrics));
}

// The number of records applied since the reader's pin. Never negative.
export function pendingSince(applied, pinned) {
  const gap = applied - pinned;
  return Number.isFinite(gap) && gap > 0 ? Math.floor(gap) : 0;
}

// The pin follows the applied count while the reader is at the tail, and holds
// while they are away, so the count of new records keeps growing.
export function repin(applied, atTail, pinned) {
  return atTail ? applied : pinned;
}

function plural(count, word) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

export function jumpLabel(count) {
  return `${plural(count, "new record")} · Jump to latest`;
}

// Announces the batch once: nothing is said until the count grows past what was
// last announced, and the caller resets the announced count to zero when the
// reader returns to the tail.
export function tailAnnouncement(announced, pending) {
  if (!(pending > announced)) return null;
  return `${plural(pending, "new record")} below. Jump to latest is available.`;
}
