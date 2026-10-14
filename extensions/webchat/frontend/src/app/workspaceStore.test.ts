import { describe, expect, it } from "vitest";
import { CONTRACT_VERSION, GAP_VERSION } from "../core/contract";
import { messageItem, snapshotOf } from "../core/testing/builders";
import { createWorkspaceStore } from "./workspaceStore";
import { flush, fakeWorkspace } from "../test-support/fakes";

function itemEvent(sequence: number, epoch = "e1") {
  return {
    schema_version: CONTRACT_VERSION,
    cursor: `${epoch}:${sequence}`,
    kind: "item",
    item: messageItem({ id: `m${sequence}`, sequence, text: `text ${sequence}` }),
  };
}

function twoItemSnapshot() {
  return snapshotOf({ items: [messageItem({ id: "m1", sequence: 1 }), messageItem({ id: "m2", sequence: 2 })] });
}

describe("workspace store: snapshot-first stream lifecycle", () => {
  it("fetches the snapshot first and opens the stream from the snapshot's cursor", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    expect(fake.fetchUrls).toEqual(["/api/v1/snapshot"]);
    expect(fake.sources).toHaveLength(0);
    await flush();
    expect(fake.sources).toHaveLength(1);
    expect(fake.sources[0]?.url).toBe("/api/v1/events?cursor=e1%3A2");
    expect(store.getSnapshot().items.map((item) => item.id)).toEqual(["m1", "m2"]);
  });

  it("reads connecting until the stream opens, and connected only after it does", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    expect(store.getSnapshot().status.label).toBe("connecting…");
    fake.sources[0]?.open();
    expect(store.getSnapshot().status.label).toBe("connected");
    expect(store.announcer.getSnapshot().text).toBe("Stream connected.");
  });

  it("applies in-order items and advances the cursor", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    fake.sources[0]?.emit("item", itemEvent(3));
    const model = store.getSnapshot();
    expect(model.items.map((item) => item.id)).toEqual(["m1", "m2", "m3"]);
    expect(model.cursor).toBe("e1:3");
  });

  it("reconnects after a stream error through a timer, and never shows connected meanwhile", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    fake.sources[0]?.fail();
    expect(store.getSnapshot().status.state).toBe("reconnecting");
    expect(store.getSnapshot().status.label).toBe("reconnecting…");
    expect(fake.sources[0]?.closed).toBe(true);
    expect(fake.armedDelays()).toHaveLength(1);
    fake.fireTimer();
    await flush();
    expect(fake.fetchUrls).toHaveLength(2);
    expect(fake.sources).toHaveLength(2);
    expect(store.getSnapshot().status.label).toBe("connecting…");
  });

  it("a server gap recovers from a fresh snapshot and says so, without inventing a success", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    fake.sources[0]?.emit("gap", { schema_version: GAP_VERSION, reason: "history_evicted", epoch: "e1", latest: 9 });
    await flush();
    expect(fake.fetchUrls).toHaveLength(2);
    const model = store.getSnapshot();
    expect(model.gapNotice).not.toBeNull();
    expect(store.announcer.getSnapshot().text).toMatch(/^Continuity gap \(history evicted\)\. Recovered from a snapshot; no success is inferred\./);
  });

  it("goes offline without a retry timer, and resumes at once when the network returns", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    fake.setOnline(false);
    store.goOffline();
    expect(store.getSnapshot().status.label).toBe("offline");
    expect(fake.armedDelays()).toHaveLength(0);
    fake.setOnline(true);
    store.goOnline();
    await flush();
    expect(fake.fetchUrls).toHaveLength(2);
  });

  it("stop closes the stream, and a later start opens a fresh one", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    store.stop();
    expect(fake.sources[0]?.closed).toBe(true);
    store.start();
    await flush();
    expect(fake.sources).toHaveLength(2);
    expect(fake.sources[1]?.closed).toBe(false);
  });
});

describe("workspace store: immutable published model", () => {
  it("replaces the snapshot only when something changed", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    const before = store.getSnapshot();
    expect(store.getSnapshot()).toBe(before);
    store.start();
    await flush();
    fake.sources[0]?.open();
    const after = store.getSnapshot();
    expect(after).not.toBe(before);
    expect(store.getSnapshot()).toBe(after);
  });

  it("keeps an earlier model's items unchanged when a later item arrives", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    fake.sources[0]?.open();
    const earlier = store.getSnapshot();
    fake.sources[0]?.emit("item", itemEvent(3));
    expect(earlier.items).toHaveLength(2);
    expect(store.getSnapshot().items).toHaveLength(3);
  });

  it("notifies subscribers on change and stops after unsubscribe", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    let calls = 0;
    const unsubscribe = store.subscribe(() => {
      calls += 1;
    });
    store.start();
    await flush();
    fake.sources[0]?.open();
    const seen = calls;
    expect(seen).toBeGreaterThan(0);
    unsubscribe();
    fake.sources[0]?.emit("item", itemEvent(3));
    expect(calls).toBe(seen);
  });
});

describe("workspace store: presentation actions", () => {
  it("sets the view, query and group, and selects a record as a toggle", async () => {
    const fake = fakeWorkspace(twoItemSnapshot());
    const store = createWorkspaceStore(fake.deps);
    store.start();
    await flush();
    const { actions } = store.getSnapshot();
    actions.setView("trajectory");
    actions.setQuery("curl");
    actions.setGroup("tool");
    actions.setFilter("denied");
    expect(store.getSnapshot()).toMatchObject({ view: "trajectory", query: "curl", group: "tool", filter: "denied" });
    actions.selectRecord("m2");
    expect(store.getSnapshot().selectedId).toBe("m2");
    actions.selectRecord("m2");
    expect(store.getSnapshot().selectedId).toBeNull();
    actions.selectRecord("m1");
    actions.clearRecord();
    expect(store.getSnapshot().selectedId).toBeNull();
  });
});
