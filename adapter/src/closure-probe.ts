import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { arch, platform, release, type as osType } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import {
  createAgentSession,
  createExtensionRuntime,
  ModelRuntime,
  SessionManager,
  SettingsManager,
  type ResourceLoader,
  type ToolDefinition,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { LockedResourceLoader } from "./locked-resource-loader.ts";
import {
  createProxyTools,
  declaredToolNames,
  type ProposalSender,
  type ToolProposal,
} from "./proxy-tools.ts";

type Profile = {
  pi_package: string;
  pi_version: string;
  schema_format: string;
  tools: string[];
  schema_sha256: Record<string, string>;
};

type InventoryEntry = {
  name: string;
  source: string;
  source_path: string;
  schema_sha256: string;
};

type SurfaceSession = Awaited<ReturnType<typeof createAgentSession>>["session"];

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

class InMemoryProbeSender implements ProposalSender {
  readonly proposals: ToolProposal[] = [];

  async send(proposal: ToolProposal) {
    this.proposals.push(structuredClone(proposal));
    return { verdict: "DENY", reason: "probe-only" } as const;
  }
}

function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonicalJson(record[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

function schemaDigest(schema: unknown): string {
  const jsonSchema = JSON.parse(JSON.stringify(schema)) as unknown;
  return createHash("sha256").update(canonicalJson(jsonSchema)).digest("hex");
}

function piPackageVersion(): string {
  const entry = fileURLToPath(import.meta.resolve("@earendil-works/pi-coding-agent"));
  const packagePath = resolve(dirname(entry), "..", "package.json");
  const manifest = JSON.parse(readFileSync(packagePath, "utf8")) as { name: string; version: string };
  assert.equal(manifest.name, "@earendil-works/pi-coding-agent");
  return manifest.version;
}

function sha256File(path: URL | string): string {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function assertResourcesClosed(loader: ResourceLoader): void {
  const extensions = loader.getExtensions();
  const skills = loader.getSkills();
  const prompts = loader.getPrompts();
  const themes = loader.getThemes();
  const agents = loader.getAgentsFiles();

  assert.equal(extensions.extensions.length, 0, "extension discovery returned an extension");
  assert.equal(extensions.errors.length, 0, "extension discovery reported an error");
  assert.equal(skills.skills.length, 0, "resource loader returned a skill");
  assert.equal(skills.diagnostics.length, 0, "resource loader returned skill diagnostics");
  assert.equal(prompts.prompts.length, 0, "resource loader returned a prompt template");
  assert.equal(prompts.diagnostics.length, 0, "resource loader returned prompt diagnostics");
  assert.equal(themes.themes.length, 0, "resource loader returned a theme");
  assert.equal(themes.diagnostics.length, 0, "resource loader returned theme diagnostics");
  assert.equal(agents.agentsFiles.length, 0, "resource loader returned a context file");
  assert.equal(loader.getSystemPrompt(), undefined, "resource loader returned a system prompt");
  assert.equal(loader.getSystemPromptSource(), undefined, "resource loader returned a system-prompt source");
  assert.deepEqual(loader.getAppendSystemPrompt(), [], "resource loader returned appended system text");
  assert.deepEqual(loader.getAppendSystemPromptSources(), [], "resource loader returned appended prompt sources");
}

function inventoryOf(session: SurfaceSession): InventoryEntry[] {
  return session.getAllTools().map((toolInfo) => {
    const definition = session.getToolDefinition(toolInfo.name);
    assert.ok(definition, `tool ${toolInfo.name} has no registered definition`);
    return {
      name: toolInfo.name,
      source: toolInfo.sourceInfo.source,
      source_path: toolInfo.sourceInfo.path,
      schema_sha256: schemaDigest(definition.parameters),
    };
  });
}

function assertInventoryClosed(
  session: SurfaceSession,
  expectedNames: readonly string[],
  expectedSchemaDigests?: Record<string, string>,
): InventoryEntry[] {
  const expected = [...expectedNames].sort();
  const inventory = inventoryOf(session);
  const actual = inventory.map(({ name }) => name).sort();
  const active = session.getActiveToolNames().sort();
  const executable = session.agent.state.tools.map((tool) => tool.name).sort();

  assert.deepEqual(actual, expected, "registered Pi tool inventory drifted");
  assert.deepEqual(active, expected, "active Pi tools drifted");
  assert.deepEqual(executable, expected, "agent executable tool set drifted");

  for (const tool of inventory) {
    assert.equal(tool.source, "sdk", `${tool.name} did not replace its native definition with an SDK tool`);
    assert.equal(tool.source_path, `<sdk:${tool.name}>`, `${tool.name} came from an unexpected source`);
    if (expectedSchemaDigests) {
      assert.equal(
        tool.schema_sha256,
        expectedSchemaDigests[tool.name],
        `${tool.name} parameter schema digest drifted`,
      );
    }
  }
  return inventory;
}

function assertFailsClosed(testName: string, check: () => void): void {
  assert.throws(check, /.+/, `${testName}: admission incorrectly accepted the negative fixture`);
}

function inertExtraTool(name: string): ToolDefinition {
  return {
    name,
    label: name,
    description: "Negative fixture. It must never become part of the admitted surface.",
    parameters: Type.Object({ value: Type.String() }),
    async execute() {
      throw new Error("negative fixture tool must never execute");
    },
  };
}

function poisonedResourceLoader(
  base: LockedResourceLoader,
  poison: "extension" | "skill" | "context",
): ResourceLoader {
  const loader = {
    getExtensions: () => ({
      ...base.getExtensions(),
      extensions: poison === "extension" ? [{}] : base.getExtensions().extensions,
    }),
    getSkills: () => ({
      ...base.getSkills(),
      skills: poison === "skill" ? [{}] : base.getSkills().skills,
    }),
    getPrompts: () => base.getPrompts(),
    getThemes: () => base.getThemes(),
    getAgentsFiles: () => ({
      agentsFiles: poison === "context" ? [{ path: "fixture/AGENTS.md", content: "injected" }] : [],
    }),
    getSystemPrompt: () => base.getSystemPrompt(),
    getSystemPromptSource: () => base.getSystemPromptSource(),
    getAppendSystemPrompt: () => base.getAppendSystemPrompt(),
    getAppendSystemPromptSources: () => base.getAppendSystemPromptSources(),
    extendResources: (paths: unknown) => base.extendResources(paths as never),
    reload: () => base.reload(),
  };
  return loader as unknown as ResourceLoader;
}

async function createSurfaceSession(options: {
  sender: ProposalSender;
  loader?: ResourceLoader;
  toolNames?: readonly string[];
  customTools?: ToolDefinition[];
}) {
  const streamAttempts = { count: 0 };
  const cwd = process.cwd();
  const modelRuntime = await ModelRuntime.create({
    credentials: new MemoryCredentialStore(),
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  modelRuntime.getAvailableSnapshot = () => [];
  modelRuntime.hasConfiguredAuth = () => false;
  modelRuntime.streamSimple = (() => {
    streamAttempts.count += 1;
    throw new Error("provider streaming is disabled in the closure probe");
  }) as typeof modelRuntime.streamSimple;

  const result = await createAgentSession({
    cwd,
    agentDir: resolve(cwd, ".probe-agent-data-do-not-create"),
    modelRuntime,
    settingsManager: SettingsManager.inMemory({
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
    }),
    sessionManager: SessionManager.inMemory(cwd),
    resourceLoader: options.loader ?? new LockedResourceLoader(),
    noTools: "builtin",
    tools: [...(options.toolNames ?? declaredToolNames)],
    customTools: options.customTools ?? createProxyTools(options.sender),
  });
  const selectedModel = result.session.model;
  assert.ok(
    !selectedModel || (selectedModel.provider === "unknown" && selectedModel.id === "unknown"),
    "probe unexpectedly selected a real provider model",
  );

  return { session: result.session, streamAttempts };
}

const sampleArguments: Record<string, Record<string, unknown>> = {
  read: { path: "probe-input.txt", offset: 1, limit: 1 },
  write: { path: "probe-output.txt", content: "probe only" },
  edit: { path: "probe-input.txt", edits: [{ oldText: "before", newText: "after" }] },
  bash: { command: "echo probe-only", timeout: 1 },
};

const unsupportedNativeToolNames = ["powershell", "grep", "find", "ls"] as const;

async function proveProxyDispatch(session: SurfaceSession, sender: InMemoryProbeSender): Promise<number> {
  const executable = new Map(session.agent.state.tools.map((tool) => [tool.name, tool]));
  let dispatched = 0;

  for (const name of declaredToolNames) {
    const tool = executable.get(name);
    assert.ok(tool, `missing executable proxy tool ${name}`);
    const callId = `probe-${name}`;
    const args = sampleArguments[name];
    const result = await tool.execute(callId, args as never, undefined, undefined);
    const proposal = sender.proposals.at(-1);
    assert.deepEqual(proposal, {
      schema_version: "tbound-proposal/v1",
      tool_call_id: callId,
      tool: name,
      arguments: args,
    });
    assert.equal(result.content[0]?.type, "text");
    assert.equal(result.content[0]?.text, JSON.stringify({ verdict: "DENY", reason: "probe-only" }));
    dispatched += 1;
  }

  assert.equal(sender.proposals.length, declaredToolNames.length, "a proxy tool dispatched more than once");
  return dispatched;
}

type NegativeCheck = {
  id: string;
  classification: "SDK-session" | "comparator-fixture";
  rejected: true;
  evidence: string;
};

async function runNegativeChecks(profile: Profile): Promise<NegativeCheck[]> {
  const results: NegativeCheck[] = [];

  for (const poison of ["extension", "skill", "context"] as const) {
    const loader = poisonedResourceLoader(new LockedResourceLoader(), poison);
    assertFailsClosed(`injected ${poison} resource`, () => assertResourcesClosed(loader));
    results.push({
      id: `injected-${poison}-resource-rejected`,
      classification: "comparator-fixture",
      rejected: true,
      evidence: "Resource admission predicate called directly with a poisoned loader; no Pi session created.",
    });
  }

  const injectedContextLoader = poisonedResourceLoader(new LockedResourceLoader(), "context");
  const injectedContextSender = new InMemoryProbeSender();
  const injectedContextResult = await createSurfaceSession({
    sender: injectedContextSender,
    loader: injectedContextLoader,
  });
  try {
    assertFailsClosed("SDK session with injected context resource", () => {
      assertResourcesClosed(injectedContextLoader);
      assertInventoryClosed(injectedContextResult.session, declaredToolNames);
    });
    assert.equal(
      injectedContextResult.streamAttempts.count,
      0,
      "injected context fixture attempted provider streaming",
    );
    results.push({
      id: "sdk-session-injected-context-resource-rejected",
      classification: "SDK-session",
      rejected: true,
      evidence: "A real createAgentSession was constructed with a loader returning an injected context file; the external resource admission predicate rejected that loader. This does not prove Pi consumed the file.",
    });
  } finally {
    injectedContextResult.session.dispose();
  }

  const extraSender = new InMemoryProbeSender();
  const extraTools = [...createProxyTools(extraSender), inertExtraTool("ls")];
  const extraSessionResult = await createSurfaceSession({
    sender: extraSender,
    customTools: extraTools,
    toolNames: [...profile.tools, "ls"],
  });
  try {
    assertFailsClosed("undeclared active tool", () =>
      assertInventoryClosed(extraSessionResult.session, declaredToolNames),
    );
    assert.equal(extraSessionResult.streamAttempts.count, 0, "negative tool fixture attempted provider streaming");
    results.push({
      id: "undeclared-tool-rejected",
      classification: "SDK-session",
      rejected: true,
      evidence: "Real Pi session inventory included an injected inert SDK tool named ls and failed the exact inventory check.",
    });
  } finally {
    extraSessionResult.session.dispose();
  }

  const replacementSender = new InMemoryProbeSender();
  const replacementTools = createProxyTools(replacementSender).filter((tool) => tool.name !== "bash");
  const replacementSessionResult = await createSurfaceSession({
    sender: replacementSender,
    customTools: replacementTools,
    toolNames: profile.tools,
  });
  try {
    assertFailsClosed("native same-name fallback", () =>
      assertInventoryClosed(replacementSessionResult.session, declaredToolNames),
    );
    assert.equal(
      replacementSessionResult.streamAttempts.count,
      0,
      "native fallback fixture attempted provider streaming",
    );
    results.push({
      id: "native-same-name-fallback-rejected",
      classification: "SDK-session",
      rejected: true,
      evidence: "Real Pi session omitted the bash proxy and exposed the native same-named built-in; source/inventory admission rejected it.",
    });
  } finally {
    replacementSessionResult.session.dispose();
  }

  if (Object.keys(profile.schema_sha256).length === declaredToolNames.length) {
    const altered = { ...profile.schema_sha256, read: "0".repeat(64) };
    const driftSender = new InMemoryProbeSender();
    const driftResult = await createSurfaceSession({ sender: driftSender });
    try {
      assertFailsClosed("schema digest drift", () =>
        assertInventoryClosed(driftResult.session, declaredToolNames, altered),
      );
      assert.equal(driftResult.streamAttempts.count, 0, "schema drift fixture attempted provider streaming");
      results.push({
        id: "schema-drift-rejected",
        classification: "comparator-fixture",
        rejected: true,
        evidence: "Real session schema was compared against an altered expected digest; this changes the comparator fixture, not the SDK session.",
      });
    } finally {
      driftResult.session.dispose();
    }
  }

  return results;
}

async function main(): Promise<void> {
  const profilePath = new URL("../profile.json", import.meta.url);
  const profile = JSON.parse(readFileSync(profilePath, "utf8")) as Profile;
  const printManifest = process.argv.includes("--print-manifest");

  const installedPiVersion = piPackageVersion();
  assert.equal(installedPiVersion, profile.pi_version, "installed Pi SDK version differs from profile pin");
  assert.deepEqual([...profile.tools].sort(), [...declaredToolNames].sort(), "profile tool names differ from adapter");

  const loader = new LockedResourceLoader();
  assertResourcesClosed(loader);
  assert.throws(() => loader.extendResources({} as never), /disabled/);
  await assert.rejects(loader.reload(), /disabled/);

  const sender = new InMemoryProbeSender();
  const { session, streamAttempts } = await createSurfaceSession({ sender, loader });
  try {
    assertResourcesClosed(loader);
    const inventory = assertInventoryClosed(
      session,
      profile.tools,
      printManifest ? undefined : profile.schema_sha256,
    );
    const proxyDispatches = await proveProxyDispatch(session, sender);

    const beforeUnknownActivation = session.getActiveToolNames().sort();
    for (const name of unsupportedNativeToolNames) {
      assert.equal(session.getToolDefinition(name), undefined, `unregistered native ${name} definition is reachable`);
      session.setActiveToolsByName([...beforeUnknownActivation, name]);
      assert.deepEqual(
        session.getActiveToolNames().sort(),
        beforeUnknownActivation,
        `unregistered native tool ${name} became active`,
      );
    }
    const negativeChecks = await runNegativeChecks(profile);
    negativeChecks.push(
      ...unsupportedNativeToolNames.map((name) => ({
        id: `native-${name}-unavailable`,
        classification: "SDK-session" as const,
        rejected: true as const,
        evidence: "Real Pi session did not register the native tool and refused its activation request.",
      })),
      {
        id: "resource-addition-rejected",
        classification: "comparator-fixture",
        rejected: true,
        evidence: "LockedResourceLoader.extendResources was called directly and threw; no resource discovery cycle was run.",
      },
      {
        id: "loader-reload-rejected",
        classification: "comparator-fixture",
        rejected: true,
        evidence: "LockedResourceLoader.reload was called directly and threw; no replacement loader was installed.",
      },
    );

    await assert.rejects(session.reload(), /disabled/);
    negativeChecks.push({
      id: "session-reload-rejected",
      classification: "SDK-session",
      rejected: true,
      evidence: "reload was requested on the real Pi session using the locked loader and rejected by the loader guard.",
    });
    assertResourcesClosed(loader);
    assertInventoryClosed(session, profile.tools, printManifest ? undefined : profile.schema_sha256);
    assert.equal(streamAttempts.count, 0, "a provider stream was attempted");

    const schemaSha256 = Object.fromEntries(inventory.map(({ name, schema_sha256 }) => [name, schema_sha256]));
    const lockfilePath = new URL("../package-lock.json", import.meta.url);
    const lockfile = JSON.parse(readFileSync(lockfilePath, "utf8")) as {
      packages?: Record<string, { version?: string }>;
    };
    const lockedPiVersion = lockfile.packages?.["node_modules/@earendil-works/pi-coding-agent"]?.version;
    assert.equal(lockedPiVersion, profile.pi_version, "package lock Pi version differs from profile pin");
    const report = {
      generated_at_utc: new Date().toISOString(),
      command: process.env.npm_lifecycle_event === "closure"
        ? "npm run check (typecheck, IPC tests, then closure probe)"
        : "node --experimental-strip-types src/closure-probe.ts",
      runtime: {
        node: process.version,
        os_type: osType(),
        os_release: release(),
        platform: platform(),
        architecture: arch(),
      },
      pi_package: {
        name: profile.pi_package,
        installed_version: installedPiVersion,
        locked_version: lockedPiVersion,
        package_lock_sha256: sha256File(lockfilePath),
      },
      profile_sha256: sha256File(profilePath),
      negative_check_classifications: {
        "SDK-session": "A real createAgentSession was constructed or inspected; each case's evidence states which session behavior was checked.",
        "comparator-fixture": "No altered SDK session was inspected; a standalone admission predicate or loader guard received synthetic input.",
      },
      tools: inventory,
      schema_manifest_sha256: createHash("sha256")
        .update(canonicalJson(schemaSha256))
        .digest("hex"),
      active_tool_names: session.getActiveToolNames().sort(),
      resources: "empty; additions and reload throw",
      proxy_dispatches: proxyDispatches,
      unavailable_native_tool_activation: Object.fromEntries(
        unsupportedNativeToolNames.map((name) => [name, "rejected"]),
      ),
      negative_checks: negativeChecks,
      provider_stream_attempts: streamAttempts.count,
      selected_provider_model: null,
      pi_model_placeholder: session.model ? `${session.model.provider}/${session.model.id}` : null,
      credential_store: "empty in-memory store; no provider key loaded from disk",
      session_reload: "rejected by locked resource loader",
      provider_broker_manifest_enforcement: "not part of adapter spike",
      surface_closed: "not established; broker enforcement is still required",
      probe_mode: printManifest ? "schema baseline capture" : "profile conformance",
      report_artifact: "artifacts/pi-closure-report.json",
    };

    const reportJson = `${JSON.stringify(report, null, 2)}\n`;
    const artifactsDir = fileURLToPath(new URL("../artifacts/", import.meta.url));
    mkdirSync(artifactsDir, { recursive: true });
    writeFileSync(resolve(artifactsDir, "pi-closure-report.json"), reportJson, "utf8");
    process.stdout.write(reportJson);
  } finally {
    session.dispose();
  }
}

main().catch((error: unknown) => {
  process.stderr.write(`${error instanceof Error ? error.stack ?? error.message : String(error)}\n`);
  process.exitCode = 1;
});
