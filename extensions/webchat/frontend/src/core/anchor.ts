// anchor.ts — the tail window as DOM-free arithmetic (port of anchor.mjs).
//
// The page scrolls as a whole. A reader at the tail keeps following new records; a
// reader who has scrolled away keeps their place, and the records that arrive
// meanwhile are counted, not hidden. The count is the number of records the store
// applied through applyItem since the reader's pin. Gaps, duplicates and snapshot
// loads never add to it, because none of them is a new record.

export const TAIL_SLACK_PX = 40;

export interface ScrollMetrics {
  readonly scrollTop: number;
  readonly clientHeight: number;
  readonly scrollHeight: number;
}

export function maxScrollTop(metrics: ScrollMetrics): number {
  return Math.max(0, metrics.scrollHeight - metrics.clientHeight);
}

export function isAtTail(metrics: ScrollMetrics, slack: number = TAIL_SLACK_PX): boolean {
  return metrics.scrollTop + metrics.clientHeight >= metrics.scrollHeight - slack;
}

export interface AnchorCapture {
  readonly capturedScrollTop: number;
  // The anchor's offset from the viewport top before the re-render. Absent when there
  // was no anchor record.
  readonly capturedOffset?: number | null;
  readonly currentScrollTop: number;
  // The anchor's offset from the viewport top after the re-render.
  readonly nextOffset?: number | null;
}

// The scroll position that keeps the anchor at the offset the reader last saw it. The
// anchor is the first record that was visible at the top of the viewport; its offset is
// measured from the viewport top before and after the re-render. If the anchor is gone,
// the captured position is used instead.
export function restoredScrollTop(capture: AnchorCapture, metrics: ScrollMetrics): number {
  const { capturedScrollTop, capturedOffset, currentScrollTop, nextOffset } = capture;
  const delta = offsetDelta(capturedOffset, nextOffset);
  const base = delta === null ? capturedScrollTop : currentScrollTop + delta;
  return Math.min(Math.max(base, 0), maxScrollTop(metrics));
}

// The change in the anchor's offset, or null when either offset is not a finite number.
function offsetDelta(captured: number | null | undefined, next: number | null | undefined): number | null {
  if (typeof captured !== "number" || typeof next !== "number") return null;
  if (!Number.isFinite(captured) || !Number.isFinite(next)) return null;
  return next - captured;
}

// The number of records applied since the reader's pin. Never negative.
export function pendingSince(applied: number, pinned: number): number {
  const gap = applied - pinned;
  return Number.isFinite(gap) && gap > 0 ? Math.floor(gap) : 0;
}

// The pin follows the applied count while the reader is at the tail, and holds while
// they are away, so the count of new records keeps growing.
export function repin(applied: number, atTail: boolean, pinned: number): number {
  return atTail ? applied : pinned;
}

function plural(count: number, word: string): string {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

export function jumpLabel(count: number): string {
  return `${plural(count, "new record")} · Jump to latest`;
}

// Announces the batch once: nothing is said until the count grows past what was last
// announced, and the caller resets the announced count to zero when the reader returns
// to the tail.
export function tailAnnouncement(announced: number, pending: number): string | null {
  if (!(pending > announced)) return null;
  return `${plural(pending, "new record")} below. Jump to latest is available.`;
}
