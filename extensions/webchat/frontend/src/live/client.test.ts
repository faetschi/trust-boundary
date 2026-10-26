import { describe, expect, it } from "vitest";
import { createFakeGateway, NOW_MS, SESSION_ID, TURN_1, type FakeGateway } from "../test-support/liveGateway";
import { createLiveClient, LiveError, shortTurn, type LiveClient } from "./client";

let counter = 0;
// Distinct bytes per call, so two keys differ and the test does not depend on the platform's random source.
function randomBytes(bytes: Uint8Array): void {
  counter += 1;
  bytes.fill(counter % 251);
  bytes[0] = counter & 0xff;
  bytes[1] = (counter >> 8) & 0xff;
}

function clientOver(gateway: FakeGateway): LiveClient {
  return createLiveClient({ fetch: gateway.fetch, nowMs: () => NOW_MS, randomBytes });
}

async function failureOf(promise: Promise<unknown>): Promise<LiveError> {
  try {
    await promise;
  } catch (error) {
    if (error instanceof LiveError) return error;
    throw error;
  }
  throw new Error("expected a LiveError");
}

describe("live client: same-origin requests and the CSRF header", () => {
  it("reads the CSRF token once from GET /api/v1/pair and sends it on every mutation and on no read", async () => {
    const gateway = createFakeGateway({ turns: [{ id: TURN_1, ordinal: 1, state: "admitted", prompt: "p" }] });
    const client = clientOver(gateway);
    await client.listSessions();
    await client.send(client.submitIntent(SESSION_ID, 4, "one"));
    await client.send(client.cancelIntent(SESSION_ID, TURN_1, 5));
    const pairReads = gateway.callsTo("GET", "/api/v1/pair");
    expect(pairReads).toHaveLength(1);
    const posts = gateway.calls.filter((call) => call.method === "POST");
    expect(posts).toHaveLength(2);
    for (const post of posts) expect(post.headers["X-TBound-CSRF"]).toBe(gateway.state.csrf);
    for (const read of gateway.calls.filter((call) => call.method === "GET")) expect(read.headers["X-TBound-CSRF"]).toBeUndefined();
    // The token is read before the first mutation, not after it.
    expect(gateway.calls.indexOf(pairReads[0]!)).toBeLessThan(gateway.calls.indexOf(posts[0]!));
  });

  it("recovers the token after a reload: a new client reads it from the cookie's session again", async () => {
    const gateway = createFakeGateway();
    await clientOver(gateway).send(clientOver(gateway).submitIntent(SESSION_ID, 4, "before"));
    const reloaded = clientOver(gateway);
    await reloaded.send(reloaded.submitIntent(SESSION_ID, 5, "after"));
    expect(gateway.callsTo("GET", "/api/v1/pair").length).toBeGreaterThanOrEqual(2);
    const last = gateway.calls.filter((call) => call.method === "POST").at(-1);
    expect(last?.headers["X-TBound-CSRF"]).toBe(gateway.state.csrf);
  });

  it("uses relative, same-origin, credentialed, uncached requests and never a cross-origin URL", async () => {
    const gateway = createFakeGateway();
    const client = clientOver(gateway);
    await client.listSessions();
    await client.snapshot(SESSION_ID);
    await client.observe(SESSION_ID, 0, 0);
    await client.send(client.submitIntent(SESSION_ID, 4, "x"));
    for (const call of gateway.calls) {
      expect(call.url.startsWith("/api/v1/")).toBe(true);
      expect(call.init.credentials).toBe("same-origin");
      expect(call.init.cache).toBe("no-store");
    }
  });

  it("does not cache a failed token read, so the next mutation reads it again", async () => {
    const gateway = createFakeGateway();
    gateway.inject((call) => call.url === "/api/v1/pair", { reject: "offline" });
    const client = clientOver(gateway);
    const first = await failureOf(client.send(client.submitIntent(SESSION_ID, 4, "x")));
    expect(first.kind).toBe("transport");
    await client.send(client.submitIntent(SESSION_ID, 4, "x"));
    expect(gateway.callsTo("GET", "/api/v1/pair")).toHaveLength(2);
  });
});

