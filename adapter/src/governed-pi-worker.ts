/**
 * Linux inherited-FD Pi SDK worker.
 *
 * This is the actual Pi AgentSession path (not a UI replacement). The fixed Go
 * provider bridge owns prompt admission, provider history/network, correlation,
 * durable gate decisions, and executor results. This worker receives only a
 * session bootstrap, admitted control prompts, proxy-tool IPC, and the real
 * provider projection stream. It is not itself a host-containment profile.
 */
import { createHash } from "node:crypto";
import { fstatSync, lstatSync, readSync, statSync, write } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Socket } from "node:net";
import {
  createAgentSessionFromServices,
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type AgentSession,
  type AgentSessionRuntimeDiagnostic,
  type ResourceLoader,
} from "@earendil-works/pi-coding-agent";
import {
  type Model,
  type Provider,
} from "@earendil-works/pi-ai";
import { createBrokerProviderFromFD, BROKER_MODEL_ID, BROKER_PROVIDER_ID } from "./broker-provider.ts";
import { declaredToolNames, createProxyTools, IpcProposalSender } from "./proxy-tools.ts";
import { IpcClient } from "./ipc-transport.ts";
import { LockedResourceLoader } from "./locked-resource-loader.ts";

export const GOVERNED_PI_WORKER_VERSION = "governed-pi-sdk-worker/v1" as const;
export const GOVERNED_PI_DESCRIPTOR_MAP = {
  executable: 3,
  worker_exposure: 4,
  bootstrap: 5,
  proposal_ipc: 6,
  provider_bridge: 7,
  worker_events: 8,
  runtime_bundle: 9,
  session_control: 0,
} as const;
export const GOVERNED_PI_WORKER_EVENT_MAX_BYTES = 1 << 20;
export const GOVERNED_PI_WORKER_EVENT_TOTAL_MAX_COUNT = 1024;
export const GOVERNED_PI_WORKER_EVENT_TOTAL_MAX_BYTES = 16 << 20;
export const GOVERNED_PI_WORKER_EVENT_QUEUE_MAX = 128;
export const GOVERNED_PI_WORKER_EVENT_QUEUE_BYTES = 2 << 20;
export const GOVERNED_PI_WORKER_CONTROL_MAX_BYTES = 1 << 16;
export const GOVERNED_PI_WORKER_PROMPT_MAX_BYTES = 16 << 10;

export interface GovernedPiBootstrap {
  schema_version: typeof GOVERNED_PI_WORKER_VERSION;
  profile_id: string;
  profile_digest: string;
  source_binding_digest: string;
  source_provenance_digest: string;
  source_generation_id: string;
  source_tree_digest: string;
  source_manifest_digest: string;
  worker_view_identity: string;
  worker_mount_id: number;
  runtime_bundle_digest: string;
  descriptor_profile: string;
  descriptor_profile_digest: string;
  child_environment_digest: string;
  session_id: string;
  workflow_id: string;
  conversation_id: string;
  private_cwd: string;
  ipc_binding_token: string;
  provider_id: typeof BROKER_PROVIDER_ID;
  model_id: typeof BROKER_MODEL_ID;
  node_version: string;
  pi_version: "0.87.1";
  tools: readonly string[];
  tool_schema_sha256: Readonly<Record<string, string>>;
}

export type GovernedPiControl =
  | { type: "prompt"; sequence: number; request_id: string; task_id: string; text: string }
  | { type: "abort"; sequence: number; request_id: string }
  | { type: "stop"; sequence: number; request_id: string };

export type GovernedPiWorkerEventPayload =
  | {
      type: "ready";
      schema_version: typeof GOVERNED_PI_WORKER_VERSION;
      profile_id: string;
      profile_digest: string;
      source_binding_digest: string;
      source_provenance_digest: string;
      source_generation_id: string;
      source_tree_digest: string;
      source_manifest_digest: string;
      worker_view_identity: string;
      worker_mount_id: number;
      runtime_bundle_digest: string;
      descriptor_profile: string;
      descriptor_profile_digest: string;
      node_version: string;
      pi_version: "0.87.1";
      provider_id: typeof BROKER_PROVIDER_ID;
      model_id: typeof BROKER_MODEL_ID;
      tools: readonly string[];
      tool_schema_sha256: Readonly<Record<string, string>>;
    }
  | { type: "turn_end"; request_id: string }
  | { type: "worker_error"; request_id?: string; code: string }
  | { type: "stopped"; request_id?: string };
