/**
 * Native Pi 0.87.1 host seam.
 *
 * This file deliberately does not start Pi's stock process loop. InteractiveMode
 * is constructed and initialized with a caller-owned Terminal so the native TUI
 * remains the UI implementation. The stock fixture uses an inert terminal
 * input callback; the separately labeled private-policy fixture can exercise
 * only its fixed prompt/steer allowlist. Pi 0.87.1 has many other private
 * submit/action handlers, so production construction and unrestricted run()
 * remain refused.
 *
 * Fixture mode is explicitly offline, in-memory, no-effect, and non-claim-
 * bearing. Its faux provider still emits genuine Pi provider streams: tool-call
 * responses cause AgentSession to execute the four proxy tools, wait for their
 * structured IPC result, and request a real continuation response.
 */
import {
  createAgentSessionFromServices,
  createAgentSessionRuntime,
  InteractiveMode,
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type AgentSession,
  type AgentSessionEvent,
  type AgentSessionRuntime,
  type AgentSessionRuntimeDiagnostic,
  type ResourceLoader,
} from "@earendil-works/pi-coding-agent";
import {
  fauxAssistantMessage,
  fauxProvider,
  fauxToolCall,
  type Provider,
  type FauxResponseStep,
  type TranscriptContext,
  type JsonObject,
  type Model,
} from "@earendil-works/pi-ai";
import type { Terminal } from "@earendil-works/pi-tui";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { declaredToolNames, createProxyTools, IpcProposalSender, type ProposalSender } from "./proxy-tools.ts";
import {
  IpcClient,
  IpcTransportError,
  type IpcErrorCode,
  type IpcProposal,
  type IpcResult,
} from "./ipc-transport.ts";
import { LockedResourceLoader } from "./locked-resource-loader.ts";
import {
  loadNativePiPolicyInteractiveMode,
  type NativePiActionPolicy,
  type NativePiPolicyAction,
  type NativePiPolicyDecision,
  type PatchedInteractiveMode,
} from "./native-pi-policy.ts";

export const NATIVE_PI_HOST_VERSION = "pi-0.87.1" as const;
export const NATIVE_PI_FIXTURE_LABEL = "offline-fixture/non-claim-bearing/no-effects" as const;
export const NATIVE_PI_PRODUCTION_REFUSAL =
  "native Pi production host refused: provider stream and InteractiveMode action governance are not admitted";

const MAX_PROMPT_BYTES = 16 * 1024;
const FIXTURE_PROVIDER_REQUEST_LIMIT = 8;
const MAX_OBSERVATION_RECORDS = 1_024;
const MAX_OBSERVATION_BYTES = 256 * 1_024;
const PINNED_DEPENDENCY_VERSIONS = {
  "@earendil-works/pi-coding-agent": "0.87.1",
  "@earendil-works/pi-ai": "0.87.1",
  "@earendil-works/pi-tui": "0.87.1",
} as const;
const PINNED_TOOL_SCHEMA_SHA256 = {
  read: "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
  write: "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
  edit: "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
  bash: "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
} as const;
const AMBIENT_PROVIDER_ENV_NAMES = [
  "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN",
  "COPILOT_GITHUB_TOKEN",
  "OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_CLOUD_API_KEY",
  "GROQ_API_KEY", "XAI_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "NVIDIA_API_KEY",
  "CEREBRAS_API_KEY", "OPENROUTER_API_KEY", "RADIUS_API_KEY", "AI_GATEWAY_API_KEY",
  "ZAI_API_KEY", "ZAI_CODING_CN_API_KEY", "MINIMAX_API_KEY", "MINIMAX_CN_API_KEY",
  "MOONSHOT_API_KEY", "HF_TOKEN", "FIREWORKS_API_KEY", "TOGETHER_API_KEY",
  "BASETEN_API_KEY", "OPENCODE_API_KEY", "KIMI_API_KEY", "META_API_KEY",
  "ANT_LING_API_KEY", "QWEN_TOKEN_PLAN_API_KEY", "QWEN_TOKEN_PLAN_CN_API_KEY",
  "CLOUDFLARE_API_KEY", "XIAOMI_API_KEY", "XIAOMI_TOKEN_PLAN_CN_API_KEY",
  "XIAOMI_TOKEN_PLAN_AMS_API_KEY", "XIAOMI_TOKEN_PLAN_SGP_API_KEY", "LLAMA_API_KEY",
  "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT",
  "GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "AWS_PROFILE", "AWS_ACCESS_KEY_ID",
  "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
  "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_WEB_IDENTITY_TOKEN_FILE",
] as const;

let offlineInitTail: Promise<void> = Promise.resolve();

export type NativePiHostMode = "fixture" | "production";

