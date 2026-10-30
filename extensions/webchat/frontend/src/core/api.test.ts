// Port of internal/webchat/jstest/api.test.mjs. Same test names and assertions. The browser's
// fetch and EventSource are injected rather than stubbed on globalThis.
import { test } from "vitest";
import assert from "node:assert/strict";

import { fetchSnapshot, openEventStream } from "./api";
import type {
  EventSourceFactory,
  EventSourceLike,
  FetchInit,
  FetchLike,
  FetchResponseLike,
  RawEventHandlers,
} from "./api";

type Listener = (event: { readonly data?: string }) => void;

// A stand-in for the browser's EventSource. It records the URL, every listener and
// every close, so a test can drive the transport the way the browser would.
class FakeEventSource implements EventSourceLike {
  static instances: FakeEventSource[] = [];

  readonly url: string;
  readonly listeners = new Map<string, Listener>();
  closes = 0;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: Listener): void {
    this.listeners.set(type, listener);
  }

  close(): void {
    this.closes += 1;
  }

  dispatch(type: string, data?: string): void {
    const listener = this.listeners.get(type);
    if (!listener) throw new Error(`no listener registered for ${type}`);
    listener({ data });
  }
}

// The source the factory built at `index`. A missing one fails the test.
function instance(index: number): FakeEventSource {
  const source = FakeEventSource.instances[index];
  if (!source) throw new Error(`no EventSource was constructed at index ${index}`);
  return source;
}

// Gives one test a factory that builds the fake, with a fresh instance list.
function withFakeEventSource(run: (factory: EventSourceFactory) => void): void {
  FakeEventSource.instances = [];
  run((url) => new FakeEventSource(url));
}

interface Calls {
  readonly item: unknown[];
  readonly session: unknown[];
  readonly gap: unknown[];
  readonly connection: ("open" | "closed")[];
  readonly error: unknown[];
}

function handlers(): RawEventHandlers & { readonly calls: Calls } {
  const calls: Calls = { item: [], session: [], gap: [], connection: [], error: [] };
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
  withFakeEventSource((factory) => {
    openEventStream(factory, { cursor: "e1:2", ...handlers() });
    openEventStream(factory, { cursor: "", ...handlers() });
    assert.equal(instance(0).url, "/api/v1/events?cursor=e1%3A2");
    assert.equal(instance(1).url, "/api/v1/events");
  });
});

test("item, session and gap events are decoded and routed to their handlers", () => {
  withFakeEventSource((factory) => {
    const h = handlers();
    openEventStream(factory, { cursor: "", ...h });
    const source = instance(0);
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
  withFakeEventSource((factory) => {
    const h = handlers();
    openEventStream(factory, { cursor: "", ...h });
    const source = instance(0);
    source.dispatch("item", "{not json");
    source.dispatch("gap", "");
    assert.equal(h.calls.error.length, 2);
    assert.ok(h.calls.error.every((error) => error instanceof SyntaxError));
    assert.equal(h.calls.item.length, 0);
    assert.equal(h.calls.gap.length, 0);
  });
});

test("open is reported as open, and nothing is reported as closed while the stream is healthy", () => {
  withFakeEventSource((factory) => {
    const h = handlers();
    openEventStream(factory, { cursor: "", ...h });
    const source = instance(0);
    source.dispatch("open");
    assert.deepEqual(h.calls.connection, ["open"]);
    assert.equal(source.closes, 0);
  });
});

test("an error closes the EventSource and reports closed exactly once, so the browser never retries on its own", () => {
  withFakeEventSource((factory) => {
    const h = handlers();
    openEventStream(factory, { cursor: "", ...h });
    const source = instance(0);
    source.dispatch("error");
    assert.equal(source.closes, 1, "the browser's fixed-delay retry is cut off by closing");
    assert.deepEqual(h.calls.connection, ["closed"]);
  });
});

test("repeated errors each close the stream and report closed", () => {
  withFakeEventSource((factory) => {
    const h = handlers();
    openEventStream(factory, { cursor: "", ...h });
    const source = instance(0);
    source.dispatch("error");
    source.dispatch("error");
    assert.equal(source.closes, 2, "each error closes; closing again is harmless");
    assert.deepEqual(h.calls.connection, ["closed", "closed"]);
  });
});

test("close() on the handle closes the underlying EventSource", () => {
  withFakeEventSource((factory) => {
    const stream = openEventStream(factory, { cursor: "", ...handlers() });
    stream.close();
    assert.equal(instance(0).closes, 1);
  });
});

test("a missing connection handler is tolerated when the stream errors", () => {
  withFakeEventSource((factory) => {
    openEventStream(factory, { cursor: "", onItem: () => {}, onSession: () => {}, onGap: () => {} });
    const source = instance(0);
    assert.doesNotThrow(() => source.dispatch("error"));
    assert.equal(source.closes, 1);
  });
});

/* ---------------------------------------------------------------------------
 * Snapshot request
 * ------------------------------------------------------------------------- */

interface FetchCall {
  readonly url: string;
  readonly init: FetchInit;
}

// Stands in for the browser's fetch: records each request and answers it with `respond`.
async function withFakeFetch(
  respond: (url: string, init: FetchInit) => FetchResponseLike,
  run: (calls: readonly FetchCall[], transport: FetchLike) => Promise<void>,
): Promise<void> {
  const calls: FetchCall[] = [];
  const transport: FetchLike = async (url, init) => {
    calls.push({ url, init });
    return respond(url, init);
  };
  await run(calls, transport);
}

test("the snapshot request is a no-store GET that carries the caller's signal", async () => {
  const controller = new AbortController();
  await withFakeFetch(
    () => ({ ok: true, status: 200, json: async () => ({ cursor: "e1:0" }) }),
    async (calls, transport) => {
      const snapshot = await fetchSnapshot(transport, controller.signal);
      assert.deepEqual(snapshot, { cursor: "e1:0" });
      assert.equal(calls[0]?.url, "/api/v1/snapshot");
      assert.equal(calls[0]?.init.method, "GET");
      assert.equal(calls[0]?.init.cache, "no-store");
      assert.equal(calls[0]?.init.signal, controller.signal, "the signal reaches fetch so an attempt can be aborted");
    },
  );
});

test("a failed snapshot response rejects with its status", async () => {
  await withFakeFetch(
    () => ({ ok: false, status: 503, json: async () => ({}) }),
    async (_calls, transport) => {
      await assert.rejects(fetchSnapshot(transport), { message: "snapshot request failed (503)" });
    },
  );
});
