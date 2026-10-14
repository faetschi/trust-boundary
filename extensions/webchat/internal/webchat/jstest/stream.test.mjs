import test from "node:test";
import assert from "node:assert/strict";

import { ATTEMPT_TIMEOUT_MS, CONNECT_WARN_MS, STABLE_OPEN_MS, createStream } from "../assets/stream.mjs";
import { createWorkspace } from "../assets/store.mjs";
import { createFakeTimers } from "./fake-timers.mjs";

// Lets pending promise continuations run. It advances no clock, so no timer
// fires and no wall-clock time passes.
async function flush() {
  for (let index = 0; index < 20; index += 1) await Promise.resolve();
}

function message(sequence) {
  return { id: `e${sequence}`, sequence, type: "message", turn_id: "t1", role: "assistant", text: `m${sequence}` };
}

function snapshotOf(sequences) {
  return {
    schema_version: "tbound-webchat-observation/v1",
    epoch: "e1",
    cursor: `e1:${sequences.length ? sequences[sequences.length - 1] : 0}`,
    source: "fixture",
    verification_scope: "unverified-input",
    session: { session_id: "s1", status: "pending", turn_count: 1 },
    panels: { publication: { state: "not-attempted" }, recovery: { state: "not-required" }, qualification: { g1: false } },
    streams: [],
    warnings: [],
    gaps: [],
    items: sequences.map(message),
  };
}

// The harness records every snapshot request and every stream open, in order, so a
// test can assert the snapshot-before-resume ordering directly.
function harness({ online = true, random = () => 1, connectWarnMs, stableOpenMs, attemptTimeoutMs } = {}) {
  const clock = createFakeTimers();
  const state = createWorkspace();
  const log = [];
  const fetches = [];
  // Every request ever made, in order, with its signal and the time it was made. Unlike
  // fetches, this list is never shifted, so a test can inspect superseded requests too.
  const requests = [];
  const streams = [];
  let onlineNow = online;
  let changes = 0;

  const fetchSnapshot = (signal) =>
    new Promise((resolve, reject) => {
      log.push("snapshot");
      const request = { resolve, reject, signal, at: clock.now };
      fetches.push(request);
      requests.push(request);
    });
  const openEventStream = ({ cursor, ...handlers }) => {
    const record = {
      cursor,
      handlers,
      closed: false,
      close() {
        record.closed = true;
      },
    };
    log.push(`open:${cursor}`);
    streams.push(record);
    return record;
  };

  const stream = createStream({
    state,
    fetchSnapshot,
    openEventStream,
    onChange: () => {
      changes += 1;
    },
    isOnline: () => onlineNow,
    setTimer: clock.setTimeout,
    clearTimer: clock.clearTimeout,
    random,
    connectWarnMs,
    stableOpenMs,
    attemptTimeoutMs,
  });

  return {
    state,
    clock,
    log,
    fetches,
    requests,
    streams,
    stream,
    get changes() {
      return changes;
    },
    setOnline(value) {
      onlineNow = value;
    },
    async answer(sequences) {
      fetches.shift().resolve(snapshotOf(sequences));
      await flush();
    },
    async refuse(message) {
      fetches.shift().reject(new Error(message));
      await flush();
    },
  };
}

test("a connection fetches a snapshot before any stream is opened, and opens from its cursor", async () => {
  const h = harness();
  h.stream.start();
  assert.deepEqual(h.log, ["snapshot"], "no stream may open before the snapshot has been applied");
  await h.answer([1, 2]);
  assert.deepEqual(h.log, ["snapshot", "open:e1:2"]);
  assert.equal(h.state.cursor, "e1:2");
});

test("the status never reads open until the stream reports it is open", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  assert.equal(h.state.connection, "connecting", "an opened but unconfirmed stream is not connected");
  h.streams[0].handlers.onConnection("open");
  assert.equal(h.state.connection, "open");
});

