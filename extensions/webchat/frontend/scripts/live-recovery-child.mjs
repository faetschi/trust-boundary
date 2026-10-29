import { randomFillSync } from "node:crypto";
import { createServer } from "vite";

const job = await new Promise((resolve) => process.once("message", resolve));
const requests = new Map();
let requestId = 0;
process.on("message", (reply) => {
  const request = requests.get(reply.id);
  if (!request) return;
  requests.delete(reply.id);
  if (reply.error) request.reject(new TypeError(reply.error));
  else request.resolve({ ok: reply.ok, status: reply.status, json: async () => reply.body });
});

const server = await createServer({ configFile: false, logLevel: "error", server: { middlewareMode: true, hmr: false, watch: null } });
try {
  const { createLiveController } = await server.ssrLoadModule("/src/live/controller.ts");
  const { createLiveClient } = await server.ssrLoadModule("/src/live/client.ts");
  const { fakeWorkspace } = await server.ssrLoadModule("/src/test-support/fakes.ts");
  const { snapshotOf } = await server.ssrLoadModule("/src/core/testing/builders.ts");
  const data = new Map(job.storage);
  const controller = createLiveController({
    client: createLiveClient({
      fetch: (url, init) => new Promise((resolve, reject) => {
        const id = ++requestId;
        requests.set(id, { resolve, reject });
        process.send({ type: "fetch", id, url, init });
      }),
      nowMs: Date.now,
      randomBytes: randomFillSync,
    }),
    base: fakeWorkspace(snapshotOf()).deps,
    storage: { get: (key) => data.get(key) ?? null, set: (key, value) => data.set(key, value) },
    sleep: async () => {},
  });
  await controller.start();
  if (job.operation !== "create") {
    controller.select(job.sessionId);
    await controller.refreshView();
  }
  const before = controller.getSnapshot().action;
  let accepted;
  if (job.operation === "create") accepted = await controller.createSession({ projectId: "demo", providerProfile: "prof", publicationMode: "review", hostCalls: 5, tokens: 500, durationSeconds: 30, readOnly: false });
  else if (job.operation === "submit") accepted = await controller.submit("exact prompt\n");
  else if (job.operation === "cancel") accepted = await controller.cancel();
  else if (job.operation === "resolve") {
    await controller.loadCandidate();
    const shown = controller.getSnapshot().candidate;
    if (shown.status !== "ready") throw new Error("no candidate shown");
    accepted = await controller.resolve(shown.candidate, job.decision);
  } else if (job.operation === "retry") accepted = await controller.retry();
  else throw new Error(`unknown operation ${job.operation}`);
  await controller.refreshView();
  process.send({ type: "result", pid: process.pid, accepted, before, after: controller.getSnapshot().action, activeId: controller.getSnapshot().activeId, storage: [...data] });
} finally {
  await server.close();
  process.disconnect();
}
