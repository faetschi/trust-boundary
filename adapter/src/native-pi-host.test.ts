import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { pathToFileURL } from "node:url";
import type { Terminal } from "@earendil-works/pi-tui";
import {
  createInMemoryIpcPair,
  type IpcProposal,
  type IpcResult,
} from "./ipc-transport.ts";
import {
  createNativePiFixtureHost,
  createNativePiProductionHost,
  NativePiHostRefusal,
  NATIVE_PI_FIXTURE_LABEL,
  NATIVE_PI_PRODUCTION_REFUSAL,
  validateNativePiDependencyProfile,
} from "./native-pi-host.ts";

const TOKEN = "ab".repeat(32);

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(record[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

function argumentsDigest(arguments_: Record<string, unknown>): string {
  return `tbound-args-jcs-rfc8785/v1:sha256:${createHash("sha256").update(canonicalJson(arguments_)).digest("hex")}`;
}

class PrivateTerminal implements Terminal {
  readonly writes: string[] = [];
  private inputHandler?: (data: string) => void;
  private resizeHandler?: () => void;
  private stopped = false;

  start(onInput: (data: string) => void, onResize: () => void): void {
    this.inputHandler = onInput;
    this.resizeHandler = onResize;
    this.stopped = false;
  }

  stop(): void {
    this.inputHandler = undefined;
    this.resizeHandler = undefined;
    this.stopped = true;
  }

  async drainInput(): Promise<void> {}

  sendInput(data: string): void {
    this.inputHandler?.(data);
  }

  write(data: string): void {
    this.writes.push(data);
  }

  get columns(): number {
    return 100;
  }

  get rows(): number {
    return 30;
  }

  get kittyProtocolActive(): boolean {
    return false;
  }

  moveBy(_lines: number): void {}
  hideCursor(): void {}
  showCursor(): void {}
  clearLine(): void {}
  clearFromCursor(): void {}
  clearScreen(): void {}
  setTitle(_title: string): void {}
  setProgress(_active: boolean): void {}

  get isStopped(): boolean {
    return this.stopped;
  }
}

function denyResult(proposal: IpcProposal): IpcResult {
  return {
    schema_version: "tbound-result/v1",
    tool_call_id: proposal.tool_call_id,
    tool: proposal.tool,
    sequence: 1,
    response_id: { issuer: "native-fixture-supervisor", opaque: `response-${proposal.tool_call_id}` },
    canonical_arguments_digest: argumentsDigest(proposal.arguments),
    verdict: "DENY",
    reason_code: "fixture_native_pi_test",
    policy_digest: "tbound-policy/v1:native-pi-fixture-test",
  };
}

test("native InteractiveMode fixture uses provider-generated tool calls and continuations", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new PrivateTerminal();
  const host = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
  });
  const seen: IpcProposal[] = [];
  const results: IpcResult[] = [];
  const supervisor = (async () => {
    for (const expectedTool of ["read", "write", "edit", "bash"] as const) {
      const proposal = await server.receiveProposal();
      assert.ok(proposal);
      seen.push(proposal);
      assert.equal(proposal.tool, expectedTool);
      const result = denyResult(proposal);
      results.push(result);
      await server.sendResult(result);
    }
  })();

  try {
    await host.init();
    assert.equal(host.mode, "fixture");
    assert.equal(host.observations[0]?.type, "host_ready");
    assert.equal((host.observations[0] as { label: string }).label, NATIVE_PI_FIXTURE_LABEL);
    assert.deepEqual((host.observations[0] as { providers: readonly string[] }).providers, ["tbound-native-fixture"]);
    assert.deepEqual((host.observations[0] as { models: readonly string[] }).models, ["tbound-native-fixture/tbound-native-fixture-model"]);
    assert.equal((host.observations[0] as { credentials: number }).credentials, 0);
    assert.ok(terminal.writes.length > 0, "native InteractiveMode did not render to the private terminal");

    const initialSessionId = host.sessionView.sessionId;
    for (const text of [
      "!echo should-not-run",
      "!!echo should-not-run",
      "/login",
      "/logout",
      "/model",
      "/reload",
      "/import fixture.jsonl",
      "/new",
      "/resume",
      "/fork",
      "/share",
      "/settings",
      "/package",
    ]) {
      terminal.sendInput(`${text}\r`);
    }
    for (const shortcut of ["\x1b", "\x04", "\x1a", "\x0c", "\x10", "\x07", "\x16", "\x1b\r"]) {
      terminal.sendInput(shortcut);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
    assert.equal(host.sessionView.sessionId, initialSessionId);
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);

    for (const prompt of ["read fixture", "write fixture", "edit fixture", "bash fixture"]) {
      await host.prompt(prompt);
    }
    await supervisor;
    await new Promise((resolve) => setTimeout(resolve, 75));

    assert.deepEqual(seen.map((proposal) => proposal.tool), ["read", "write", "edit", "bash"]);
    assert.deepEqual(results.map((result) => result.tool_call_id), seen.map((proposal) => proposal.tool_call_id));
    assert.deepEqual(results.map((result) => result.response_id?.opaque), seen.map((proposal) => `response-${proposal.tool_call_id}`));
    assert.ok(results.every((result, index) => result.canonical_arguments_digest === argumentsDigest(seen[index]!.arguments)));
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 4);
    assert.equal(host.observations.filter((item) => item.type === "tool_result").length, 4);
    assert.ok(
      terminal.writes.join("").includes("native Pi fixture continuation"),
      "continuation was not rendered by native InteractiveMode",
    );
    await assert.rejects(host.run(), (error: unknown) =>
      error instanceof NativePiHostRefusal && error.message === NATIVE_PI_PRODUCTION_REFUSAL,
    );
  } finally {
    await host.dispose();
    server.close();
  }
  assert.equal(terminal.isStopped, true);
});

