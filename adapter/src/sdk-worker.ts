/**
 * Restricted Pi 0.87.1 SDK worker.
 *
 * This is an in-process SDK bridge, not Pi RPC/TUI. The only registered tools
 * are the four existing proxy tools and their proposals use the existing
 * tbound IPC transport. Fixture mode is explicit and non-claim-bearing: the
 * faux provider genuinely emits the scripted tool calls and final turns, while
 * no provider network or credential store is consulted.
 */
import { createInterface } from "node:readline";
import { createConnection, type Socket } from "node:net";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  createAgentSession,
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type AgentSession,
  type ResourceLoader,
} from "@earendil-works/pi-coding-agent";
import {
  fauxAssistantMessage,
  fauxToolCall,
  fauxProvider,
  type AssistantMessageEvent,
  type JsonObject,
  type Model,
} from "@earendil-works/pi-ai";
import { LockedResourceLoader } from "./locked-resource-loader.ts";
import {
  createProxyTools,
  IpcProposalSender,
  declaredToolNames,
  type ProposalSender,
} from "./proxy-tools.ts";
import { IpcClient } from "./ipc-transport.ts";

export const SDK_WORKER_MODE = "fixture" as const;
export const SDK_WORKER_VERSION = "pi-0.87.1" as const;
const MAX_LINE_BYTES = 1 << 20;

function installedPackageVersion(packageName: string): string {
  const entryPath = fileURLToPath(import.meta.resolve(packageName));
  const packagePath = resolve(dirname(entryPath), "..", "package.json");
  const packageJSON = JSON.parse(readFileSync(packagePath, "utf8")) as { version?: unknown };
  if (typeof packageJSON.version !== "string") throw new Error(`${packageName} package version is unavailable`);
  return packageJSON.version;
}

export function verifyInstalledSDKProfile(): void {
  for (const packageName of ["@earendil-works/pi-coding-agent", "@earendil-works/pi-ai"]) {
    if (installedPackageVersion(packageName) !== "0.87.1") {
      throw new Error(`${packageName} is not the pinned Pi 0.87.1 dependency`);
    }
  }
}

type WorkerOutput = {
  type: "ready" | "text_delta" | "turn_end" | "error" | "stopped";
  request_id?: string;
  text?: string;
  error?: string;
  model?: { provider: string; id: string };
  tools?: readonly string[];
};

export type FixtureSessionOptions = {
  cwd: string;
  sender: ProposalSender;
  onTextDelta?: (text: string) => void;
};

export type FixtureSession = {
  session: AgentSession;
  model: Model<string>;
  prompt(text: string): Promise<void>;
  dispose(): void;
};

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

function emptyResources(): ResourceLoader {
  return new LockedResourceLoader();
}

/**
 * Creates a real AgentSession and streams a real SDK provider stream. The
 * provider is faux only so tests cannot accidentally make a network request.
 */
export async function createFixtureSession(options: FixtureSessionOptions): Promise<FixtureSession> {
  verifyInstalledSDKProfile();
  const faux = fauxProvider({
    provider: "tbound-fixture",
    api: "faux",
    models: [{ id: "tbound-fixture-model", name: "TBound fixture model" }],
    tokensPerSecond: 0,
  });
  const fixtureResponse = (_context: any, _requestOptions: any, state: { callCount: number }, _model: Model<string>) => {
      const scripts: Array<{ tool: "read" | "write" | "edit" | "bash"; id: string; response: string; args: JsonObject }> = [
        { tool: "read" as const, id: "fixture-call-read-1", response: "fixture-response-1", args: { path: "fixture/read.txt", offset: 1, limit: 4 } },
        { tool: "write" as const, id: "fixture-call-write-1", response: "fixture-response-2", args: { path: "fixture/write.txt", content: "fixture write" } },
        { tool: "edit" as const, id: "fixture-call-edit-1", response: "fixture-response-3", args: { path: "fixture/edit.txt", edits: [{ oldText: "before", newText: "after" }] } },
        { tool: "bash" as const, id: "fixture-call-bash-1", response: "fixture-response-4", args: { command: "echo fixture", timeout: 1 } },
      ];
      const requestIndex = state.callCount - 1;
      const script = scripts[Math.floor(requestIndex / 2)];
      if (script && requestIndex % 2 === 0) {
        return fauxAssistantMessage(fauxToolCall(script.tool, script.args, { id: script.id }), {
          stopReason: "toolUse",
          responseId: script.response,
        });
      }
      const completed = script ?? scripts.at(-1)!;
      return fauxAssistantMessage(
        `fixture final after ${completed.tool} (non-claim-bearing)\n<script>hostile content is text</script>`,
        { responseId: `${completed.response}-final` },
      );
  };
  // Each prompt consumes a tool-call response and a final response. Keeping
  // the sequence finite makes an unexpected extra provider request fail
  // closed instead of silently inventing another capture.
  faux.setResponses(Array.from({ length: 8 }, () => fixtureResponse));

  const modelRuntime = await ModelRuntime.create({
    credentials: new MemoryCredentialStore(),
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  modelRuntime.registerNativeProvider(faux.provider);
  const registeredProviders = modelRuntime.getRegisteredProviderIds();
  if (registeredProviders.length !== 1 || registeredProviders[0] !== faux.provider.id ||
      modelRuntime.getRegisteredNativeProvider(faux.provider.id) !== faux.provider) {
    throw new Error("fixture model runtime provider inventory is not closed");
  }
  if ((await modelRuntime.listCredentials()).length !== 0) {
    throw new Error("fixture model runtime credential inventory is not empty");
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
  });
  const sessionManager = SessionManager.inMemory(options.cwd);

  const resourceLoader = emptyResources();
  const resourceInventory = resourceLoader.getExtensions();
  if (resourceInventory.extensions.length !== 0 || resourceInventory.errors.length !== 0 ||
      resourceLoader.getSkills().skills.length !== 0 || resourceLoader.getPrompts().prompts.length !== 0 ||
      resourceLoader.getThemes().themes.length !== 0 || resourceLoader.getAgentsFiles().agentsFiles.length !== 0 ||
      resourceLoader.getSystemPrompt() !== undefined || resourceLoader.getAppendSystemPrompt().length !== 0) {
    throw new Error("fixture resource inventory is not closed");
  }

  const result = await createAgentSession({
    cwd: options.cwd,
    agentDir: resolve(options.cwd, ".tbound-fixture-agent-do-not-persist"),
    modelRuntime,
    model,
    thinkingLevel: "off",
    settingsManager,
    sessionManager,
    resourceLoader,
    noTools: "builtin",
    tools: [...declaredToolNames],
    customTools: createProxyTools(options.sender),
  });

  const unsubscribe = result.session.subscribe((event) => {
    if (event.type !== "message_update") return;
    const assistantEvent = event.assistantMessageEvent as AssistantMessageEvent;
    if (assistantEvent.type === "text_delta") options.onTextDelta?.(assistantEvent.delta);
  });

  return {
    session: result.session,
    model,
    // Explicitly disable prompt/template/command expansion for every turn.
    prompt: (text: string) => result.session.prompt(text, {
      expandPromptTemplates: false,
      source: "rpc",
    }),
    dispose: () => {
      unsubscribe();
      result.session.dispose();
    },
  };
}

function parseArgs(argv: readonly string[]): { address: string; token: string; cwd: string } {
  let address = "";
  let cwd = process.cwd();
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--mode" && argv[index + 1] === SDK_WORKER_MODE) {
      index += 1;
      continue;
    }
    if (argument === "--ipc-address" && argv[index + 1]) {
      address = argv[++index]!;
      continue;
    }
    if (argument === "--cwd" && argv[index + 1]) {
      cwd = resolve(argv[++index]!);
      continue;
    }
    throw new Error("sdk-worker accepts only --mode fixture, --ipc-address, and --cwd");
  }
  const token = process.env.TBOUND_IPC_TOKEN ?? "";
  if (!address || !/^[0-9a-f]{64}$/.test(token)) throw new Error("fixture SDK worker IPC binding is incomplete");
  return { address, token, cwd };
}

