/**
 * Public Pi AI Provider backed by the inherited, Go-owned conversation FD.
 * It deliberately ignores Pi's TranscriptContext and request options: user
 * prompts must be separately admitted by trusted session control, while tool
 * results/history are returned to Go only through the existing executor path.
 */
import { randomUUID } from "node:crypto";
import { Socket } from "node:net";
import type { Duplex } from "node:stream";
import {
  createAssistantMessageEventStream,
  type Api,
  type AssistantMessage,
  type AssistantMessageEventStream,
  type JsonObject,
  type JsonValue,
  type Model,
  type Provider,
  type SimpleStreamOptions,
  type StreamOptions,
  type ToolCall,
  type TranscriptContext,
  type Usage,
} from "@earendil-works/pi-ai";

export const BROKER_PROVIDER_ID = "tbound-go-broker" as const;
export const BROKER_API = "tbound-provider-bridge/v1" as const;
export const BROKER_MODEL_ID = "nvidia/nemotron-3.5-lightning:free" as const;
export const BROKER_CHANNEL_VERSION = "tbound-provider-bridge/v1" as const;
export const MAX_PROVIDER_FRAME_BYTES = 1 << 20;

const toolNames = new Set(["read", "write", "edit", "bash"]);

export type BrokerTurn = {
  schema_version: typeof BROKER_CHANNEL_VERSION;
  response_id: string;
  model: typeof BROKER_MODEL_ID;
  created: number;
  generation: string;
  sequence: number;
  assistant_text: string;
  finish_reason: "stop" | "tool_calls";
  usage: unknown;
  tool_call?: { id: string; name: string; arguments: JsonObject };
  journal_sequence: number;
  journal_hash: string;
};

type RequestFrame = {
  version: typeof BROKER_CHANNEL_VERSION;
  sequence: number;
  kind: "next" | "cancel";
  request_id: string;
};

type ResponseFrame = {
  version: typeof BROKER_CHANNEL_VERSION;
  sequence: number;
  kind: "turn" | "error";
  request_id: string;
  turn?: BrokerTurn;
  error?: string;
};

/**
 * Request-only RPC client. The protocol has no method capable of sending a
 * prompt, transcript, model, headers, key, or tool result to Go.
 */
export class BrokerChannelClient {
  private readonly channel: Duplex;
  private readonly reader: AsyncIterator<Buffer | string>;
  private buffered = Buffer.alloc(0);
  private writeSequence = 1;
  private readSequence = 1;
  private busy = false;
  private closed = false;

  constructor(channel: Duplex) {
    this.channel = channel;
    this.reader = channel[Symbol.asyncIterator]();
  }

  async next(signal?: AbortSignal): Promise<BrokerTurn> {
    if (this.closed) throw new Error("provider bridge channel is closed");
    if (this.busy) throw new Error("overlapping provider turns are forbidden");
    if (signal?.aborted) throw abortError();
    this.busy = true;
    const requestID = randomUUID();
    let aborted = false;
    let cancelWrite: Promise<void> | undefined;
    const onAbort = (): void => {
      aborted = true;
      cancelWrite ??= this.writeRequest("cancel", requestID).catch((error: unknown) => {
        this.closed = true;
        this.channel.destroy(error instanceof Error ? error : undefined);
      });
    };
    signal?.addEventListener("abort", onAbort, { once: true });
    try {
      await this.writeRequest("next", requestID);
      const response = await this.readResponse();
      if (aborted) {
        await cancelWrite;
        this.closed = true;
        throw abortError();
      }
      if (response.request_id !== requestID) {
        this.closed = true;
        throw new Error("provider response does not match the active request");
      }
      if (response.kind === "error") {
        this.closed = true;
        throw new Error("Go provider conversation failed; turn withheld");
      }
      if (!response.turn) {
        this.closed = true;
        throw new Error("provider turn frame omitted its turn projection");
      }
      return response.turn;
    } catch (error) {
      if (aborted) {
        this.closed = true;
        this.channel.destroy(error instanceof Error && error.name !== "AbortError" ? error : undefined);
        throw abortError();
      }
      this.closed = true;
      this.channel.destroy(error instanceof Error ? error : undefined);
      throw error;
    } finally {
      signal?.removeEventListener("abort", onAbort);
      this.busy = false;
    }
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.channel.destroy();
  }

