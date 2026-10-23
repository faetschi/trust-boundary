import assert from "node:assert/strict";
import { test } from "node:test";
import { Type } from "typebox";
import {
  IPC_MAX_MESSAGE_BYTES,
  IPC_WIRE_VERSION,
  IpcClient,
  IpcServer,
  IpcTransportError,
  createInMemoryDuplexPair,
  createInMemoryIpcPair,
  decodeIpcFrame,
  encodeIpcFrame,
  type IpcProposal,
  type IpcResult,
} from "./ipc-transport.ts";
import { createIpcProxyTools, declaredToolNames } from "./proxy-tools.ts";
import { permittedAlternativeHints } from "./result-hints.ts";

const TOKEN = "00".repeat(32);

const proposal: IpcProposal = {
  schema_version: "tbound-proposal/v1",
  tool_call_id: "c",
  tool: "read",
  arguments: { path: "x" },
};

const result: IpcResult = {
  schema_version: "tbound-result/v1",
  tool_call_id: "c",
  tool: "read",
  sequence: 0,
  verdict: "DENY",
  reason_code: "policy_rule_deny",
  policy_digest: "test-policy",
};

const HAND_WRITTEN_JSON = Buffer.from(
  '{"version":"tbound-ipc/v1","binding_token":"0000000000000000000000000000000000000000000000000000000000000000",' +
  '"sequence":1,"kind":"proposal","payload":{"schema_version":"tbound-proposal/v1","tool_call_id":"c",' +
  '"tool":"read","arguments":{"path":"x"}}}',
  "utf8",
);
const HAND_WRITTEN_VECTOR = Buffer.concat([Buffer.from([0x00, 0x00, 0x00, 0xf9]), HAND_WRITTEN_JSON]);

function wireFrame(json: string): Buffer {
  const body = Buffer.from(json, "utf8");
  const wire = Buffer.alloc(4 + body.length);
  wire.writeUInt32BE(body.length, 0);
  body.copy(wire, 4);
  return wire;
}

function rawEnvelope(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    version: IPC_WIRE_VERSION,
    binding_token: TOKEN,
    sequence: 1,
    kind: "result",
    payload: result,
    ...overrides,
  });
}

async function writeRaw(stream: NodeJS.WritableStream, bytes: Uint8Array): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    stream.write(bytes, (error?: Error | null) => error ? reject(error) : resolve());
  });
}

async function clientAgainstRawPeer(): Promise<{ client: IpcClient; peer: ReturnType<typeof createInMemoryDuplexPair>[1] }> {
  const [clientStream, peer] = createInMemoryDuplexPair();
  return { client: new IpcClient(clientStream, TOKEN), peer };
}

async function expectCode(operation: Promise<unknown>, code: string): Promise<void> {
  await assert.rejects(operation, (error: unknown) =>
    error instanceof IpcTransportError && error.code === code,
  );
}

test("Go-compatible frame golden vector encodes and decodes", () => {
  const encoded = encodeIpcFrame({
    version: IPC_WIRE_VERSION,
    binding_token: TOKEN,
    sequence: 1,
    kind: "proposal",
    payload: proposal,
  });
  assert.deepEqual(encoded, HAND_WRITTEN_VECTOR.subarray(4));
  assert.deepEqual(decodeIpcFrame(HAND_WRITTEN_JSON), {
    version: IPC_WIRE_VERSION,
    binding_token: TOKEN,
    sequence: 1,
    kind: "proposal",
    payload: proposal,
  });
  assert.deepEqual(HAND_WRITTEN_VECTOR.subarray(0, 4), Buffer.from([0x00, 0x00, 0x00, 0xf9]));
  assert.equal(HAND_WRITTEN_VECTOR.readUInt32BE(0), HAND_WRITTEN_VECTOR.length - 4);
});

