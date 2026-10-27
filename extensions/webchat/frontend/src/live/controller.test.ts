import { describe, expect, it } from "vitest";
import { createWorkspaceStore, type WorkspaceStore } from "../app/workspaceStore";
import { snapshotOf } from "../core/testing/builders";
import { fakeWorkspace } from "../test-support/fakes";
import { createFakeGateway, NOW_MS, SESSION_ID, TURN_1, type FakeGateway } from "../test-support/liveGateway";
import { createLiveClient } from "./client";
import {
  ACTIVE_SESSION_KEY,
  AUTO_RETRY_DELAYS_MS,
  createLiveController,
  cursorKey,
  type LiveController,
  type LiveStorage,
} from "./controller";
import { retryable } from "./readings";

const flush = async () => {
  for (let round = 0; round < 8; round++) await new Promise((resolve) => setTimeout(resolve, 0));
};

function memoryStorage(initial: Record<string, string> = {}): LiveStorage & { readonly data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return { data, get: (key) => data.get(key) ?? null, set: (key, value) => void data.set(key, value) };
}

let keyCounter = 0;
function randomBytes(bytes: Uint8Array): void {
  keyCounter += 1;
  bytes.fill(7);
  bytes[0] = keyCounter & 0xff;
  bytes[1] = (keyCounter >> 8) & 0xff;
}

const RUNNING = { id: TURN_1, ordinal: 1, state: "admitted", prompt: "fix it" } as const;
const EVENTS = [
  { kind: "turn_admitted", turn_id: TURN_1, text: "fix it" },
  { kind: "turn_linked", turn_id: TURN_1 },
  { kind: "assistant_message", turn_id: TURN_1, text: "Looking." },
] as const;

interface Rig {
  readonly gateway: FakeGateway;
  readonly controller: LiveController;
  readonly storage: ReturnType<typeof memoryStorage>;
  readonly sleeps: number[];
  readonly stores: WorkspaceStore[];
  // Opens the session as the app does: the workspace store loads the snapshot and starts the event stream.
  connect(): WorkspaceStore;
}

function rig(options: { gateway?: FakeGateway; storage?: ReturnType<typeof memoryStorage> } = {}): Rig {
  const gateway = options.gateway ?? createFakeGateway();
  const storage = options.storage ?? memoryStorage();
  const sleeps: number[] = [];
  const stores: WorkspaceStore[] = [];
  const controller = createLiveController({
    client: createLiveClient({ fetch: gateway.fetch, nowMs: () => NOW_MS, randomBytes }),
    base: fakeWorkspace(snapshotOf()).deps,
    storage,
    sleep: async (ms) => void sleeps.push(ms),
  });
  return {
    gateway,
    controller,
    storage,
    sleeps,
    stores,
    connect() {
      const store = createWorkspaceStore(controller.workspaceDeps(SESSION_ID));
      stores.push(store);
      store.start();
      return store;
    },
  };
}

async function open(setup: Rig): Promise<void> {
  await setup.controller.start();
  setup.controller.select(SESSION_ID);
  setup.connect();
  await flush();
}

function posts(gateway: FakeGateway, part: string) {
  return gateway.callsTo("POST", part);
}

