import { test } from "vitest";
import assert from "node:assert/strict";

import { createBackoff, createScheduler } from "./backoff";
import { createFakeTimers } from "./testing/fake-timers";

// random = 1 selects the top of the jitter band, so the sequence is the raw capped exponential.
test("the backoff sequence is 500, 1000, 2000, … capped at 10000", () => {
  const backoff = createBackoff({ random: () => 1 });
  const delays = Array.from({ length: 7 }, () => backoff.next());
  assert.deepEqual(delays, [500, 1000, 2000, 4000, 8000, 10000, 10000]);
});

test("jitter keeps every delay in the 50–100% band of the computed value", () => {
  // random = 0 selects the floor, so each delay is exactly half the computed value.
  const floor = createBackoff({ random: () => 0 });
  assert.deepEqual(
    Array.from({ length: 6 }, () => floor.next()),
    [250, 500, 1000, 2000, 4000, 5000],
  );
  // A mid-band draw lands at 75% of the computed value.
  const middle = createBackoff({ random: () => 0.5 });
  assert.equal(middle.next(), 375);
  assert.equal(middle.next(), 750);
});

test("every draw across the band stays within [50%, 100%] of the cap-limited value", () => {
  for (let step = 0; step <= 100; step += 1) {
    const draw = step / 100;
    const backoff = createBackoff({ random: () => draw });
    for (let attempt = 0; attempt < 9; attempt += 1) {
      const computed = Math.min(10000, 500 * 2 ** attempt);
      const delay = backoff.next();
      assert.ok(delay >= computed / 2 - 1e-9, `delay ${delay} below the 50% floor of ${computed}`);
      assert.ok(delay <= computed + 1e-9, `delay ${delay} above the computed value ${computed}`);
    }
  }
});

test("an out-of-range or non-finite random draw is clamped into the band", () => {
  const low = createBackoff({ random: () => -5 });
  assert.equal(low.next(), 250, "a negative draw clamps to the floor");
  const high = createBackoff({ random: () => 9 });
  assert.equal(high.next(), 500, "a draw above one clamps to the top of the band");
  const broken = createBackoff({ random: () => Number.NaN });
  assert.equal(broken.next(), 250, "a NaN draw is treated as the floor");
});

test("reset returns the sequence to the base delay", () => {
  const backoff = createBackoff({ random: () => 1 });
  backoff.next();
  backoff.next();
  backoff.next();
  assert.equal(backoff.attempts, 3);
  backoff.reset();
  assert.equal(backoff.attempts, 0);
  assert.equal(backoff.next(), 500);
});

test("the attempt counter does not grow the delay past the cap", () => {
  const backoff = createBackoff({ random: () => 1 });
  for (let attempt = 0; attempt < 200; attempt += 1) backoff.next();
  assert.equal(backoff.next(), 10000);
});

test("a scheduler fires its callback once after the delay, using the injected timers", () => {
  const clock = createFakeTimers();
  const scheduler = createScheduler({ setTimer: clock.setTimeout, clearTimer: clock.clearTimeout });
  const fired: string[] = [];
  scheduler.schedule(500, () => fired.push("retry"));
  assert.equal(scheduler.pending, true);
  clock.advance(499);
  assert.deepEqual(fired, []);
  clock.advance(1);
  assert.deepEqual(fired, ["retry"]);
  assert.equal(scheduler.pending, false);
  clock.advance(10000);
  assert.deepEqual(fired, ["retry"], "a fired timer does not fire again");
});

test("cancelling a scheduler prevents its callback and is idempotent", () => {
  const clock = createFakeTimers();
  const scheduler = createScheduler({ setTimer: clock.setTimeout, clearTimer: clock.clearTimeout });
  const fired: string[] = [];
  scheduler.schedule(500, () => fired.push("retry"));
  scheduler.cancel();
  scheduler.cancel();
  assert.equal(scheduler.pending, false);
  clock.advance(10000);
  assert.deepEqual(fired, []);
});

test("scheduling again replaces the pending timer rather than stacking a second one", () => {
  const clock = createFakeTimers();
  const scheduler = createScheduler({ setTimer: clock.setTimeout, clearTimer: clock.clearTimeout });
  const fired: string[] = [];
  scheduler.schedule(500, () => fired.push("first"));
  scheduler.schedule(2000, () => fired.push("second"));
  clock.advance(1999);
  assert.deepEqual(fired, []);
  clock.advance(1);
  assert.deepEqual(fired, ["second"]);
});