export type GovernedPiWorkerEvent = GovernedPiWorkerEventPayload & { sequence: number };

type WorkerServices = {
  session: AgentSession;
  provider: Provider;
  model: Model<string>;
  resourceLoader: ResourceLoader;
  modelRuntime: ModelRuntime;
  closeProvider: () => void;
};

const TOOL_SCHEMA_SHA256 = {
  read: "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
  write: "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
  edit: "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
  bash: "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
} as const;
const EXPECTED_ENVIRONMENT_NAMES = ["HOME", "LANG", "PI_OFFLINE", "TMPDIR"] as const;

class MemoryCredentialStore {
  async read() { return undefined; }
  async list() { return []; }
  async modify(_providerId: string, update: (current: undefined) => Promise<undefined>) { return update(undefined); }
  async delete() {}
}

/**
 * Narrows Pi's built-in ModelRuntime surface to exactly the inherited Go
 * provider. It has no route to credentials or model/provider mutation.
 */
class ClosedBrokerModelRuntime {
  readonly #delegate: ModelRuntime;
  readonly #provider: Provider;
  readonly #model: Model<string>;

  constructor(delegate: ModelRuntime, provider: Provider, model: Model<string>) {
    this.#delegate = delegate;
    this.#provider = provider;
    this.#model = model;
  }