export type NativePiObservation =
  | {
      type: "host_ready";
      mode: "fixture";
      pi_version: typeof NATIVE_PI_HOST_VERSION;
      label: typeof NATIVE_PI_FIXTURE_LABEL;
      tools: readonly string[];
      providers: readonly string[];
      models: readonly string[];
      credentials: number;
    }
  | {
      type: "agent_event";
      event: AgentSessionEvent["type"];
    }
  | {
      type: "tool_proposal";
      proposal: IpcProposal;
    }
  | {
      type: "tool_result";
      result: IpcResult;
    }
  | {
      type: "policy_decision";
      action: NativePiPolicyAction;
      decision: NativePiPolicyDecision;
    }
  | {
      type: "host_refusal";
      reason: string;
    }
  | {
      type: "ipc_error";
      operation: "proposal_request";
      tool_call_id: string;
      code: IpcErrorCode;
      aborted: boolean;
    }
  | {
      type: "fixture_provider_continuation";
      tool_call_id: string;
      tool: (typeof declaredToolNames)[number];
      preceding_result_verdict: "DENY";
    }
  | {
      type: "observation_gap";
      dropped_records: number;
      dropped_bytes: number;
    };

export type NativePiObservationSink = (observation: NativePiObservation) => void;

const ADMITTED_FIXTURE_SHORTCUTS = new Set([
  "app.interrupt",
  "app.interrupt_bash",
  "editor.clear_bash",
  "app.tools.expand",
  "app.thinking.toggle",
  "app.message.followUp",
]);

export function isNativePiFixtureActionAdmitted(action: NativePiPolicyAction): boolean {
  if (action.type === "submit") return action.semantic === "prompt" || action.semantic === "steer";
  return action.type === "shortcut" && ADMITTED_FIXTURE_SHORTCUTS.has(action.action);
}

export interface NativePiHostOptions {
  /** Explicit mode is required so fixture mode cannot silently become product mode. */
  mode: NativePiHostMode;
  /** Existing binding-token-authenticated TBound IPC client. */
  ipc: IpcClient;
  /** Cwd presented to Pi. SessionManager remains in-memory in fixture mode. */
  cwd: string;
  /** Caller-owned terminal; no ProcessTerminal is created implicitly. */
  terminal: Terminal;
  onObservation?: NativePiObservationSink;
  /** Set false only when the supervisor owns IPC lifetime. */
  closeIpcOnDispose?: boolean;
  /** Experimental private-patch policy; never accepted by production construction. */
  experimentalNativePolicy?: NativePiActionPolicy;
  /** Internal selector used only by createNativePiPolicyFixtureHost(). */
  experimentalPolicyArtifact?: boolean;
}

export class NativePiHostRefusal extends Error {
  readonly code = "ERR_NATIVE_PI_HOST_REFUSED" as const;

  constructor(message: string) {
    super(message);
    this.name = "NativePiHostRefusal";
  }
}

class MemoryCredentialStore {
  async read() {
    return undefined;
  }

  async list() {
    return [];
  }

  async modify(_providerId: string, update: (current: undefined) => Promise<undefined>) {
    return update(undefined);
  }

  async delete() {}
}

class BoundedObservationBuffer {
  readonly records: NativePiObservation[] = [];
  private bytes = 0;
  private droppedRecords = 0;
  private droppedBytes = 0;

  push(observation: NativePiObservation): void {
    const bytes = Buffer.byteLength(JSON.stringify(observation), "utf8");
    if (bytes > MAX_OBSERVATION_BYTES) {
      this.droppedRecords += 1;
      this.droppedBytes += bytes;
      return;
    }
    while (this.records.length >= MAX_OBSERVATION_RECORDS || this.bytes + bytes > MAX_OBSERVATION_BYTES) {
      const removed = this.records.shift();
      if (!removed) break;
      const removedBytes = Buffer.byteLength(JSON.stringify(removed), "utf8");
      this.bytes -= removedBytes;
      this.droppedRecords += 1;
      this.droppedBytes += removedBytes;
    }
    this.records.push(observation);
    this.bytes += bytes;
  }

  snapshot(): readonly NativePiObservation[] {
    const snapshot = structuredClone(this.records);
    if (this.droppedRecords === 0) return snapshot;
    return [
      ...snapshot,
      {
        type: "observation_gap",
        dropped_records: this.droppedRecords,
        dropped_bytes: this.droppedBytes,
      },
    ];
  }
}

