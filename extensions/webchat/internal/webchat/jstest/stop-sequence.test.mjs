import test from "node:test";
import assert from "node:assert/strict";

import { CLEARED_TEXT, CONFIRMED_TEXT, armedText, initialState, step } from "../assets/stop-sequence.mjs";

const TARGET = { sessionId: "s1", turnId: "t1", generation: 3 };
const REQUEST = { type: "cancel-request", sessionId: "s1", turnId: "t1", generation: 3 };

function key(at, overrides = {}) {
  return { type: "key", at, key: "Escape", target: TARGET, inRegion: true, ...overrides };
}

function context(at, overrides = {}) {
  return { type: "context", at, target: TARGET, inRegion: true, ...overrides };
}

// Feeds events through step() in order, carrying the state forward.
function run(events, options = {}) {
  let state = initialState();
  return events.map((event) => {
    const outcome = step(state, event, options);
    state = outcome.state;
    return outcome;
  });
}

function fakeClock(start = 0) {
  let now = start;
  return {
    now: () => now,
    advance: (ms) => {
      now += ms;
    },
  };
}

test("a first Escape arms and a second Escape inside the window confirms once", () => {
  const [armed, confirmed] = run([key(0), key(1500)]);
  assert.equal(armed.view, "armed");
  assert.equal(armed.status, "Press Escape again to request cancel of turn t1");
  assert.equal(armed.intent, null);
  assert.equal(confirmed.view, "confirmed");
  assert.equal(confirmed.status, CONFIRMED_TEXT);
  assert.deepEqual(confirmed.intent, REQUEST);
  assert.equal(confirmed.state.phase, "idle");
});

test("a target without a generation produces an intent without a generation field", () => {
  const bare = { sessionId: "s1", turnId: "t1" };
  const [, confirmed] = run([key(0, { target: bare }), key(10, { target: bare })]);
  assert.deepEqual(confirmed.intent, { type: "cancel-request", sessionId: "s1", turnId: "t1" });
  assert.equal("generation" in confirmed.intent, false);
});

test("the default window is 2000 ms: 1999 ms confirms and 2000 ms expires", () => {
  const [, inside] = run([key(0), key(1999)]);
  assert.equal(inside.view, "confirmed");

  const [, outside] = run([key(0), key(2000)]);
  assert.equal(outside.intent, null);
  assert.equal(outside.view, "armed");
  assert.equal(outside.status, armedText("t1"), "expiry re-arms as a fresh first press");
});

test("windowMs is injectable", () => {
  const [, late] = run([key(0), key(600)], { windowMs: 500 });
  assert.equal(late.intent, null);
  assert.equal(late.view, "armed");
});

test("expiry with a fake clock clears the armed request without any key press", () => {
  const clock = fakeClock(1000);
  const armed = step(initialState(), key(clock.now()));
  clock.advance(2000);
  const expired = step(armed.state, context(clock.now()));
  assert.equal(expired.state.phase, "idle");
  assert.equal(expired.view, "idle");
  assert.equal(expired.status, CLEARED_TEXT);
  assert.equal(expired.reason, "expired");
  assert.equal(expired.intent, null);
});

test("a clock that runs backwards or is missing fails closed and never confirms", () => {
  const [, backwards] = run([key(5000), key(1000)]);
  assert.equal(backwards.intent, null);
  assert.equal(backwards.view, "armed");

  const [, missing] = run([key(0), key(undefined)]);
  assert.equal(missing.intent, null);
});

const CHANGED_TARGETS = {
  "session changes": { sessionId: "s2", turnId: "t1", generation: 3 },
  "turn changes": { sessionId: "s1", turnId: "t2", generation: 3 },
  "generation changes": { sessionId: "s1", turnId: "t1", generation: 4 },
  "generation is dropped": { sessionId: "s1", turnId: "t1" },
  "no turn is active": null,
};

for (const [label, changed] of Object.entries(CHANGED_TARGETS)) {
  test(`an armed request is cleared when the target ${label} and never confirms`, () => {
    const [, cleared, afterwards] = run([key(0), context(100, { target: changed }), key(200, { target: changed })]);
    assert.equal(cleared.state.phase, "idle");
    assert.equal(cleared.status, CLEARED_TEXT);
    assert.equal(cleared.reason, "target-changed");
    assert.equal(cleared.intent, null);
    assert.notEqual(afterwards.view, "confirmed");
    assert.equal(afterwards.intent, null);
  });
}

test("a key press on a changed target arms the new target rather than confirming the old one", () => {
  const next = CHANGED_TARGETS["turn changes"];
  const [, rearmed] = run([key(0), key(100, { target: next })]);
  assert.equal(rearmed.view, "armed");
  assert.equal(rearmed.intent, null);
  assert.equal(rearmed.status, armedText("t2"));
});

test("leaving the declared region clears an armed request", () => {
  const [, left] = run([key(0), context(100, { inRegion: false })]);
  assert.equal(left.state.phase, "idle");
  assert.equal(left.reason, "focus-left");
  assert.equal(left.intent, null);
});

test("Escape outside the declared region neither arms nor confirms", () => {
  const [outside] = run([key(0, { inRegion: false })]);
  assert.equal(outside.view, "idle");
  assert.equal(outside.reason, "outside-region");

  const [, leftThenBack, afterwards] = run([key(0), key(100, { inRegion: false }), key(200)]);
  assert.equal(leftThenBack.reason, "focus-left");
  assert.equal(afterwards.view, "armed");
  assert.equal(afterwards.intent, null);
});

