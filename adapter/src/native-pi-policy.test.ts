import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { test } from "node:test";
import type { Terminal } from "@earendil-works/pi-tui";
import {
  createInMemoryIpcPair,
  type IpcProposal,
  type IpcResult,
} from "./ipc-transport.ts";
import {
  createDefaultNativePiFixturePolicy,
  assertNativePiPolicyArtifact,
  loadNativePiPolicyInteractiveMode,
  NATIVE_PI_POLICY_VERSION,
  NativePiPolicyRefusal,
  validateNativePiPolicyArtifactBytes,
  type NativePiPolicyAction,
} from "./native-pi-policy.ts";
import { createNativePiPolicyFixtureHost, NativePiHostRefusal } from "./native-pi-host.ts";

const TOKEN = "cd".repeat(32);

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalJson(record[key])}`).join(",")}}`;
  }
  return JSON.stringify(value);
}

class VirtualTerminal implements Terminal {
  readonly writes: string[] = [];
  private inputHandler?: (data: string) => void;
  private stopped = false;
  deliveredCount = 0;
  inputCount = 0;
  startCount = 0;
  private writeCount = 0;
  onWriteCount?: { count: number; callback: () => void };

  start(onInput: (data: string) => void, _onResize: () => void): void {
    this.startCount += 1;
    this.inputHandler = onInput;
    this.stopped = false;
  }

  stop(): void {
    this.inputHandler = undefined;
    this.stopped = true;
  }

  async drainInput(): Promise<void> {}

  send(data: string): void {
    this.inputCount += 1;
    if (this.inputHandler) this.deliveredCount += 1;
    this.inputHandler?.(data);
  }

  write(data: string): void {
    this.writes.push(data);
    this.writeCount += 1;
    if (this.onWriteCount?.count === this.writeCount) {
      const callback = this.onWriteCount.callback;
      this.onWriteCount = undefined;
      setImmediate(callback);
    }
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
    response_id: { issuer: "private-native-policy-supervisor", opaque: `response-${proposal.tool_call_id}` },
    canonical_arguments_digest: `tbound-args-jcs-rfc8785/v1:sha256:${createHash("sha256").update(canonicalJson(proposal.arguments)).digest("hex")}`,
    verdict: "DENY",
    reason_code: "private_native_policy_test",
    policy_digest: "tbound-policy/v1:private-native-policy-test",
  };
}

function sleep(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function receiveWithin(server: { receiveProposal(): Promise<IpcProposal | null> }, milliseconds: number): Promise<IpcProposal> {
  return await Promise.race([
    server.receiveProposal().then((proposal) => {
      if (!proposal) throw new Error("private policy test IPC closed before proposal");
      return proposal;
    }),
    sleep(milliseconds).then(() => {
      throw new Error(`private policy test timed out waiting for IPC proposal after ${milliseconds}ms`);
    }),
  ]);
}

function actionLabel(action: NativePiPolicyAction): string {
  if (action.type === "submit") {
    const prefix = action.semantic === "user_bash" ? (action.excludeFromContext ? "!!" : "!") : "";
    return `${prefix}${action.semantic}:${action.command ?? ""}`;
  }
  if (action.type === "shortcut") return action.action;
  return action.type;
}

test("patched InteractiveMode default-denies when no policy callback is installed", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new VirtualTerminal();
  const host = await createNativePiPolicyFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
  });
  try {
    await host.init();
    const pendingPrompt = host.nativePrompt().then(
      () => undefined,
      (error: unknown) => error,
    );
    for (const character of "ordinary text") terminal.send(character);
    terminal.send("\r");
    await sleep(50);
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
    const denied = host.observations.filter((item) => item.type === "policy_decision" && !item.decision.allow);
    assert.ok(denied.some((item) => item.type === "policy_decision" && item.action.type === "submit" && item.action.semantic === "prompt"));
    await host.dispose();
    assert.ok((await pendingPrompt) instanceof NativePiHostRefusal);
    terminal.send("late input after dispose");
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
  } finally {
    await host.dispose();
    server.close();
  }
  assert.equal(terminal.isStopped, true);
});