export interface NativePiSessionView {
  readonly sessionId: string;
  readonly pendingMessageCount: number;
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

function argumentsDigest(arguments_: Record<string, unknown>): string {
  return `tbound-args-jcs-rfc8785/v1:sha256:${createHash("sha256").update(canonicalJson(arguments_)).digest("hex")}`;
}

export function validateNativePiDependencyProfile(
  manifests: readonly { name?: string; version?: string }[],
  schemaDigests: Readonly<Record<string, string>>,
): void {
  for (const [packageName, expectedVersion] of Object.entries(PINNED_DEPENDENCY_VERSIONS)) {
    const manifest = manifests.find((candidate) => candidate.name === packageName);
    if (!manifest || manifest.version !== expectedVersion) {
      throw new NativePiHostRefusal(`native Pi dependency version drifted for ${packageName}`);
    }
  }
  for (const toolName of declaredToolNames) {
    if (schemaDigests[toolName] !== PINNED_TOOL_SCHEMA_SHA256[toolName]) {
      throw new NativePiHostRefusal(`native Pi tool ${toolName} schema drifted from the pinned profile`);
    }
  }
}

async function assertPinnedRuntimeDependencies(): Promise<void> {
  const manifests: Array<{ name?: string; version?: string }> = [];
  for (const packageName of Object.keys(PINNED_DEPENDENCY_VERSIONS)) {
    const entry = fileURLToPath(import.meta.resolve(packageName));
    const packagePath = resolve(dirname(entry), "..", "package.json");
    let manifest: { name?: string; version?: string };
    try {
      manifest = JSON.parse(await readFile(packagePath, "utf8")) as typeof manifest;
    } catch (error) {
      throw new NativePiHostRefusal(`native Pi dependency manifest unavailable for ${packageName}: ${error instanceof Error ? error.message : String(error)}`);
    }
    manifests.push(manifest);
  }
  validateNativePiDependencyProfile(manifests, PINNED_TOOL_SCHEMA_SHA256);
}

export function assertNoAmbientProviderAuthority(): void {
  const present = AMBIENT_PROVIDER_ENV_NAMES.find((name) => Object.hasOwn(process.env, name));
  if (present) {
    throw new NativePiHostRefusal(`native Pi fixture refused ambient provider authority from ${present}; launch with a minimal child environment`);
  }
}

class ClosedFixtureModelRuntime {
  private readonly delegate: ModelRuntime;
  private readonly provider: Provider;
  private readonly model: Model<string>;

  constructor(
    delegate: ModelRuntime,
    provider: Provider,
    model: Model<string>,
  ) {
    this.delegate = delegate;
    this.provider = provider;
    this.model = model;
  }

  getProviders(): readonly Provider[] { return [this.provider]; }
  getProvider(providerId: string): Provider | undefined { return providerId === this.provider.id ? this.provider : undefined; }
  getModels(providerId?: string): readonly Model<string>[] { return !providerId || providerId === this.provider.id ? [this.model] : []; }
  getModel(providerId: string, modelId: string): Model<string> | undefined {
    return providerId === this.provider.id && modelId === this.model.id ? this.model : undefined;
  }
  getAvailableSnapshot(): readonly Model<string>[] { return [this.model]; }
  getError(): undefined { return undefined; }
  getRegisteredProviderIds(): readonly string[] { return [this.provider.id]; }
  getRegisteredNativeProvider(providerId: string): Provider | undefined { return this.getProvider(providerId); }
  hasConfiguredAuth(providerId: string): boolean { return providerId === this.provider.id; }
  isUsingOAuth(_providerId: string): boolean { return false; }
  isUsingSubscription(_providerId: string): boolean { return false; }
  getProviderAuthStatus(providerId: string): { configured: boolean; source?: string } { return { configured: providerId === this.provider.id, source: "fixture" }; }
  async checkAuth(providerId: string): Promise<unknown> { return providerId === this.provider.id ? this.delegate.checkAuth(providerId) : undefined; }
  async getAuth(providerOrModel: string | Model<string>, overrides?: unknown): Promise<unknown> {
    const providerId = typeof providerOrModel === "string" ? providerOrModel : providerOrModel.provider;
    if (providerId !== this.provider.id) return undefined;
    return this.delegate.getAuth(providerOrModel as never, overrides as never);
  }
  async getAvailable(providerId?: string): Promise<readonly Model<string>[]> { return !providerId || providerId === this.provider.id ? [this.model] : []; }
  async listCredentials(): Promise<readonly unknown[]> { return []; }
  async refresh(): Promise<{ aborted: false; errors: ReadonlyMap<string, Error> }> { return { aborted: false, errors: new Map() }; }
  stream(model: Model<string>, context: unknown, options?: unknown): unknown { this.assertModel(model); return this.delegate.stream(model, context as never, options as never); }
  complete(model: Model<string>, context: unknown, options?: unknown): Promise<unknown> { this.assertModel(model); return this.delegate.complete(model, context as never, options as never); }
  streamSimple(model: Model<string>, context: unknown, options?: unknown): unknown { this.assertModel(model); return this.delegate.streamSimple(model, context as never, options as never); }
  completeSimple(model: Model<string>, context: unknown, options?: unknown): Promise<unknown> { this.assertModel(model); return this.delegate.completeSimple(model, context as never, options as never); }
  getCompatibilityRequestConfig(): undefined { return undefined; }
  registerNativeProvider(): never { throw new NativePiHostRefusal("native Pi fixture provider inventory is immutable"); }
  registerProvider(): never { throw new NativePiHostRefusal("native Pi fixture provider inventory is immutable"); }
  unregisterProvider(): never { throw new NativePiHostRefusal("native Pi fixture provider inventory is immutable"); }
  login(): never { throw new NativePiHostRefusal("native Pi fixture authentication is disabled"); }
  logout(): never { throw new NativePiHostRefusal("native Pi fixture authentication is disabled"); }
  setRuntimeApiKey(): never { throw new NativePiHostRefusal("native Pi fixture authentication is disabled"); }
  removeRuntimeApiKey(): never { throw new NativePiHostRefusal("native Pi fixture authentication is disabled"); }

  private assertModel(model: Model<string>): void {
    if (model.provider !== this.provider.id || model.id !== this.model.id) {
      throw new NativePiHostRefusal("native Pi fixture rejected a model outside the closed provider inventory");
    }
  }
}

class ObservedProposalSender implements ProposalSender {
  private readonly delegate: IpcProposalSender;
  private readonly observe: NativePiObservationSink;

