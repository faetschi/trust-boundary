import { fork } from "node:child_process";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import { createFakeGateway, SESSION_ID, TURN_1 } from "../test-support/liveGateway";
import { PENDING_ACTIONS_KEY } from "./storedIntent";

function runPage(gateway, job) {
  return new Promise((resolve, reject) => {
    const child = fork(fileURLToPath(new URL("../../scripts/live-recovery-child.mjs", import.meta.url)), { silent: true });
    const timeout = setTimeout(() => { child.kill(); }, 10_000);
    let result;
    let stderr = "";
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("message", async (message) => {
      if (message.type === "result") {
        result = message;
        return;
      }
      try {
        const reply = await gateway.fetch(message.url, message.init);
        child.send({ id: message.id, ok: reply.ok, status: reply.status, body: await reply.json() });
      } catch (error) {
        child.send({ id: message.id, error: String(error) });
      }
    });
    child.on("exit", (code) => {
      clearTimeout(timeout);
      if (code === 0 && result) resolve(result);
      else reject(new Error(`recovery process exited ${code}: ${stderr}`));
    });
    child.send({ sessionId: SESSION_ID, storage: [], ...job });
  });
}

it.each([
  { operation: "create", path: "/api/v1/sessions" },
  { operation: "cancel", path: "/cancel" },
  { operation: "resolve", decision: "publish", path: "/resolve" },
  { operation: "resolve", decision: "discard", path: "/resolve" },
  { operation: "submit", path: "/turns" },
])("recovers $operation in a fresh process after settlement without creating new work (%#)", async ({ operation, decision, path }) => {
  const gateway = createFakeGateway({ turns: operation === "cancel" || operation === "resolve"
    ? [{ id: TURN_1, ordinal: 1, state: "linked", prompt: "fix it" }] : [] });
  if (operation === "resolve") gateway.awaitReview("sha256:cand1");
  gateway.inject((call) => call.method === "POST" && call.url.endsWith(path), { lose: "lost answer" }, 3);
  const first = await runPage(gateway, { operation, decision });
  expect(first.accepted).toBe(false);
  expect(first.after.status).toBe("unknown");
  const original = gateway.callsTo("POST", path)[0];
  expect(gateway.callsTo("POST", path)).toHaveLength(3);
  const turn = gateway.state.turns[0];
  if (turn) {
    turn.state = "terminal";
    turn.outcome = operation === "submit" ? "no_diff" : operation === "cancel" ? "cancelled" : decision === "publish" ? "published" : "discarded";
    gateway.state.candidate = null;
    gateway.state.revision += 1;
  }
  const eventCount = gateway.state.events.length;
  const restored = await runPage(gateway, { operation: "retry", storage: first.storage });
  expect(restored.pid).not.toBe(first.pid);
  expect(restored.before.status).toBe("unknown");
  expect(restored.before.intent).toEqual(first.after.intent);
  expect(restored.accepted).toBe(true);
  expect(restored.after.status).toBe("idle");
  if (operation === "create") expect(restored.activeId).toBe("psess-new");
  else expect(restored.after.done).toEqual({ kind: operation, replayed: true });
  expect(new Map(restored.storage).get(PENDING_ACTIONS_KEY)).toBe("[]");
  expect(gateway.callsTo("POST", path)).toHaveLength(4);
  expect(gateway.callsTo("POST", path).at(-1)?.url).toBe(original.url);
  expect(gateway.callsTo("POST", path).at(-1)?.body).toEqual(original.body);
  expect(gateway.state.sessions).toHaveLength(operation === "create" ? 1 : 0);
  expect(gateway.state.turns).toHaveLength(operation === "create" ? 0 : 1);
  expect(gateway.state.events).toHaveLength(eventCount);
  if (operation === "submit") {
    const copiedTab = await runPage(gateway, { operation: "retry", storage: structuredClone(first.storage) });
    expect(copiedTab.pid).not.toBe(restored.pid);
    expect(copiedTab.accepted).toBe(true);
    expect(copiedTab.after.done).toEqual({ kind: "submit", replayed: true });
    expect(gateway.callsTo("POST", path)).toHaveLength(5);
    expect(gateway.callsTo("POST", path).at(-1)?.body).toEqual(original.body);
    expect(gateway.state.turns).toHaveLength(1);
    expect(gateway.state.events).toHaveLength(eventCount);
  }
}, 20_000);
