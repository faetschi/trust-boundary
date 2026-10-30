import { describe, expect, it } from "vitest";
import type { ItemEvent, ReplayGap } from "../core/contract";
import type { StreamEventHandlers } from "../core/stream";
import { createFakeGateway, NOW_MS, SESSION_ID, TURN_1, type FakeGateway } from "../test-support/liveGateway";
import { createLiveClient } from "./client";
import { cursorCount, openLongPoll, POLL_WAIT_MS } from "./poller";
import type { LiveEvent } from "./wire";

const flush = async () => {
  for (let round = 0; round < 6; round++) await new Promise((resolve) => setTimeout(resolve, 0));
};

const EVENTS = [
  { kind: "turn_admitted", turn_id: TURN_1, text: "fix it" },
  { kind: "turn_linked", turn_id: TURN_1 },
  { kind: "assistant_message", turn_id: TURN_1, text: "Reading the file." },
] as const;

interface Run {
  readonly gateway: FakeGateway;
  readonly items: ItemEvent[];
  readonly gaps: ReplayGap[];
  readonly invalid: unknown[];
  readonly errors: unknown[];
  readonly connections: string[];
  readonly batches: { events: readonly LiveEvent[]; next: number }[];
  readonly observed: () => string[];
  close(): void;
}

function start(gateway: FakeGateway, cursor: string): Run {
  const client = createLiveClient({ fetch: gateway.fetch, nowMs: () => NOW_MS, randomBytes: (bytes) => bytes.fill(1) });
  const run: Omit<Run, "close"> = { gateway, items: [], gaps: [], invalid: [], errors: [], connections: [], batches: [], observed: () => gateway.callsTo("GET", "/events").map((call) => call.url) };
  const handlers: StreamEventHandlers = {
    cursor,
    projection: { source: "live", verification: "structural-audit-only" },
    onItem: (event) => run.items.push(event),
    onSession: () => {},
    onGap: (gap) => run.gaps.push(gap),
    onInvalid: (error) => run.invalid.push(error),
    onConnection: (value) => run.connections.push(value),
    onError: (error) => run.errors.push(error),
  };
  const handle = openLongPoll(
    {
      client,
      sessionId: SESSION_ID,
      nowMs: () => NOW_MS,
      createAbortController: () => {
        const signal = { aborted: false };
        return { signal, abort: () => void (signal.aborted = true) };
      },
      onBatch: (events, next) => run.batches.push({ events, next }),
    },
    handlers,
  );
  return { ...run, close: () => handle.close() };
}