  constructor(
    delegate: IpcProposalSender,
    observe: NativePiObservationSink,
  ) {
    this.delegate = delegate;
    this.observe = observe;
  }

  async send(proposal: IpcProposal, signal?: AbortSignal): Promise<IpcResult> {
    this.observe({ type: "tool_proposal", proposal: structuredClone(proposal) });
    try {
      const result = await this.delegate.send(proposal, signal);
      if (result.canonical_arguments_digest !== argumentsDigest(proposal.arguments)) {
        throw new IpcTransportError("ERR_IPC_CORRELATION_MISMATCH", "IPC result arguments digest does not match the proposal");
      }
      this.observe({ type: "tool_result", result: structuredClone(result) });
      return result;
    } catch (error) {
      const ipcError = error instanceof IpcTransportError
        ? error
        : new IpcTransportError("ERR_IPC_IO", "IPC proposal request failed", { cause: error });
      this.observe({
        type: "ipc_error",
        operation: "proposal_request",
        tool_call_id: proposal.tool_call_id,
        code: ipcError.code,
        aborted: ipcError.code === "ERR_IPC_ABORTED",
      });
      throw error;
    }
  }
}

function assertResourcesLocked(loader: ResourceLoader): void {
  const extensions = loader.getExtensions();
  const skills = loader.getSkills();
  const prompts = loader.getPrompts();
  const themes = loader.getThemes();
  const agents = loader.getAgentsFiles();

  if (extensions.extensions.length !== 0 || extensions.errors.length !== 0 ||
      skills.skills.length !== 0 || skills.diagnostics.length !== 0 ||
      prompts.prompts.length !== 0 || prompts.diagnostics.length !== 0 ||
      themes.themes.length !== 0 || themes.diagnostics.length !== 0 ||
      agents.agentsFiles.length !== 0 || loader.getSystemPrompt() !== undefined ||
      loader.getSystemPromptSource() !== undefined || loader.getAppendSystemPrompt().length !== 0 ||
      loader.getAppendSystemPromptSources().length !== 0) {
    throw new NativePiHostRefusal("native Pi resource inventory is not closed");
  }
}

function assertToolsClosed(session: AgentSession): void {
  const expected = [...declaredToolNames].sort();
  const inventory = session.getAllTools();
  const actual = inventory.map((tool) => tool.name).sort();
  const active = session.getActiveToolNames().sort();
  const executable = session.agent.state.tools.map((tool) => tool.name).sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected) ||
      JSON.stringify(active) !== JSON.stringify(expected) ||
      JSON.stringify(executable) !== JSON.stringify(expected)) {
    throw new NativePiHostRefusal("native Pi tool inventory is not exactly read/write/edit/bash");
  }
  for (const tool of inventory) {
    if (tool.sourceInfo.source !== "sdk" || tool.sourceInfo.path !== `<sdk:${tool.name}>`) {
      throw new NativePiHostRefusal(`native Pi tool ${tool.name} is not an SDK proxy definition`);
    }
    const expectedSchemaDigest = PINNED_TOOL_SCHEMA_SHA256[tool.name as keyof typeof PINNED_TOOL_SCHEMA_SHA256];
    if (!expectedSchemaDigest || schemaDigest(tool.parameters) !== expectedSchemaDigest) {
      throw new NativePiHostRefusal(`native Pi tool ${tool.name} schema drifted from the pinned profile`);
    }
  }
}

function fixtureResponses(onContinuation?: (tool: (typeof declaredToolNames)[number], toolCallId: string) => void): FauxResponseStep[] {
  const scripts: Array<{
    tool: (typeof declaredToolNames)[number];
    id: string;
    response: string;
    args: JsonObject;
  }> = [
    { tool: "read", id: "native-fixture-read-1", response: "native-fixture-read-result", args: { path: "fixture/read.txt", offset: 1, limit: 4 } },
    { tool: "write", id: "native-fixture-write-1", response: "native-fixture-write-result", args: { path: "fixture/write.txt", content: "fixture write" } },
    { tool: "edit", id: "native-fixture-edit-1", response: "native-fixture-edit-result", args: { path: "fixture/edit.txt", edits: [{ oldText: "before", newText: "after" }] } },
    { tool: "bash", id: "native-fixture-bash-1", response: "native-fixture-bash-result", args: { command: "echo fixture", timeout: 1 } },
  ];
  const responses: FauxResponseStep[] = [];
  for (const script of scripts) {
    responses.push(
      fauxAssistantMessage(fauxToolCall(script.tool, script.args, { id: script.id }), {
        stopReason: "toolUse",
        responseId: script.response,
      }),
      (context: TranscriptContext) => {
        const result = [...context.messages]
          .reverse()
          .find((message) => message.role === "toolResult" && message.toolCallId === script.id);
        const contentIncludesDeny = Array.isArray(result?.content) && result.content.some((content) => content.type === "text" && content.text.includes(`\"tool_call_id\":\"${script.id}\"`) && content.text.includes("\"verdict\":\"DENY\""));
        if (!result || !contentIncludesDeny) {
          throw new Error(`native Pi fixture continuation did not consume the DENY result for ${script.id}`);
        }
        onContinuation?.(script.tool, script.id);
        return fauxAssistantMessage(
          `native Pi fixture continuation after ${script.tool} (non-claim-bearing)\n<script>hostile content is text</script>`,
          { responseId: `${script.response}-final` },
        );
      },
    );
  }
  return responses;
}