describe("live controller: opening and resuming a session", () => {
  it("lists the sessions and reopens the one this tab had open", async () => {
    const setup = rig({ storage: memoryStorage({ [ACTIVE_SESSION_KEY]: SESSION_ID }) });
    await setup.controller.start();
    expect(setup.controller.getSnapshot().sessions?.map((session) => session.id)).toEqual([SESSION_ID]);
    expect(setup.controller.getSnapshot().activeId).toBe(SESSION_ID);
  });

  it("does not reopen a session the service no longer lists", async () => {
    const setup = rig({ storage: memoryStorage({ [ACTIVE_SESSION_KEY]: "psess-gone" }) });
    await setup.controller.start();
    expect(setup.controller.getSnapshot().activeId).toBeNull();
  });

  it("takes the revision from the snapshot's live view", async () => {
    const setup = rig();
    await open(setup);
    expect(setup.controller.getSnapshot().view?.revision).toBe(4);
  });

  it("stores the cursor, and a reload says how many events arrived while the page was closed", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const storage = memoryStorage({ [cursorKey(SESSION_ID)]: "1" });
    const first = rig({ gateway, storage });
    await open(first);
    expect(first.controller.getSnapshot().sinceLeft).toBe(2);
    expect(storage.data.get(cursorKey(SESSION_ID))).toBe("3");

    gateway.append([{ kind: "assistant_message", turn_id: TURN_1, text: "more" }]);
    await flush();
    expect(storage.data.get(cursorKey(SESSION_ID))).toBe("4");
    first.stores.forEach((store) => store.stop());

    const reloaded = rig({ gateway, storage });
    await open(reloaded);
    expect(reloaded.controller.getSnapshot().sinceLeft).toBe(0);
    // The stream resumes from the snapshot's cursor: the first poll asks for the 4 events the snapshot already holds.
    expect(gateway.callsTo("GET", "/events").some((call) => call.url.includes("cursor=4&wait=0"))).toBe(true);
    reloaded.stores.forEach((store) => store.stop());
  });

  it("keeps the workspace's session header and panels current as the session changes", async () => {
    const setup = rig();
    await setup.controller.start();
    setup.controller.select(SESSION_ID);
    const store = setup.connect();
    await flush();
    expect(store.getSnapshot().session?.turn_count).toBe(0);
    expect(await setup.controller.submit("fix it")).toBe(true);
    await flush();
    expect(store.getSnapshot().session?.turn_count).toBe(1);
    store.stop();
    // A closed stream is not fed.
    setup.gateway.state.turns.push({ id: "psess-t1/turn-9", ordinal: 9, state: "terminal", prompt: "x" });
    await setup.controller.refreshView();
    expect(store.getSnapshot().session?.turn_count).toBe(1);
  });

  it("applies the polled items to the workspace in order", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const setup = rig({ gateway });
    await setup.controller.start();
    setup.controller.select(SESSION_ID);
    const store = setup.connect();
    await flush();
    expect(store.getSnapshot().items.map((item) => item.sequence)).toEqual([1, 2, 3]);
    gateway.append([{ kind: "assistant_message", turn_id: TURN_1, text: "four" }]);
    await flush();
    expect(store.getSnapshot().items.map((item) => item.sequence)).toEqual([1, 2, 3, 4]);
    expect(store.getSnapshot().status.state).toBe("open");
    store.stop();
  });

  it("recovers from a gap with a fresh snapshot, and says so", async () => {
    const gateway = createFakeGateway({ events: [...EVENTS] });
    const setup = rig({ gateway });
    await setup.controller.start();
    setup.controller.select(SESSION_ID);
    gateway.inject(
      (call) => call.url.includes("/events"),
      {
        status: 200,
        body: {
          schema_version: "tbound-webchat-observation/v1",
          session_id: SESSION_ID,
          cursor: 3,
          next_cursor: 3,
          gap: true,
          events: [],
          items: [],
        },
      },
    );
    const store = setup.connect();
    await flush();
    expect(gateway.callsTo("GET", `/api/v1/sessions/${SESSION_ID}`).filter((call) => !call.url.includes("/events"))).toHaveLength(2);
    expect(store.getSnapshot().gapNotice).toMatchObject({ reason: "history_evicted" });
    expect(store.getSnapshot().items).toHaveLength(3);
    store.stop();
  });
});

