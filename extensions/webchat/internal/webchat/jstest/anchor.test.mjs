import test from "node:test";
import assert from "node:assert/strict";

const {
  TAIL_SLACK_PX,
  isAtTail,
  jumpLabel,
  maxScrollTop,
  pendingSince,
  repin,
  restoredScrollTop,
  tailAnnouncement,
} = await import("../assets/anchor.mjs");

test("the tail is within 40px of the bottom, and a document shorter than the viewport is always at the tail", () => {
  assert.equal(TAIL_SLACK_PX, 40);
  assert.equal(isAtTail({ scrollTop: 600, clientHeight: 400, scrollHeight: 1000 }), true, "exactly at the bottom");
  assert.equal(isAtTail({ scrollTop: 560, clientHeight: 400, scrollHeight: 1000 }), true, "40px above the bottom");
  assert.equal(isAtTail({ scrollTop: 559, clientHeight: 400, scrollHeight: 1000 }), false, "41px above the bottom");
  assert.equal(isAtTail({ scrollTop: 0, clientHeight: 800, scrollHeight: 300 }), true);
  assert.equal(maxScrollTop({ scrollTop: 0, clientHeight: 800, scrollHeight: 300 }), 0, "never a negative scroll");
});

test("only records applied since the reader's pin are new, and the pin follows the tail", () => {
  assert.equal(pendingSince(10, 10), 0);
  assert.equal(pendingSince(13, 10), 3);
  assert.equal(pendingSince(10, 13), 0, "a pin ahead of the count never yields a negative count");
  assert.equal(pendingSince(Number.NaN, 0), 0, "a non-numeric count is treated as nothing new");
  assert.equal(repin(13, true, 10), 13, "at the tail the pin moves to the applied count");
  assert.equal(repin(13, false, 10), 10, "away from the tail the pin holds, so the count keeps growing");
});

test("records appended below a scrolled reader leave the viewport where it was", () => {
  const metrics = { scrollTop: 900, clientHeight: 800, scrollHeight: 3000 };
  // The anchor sat 120px below the viewport top and is still there: nothing moves.
  assert.equal(
    restoredScrollTop({ capturedScrollTop: 900, capturedOffset: 120, currentScrollTop: 900, nextOffset: 120 }, metrics),
    900,
  );
  // Older records trimmed above the anchor move it up by 300px; the reader follows it.
  assert.equal(
    restoredScrollTop({ capturedScrollTop: 900, capturedOffset: 120, currentScrollTop: 900, nextOffset: -180 }, metrics),
    600,
  );
  // The anchor is gone: fall back to the captured position, clamped to the document.
  assert.equal(
    restoredScrollTop({ capturedScrollTop: 900, capturedOffset: 120, currentScrollTop: 0, nextOffset: null }, metrics),
    900,
  );
  assert.equal(
    restoredScrollTop({ capturedScrollTop: 900, capturedOffset: 120, currentScrollTop: 0, nextOffset: 5000 }, metrics),
    2200,
    "a restored position never passes the bottom of the document",
  );
});

test("the jump label and the once-per-batch announcement state the applied count", () => {
  assert.equal(jumpLabel(1), "1 new record · Jump to latest");
  assert.equal(jumpLabel(3), "3 new records · Jump to latest");
  assert.equal(tailAnnouncement(0, 0), null, "nothing new, nothing to announce");
  assert.equal(tailAnnouncement(0, 1), "1 new record below. Jump to latest is available.");
  assert.equal(tailAnnouncement(0, 2), "2 new records below. Jump to latest is available.");
  assert.equal(tailAnnouncement(2, 2), null, "a batch already announced is not announced again");
  assert.equal(tailAnnouncement(2, 5), "5 new records below. Jump to latest is available.");
});
