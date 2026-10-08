import assert from "node:assert/strict";
import { test } from "node:test";
import { Duplex } from "node:stream";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import {
  createAgentSession,
  ModelRuntime,
  SessionManager,
  SettingsManager,
} from "@earendil-works/pi-coding-agent";
import type { JsonObject, Model } from "@earendil-works/pi-ai";
import { createProxyTools, type ProposalSender, type ToolProposal } from "./proxy-tools.ts";
import { LockedResourceLoader } from "./locked-resource-loader.ts";
import {
  BROKER_API,
  BROKER_CHANNEL_VERSION,
  BROKER_MODEL_ID,
  BROKER_PROVIDER_ID,
  createBrokerProvider,
} from "./broker-provider.ts";

const maxFrame = 1 << 20;

class LinkedDuplex extends Duplex {
  peer?: LinkedDuplex;

  _read(): void {}

  _write(chunk: Buffer | string, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
    if (!this.peer || this.peer.destroyed) {
      callback(new Error("fixture bridge peer closed"));
      return;
    }
    this.peer.push(Buffer.isBuffer(chunk) ? Buffer.from(chunk) : Buffer.from(chunk));
    callback();
  }

  _final(callback: (error?: Error | null) => void): void {
    this.peer?.push(null);
    callback();
  }
}

function duplexPair(): [LinkedDuplex, LinkedDuplex] {
  const left = new LinkedDuplex();
  const right = new LinkedDuplex();
  left.peer = right;
  right.peer = left;
  return [left, right];
}

class FrameReader {
  private readonly iterator: AsyncIterator<Buffer | string>;
  private buffered = Buffer.alloc(0);

  constructor(stream: Duplex) {
    this.iterator = stream[Symbol.asyncIterator]();
  }

  async next(): Promise<Record<string, unknown>> {
    const prefix = await this.exactly(4);
    const length = prefix.readUInt32BE(0);
    assert.ok(length > 0 && length <= maxFrame);
    const bytes = await this.exactly(length);
    const value: unknown = JSON.parse(bytes.toString("utf8"));
    assert.ok(value && typeof value === "object" && !Array.isArray(value));
    return value as Record<string, unknown>;
  }

  private async exactly(count: number): Promise<Buffer> {
    while (this.buffered.length < count) {
      const item = await this.iterator.next();
      if (item.done) throw new Error("fixture bridge closed before complete frame");
      const chunk = typeof item.value === "string" ? Buffer.from(item.value) : item.value;
      this.buffered = this.buffered.length === 0 ? Buffer.from(chunk) : Buffer.concat([this.buffered, chunk]);
    }
    const result = this.buffered.subarray(0, count);
    this.buffered = this.buffered.subarray(count);
    return result;
  }
}

async function writeFrame(stream: Duplex, frame: unknown): Promise<void> {
  const body = Buffer.from(JSON.stringify(frame), "utf8");
  const prefix = Buffer.allocUnsafe(4);
  prefix.writeUInt32BE(body.length, 0);
  await new Promise<void>((resolvePromise, reject) => {
    stream.write(Buffer.concat([prefix, body]), (error?: Error | null) => error ? reject(error) : resolvePromise());
  });
}

function makeTurn(sequence: number, responseID: string, toolCall?: { id: string; name: string; arguments: JsonObject }) {
  return {
    schema_version: BROKER_CHANNEL_VERSION,
    response_id: responseID,
    model: BROKER_MODEL_ID,
    created: 1_790_000_000,
    generation: "fixture-generation-g0",
    sequence,
    assistant_text: toolCall ? "Reading the fixture." : "The fixture was read.",
    finish_reason: toolCall ? "tool_calls" : "stop",
    usage: { prompt_tokens: 9, completion_tokens: 4, total_tokens: 13, cost: 0 },
    ...(toolCall ? { tool_call: toolCall } : {}),
    journal_sequence: sequence + 1,
    journal_hash: sequence.toString(16).padStart(64, "0"),
  };
}