describe("live controller: sending a prompt", () => {
  it("sends the displayed revision under a fresh key, and a later prompt gets another key", async () => {
    const setup = rig();
    await open(setup);
    expect(await setup.controller.submit("first")).toBe(true);
    await flush();
    const [first] = posts(setup.gateway, "/turns");
    expect(first?.body).toMatchObject({ expected_revision: 4, prompt: "first" });
    // The turn runs and ends; the view is read again, so the next prompt carries the new revision.
    const turn = setup.gateway.state.turns[0];
    if (turn === undefined) throw new Error("no turn");
    turn.state = "terminal";
    turn.outcome = "no_diff";
    setup.gateway.state.revision += 1;
    setup.gateway.append([{ kind: "turn_terminal", turn_id: turn.id, outcome: "no_diff" }]);
    await flush();
    expect(setup.controller.getSnapshot().view?.revision).toBe(setup.gateway.state.revision);
    expect(await setup.controller.submit("second")).toBe(true);
    const sent = posts(setup.gateway, "/turns").map((call) => call.body as { request_key: string; expected_revision: number });
    expect(sent).toHaveLength(2);
    expect(sent[1]?.request_key).not.toBe(sent[0]?.request_key);
    expect(sent[1]?.expected_revision).toBe(setup.gateway.state.revision - 1);
    setup.stores.forEach((store) => store.stop());
  });

  it("refuses to send while a turn is open, an outcome is unknown, or the prompt is blank", async () => {
    const setup = rig({ gateway: createFakeGateway({ turns: [RUNNING] }) });
    await open(setup);
    expect(await setup.controller.submit("while running")).toBe(false);
    expect(await setup.controller.submit("   ")).toBe(false);
    expect(posts(setup.gateway, "/turns")).toHaveLength(0);
    setup.stores.forEach((store) => store.stop());
  });

  it("repeats a transport failure with the same key and body, and waits between repeats", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST" && call.url.endsWith("/turns"), { reject: "connection reset" }, 2);
    expect(await setup.controller.submit("fix it")).toBe(true);
    const sent = posts(setup.gateway, "/turns");
    expect(sent).toHaveLength(3);
    expect(sent[1]?.body).toEqual(sent[0]?.body);
    expect(sent[2]?.body).toEqual(sent[0]?.body);
    expect(setup.sleeps).toEqual([...AUTO_RETRY_DELAYS_MS]);
    expect(setup.controller.getSnapshot().action.status).toBe("idle");
    setup.stores.forEach((store) => store.stop());
  });

  it("shows an unknown outcome after the repeats fail, keeps the key, and blocks a new prompt until it is settled", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST" && call.url.endsWith("/turns"), { reject: "connection reset" }, 3);
    expect(await setup.controller.submit("fix it")).toBe(false);
    const action = setup.controller.getSnapshot().action;
    expect(action.status).toBe("unknown");
    expect(action.error?.outcomeUnknown).toBe(true);
    expect(posts(setup.gateway, "/turns")).toHaveLength(3);
    const key = (posts(setup.gateway, "/turns")[0]?.body as { request_key: string }).request_key;

    expect(await setup.controller.submit("a different prompt")).toBe(false);
    expect(posts(setup.gateway, "/turns")).toHaveLength(3);

    expect(await setup.controller.retry()).toBe(true);
    const sent = posts(setup.gateway, "/turns");
    expect(sent).toHaveLength(4);
    expect((sent[3]?.body as { request_key: string }).request_key).toBe(key);
    expect(sent[3]?.body).toEqual(sent[0]?.body);
    expect(setup.controller.getSnapshot().action.status).toBe("idle");
    setup.stores.forEach((store) => store.stop());
  });

  it("treats the gateway's outcome_unknown mark as unknown and does not mint a new key to settle it", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject(
      (call) => call.method === "POST" && call.url.endsWith("/turns"),
      { status: 503, body: { error: { code: "SERVICE_UNAVAILABLE", detail: "lost after send", outcome_unknown: true } } },
      3,
    );
    await setup.controller.submit("fix it");
    expect(setup.controller.getSnapshot().action.status).toBe("unknown");
    expect(await setup.controller.retry()).toBe(true);
    expect(posts(setup.gateway, "/turns")).toHaveLength(4);
    expect((posts(setup.gateway, "/turns")[3]?.body as { request_key: string }).request_key).toBe(
      (posts(setup.gateway, "/turns")[0]?.body as { request_key: string }).request_key,
    );
    setup.stores.forEach((store) => store.stop());
  });

  it("does not repeat a refusal, and lets the user send again under a new key", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject(
      (call) => call.method === "POST" && call.url.endsWith("/turns"),
      { status: 409, body: { error: { code: "PROJECT_BUSY", detail: "another session" } } },
    );
    expect(await setup.controller.submit("fix it")).toBe(false);
    const action = setup.controller.getSnapshot().action;
    expect(action.status).toBe("failed");
    expect(action.error?.code).toBe("PROJECT_BUSY");
    expect(retryable(action.error!)).toBe(false);
    expect(posts(setup.gateway, "/turns")).toHaveLength(1);
    expect(setup.sleeps).toEqual([]);

    expect(await setup.controller.submit("fix it")).toBe(true);
    const sent = posts(setup.gateway, "/turns").map((call) => (call.body as { request_key: string }).request_key);
    expect(new Set(sent).size).toBe(2);
    setup.stores.forEach((store) => store.stop());
  });

  it("reads the session again after a stale revision and does not send the prompt again", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.state.revision += 1;
    expect(await setup.controller.submit("fix it")).toBe(false);
    expect(setup.controller.getSnapshot().action.error?.code).toBe("STALE_REVISION");
    await flush();
    expect(posts(setup.gateway, "/turns")).toHaveLength(1);
    expect(setup.controller.getSnapshot().view?.revision).toBe(5);
    expect(setup.controller.getSnapshot().action.status).toBe("failed");
    setup.stores.forEach((store) => store.stop());
  });

  it("dismisses a failed request", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST", { status: 409, body: { error: { code: "TURN_ACTIVE" } } });
    await setup.controller.submit("x");
    setup.controller.dismiss();
    expect(setup.controller.getSnapshot().action.status).toBe("idle");
  });

  it("runs a prompt once when the answer is lost on the way back: the repeat under the same key is a replay", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST" && call.url.endsWith("/turns"), { lose: "connection reset" }, 1);
    expect(await setup.controller.submit("fix it")).toBe(true);
    expect(posts(setup.gateway, "/turns")).toHaveLength(2);
    expect(setup.gateway.state.turns).toHaveLength(1);
    expect(setup.controller.getSnapshot().action.done).toEqual({ kind: "submit", replayed: true });
    setup.stores.forEach((store) => store.stop());
  });
});