test("a dropped stream reconnects snapshot-first, from the fresh cursor and never the old one", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: message(3) });
  assert.equal(h.state.cursor, "e1:3");

  h.streams[0].handlers.onConnection("closed");
  assert.equal(h.streams[0].closed, true, "a dropped stream is closed so the browser cannot retry it on its own");
  assert.deepEqual(h.log, ["snapshot", "open:e1:2"], "a drop must wait for the policy, not reopen at once");

  h.clock.advance(500);
  await flush();
  assert.equal(h.log.at(-1), "snapshot", "the reconnect fetches a snapshot before any open");
  await h.answer([1, 2, 3, 4]);
  assert.deepEqual(h.log.slice(-2), ["snapshot", "open:e1:4"], "the reopened stream resumes from the fresh snapshot cursor");
});

test("a failed snapshot is surfaced and retried on the backoff schedule", async () => {
  const h = harness();
  h.stream.start();
  await h.refuse("snapshot request failed (503)");
  assert.equal(h.state.connection, "reconnecting");
  assert.equal(h.state.error, "snapshot request failed (503)");
  h.clock.advance(499);
  assert.equal(h.fetches.length, 0, "no retry before the first delay elapses");
  h.clock.advance(1);
  assert.equal(h.fetches.length, 1, "the retry fires at the base delay");
});

test("consecutive failures grow the delay: 500, 1000, 2000 ms", async () => {
  const h = harness();
  h.stream.start();
  await h.refuse("down");
  h.clock.advance(500);
  await h.refuse("down");
  h.clock.advance(999);
  assert.equal(h.fetches.length, 0);
  h.clock.advance(1);
  assert.equal(h.fetches.length, 1, "the second retry waits 1000 ms");
  await h.refuse("down");
  h.clock.advance(1999);
  assert.equal(h.fetches.length, 0);
  h.clock.advance(1);
  assert.equal(h.fetches.length, 1, "the third retry waits 2000 ms");
});

test("a connection that stays open resets the retry counter", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onConnection("closed");
  assert.equal(h.stream.attempts, 1, "the first drop consumes an attempt");

  h.clock.advance(500);
  await flush();
  await h.answer([1]);
  h.streams[1].handlers.onConnection("open");
  h.clock.advance(STABLE_OPEN_MS);
  assert.equal(h.stream.attempts, 0, "a connection that stayed open resets the counter");

  h.streams[1].handlers.onConnection("closed");
  h.clock.advance(499);
  assert.equal(h.fetches.length, 0);
  h.clock.advance(1);
  assert.equal(h.fetches.length, 1, "the next drop starts again at the base delay");
});

test("a connection that flaps before it is stable does not reset the retry counter", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onConnection("closed");
  h.clock.advance(500);
  await flush();
  await h.answer([1]);
  h.streams[1].handlers.onConnection("open");
  h.clock.advance(STABLE_OPEN_MS - 1);
  h.streams[1].handlers.onConnection("closed");
  assert.equal(h.stream.attempts, 2, "a drop before the stable window keeps the counter growing");
  h.clock.advance(999);
  assert.equal(h.fetches.length, 0);
  h.clock.advance(1);
  assert.equal(h.fetches.length, 1, "the flapping stream's next retry waits 1000 ms, not the base");
});

test("the slow-handshake warning appears after 3 s and never cancels the attempt", async () => {
  const h = harness();
  h.stream.start();
  h.clock.advance(CONNECT_WARN_MS - 1);
  assert.equal(h.state.connectionWarning, "", "no warning before the threshold");
  h.clock.advance(1);
  assert.match(h.state.connectionWarning, /slow/);
  assert.equal(h.state.connection, "connecting", "the warning does not change the connection state");
  assert.equal(h.fetches.length, 1, "the attempt is still in flight");

  await h.answer([1]);
  assert.deepEqual(h.log, ["snapshot", "open:e1:1"], "the slow attempt still completes and opens");
  assert.match(h.state.connectionWarning, /slow/, "the handshake is still unconfirmed, so the warning stays");
  h.streams[0].handlers.onConnection("open");
  assert.equal(h.state.connectionWarning, "", "the warning clears once the stream reports open");
  assert.equal(h.state.connection, "open");
});