function assertFixtureModelRuntime(modelRuntime: ClosedFixtureModelRuntime, providerId: string, provider: unknown): void {
  const providers = modelRuntime.getRegisteredProviderIds();
  if (providers.length !== 1 || providers[0] !== providerId ||
      modelRuntime.getRegisteredNativeProvider(providerId) !== provider ||
      modelRuntime.getProviders().length !== 1 || modelRuntime.getProviders()[0] !== provider ||
      modelRuntime.getAvailableSnapshot().length !== 1) {
    throw new NativePiHostRefusal("fixture model runtime provider inventory is not closed");
  }
}

function validPrompt(text: string): void {
  const bytes = Buffer.byteLength(text, "utf8");
  if (bytes === 0 || bytes > MAX_PROMPT_BYTES) {
    throw new NativePiHostRefusal("native Pi prompt is empty or exceeds the configured bound");
  }
}

function createRenderOnlyTerminal(terminal: Terminal): Terminal {
  return {
    start: (_onInput, onResize) => terminal.start(() => {}, onResize),
    stop: () => terminal.stop(),
    drainInput: (maxMs, idleMs) => terminal.drainInput(maxMs, idleMs),
    write: (data) => terminal.write(data),
    get columns() {
      return terminal.columns;
    },
    get rows() {
      return terminal.rows;
    },
    get kittyProtocolActive() {
      return terminal.kittyProtocolActive;
    },
    moveBy: (lines) => terminal.moveBy(lines),
    hideCursor: () => terminal.hideCursor(),
    showCursor: () => terminal.showCursor(),
    clearLine: () => terminal.clearLine(),
    clearFromCursor: () => terminal.clearFromCursor(),
    clearScreen: () => terminal.clearScreen(),
    setTitle: (title) => terminal.setTitle(title),
    setProgress: (active) => terminal.setProgress(active),
  };
}

/** Serialize the process-global Pi offline flag across concurrent fixture init calls. */
async function withOfflinePiInitialization<T>(task: () => Promise<T>): Promise<T> {
  const predecessor = offlineInitTail;
  let release!: () => void;
  offlineInitTail = new Promise<void>((resolve) => {
    release = resolve;
  });
  await predecessor;

  const previousOffline = process.env.PI_OFFLINE;
  process.env.PI_OFFLINE = "1";
  try {
    return await task();
  } finally {
    if (previousOffline === undefined) delete process.env.PI_OFFLINE;
    else process.env.PI_OFFLINE = previousOffline;
    release();
  }
}

/**
 * A native InteractiveMode host. `init()` is a render-only/native-TUI seam;
 * `run()` intentionally refuses until built-in action governance is admitted.
 */
type NativeInteractiveModeInstance = Pick<InteractiveMode, "init" | "stop" | "getUserInput"> | PatchedInteractiveMode;

export class NativePiHost {
  readonly mode: "fixture";

  #runtime: AgentSessionRuntime;
  #interactiveMode: NativeInteractiveModeInstance;
  #terminal: Terminal;
  #nativeInputEnabled: boolean;
  #ipc: IpcClient;
  #closeIpcOnDispose: boolean;
  #sink?: NativePiObservationSink;
  #sessionUnsubscribers: Set<() => void>;
  #observationState: BoundedObservationBuffer;
  #initialized = false;
  #disposed = false;
  #disposeRequested = false;
  #initPromise?: Promise<void>;
  #disposePromise?: Promise<void>;
  #lifecycleAbort = new AbortController();

  private constructor(
    runtime: AgentSessionRuntime,
    interactiveMode: NativeInteractiveModeInstance,
    model: Model<string>,
    ipc: IpcClient,
    options: NativePiHostOptions,
    observations: BoundedObservationBuffer,
    sessionUnsubscribers: Set<() => void>,
  ) {
    this.#runtime = runtime;
    this.#interactiveMode = interactiveMode;
    this.#terminal = options.terminal;
    this.#nativeInputEnabled = options.experimentalPolicyArtifact === true;
    this.#ipc = ipc;
    this.#closeIpcOnDispose = options.closeIpcOnDispose ?? true;
    this.#sink = options.onObservation;
    this.mode = "fixture";
    this.#observationState = observations;
    this.#sessionUnsubscribers = sessionUnsubscribers;
  }

