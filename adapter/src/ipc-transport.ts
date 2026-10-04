import { timingSafeEqual } from "node:crypto";
import { createConnection, type Socket } from "node:net";
import { Duplex } from "node:stream";

export const IPC_WIRE_VERSION = "tbound-ipc/v1";
export const IPC_PREFIX_BYTES = 4;
export const IPC_MAX_MESSAGE_BYTES = 1_048_576;
export const IPC_MAX_FRAME_BYTES = IPC_PREFIX_BYTES + IPC_MAX_MESSAGE_BYTES;
export const IPC_MAX_FRAMES_PER_DIRECTION = 1_024;
export const IPC_MAX_STREAM_BYTES = 16 * 1_024 * 1_024;
export const IPC_BINDING_TOKEN_BYTES = 32;
export const IPC_BINDING_TOKEN_HEX_BYTES = IPC_BINDING_TOKEN_BYTES * 2;

export const IPC_TOOL_NAMES = ["read", "write", "edit", "bash"] as const;
export type IpcToolName = (typeof IPC_TOOL_NAMES)[number];

export interface IpcProposal {
  schema_version: "tbound-proposal/v1";
  tool_call_id: string;
  tool: IpcToolName;
  arguments: Record<string, unknown>;
}

export interface IpcResponseId {
  issuer: string;
  opaque: string;
}

export interface IpcResult {
  schema_version: "tbound-result/v1";
  response_id?: IpcResponseId | null;
  tool_call_id: string;
  tool: string;
  sequence: number;
  canonical_arguments_digest?: string;
  verdict: "ALLOW" | "DENY";
  reason_code: string;
  policy_digest: string;
  output?: unknown;
}

export type IpcKind = "proposal" | "result";

export interface IpcFrame {
  version: typeof IPC_WIRE_VERSION;
  binding_token: string;
  sequence: number;
  kind: IpcKind;
  payload: IpcProposal | IpcResult;
}

export type IpcErrorCode =
  | "ERR_IPC_CLOSED"
  | "ERR_IPC_MALFORMED_FRAME"
  | "ERR_IPC_FRAME_TOO_LARGE"
  | "ERR_IPC_BINDING_MISMATCH"
  | "ERR_IPC_SEQUENCE"
  | "ERR_IPC_UNEXPECTED_KIND"
  | "ERR_IPC_UNKNOWN_KIND"
  | "ERR_IPC_INVALID_TOKEN"
  | "ERR_IPC_IO"
  | "ERR_IPC_ABORTED"
  | "ERR_IPC_CORRELATION_MISMATCH";

export class IpcTransportError extends Error {
  readonly code: IpcErrorCode;

  constructor(code: IpcErrorCode, message: string, options?: ErrorOptions) {
    super(message, options);
    this.code = code;
    this.name = "IpcTransportError";
  }
}

const MALFORMED = "ERR_IPC_MALFORMED_FRAME";
const OBJECT_NUMBER_TOKENS = new WeakMap<object, Map<string, string>>();

function fail(code: IpcErrorCode, message: string): never {
  throw new IpcTransportError(code, message);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []): void {
  const allowed = new Set([...required, ...optional]);
  if (Object.keys(value).length < required.length || Object.keys(value).length > allowed.size) {
    fail(MALFORMED, "object has missing or unknown fields");
  }
  for (const key of required) {
    if (!Object.hasOwn(value, key)) fail(MALFORMED, `object is missing field ${JSON.stringify(key)}`);
  }
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) fail(MALFORMED, `unknown field ${JSON.stringify(key)}`);
  }
}