test("the slow-handshake warning honours an injected threshold", async () => {
  const h = harness({ connectWarnMs: 100 });
  h.stream.start();
  h.clock.advance(100);
  assert.match(h.state.connectionWarning, /slow/);
});

test("going offline suspends retries and nothing fires while offline", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onConnection("closed");
  assert.equal(h.clock.pendingCount, 1, "a retry is pending while online");

  h.setOnline(false);
  h.stream.goOffline();
  assert.equal(h.state.connection, "offline");
  assert.equal(h.clock.pendingCount, 0, "the pending retry is suspended while offline");
  h.clock.advance(60000);
  assert.equal(h.fetches.length, 0, "no snapshot is requested while offline");
});

test("starting while offline makes no request and waits for the browser to come online", async () => {
  const h = harness({ online: false });
  h.stream.start();
  assert.equal(h.state.connection, "offline");
  assert.equal(h.fetches.length, 0, "a browser that is offline at start makes no request");
  assert.equal(h.clock.pendingCount, 0);
});

test("a drop while offline schedules no retry", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.setOnline(false);
  h.streams[0].handlers.onConnection("closed");
  assert.equal(h.state.connection, "offline", "a drop while the browser is offline reports offline, not reconnecting");
  assert.equal(h.clock.pendingCount, 0, "no retry timer is armed while offline");
  h.clock.advance(60000);
  assert.equal(h.fetches.length, 0);
});

test("coming back online resumes at once with a fresh snapshot", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.setOnline(false);
  h.stream.goOffline();
  h.setOnline(true);
  h.stream.goOnline();
  assert.equal(h.fetches.length, 1, "the resume fetches a snapshot immediately, with no retry delay");
  await h.answer([1, 2]);
  assert.deepEqual(h.log.slice(-2), ["snapshot", "open:e1:2"]);
});

test("going online while the stream is open does not restart it", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.stream.goOnline();
  assert.equal(h.fetches.length, 0);
  assert.equal(h.streams[0].closed, false);
});

test("the subscriber limit is terminal and is never retried, even when back online", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onGap({ reason: "subscriber_limit" });
  assert.equal(h.state.connection, "error");
  assert.equal(h.streams[0].closed, true);
  assert.equal(h.clock.pendingCount, 0);
  h.clock.advance(60000);
  h.stream.goOnline();
  assert.equal(h.fetches.length, 0, "every slot is taken, so retrying would only churn");
  assert.equal(h.state.connection, "error");
});

test("a sequence gap recovers from a fresh snapshot before the stream resumes", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: message(4) });
  assert.equal(h.state.lastSequence, 2, "the gapped element is not applied");
  assert.equal(h.log.at(-1), "snapshot", "recovery fetches the snapshot first");
  await h.answer([1, 2, 3, 4]);
  assert.deepEqual(h.log.slice(-2), ["snapshot", "open:e1:4"]);
  assert.equal(h.state.gaps.length, 1);
  assert.equal(h.state.gaps[0].reason, "sequence_gap");
});

test("a gap whose recovery fails is still reported once a later snapshot lands", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: message(4) });
  await h.refuse("snapshot request failed (500)");
  assert.equal(h.state.gaps.length, 0, "no gap is noted before a snapshot has been applied");
  h.clock.advance(500);
  await flush();
  await h.answer([1, 2, 3, 4]);
  assert.equal(h.state.gaps.length, 1, "the gap is reported exactly once after recovery");
  assert.equal(h.state.gaps[0].reason, "sequence_gap");
});

test("a replay gap from the server recovers from a snapshot and is noted", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  h.streams[0].handlers.onGap({ reason: "history_evicted", requested_cursor: "e1:0" });
  await h.answer([3, 4]);
  assert.equal(h.state.gaps.length, 1);
  assert.equal(h.state.gaps[0].reason, "history_evicted");
  assert.equal(h.log.at(-1), "open:e1:4");
});

test("a stream that has been replaced cannot change the current state", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  const old = h.streams[0];
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onConnection("closed");
  h.clock.advance(500);
  await flush();
  await h.answer([1, 2]);
  old.handlers.onItem({ item: message(3) });
  assert.equal(h.state.cursor, "e1:2", "a late event from a closed stream is ignored");
  assert.equal(h.state.lastSequence, 2);
});