  static async create(options: NativePiHostOptions): Promise<NativePiHost> {
    if (options.mode !== "fixture") {
      try {
        options.onObservation?.({ type: "host_refusal", reason: NATIVE_PI_PRODUCTION_REFUSAL });
      } catch {
        // Refusal remains authoritative even when telemetry is broken.
      }
      throw new NativePiHostRefusal(NATIVE_PI_PRODUCTION_REFUSAL);
    }
    if (!options.ipc) throw new NativePiHostRefusal("native Pi host requires the existing authenticated IPC client");
    if (!options.terminal) throw new NativePiHostRefusal("native Pi host requires a caller-owned terminal");
    await assertPinnedRuntimeDependencies();
    assertNoAmbientProviderAuthority();
    const policyInteractiveMode = options.experimentalPolicyArtifact || options.experimentalNativePolicy
      ? await loadNativePiPolicyInteractiveMode()
      : undefined;
    const cwd = resolve(options.cwd);
    const observations = new BoundedObservationBuffer();
    const emitObservation: NativePiObservationSink = (observation) => {
      let recorded: NativePiObservation;
      try {
        recorded = structuredClone(observation);
      } catch {
        return;
      }
      observations.push(recorded);
      try {
        options.onObservation?.(structuredClone(recorded));
      } catch {
        // Observation is non-authoritative and must not affect the host.
      }
    };
    const observed = new ObservedProposalSender(
      new IpcProposalSender(options.ipc),
      emitObservation,
    );
    const faux = fauxProvider({
      provider: "tbound-native-fixture",
      api: "faux",
      models: [{ id: "tbound-native-fixture-model", name: "TBound native Pi fixture" }],
      tokensPerSecond: 0,
    });
    const responses = fixtureResponses((tool, toolCallId) => {
      emitObservation({
        type: "fixture_provider_continuation",
        tool_call_id: toolCallId,
        tool,
        preceding_result_verdict: "DENY",
      });
    });
    if (responses.length !== FIXTURE_PROVIDER_REQUEST_LIMIT) {
      throw new NativePiHostRefusal("native Pi fixture response budget drifted");
    }
    faux.setResponses(responses);

    const modelRuntime = await ModelRuntime.create({
      credentials: new MemoryCredentialStore(),
      modelsPath: null,
      allowModelNetwork: false,
      refreshOnCreate: false,
    });
    modelRuntime.registerNativeProvider(faux.provider);
    const closedModelRuntime = new ClosedFixtureModelRuntime(modelRuntime, faux.provider, faux.getModel());
    assertFixtureModelRuntime(closedModelRuntime, faux.provider.id, faux.provider);
    if ((await closedModelRuntime.listCredentials()).length !== 0) {
      throw new NativePiHostRefusal("native Pi fixture credential inventory is not empty");
    }
    const model = faux.getModel();
    const settingsManager = SettingsManager.inMemory({
      defaultTools: [],
      extensions: [],
      packages: [],
      skills: [],
      prompts: [],
      themes: [],
      enableSkillCommands: false,
      enableAnalytics: false,
      enableInstallTelemetry: false,
      cacheWarming: "off",
      compaction: { enabled: false },
      retry: { enabled: false },
      quietStartup: true,
    });
    const sessionManager = SessionManager.inMemory(cwd);
    const agentDir = resolve(cwd, ".tbound-native-pi-fixture-agent-do-not-persist");
    const sessionUnsubscribers = new Set<() => void>();
    const createSession = async (factoryOptions: {
      cwd: string;
      agentDir: string;
      sessionManager: SessionManager;
      sessionStartEvent?: { type: "session_start"; reason: "startup" | "reload" | "new" | "resume" | "fork" };
    }) => {
      if (resolve(factoryOptions.cwd) !== cwd ||
          resolve(factoryOptions.agentDir) !== agentDir ||
          factoryOptions.sessionManager !== sessionManager) {
        throw new NativePiHostRefusal("native Pi fixture session target drifted outside the closed runtime");
      }
      const resourceLoader = new LockedResourceLoader();
      assertResourcesLocked(resourceLoader);
      const created = await createAgentSessionFromServices({
        services: {
          cwd: factoryOptions.cwd,
          agentDir: factoryOptions.agentDir,
          modelRuntime: closedModelRuntime as unknown as ModelRuntime,
          settingsManager,
          resourceLoader,
          diagnostics: [] as AgentSessionRuntimeDiagnostic[],
        },
        sessionManager: factoryOptions.sessionManager,
        sessionStartEvent: factoryOptions.sessionStartEvent,
        model,
        thinkingLevel: "off",
        noTools: "builtin",
        tools: [...declaredToolNames],
        customTools: createProxyTools(observed),
      });
      assertResourcesLocked(resourceLoader);
      assertToolsClosed(created.session);
      sessionUnsubscribers.add(created.session.subscribe((event) => {
        emitObservation({ type: "agent_event", event: event.type });
      }));
      return {
        ...created,
        services: {
          cwd: factoryOptions.cwd,
          agentDir: factoryOptions.agentDir,
          modelRuntime: closedModelRuntime as unknown as ModelRuntime,
          settingsManager,
          resourceLoader,
          diagnostics: [] as AgentSessionRuntimeDiagnostic[],
        },
        diagnostics: [] as AgentSessionRuntimeDiagnostic[],
      };
    };
    const runtime = await createAgentSessionRuntime(createSession, {
      cwd,
      agentDir,
      sessionManager,
    });
    assertToolsClosed(runtime.session);
    const configuredPolicy = options.experimentalNativePolicy;
    const policy = options.experimentalPolicyArtifact || configuredPolicy
      ? async (action: NativePiPolicyAction): Promise<NativePiPolicyDecision> => {
          let decision: NativePiPolicyDecision;
          if (!isNativePiFixtureActionAdmitted(action)) {
            decision = { allow: false, reason: `fixture action is outside the fixed admitted set: ${action.type}` };
          } else if (action.type === "submit" &&
              (Buffer.byteLength(action.text, "utf8") === 0 || Buffer.byteLength(action.text, "utf8") > MAX_PROMPT_BYTES)) {
            decision = { allow: false, reason: "native prompt is empty or exceeds the fixture prompt bound" };
          } else if (!configuredPolicy) {
            decision = { allow: false, reason: `fixture action refused: no caller policy for ${action.type}` };
          } else {
            try {
              const callbackAction = Object.freeze(structuredClone(action));
              const candidate: unknown = await configuredPolicy(callbackAction);
              if (candidate === null || typeof candidate !== "object" ||
                  typeof (candidate as { allow?: unknown }).allow !== "boolean" ||
                  ((candidate as { reason?: unknown }).reason !== undefined && typeof (candidate as { reason?: unknown }).reason !== "string")) {
                decision = { allow: false, reason: "native policy returned a malformed decision" };
              } else {
                const suppliedReason = (candidate as { reason?: string }).reason;
                decision = {
                  allow: (candidate as { allow: boolean }).allow,
                  ...(suppliedReason === undefined ? {} : { reason: suppliedReason.slice(0, 512) }),
                };
              }
            } catch (error) {
              decision = {
                allow: false,
                reason: `native policy callback failed: ${error instanceof Error ? error.message : String(error)}`,
              };
            }
          }
          try {
            emitObservation({ type: "policy_decision", action: structuredClone(action), decision: structuredClone(decision) });
          } catch {
            // Invalid telemetry payloads are dropped; the decision remains authoritative.
          }
          return decision;
        }
      : undefined;
    const interactiveMode = policyInteractiveMode
      ? new policyInteractiveMode(runtime, {
          terminal: options.terminal,
          verbose: false,
          tuiMode: "regular",
          startupDiagnostics: [],
          actionPolicy: policy,
        })
      : new InteractiveMode(runtime, {
          terminal: createRenderOnlyTerminal(options.terminal),
          verbose: false,
          tuiMode: "regular",
          startupDiagnostics: [],
        });
    const host = new NativePiHost(
      runtime,
      interactiveMode,
      model,
      options.ipc,
      options,
      observations,
      sessionUnsubscribers,
    );
    host.emit({
      type: "host_ready",
      mode: "fixture",
      pi_version: NATIVE_PI_HOST_VERSION,
      label: NATIVE_PI_FIXTURE_LABEL,
      tools: [...declaredToolNames],
      providers: [...closedModelRuntime.getRegisteredProviderIds()],
      models: [...closedModelRuntime.getAvailableSnapshot()].map((availableModel) => `${availableModel.provider}/${availableModel.id}`),
      credentials: 0,
    });
    return host;
  }

