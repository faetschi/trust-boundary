import test from "node:test";
import assert from "node:assert/strict";

import { fetchSnapshot, openEventStream } from "../assets/api.mjs";

// A stand-in for the browser's EventSource. It records the URL, every listener and
// every close, so a test can drive the transport the way the browser would.
class FakeEventSource {
  static instances = [];

  constructor(url) {
    this.url = url;
    this.listeners = new Map();
    this.closes = 0;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type, listener) {
    this.listeners.set(type, listener);
  }

  close() {
    this.closes += 1;
  }

  dispatch(type, data) {
    this.listeners.get(type)({ data });
  }
}

// Installs the fake for one test and restores the global afterwards.
function withFakeEventSource(run) {
  const original = globalThis.EventSource;
  FakeEventSource.instances = [];
  globalThis.EventSource = FakeEventSource;
  try {
    return run();
  } finally {
    globalThis.EventSource = original;
  }
}

function handlers() {
  const calls = { item: [], session: [], gap: [], connection: [], error: [] };
  return {
    calls,
    onItem: (value) => calls.item.push(value),
    onSession: (value) => calls.session.push(value),
    onGap: (value) => calls.gap.push(value),
    onConnection: (value) => calls.connection.push(value),
    onError: (error) => calls.error.push(error),
  };
}

test("the stream opens from the cursor it is given, and without one when it has none", () => {
  withFakeEventSource(() => {
    openEventStream({ cursor: "e1:2", ...handlers() });
    openEventStream({ cursor: "", ...handlers() });
    assert.equal(FakeEventSource.instances[0].url, "/api/v1/events?cursor=e1%3A2");
    assert.equal(FakeEventSource.instances[1].url, "/api/v1/events");
  });
});

test("item, session and gap events are decoded and routed to their handlers", () => {
  withFakeEventSource(() => {
    const h = handlers();
    openEventStream({ cursor: "", ...h });
    const source = FakeEventSource.instances[0];
    source.dispatch("item", '{"id":"a","sequence":1}');
    source.dispatch("session", '{"session":{"session_id":"s1"}}');
    source.dispatch("gap", '{"reason":"history_evicted"}');
    assert.deepEqual(h.calls.item, [{ id: "a", sequence: 1 }]);
    assert.deepEqual(h.calls.session, [{ session: { session_id: "s1" } }]);
    assert.deepEqual(h.calls.gap, [{ reason: "history_evicted" }]);
    assert.equal(h.calls.error.length, 0);
  });
});

test("a payload that does not decode goes to onError, and no handler runs for it", () => {
  withFakeEventSource(() => {
    const h = handlers();
    openEventStream({ cursor: "", ...h });
    const source = FakeEventSource.instances[0];
    source.dispatch("item", "{not json");
    source.dispatch("gap", "");
    assert.equal(h.calls.error.length, 2);
    assert.ok(h.calls.error.every((error) => error instanceof SyntaxError));
    assert.equal(h.calls.item.length, 0);
    assert.equal(h.calls.gap.length, 0);
  });
});

test("open is reported as open, and nothing is reported as closed while the stream is healthy", () => {
  withFakeEventSource(() => {
    const h = handlers();
    openEventStream({ cursor: "", ...h });
    const source = FakeEventSource.instances[0];
    source.dispatch("open");
    assert.deepEqual(h.calls.connection, ["open"]);
    assert.equal(source.closes, 0);
  });
});

test("an error closes the EventSource and reports closed exactly once, so the browser never retries on its own", () => {
  withFakeEventSource(() => {
    const h = handlers();
    openEventStream({ cursor: "", ...h });
    const source = FakeEventSource.instances[0];
    source.dispatch("error");
    assert.equal(source.closes, 1, "the browser's fixed-delay retry is cut off by closing");
    assert.deepEqual(h.calls.connection, ["closed"]);
  });
});

test("repeated errors each close the stream and report closed", () => {
  withFakeEventSource(() => {
    const h = handlers();
    openEventStream({ cursor: "", ...h });
    const source = FakeEventSource.instances[0];
    source.dispatch("error");
    source.dispatch("error");
    assert.equal(source.closes, 2, "each error closes; closing again is harmless");
    assert.deepEqual(h.calls.connection, ["closed", "closed"]);
  });
});

test("close() on the handle closes the underlying EventSource", () => {
  withFakeEventSource(() => {
    const stream = openEventStream({ cursor: "", ...handlers() });
    stream.close();
    assert.equal(FakeEventSource.instances[0].closes, 1);
  });
});

test("a missing connection handler is tolerated when the stream errors", () => {
  withFakeEventSource(() => {
    openEventStream({ cursor: "" });
    const source = FakeEventSource.instances[0];
    assert.doesNotThrow(() => source.dispatch("error"));
    assert.equal(source.closes, 1);
  });
});

/* ---------------------------------------------------------------------------
 * Snapshot request
 * ------------------------------------------------------------------------- */

async function withFakeFetch(respond, run) {
  const original = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url, init });
    return respond(url, init);
  };
  try {
    return await run(calls);
  } finally {
    globalThis.fetch = original;
  }
}

test("the snapshot request is a no-store GET that carries the caller's signal", async () => {
  const controller = new AbortController();
  await withFakeFetch(
    () => ({ ok: true, json: async () => ({ cursor: "e1:0" }) }),
    async (calls) => {
      const snapshot = await fetchSnapshot(controller.signal);
      assert.deepEqual(snapshot, { cursor: "e1:0" });
      assert.equal(calls[0].url, "/api/v1/snapshot");
      assert.equal(calls[0].init.method, "GET");
      assert.equal(calls[0].init.cache, "no-store");
      assert.equal(calls[0].init.signal, controller.signal, "the signal reaches fetch so an attempt can be aborted");
    },
  );
});

test("a failed snapshot response rejects with its status", async () => {
  await withFakeFetch(
    () => ({ ok: false, status: 503, json: async () => ({}) }),
    async () => {
      await assert.rejects(fetchSnapshot(), { message: "snapshot request failed (503)" });
    },
  );
});