test("a recovery that drops retained elements sets a windowGap; one that loses nothing does not", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2, 3]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: message(5) });
  await h.answer([3, 4, 5]);
  assert.deepEqual(h.state.windowGap, { kind: "server", reason: "history_evicted", since: 1 });

  const clean = harness();
  clean.stream.start();
  await clean.answer([1, 2]);
  clean.streams[0].handlers.onConnection("open");
  clean.streams[0].handlers.onItem({ item: message(4) });
  await clean.answer([1, 2, 3, 4]);
  assert.equal(clean.state.windowGap, null, "a recovery that keeps the whole window claims no missing history");
});

test("goOffline during an attempt is re-checked before the stream opens", async () => {
  const h = harness();
  h.stream.start();
  h.setOnline(false);
  h.stream.goOffline();
  await h.answer([1]);
  assert.equal(h.streams.length, 0, "an attempt that lands while offline must not open a stream");
  assert.equal(h.state.connection, "offline");
});

/* ---------------------------------------------------------------------------
 * A snapshot request that never settles
 * ------------------------------------------------------------------------- */

test("a snapshot request that never settles is abandoned at the attempt timeout and retried", async () => {
  const h = harness();
  h.stream.start();
  h.clock.advance(ATTEMPT_TIMEOUT_MS - 1);
  assert.equal(h.state.connection, "connecting", "the attempt is still in flight before its deadline");
  assert.equal(h.requests[0].signal.aborted, false);

  h.clock.advance(1);
  assert.equal(h.requests[0].signal.aborted, true, "the hung request is aborted at the deadline");
  assert.equal(h.state.connection, "reconnecting");
  assert.match(h.state.error, /timed out/);

  h.clock.advance(500);
  assert.equal(h.requests.length, 2, "the retry follows the backoff policy");
  assert.equal(h.requests[1].at, ATTEMPT_TIMEOUT_MS + 500);
});

test("the attempt timeout honours an injected value", async () => {
  const h = harness({ attemptTimeoutMs: 100 });
  h.stream.start();
  h.clock.advance(100);
  assert.equal(h.state.connection, "reconnecting");
  assert.match(h.state.error, /timed out/);
});

test("a snapshot that arrives after its attempt timed out cannot open a stream or apply", async () => {
  const h = harness();
  h.stream.start();
  h.clock.advance(ATTEMPT_TIMEOUT_MS);
  h.requests[0].resolve(snapshotOf([1, 2]));
  await flush();
  assert.equal(h.streams.length, 0, "a superseded attempt cannot open a stream");
  assert.equal(h.state.loaded, false, "nor apply its snapshot");
});

test("going offline during a hung attempt, then back online, resumes at once", async () => {
  const h = harness();
  h.stream.start();
  h.setOnline(false);
  h.stream.goOffline();
  assert.equal(h.requests[0].signal.aborted, true, "going offline aborts the request in flight");
  h.setOnline(true);
  h.stream.goOnline();
  assert.equal(h.requests.length, 2, "coming back online starts a new attempt at once");
  assert.equal(h.state.connection, "connecting");

  h.requests[1].resolve(snapshotOf([1]));
  await flush();
  assert.deepEqual(h.log.slice(-2), ["snapshot", "open:e1:1"]);
});

test("going online supersedes an attempt that is still in flight", async () => {
  const h = harness();
  h.stream.start();
  h.clock.advance(CONNECT_WARN_MS);
  assert.match(h.state.connectionWarning, /slow/);

  h.stream.goOnline();
  assert.equal(h.requests.length, 2, "the new attempt starts now, not after the hung one settles");
  assert.equal(h.requests[0].signal.aborted, true, "the superseded request is aborted");
  assert.equal(h.state.connectionWarning, "", "the warning describes the new attempt only");

  h.requests[0].resolve(snapshotOf([1, 2, 3]));
  await flush();
  assert.equal(h.streams.length, 0, "the superseded snapshot cannot open a stream");
  assert.equal(h.state.loaded, false, "nor be applied");

  h.requests[1].resolve(snapshotOf([1]));
  await flush();
  assert.deepEqual(h.log.slice(-1), ["open:e1:1"]);
  assert.equal(h.state.cursor, "e1:1", "only the current attempt's snapshot is applied");
});