  private async writeRequest(kind: RequestFrame["kind"], requestID: string): Promise<void> {
    if (this.writeSequence >= Number.MAX_SAFE_INTEGER) throw new Error("provider request sequence exhausted");
    const frame: RequestFrame = {
      version: BROKER_CHANNEL_VERSION,
      sequence: this.writeSequence,
      kind,
      request_id: requestID,
    };
    this.writeSequence += 1;
    await this.writeFrame(frame);
  }

  private async writeFrame(frame: RequestFrame): Promise<void> {
    const body = Buffer.from(JSON.stringify(frame), "utf8");
    if (body.length === 0 || body.length > MAX_PROVIDER_FRAME_BYTES) throw new Error("provider request frame exceeds bound");
    const encoded = Buffer.allocUnsafe(4 + body.length);
    encoded.writeUInt32BE(body.length, 0);
    body.copy(encoded, 4);
    await new Promise<void>((resolve, reject) => {
      this.channel.write(encoded, (error?: Error | null) => error ? reject(error) : resolve());
    });
  }

  private async readResponse(): Promise<ResponseFrame> {
    const prefix = await this.readExactly(4);
    const length = prefix.readUInt32BE(0);
    if (length === 0 || length > MAX_PROVIDER_FRAME_BYTES) throw new Error("provider response frame length is invalid");
    const body = await this.readExactly(length);
    const decoded: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body));
    const response = validateResponseFrame(decoded);
    if (response.sequence !== this.readSequence || this.readSequence >= Number.MAX_SAFE_INTEGER) {
      throw new Error("provider response sequence is duplicate, stale, or exhausted");
    }
    this.readSequence += 1;
    return response;
  }

  private async readExactly(size: number): Promise<Buffer> {
    while (this.buffered.length < size) {
      const item = await this.reader.next();
      if (item.done) throw new Error("Go provider channel closed before a complete frame");
      const bytes = typeof item.value === "string" ? Buffer.from(item.value, "utf8") : item.value;
      if (bytes.length === 0) continue;
      this.buffered = this.buffered.length === 0 ? Buffer.from(bytes) : Buffer.concat([this.buffered, bytes]);
      if (this.buffered.length > MAX_PROVIDER_FRAME_BYTES + 4) throw new Error("provider receive buffer exceeds bound");
    }
    const value = this.buffered.subarray(0, size);
    this.buffered = this.buffered.subarray(size);
    return value;
  }
}

export type BrokerProviderRuntime = {
  provider: Provider<typeof BROKER_API>;
  model: Model<typeof BROKER_API>;
  client: BrokerChannelClient;
};

/** Constructs the real Pi SDK provider; no process/network provider fallback exists. */
export function createBrokerProvider(channel: Duplex): BrokerProviderRuntime {
  const client = new BrokerChannelClient(channel);
  const model: Model<typeof BROKER_API> = {
    id: BROKER_MODEL_ID,
    name: "Approved OpenRouter model via TBound Go broker",
    api: BROKER_API,
    provider: BROKER_PROVIDER_ID,
    baseUrl: "tbound://inherited-provider-channel",
    reasoning: false,
    input: ["text"],
    inputLimits: { maxRequestBytes: MAX_PROVIDER_FRAME_BYTES },
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 64_000,
    maxTokens: 4_096,
  };
  const provider: Provider<typeof BROKER_API> = {
    id: BROKER_PROVIDER_ID,
    name: "TBound Go provider broker",
    baseUrl: "tbound://inherited-provider-channel",
    auth: {
      apiKey: {
        name: "Local Go broker channel (no provider credential in Pi)",
        check: async () => ({ type: "api_key" }),
        resolve: async () => ({ auth: {}, source: "TBound inherited channel" }),
      },
    },
    getModels: () => [model],
    stream: (_requestedModel: Model<typeof BROKER_API>, _untrustedTranscript: TranscriptContext, options?: StreamOptions) =>
      streamFromBroker(client, model, options?.signal),
    streamSimple: (_requestedModel: Model<typeof BROKER_API>, _untrustedTranscript: TranscriptContext, options?: SimpleStreamOptions) =>
      streamFromBroker(client, model, options?.signal),
  };
  return { provider, model, client };
}

/** Binds the fixed inherited fd used by the host composition; it opens no socket. */
export function createBrokerProviderFromFD(fd = 3): BrokerProviderRuntime {
  if (!Number.isInteger(fd) || fd < 3) throw new Error("provider bridge must use a host-inherited descriptor >= 3");
  return createBrokerProvider(new Socket({ fd, readable: true, writable: true, allowHalfOpen: true }));
}