function unsignedIntegerToken(value: Record<string, unknown>, key: string): string | undefined {
  return OBJECT_NUMBER_TOKENS.get(value)?.get(key);
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const low = value.charCodeAt(index + 1);
      if (low < 0xdc00 || low > 0xdfff) return true;
      index += 1;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function validText(value: unknown, maxBytes = 256): value is string {
  return typeof value === "string" && value.length > 0 &&
    Buffer.byteLength(value, "utf8") <= maxBytes &&
    !hasUnpairedSurrogate(value) &&
    !/^\p{White_Space}|\p{White_Space}$/u.test(value) &&
    !/\p{Cc}/u.test(value);
}

function validBindingToken(token: unknown): token is string {
  return typeof token === "string" && /^[0-9a-f]{64}$/.test(token);
}

function validateBindingToken(token: unknown): asserts token is string {
  if (!validBindingToken(token)) {
    fail("ERR_IPC_INVALID_TOKEN", "IPC binding token must encode 32 bytes as lowercase hex");
  }
}

function assertJsonCompatible(value: unknown, seen = new Set<object>()): void {
  if (value === null || typeof value === "string" || typeof value === "boolean") {
    if (typeof value === "string" && hasUnpairedSurrogate(value)) fail(MALFORMED, "JSON string contains an unpaired surrogate");
    return;
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value) || Object.is(value, -0)) fail(MALFORMED, "JSON number is not a supported finite value");
    return;
  }
  if (Array.isArray(value)) {
    if (seen.has(value)) fail(MALFORMED, "cyclic values are not valid JSON");
    seen.add(value);
    for (const item of value) assertJsonCompatible(item, seen);
    seen.delete(value);
    return;
  }
  if (!isRecord(value)) fail(MALFORMED, "value is not JSON-compatible");
  const prototype = Object.getPrototypeOf(value);
  if (prototype !== Object.prototype && prototype !== null) fail(MALFORMED, "JSON object has an unsupported prototype");
  if (seen.has(value)) fail(MALFORMED, "cyclic values are not valid JSON");
  seen.add(value);
  for (const [key, item] of Object.entries(value)) {
    if (hasUnpairedSurrogate(key)) fail(MALFORMED, "JSON object key contains an unpaired surrogate");
    if (item === undefined || typeof item === "function" || typeof item === "symbol" || typeof item === "bigint") {
      fail(MALFORMED, "value is not JSON-compatible");
    }
    assertJsonCompatible(item, seen);
  }
  seen.delete(value);
}

function validateToolArguments(tool: unknown, argumentsValue: unknown): void {
  if (!IPC_TOOL_NAMES.includes(tool as IpcToolName)) fail(MALFORMED, "proposal tool is not registered");
  if (!isRecord(argumentsValue)) fail(MALFORMED, "tool arguments must be an object");

  const stringField = (field: string): void => {
    if (typeof argumentsValue[field] !== "string") fail(MALFORMED, `tool argument ${field} must be a string`);
  };
  const numberField = (field: string): void => {
    if (typeof argumentsValue[field] !== "number" || !Number.isFinite(argumentsValue[field])) {
      fail(MALFORMED, `tool argument ${field} must be a finite JSON number`);
    }
  };

  switch (tool) {
    case "read":
      exactKeys(argumentsValue, ["path"], ["offset", "limit"]);
      stringField("path");
      if (Object.hasOwn(argumentsValue, "offset")) numberField("offset");
      if (Object.hasOwn(argumentsValue, "limit")) numberField("limit");
      return;
    case "write":
      exactKeys(argumentsValue, ["path", "content"]);
      stringField("path");
      stringField("content");
      return;
    case "edit": {
      exactKeys(argumentsValue, ["path", "edits"]);
      stringField("path");
      if (!Array.isArray(argumentsValue.edits)) fail(MALFORMED, "edit.edits must be an array");
      for (const [index, item] of argumentsValue.edits.entries()) {
        if (!isRecord(item)) fail(MALFORMED, `edit.edits[${index}] must be an object`);
        exactKeys(item, ["oldText", "newText"]);
        if (typeof item.oldText !== "string" || typeof item.newText !== "string") {
          fail(MALFORMED, `edit.edits[${index}] fields must be strings`);
        }
      }
      return;
    }
    case "bash":
      exactKeys(argumentsValue, ["command"], ["timeout"]);
      stringField("command");
      if (Object.hasOwn(argumentsValue, "timeout")) {
        numberField("timeout");
        const timeout = argumentsValue.timeout as number;
        if (timeout <= 0 || timeout > 2_147_483_647 / 1_000) {
          fail(MALFORMED, "bash.timeout is outside the supported range");
        }
      }
      return;
  }
}