describe("live controller: unresolved intent recovery", () => {
  async function lostSubmit(): Promise<Rig> {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST" && call.url.endsWith("/turns"), { lose: "lost answer" }, 3);
    expect(await setup.controller.submit("exact prompt\n")).toBe(false);
    setup.stores.forEach((store) => store.stop());
    return setup;
  }

  it("reloads an unknown submit and settles a replay with the original key and body", async () => {
    const first = await lostSubmit();
    const original = posts(first.gateway, "/turns")[0]?.body;
    const reloaded = rig({ gateway: first.gateway, storage: first.storage });
    await open(reloaded);
    expect(reloaded.controller.getSnapshot().action.status).toBe("unknown");
    expect(await reloaded.controller.retry()).toBe(true);
    expect(posts(first.gateway, "/turns").at(-1)?.body).toEqual(original);
    expect(first.gateway.state.turns).toHaveLength(1);
    expect(reloaded.controller.getSnapshot().action.done?.replayed).toBe(true);
    reloaded.stores.forEach((store) => store.stop());
    const settled = rig({ gateway: first.gateway, storage: first.storage });
    await settled.controller.start();
    expect(settled.controller.getSnapshot().action.status).toBe("idle");
    expect(await settled.controller.retry()).toBe(false);
  });

  it("keeps the unknown intent when switching away and back", async () => {
    const setup = await lostSubmit();
    const intent = setup.controller.getSnapshot().action.intent;
    setup.controller.select("psess-other");
    setup.controller.select(SESSION_ID);
    expect(setup.controller.getSnapshot().action).toMatchObject({ status: "unknown", intent });
    expect(await setup.controller.retry()).toBe(true);
    expect(posts(setup.gateway, "/turns").at(-1)?.body).toEqual(intent?.body);
  });

  it("does not erase an unknown intent on dismiss", async () => {
    const setup = await lostSubmit();
    const intent = setup.controller.getSnapshot().action.intent;
    setup.controller.dismiss();
    expect(setup.controller.getSnapshot().action).toMatchObject({ status: "unknown", intent });
    expect(await setup.controller.retry()).toBe(true);
    expect(setup.gateway.state.turns).toHaveLength(1);
  });

  it("does not send if tab storage cannot retain the intent", async () => {
    const setup = rig();
    await open(setup);
    setup.storage.set = () => { throw new Error("quota exceeded"); };
    expect(await setup.controller.submit("fix it")).toBe(false);
    expect(posts(setup.gateway, "/turns")).toHaveLength(0);
    expect(setup.controller.getSnapshot().fault).toContain("No request was sent");
    setup.stores.forEach((store) => store.stop());
  });

  it("does not send an intent that cannot be restored within the request body bound", async () => {
    const setup = rig();
    await open(setup);
    expect(await setup.controller.submit("x".repeat(64 * 1024))).toBe(false);
    expect(posts(setup.gateway, "/turns")).toHaveLength(0);
    setup.stores.forEach((store) => store.stop());
  });

  it("keeps unknown sticky after a later clean 503, including within automatic retries", async () => {
    const setup = rig();
    await open(setup);
    setup.gateway.inject((call) => call.method === "POST", { lose: "lost answer" });
    setup.gateway.inject((call) => call.method === "POST", { status: 503, body: { error: { code: "SERVICE_UNAVAILABLE" } } }, 5);
    expect(await setup.controller.submit("fix it")).toBe(false);
    expect(setup.controller.getSnapshot().action.status).toBe("unknown");
    expect(await setup.controller.retry()).toBe(false);
    expect(setup.controller.getSnapshot().action.status).toBe("unknown");
    expect(await setup.controller.submit("another prompt")).toBe(false);
    setup.stores.forEach((store) => store.stop());
  });

  it.each(["{broken", "null", "{}", '[{"sessionId":"psess-t1","intent":{"path":"https://evil.test","kind":"submit","key":"ck-x","body":{"csrf_token":"bad"}}}]', " ".repeat(1024 * 1024 + 1)])(
    "ignores corrupt or oversized stored intents safely (%#)", async (value) => {
      const setup = rig({ storage: memoryStorage({ "tbound.live.pending": value }) });
      await open(setup);
      expect(setup.controller.getSnapshot().action.status).toBe("idle");
      expect(await setup.controller.retry()).toBe(false);
      expect(posts(setup.gateway, "/turns")).toHaveLength(0);
      setup.stores.forEach((store) => store.stop());
    },
  );
});