function streamFromBroker(client: BrokerChannelClient, model: Model<typeof BROKER_API>, signal?: AbortSignal): AssistantMessageEventStream {
  const stream = createAssistantMessageEventStream();
  void (async () => {
    try {
      const turn = await client.next(signal);
      validateTurn(turn);
      if (signal?.aborted) throw abortError();
      emitTurn(stream, model, turn);
    } catch (caught) {
      const aborted = signal?.aborted || (caught instanceof Error && caught.name === "AbortError");
      if (aborted) client.close();
      const error = caught instanceof Error ? caught : new Error("provider bridge failed");
      const message = makeAssistant(model, {
        content: [{ type: "text", text: "" }],
        responseId: "tbound-bridge-error",
        timestamp: Date.now(),
        stopReason: aborted ? "aborted" : "error",
        errorMessage: aborted ? "Provider turn canceled" : "Go provider bridge failed; turn withheld",
        usage: emptyUsage(),
      });
      stream.push({ type: "error", reason: aborted ? "aborted" : "error", error: message });
      stream.end(message);
    }
  })();
  return stream;
}

function emitTurn(stream: AssistantMessageEventStream, model: Model<typeof BROKER_API>, turn: BrokerTurn): void {
  const call = turn.tool_call;
  if ((turn.finish_reason === "tool_calls") !== (call !== undefined)) throw new Error("provider finish reason and captured tool call differ");
  const usage = usageFrom(turn.usage);
  const partial = makeAssistant(model, {
    content: [], responseId: turn.response_id, timestamp: turn.created * 1000,
    stopReason: "pending", usage,
  });
  stream.push({ type: "start", partial: cloneAssistant(partial) });
  let index = 0;
  if (turn.assistant_text.length !== 0) {
    partial.content.push({ type: "text", text: "" });
    stream.push({ type: "text_start", contentIndex: index, partial: cloneAssistant(partial) });
    (partial.content[index] as { type: "text"; text: string }).text = turn.assistant_text;
    stream.push({ type: "text_delta", contentIndex: index, delta: turn.assistant_text, partial: cloneAssistant(partial) });
    stream.push({ type: "text_end", contentIndex: index, content: turn.assistant_text, partial: cloneAssistant(partial) });
    index += 1;
  }
  if (call) {
    if (!toolNames.has(call.name) || !isObject(call.arguments)) throw new Error("Go returned an unregistered or malformed provider tool call");
    const toolCall: ToolCall = { type: "toolCall", id: call.id, name: call.name, arguments: call.arguments };
    partial.content.push({ type: "toolCall", id: call.id, name: call.name, arguments: {} });
    stream.push({ type: "toolcall_start", contentIndex: index, partial: cloneAssistant(partial) });
    stream.push({ type: "toolcall_delta", contentIndex: index, delta: JSON.stringify(call.arguments), partial: cloneAssistant(partial) });
    partial.content[index] = toolCall;
    stream.push({ type: "toolcall_end", contentIndex: index, toolCall, partial: cloneAssistant(partial) });
  }
  partial.stopReason = call ? "toolUse" : "stop";
  const reason = call ? "toolUse" : "stop";
  stream.push({ type: "done", reason, message: cloneAssistant(partial) });
  stream.end(cloneAssistant(partial));
}

function makeAssistant(model: Model<typeof BROKER_API>, fields: Pick<AssistantMessage, "content" | "responseId" | "timestamp" | "stopReason" | "usage"> & Partial<Pick<AssistantMessage, "errorMessage">>): AssistantMessage {
  return {
    role: "assistant", content: fields.content, api: BROKER_API, provider: BROKER_PROVIDER_ID,
    model: model.id, responseModel: model.id, responseId: fields.responseId,
    usage: fields.usage, stopReason: fields.stopReason, timestamp: fields.timestamp,
    ...(fields.errorMessage ? { errorMessage: fields.errorMessage } : {}),
  };
}

function cloneAssistant(message: AssistantMessage): AssistantMessage {
  return structuredClone(message);
}

function usageFrom(raw: unknown): Usage {
  if (!isObject(raw)) return emptyUsage();
  const input = nonnegativeInt(raw.prompt_tokens);
  const output = nonnegativeInt(raw.completion_tokens);
  const details = isObject(raw.prompt_tokens_details) ? raw.prompt_tokens_details : {};
  const cacheRead = Math.min(input, nonnegativeInt(details.cached_tokens));
  const cost = typeof raw.cost === "number" && Number.isFinite(raw.cost) && raw.cost >= 0 ? raw.cost : 0;
  return {
    input, output, cacheRead, cacheWrite: 0,
    totalTokens: input > Number.MAX_SAFE_INTEGER - output ? Number.MAX_SAFE_INTEGER : input + output,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: cost },
  };
}