  getProviders(): readonly Provider[] { return [this.#provider]; }
  getProvider(id: string): Provider | undefined { return id === this.#provider.id ? this.#provider : undefined; }
  getModels(provider?: string): readonly Model<string>[] { return !provider || provider === this.#provider.id ? [this.#model] : []; }
  getModel(provider: string, id: string): Model<string> | undefined {
    return provider === this.#provider.id && id === this.#model.id ? this.#model : undefined;
  }
  getAvailableSnapshot(): readonly Model<string>[] { return [this.#model]; }
  getError(): undefined { return undefined; }
  getRegisteredProviderIds(): readonly string[] { return [this.#provider.id]; }
  getRegisteredNativeProvider(id: string): Provider | undefined { return id === this.#provider.id ? this.#provider : undefined; }
  getProviderAuthStatus(id: string): { configured: boolean; source?: string } {
    return { configured: id === this.#provider.id, source: "inherited Go provider bridge" };
  }
  hasConfiguredAuth(id: string): boolean { return id === this.#provider.id; }
  isUsingOAuth(): boolean { return false; }
  isUsingSubscription(): boolean { return false; }
  async checkAuth(id: string): Promise<unknown> { return id === this.#provider.id ? this.#delegate.checkAuth(id) : undefined; }
  async getAuth(value: string | Model<string>, overrides?: unknown): Promise<unknown> {
    const id = typeof value === "string" ? value : value.provider;
    return id === this.#provider.id ? this.#delegate.getAuth(value as never, overrides as never) : undefined;
  }
  async getAvailable(id?: string): Promise<readonly Model<string>[]> { return !id || id === this.#provider.id ? [this.#model] : []; }
  async listCredentials(): Promise<readonly unknown[]> { return []; }
  async refresh(): Promise<{ aborted: false; errors: ReadonlyMap<string, Error> }> { return { aborted: false, errors: new Map() }; }
  stream(model: Model<string>, context: unknown, options?: unknown): unknown { this.#assertModel(model); return this.#delegate.stream(model, context as never, options as never); }
  complete(model: Model<string>, context: unknown, options?: unknown): Promise<unknown> { this.#assertModel(model); return this.#delegate.complete(model, context as never, options as never); }
  streamSimple(model: Model<string>, context: unknown, options?: unknown): unknown { this.#assertModel(model); return this.#delegate.streamSimple(model, context as never, options as never); }
  completeSimple(model: Model<string>, context: unknown, options?: unknown): Promise<unknown> { this.#assertModel(model); return this.#delegate.completeSimple(model, context as never, options as never); }
  getCompatibilityRequestConfig(): undefined { return undefined; }
  registerProvider(): never { throw new Error("governed worker provider inventory is immutable"); }
  registerNativeProvider(): never { throw new Error("governed worker provider inventory is immutable"); }
  unregisterProvider(): never { throw new Error("governed worker provider inventory is immutable"); }
  login(): never { throw new Error("governed worker authentication is disabled"); }
  logout(): never { throw new Error("governed worker authentication is disabled"); }
  setRuntimeApiKey(): never { throw new Error("governed worker authentication is disabled"); }
  removeRuntimeApiKey(): never { throw new Error("governed worker authentication is disabled"); }

  #assertModel(model: Model<string>): void {
    if (model.provider !== this.#provider.id || model.id !== this.#model.id) {
      throw new Error("governed worker refused a model outside its fixed Go provider");
    }
  }
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  return Object.keys(value).sort().join("\0") === [...keys].sort().join("\0");
}

function validIdentity(value: unknown, maxBytes = 256): value is string {
  return typeof value === "string" && value.length > 0 && Buffer.byteLength(value, "utf8") <= maxBytes && !/[\u0000-\u001f\u007f]/u.test(value);
}

export interface WorkerFileIdentity {
  dev: number;
  ino: number;
  mode: number;
  uid: number;
  isDirectory(): boolean;
  isFile(): boolean;
  isSymbolicLink(): boolean;
}

export interface GovernedPiWorkerPreflight {
  environment: NodeJS.ProcessEnv;
  nodeVersion: string;
  cwd: string;
  uid: number;
  cwdDescriptor: WorkerFileIdentity;
  cwdPath: WorkerFileIdentity;
  bundleDescriptor: WorkerFileIdentity;
  bundlePath: WorkerFileIdentity;
  executableDescriptor: WorkerFileIdentity;
  workerEntryDescriptor: WorkerFileIdentity;
  argvWorkerEntry: WorkerFileIdentity | undefined;
  argvWorkerPath: string | undefined;
  homePath: WorkerFileIdentity;
  tempPath: WorkerFileIdentity;
}

function actualWorkerPreflight(): GovernedPiWorkerPreflight {
  const home = process.env.HOME;
  const temp = process.env.TMPDIR;
  if (!home || !temp || process.getuid === undefined) throw new Error("worker lacks Linux private-directory identity data");
  return {
    environment: process.env,
    nodeVersion: process.version,
    cwd: process.cwd(),
    uid: process.getuid(),
    cwdDescriptor: fstatSync(GOVERNED_PI_DESCRIPTOR_MAP.worker_exposure),
    cwdPath: lstatSync(process.cwd()),
    bundleDescriptor: fstatSync(GOVERNED_PI_DESCRIPTOR_MAP.runtime_bundle),
    bundlePath: statSync("/proc/self/fd/9"),
    executableDescriptor: fstatSync(GOVERNED_PI_DESCRIPTOR_MAP.executable),
    workerEntryDescriptor: statSync("/proc/self/fd/9/src/governed-pi-worker.ts"),
    argvWorkerEntry: process.argv[1] ? statSync(process.argv[1]) : undefined,
    argvWorkerPath: process.argv[1],
    homePath: lstatSync(home),
    tempPath: lstatSync(temp),
  };
}

export function validateGovernedPiBootstrap(value: unknown, actual: GovernedPiWorkerPreflight): GovernedPiBootstrap {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("worker bootstrap is not an object");
  const data = value as Record<string, unknown>;
  const keys = ["schema_version", "profile_id", "profile_digest", "source_binding_digest", "source_provenance_digest", "source_generation_id", "source_tree_digest", "source_manifest_digest", "worker_view_identity", "worker_mount_id", "runtime_bundle_digest", "descriptor_profile", "descriptor_profile_digest", "child_environment_digest", "node_version", "session_id", "workflow_id", "conversation_id", "private_cwd", "ipc_binding_token", "provider_id", "model_id", "pi_version", "tools", "tool_schema_sha256"];
  if (!exactKeys(data, keys) || data.schema_version !== GOVERNED_PI_WORKER_VERSION ||
      !validIdentity(data.profile_id) || !/^sha256:[0-9a-f]{64}$/u.test(String(data.profile_digest)) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.source_binding_digest)) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.source_provenance_digest)) ||
      !validIdentity(data.source_generation_id) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.source_tree_digest)) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.source_manifest_digest)) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.worker_view_identity)) ||
      !Number.isSafeInteger(data.worker_mount_id) || Number(data.worker_mount_id) <= 0 ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.runtime_bundle_digest)) ||
       data.descriptor_profile !== "tbound-pi-sdk-fdmap/v1;exec=3;exposure=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0" ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.descriptor_profile_digest)) ||
      !/^sha256:[0-9a-f]{64}$/u.test(String(data.child_environment_digest)) ||
       typeof data.node_version !== "string" || actual.nodeVersion !== data.node_version ||
       !validIdentity(data.session_id) || !validIdentity(data.workflow_id) || !validIdentity(data.conversation_id) ||
       !validIdentity(data.private_cwd, 4096) || data.private_cwd !== actual.cwd ||
      typeof data.ipc_binding_token !== "string" || !/^[0-9a-f]{64}$/u.test(data.ipc_binding_token) ||
      data.provider_id !== BROKER_PROVIDER_ID || data.model_id !== BROKER_MODEL_ID || data.pi_version !== "0.87.1" ||
      !Array.isArray(data.tools) || data.tools.length !== 4 ||
      data.tools.join("\0") !== "read\0write\0edit\0bash" ||
      !data.tool_schema_sha256 || typeof data.tool_schema_sha256 !== "object" || Array.isArray(data.tool_schema_sha256)) {
    throw new Error("worker bootstrap does not match the fixed admitted Pi profile");
  }
  const environmentNames = Object.keys(actual.environment).sort();
  if (environmentNames.join("\0") !== [...EXPECTED_ENVIRONMENT_NAMES].sort().join("\0") ||
      actual.environment.PI_OFFLINE !== "1" || actual.environment.LANG !== "C.UTF-8" ||
      !actual.environment.HOME || !actual.environment.TMPDIR || actual.environment.NODE_OPTIONS !== undefined || actual.environment.NODE_PATH !== undefined) {
    throw new Error("worker process environment is not the fixed minimal offline allowlist");
  }
  const environmentDigest = `sha256:${createHash("sha256").update(environmentNames.map((name) => `${name}=${actual.environment[name] ?? ""}`).sort().join("\0")).digest("hex")}`;
  if (environmentDigest !== data.child_environment_digest) throw new Error("worker environment digest differs from admitted host profile");
  if (!actual.cwdDescriptor.isDirectory() || !actual.cwdPath.isDirectory() || actual.cwdPath.isSymbolicLink() ||
      (actual.cwdPath.mode & 0o777) !== 0o700 || actual.cwdDescriptor.dev !== actual.cwdPath.dev || actual.cwdDescriptor.ino !== actual.cwdPath.ino ||
      !actual.bundleDescriptor.isDirectory() || !actual.bundlePath.isDirectory() || actual.bundleDescriptor.dev !== actual.bundlePath.dev || actual.bundleDescriptor.ino !== actual.bundlePath.ino ||
      !actual.workerEntryDescriptor.isFile() || !actual.argvWorkerEntry?.isFile() ||
      actual.workerEntryDescriptor.dev !== actual.argvWorkerEntry.dev || actual.workerEntryDescriptor.ino !== actual.argvWorkerEntry.ino || !actual.executableDescriptor.isFile() ||
      [actual.homePath, actual.tempPath].some((info) => !info.isDirectory() || info.isSymbolicLink() || (info.mode & 0o777) !== 0o700 || info.uid !== actual.uid)) {
    throw new Error("worker cwd/runtime bundle descriptor does not match the actual process launch");
  }
  const suppliedSchemas = data.tool_schema_sha256 as Record<string, unknown>;
  if (!exactKeys(suppliedSchemas, ["read", "write", "edit", "bash"]) ||
      Object.entries(TOOL_SCHEMA_SHA256).some(([name, digest]) => suppliedSchemas[name] !== digest)) {
    throw new Error("worker bootstrap tool schema profile differs from the pinned four-tool set");
  }
  return data as unknown as GovernedPiBootstrap;
}

function validateCurrentWorkerBootstrap(value: unknown): GovernedPiBootstrap {
  return validateGovernedPiBootstrap(value, actualWorkerPreflight());
}

function readExactlySync(fd: number, length: number): Buffer {
  const result = Buffer.allocUnsafe(length);
  let offset = 0;
  while (offset < length) {
    const count = readSync(fd, result, offset, length - offset, null);
    if (count === 0) throw new Error("worker bootstrap ended before its declared frame");
    offset += count;
  }
  return result;
}

function readBootstrap(): GovernedPiBootstrap {
  const prefix = readExactlySync(GOVERNED_PI_DESCRIPTOR_MAP.bootstrap, 4);
  const length = prefix.readUInt32BE(0);
  if (length === 0 || length > GOVERNED_PI_WORKER_CONTROL_MAX_BYTES) throw new Error("worker bootstrap length is invalid");
  const bytes = readExactlySync(GOVERNED_PI_DESCRIPTOR_MAP.bootstrap, length);
  const trailing = Buffer.allocUnsafe(1);
  if (readSync(GOVERNED_PI_DESCRIPTOR_MAP.bootstrap, trailing, 0, 1, null) !== 0) throw new Error("worker bootstrap contains trailing frames");
  const decoded: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  return validateCurrentWorkerBootstrap(decoded);
}

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(record[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

function schemaDigest(schema: unknown): string {
  return createHash("sha256").update(canonicalJson(JSON.parse(JSON.stringify(schema)))).digest("hex");
}

async function assertPinnedPackages(): Promise<void> {
  for (const packageName of ["@earendil-works/pi-coding-agent", "@earendil-works/pi-ai", "@earendil-works/pi-tui", "typebox"]) {
    const entry = fileURLToPath(import.meta.resolve(packageName));
    const packagePath = resolve(dirname(entry), "..", "package.json");
    const manifest = JSON.parse(await readFile(packagePath, "utf8")) as { name?: string; version?: string };
    const expectedVersion = packageName === "typebox" ? "1.3.27" : "0.87.1";
    if (manifest.name !== packageName || manifest.version !== expectedVersion) throw new Error("Pi package profile mismatch");
  }
}

async function createSession(bootstrap: GovernedPiBootstrap, ipc: IpcClient): Promise<WorkerServices> {
  await assertPinnedPackages();
  const broker = createBrokerProviderFromFD(GOVERNED_PI_DESCRIPTOR_MAP.provider_bridge);
  if (broker.provider.id !== bootstrap.provider_id || broker.model.id !== bootstrap.model_id) {
    broker.client.close();
    throw new Error("Go provider bridge identity differs from the admitted bootstrap");
  }
  const modelRuntime = await ModelRuntime.create({
    credentials: new MemoryCredentialStore(),
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  modelRuntime.registerNativeProvider(broker.provider);
  const closedRuntime = new ClosedBrokerModelRuntime(modelRuntime, broker.provider, broker.model);
  if (closedRuntime.getProviders().length !== 1 || closedRuntime.getModels().length !== 1 ||
      closedRuntime.getRegisteredProviderIds().length !== 1 || closedRuntime.getRegisteredProviderIds()[0] !== bootstrap.provider_id ||
      (await closedRuntime.listCredentials()).length !== 0) {
    broker.client.close();
    throw new Error("worker model runtime did not close to the inherited Go provider");
  }
  const settingsManager = SettingsManager.inMemory({
    defaultTools: [], extensions: [], packages: [], skills: [], prompts: [], themes: [],
    enableSkillCommands: false, enableAnalytics: false, enableInstallTelemetry: false,
    cacheWarming: "off", compaction: { enabled: false }, retry: { enabled: false },
  });
  const sessionManager = SessionManager.inMemory(bootstrap.private_cwd);
  const resourceLoader = new LockedResourceLoader();
  const created = await createAgentSessionFromServices({
    services: {
      cwd: bootstrap.private_cwd,
      agentDir: resolve(process.env.HOME!, ".tbound-governed-worker-agent"),
      modelRuntime: closedRuntime as unknown as ModelRuntime,
      settingsManager,
      resourceLoader,
      diagnostics: [] as AgentSessionRuntimeDiagnostic[],
    },
    sessionManager,
    model: broker.model,
    thinkingLevel: "off",
    noTools: "builtin",
    tools: [...declaredToolNames],
    customTools: createProxyTools(new IpcProposalSender(ipc)),
  });
  const active = created.session.getActiveToolNames().sort();
  const all = created.session.getAllTools().map((tool) => tool.name).sort();
  const executable = created.session.agent.state.tools.map((tool) => tool.name).sort();
  if (active.join("\0") !== "bash\0edit\0read\0write" || all.join("\0") !== active.join("\0") || executable.join("\0") !== active.join("\0")) {
    created.session.dispose();
    broker.client.close();
    throw new Error("Pi session tool inventory is not exactly the four proxy tools");
  }
  const schemas = Object.fromEntries(created.session.getAllTools().map((tool) => [tool.name, schemaDigest(tool.parameters)]));
  if (Object.entries(TOOL_SCHEMA_SHA256).some(([name, digest]) => schemas[name] !== digest)) {
    created.session.dispose();
    broker.client.close();
    throw new Error("Pi session proxy tool schema profile mismatch");
  }
  return {
    session: created.session,
    provider: broker.provider,
    model: broker.model,
    resourceLoader,
    modelRuntime,
    closeProvider: () => broker.client.close(),
  };
}

class FramedControlReader {
  readonly #iterator = process.stdin[Symbol.asyncIterator]();
  #buffer = Buffer.alloc(0);
  #ended = false;
  #sequence = 1;

  async #exact(length: number): Promise<Buffer | null> {
    while (this.#buffer.length < length) {
      const item = await this.#iterator.next();
      if (item.done) {
        this.#ended = true;
        if (this.#buffer.length === 0 && length === 4) return null;
        throw new Error("session control channel ended in a partial frame");
      }
      const bytes = typeof item.value === "string" ? Buffer.from(item.value, "utf8") : Buffer.from(item.value);
      this.#buffer = this.#buffer.length === 0 ? bytes : Buffer.concat([this.#buffer, bytes]);
      if (this.#buffer.length > GOVERNED_PI_WORKER_CONTROL_MAX_BYTES + 4) throw new Error("session control receive buffer exceeded bound");
    }
    const result = this.#buffer.subarray(0, length);
    this.#buffer = this.#buffer.subarray(length);
    return result;
  }

  async next(): Promise<GovernedPiControl | null> {
    if (this.#ended) return null;
    const prefix = await this.#exact(4);
    if (!prefix) return null;
    const length = prefix.readUInt32BE(0);
    if (length === 0 || length > GOVERNED_PI_WORKER_CONTROL_MAX_BYTES) throw new Error("session control frame length is invalid");
    const body = await this.#exact(length);
    if (!body) throw new Error("session control frame is missing its body");
    const parsed: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(body));
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("session control frame is not an object");
    const value = parsed as Record<string, unknown>;
    if (!Number.isSafeInteger(value.sequence) || value.sequence !== this.#sequence || this.#sequence >= Number.MAX_SAFE_INTEGER) {
      throw new Error("session control sequence is duplicate, stale, or exhausted");
    }
    this.#sequence += 1;
    if (value.type === "prompt" && exactKeys(value, ["type", "sequence", "request_id", "task_id", "text"]) &&
        validIdentity(value.request_id) && validIdentity(value.task_id, 128) && typeof value.text === "string" &&
        Buffer.byteLength(value.text, "utf8") > 0 && Buffer.byteLength(value.text, "utf8") <= GOVERNED_PI_WORKER_PROMPT_MAX_BYTES) return value as unknown as GovernedPiControl;
    if ((value.type === "abort" || value.type === "stop") && exactKeys(value, ["type", "sequence", "request_id"]) && validIdentity(value.request_id)) return value as unknown as GovernedPiControl;
    throw new Error("session control frame violates the fixed worker protocol");
  }
}

let eventWriteQueue = Promise.resolve();
let eventWriteFailure: unknown;
let eventSequence = 1;
let eventQueueCount = 0;
let eventQueueBytes = 0;
let eventTotalCount = 0;
let eventTotalBytes = 0;

function writeEventBytes(buffer: Buffer): Promise<void> {
  return new Promise((resolvePromise, reject) => {
    let offset = 0;
    const writeNext = (): void => {
      if (offset >= buffer.length) { resolvePromise(); return; }
      write(GOVERNED_PI_DESCRIPTOR_MAP.worker_events, buffer, offset, buffer.length - offset, null, (error, written) => {
        if (error) { reject(error); return; }
        if (written <= 0) { reject(new Error("worker event channel made no write progress")); return; }
        offset += written;
        writeNext();
      });
    };
    writeNext();
  });
}

function encodeEventFrame(event: GovernedPiWorkerEventPayload, sequence: number): Buffer {
  const bytes = Buffer.from(JSON.stringify({ ...event, sequence }), "utf8");
  if (bytes.length === 0 || bytes.length > GOVERNED_PI_WORKER_EVENT_MAX_BYTES) throw new Error("worker event exceeds its bound");
  const frame = Buffer.allocUnsafe(bytes.length + 4);
  frame.writeUInt32BE(bytes.length, 0);
  bytes.copy(frame, 4);
  return frame;
}

function enqueueEventFrames(frames: readonly Buffer[]): Promise<void> {
  if (eventWriteFailure) return Promise.reject(eventWriteFailure);
  const bytes = frames.reduce((total, frame) => total + frame.length, 0);
  if (frames.length === 0 || eventQueueCount + frames.length > GOVERNED_PI_WORKER_EVENT_QUEUE_MAX ||
      eventQueueBytes + bytes > GOVERNED_PI_WORKER_EVENT_QUEUE_BYTES) {
    return Promise.reject(new Error("bounded worker event queue is full"));
  }
  eventQueueCount += frames.length;
  eventQueueBytes += bytes;
  const current = eventWriteQueue.then(async () => {
    for (const frame of frames) await writeEventBytes(frame);
  }).catch((error: unknown) => {
    eventWriteFailure = error;
    throw error;
  }).finally(() => {
    eventQueueCount -= frames.length;
    eventQueueBytes -= bytes;
  });
  eventWriteQueue = current.catch(() => undefined);
  return current;
}

function emitEvent(event: GovernedPiWorkerEventPayload): Promise<void> {
  if (eventWriteFailure) return Promise.reject(eventWriteFailure);
  if (eventSequence >= Number.MAX_SAFE_INTEGER) return Promise.reject(new Error("worker event sequence exhausted"));
  const frame = encodeEventFrame(event, eventSequence);
  const bodyBytes = frame.length - 4;
  if (eventTotalCount >= GOVERNED_PI_WORKER_EVENT_TOTAL_MAX_COUNT ||
      eventTotalBytes > GOVERNED_PI_WORKER_EVENT_TOTAL_MAX_BYTES - bodyBytes) {
    eventWriteFailure = new Error("worker event stream budget exhausted");
    return Promise.reject(eventWriteFailure);
  }
  if (eventQueueCount + 1 > GOVERNED_PI_WORKER_EVENT_QUEUE_MAX ||
      eventQueueBytes + frame.length > GOVERNED_PI_WORKER_EVENT_QUEUE_BYTES) {
    eventWriteFailure = new Error("bounded worker event queue is full");
    return Promise.reject(eventWriteFailure);
  }
  eventTotalCount += 1;
  eventTotalBytes += bodyBytes;
  eventSequence += 1;
  return enqueueEventFrames([frame]);
}

export async function runGovernedPiWorker(): Promise<void> {
  const bootstrap = readBootstrap();
  const socket = new Socket({ fd: GOVERNED_PI_DESCRIPTOR_MAP.proposal_ipc, readable: true, writable: true, allowHalfOpen: true });
  const ipc = new IpcClient(socket, bootstrap.ipc_binding_token);
  let services: WorkerServices | undefined;
  let requestID: string | undefined;
  let promptTask: Promise<void> | undefined;
  let shuttingDown = false;
  try {
    services = await createSession(bootstrap, ipc);
    await emitEvent({
      type: "ready",
      schema_version: GOVERNED_PI_WORKER_VERSION,
      profile_id: bootstrap.profile_id,
      profile_digest: bootstrap.profile_digest,
      source_binding_digest: bootstrap.source_binding_digest,
      source_provenance_digest: bootstrap.source_provenance_digest,
      source_generation_id: bootstrap.source_generation_id,
      source_tree_digest: bootstrap.source_tree_digest,
      source_manifest_digest: bootstrap.source_manifest_digest,
      worker_view_identity: bootstrap.worker_view_identity,
      worker_mount_id: bootstrap.worker_mount_id,
      runtime_bundle_digest: bootstrap.runtime_bundle_digest,
      descriptor_profile: bootstrap.descriptor_profile,
      descriptor_profile_digest: bootstrap.descriptor_profile_digest,
      node_version: process.version,
      pi_version: "0.87.1",
      provider_id: BROKER_PROVIDER_ID,
      model_id: BROKER_MODEL_ID,
      tools: [...declaredToolNames],
      tool_schema_sha256: { ...TOOL_SCHEMA_SHA256 },
    });
    const controls = new FramedControlReader();
    for (;;) {
      const control = await controls.next();
      if (!control) break;
      if (control.type === "prompt") {
        if (promptTask || requestID) throw new Error("overlapping host prompts are forbidden");
        requestID = control.request_id;
        const activeRequest = control.request_id;
        promptTask = services.session.prompt(control.text, { expandPromptTemplates: false, source: "rpc" })
          .then(async () => { await emitEvent({ type: "turn_end", request_id: activeRequest }); })
          .catch(async () => {
            try { await emitEvent({ type: "worker_error", request_id: activeRequest, code: "PI_SESSION_TURN_FAILED" }); }
            catch { process.exitCode = 2; }
          })
          .finally(() => {
            requestID = undefined;
            promptTask = undefined;
          });
        continue;
      }
      if (control.type === "abort") {
        if (!promptTask || control.request_id !== requestID) throw new Error("abort request does not match the active prompt");
        await services.session.abort();
        continue;
      }
      if (control.type === "stop") {
        if (promptTask) {
          await services.session.abort();
          await promptTask;
        }
        shuttingDown = true;
        break;
      }
    }
  } finally {
    if (promptTask) {
      await services?.session.abort().catch(() => undefined);
      await promptTask.catch(() => undefined);
    }
    ipc.close();
    services?.closeProvider();
    if (services) services.session.dispose();
    if (shuttingDown) await emitEvent({ type: "stopped" });
    process.stdin.pause();
  }
}

const isGovernedWorkerMain = process.argv[1]?.endsWith("governed-pi-worker.ts") || process.argv[1]?.endsWith("governed-pi-worker.js");
if (isGovernedWorkerMain) {
  runGovernedPiWorker().catch(async () => {
    try { await emitEvent({ type: "worker_error", code: "WORKER_BOOTSTRAP_OR_RUNTIME_FAILED" }); } catch { /* event peer may already be closed */ }
    process.exitCode = 2;
  });
}
