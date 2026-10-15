import assert from "node:assert/strict";
import { test } from "node:test";
import { Duplex } from "node:stream";
import { createAssistantMessageEventStream, type JsonObject } from "@earendil-works/pi-ai";
import {
  BROKER_API,
  BROKER_CHANNEL_VERSION,
  BROKER_MODEL_ID,
  BROKER_PROVIDER_ID,
  createBrokerProvider,
} from "./broker-provider.ts";

const maxFrame = 1 << 20;
// Mirrors the Go broker's ApprovedModel; supervisor piinstall's
// TestPinnedModelsMatchBrokerApprovedModel checks BROKER_MODEL_ID against it.
const GO_APPROVED_MODEL = "deepseek-v4.1-flash";

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
    model: GO_APPROVED_MODEL,
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

test("adapter's registered model is the broker-approved model", () => {
  assert.equal(BROKER_MODEL_ID, GO_APPROVED_MODEL);
});

test("Go turn projection must match its fixed model and finish/call shape", async () => {
  const [clientDuplex, serverDuplex] = duplexPair();
  const runtime = createBrokerProvider(clientDuplex, { createAssistantMessageEventStream });
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
  const client = createBrokerProvider(clientDuplex, { createAssistantMessageEventStream }).client;
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