  get sessionView(): NativePiSessionView {
    this.assertUsable();
    const thisHost = this;
    return Object.freeze({
      get sessionId() {
        thisHost.assertUsable();
        return thisHost.#runtime.session.sessionManager.getSessionId();
      },
      get pendingMessageCount() {
        thisHost.assertUsable();
        return thisHost.#runtime.session.pendingMessageCount;
      },
    });
  }

  get observations(): readonly NativePiObservation[] {
    return this.#observationState.snapshot();
  }

  /**
   * Initialize and render real pinned InteractiveMode with offline fd/rg
   * bootstrap. Stock fixture input is inert; the explicit experimental policy
   * fixture uses the private patched copy and fixed action allowlist.
   */
  async init(): Promise<void> {
    this.assertUsable();
    if (this.#initialized) return;
    if (this.#initPromise) return this.#initPromise;
    const initialization = withOfflinePiInitialization(async () => {
      if (this.#disposeRequested || this.#disposed) {
        throw new NativePiHostRefusal("native Pi host initialization was cancelled by disposal");
      }
      try {
        await this.#interactiveMode.init();
        if (this.#disposeRequested || this.#disposed) {
          throw new NativePiHostRefusal("native Pi host initialization was cancelled by disposal");
        }
        this.#initialized = true;
      } catch (error) {
        this.#initialized = false;
        this.safeStopInteractiveMode();
        throw error;
      }
    });
    this.#initPromise = initialization.finally(() => {
      this.#initPromise = undefined;
    });
    return this.#initPromise;
  }

  /**
   * Refuses the unrestricted InteractiveMode loop. Calling stock run() would
   * make private built-in action handlers authoritative before host observation.
   */
  async run(): Promise<never> {
    this.refuse(NATIVE_PI_PRODUCTION_REFUSAL);
    throw new NativePiHostRefusal(NATIVE_PI_PRODUCTION_REFUSAL);
  }

  async prompt(text: string): Promise<void> {
    this.assertInitialized();
    validPrompt(text);
    await this.#runtime.session.prompt(text, { expandPromptTemplates: false, source: "interactive" });
  }