function validateProposal(value: unknown): asserts value is IpcProposal {
  if (!isRecord(value)) fail(MALFORMED, "proposal payload must be an object");
  exactKeys(value, ["schema_version", "tool_call_id", "tool", "arguments"]);
  if (value.schema_version !== "tbound-proposal/v1" || !validText(value.tool_call_id) ||
      !validText(value.tool)) {
    fail(MALFORMED, "proposal has invalid required fields");
  }
  validateToolArguments(value.tool, value.arguments);
}

function validateResponseId(value: unknown): asserts value is IpcResponseId {
  if (!isRecord(value)) fail(MALFORMED, "result response_id must be an object");
  exactKeys(value, ["issuer", "opaque"]);
  if (!validText(value.issuer) || !validText(value.opaque)) fail(MALFORMED, "result has invalid response identity");
}

function validateResult(value: unknown): asserts value is IpcResult {
  if (!isRecord(value)) fail(MALFORMED, "result payload must be an object");
  exactKeys(
    value,
    ["schema_version", "tool_call_id", "tool", "sequence", "verdict", "reason_code", "policy_digest"],
    ["response_id", "canonical_arguments_digest", "output"],
  );
  if (value.schema_version !== "tbound-result/v1" || !validText(value.tool_call_id) ||
      !validText(value.tool) || !validText(value.reason_code) || !validText(value.policy_digest) ||
      !Number.isSafeInteger(value.sequence) || (value.sequence as number) < 0 ||
      (unsignedIntegerToken(value, "sequence") !== undefined &&
       !/^(?:0|[1-9][0-9]*)$/.test(unsignedIntegerToken(value, "sequence")!)) ||
      (value.verdict !== "ALLOW" && value.verdict !== "DENY")) {
    fail(MALFORMED, "result has invalid required values");
  }
  if (Object.hasOwn(value, "response_id") && value.response_id !== null) validateResponseId(value.response_id);
  if (Object.hasOwn(value, "canonical_arguments_digest")) {
    const digest = value.canonical_arguments_digest;
    if (typeof digest !== "string" || (digest !== "" && !/^tbound-args-jcs-rfc8785\/v1:sha256:[0-9a-f]{64}$/.test(digest))) {
      fail(MALFORMED, "result has an invalid canonical-arguments digest");
    }
  }
  if (value.verdict === "ALLOW" &&
      (!isRecord(value.response_id) || (value.sequence as number) === 0 ||
       typeof value.canonical_arguments_digest !== "string" || value.canonical_arguments_digest === "" ||
       !IPC_TOOL_NAMES.includes(value.tool as IpcToolName) || value.reason_code !== "policy_rule_allow")) {
    fail(MALFORMED, "allow result is not bound to a matched registered proposal");
  }
  if (value.verdict === "DENY" && Object.hasOwn(value, "output")) {
    fail(MALFORMED, "denial result may not contain executor output");
  }
}

function validatePayload(kind: IpcKind, payload: unknown): asserts payload is IpcProposal | IpcResult {
  if (!isRecord(payload)) fail(MALFORMED, "IPC payload must be an object");
  if (kind === "proposal") validateProposal(payload);
  else if (kind === "result") validateResult(payload);
  else fail("ERR_IPC_UNKNOWN_KIND", `unknown IPC kind ${JSON.stringify(kind)}`);
}

class StrictJsonParser {
  private offset = 0;
  private readonly source: string;

  constructor(source: string) {
    this.source = source;
  }

  parse(): unknown {
    this.skipWhitespace();
    const value = this.parseValue(0);
    this.skipWhitespace();
    if (this.offset !== this.source.length) fail(MALFORMED, "JSON has trailing content");
    return value;
  }