test("going offline during an attempt holds even when the browser still reports online", async () => {
  const h = harness();
  h.stream.start();
  h.stream.goOffline();
  h.requests[0].resolve(snapshotOf([1]));
  await flush();
  assert.equal(h.streams.length, 0, "an attempt that lands after goOffline must not open a stream");
  assert.equal(h.state.connection, "offline");
});

test("going online clears the offline request, so the next attempt may open", async () => {
  const h = harness();
  h.stream.start();
  h.stream.goOffline();
  h.stream.goOnline();
  h.requests[1].resolve(snapshotOf([1]));
  await flush();
  assert.deepEqual(h.log.slice(-1), ["open:e1:1"]);
});

/* ---------------------------------------------------------------------------
 * Gap storms
 * ------------------------------------------------------------------------- */

// A server that answers every open with a gap. Each answered request is followed by
// an open and then a gap on that stream. Requests pending at an instant are answered
// before the clock moves on, and the clock advances in 10 ms steps.
async function gapStorm(h, untilMs) {
  while (h.requests.length < 50) {
    for (let guard = 0; guard < 20 && h.fetches.length; guard++) {
      await h.answer([1]);
      const current = h.streams.at(-1);
      current.handlers.onConnection("open");
      current.handlers.onItem({ item: message(3) });
    }
    if (h.clock.now >= untilMs) break;
    h.clock.advance(10);
  }
}

test("a server that answers every open with a gap is bounded and increasingly spaced", async () => {
  const h = harness({ random: () => 1 });
  h.stream.start();
  await gapStorm(h, 60000);

  const times = h.requests.map((request) => request.at);
  assert.ok(times.length <= 12, `${times.length} snapshot requests in 60 s`);
  const spacing = times.slice(1).map((at, index) => at - times[index]);
  assert.deepEqual(
    spacing.slice(0, 6),
    [0, 500, 1000, 2000, 4000, 8000],
    "the first gap recovers at once, then each repeated gap waits longer under the backoff policy",
  );
  assert.equal(spacing[6], 10000, "the delay reaches the policy cap");
});

test("every stream open in a gap storm follows a snapshot", async () => {
  const h = harness({ random: () => 1 });
  h.stream.start();
  await gapStorm(h, 20000);
  h.log.forEach((entry, index) => {
    if (entry.startsWith("open:")) assert.equal(h.log[index - 1], "snapshot", `open at log ${index} has no snapshot before it`);
  });
});

/* ---------------------------------------------------------------------------
 * Malformed items
 * ------------------------------------------------------------------------- */

test("an item without an id recovers from a snapshot and is disclosed, not dropped", async () => {
  const h = harness();
  h.stream.start();
  await h.answer([1, 2]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: { sequence: 3, type: "message", text: "no id" } });
  assert.equal(h.log.at(-1), "snapshot", "the malformed item triggers a snapshot recovery");
  await h.answer([1, 2, 3]);
  assert.deepEqual(h.log.slice(-2), ["snapshot", "open:e1:3"]);
  assert.equal(h.state.gaps.at(-1).reason, "invalid_item");
  assert.equal(h.state.gapNotice.reason, "invalid_item", "the notice names the reason");
});

test("repeated malformed items wait under the backoff policy, as repeated gaps do", async () => {
  const h = harness({ random: () => 1 });
  h.stream.start();
  await h.answer([1]);
  h.streams[0].handlers.onConnection("open");
  h.streams[0].handlers.onItem({ item: { sequence: 2, type: "message" } });
  await h.answer([1]);
  h.streams[1].handlers.onConnection("open");
  h.streams[1].handlers.onItem({ item: { sequence: 2, type: "message" } });
  assert.equal(h.fetches.length, 0, "the second malformed item waits rather than recovering at once");
  h.clock.advance(500);
  assert.equal(h.fetches.length, 1, "it recovers after the base delay");
});