test("temporary init handlers are guarded before terminal input starts, regardless of caller allow-all", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new VirtualTerminal();
  const callerPolicyActions: NativePiPolicyAction[] = [];
  const host = await createNativePiPolicyFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
    actionPolicy: async (action) => {
      callerPolicyActions.push(action);
      return { allow: true };
    },
  });
  terminal.onWriteCount = { count: 2, callback: () => {
    terminal.send("\x04");
    terminal.send("\x03");
    terminal.send("\x03");
    for (const character of "/login") terminal.send(character);
    terminal.send("\r");
  } };
  try {
    await host.init();
    await sleep(50);
    const startupSessionId = host.sessionView.sessionId;
    assert.ok(terminal.inputCount > 0, "virtual terminal did not inject during init");
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
    assert.equal(host.sessionView.sessionId, startupSessionId);
    // Exercise the same effect routes once the editor is ready. They remain
    // permanently denied by the artifact/host boundary even with allow-all.
    const pendingPrompt = host.nativePrompt().then(
      () => undefined,
      (error: unknown) => error,
    );
    terminal.send("\x04");
    terminal.send("\x03");
    terminal.send("\x03");
    terminal.send("/login");
    terminal.send("\r");
    await sleep(50);
    const decisions = host.observations.filter((item) => item.type === "policy_decision");
    assert.ok(decisions.some((item) => item.action.type === "shortcut" && item.action.action === "app.exit" && !item.decision.allow));
    assert.ok(decisions.some((item) => item.action.type === "shortcut" && item.action.action === "app.clear" && !item.decision.allow));
    assert.ok(decisions.some((item) => item.action.type === "submit" && item.action.semantic === "builtin_command" && !item.decision.allow));
    assert.equal(callerPolicyActions.length, 0, "caller policy must not be consulted for permanently denied init actions");
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
    assert.equal(host.sessionView.sessionId, startupSessionId);
    await host.dispose();
    assert.ok((await pendingPrompt) instanceof NativePiHostRefusal);
  } finally {
    terminal.onWriteCount = undefined;
    await host.dispose();
    server.close();
  }
});

test("real patched editor permanently denies shell, slash, selector, session, and shortcut effects under allow-all", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new VirtualTerminal();
  const callerPolicyActions: NativePiPolicyAction[] = [];
  const host = await createNativePiPolicyFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
    actionPolicy: async (action) => {
      callerPolicyActions.push(action);
      return { allow: true };
    },
  });
  try {
    await host.init();
    const pendingPrompt = host.nativePrompt().then(
      () => undefined,
      (error: unknown) => error,
    );
    terminal.send("\x1b[200~");
    for (const character of "/login") terminal.send(character);
    terminal.send("\x1b[201~");
    terminal.send("\r");
    await sleep(20);
    for (const text of ["!echo denied", "!!echo denied", "/login", "/model", "/reload", "/new", "/resume", "/fork", "/share"]) {
      if (text === "/model") terminal.send(text);
      else for (const character of text) terminal.send(character);
      terminal.send("\r");
      await sleep(10);
    }
    for (const key of ["\x04", "\x03", "\x03", "\x16", "\x1b", "\x1b"]) {
      terminal.send(key);
      await sleep(10);
    }
    await sleep(50);
    const decisions = host.observations.filter((item) => item.type === "policy_decision");
    for (const expected of [
      "!user_bash:echo denied",
      "!!user_bash:echo denied",
      "builtin_command:login",
      "builtin_command:model",
      "builtin_command:reload",
      "builtin_command:new",
      "builtin_command:resume",
      "builtin_command:fork",
      "builtin_command:share",
      "app.exit",
      "app.clear",
      "app.session.tree",
    ]) {
      assert.ok(decisions.some(({ action, decision }) => actionLabel(action) === expected && !decision.allow), `missing permanent denial for ${expected}`);
    }
    assert.ok(callerPolicyActions.every((action) =>
      (action.type === "submit" && (action.semantic === "prompt" || action.semantic === "steer")) ||
      (action.type === "shortcut" && ["app.interrupt", "app.interrupt_bash", "editor.clear_bash", "app.tools.expand", "app.thinking.toggle", "app.message.followUp"].includes(action.action)),
    ), "caller allow-all must not be consulted for forbidden actions");
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
    assert.ok(terminal.writes.join("").includes("fixture action permanently refused"));
    await host.dispose();
    assert.ok((await pendingPrompt) instanceof NativePiHostRefusal);
  } finally {
    await host.dispose();
    server.close();
  }
});