  private parseValue(depth: number): unknown {
    const character = this.source[this.offset];
    if (character === "{") return this.parseObject(depth);
    if (character === "[") return this.parseArray(depth);
    if (character === '"') return this.parseString();
    if (character === "t" && this.source.startsWith("true", this.offset)) {
      this.offset += 4;
      return true;
    }
    if (character === "f" && this.source.startsWith("false", this.offset)) {
      this.offset += 5;
      return false;
    }
    if (character === "n" && this.source.startsWith("null", this.offset)) {
      this.offset += 4;
      return null;
    }
    return this.parseNumber();
  }

  private parseObject(depth: number): Record<string, unknown> {
    if (depth >= 64) fail(MALFORMED, "JSON nesting exceeds the protocol limit");
    this.offset += 1;
    this.skipWhitespace();
    const value: Record<string, unknown> = {};
    const keys = new Set<string>();
    if (this.source[this.offset] === "}") {
      this.offset += 1;
      return value;
    }
    while (true) {
      if (this.source[this.offset] !== '"') fail(MALFORMED, "JSON object key is not a string");
      const key = this.parseString();
      if (keys.has(key)) fail(MALFORMED, `duplicate JSON object key ${JSON.stringify(key)}`);
      keys.add(key);
      this.skipWhitespace();
      if (this.source[this.offset] !== ":") fail(MALFORMED, "malformed JSON object");
      this.offset += 1;
      this.skipWhitespace();
      const valueOffset = this.offset;
      const item = this.parseValue(depth + 1);
      Object.defineProperty(value, key, {
        value: item,
        enumerable: true,
        configurable: true,
        writable: true,
      });
      if (typeof item === "number") {
        let numberTokens = OBJECT_NUMBER_TOKENS.get(value);
        if (!numberTokens) {
          numberTokens = new Map<string, string>();
          OBJECT_NUMBER_TOKENS.set(value, numberTokens);
        }
        numberTokens.set(key, this.source.slice(valueOffset, this.offset));
      }
      this.skipWhitespace();
      const delimiter = this.source[this.offset];
      if (delimiter === "}") {
        this.offset += 1;
        return value;
      }
      if (delimiter !== ",") fail(MALFORMED, "malformed JSON object");
      this.offset += 1;
      this.skipWhitespace();
    }
  }

  private parseArray(depth: number): unknown[] {
    if (depth >= 64) fail(MALFORMED, "JSON nesting exceeds the protocol limit");
    this.offset += 1;
    this.skipWhitespace();
    const value: unknown[] = [];
    if (this.source[this.offset] === "]") {
      this.offset += 1;
      return value;
    }
    while (true) {
      value.push(this.parseValue(depth + 1));
      this.skipWhitespace();
      const delimiter = this.source[this.offset];
      if (delimiter === "]") {
        this.offset += 1;
        return value;
      }
      if (delimiter !== ",") fail(MALFORMED, "malformed JSON array");
      this.offset += 1;
      this.skipWhitespace();
    }
  }

  private parseString(): string {
    const start = this.offset;
    this.offset += 1;
    while (this.offset < this.source.length) {
      const code = this.source.charCodeAt(this.offset);
      if (code === 0x22) {
        this.offset += 1;
        let value: unknown;
        try {
          value = JSON.parse(this.source.slice(start, this.offset)) as unknown;
        } catch (error) {
          fail(MALFORMED, `invalid JSON string: ${String(error)}`);
        }
        if (typeof value !== "string" || hasUnpairedSurrogate(value)) {
          fail(MALFORMED, "JSON string contains an unpaired surrogate");
        }
        return value;
      }
      if (code < 0x20) fail(MALFORMED, "JSON string contains an unescaped control character");
      if (code === 0x5c) {
        this.offset += 1;
        const escaped = this.source[this.offset];
        if (escaped === "u") this.offset += 5;
        else this.offset += 1;
      } else {
        this.offset += 1;
      }
    }
    fail(MALFORMED, "unterminated JSON string");
  }