describe("live poller: resuming from a cursor", () => {
  it("reads the count out of a snapshot cursor", () => {
    expect(cursorCount("psess-t1:12")).toBe(12);
    expect(cursorCount("psess-t1:0")).toBe(0);
    expect(cursorCount("psess-t1:x")).toBeNull();
    expect(cursorCount("")).toBeNull();
  });

  it("starts at the snapshot's cursor, with no wait on the first poll and the gateway's limit on the next", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const run = start(gateway, `${SESSION_ID}:1`);
    await flush();
    expect(run.observed()).toEqual([
      `/api/v1/sessions/${SESSION_ID}/events?cursor=1&wait=0`,
      `/api/v1/sessions/${SESSION_ID}/events?cursor=3&wait=${POLL_WAIT_MS}`,
    ]);
    run.close();
  });

  it("delivers each window's items in order, as events the stream controller already accepts", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const run = start(gateway, `${SESSION_ID}:1`);
    await flush();
    expect(run.items.map((event) => event.item.sequence)).toEqual([2, 3]);
    expect(run.items.map((event) => event.cursor)).toEqual([`${SESSION_ID}:2`, `${SESSION_ID}:3`]);
    expect(run.items[0]?.item).toMatchObject({ type: "message", role: "system", authority: "observed" });
    expect(run.items[1]?.item).toMatchObject({ type: "message", role: "assistant", text: "Reading the file." });
    expect(run.connections).toEqual(["open"]);
    expect(run.batches).toHaveLength(1);
    expect(run.batches[0]?.events.map((event) => event.kind)).toEqual(["turn_linked", "assistant_message"]);
    expect(run.batches[0]?.next).toBe(3);

    gateway.append([{ kind: "tool_call", turn_id: TURN_1, tool: "bash", tool_call_id: "call_1", verdict: "DENY", reason_code: "policy_rule_deny", hint: "use read" }]);
    await flush();
    expect(run.items.map((event) => event.item.sequence)).toEqual([2, 3, 4]);
    expect(run.items[2]?.item).toMatchObject({ type: "decision", verdict: "DENY", reason_code: "policy_rule_deny", summary: "use read" });
    expect(run.observed().at(-1)).toContain("cursor=4");
    expect(run.connections).toEqual(["open"]);
    run.close();
  });

  it("stops at a gap without applying its window, so the controller recovers from a snapshot", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    gateway.inject(
      (call) => call.url.includes("/events"),
      {
        status: 200,
        body: {
          schema_version: "tbound-webchat-observation/v1",
          session_id: SESSION_ID,
          cursor: 1,
          next_cursor: 2,
          gap: true,
          events: [{ seq: 1, kind: "turn_linked", turn_id: TURN_1 }],
          items: [JSON.parse(JSON.stringify(gateway.snapshot().items[1]))],
        },
      },
    );
    const run = start(gateway, `${SESSION_ID}:1`);
    await flush();
    expect(run.gaps).toEqual([{ schema_version: "tbound-webchat-stream-gap/v1", reason: "history_evicted", epoch: SESSION_ID, latest: 2 }]);
    expect(run.items).toEqual([]);
    expect(run.batches).toEqual([]);
    expect(run.observed()).toHaveLength(1);
  });

  it("reports a transport failure as a closed connection, so the controller reconnects snapshot first", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    gateway.inject((call) => call.url.includes("/events"), { reject: "network down" });
    const run = start(gateway, `${SESSION_ID}:1`);
    await flush();
    expect(run.errors).toHaveLength(1);
    expect(run.connections).toEqual(["closed"]);
    expect(run.observed()).toHaveLength(1);
  });

  it("refuses an item the projection does not admit and stops", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const items = gateway.snapshot().items.map((item) => JSON.parse(JSON.stringify(item)));
    items[1].authority = "authoritative";
    gateway.inject(
      (call) => call.url.includes("/events"),
      {
        status: 200,
        body: {
          schema_version: "tbound-webchat-observation/v1",
          session_id: SESSION_ID,
          cursor: 1,
          next_cursor: 3,
          gap: false,
          events: [
            { seq: 1, kind: "turn_linked", turn_id: TURN_1 },
            { seq: 2, kind: "assistant_message", turn_id: TURN_1, text: "x" },
          ],
          items: items.slice(1),
        },
      },
    );
    const run = start(gateway, `${SESSION_ID}:1`);
    await flush();
    expect(run.invalid).toHaveLength(1);
    expect(run.items).toEqual([]);
    expect(run.observed()).toHaveLength(1);
  });

  it("stops polling and delivers nothing after close", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const run = start(gateway, `${SESSION_ID}:3`);
    await flush();
    expect(run.observed()).toHaveLength(2);
    run.close();
    gateway.append([{ kind: "assistant_message", turn_id: TURN_1, text: "late" }]);
    await flush();
    expect(run.items).toEqual([]);
    expect(run.observed()).toHaveLength(2);
  });

  it("refuses a cursor that is not a session cursor", async () => {
    const run = start(createFakeGateway(), "garbage");
    await flush();
    expect(run.errors).toHaveLength(1);
    expect(run.connections).toEqual(["closed"]);
    expect(run.observed()).toEqual([]);
  });
});