function connect(address: string): Promise<Socket> {
  return new Promise((resolvePromise, reject) => {
    const separator = address.lastIndexOf(":");
    const host = separator > 0 ? address.slice(0, separator) : "";
    const port = separator > 0 ? Number(address.slice(separator + 1)) : NaN;
    if (!host || !Number.isInteger(port) || port <= 0 || port > 65535) {
      reject(new Error("fixture SDK worker IPC address is not host:port"));
      return;
    }
    const socket = createConnection({ host, port });
    socket.once("connect", () => resolvePromise(socket));
    socket.once("error", reject);
  });
}

async function runProcess(argv: readonly string[]): Promise<void> {
  const options = parseArgs(argv);
  const socket = await connect(options.address);
  const client = new IpcClient(socket, options.token);
  let requestID = "";
  const emit = (value: WorkerOutput): void => {
    process.stdout.write(`${JSON.stringify(value)}\n`);
  };

  const fixture = await createFixtureSession({
    cwd: options.cwd,
    sender: new IpcProposalSender(client),
    onTextDelta: (text) => emit({ type: "text_delta", request_id: requestID, text }),
  });
  emit({
    type: "ready",
    model: { provider: fixture.model.provider, id: fixture.model.id },
    tools: [...fixture.session.getActiveToolNames()],
  });

  const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
  try {
    for await (const line of input) {
      if (Buffer.byteLength(line, "utf8") > MAX_LINE_BYTES) throw new Error("worker command exceeds size limit");
      let command: unknown;
      try {
        command = JSON.parse(line);
      } catch {
        throw new Error("worker command is not JSON");
      }
      if (!command || typeof command !== "object" || Array.isArray(command)) throw new Error("worker command is not an object");
      const value = command as Record<string, unknown>;
      if (value.type === "stop") {
        await fixture.session.abort();
        emit({ type: "stopped" });
        return;
      }
      if (value.type !== "prompt" || typeof value.request_id !== "string" || typeof value.text !== "string") {
        throw new Error("worker command has an unsupported shape");
      }
      if (Buffer.byteLength(value.text, "utf8") === 0 || Buffer.byteLength(value.text, "utf8") > 16 * 1024) {
        throw new Error("worker prompt exceeds size bounds");
      }
      requestID = value.request_id;
      let error = "";
      try {
        await fixture.prompt(value.text);
      } catch (caught) {
        error = caught instanceof Error ? caught.message : String(caught);
      } finally {
        emit(error ? { type: "error", request_id: requestID, error } : { type: "turn_end", request_id: requestID });
        requestID = "";
      }
    }
  } finally {
    input.close();
    client.close();
    fixture.dispose();
  }
}

const isMain = process.argv[1]?.endsWith("sdk-worker.ts") || process.argv[1]?.endsWith("sdk-worker.js");
if (isMain) {
  runProcess(process.argv.slice(2)).catch((error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 2;
  });
}