test("rejects oversized, truncated, and malformed frames", async (t) => {
  await t.test("clean EOF is a normal close", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    peer.end();
    assert.equal(await client.receiveResult(), null);
    await assert.rejects(client.receiveResult(), (error: unknown) =>
      error instanceof IpcTransportError && error.code === "ERR_IPC_CLOSED",
    );
  });

  await t.test("oversized announced length", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    const prefix = Buffer.alloc(4);
    prefix.writeUInt32BE(IPC_MAX_MESSAGE_BYTES + 1, 0);
    await writeRaw(peer, prefix);
    await expectCode(reading, "ERR_IPC_FRAME_TOO_LARGE");
  });

  await t.test("truncated prefix", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    peer.end(Buffer.from([0, 0]));
    await expectCode(reading, "ERR_IPC_MALFORMED_FRAME");
  });

  await t.test("truncated payload", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    const prefix = Buffer.alloc(4);
    prefix.writeUInt32BE(20, 0);
    await writeRaw(peer, Buffer.concat([prefix, Buffer.from("{}") ]));
    peer.end();
    await expectCode(reading, "ERR_IPC_MALFORMED_FRAME");
  });

  await t.test("duplicate and unknown fields", async () => {
    const duplicate = `{"version":"${IPC_WIRE_VERSION}","version":"${IPC_WIRE_VERSION}",` +
      `"binding_token":"${TOKEN}","sequence":1,"kind":"result","payload":${JSON.stringify(result)}}`;
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    await writeRaw(peer, wireFrame(duplicate));
    await expectCode(reading, "ERR_IPC_MALFORMED_FRAME");

    const unknownPair = await clientAgainstRawPeer();
    const unknownRead = unknownPair.client.receiveResult();
    await writeRaw(unknownPair.peer, wireFrame(rawEnvelope({ extra: true })));
    await expectCode(unknownRead, "ERR_IPC_MALFORMED_FRAME");

    const fractionalSequencePair = await clientAgainstRawPeer();
    const fractionalSequenceRead = fractionalSequencePair.client.receiveResult();
    const fractionalSequence = rawEnvelope({ payload: { ...result, sequence: 1 } })
      .replace('"sequence":1,"verdict"', '"sequence":1e0,"verdict"');
    await writeRaw(fractionalSequencePair.peer, wireFrame(fractionalSequence));
    await expectCode(fractionalSequenceRead, "ERR_IPC_MALFORMED_FRAME");
  });
});

test("checks token, sequence, replay, and message direction", async (t) => {
  await t.test("binding token mismatch", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    await writeRaw(peer, wireFrame(rawEnvelope({ binding_token: "11".repeat(32) })));
    await expectCode(reading, "ERR_IPC_BINDING_MISMATCH");
  });

  await t.test("sequence must start at one and increase by one", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    await writeRaw(peer, wireFrame(rawEnvelope({ sequence: 2 })));
    await expectCode(reading, "ERR_IPC_SEQUENCE");
  });

  await t.test("replayed sequence is rejected", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const encoded = wireFrame(rawEnvelope());
    const first = client.receiveResult();
    await writeRaw(peer, encoded);
    assert.deepEqual(await first, result);
    const second = client.receiveResult();
    await writeRaw(peer, encoded);
    await expectCode(second, "ERR_IPC_SEQUENCE");
  });

  await t.test("wrong direction is rejected", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    const wrongDirection = JSON.stringify({
      version: IPC_WIRE_VERSION,
      binding_token: TOKEN,
      sequence: 1,
      kind: "proposal",
      payload: proposal,
    });
    await writeRaw(peer, wireFrame(wrongDirection));
    await expectCode(reading, "ERR_IPC_UNEXPECTED_KIND");
  });

  await t.test("unknown kind is rejected", async () => {
    const { client, peer } = await clientAgainstRawPeer();
    const reading = client.receiveResult();
    await writeRaw(peer, wireFrame(rawEnvelope({ kind: "future-kind" })));
    await expectCode(reading, "ERR_IPC_UNKNOWN_KIND");
  });
});

