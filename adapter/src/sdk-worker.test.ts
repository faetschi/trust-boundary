import assert from "node:assert/strict";
import { test } from "node:test";
import { createFixtureSession } from "./sdk-worker.ts";
import type { IpcProposal, IpcResult } from "./ipc-transport.ts";
import type { ProposalSender } from "./proxy-tools.ts";

class RecordingSender implements ProposalSender {
  readonly proposals: IpcProposal[] = [];

  async send(proposal: IpcProposal): Promise<IpcResult> {
    this.proposals.push(structuredClone(proposal));
    return {
      schema_version: "tbound-result/v1",
      tool_call_id: proposal.tool_call_id,
      tool: proposal.tool,
      sequence: 1,
      canonical_arguments_digest: "",
      verdict: "DENY",
      reason_code: "fixture_test_only",
      policy_digest: "tbound-policy/v1:fixture",
    };
  }
}

test("creates a real Pi SDK fixture session with the closed four-tool surface", async () => {
  const sender = new RecordingSender();
  const deltas: string[] = [];
  const fixture = await createFixtureSession({
    cwd: process.cwd(),
    sender,
    onTextDelta: (text) => deltas.push(text),
  });
  try {
    assert.equal(fixture.session.getActiveToolNames().sort().join(","), "bash,edit,read,write");
    assert.deepEqual(fixture.session.getAllTools().map((tool) => tool.name).sort(), ["bash", "edit", "read", "write"]);
    for (const prompt of ["read fixture", "write fixture", "edit fixture", "bash fixture"]) {
      await fixture.prompt(prompt);
    }
    assert.match(deltas.join(""), /fixture final/);
    assert.match(deltas.join(""), /<script>hostile content is text<\/script>/);
    assert.deepEqual(sender.proposals.map((proposal) => proposal.tool), ["read", "write", "edit", "bash"]);
    assert.deepEqual(sender.proposals.map((proposal) => proposal.tool_call_id), [
      "fixture-call-read-1", "fixture-call-write-1", "fixture-call-edit-1", "fixture-call-bash-1",
    ]);
  } finally {
    fixture.dispose();
  }
});

test("proxy tools dispatch all four calls through the ProposalSender seam", async () => {
  const sender = new RecordingSender();
  const fixture = await createFixtureSession({ cwd: process.cwd(), sender });
  try {
    const tools = new Map(fixture.session.agent.state.tools.map((tool) => [tool.name, tool]));
    const argumentsByTool: Record<string, Record<string, unknown>> = {
      read: { path: "fixture.txt" },
      write: { path: "fixture.txt", content: "no effect" },
      edit: { path: "fixture.txt", edits: [{ oldText: "a", newText: "b" }] },
      bash: { command: "echo fixture", timeout: 1 },
    };
    for (const [name, args] of Object.entries(argumentsByTool)) {
      const tool = tools.get(name);
      assert.ok(tool, `missing ${name}`);
      await tool.execute(`fixture-${name}`, args as never, undefined, undefined);
    }
    assert.deepEqual(sender.proposals.map((proposal) => proposal.tool), ["read", "write", "edit", "bash"]);
  } finally {
    fixture.dispose();
  }
});