test("patched policy rejects thrown and malformed callbacks before prompt dispatch", async () => {
  for (const actionPolicy of [
    async () => { throw new Error("synthetic policy callback failure"); },
    async () => ({ allow: "yes" } as unknown as { allow: boolean }),
  ]) {
    const { client, server } = createInMemoryIpcPair(TOKEN);
    const terminal = new VirtualTerminal();
    const host = await createNativePiPolicyFixtureHost({
      cwd: process.cwd(),
      ipc: client,
      terminal,
      actionPolicy,
    });
    try {
      await host.init();
      const prompt = host.nativePrompt().then(
        () => undefined,
        (error: unknown) => error,
      );
      for (const character of "should be refused") terminal.send(character);
      terminal.send("\r");
      await sleep(30);
      assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 0);
      assert.ok(host.observations.some((item) =>
        item.type === "policy_decision" && item.action.type === "submit" && item.action.semantic === "prompt" && !item.decision.allow,
      ));
      await host.dispose();
      assert.ok((await prompt) instanceof NativePiHostRefusal);
    } finally {
      await host.dispose();
      server.close();
    }
  }
});

test("private Pi policy artifact is pinned, real, and loadable", async () => {
  await assertNativePiPolicyArtifact();
  const Constructor = await loadNativePiPolicyInteractiveMode();
  assert.equal(typeof Constructor, "function");
  const patchedSource = readFileSync(resolve(process.cwd(), "native-pi-policy/patched-interactive-mode.js"), "utf8");
  assert.equal(NATIVE_PI_POLICY_VERSION, "pi-0.87.1");
  for (const marker of [
    "classifyNativeSubmit",
    "native action refused without a policy",
    "Guard even the temporary handlers before the TUI starts accepting terminal bytes",
    "type: \"extension_shortcut\"",
    "app.clipboard.pasteImage",
    "app.clipboard.rightClickPaste",
    "app.abort_compaction",
    "app.abort_retry",
    "app.session.new",
    "app.model.select",
  ]) {
    assert.ok(patchedSource.includes(marker), `patched policy source is missing ${marker}`);
  }
  const temporaryExitGuard = patchedSource.indexOf('this.defaultEditor.onCtrlD = () => {\n            void this.runNativeAction({ type: "shortcut", action: "app.exit" }');
  const tuiStart = patchedSource.indexOf("        this.ui.start();", temporaryExitGuard);
  assert.ok(temporaryExitGuard >= 0 && tuiStart > temporaryExitGuard, "temporary exit handler must be guarded before TUI input starts");
  const startupSubmitGuard = patchedSource.indexOf('semantic: "startup", text }, () => this.handleStartupSubmit(text)');
  assert.ok(startupSubmitGuard >= 0 && startupSubmitGuard < tuiStart, "temporary startup submit handler must be guarded before TUI input starts");
});

test("patched InteractiveMode admits native prompts/steer/follow-up and denies effects before dispatch", async () => {
  const { client, server } = createInMemoryIpcPair(TOKEN);
  const terminal = new VirtualTerminal();
  const decisions: Array<{ action: NativePiPolicyAction; allow: boolean }> = [];
  const policy = createDefaultNativePiFixturePolicy((action, decision) => {
    decisions.push({ action, allow: decision.allow });
  });
  const host = await createNativePiPolicyFixtureHost({
    cwd: process.cwd(),
    ipc: client,
    terminal,
    actionPolicy: policy,
  });
  const expectedTools = ["read", "write"] as const;
  let releaseFirst!: () => void;
  const firstProposalHeld = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  let firstProposalSeen!: () => void;
  const firstProposalReady = new Promise<void>((resolve) => {
    firstProposalSeen = resolve;
  });
  const seen: IpcProposal[] = [];
  const supervisor = (async () => {
    for (let index = 0; index < expectedTools.length; index += 1) {
      const proposal = await receiveWithin(server, 5_000);
      seen.push(proposal);
      assert.equal(proposal.tool, expectedTools[index]);
      if (index === 0) {
        firstProposalSeen();
        await firstProposalHeld;
      }
      await server.sendResult(denyResult(proposal));
    }
  })();

  try {
    await host.init();
    const initialSessionId = host.sessionView.sessionId;
    const nativePrompt = host.nativePrompt();
    for (const character of "ordinary native prompt") terminal.send(character);
    terminal.send("\r");
    await firstProposalReady;

    terminal.send("native steer while provider is streaming");
    terminal.send("\r");
    await sleep(50);
    terminal.send("native follow-up while provider is streaming");
    terminal.send("\x11");
    await sleep(50);
    assert.ok(host.sessionView.pendingMessageCount > 0, "native steer/follow-up did not queue on the real AgentSession");
    releaseFirst();
    await nativePrompt;
    await supervisor;

    assert.deepEqual(seen.map((proposal) => proposal.tool), ["read", "write"]);
    assert.equal(host.sessionView.sessionId, initialSessionId);
    assert.equal(host.observations.filter((item) => item.type === "tool_proposal").length, 2);
    assert.deepEqual(
      host.observations
        .filter((item) => item.type === "fixture_provider_continuation")
        .map((item) => [item.tool, item.tool_call_id, item.preceding_result_verdict]),
      [
        ["read", "native-fixture-read-1", "DENY"],
        ["write", "native-fixture-write-1", "DENY"],
      ],
      "faux provider continuations must consume the preceding correlated DENY results",
    );

    const labels = decisions.map(({ action }) => actionLabel(action));
    assert.ok(labels.includes("prompt:"));
    assert.ok(labels.includes("steer:"));
    assert.ok(labels.includes("app.message.followUp"));
    const firstProposalIndex = host.observations.findIndex((item) => item.type === "tool_proposal");
    const promptDecisionIndex = host.observations.findIndex((item) => item.type === "policy_decision" && item.decision.allow && item.action.type === "submit");
    assert.ok(promptDecisionIndex >= 0 && promptDecisionIndex < firstProposalIndex, "policy decision did not precede provider/tool effects");
  } finally {
    await host.dispose();
    server.close();
  }
  assert.equal(terminal.isStopped, true);
});