function emptyUsage(): Usage {
  return { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
}

function nonnegativeInt(value: unknown): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function validateTurn(turn: BrokerTurn): void {
  const requiredKeys = [
    "schema_version", "response_id", "model", "created", "generation", "sequence",
    "assistant_text", "finish_reason", "usage", "journal_sequence", "journal_hash",
  ];
  const allowedKeys = new Set([...requiredKeys, "tool_call"]);
  if (!turn || turn.schema_version !== BROKER_CHANNEL_VERSION || turn.model !== BROKER_MODEL_ID ||
      typeof turn.response_id !== "string" || turn.response_id.length === 0 || turn.response_id.length > 256 ||
      typeof turn.generation !== "string" || turn.generation.length === 0 || turn.generation.length > 256 ||
      !Number.isSafeInteger(turn.sequence) || turn.sequence <= 0 ||
      !Number.isSafeInteger(turn.created) || turn.created < 0 ||
      typeof turn.assistant_text !== "string" || Buffer.byteLength(turn.assistant_text, "utf8") > MAX_PROVIDER_FRAME_BYTES ||
      !Number.isSafeInteger(turn.journal_sequence) || turn.journal_sequence <= 0 ||
      typeof turn.journal_hash !== "string" || !/^[0-9a-f]{64}$/.test(turn.journal_hash) ||
      (turn.finish_reason !== "stop" && turn.finish_reason !== "tool_calls") ||
      requiredKeys.some((key) => !Object.hasOwn(turn, key)) || Object.keys(turn).some((key) => !allowedKeys.has(key))) {
    throw new Error("Go provider turn projection is malformed or outside bounds");
  }
  if (turn.tool_call) {
    const callKeys = ["id", "name", "arguments"];
    if (!isObject(turn.tool_call) || Object.keys(turn.tool_call).length !== callKeys.length ||
        callKeys.some((key) => !Object.hasOwn(turn.tool_call!, key)) ||
        typeof turn.tool_call.id !== "string" || turn.tool_call.id.length === 0 || turn.tool_call.id.length > 256 ||
        !toolNames.has(turn.tool_call.name) || !isJsonObject(turn.tool_call.arguments)) {
      throw new Error("Go provider tool-call projection is malformed");
    }
  }
}

function validateResponseFrame(value: unknown): ResponseFrame {
  if (!isObject(value)) throw new Error("Go provider response is not an object");
  const kind = value.kind;
  const expected = kind === "turn"
    ? ["version", "sequence", "kind", "request_id", "turn"]
    : kind === "error"
      ? ["version", "sequence", "kind", "request_id", "error"]
      : [];
  if (expected.length === 0 || Object.keys(value).length !== expected.length || expected.some((key) => !Object.hasOwn(value, key))) {
    throw new Error("Go provider response has an unexpected shape");
  }
  if (value.version !== BROKER_CHANNEL_VERSION || !Number.isSafeInteger(value.sequence) ||
      typeof value.request_id !== "string" || value.request_id.length === 0 ||
      (kind === "turn" && !isObject(value.turn)) || (kind === "error" && typeof value.error !== "string")) {
    throw new Error("Go provider response field is invalid");
  }
  if (kind === "turn") validateTurn(value.turn as unknown as BrokerTurn);
  return value as unknown as ResponseFrame;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isJsonObject(value: unknown, depth = 0): value is JsonObject {
  if (!isObject(value) || depth > 64) return false;
  return Object.values(value).every((item) => isJsonValue(item, depth + 1));
}

function isJsonValue(value: unknown, depth: number): value is JsonValue {
  if (depth > 64) return false;
  if (value === null || typeof value === "string" || typeof value === "boolean") return true;
  if (typeof value === "number") return Number.isFinite(value);
  if (Array.isArray(value)) return value.every((item) => isJsonValue(item, depth + 1));
  if (isObject(value)) return Object.values(value).every((item) => isJsonValue(item, depth + 1));
  return false;
}

function abortError(): Error {
  const error = new Error("Provider turn canceled");
  error.name = "AbortError";
  return error;
}