for (const other of ["a", "Enter", "Tab"]) {
  test(`pressing ${other} after the first Escape disarms the request`, () => {
    const [, disarmed, afterwards] = run([key(0), key(100, { key: other }), key(200)]);
    assert.equal(disarmed.view, "idle");
    assert.equal(disarmed.status, CLEARED_TEXT);
    assert.equal(disarmed.reason, "other-key");
    assert.equal(disarmed.intent, null);
    assert.equal(afterwards.view, "armed");
    assert.equal(afterwards.intent, null);
  });
}

const IGNORED = {
  "IME composition (isComposing)": { isComposing: true },
  "IME composition (keyCode 229)": { keyCode: 229 },
  "auto-repeat": { repeat: true },
  "Ctrl+Escape": { ctrlKey: true },
  "Alt+Escape": { altKey: true },
  "Meta+Escape": { metaKey: true },
  "Shift+Escape": { shiftKey: true },
};

for (const [label, flags] of Object.entries(IGNORED)) {
  test(`${label} is ignored and leaves an armed request intact`, () => {
    const [, ignored, confirmed] = run([key(0), key(100, flags), key(200)]);
    assert.equal(ignored.view, "armed");
    assert.equal(ignored.status, "");
    assert.equal(ignored.intent, null);
    assert.equal(confirmed.view, "confirmed");
    assert.deepEqual(confirmed.intent, REQUEST);
  });

  test(`${label} does not arm from idle`, () => {
    const [ignored] = run([key(0, flags)]);
    assert.equal(ignored.state.phase, "idle");
    assert.equal(ignored.view, "idle");
    assert.equal(ignored.intent, null);
  });
}

test("an active modal blocks arming while it is open", () => {
  const options = { isModalActive: () => true };
  const [blocked] = run([key(0)], options);
  assert.equal(blocked.view, "idle");
  assert.equal(blocked.reason, "modal-active");
});

test("a modal that opens clears an armed request, and Escape never confirms while it is open", () => {
  let modal = false;
  const options = { isModalActive: () => modal };

  const armed = step(initialState(), key(0), options);
  modal = true;
  const opened = step(armed.state, context(100), options);
  assert.equal(opened.state.phase, "idle");
  assert.equal(opened.reason, "modal-active");
  assert.equal(opened.intent, null);

  modal = false;
  const rearmed = step(initialState(), key(200), options);
  modal = true;
  const pressed = step(rearmed.state, key(300), options);
  assert.equal(pressed.state.phase, "idle");
  assert.equal(pressed.intent, null);
});

test("each confirmation emits exactly one intent and the machine returns to idle", () => {
  const outcomes = run([key(0), key(10), key(20), key(30)]);
  assert.equal(outcomes.filter((outcome) => outcome.intent !== null).length, 2);
  assert.equal(outcomes[1].view, "confirmed");
  assert.equal(outcomes[1].state.phase, "idle");
  assert.equal(outcomes[2].view, "armed");
  assert.equal(outcomes[3].view, "confirmed");
  assert.equal(outcomes[3].state.phase, "idle");
});

test("a reconnect or resync after confirmation does not re-emit or retry", () => {
  const outcomes = run([key(0), key(10), context(5000), context(6000), context(7000)]);
  assert.equal(outcomes.filter((outcome) => outcome.intent !== null).length, 1);
  for (const outcome of outcomes.slice(2)) {
    assert.equal(outcome.intent, null);
    assert.equal(outcome.state.phase, "idle");
  }
});

test("after an invalidation no request is emitted until a fresh confirmation", () => {
  const outcomes = run([key(0), key(50, { key: "x" }), key(100), context(200)]);
  assert.equal(outcomes.filter((outcome) => outcome.intent !== null).length, 0);
  assert.equal(outcomes[2].view, "armed");
  assert.equal(outcomes[3].state.phase, "armed");
});

test("Escape with no active target, or an incomplete one, does not arm", () => {
  const [none] = run([key(0, { target: null })]);
  assert.equal(none.view, "idle");
  assert.equal(none.reason, "no-target");

  const [partial] = run([key(0, { target: { sessionId: "s1" } })]);
  assert.equal(partial.state.phase, "idle");
  assert.equal(partial.intent, null);
});

test("no status wording claims a turn was cancelled, stopped or settled", () => {
  const outcomes = [
    ...run([key(0), key(10)]),
    ...run([key(0), key(2500)]),
    ...run([key(0), key(10, { key: "x" })]),
    ...run([key(0), context(10, { target: null })]),
  ];
  const texts = [CONFIRMED_TEXT, CLEARED_TEXT, armedText("t1"), armedText("turn-42")];
  for (const text of [...outcomes.map((outcome) => outcome.status), ...texts]) {
    assert.doesNotMatch(text, /cancelled|stopped|settled/i);
  }
  assert.equal(CONFIRMED_TEXT, "Cancel requested; waiting for the supervisor to settle the turn");
});

test("step never mutates the state it receives", () => {
  const armed = step(initialState(), key(0)).state;
  const expected = {
    phase: "armed",
    armed: { target: { sessionId: "s1", turnId: "t1", generation: 3 }, armedAt: 0 },
  };
  step(armed, key(10));
  step(armed, context(5000));
  assert.deepEqual(armed, expected);
});
