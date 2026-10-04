import { Type, type Static } from "typebox";
import type { ToolDefinition } from "@earendil-works/pi-coding-agent";
import type { IpcClient, IpcProposal, IpcResult } from "./ipc-transport.ts";

export const declaredToolNames = ["read", "write", "edit", "bash"] as const;
export type DeclaredToolName = (typeof declaredToolNames)[number];

export type ToolProposal = IpcProposal;

export interface ProbeProposalReply {
  verdict: "DENY";
  reason: "probe-only";
}

export type ProposalReply = IpcResult | ProbeProposalReply;

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

const readParameters = Type.Object({
  path: Type.String({ description: "Path to the file to read (relative or absolute)" }),
  offset: Type.Optional(Type.Number({ description: "Line number to start reading from (1-indexed)" })),
  limit: Type.Optional(Type.Number({ description: "Maximum number of lines to read" })),
});

const writeParameters = Type.Object({
  path: Type.String({ description: "Path to the file to write (relative or absolute)" }),
  content: Type.String({ description: "Content to write to the file" }),
});

const editParameters = Type.Object(
  {
    path: Type.String({ description: "Path to the file to edit (relative or absolute)" }),
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
  command: Type.String({ description: "Shell command to execute" }),
  timeout: Type.Optional(Type.Number({ description: "Timeout in seconds (optional, no default timeout)" })),
});

export const proxyParameters = {
  read: readParameters,
  write: writeParameters,
  edit: editParameters,
  bash: bashParameters,
} as const;

function makeProxyTool<TName extends DeclaredToolName, TParams extends typeof proxyParameters[TName]>(
  name: TName,
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
      return { content: [{ type: "text", text: JSON.stringify(reply) }], details: undefined };
    },
  };
}

export function createProxyTools(sender: ProposalSender): ToolDefinition[] {
  return [
    makeProxyTool(
      "read",
      readParameters,
      "Read the contents of a file. Supports text files and images. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "write",
      writeParameters,
      "Write content to a file. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "edit",
      editParameters,
      "Edit a single file using exact text replacements. Requests are forwarded to tbound.",
      sender,
    ),
    makeProxyTool(
      "bash",
      bashParameters,
      "Execute a bash command. Requests are forwarded to tbound.",
      sender,
    ),
  ];
}

export function createIpcProxyTools(client: IpcClient): ToolDefinition[] {
  return createProxyTools(new IpcProposalSender(client));
}