  private parseNumber(): number {
    const match = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(this.source.slice(this.offset));
    if (!match) fail(MALFORMED, "invalid JSON value");
    this.offset += match[0].length;
    const value = Number(match[0]);
    if (!Number.isFinite(value) || Object.is(value, -0) || (value === 0 && /[1-9]/.test(match[0]))) {
      fail(MALFORMED, `JSON number is not a finite supported value: ${match[0]}`);
    }
    return value;
  }

  private skipWhitespace(): void {
    while (this.offset < this.source.length && /[\u0020\u0009\u000a\u000d]/.test(this.source[this.offset]!)) {
      this.offset += 1;
    }
  }
}

function parseStrictJson(encoded: Uint8Array): unknown {
  let source: string;
  try {
    source = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(encoded);
  } catch (error) {
    fail(MALFORMED, `JSON is not valid UTF-8: ${String(error)}`);
  }
  return new StrictJsonParser(source).parse();
}

interface DecodedEnvelope {
  version: typeof IPC_WIRE_VERSION;
  binding_token: string;
  sequence: number;
  kind: IpcKind;
  payload: unknown;
}

function decodeEnvelope(encoded: Uint8Array): DecodedEnvelope {
  if (encoded.byteLength === 0 || encoded.byteLength > IPC_MAX_MESSAGE_BYTES) {
    fail("ERR_IPC_FRAME_TOO_LARGE", "IPC message length is outside the configured limit");
  }
  const value = parseStrictJson(encoded);
  if (!isRecord(value)) fail(MALFORMED, "IPC envelope must be an object");
  exactKeys(value, ["version", "binding_token", "sequence", "kind", "payload"]);
  if (value.version !== IPC_WIRE_VERSION || typeof value.binding_token !== "string" ||
      !Number.isSafeInteger(value.sequence) || (value.sequence as number) <= 0 ||
      !/^[1-9][0-9]*$/.test(unsignedIntegerToken(value, "sequence") ?? "") ||
      !isRecord(value.payload)) {
    fail(MALFORMED, "IPC envelope has invalid values");
  }
  if (value.kind !== "proposal" && value.kind !== "result") {
    fail("ERR_IPC_UNKNOWN_KIND", `unknown IPC message kind ${JSON.stringify(value.kind)}`);
  }
  return value as unknown as DecodedEnvelope;
}

export function decodeIpcFrame(encoded: Uint8Array): IpcFrame {
  const envelope = decodeEnvelope(encoded);
  validateBindingToken(envelope.binding_token);
  validatePayload(envelope.kind, envelope.payload);
  return envelope as IpcFrame;
}

export function encodeIpcFrame(frame: IpcFrame): Buffer {
  validateBindingToken(frame.binding_token);
  if (frame.version !== IPC_WIRE_VERSION || !Number.isSafeInteger(frame.sequence) || frame.sequence <= 0) {
    fail(MALFORMED, "IPC envelope has invalid version or sequence");
  }
  if (frame.kind !== "proposal" && frame.kind !== "result") {
    fail("ERR_IPC_UNKNOWN_KIND", `unknown IPC message kind ${JSON.stringify(frame.kind)}`);
  }
  assertJsonCompatible(frame.payload);
  validatePayload(frame.kind, frame.payload);
  let payloadJson: string | undefined;
  try {
    payloadJson = JSON.stringify(frame.payload);
  } catch (error) {
    fail(MALFORMED, `could not encode IPC payload: ${String(error)}`);
  }
  if (payloadJson === undefined) fail(MALFORMED, "IPC payload is not JSON-serializable");
  const encoded = Buffer.from(
    `{"version":${JSON.stringify(frame.version)},"binding_token":${JSON.stringify(frame.binding_token)},` +
    `"sequence":${frame.sequence},"kind":${JSON.stringify(frame.kind)},"payload":${payloadJson}}`,
    "utf8",
  );
  if (encoded.byteLength === 0 || encoded.byteLength > IPC_MAX_MESSAGE_BYTES) {
    fail("ERR_IPC_FRAME_TOO_LARGE", "IPC message exceeds the configured limit");
  }
  // Parse the bytes we will actually send, not merely the source object.
  decodeIpcFrame(encoded);
  return encoded;
}

