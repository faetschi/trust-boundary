import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { dialUnixIpc, type IpcProposal } from "./ipc-transport.ts";

const proposals: IpcProposal[] = [
  {
    schema_version: "tbound-proposal/v1",
    tool_call_id: "smoke-read",
    tool: "read",
    arguments: { path: "README.md" },
  },
  {
    schema_version: "tbound-proposal/v1",
    tool_call_id: "smoke-write",
    tool: "write",
    arguments: { path: "smoke-output.txt", content: "synthetic; no filesystem write" },
  },
  {
    schema_version: "tbound-proposal/v1",
    tool_call_id: "smoke-edit",
    tool: "edit",
    arguments: { path: "README.md", edits: [{ oldText: "before", newText: "after" }] },
  },
  {
    schema_version: "tbound-proposal/v1",
    tool_call_id: "smoke-bash",
    tool: "bash",
    arguments: { command: "printf synthetic smoke", timeout: 1 },
  },
];

function parseArguments(args: string[]): { socket: string; tokenFile: string } {
  let socket: string | undefined;
  let tokenFile: string | undefined;
  for (let index = 0; index < args.length; index += 1) {
    const name = args[index];
    const value = args[index + 1];
    if (!value || value.startsWith("--")) throw new Error(`${name} requires a value`);
    if (name === "--socket" && socket === undefined) socket = value;
    else if (name === "--token-file" && tokenFile === undefined) tokenFile = value;
    else throw new Error(`unknown or duplicate argument ${JSON.stringify(name)}`);
    index += 1;
  }
  if (!socket || !tokenFile) throw new Error("usage: ipc-smoke-client.ts --socket PATH --token-file PATH");
  return { socket, tokenFile };
}

async function main(): Promise<void> {
  const { socket, tokenFile } = parseArguments(process.argv.slice(2));
  const token = await readFile(tokenFile, "utf8");
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error("token file must contain exactly 64 lowercase hex characters");
  const client = await dialUnixIpc(socket, token);
  try {
    for (const proposal of proposals) {
      const result = await client.request(proposal);
      process.stdout.write(`${JSON.stringify(result)}\n`);
    }
  } finally {
    client.close();
  }
}

const invokedPath = process.argv[1];
if (invokedPath && import.meta.url === pathToFileURL(resolve(invokedPath)).href) {
  main().catch((error: unknown) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  });
}