test("private policy artifact guard rejects source, output, and manifest drift", () => {
  const artifactDir = resolve(process.cwd(), "native-pi-policy");
  const input = readFileSync(resolve(process.cwd(), "node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/interactive-mode.js"));
  const output = readFileSync(resolve(artifactDir, "patched-interactive-mode.js"));
  const packageJson = JSON.parse(readFileSync(resolve(process.cwd(), "node_modules/@earendil-works/pi-coding-agent/package.json"), "utf8")) as { name: string; version: string };
  const manifest = JSON.parse(readFileSync(resolve(artifactDir, "manifest.json"), "utf8"));
  assert.throws(
    () => validateNativePiPolicyArtifactBytes(Buffer.from(`${input.toString("utf8")}\n`), output, packageJson, manifest),
    (error: unknown) => error instanceof NativePiPolicyRefusal && error.code === "ERR_NATIVE_PI_POLICY_REFUSED",
  );
  assert.throws(
    () => validateNativePiPolicyArtifactBytes(input, Buffer.from(`${output.toString("utf8")}\n`), packageJson, manifest),
    (error: unknown) => error instanceof NativePiPolicyRefusal && error.code === "ERR_NATIVE_PI_POLICY_REFUSED",
  );
  assert.throws(
    () => validateNativePiPolicyArtifactBytes(input, output, { ...packageJson, version: "0.87.0" }, manifest),
    (error: unknown) => error instanceof NativePiPolicyRefusal && error.code === "ERR_NATIVE_PI_POLICY_REFUSED",
  );
  assert.throws(
    () => validateNativePiPolicyArtifactBytes(input, output, packageJson, {
      ...manifest,
      input: { ...manifest.input, contextChecks: ["changed context"] },
    }),
    (error: unknown) => error instanceof NativePiPolicyRefusal && error.code === "ERR_NATIVE_PI_POLICY_REFUSED",
  );
});

test("private policy NOTICE matches installed Pi attribution and declared license", () => {
  const packageJson = JSON.parse(readFileSync(resolve(process.cwd(), "node_modules/@earendil-works/pi-coding-agent/package.json"), "utf8")) as {
    name: string;
    version: string;
    author: string;
    license: string;
    repository: { url: string };
  };
  const notice = readFileSync(resolve(process.cwd(), "native-pi-policy/NOTICE.md"), "utf8");
  assert.equal(packageJson.name, "@earendil-works/pi-coding-agent");
  assert.equal(packageJson.version, "0.87.1");
  assert.equal(packageJson.author, "Mario Zechner");
  assert.equal(packageJson.license, "MIT");
  assert.ok(notice.includes(packageJson.name));
  assert.ok(notice.includes(packageJson.author));
  assert.ok(notice.includes(packageJson.repository.url));
  assert.ok(notice.includes("license as"));
  assert.ok(notice.includes("`MIT`"));
});