test("native host disposal wins an init race and always closes terminal input", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new PrivateTerminal();
  const host = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
  });
  const initialization = host.init();
  const disposal = host.dispose();
  await disposal;
  await assert.rejects(initialization, (error: unknown) =>
    error instanceof NativePiHostRefusal || error instanceof Error,
  );
  assert.equal(terminal.isStopped, true);
  assert.throws(() => host.sessionView, (error: unknown) => error instanceof NativePiHostRefusal);
  server.close();
});

test("native host cleans up after terminal initialization failure", async () => {
  class FailingTerminal extends PrivateTerminal {
    override start(onInput: (data: string) => void, onResize: () => void): void {
      super.start(onInput, onResize);
      throw new Error("synthetic terminal initialization failure");
    }
  }
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new FailingTerminal();
  const host = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
  });
  await assert.rejects(host.init(), /synthetic terminal initialization failure/);
  await host.dispose();
  assert.equal(terminal.isStopped, true);
  server.close();
});

test("native fixture refuses ambient provider authority in a minimal child environment", () => {
  const moduleUrl = pathToFileURL(`${process.cwd()}/src/native-pi-host.ts`).href;
  for (const [name, value] of [
    ["OPENAI_API_KEY", "synthetic-openai-sentinel"],
    ["COPILOT_GITHUB_TOKEN", "synthetic-copilot-sentinel"],
  ] as const) {
    const child = spawnSync(
      process.execPath,
      [
        "--experimental-strip-types",
        "--input-type=module",
        "-e",
        `import { assertNoAmbientProviderAuthority } from ${JSON.stringify(moduleUrl)}; assertNoAmbientProviderAuthority();`,
      ],
      {
        env: {
          SystemRoot: "C:\\Windows",
          [name]: value,
        },
        encoding: "utf8",
      },
    );
    assert.notEqual(child.status, 0);
    assert.match(`${child.stderr}${child.stdout}`, new RegExp(name));
    assert.doesNotMatch(`${child.stderr}${child.stdout}`, new RegExp(value));
  }
});