test("proxy tools each round-trip one proposal and return its correlated result", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const tools = createIpcProxyTools(client, Type);
  assert.deepEqual(tools.map((tool) => tool.name), [...declaredToolNames]);

  const args: Record<(typeof declaredToolNames)[number], Record<string, unknown>> = {
    read: { path: "input.txt", offset: 1, limit: 2 },
    write: { path: "output.txt", content: "hello" },
    edit: { path: "input.txt", edits: [{ oldText: "before", newText: "after" }] },
    bash: { command: "echo ok", timeout: 1 },
  };
  const serverWork = (async () => {
    const proposals: IpcProposal[] = [];
    for (const name of declaredToolNames) {
      const received = await server.receiveProposal();
      assert.ok(received);
      proposals.push(received);
      await server.sendResult({
        ...result,
        tool_call_id: received.tool_call_id,
        tool: received.tool,
      });
    }
    return proposals;
  })();

  for (const name of declaredToolNames) {
    const tool = tools.find((candidate) => candidate.name === name);
    assert.ok(tool);
    const callId = `call-${name}`;
    const execution = await tool.execute(callId, args[name] as never, undefined, undefined, {} as never);
    assert.equal(execution.content[0]?.type, "text");
    assert.equal(execution.content[0]!.text, JSON.stringify({
      ...result,
      tool_call_id: callId,
      tool: name,
    }) + "\nhint: The policy does not allow this tool or action. Use another available tool on the task's files.");
  }

  const proposals = await serverWork;
  assert.deepEqual(proposals, declaredToolNames.map((name) => ({
    schema_version: "tbound-proposal/v1",
    tool_call_id: `call-${name}`,
    tool: name,
    arguments: args[name],
  })));
  client.close();
  server.close();
});

test("proxy feedback appends exactly the fixed hint, and leaves successful ALLOW unchanged", async (t) => {
  const allowed: IpcResult = {
    ...result,
    response_id: { issuer: "test", opaque: "response" },
    tool: "bash",
    sequence: 1,
    canonical_arguments_digest: `tbound-args-jcs-rfc8785/v1:sha256:${"a".repeat(64)}`,
    verdict: "ALLOW",
    reason_code: "policy_rule_allow",
    output: { text: "unchanged <output>\nsecond line", exit_code: 0 },
  };
  const cases: Array<{ name: string; reply: IpcResult; hint?: string }> = [
    { name: "successful ALLOW", reply: allowed },
    { name: "failed command", reply: { ...allowed, status: "command_failed" }, hint: permittedAlternativeHints.command_failed },
    { name: "unknown reason", reply: { ...result, reason_code: "future_reason" } },
    { name: "empty status", reply: { ...allowed, status: "" } },
    { name: "object prototype name", reply: { ...result, reason_code: "__proto__" } },
    ...Object.entries(permittedAlternativeHints).filter(([reason]) => reason !== "command_failed").map(([reason, hint]) => ({
      name: reason, reply: { ...result, reason_code: reason }, hint,
    })),
  ];
  for (const { name, reply, hint } of cases) {
    await t.test(name, async () => {
      const { client, server } = createInMemoryIpcPair(TOKEN);
      try {
        const tool = createIpcProxyTools(client, Type).find((tool) => tool.name === "bash");
        assert.ok(tool);
        const pending = tool.execute("c", { command: "true" }, undefined, undefined, {} as never);
        assert.ok(await server.receiveProposal());
        const sent = { ...reply, tool: "bash" };
        await server.sendResult(sent);
        const execution = await pending;
        assert.deepEqual(execution, {
          content: [{ type: "text", text: JSON.stringify(sent) + (hint ? `\nhint: ${hint}` : "") }],
          details: undefined,
        });
      } finally {
        client.close();
        server.close();
      }
    });
  }
});

test("IPC rejects command status outside allowed failed bash results", async (t) => {
  for (const payload of [
    { ...result, status: "command_failed" },
    { ...result, status: "unexpected" },
    { ...result, verdict: "ALLOW", reason_code: "policy_rule_allow", tool: "read", sequence: 1,
      response_id: { issuer: "test", opaque: "response" },
      canonical_arguments_digest: `tbound-args-jcs-rfc8785/v1:sha256:${"a".repeat(64)}`, status: "command_failed" },
  ]) {
    await t.test(`${payload.verdict} ${payload.tool} ${payload.status}`, async () => {
      const { client, peer } = await clientAgainstRawPeer();
      const reading = client.receiveResult();
      await writeRaw(peer, wireFrame(rawEnvelope({ payload })));
      await expectCode(reading, "ERR_IPC_MALFORMED_FRAME");
    });
  }
});