describe("live controller: cancel", () => {
  it("cancels the open turn on its path, with the displayed revision and a fresh key", async () => {
    const setup = rig({ gateway: createFakeGateway({ turns: [RUNNING] }) });
    await open(setup);
    expect(await setup.controller.cancel()).toBe(true);
    const [cancel] = posts(setup.gateway, "/cancel");
    expect(cancel?.url).toBe(`/api/v1/sessions/${SESSION_ID}/turns/turn-1/cancel`);
    expect(cancel?.body).toMatchObject({ expected_revision: 4 });
    expect((cancel?.body as { request_key: string }).request_key).toMatch(/^ck-/);
    await flush();
    // Cancel was requested: the view says so, and a second cancel is refused.
    expect(setup.controller.getSnapshot().view?.turns?.[0]?.cancel_requested).toBe(true);
    expect(await setup.controller.cancel()).toBe(false);
    setup.stores.forEach((store) => store.stop());
  });

  it("does nothing when no turn is open", async () => {
    const setup = rig();
    await open(setup);
    expect(await setup.controller.cancel()).toBe(false);
    expect(posts(setup.gateway, "/cancel")).toHaveLength(0);
    setup.stores.forEach((store) => store.stop());
  });
});

describe("live controller: reviewing a candidate", () => {
  async function reviewing(): Promise<Rig> {
    const setup = rig({ gateway: createFakeGateway({ events: [...EVENTS], turns: [RUNNING] }) });
    await open(setup);
    setup.gateway.awaitReview("sha256:cand1");
    await flush();
    return setup;
  }

  it("reads the candidate when the turn reaches review, and shows it", async () => {
    const setup = await reviewing();
    const { candidate, view } = setup.controller.getSnapshot();
    expect(view?.turns?.[0]?.state).toBe("awaiting_review");
    expect(candidate.status).toBe("ready");
    if (candidate.status !== "ready") return;
    expect(candidate.candidate.candidateDigest).toBe("sha256:cand1");
    expect(candidate.candidate.changes[0]).toMatchObject({ path: "a.go", op: "modify", diff: "-a\n+b\n" });
    setup.stores.forEach((store) => store.stop());
  });

  it("publishes and discards exactly the candidate that is displayed, even if the record has moved on", async () => {
    const setup = await reviewing();
    const shown = setup.controller.getSnapshot().candidate;
    if (shown.status !== "ready") throw new Error("no candidate shown");
    // The service records another candidate and the page reads the new view, while a click on the diff it showed is in flight.
    const turn = setup.gateway.state.turns[0];
    if (turn === undefined) throw new Error("no turn");
    turn.candidate_digest = "sha256:cand2";
    setup.gateway.state.candidate = { digest: "sha256:cand2", changes: [{ path: "b.go", op: "add", diff: "+x\n" }] };
    setup.gateway.state.revision += 1;
    await setup.controller.refreshView();
    expect(setup.controller.getSnapshot().view?.turns?.[0]?.candidate_digest).toBe("sha256:cand2");

    expect(await setup.controller.resolve(shown.candidate, "publish")).toBe(false);
    const [attempt] = posts(setup.gateway, "/resolve");
    expect(attempt?.body).toMatchObject({ candidate_digest: "sha256:cand1", decision: "publish" });
    expect(setup.controller.getSnapshot().action.error?.code).toBe("CANDIDATE_MISMATCH");
    expect(setup.controller.getSnapshot().action.status).toBe("failed");

    // The page has read the new candidate by now, and the next decision is bound to that one.
    await flush();
    const next = setup.controller.getSnapshot().candidate;
    expect(next.status === "ready" && next.candidate.candidateDigest).toBe("sha256:cand2");
    if (next.status !== "ready") return;
    expect(await setup.controller.resolve(next.candidate, "discard")).toBe(true);
    const sent = posts(setup.gateway, "/resolve");
    expect(sent[1]?.body).toMatchObject({ candidate_digest: "sha256:cand2", decision: "discard" });
    expect(new Set(sent.map((call) => (call.body as { request_key: string }).request_key)).size).toBe(2);
    setup.stores.forEach((store) => store.stop());
  });

  it("drops the candidate once a decision is recorded", async () => {
    const setup = await reviewing();
    const shown = setup.controller.getSnapshot().candidate;
    if (shown.status !== "ready") throw new Error("no candidate shown");
    expect(await setup.controller.resolve(shown.candidate, "publish")).toBe(true);
    await flush();
    expect(setup.controller.getSnapshot().candidate.status).toBe("none");
    expect(setup.controller.getSnapshot().view?.turns?.[0]?.resolution).toBe("publish");
    setup.stores.forEach((store) => store.stop());
  });

  it("says when the candidate cannot be read, and reads it again on request", async () => {
    const setup = rig({ gateway: createFakeGateway({ events: [...EVENTS], turns: [RUNNING] }) });
    await open(setup);
    setup.gateway.inject((call) => call.url.endsWith("/candidate"), { status: 409, body: { error: { code: "REVIEW_NOT_PENDING", detail: "closed" } } });
    setup.gateway.awaitReview("sha256:cand1");
    await flush();
    expect(setup.controller.getSnapshot().candidate).toMatchObject({ status: "failed", message: "REVIEW_NOT_PENDING: closed" });
    await setup.controller.loadCandidate();
    expect(setup.controller.getSnapshot().candidate.status).toBe("ready");
    setup.stores.forEach((store) => store.stop());
  });

  it("explains the service's explicit refusal of an oversized complete preview", async () => {
    const setup = rig({ gateway: createFakeGateway({ turns: [RUNNING] }) });
    await open(setup);
    setup.gateway.inject((call) => call.url.endsWith("/candidate"), { status: 413, body: { error: { code: "CANDIDATE_TOO_LARGE" } } });
    setup.gateway.awaitReview("sha256:cand1");
    await flush();
    expect(setup.controller.getSnapshot().candidate).toMatchObject({
      status: "failed", message: expect.stringContaining("1 MiB response limit"),
    });
    expect(setup.controller.getSnapshot().candidate).toMatchObject({ message: expect.stringContaining("No paths were omitted") });
    setup.stores.forEach((store) => store.stop());
  });
});

