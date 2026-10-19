import { expect, it } from "vitest";
import { CONTRACT_VERSION } from "../core/contract";
import { messageItem, snapshotOf } from "../core/testing/builders";
import { fakeWorkspace, flush } from "../test-support/fakes";
import { createWorkspaceStore } from "./workspaceStore";

it("local actions retain immutable item snapshots while publishing UI changes", async () => {
  const fake = fakeWorkspace(snapshotOf({ items: [messageItem({ id: "m1", sequence: 1 })] }));
  const store = createWorkspaceStore(fake.deps);
  store.start(); await flush(); fake.sources[0]?.open();
  const original = store.getSnapshot();
  let notifications = 0;
  store.subscribe(() => notifications++);
  const a = original.actions;
  for (const action of [() => a.setView("trajectory"), () => a.setQuery("hello"), () => a.setGroup("tool"),
    () => a.setFilter("denied"), () => a.selectRecord("m1"), () => a.clearRecord(), () => a.repinTail(true)]) {
    const before = store.getSnapshot(); const calls = notifications;
    action(); const after = store.getSnapshot();
    expect(after).not.toBe(before);
    expect(after.items).toBe(original.items);
    expect(after.itemsById).toBe(original.itemsById);
    expect(notifications).toBe(calls + 1);
  }
  expect(store.getSnapshot()).toMatchObject({ view: "trajectory", query: "hello", group: "tool", filter: "denied", selectedId: null });
  expect(fake.fetchUrls).toHaveLength(1);
  expect(original.items.map(i => i.id)).toEqual(["m1"]);
  store.stop();
});

it("stream append and capacity eviction replace both collections and preserve old snapshots", async () => {
  const items = Array.from({ length: 512 }, (_, i) => messageItem({ id: `m${i + 1}`, sequence: i + 1 }));
  const fake = fakeWorkspace(snapshotOf({ items }));
  const store = createWorkspaceStore(fake.deps);
  store.start(); await flush(); fake.sources[0]?.open();
  const before = store.getSnapshot();
  fake.sources[0]?.emit("item", { schema_version: CONTRACT_VERSION, cursor: "e1:513", kind: "item", item: messageItem({ id: "m513", sequence: 513 }) });
  const after = store.getSnapshot();
  expect(after.items).not.toBe(before.items); expect(after.itemsById).not.toBe(before.itemsById);
  expect(after.items).toHaveLength(512); expect(after.itemsById.has("m1")).toBe(false);
  expect(before.itemsById.has("m1")).toBe(true); expect(before.items).toHaveLength(512);
  expect(after.itemsById.has("m513")).toBe(true);
  store.stop();
});

it("a same-sized replacement snapshot never reuses the prior item collections", async () => {
  const fake = fakeWorkspace(snapshotOf({ items: [messageItem({ id: "first", sequence: 1 })] }));
  const store = createWorkspaceStore(fake.deps); store.start(); await flush();
  const before = store.getSnapshot();
  fake.reply = { ok: true, status: 200, body: snapshotOf({ items: [messageItem({ id: "replacement", sequence: 1 })] }) };
  store.start(); await flush();
  const after = store.getSnapshot();
  expect(after.items).not.toBe(before.items); expect(after.itemsById).not.toBe(before.itemsById);
  expect(after.items.map(i => i.id)).toEqual(["replacement"]);
  expect(before.items.map(i => i.id)).toEqual(["first"]);
  store.stop();
});