describe("live client: request keys", () => {
  it("builds a fresh key for each intent", () => {
    const client = clientOver(createFakeGateway());
    const one = client.submitIntent(SESSION_ID, 4, "same prompt");
    const two = client.submitIntent(SESSION_ID, 4, "same prompt");
    expect(one.key).not.toBe(two.key);
    expect(one.key).toMatch(/^ck-[0-9a-f]{32}$/);
    expect(one.body.request_key).toBe(one.key);
    const cancel = client.cancelIntent(SESSION_ID, TURN_1, 4);
    const resolve = client.resolveIntent(SESSION_ID, TURN_1, 4, "sha256:c", "publish");
    const create = client.createIntent({ projectId: "demo", providerProfile: "prof", publicationMode: "review", hostCalls: 1, tokens: 1, durationSeconds: 1, readOnly: false });
    expect(new Set([one.key, two.key, cancel.key, resolve.key, create.key]).size).toBe(5);
  });

  it("sends the same body and the same key when an intent is sent again", async () => {
    const gateway = createFakeGateway();
    gateway.inject((call) => call.method === "POST", { reject: "connection reset" });
    const client = clientOver(gateway);
    const intent = client.submitIntent(SESSION_ID, 4, "fix it");
    const failed = await failureOf(client.send(intent));
    expect(failed.kind).toBe("transport");
    expect(failed.outcomeUnknown).toBe(true);
    const answer = await client.send(intent);
    expect(answer.kind).toBe("submit");
    const posts = gateway.calls.filter((call) => call.method === "POST");
    expect(posts).toHaveLength(2);
    expect(posts[1]?.body).toEqual(posts[0]?.body);
    expect((posts[1]?.body as { request_key: string }).request_key).toBe(intent.key);
  });

  it("carries the expected revision, the prompt and nothing the gateway does not allow", () => {
    const client = clientOver(createFakeGateway());
    const submit = client.submitIntent(SESSION_ID, 7, "hello");
    expect(Object.keys(submit.body).sort()).toEqual(["expected_revision", "prompt", "request_key"]);
    expect(submit.body.expected_revision).toBe(7);
    const create = client.createIntent({ projectId: "demo", providerProfile: "prof", publicationMode: "auto", hostCalls: 3, tokens: 4, durationSeconds: 5, readOnly: true });
    expect(create.body).toMatchObject({ project_id: "demo", provider_profile: "prof", publication_mode: "auto", budget: { host_calls: 3, tokens: 4, duration_seconds: 5 }, read_only: true });
    const plain = client.createIntent({ projectId: "demo", providerProfile: "prof", publicationMode: "auto", hostCalls: 3, tokens: 4, durationSeconds: 5, readOnly: false });
    expect(plain.body).not.toHaveProperty("read_only");
  });

  it("names a turn by its short form on the path", () => {
    expect(shortTurn(TURN_1)).toBe("turn-1");
    expect(shortTurn("turn-2")).toBe("turn-2");
    const client = clientOver(createFakeGateway());
    expect(client.cancelIntent(SESSION_ID, TURN_1, 1).path).toBe(`/api/v1/sessions/${SESSION_ID}/turns/turn-1/cancel`);
  });
});