test("native fixture profile rejects dependency and schema drift", () => {
  const manifests = [
    { name: "@earendil-works/pi-coding-agent", version: "0.87.1" },
    { name: "@earendil-works/pi-ai", version: "0.87.1" },
    { name: "@earendil-works/pi-tui", version: "0.87.1" },
  ];
  const schemas = {
    read: "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
    write: "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
    edit: "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
    bash: "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
  };
  validateNativePiDependencyProfile(manifests, schemas);
  assert.throws(
    () => validateNativePiDependencyProfile(
      manifests.map((manifest) => manifest.name === "@earendil-works/pi-ai" ? { ...manifest, version: "0.87.0" } : manifest),
      schemas,
    ),
    (error: unknown) => error instanceof NativePiHostRefusal,
  );
  assert.throws(
    () => validateNativePiDependencyProfile(manifests, { ...schemas, bash: "0".repeat(64) }),
    (error: unknown) => error instanceof NativePiHostRefusal,
  );
});

test("native host reports IPC mismatch and cancellation without changing authority", async () => {
  const mismatch = createInMemoryIpcPair(TOKEN);
  const mismatchHost = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: mismatch.client,
    terminal: new PrivateTerminal(),
  });
  try {
    await mismatchHost.init();
    const prompt = mismatchHost.prompt("mismatch test");
    const proposal = await mismatch.server.receiveProposal();
    assert.ok(proposal);
    await mismatch.server.sendResult({
      ...denyResult(proposal),
      canonical_arguments_digest: "tbound-args-jcs-rfc8785/v1:sha256:" + "0".repeat(64),
    });
    await prompt.catch(() => undefined);
    const mismatchErrors = mismatchHost.observations.filter((item) => item.type === "ipc_error");
    assert.equal(mismatchErrors.length, 1);
    assert.equal(mismatchErrors[0]?.code, "ERR_IPC_CORRELATION_MISMATCH");
  } finally {
    await mismatchHost.dispose();
    mismatch.server.close();
  }

  const unbound = createInMemoryIpcPair(TOKEN);
  const unboundTerminal = new PrivateTerminal();
  const unboundHost = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: unbound.client,
    terminal: unboundTerminal,
  });
  try {
    await unboundHost.init();
    const prompt = unboundHost.prompt("missing digest test");
    const proposal = await unbound.server.receiveProposal();
    assert.ok(proposal);
    const { canonical_arguments_digest: _omitted, ...missingDigest } = denyResult(proposal);
    await unbound.server.sendResult(missingDigest);
    await prompt.catch(() => undefined);
    assert.equal(unboundHost.observations.filter((item) => item.type === "tool_result").length, 0);
    assert.equal(unboundHost.observations.filter((item) => item.type === "ipc_error").some((item) => item.code === "ERR_IPC_CORRELATION_MISMATCH"), true);
    assert.equal(unboundTerminal.writes.join("").includes("native Pi fixture continuation"), false);
  } finally {
    await unboundHost.dispose();
    unbound.server.close();
  }

  const cancellation = createInMemoryIpcPair(TOKEN);
  const cancellationHost = await createNativePiFixtureHost({
    cwd: process.cwd(),
    ipc: cancellation.client,
    terminal: new PrivateTerminal(),
  });
  try {
    await cancellationHost.init();
    const prompt = cancellationHost.prompt("cancellation test");
    const proposal = await cancellation.server.receiveProposal();
    assert.ok(proposal);
    await cancellationHost.dispose();
    await prompt.catch(() => undefined);
    const cancellationErrors = cancellationHost.observations.filter((item) => item.type === "ipc_error");
    assert.ok(cancellationErrors.some((item) => item.code === "ERR_IPC_ABORTED" || item.code === "ERR_IPC_CLOSED"));
  } finally {
    cancellation.server.close();
  }
});

test("production/native provider construction refuses without effects", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const observations: string[] = [];
  try {
    await assert.rejects(
      createNativePiProductionHost({
        cwd: process.cwd(),
        ipc: client,
        terminal: new PrivateTerminal(),
        onObservation: (observation) => {
          observations.push(observation.type);
          throw new Error("synthetic observer failure");
        },
      }),
      (error: unknown) => error instanceof NativePiHostRefusal && error.message === NATIVE_PI_PRODUCTION_REFUSAL,
    );
    assert.deepEqual(observations, ["host_refusal"]);
  } finally {
    client.close();
    server.close();
  }
});
