import assert from "node:assert/strict";
import { spawn, execFileSync, type ChildProcessWithoutNullStreams } from "node:child_process";
import { copyFile, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { existsSync, realpathSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { IpcServer } from "./ipc-transport.ts";

const piVersion = "0.87.1";
const nodeVersion = "v24.21.0";
const brokerVersion = "tbound-provider-bridge/v1";
const brokerModel = "deepseek-v4.1-flash";
const brokerProvider = "tbound-go-broker";
const token = "ab".repeat(32);
const descriptorProfile = "tbound-pi-cli-fdmap/v1;exec=3;exposure=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0";
const sourceRoot = dirname(fileURLToPath(import.meta.url));

function findPi(): string | undefined {
  // This integration path exercises Linux inherited descriptors, so a Windows
  // npm .cmd shim is neither an eligible CLI nor evidence of the test runtime.
  if (process.platform !== "linux") return undefined;
  const explicit = process.env.TBOUND_PI_CLI_BIN;
  if (explicit) return explicit;
  const pathEntries = (process.env.PATH ?? "").split(":");
  for (const directory of pathEntries) {
    if (directory.split(/[\\/]/).includes("node_modules")) continue;
    const candidate = join(directory, "pi");
    if (existsSync(candidate)) return candidate;
  }
  return undefined;
}

function encodeFrame(value: unknown): Buffer {
  const body = Buffer.from(JSON.stringify(value), "utf8");
  const frame = Buffer.allocUnsafe(4 + body.length);
  frame.writeUInt32BE(body.length, 0);
  body.copy(frame, 4);
  return frame;
}

class ByteReader {
  private readonly iterator: AsyncIterator<Buffer | string>;
  private buffered = Buffer.alloc(0);

  constructor(stream: NodeJS.ReadableStream & AsyncIterable<Buffer | string>) {
    this.iterator = stream[Symbol.asyncIterator]();
  }

  async exactly(size: number): Promise<Buffer> {
    while (this.buffered.length < size) {
      const next = await this.iterator.next();
      if (next.done) throw new Error("Pi fixture channel closed before a complete frame");
      const chunk = typeof next.value === "string" ? Buffer.from(next.value, "utf8") : next.value;
      this.buffered = this.buffered.length === 0 ? Buffer.from(chunk) : Buffer.concat([this.buffered, chunk]);
    }
    const result = this.buffered.subarray(0, size);
    this.buffered = this.buffered.subarray(size);
    return result;
  }

  async frame(): Promise<Record<string, unknown>> {
    const prefix = await this.exactly(4);
    const length = prefix.readUInt32BE(0);
    assert.ok(length > 0 && length <= 1 << 20, `invalid fixture frame length ${length}`);
    const value: unknown = JSON.parse((await this.exactly(length)).toString("utf8"));
    assert.ok(value && typeof value === "object" && !Array.isArray(value));
    return value as Record<string, unknown>;
  }
}

class RpcReader {
  private readonly stream: NodeJS.ReadableStream & AsyncIterable<Buffer | string>;
  private readonly pending: Record<string, unknown>[] = [];
  private readonly waiters: Array<(record: Record<string, unknown>) => void> = [];
  private failure: Error | undefined;
  readonly seen: Record<string, unknown>[] = [];

  constructor(stream: NodeJS.ReadableStream & AsyncIterable<Buffer | string>) {
    this.stream = stream;
    void this.readLoop();
  }

  async matching(predicate: (record: Record<string, unknown>) => boolean): Promise<Record<string, unknown>> {
    const found = this.pending.findIndex(predicate);
    if (found >= 0) return this.pending.splice(found, 1)[0]!;
    if (this.failure) throw this.failure;
    return new Promise((resolvePromise, reject) => {
      const waiter = (record: Record<string, unknown>): void => {
        if (predicate(record)) resolvePromise(record);
        else {
          this.pending.push(record);
          this.waiters.push(waiter);
        }
      };
      this.waiters.push(waiter);
      if (this.failure) reject(this.failure);
    });
  }

  private async readLoop(): Promise<void> {
    let buffer = Buffer.alloc(0);
    try {
      for await (const item of this.stream) {
        const chunk = typeof item === "string" ? Buffer.from(item, "utf8") : item;
        buffer = buffer.length === 0 ? Buffer.from(chunk) : Buffer.concat([buffer, chunk]);
        while (true) {
          const newline = buffer.indexOf(0x0a);
          if (newline < 0) break;
          const line = buffer.subarray(0, newline);
          buffer = buffer.subarray(newline + 1);
          if (line.length === 0) continue;
          const record: unknown = JSON.parse(line.toString("utf8"));
          assert.ok(record && typeof record === "object" && !Array.isArray(record));
          this.seen.push(record as Record<string, unknown>);
          const waiter = this.waiters.shift();
          if (waiter) waiter(record as Record<string, unknown>);
          else this.pending.push(record as Record<string, unknown>);
        }
      }
      if (buffer.length !== 0) throw new Error("Pi RPC stdout ended inside a JSONL record");
      throw new Error("Pi RPC stdout closed before the expected response/event");
    } catch (caught) {
      this.failure = caught instanceof Error ? caught : new Error(String(caught));
      for (const waiter of this.waiters.splice(0)) {
        // A rejected waiter is surfaced through the record path without leaving an unhandled promise.
        waiter({ __reader_error: this.failure.message });
      }
    }
  }
}

async function within<T>(promise: Promise<T>, label: string, milliseconds = 15_000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} timed out after ${milliseconds} ms`)), milliseconds);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

async function waitRpc(reader: RpcReader, predicate: (record: Record<string, unknown>) => boolean, label: string) {
  const record = await within(reader.matching((candidate) => candidate.__reader_error !== undefined || predicate(candidate)), label);
  if (record.__reader_error !== undefined) throw new Error(String(record.__reader_error));
  return record;
}

function bootstrap(privateCwd: string) {
  const digest = "1".repeat(64);
  return {
    schema_version: "governed-pi-cli/v1",
    profile_id: "fixture-cli-profile",
    profile_digest: digest,
    source_binding_digest: digest,
    source_provenance_digest: digest,
    source_generation_id: "fixture-generation-g0",
    source_tree_digest: digest,
    source_manifest_digest: digest,
    worker_view_identity: "fixture-view",
    worker_mount_id: 1,
    runtime_bundle_digest: digest,
    descriptor_profile: descriptorProfile,
    descriptor_profile_digest: digest,
    child_environment_digest: digest,
    session_id: "fixture-session",
    workflow_id: "fixture-workflow",
    conversation_id: "fixture-conversation",
    private_cwd: privateCwd,
    ipc_binding_token: token,
    provider_id: brokerProvider,
    model_id: brokerModel,
    node_version: nodeVersion,
    pi_version: piVersion,
    tools: ["read", "write", "edit", "bash"],
    tool_schema_sha256: { read: digest, write: digest, edit: digest, bash: digest },
  };
}

function brokerTurn(sequence: number, toolCall?: { id: string; name: string; arguments: Record<string, unknown> }) {
  return {
    schema_version: brokerVersion,
    response_id: `fixture-response-${sequence}`,
    model: brokerModel,
    created: 1_790_000_000,
    generation: "fixture-generation-g0",
    sequence,
    assistant_text: toolCall ? "Checking the governed file." : "The fixture read was denied.",
    finish_reason: toolCall ? "tool_calls" : "stop",
    usage: { prompt_tokens: 8, completion_tokens: 4, total_tokens: 12, cost: 0 },
    ...(toolCall ? { tool_call: toolCall } : {}),
    journal_sequence: sequence + 1,
    journal_hash: String(sequence).padStart(64, "0"),
  };
}

const piPath = findPi();
const mandatory = process.env.TBOUND_REQUIRE_PI_CLI_INTEGRATION === "1";
const required = mandatory || Boolean(process.env.TBOUND_PI_CLI_BIN) || Boolean(piPath);

test("official Pi CLI 0.87.1 loads the mediated extension and settles simulated host-fixture denials", {
  skip: required ? false : "set TBOUND_REQUIRE_PI_CLI_INTEGRATION=1 in CI or provide the pinned pi executable on PATH",
}, async (t) => {
  assert.ok(piPath, "TBound Pi CLI integration was required but no pi executable was found");
  if (mandatory) assert.ok(process.env.TBOUND_PI_CLI_BIN, "CI must set TBOUND_PI_CLI_BIN to the separately installed official CLI");
  const actualPiVersion = execFileSync(piPath, ["--version"], { encoding: "utf8", env: { PATH: dirname(process.execPath) } }).trim();
  assert.equal(actualPiVersion, piVersion, `installed Pi CLI must be exactly ${piVersion}`);
  assert.equal(process.version, nodeVersion, `integration requires the qualified Node runtime ${nodeVersion}`);
  const realPiPath = realpathSync(piPath);
  const root = await mkdtemp(join(tmpdir(), "tbound-pi-cli-integration-"));
  const home = join(root, "home");
  const temp = join(root, "tmp");
  const cwd = join(root, "workspace");
  const config = join(root, "pi-config");
  const extensionRoot = join(root, "isolated-extension");
  const extensionSrc = join(extensionRoot, "src");
  await Promise.all([home, temp, cwd, config, extensionSrc].map((path) => mkdir(path, { recursive: true, mode: 0o700 })));
  t.after(async () => rm(root, { recursive: true, force: true }));

  for (const name of ["tbound-cli-extension.ts", "broker-provider.ts", "proxy-tools.ts", "ipc-transport.ts", "result-hints.ts"]) {
    await copyFile(join(sourceRoot, name), join(extensionSrc, name));
  }
  await writeFile(join(extensionRoot, "package.json"), '{"private":true,"type":"module"}\n', { mode: 0o600 });
  assert.equal(realpathSync(extensionRoot).includes(`${resolve(process.cwd())}/adapter`), false,
    "the staged extension must not be loaded from the repository's adapter tree");
  let ancestor = realpathSync(extensionRoot);
  while (true) {
    assert.notEqual(ancestor.split(/[\\/]/).at(-1), "node_modules", `unexpected node_modules ancestor: ${ancestor}`);
    const parent = dirname(ancestor);
    if (parent === ancestor) break;
    ancestor = parent;
  }

  const args = [
    "--mode", "rpc", "--no-session", "--no-extensions", "--no-builtin-tools",
    "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
    "--no-approve", "--offline", "--extension", join(extensionSrc, "tbound-cli-extension.ts"),
    "--provider", brokerProvider, "--model", `${brokerProvider}/${brokerModel}`,
  ];
  const env = {
    PATH: dirname(process.execPath),
    HOME: home,
    TMPDIR: temp,
    LANG: "C.UTF-8",
    PI_OFFLINE: "1",
    PI_CODING_AGENT_DIR: config,
    PI_CODING_AGENT_SESSION_DIR: join(config, "sessions"),
    PI_TELEMETRY: "0",
    TBOUND_PI_CLI_ENTRY: realPiPath,
  };
  const child = spawn(piPath, args, {
    cwd,
    env,
    stdio: ["pipe", "pipe", "pipe", "ignore", "ignore", "pipe", "pipe", "pipe", "pipe", "ignore"],
  }) as unknown as ChildProcessWithoutNullStreams;
  const inherited = child.stdio as unknown as Array<NodeJS.ReadWriteStream | null>;
  const inheritedStream = (fd: number): NodeJS.ReadWriteStream => {
    const stream = inherited[fd];
    assert.ok(stream, `child descriptor ${fd} was not created`);
    return stream;
  };
  t.after(() => {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    for (let fd = 5; fd <= 8; fd += 1) (inherited[fd] as (NodeJS.ReadWriteStream & { destroy(): void }) | null)?.destroy();
    child.stdin.destroy();
    child.stdout.destroy();
    child.stderr.destroy();
  });

  let stderr = "";
  child.stderr.setEncoding("utf8").on("data", (chunk: string) => { stderr += chunk; });
  const rpc = new RpcReader(child.stdout);
  const events = new ByteReader(inheritedStream(8) as NodeJS.ReadableStream & AsyncIterable<Buffer | string>);
  const provider = new ByteReader(inheritedStream(7) as NodeJS.ReadableStream & AsyncIterable<Buffer | string>);
  const ipc = new IpcServer(inheritedStream(6) as never, token);
  inheritedStream(5).write(encodeFrame(bootstrap(cwd)));
  inheritedStream(5).end();

  let ready: Record<string, unknown>;
  try {
    ready = await within(events.frame(), "extension readiness event");
  } catch (error) {
    throw new Error(`${error instanceof Error ? error.message : String(error)}; Pi stderr: ${stderr}`);
  }
  assert.equal(ready.type, "ready");
  assert.equal(ready.schema_version, "governed-pi-cli/v1");
  assert.equal(ready.pi_version, piVersion);
  assert.equal(ready.provider_id, brokerProvider);
  assert.equal(ready.model_id, brokerModel);
  assert.deepEqual(ready.tools, ["read", "write", "edit", "bash"]);

  child.stdin.write('{"id":"state-1","type":"get_state"}\n');
  const stateResponse = await waitRpc(rpc, (record) => record.id === "state-1", "Pi get_state response");
  assert.equal(stateResponse.type, "response");
  assert.equal(stateResponse.command, "get_state");
  assert.equal(stateResponse.success, true, `Pi RPC startup failed: ${stderr}`);
  const model = (stateResponse.data as Record<string, unknown> | undefined)?.model as Record<string, unknown> | undefined;
  assert.equal(model?.provider, brokerProvider, `Pi selected an unexpected provider: ${JSON.stringify(model)}`);
  assert.equal(model?.id, brokerModel);

  child.stdin.write('{"id":"prompt-1","type":"prompt","message":"Read the fixture file."}\n');
  const promptResponse = await waitRpc(rpc, (record) => record.id === "prompt-1", "Pi prompt acceptance");
  assert.equal(promptResponse.success, true, `Pi rejected the fixture prompt: ${JSON.stringify(promptResponse)}`);

  const fixtureToolCalls = [
    { name: "read", arguments: { path: "fixture.txt" } },
    { name: "write", arguments: { path: "fixture.txt", content: "must be denied" } },
    { name: "edit", arguments: { path: "fixture.txt", edits: [{ oldText: "old", newText: "new" }] } },
    { name: "bash", arguments: { command: "true" } },
  ] as const;
  for (const [index, tool] of fixtureToolCalls.entries()) {
    const sequence = index + 1;
    const request = await within(provider.frame(), `simulated broker request ${sequence}`);
    assert.deepEqual(Object.keys(request).sort(), ["kind", "request_id", "sequence", "version"]);
    assert.equal(request.version, brokerVersion);
    assert.equal(request.kind, "next");
    assert.equal(request.sequence, sequence);
    const callID = `fixture-tool-call-${tool.name}`;
    inheritedStream(7).write(encodeFrame({
      version: brokerVersion,
      sequence,
      kind: "turn",
      request_id: request.request_id,
      turn: brokerTurn(sequence, { id: callID, name: tool.name, arguments: tool.arguments }),
    }));

    const proposal = await within(ipc.receiveProposal(), `mediated ${tool.name} proposal`);
    assert.ok(proposal);
    assert.deepEqual(proposal, {
      schema_version: "tbound-proposal/v1",
      tool_call_id: callID,
      tool: tool.name,
      arguments: tool.arguments,
    });
    const denial = {
      schema_version: "tbound-result/v1",
      tool_call_id: proposal.tool_call_id,
      tool: proposal.tool,
      sequence,
      verdict: "DENY",
      reason_code: "policy_rule_deny",
      policy_digest: "simulated-host-fixture/v1",
    } satisfies Parameters<IpcServer["sendResult"]>[0];
    await ipc.sendResult(denial);
    const completed = await waitRpc(rpc, (record) =>
      record.type === "tool_execution_end" && record.toolCallId === callID, `hinted ${tool.name} result`);
    assert.deepEqual(completed.result, {
      content: [{ type: "text", text: JSON.stringify(denial) +
        "\nhint: The policy does not allow this action. Use read, write or edit on task files, or bash inside the workspace." }],
    });
  }

  const finalSequence = fixtureToolCalls.length + 1;
  const finalRequest = await within(provider.frame(), "final simulated broker request");
  assert.equal(finalRequest.kind, "next");
  assert.equal(finalRequest.sequence, finalSequence);
  inheritedStream(7).write(encodeFrame({
    version: brokerVersion,
    sequence: finalSequence,
    kind: "turn",
    request_id: finalRequest.request_id,
    turn: brokerTurn(finalSequence),
  }));

  let settled: Record<string, unknown>;
  try {
    settled = await waitRpc(rpc, (record) => record.type === "agent_settled", "Pi agent_settled event");
  } catch (error) {
    const summary = rpc.seen.map(({ type, id, command, success, toolsAdded }) => ({
      type, id, command, success,
      toolNames: Array.isArray(toolsAdded) ? toolsAdded.map((tool) => (tool as Record<string, unknown>).name) : undefined,
    }));
    throw new Error(`${error instanceof Error ? error.message : String(error)}; RPC records: ${JSON.stringify(summary)}; Pi stderr: ${stderr}`);
  }
  assert.equal(settled.type, "agent_settled");
  const systemStart = rpc.seen.find((record) => {
    const message = record.message as Record<string, unknown> | undefined;
    return record.type === "message_start" && message?.role === "system";
  });
  assert.ok(systemStart, "Pi RPC events omitted the system prompt and tool surface");
  const message = systemStart.message as Record<string, unknown>;
  const sections = message.sections as Record<string, unknown> | undefined;
  assert.equal(typeof sections?.tools, "string", "Pi system prompt omitted its tool section");
  const exposedToolNames = [...(sections!.tools as string).matchAll(/^-\s+([a-z][a-z0-9_-]*):/gm)].map((match) => match[1]);
  assert.deepEqual(exposedToolNames, ["read", "write", "edit", "bash"]);
  child.stdin.end();
  const exit = await within(new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolvePromise) => {
    child.once("close", (code, signal) => resolvePromise({ code, signal }));
  }), "Pi RPC orderly shutdown");
  assert.equal(exit.code, 0, `Pi CLI exited with ${exit.code}/${exit.signal}: ${stderr}`);
  ipc.close();
  assert.ok(realPiPath.length > 0);
});