describe("live controller: sessions", () => {
  it("creates a session under a fresh key, repeats it with the same key, and opens it", async () => {
    const setup = rig();
    await setup.controller.start();
    setup.gateway.inject((call) => call.method === "POST" && call.url === "/api/v1/sessions", { reject: "reset" }, 1);
    const ok = await setup.controller.createSession({ projectId: "proj", providerProfile: "prof", publicationMode: "review", hostCalls: 5, tokens: 500, durationSeconds: 30, readOnly: false });
    expect(ok).toBe(true);
    const sent = posts(setup.gateway, "/api/v1/sessions").map((call) => call.body);
    expect(sent).toHaveLength(2);
    expect(sent[1]).toEqual(sent[0]);
    expect(setup.controller.getSnapshot().activeId).toBe("psess-new");
    expect(setup.storage.data.get(ACTIVE_SESSION_KEY)).toBe("psess-new");
    expect(setup.controller.getSnapshot().sessions?.map((session) => session.id)).toContain("psess-new");
  });

  it("drops a view that belongs to the session the user left", async () => {
    const setup = rig();
    await open(setup);
    setup.controller.select("psess-new");
    expect(setup.controller.getSnapshot().view).toBeNull();
    await setup.controller.refreshView();
    expect(setup.controller.getSnapshot().view).toBeNull();
    setup.stores.forEach((store) => store.stop());
  });

  it("collapses overlapping view reads", async () => {
    const setup = rig();
    await open(setup);
    const before = setup.gateway.callsTo("GET", `/api/v1/sessions/${SESSION_ID}`).filter((call) => !call.url.includes("/events")).length;
    await Promise.all([setup.controller.refreshView(), setup.controller.refreshView(), setup.controller.refreshView()]);
    const after = setup.gateway.callsTo("GET", `/api/v1/sessions/${SESSION_ID}`).filter((call) => !call.url.includes("/events")).length;
    expect(after - before).toBeLessThanOrEqual(2);
    setup.stores.forEach((store) => store.stop());
  });

  it("records that the pairing ended when the snapshot of the open session is refused with 401", async () => {
    const setup = rig();
    await setup.controller.start();
    setup.controller.select(SESSION_ID);
    setup.gateway.inject((call) => call.method === "GET" && call.url === `/api/v1/sessions/${SESSION_ID}`, { status: 401, body: { error: { code: "UNAUTHENTICATED" } } });
    const store = setup.connect();
    await flush();
    expect(setup.controller.getSnapshot().authLost).toBe(true);
    expect(store.getSnapshot().error).toContain("UNAUTHENTICATED");
    store.stop();
  });

  it("records that the pairing ended when the gateway answers 401", async () => {
    const setup = rig();
    setup.gateway.inject((call) => call.url === "/api/v1/sessions", { status: 401, body: { error: { code: "UNAUTHENTICATED" } } });
    await setup.controller.start();
    expect(setup.controller.getSnapshot().authLost).toBe(true);
    expect(setup.controller.getSnapshot().fault).toContain("UNAUTHENTICATED");
  });
});