  /**
   * Await one real native-editor prompt in the explicitly experimental patched
   * fixture. The stock/inert host refuses this seam; unsupported submissions are
   * denied by the patched policy and do not resolve this prompt waiter.
   */
  async nativePrompt(): Promise<void> {
    this.assertInitialized();
    if (!this.#nativeInputEnabled) {
      throw new NativePiHostRefusal("native Pi terminal input requires the experimental pinned policy artifact");
    }
    const inputPromise = this.#interactiveMode.getUserInput();
    let removeAbortListener: (() => void) | undefined;
    const aborted = new Promise<never>((_resolve, reject) => {
      const onAbort = (): void => reject(this.#lifecycleAbort.signal.reason ?? new NativePiHostRefusal("native prompt cancelled by disposal"));
      if (this.#lifecycleAbort.signal.aborted) onAbort();
      else this.#lifecycleAbort.signal.addEventListener("abort", onAbort, { once: true });
      removeAbortListener = () => this.#lifecycleAbort.signal.removeEventListener("abort", onAbort);
    });
    let text: string;
    try {
      text = await Promise.race([inputPromise, aborted]);
    } finally {
      removeAbortListener?.();
    }
    this.assertInitialized();
    validPrompt(text);
    await this.#runtime.session.prompt(text, { expandPromptTemplates: false, source: "interactive" });
  }

  async steer(text: string): Promise<void> {
    this.assertInitialized();
    validPrompt(text);
    await this.#runtime.session.steer(text, undefined, { source: "interactive" });
  }

  async followUp(text: string): Promise<void> {
    this.assertInitialized();
    validPrompt(text);
    await this.#runtime.session.followUp(text, undefined, { source: "interactive" });
  }

  stop(): void {
    if (this.#disposed) return;
    this.safeStopInteractiveMode();
    this.#initialized = false;
  }

  async dispose(): Promise<void> {
    if (this.#disposePromise) return this.#disposePromise;
    if (this.#disposed) return;
    this.#disposeRequested = true;
    this.#lifecycleAbort.abort(new NativePiHostRefusal("native prompt cancelled by disposal"));
    this.#disposePromise = (async () => {
      try {
        await this.#initPromise?.catch(() => undefined);
        this.safeStopInteractiveMode();
        this.#initialized = false;
        for (const unsubscribe of this.#sessionUnsubscribers) unsubscribe();
        this.#sessionUnsubscribers.clear();
        await this.#runtime.dispose();
      } finally {
        this.#disposed = true;
        if (this.#closeIpcOnDispose) this.#ipc.close();
      }
    })();
    return this.#disposePromise;
  }

  private emit(observation: NativePiObservation): void {
    let recorded: NativePiObservation;
    try {
      recorded = structuredClone(observation);
    } catch {
      return;
    }
    this.#observationState.push(recorded);
    try {
      this.#sink?.(structuredClone(recorded));
    } catch {
      // Observation is a separate, non-authoritative channel. Sink failures
      // must never alter tool authorization or agent control flow.
    }
  }

  private refuse(reason: string): void {
    this.emit({ type: "host_refusal", reason });
  }

  private assertUsable(): void {
    if (this.#disposed || this.#disposeRequested) throw new NativePiHostRefusal("native Pi host is disposed or disposing");
  }

  private assertInitialized(): void {
    this.assertUsable();
    if (!this.#initialized) throw new NativePiHostRefusal("native Pi host UI is not initialized");
  }

  private safeStopInteractiveMode(): void {
    try {
      this.#interactiveMode.stop();
    } catch {
      // The caller-owned terminal is still stopped below; cleanup is fail-closed.
    }
    try {
      this.#terminal.stop();
    } catch {
      // Terminal cleanup is best effort and cannot restore authority.
    }
  }
}

export async function createNativePiHost(options: NativePiHostOptions): Promise<NativePiHost> {
  return NativePiHost.create(options);
}

/** Explicit alias used by fixture-only tests and non-claiming demonstrations. */
export async function createNativePiFixtureHost(
  options: Omit<NativePiHostOptions, "mode">,
): Promise<NativePiHost> {
  return createNativePiHost({ ...options, mode: "fixture" });
}

/** Explicit experimental alias using only the separately pinned private patch. */
export async function createNativePiPolicyFixtureHost(
  options: Omit<NativePiHostOptions, "mode" | "experimentalNativePolicy"> & {
    actionPolicy?: NativePiActionPolicy;
  },
): Promise<NativePiHost> {
  return createNativePiHost({
    ...options,
    mode: "fixture",
    experimentalNativePolicy: options.actionPolicy,
    experimentalPolicyArtifact: true,
  });
}

/** Production is intentionally an explicit refusal, not an SDK/browser fallback. */
export async function createNativePiProductionHost(
  options: Omit<NativePiHostOptions, "mode">,
): Promise<never> {
  try {
    options.onObservation?.({ type: "host_refusal", reason: NATIVE_PI_PRODUCTION_REFUSAL });
  } catch {
    // Refusal remains authoritative even when telemetry is broken.
  }
  throw new NativePiHostRefusal(NATIVE_PI_PRODUCTION_REFUSAL);
}