class StreamReader {
  private readonly iterator: AsyncIterator<Buffer>;
  private readonly chunks: Buffer[] = [];
  private chunkOffset = 0;
  private available = 0;
  private ended = false;
  private readonly stream: Duplex;

  constructor(stream: Duplex) {
    this.stream = stream;
    this.iterator = stream[Symbol.asyncIterator]() as AsyncIterator<Buffer>;
  }

  async readExactly(length: number, allowCleanEof: boolean): Promise<Buffer | null> {
    while (this.available < length && !this.ended) {
      let next: IteratorResult<Buffer>;
      try {
        next = await this.iterator.next();
      } catch (error) {
        throw new IpcTransportError("ERR_IPC_IO", "IPC stream read failed", { cause: error });
      }
      if (next.done) {
        this.ended = true;
        break;
      }
      const chunk = next.value;
      if (!Buffer.isBuffer(chunk)) throw new IpcTransportError(MALFORMED, "IPC stream returned non-byte data");
      if (chunk.length === 0) continue;
      if (this.available + chunk.length > IPC_MAX_FRAME_BYTES) {
        throw new IpcTransportError("ERR_IPC_FRAME_TOO_LARGE", "IPC stream buffered more than one maximum frame");
      }
      this.chunks.push(chunk);
      this.available += chunk.length;
    }
    if (this.available < length) {
      if (this.available === 0 && allowCleanEof) return null;
      throw new IpcTransportError(MALFORMED, "truncated IPC frame");
    }

    const result = Buffer.allocUnsafe(length);
    let copied = 0;
    while (copied < length) {
      const head = this.chunks[0];
      if (!head) throw new IpcTransportError(MALFORMED, "IPC reader buffer accounting failed");
      const count = Math.min(length - copied, head.length - this.chunkOffset);
      head.copy(result, copied, this.chunkOffset, this.chunkOffset + count);
      copied += count;
      this.chunkOffset += count;
      this.available -= count;
      if (this.chunkOffset === head.length) {
        this.chunks.shift();
        this.chunkOffset = 0;
      }
    }
    return result;
  }
}

class SerialQueue {
  private tail: Promise<void> = Promise.resolve();

  run<T>(operation: () => Promise<T>): Promise<T> {
    const previous = this.tail;
    let release!: () => void;
    this.tail = new Promise<void>((resolve) => { release = resolve; });
    return previous.then(operation).finally(release);
  }
}

type ReadKind = "proposal" | "result";
type WriteKind = "proposal" | "result";

class Endpoint {
  private readonly reader: StreamReader;
  private readonly readQueue = new SerialQueue();
  private readonly writeQueue = new SerialQueue();
  private closed = false;
  private readNext = 1;
  private writeNext = 1;
  private readFrames = 0;
  private writeFrames = 0;
  private readBytes = 0;
  private writeBytes = 0;
  private readonly stream: Duplex;
  private readonly token: string;

  constructor(stream: Duplex, token: string) {
    this.stream = stream;
    this.token = token;
    validateBindingToken(token);
    this.reader = new StreamReader(stream);
  }

