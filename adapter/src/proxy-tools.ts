import type { Static, TSchema } from "typebox";
import type { ToolDefinition } from "@earendil-works/pi-coding-agent";
import type { IpcClient, IpcProposal, IpcResult } from "./ipc-transport.ts";
import { permittedAlternativeHints } from "./result-hints.ts";

export const declaredToolNames = ["read", "write", "edit", "bash"] as const;
export type DeclaredToolName = (typeof declaredToolNames)[number];

export type ToolProposal = IpcProposal;

export type ProposalReply = IpcResult;

export interface ProposalSender {
  send(proposal: ToolProposal, signal?: AbortSignal): Promise<ProposalReply>;
}

export class IpcProposalSender implements ProposalSender {
  private readonly client: IpcClient;

  constructor(client: IpcClient) {
    this.client = client;
  }

  send(proposal: ToolProposal, signal?: AbortSignal): Promise<IpcResult> {
    return this.client.request(proposal, signal);
  }
}

type TypeboxBuilder = typeof import("typebox").Type;

export function createProxyParameters(Type: TypeboxBuilder) {
const readParameters = Type.Object({
  path: Type.String({ description: "Path to the file to read, relative to the workspace root (an absolute path ends the turn)" }),
});

const writeParameters = Type.Object({
  path: Type.String({ description: "Path to the file to write, relative to the workspace root (an absolute path ends the turn)" }),
  content: Type.String({ description: "Content to write to the file" }),
});

const editParameters = Type.Object(
  {
    path: Type.String({ description: "Path to the file to edit, relative to the workspace root (an absolute path ends the turn)" }),
    edits: Type.Array(
      Type.Object(
        {
          oldText: Type.String({
            description:
              "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call.",
          }),
          newText: Type.String({ description: "Replacement text for this targeted edit." }),
        },
        {},
      ),
      {
        description:
          "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.",
      },
    ),
  },
  {},
);

const bashParameters = Type.Object({
  command: Type.String({ description: "Shell command to run with bash in the workspace root" }),
});

const proxyParameters = {
  read: readParameters,
  write: writeParameters,
  edit: editParameters,
  bash: bashParameters,
} as const;

return proxyParameters;
}

function makeProxyTool<TParams extends TSchema>(
	name: DeclaredToolName,
  schema: TParams,
  description: string,
  sender: ProposalSender,
): ToolDefinition<TParams> {
  return {
    name,
    label: name,
    description,
    promptSnippet: name,
    parameters: schema,
    executionMode: "sequential",
    constrainedSampling: { type: "json_schema", strict: "prefer" },
    async execute(toolCallId, params: Static<TParams>, signal) {
      if (signal?.aborted) throw new Error("Proposal cancelled before dispatch");
      const reply = await sender.send(
        {
          schema_version: "tbound-proposal/v1",
          tool_call_id: toolCallId,
          tool: name,
          arguments: params as Record<string, unknown>,
        },
        signal,
      );
      let text = JSON.stringify(reply);
      if ("reason_code" in reply) {
        const reason = reply.verdict === "DENY" ? reply.reason_code : reply.status;
        if (reason && Object.hasOwn(permittedAlternativeHints, reason)) {
          text += `\nhint: ${permittedAlternativeHints[reason]}`;
        }
      }
      return { content: [{ type: "text", text }], details: undefined };
    },
  };
}

export function createProxyTools(sender: ProposalSender, Type: TypeboxBuilder): ToolDefinition[] {
  const proxyParameters = createProxyParameters(Type);
  return [
    makeProxyTool(
      "read",
      proxyParameters.read,
      "Read the whole contents of an existing UTF-8 text file in the workspace. There is no offset or limit. A missing file, an absolute path, a file of about 1 MiB or more, or a file that is not valid UTF-8 (a binary or image file) cannot be read and ends the turn. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "write",
      proxyParameters.write,
      "Write content to a file. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "edit",
      proxyParameters.edit,
      "Edit a single file using exact text replacements. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "bash",
      proxyParameters.bash,
      "Execute a bash command in the workspace and return its exit code, stdout and stderr. Each stream is cut to its last 16 KiB. There is no timeout parameter: a command that runs longer than 10 minutes, or prints more than 1 MiB on one stream, is stopped and returns no result. Requests are forwarded to tbound.",
      sender,
    ),
  ];
}

export function createIpcProxyTools(client: IpcClient, Type: TypeboxBuilder): ToolDefinition[] {
  return createProxyTools(new IpcProposalSender(client), Type);
}