describe("live client: failures say whether the outcome is unknown", () => {
  const post = (gateway: FakeGateway, reply: Parameters<FakeGateway["inject"]>[1]) => {
    gateway.inject((call) => call.method === "POST", reply);
    const client = clientOver(gateway);
    return failureOf(client.send(client.submitIntent(SESSION_ID, 4, "x")));
  };

  it("keeps the service's stable code, and a refusal proves nothing ran", async () => {
    const error = await post(createFakeGateway(), { status: 409, body: { error: { code: "TURN_ACTIVE", detail: "a turn is running" } } });
    expect(error.code).toBe("TURN_ACTIVE");
    expect(error.status).toBe(409);
    expect(error.kind).toBe("service");
    expect(error.outcomeUnknown).toBe(false);
  });

  it("reports the gateway's own mark", async () => {
    const error = await post(createFakeGateway(), { status: 503, body: { error: { code: "SERVICE_UNAVAILABLE", detail: "lost after send", outcome_unknown: true } } });
    expect(error.code).toBe("SERVICE_UNAVAILABLE");
    expect(error.outcomeUnknown).toBe(true);
  });

  it("does not mark a clean 503: the request never reached the service", async () => {
    const error = await post(createFakeGateway(), { status: 503, body: { error: { code: "SERVICE_UNAVAILABLE", detail: "not reachable" } } });
    expect(error.outcomeUnknown).toBe(false);
  });

  it("marks a 502 and an answer that is not the contract's: the service may have run it", async () => {
    const bad = await post(createFakeGateway(), { status: 502, body: { error: { code: "BAD_SERVICE_RESPONSE" } } });
    expect(bad.outcomeUnknown).toBe(true);
    const html = await post(createFakeGateway(), { unreadable: 502 });
    expect(html.kind).toBe("protocol");
    expect(html.outcomeUnknown).toBe(true);
  });

  it("marks a 200 whose answer cannot be read: the mutation ran", async () => {
    const error = await post(createFakeGateway(), { status: 200, body: { turn: { id: "x" } } });
    expect(error.code).toBe("BAD_RESPONSE");
    expect(error.outcomeUnknown).toBe(true);
  });

  it("does not mark a failed read", async () => {
    const gateway = createFakeGateway();
    gateway.inject((call) => call.method === "GET", { reject: "offline" });
    const error = await failureOf(clientOver(gateway).listSessions());
    expect(error.kind).toBe("transport");
    expect(error.outcomeUnknown).toBe(false);
  });

  it("surfaces a gateway refusal of the token and reads the token again next time", async () => {
    const gateway = createFakeGateway();
    const client = clientOver(gateway);
    await client.send(client.submitIntent(SESSION_ID, 4, "ok"));
    gateway.state.csrf = "rotated";
    const refused = await failureOf(client.send(client.submitIntent(SESSION_ID, 5, "again")));
    expect(refused.code).toBe("FORBIDDEN");
    await client.send(client.submitIntent(SESSION_ID, 5, "third"));
    expect(gateway.callsTo("GET", "/api/v1/pair")).toHaveLength(2);
  });
});

describe("live client: answers are parsed at the boundary", () => {
  it("returns the snapshot with its live view", async () => {
    const gateway = createFakeGateway();
    const snapshot = await clientOver(gateway).snapshot(SESSION_ID);
    expect(snapshot.source).toBe("live");
    expect(snapshot.live?.revision).toBe(4);
  });

  it("refuses an events answer whose items do not pair with its events", async () => {
    const gateway = createFakeGateway({ events: [{ kind: "turn_admitted", turn_id: TURN_1, text: "hi" }] });
    gateway.inject(
      (call) => call.url.includes("/events"),
      { status: 200, body: { schema_version: "tbound-webchat-observation/v1", session_id: SESSION_ID, cursor: 0, next_cursor: 1, gap: false, events: [{ seq: 0, kind: "turn_admitted", turn_id: TURN_1 }], items: [] } },
    );
    const error = await failureOf(clientOver(gateway).observe(SESSION_ID, 0, 0));
    expect(error.code).toBe("BAD_RESPONSE");
    expect(error.detail).toContain("does not pair");
  });

  it("refuses an events window that does not continue the requested cursor", async () => {
    const gateway = createFakeGateway({ events: [{ kind: "turn_admitted", turn_id: TURN_1, text: "hi" }] });
    gateway.inject(
      (call) => call.url.includes("/events"),
      { status: 200, body: { schema_version: "tbound-webchat-observation/v1", session_id: SESSION_ID, cursor: 3, next_cursor: 3, gap: false, events: [], items: [] } },
    );
    const error = await failureOf(clientOver(gateway).observe(SESSION_ID, 0, 0));
    expect(error.detail).toContain("not the requested cursor");
  });

  it("refuses a snapshot that is not a live projection", async () => {
    const gateway = createFakeGateway();
    const fixture = { ...gateway.snapshot(), source: "fixture", verification_scope: "unverified-input", live: undefined };
    gateway.inject((call) => call.url.endsWith(SESSION_ID), { status: 200, body: JSON.parse(JSON.stringify(fixture)) });
    const error = await failureOf(clientOver(gateway).snapshot(SESSION_ID));
    expect(error.code).toBe("BAD_RESPONSE");
  });
});