  read<T extends IpcProposal | IpcResult>(want: ReadKind): Promise<T | null> {
    return this.readQueue.run(async () => {
      this.checkOpen();
      try {
        const prefix = await this.reader.readExactly(IPC_PREFIX_BYTES, true);
        if (prefix === null) {
          this.close();
          return null;
        }
        const length = prefix.readUInt32BE(0);
        if (length === 0 || length > IPC_MAX_MESSAGE_BYTES) {
          throw new IpcTransportError("ERR_IPC_FRAME_TOO_LARGE", "IPC frame length is outside the configured limit");
        }
        if (this.readFrames >= IPC_MAX_FRAMES_PER_DIRECTION || this.readBytes + length > IPC_MAX_STREAM_BYTES) {
          throw new IpcTransportError("ERR_IPC_FRAME_TOO_LARGE", "IPC stream limit exceeded");
        }
        const encoded = await this.reader.readExactly(length, false);
        if (encoded === null) throw new IpcTransportError(MALFORMED, "truncated IPC frame payload");
        const envelope = decodeEnvelope(encoded);
        if (!validBindingToken(envelope.binding_token) ||
            !timingSafeEqual(Buffer.from(envelope.binding_token, "hex"), Buffer.from(this.token, "hex"))) {
          throw new IpcTransportError("ERR_IPC_BINDING_MISMATCH", "IPC session binding token mismatch");
        }
        if (envelope.sequence !== this.readNext) {
          throw new IpcTransportError("ERR_IPC_SEQUENCE", `IPC sequence is ${envelope.sequence}; expected ${this.readNext}`);
        }
        if (envelope.kind !== "proposal" && envelope.kind !== "result") {
          throw new IpcTransportError("ERR_IPC_UNKNOWN_KIND", `unknown IPC message kind ${JSON.stringify(envelope.kind)}`);
        }
        if (envelope.kind !== want) {
          throw new IpcTransportError("ERR_IPC_UNEXPECTED_KIND", "IPC message kind is not valid in this direction");
        }
        validatePayload(envelope.kind, envelope.payload);
        this.readFrames += 1;
        this.readBytes += length;
        this.readNext += 1;
        return envelope.payload as T;
      } catch (error) {
        throw this.fail(asIpcError(error));
      }
    });
  }

  write(kind: WriteKind, payload: IpcProposal | IpcResult): Promise<void> {
    return this.writeQueue.run(async () => {
      this.checkOpen();
      try {
        if (this.writeFrames >= IPC_MAX_FRAMES_PER_DIRECTION) {
          throw new IpcTransportError("ERR_IPC_FRAME_TOO_LARGE", "IPC stream frame-count limit exceeded");
        }
        const frame = encodeIpcFrame({
          version: IPC_WIRE_VERSION,
          binding_token: this.token,
          sequence: this.writeNext,
          kind,
          payload,
        });
        if (this.writeBytes + frame.byteLength > IPC_MAX_STREAM_BYTES) {
          throw new IpcTransportError("ERR_IPC_FRAME_TOO_LARGE", "IPC stream byte limit exceeded");
        }
        const wire = Buffer.allocUnsafe(IPC_PREFIX_BYTES + frame.byteLength);
        wire.writeUInt32BE(frame.byteLength, 0);
        frame.copy(wire, IPC_PREFIX_BYTES);
        await new Promise<void>((resolve, reject) => {
          this.stream.write(wire, (error?: Error | null) => error ? reject(error) : resolve());
        });
        this.writeFrames += 1;
        this.writeBytes += frame.byteLength;
        this.writeNext += 1;
      } catch (error) {
        throw this.fail(asIpcError(error));
      }
    });
  }

  fail(error: IpcTransportError): IpcTransportError {
    if (!this.closed) {
      this.closed = true;
      this.stream.destroy();
    }
    return error;
  }

  close(): void {
    if (!this.closed) {
      this.closed = true;
      this.stream.destroy();
    }
  }

  private checkOpen(): void {
    if (this.closed || this.stream.destroyed) {
      throw new IpcTransportError("ERR_IPC_CLOSED", "IPC endpoint is closed");
    }
  }
}

function asIpcError(error: unknown): IpcTransportError {
  if (error instanceof IpcTransportError) return error;
  return new IpcTransportError("ERR_IPC_IO", "IPC transport operation failed", { cause: error });
}

export class IpcClient {
  private readonly endpoint: Endpoint;
  private readonly requests = new SerialQueue();

  constructor(stream: Duplex, bindingToken: string) {
    this.endpoint = new Endpoint(stream, bindingToken);
  }

  sendProposal(proposal: IpcProposal): Promise<void> {
    return this.endpoint.write("proposal", proposal);
  }

  receiveResult(): Promise<IpcResult | null> {
    return this.endpoint.read<IpcResult>("result");
  }

