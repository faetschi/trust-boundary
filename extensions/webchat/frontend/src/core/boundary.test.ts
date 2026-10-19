// The validated JSON boundary (api.ts fetchValidatedSnapshot and openValidatedEventStream). The raw legacy
// transports are covered by api.test.ts; these tests cover the new typed layer on top of them.
import { describe, expect, test } from "vitest";

import { fetchValidatedSnapshot, openValidatedEventStream, type EventSourceLike, type FetchLike, type FetchResponseLike } from "./api";
import { CONTRACT_VERSION, GAP_VERSION, type ItemEvent, type ReplayGap, type SessionEvent } from "./contract";
import { AdmissionError, ContractError } from "./validate";
import { messageItem, resultItem, snapshotOf } from "./testing/builders";
import type { ProjectionContext } from "./verification";

// A fixed clock, after every builder timestamp (2026-10-13).
const NOW = Date.parse("2026-10-14T00:00:00Z");

// The projection a stream opened after a fixture snapshot binds to, and the live one.
const FIXTURE: ProjectionContext = { source: "fixture", verification: "unverified-input" };
const LIVE: ProjectionContext = { source: "live", verification: "structural-audit-only" };

// A stand-in for the browser's EventSource that records its listeners and lets a test dispatch.
class FakeEventSource implements EventSourceLike {
  readonly listeners = new Map<string, (event: { readonly data?: string }) => void>();
  closes = 0;

  addEventListener(type: string, listener: (event: { readonly data?: string }) => void): void {
    this.listeners.set(type, listener);
  }

  close(): void {
    this.closes += 1;
  }

  dispatch(type: string, data?: string): void {
    const listener = this.listeners.get(type);
    if (!listener) throw new Error(`no listener for ${type}`);
    listener({ data });
  }
}

function responder(body: unknown): FetchLike {
  return async (): Promise<FetchResponseLike> => ({
    ok: true,
    status: 200,
    json: async () => body,
  });
}

describe("validated snapshot boundary", () => {
  test("a snapshot that passes the contract resolves to the typed snapshot", async () => {
    const snapshot = snapshotOf({
      epoch: "e1",
      items: [messageItem({ id: "m1", sequence: 1, text: "hello", role: "user" })],
    });
    // The wire form: the snapshot as JSON text, decoded with no type information.
    const body: unknown = JSON.parse(JSON.stringify(snapshot));
    const parsed = await fetchValidatedSnapshot(responder(body), () => NOW);
    expect(parsed.cursor).toBe("e1:1");
    expect(parsed.items.map((item) => item.id)).toEqual(["m1"]);
  });

  test("a payload with an unknown field rejects with a ContractError, not a partial value", async () => {
    const snapshot = { ...snapshotOf({ epoch: "e1" }), extra: true };
    await expect(fetchValidatedSnapshot(responder(snapshot), () => NOW)).rejects.toThrow(ContractError);
    await expect(fetchValidatedSnapshot(responder(snapshot), () => NOW)).rejects.toThrow(/unknown field "extra"/);
  });

  test("a failed HTTP status rejects before any payload is read", async () => {
    const failing: FetchLike = async () => ({ ok: false, status: 503, json: async () => ({}) });
    await expect(fetchValidatedSnapshot(failing, () => NOW)).rejects.toThrow("snapshot request failed (503)");
  });
});

