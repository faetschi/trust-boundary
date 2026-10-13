/**
 * TBound's pinned Pi CLI extension. Pi owns the agent loop in its official
 * `pi --mode rpc` process; this extension only registers the four broker tools
 * and the Go-owned provider bridge. It never creates an AgentSession.
 */
import { readSync, writeSync } from "node:fs";
import { readFileSync, realpathSync, statSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { pathToFileURL } from "node:url";
import { Socket } from "node:net";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { createBrokerProviderFromFD, BROKER_MODEL_ID, BROKER_PROVIDER_ID } from "./broker-provider.ts";
import { IpcClient } from "./ipc-transport.ts";
import { createIpcProxyTools } from "./proxy-tools.ts";

const BOOTSTRAP_FD = 5;
const IPC_FD = 6;
const PROVIDER_FD = 7;
const EVENT_FD = 8;
const MAX_BOOTSTRAP_BYTES = 16 << 10;
const MAX_EVENT_BYTES = 1 << 20;

type Bootstrap = {
  schema_version: "governed-pi-cli/v1";
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
  tools: readonly ["read", "write", "edit", "bash"];
  tool_schema_sha256: Record<string, string>;
};

function readExact(fd: number, length: number): Buffer {
  const output = Buffer.alloc(length);
  let offset = 0;
  while (offset < length) {
    const count = readSync(fd, output, offset, length - offset, null);
    if (count <= 0) throw new Error("TBound bootstrap descriptor closed before its bounded frame completed");
    offset += count;
  }
  return output;
}

function readBootstrap(): Bootstrap {
  const size = readExact(BOOTSTRAP_FD, 4).readUInt32BE(0);
  if (size === 0 || size > MAX_BOOTSTRAP_BYTES) throw new Error("TBound bootstrap length is invalid");
  const value: unknown = JSON.parse(readExact(BOOTSTRAP_FD, size).toString("utf8"));
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("TBound bootstrap is malformed");
  const bootstrap = value as Bootstrap;
  if (bootstrap.schema_version !== "governed-pi-cli/v1" || bootstrap.pi_version !== "0.87.1" ||
      bootstrap.provider_id !== BROKER_PROVIDER_ID || bootstrap.model_id !== BROKER_MODEL_ID ||
      bootstrap.tools.join("\0") !== "read\0write\0edit\0bash" ||
      !/^[0-9a-f]{64}$/.test(bootstrap.ipc_binding_token) ||
      bootstrap.descriptor_profile !== "tbound-pi-cli-fdmap/v1;exec=3;exposure=4;bootstrap=5;ipc=6;provider=7;events=8;bundle=9;control=0") {
    throw new Error("TBound CLI bootstrap differs from its fixed admitted profile");
  }
  return bootstrap;
}

function announceReady(bootstrap: Bootstrap): void {
  const event = {
    type: "ready",
    sequence: 1,
    schema_version: bootstrap.schema_version,
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
    node_version: bootstrap.node_version,
    pi_version: bootstrap.pi_version,
    provider_id: bootstrap.provider_id,
    model_id: bootstrap.model_id,
    tools: bootstrap.tools,
    tool_schema_sha256: bootstrap.tool_schema_sha256,
  };
  const body = Buffer.from(JSON.stringify(event), "utf8");
  if (body.length === 0 || body.length > MAX_EVENT_BYTES) throw new Error("TBound ready event exceeds its frame bound");
  const frame = Buffer.allocUnsafe(4 + body.length);
  frame.writeUInt32BE(body.length, 0);
  body.copy(frame, 4);
  let offset = 0;
  while (offset < frame.length) offset += writeSync(EVENT_FD, frame, offset, frame.length - offset);
}

function importPinnedPackage(cliEntry: string, name: string, version: string): Promise<Record<string, unknown>> {
  const realEntry = realpathSync(cliEntry);
  if (!statSync(realEntry).isFile()) throw new Error("resolved Pi CLI entry is not a regular file");
  let cursor = dirname(realEntry);
  while (true) {
    const packageRoot = join(cursor, "node_modules", name);
    try {
      const manifestPath = join(packageRoot, "package.json");
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as {
        name?: string; version?: string; exports?: unknown;
      };
      if (manifest.name !== name || manifest.version !== version) throw new Error(`unexpected ${name} package identity/version`);
      const exportsValue = manifest.exports as Record<string, unknown> | undefined;
      const dot = exportsValue && Object.hasOwn(exportsValue, ".") ? exportsValue["."] : exportsValue;
      const pickImport = (value: unknown): string | undefined => {
        if (typeof value === "string") return value;
        if (!value || typeof value !== "object") return undefined;
        const conditions = value as Record<string, unknown>;
        if (Object.hasOwn(conditions, "import")) return pickImport(conditions.import);
        for (const key of ["node", "default"]) if (Object.hasOwn(conditions, key)) {
          const found = pickImport(conditions[key]);
          if (found) return found;
        }
        return undefined;
      };
      const exportPath = pickImport(dot);
      if (!exportPath || !exportPath.startsWith("./")) throw new Error(`${name} has no explicit ESM package export`);
      const packageReal = realpathSync(packageRoot);
      const modulePath = realpathSync(join(packageReal, exportPath));
      const escaped = relative(packageReal, modulePath);
      if (escaped === ".." || escaped.startsWith(`..${sep}`) || !statSync(modulePath).isFile()) {
        throw new Error(`${name} ESM export escapes its package directory`);
      }
      return import(pathToFileURL(modulePath).href) as Promise<Record<string, unknown>>;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
    const parent = dirname(cursor);
    if (parent === cursor) break;
    cursor = parent;
  }
  throw new Error(`cannot resolve pinned ${name}@${version} from the official Pi CLI installation`);
}

export default async function registerTBoundExtension(pi: ExtensionAPI): Promise<void> {
  const bootstrap = readBootstrap();
  const ipcSocket = new Socket({ fd: IPC_FD, readable: true, writable: true, allowHalfOpen: true });
  const ipc = new IpcClient(ipcSocket, bootstrap.ipc_binding_token);
  const cliEntry = process.env.TBOUND_PI_CLI_ENTRY || process.argv[1];
  if (!cliEntry) throw new Error("cannot resolve the running pinned Pi CLI module for its public extension dependencies");
  const [piAI, typebox] = await Promise.all([
    importPinnedPackage(cliEntry, "@earendil-works/pi-ai", "0.87.1"),
    importPinnedPackage(cliEntry, "typebox", "1.3.27"),
  ]);
  const eventFactory = piAI as Pick<typeof import("@earendil-works/pi-ai"), "createAssistantMessageEventStream">;
  const Type = (typebox as typeof import("typebox")).Type;
  const broker = createBrokerProviderFromFD(eventFactory, PROVIDER_FD);
  pi.registerProvider(broker.provider);
  for (const tool of createIpcProxyTools(ipc, Type)) pi.registerTool(tool);
  pi.on("session_shutdown", () => {
    ipcSocket.destroy();
    broker.client.close();
  });
  pi.on("session_start", () => announceReady(bootstrap));
}