  request(proposal: IpcProposal, signal?: AbortSignal): Promise<IpcResult> {
    if (signal?.aborted) return Promise.reject(abortedError());
    return this.requests.run(async () => {
      if (signal?.aborted) throw abortedError();
      const onAbort = (): void => { this.endpoint.fail(abortedError()); };
      signal?.addEventListener("abort", onAbort, { once: true });
      try {
        await this.sendProposal(proposal);
        if (signal?.aborted) throw abortedError();
        const result = await this.receiveResult();
        if (signal?.aborted) throw abortedError();
        if (result === null) throw new IpcTransportError("ERR_IPC_CLOSED", "supervisor closed IPC before returning a result");
        if (result.tool_call_id !== proposal.tool_call_id || result.tool !== proposal.tool) {
          throw this.endpoint.fail(new IpcTransportError(
            "ERR_IPC_CORRELATION_MISMATCH",
            "IPC result does not match the outstanding tool proposal",
          ));
        }
        return result;
      } catch (error) {
        if (signal?.aborted) throw abortedError();
        throw error;
      } finally {
        signal?.removeEventListener("abort", onAbort);
      }
    });
  }

  close(): void {
    this.endpoint.close();
  }
}

export class IpcServer {
  private readonly endpoint: Endpoint;

  constructor(stream: Duplex, bindingToken: string) {
    this.endpoint = new Endpoint(stream, bindingToken);
  }

  receiveProposal(): Promise<IpcProposal | null> {
    return this.endpoint.read<IpcProposal>("proposal");
  }

  sendResult(result: IpcResult): Promise<void> {
    return this.endpoint.write("result", result);
  }

  close(): void {
    this.endpoint.close();
  }
}

function abortedError(): IpcTransportError {
  return new IpcTransportError("ERR_IPC_ABORTED", "IPC request was aborted");
}

export function createInMemoryDuplexPair(): readonly [Duplex, Duplex] {
  class MemoryDuplex extends Duplex {
    peer: MemoryDuplex | undefined;

    constructor() {
      super({ allowHalfOpen: false });
    }

    override _read(): void {}

    override _write(chunk: Buffer, _encoding: BufferEncoding, callback: (error?: Error | null) => void): void {
      if (!this.peer || this.peer.destroyed) {
        callback(new Error("in-memory IPC peer is closed"));
        return;
      }
      this.peer.push(Buffer.from(chunk));
      callback();
    }

    override _final(callback: (error?: Error | null) => void): void {
      this.peer?.push(null);
      callback();
    }

    override _destroy(error: Error | null, callback: (error?: Error | null) => void): void {
      this.peer?.push(null);
      callback(error);
    }
  }

  const left = new MemoryDuplex();
  const right = new MemoryDuplex();
  left.peer = right;
  right.peer = left;
  return [left, right];
}

export function createInMemoryIpcPair(bindingToken: string): { client: IpcClient; server: IpcServer } {
  const [clientStream, serverStream] = createInMemoryDuplexPair();
  return {
    client: new IpcClient(clientStream, bindingToken),
    server: new IpcServer(serverStream, bindingToken),
  };
}

export async function dialUnixIpc(path: string, bindingToken: string): Promise<IpcClient> {
  validateBindingToken(bindingToken);
  if (process.platform !== "linux") {
    throw new Error("Unix-domain IPC transport is available only on Linux");
  }
  if (Buffer.byteLength(path, "utf8") === 0 || Buffer.byteLength(path, "utf8") >= 108) {
    throw new Error("Unix socket path is empty or exceeds the Linux pathname limit");
  }
  const socket: Socket = createConnection(path);
  try {
    await new Promise<void>((resolve, reject) => {
      const onConnect = (): void => { cleanup(); resolve(); };
      const onError = (error: Error): void => { cleanup(); reject(error); };
      const cleanup = (): void => {
        socket.removeListener("connect", onConnect);
        socket.removeListener("error", onError);
      };
      socket.once("connect", onConnect);
      socket.once("error", onError);
    });
    return new IpcClient(socket, bindingToken);
  } catch (error) {
    socket.destroy();
    throw error;
  }
}