test("real Pi SDK consumes Go broker projections without forwarding SDK history", async () => {
  const [clientDuplex, serverDuplex] = duplexPair();
  const providerBridge = createBrokerProvider(clientDuplex);
  const requests: Record<string, unknown>[] = [];
  const reader = new FrameReader(serverDuplex);
  let serverError: unknown;
  const server = (async () => {
    for (let index = 0; index < 2; index += 1) {
      const request = await reader.next();
      requests.push(request);
      assert.deepEqual(Object.keys(request).sort(), ["kind", "request_id", "sequence", "version"]);
      assert.equal(request.version, BROKER_CHANNEL_VERSION);
      assert.equal(request.kind, "next");
      assert.equal(request.sequence, index + 1);
      const turn = index === 0
        ? makeTurn(1, "response-sdk-1", { id: "provider-tool-1", name: "read", arguments: { path: "fixture.txt" } })
        : makeTurn(2, "response-sdk-2");
      await writeFrame(serverDuplex, {
        version: BROKER_CHANNEL_VERSION,
        sequence: index + 1,
        kind: "turn",
        request_id: request.request_id,
        turn,
      });
    }
  })().catch((error: unknown) => { serverError = error; });

  const credentials = {
    async read() { return undefined; },
    async list() { return []; },
    async modify(_providerID: string, update: (current: undefined) => Promise<undefined>) { return update(undefined); },
    async delete() {},
  };
  const modelRuntime = await ModelRuntime.create({
    credentials,
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  modelRuntime.registerNativeProvider(providerBridge.provider);
  assert.deepEqual(modelRuntime.getRegisteredProviderIds(), [BROKER_PROVIDER_ID]);
  const model = modelRuntime.getModel(BROKER_PROVIDER_ID, BROKER_MODEL_ID) as Model<typeof BROKER_API> | undefined;
  assert.ok(model);
  const cwd = await mkdtemp(join(tmpdir(), "tbound-broker-provider-"));
  let proposals = 0;
  const sender: ProposalSender = {
    async send(proposal: ToolProposal) {
      proposals += 1;
      assert.equal(proposal.tool, "read");
      assert.deepEqual(proposal.arguments, { path: "fixture.txt" });
      return {
        schema_version: "tbound-result/v1",
        response_id: { issuer: "fixture-response", opaque: "response-sdk-1" },
        tool_call_id: proposal.tool_call_id,
        tool: proposal.tool,
        sequence: 1,
        verdict: "ALLOW",
        reason_code: "policy_rule_allow",
        policy_digest: "fixture-policy/v1",
        output: { source: "executor-fixture", content: "fixture data" },
      };
    },
  };
  const settingsManager = SettingsManager.inMemory({
    defaultTools: [], extensions: [], packages: [], skills: [], prompts: [], themes: [],
    enableSkillCommands: false, enableAnalytics: false, enableInstallTelemetry: false,
    cacheWarming: "off", compaction: { enabled: false }, retry: { enabled: false },
  });
  const sessionManager = SessionManager.inMemory(cwd);
  const resourceLoader = new LockedResourceLoader();
  const created = await createAgentSession({
    cwd,
    agentDir: resolve(cwd, ".tbound-broker-provider-test"),
    modelRuntime,
    model,
    thinkingLevel: "off",
    settingsManager,
    sessionManager,
    resourceLoader,
    noTools: "builtin",
    tools: ["read", "write", "edit", "bash"],
    customTools: createProxyTools(sender),
  });

  try {
    await created.session.prompt("UNTRUSTED_CONTEXT_SENTINEL");
    assert.equal(proposals, 1, "Pi SDK did not execute the captured proxy tool");
    assert.deepEqual(requests.map((request) => Object.keys(request).sort()), [
      ["kind", "request_id", "sequence", "version"],
      ["kind", "request_id", "sequence", "version"],
    ]);
    assert.equal(JSON.stringify(requests).includes("UNTRUSTED_CONTEXT_SENTINEL"), false,
      "Pi history/prompt crossed the inherited provider channel");
    assert.equal(JSON.stringify(requests).includes("api_key"), false);
    assert.equal(JSON.stringify(requests).includes("headers"), false);
  } finally {
    created.session.dispose();
    providerBridge.client.close();
    serverDuplex.destroy();
    await server;
    await rm(cwd, { recursive: true, force: true });
  }
  if (serverError) throw serverError;
});

test("Go turn projection must match its fixed model and finish/call shape", async () => {
  const [clientDuplex, serverDuplex] = duplexPair();
  const runtime = createBrokerProvider(clientDuplex);
  const reader = new FrameReader(serverDuplex);
  const requestPromise = reader.next();
  const model = runtime.model;
  const stream = runtime.provider.stream(model, { messages: [] } as never, {});
  const request = await requestPromise;
  await writeFrame(serverDuplex, {
    version: BROKER_CHANNEL_VERSION,
    sequence: 1,
    kind: "turn",
    request_id: request.request_id,
    turn: makeTurn(1, "response-1", { id: "call-1", name: "read", arguments: { path: "x" } }),
  });
  const events: string[] = [];
  for await (const event of stream) events.push(event.type);
  assert.deepEqual(events, ["start", "text_start", "text_delta", "text_end", "toolcall_start", "toolcall_delta", "toolcall_end", "done"]);
  assert.equal(request.kind, "next");
  runtime.client.close();
  serverDuplex.destroy();
});

test("provider cancellation sends only the matching bounded cancel frame", async () => {
  const [clientDuplex, serverDuplex] = duplexPair();
  const client = createBrokerProvider(clientDuplex).client;
  const reader = new FrameReader(serverDuplex);
  const abort = new AbortController();
  const turn = client.next(abort.signal);
  const next = await reader.next();
  assert.equal(next.kind, "next");
  abort.abort();
  const cancel = await reader.next();
  assert.equal(cancel.kind, "cancel");
  assert.equal(cancel.request_id, next.request_id);
  assert.equal(cancel.sequence, 2);
  assert.deepEqual(Object.keys(cancel).sort(), ["kind", "request_id", "sequence", "version"]);
  await writeFrame(serverDuplex, {
    version: BROKER_CHANNEL_VERSION,
    sequence: 1,
    kind: "error",
    request_id: next.request_id,
    error: "provider canceled",
  });
  await assert.rejects(turn, { name: "AbortError" });
  serverDuplex.destroy();
});