describe("validated event stream boundary", () => {
  function open(projection: ProjectionContext = FIXTURE): {
    source: FakeEventSource;
    items: ItemEvent[];
    sessions: SessionEvent[];
    gaps: ReplayGap[];
    errors: unknown[];
    invalid: unknown[];
  } {
    const source = new FakeEventSource();
    const items: ItemEvent[] = [];
    const sessions: SessionEvent[] = [];
    const gaps: ReplayGap[] = [];
    const errors: unknown[] = [];
    const invalid: unknown[] = [];
    openValidatedEventStream(
      () => source,
      {
        cursor: "e1:0",
        nowMs: () => NOW,
        projection,
        onItem: (event) => items.push(event),
        onSession: (event) => sessions.push(event),
        onGap: (gap) => gaps.push(gap),
        onError: (error) => errors.push(error),
        onInvalid: (error) => invalid.push(error),
      },
    );
    return { source, items, sessions, gaps, errors, invalid };
  }

  test("a streamed observed item in a fixture projection is refused, reported, and handed to onInvalid", () => {
    // The item passes the item-only rule (observed, structurally audited). Only the projection it arrives in refuses it.
    const { source, items, errors, invalid } = open(FIXTURE);
    const observed = resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" });
    source.dispatch("item", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: observed }));
    expect(items).toHaveLength(0);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(AdmissionError);
    expect(invalid).toHaveLength(1);
    expect(invalid[0]).toBe(errors[0]);
  });

  test("the same streamed observed item is admitted in a live structural projection", () => {
    const { source, items, errors, invalid } = open(LIVE);
    const observed = resultItem({ id: "r1", sequence: 1, tool: "bash", tool_call_id: "c1", state: "succeeded", authority: "observed", verification_scope: "structural-audit-only" });
    source.dispatch("item", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: observed }));
    expect(errors).toEqual([]);
    expect(invalid).toEqual([]);
    expect(items.map((event) => event.item.id)).toEqual(["r1"]);
  });

  test("an item payload that is not JSON is reported and handed to onInvalid too, so the stream recovers rather than dropping it", () => {
    const { source, items, errors, invalid } = open(FIXTURE);
    source.dispatch("item", "{not json");
    expect(items).toHaveLength(0);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(SyntaxError);
    expect(invalid).toEqual(errors);
  });

  test("an admitted item never reaches onInvalid, and a contract failure reaches it as well as onError", () => {
    const { source, items, errors, invalid } = open(FIXTURE);
    source.dispatch("item", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: messageItem({ id: "m1", sequence: 1, text: "hi" }) }));
    expect(items).toHaveLength(1);
    expect(invalid).toEqual([]);
    source.dispatch("item", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:2", kind: "item", item: { ...messageItem({ id: "m2", sequence: 2 }), extra: 1 } }));
    expect(errors).toHaveLength(1);
    expect(invalid).toHaveLength(1);
    expect(invalid[0]).toBeInstanceOf(ContractError);
  });

  test("a valid item event reaches onItem as a typed ItemEvent", () => {
    const { source, items, errors } = open();
    const event = { schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: messageItem({ id: "m1", sequence: 1, text: "hi" }) };
    source.dispatch("item", JSON.stringify(event));
    expect(errors).toEqual([]);
    expect(items.map((item) => item.item.id)).toEqual(["m1"]);
  });

  test("a valid session event reaches onSession, and an item on the session channel is refused", () => {
    const { source, sessions, errors } = open();
    source.dispatch("session", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:0", kind: "session", session: { status: "running", turn_count: 1 } }));
    expect(sessions).toHaveLength(1);
    source.dispatch("session", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: messageItem({ id: "m1", sequence: 1 }) }));
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
  });

  test("an event that fails the contract is reported to onError and no handler runs for it", () => {
    const { source, items, errors } = open();
    source.dispatch("item", JSON.stringify({ schema_version: CONTRACT_VERSION, cursor: "e1:1", kind: "item", item: { ...messageItem({ id: "m1", sequence: 1 }), extra: 1 } }));
    expect(items).toHaveLength(0);
    expect(errors[0]).toBeInstanceOf(ContractError);
    expect(String(errors[0])).toContain('unknown field "extra"');
  });

  test("a payload that is not JSON is reported to onError, as in the raw transport", () => {
    const { source, items, errors } = open();
    source.dispatch("item", "{not json");
    expect(items).toHaveLength(0);
    expect(errors[0]).toBeInstanceOf(SyntaxError);
  });

  test("a valid replay gap reaches onGap, and a gap with an unknown field is refused", () => {
    const { source, gaps, errors } = open();
    source.dispatch("gap", JSON.stringify({ schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 4, earliest: 2 }));
    expect(gaps.map((gap) => gap.reason)).toEqual(["history_evicted"]);
    source.dispatch("gap", JSON.stringify({ schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 4, retry: true }));
    expect(errors).toHaveLength(1);
    expect(String(errors[0])).toContain('unknown field "retry"');
  });
});
