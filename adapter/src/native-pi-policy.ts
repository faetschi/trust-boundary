import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { AgentSessionRuntime } from "@earendil-works/pi-coding-agent";
import type { Terminal } from "@earendil-works/pi-tui";

export type NativePiPolicyAction =
  | {
      type: "submit";
      semantic: "prompt" | "steer" | "startup" | "user_bash" | "builtin_command";
      text: string;
      command?: string;
      excludeFromContext?: boolean;
    }
  | {
      type: "shortcut";
      action: string;
    }
  | {
      type: "extension_shortcut";
      shortcut: string;
      extension: string;
    }
  | {
      type: "startup";
      operations: readonly string[];
    };

export interface NativePiPolicyDecision {
  allow: boolean;
  reason?: string;
}

export type NativePiActionPolicy = (
  action: NativePiPolicyAction,
) => NativePiPolicyDecision | Promise<NativePiPolicyDecision>;

export interface PatchedInteractiveModeOptions {
  terminal?: Terminal;
  verbose?: boolean;
  tuiMode?: string;
  startupDiagnostics?: readonly unknown[];
  actionPolicy?: NativePiActionPolicy;
}

export interface PatchedInteractiveMode {
  init(): Promise<void>;
  getUserInput(): Promise<string>;
  stop(...args: unknown[]): void;
}

export interface PatchedInteractiveModeConstructor {
  new (
    runtimeHost: AgentSessionRuntime,
    options?: PatchedInteractiveModeOptions,
  ): PatchedInteractiveMode;
}

export const NATIVE_PI_POLICY_VERSION = "pi-0.87.1" as const;
export const NATIVE_PI_POLICY_INPUT_SHA256 = "7dc366e8609d7d1e81fb85952e466aea9933b13e74842c2a9b9df678d267d12e" as const;
export const NATIVE_PI_POLICY_INPUT_BYTES = 268619 as const;
export const NATIVE_PI_POLICY_OUTPUT_SHA256 = "d48a7c98e2ee2f97ecf4b148b37098dae35fb5afe874318879c5d7d953cab953" as const;
export const NATIVE_PI_POLICY_OUTPUT_BYTES = 286139 as const;

const CONTEXT_CHECKS = [
  "export class InteractiveMode {",
  "        this.defaultEditor.onSubmit = async (text) => {",
  "        this.defaultEditor.onEscape = () => {\n            if (this.session.isStreaming)",
  "// Accept text while startup completes, but only enable interrupt, exit, and submission feedback.",
  'this.defaultEditor.onAction("app.model.cycleForward", () => this.cycleModel("forward"));',
  "this.defaultEditor.onExtensionShortcut = (data) => {",
  "async run() {",
] as const;

export class NativePiPolicyRefusal extends Error {
  readonly code = "ERR_NATIVE_PI_POLICY_REFUSED" as const;

  constructor(message: string) {
    super(message);
    this.name = "NativePiPolicyRefusal";
  }
}

interface NativePiPolicyManifest {
  package?: string;
  version?: string;
  input?: { sha256?: string; bytes?: number; contextChecks?: string[] };
  output?: { sha256?: string; bytes?: number };
}

export function createDefaultNativePiFixturePolicy(
  onDecision?: (action: NativePiPolicyAction, decision: NativePiPolicyDecision) => void,
): NativePiActionPolicy {
  return async (action) => {
    const allow = action.type === "submit"
      ? action.semantic === "prompt" || action.semantic === "steer"
      : action.type === "shortcut"
        ? [
            "app.interrupt",
            "app.interrupt_bash",
            "editor.clear_bash",
            "app.tools.expand",
            "app.thinking.toggle",
            "app.message.followUp",
          ].includes(action.action)
        : false;
    const label = action.type === "shortcut"
      ? action.action
      : action.type === "submit"
        ? `${action.semantic}:${action.command ?? ""}`
        : action.type;
    const decision: NativePiPolicyDecision = allow
      ? { allow: true }
      : { allow: false, reason: `fixture policy denied ${label}` };
    onDecision?.(action, decision);
    return decision;
  };
}

function sha256(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function countContext(source: string, context: string): number {
  return source.split(context).length - 1;
}

export function validateNativePiPolicyArtifactBytes(
  input: Uint8Array,
  output: Uint8Array,
  packageJson: { name?: string; version?: string },
  manifest: NativePiPolicyManifest,
): void {
  if (packageJson.name !== "@earendil-works/pi-coding-agent" || packageJson.version !== "0.87.1") {
    throw new NativePiPolicyRefusal("native Pi policy refused: installed Pi package identity/version mismatch");
  }
  if (input.byteLength !== NATIVE_PI_POLICY_INPUT_BYTES || sha256(input) !== NATIVE_PI_POLICY_INPUT_SHA256) {
    throw new NativePiPolicyRefusal("native Pi policy refused: installed InteractiveMode source hash/size mismatch; rebuild the reviewed artifact");
  }
  for (const context of CONTEXT_CHECKS) {
    if (countContext(Buffer.from(input).toString("utf8"), context) !== 1) {
      throw new NativePiPolicyRefusal(`native Pi policy refused: installed InteractiveMode context drift for ${JSON.stringify(context)}`);
    }
  }
  if (output.byteLength !== NATIVE_PI_POLICY_OUTPUT_BYTES || sha256(output) !== NATIVE_PI_POLICY_OUTPUT_SHA256) {
    throw new NativePiPolicyRefusal("native Pi policy refused: patched InteractiveMode output hash/size mismatch");
  }
  if (manifest.package !== "@earendil-works/pi-coding-agent" ||
      manifest.version !== NATIVE_PI_POLICY_VERSION.slice(3) ||
      manifest.input?.sha256 !== NATIVE_PI_POLICY_INPUT_SHA256 ||
      manifest.input?.bytes !== NATIVE_PI_POLICY_INPUT_BYTES ||
      manifest.output?.sha256 !== NATIVE_PI_POLICY_OUTPUT_SHA256 ||
      manifest.output?.bytes !== NATIVE_PI_POLICY_OUTPUT_BYTES ||
      JSON.stringify(manifest.input?.contextChecks) !== JSON.stringify(CONTEXT_CHECKS)) {
    throw new NativePiPolicyRefusal("native Pi policy refused: output manifest mismatch");
  }
}

export async function assertNativePiPolicyArtifact(): Promise<void> {
  const artifactDir = resolve(dirname(fileURLToPath(import.meta.url)), "..", "native-pi-policy");
  const adapterDir = resolve(artifactDir, "..");
  const inputPath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "modes", "interactive", "interactive-mode.js");
  const packagePath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "package.json");
  const outputPath = join(artifactDir, "patched-interactive-mode.js");
  const manifestPath = join(artifactDir, "manifest.json");
  let input: Buffer;
  let output: Buffer;
  let packageJson: { name?: string; version?: string } = {};
  let manifest: NativePiPolicyManifest = {};
  try {
    [input, output] = await Promise.all([readFile(inputPath), readFile(outputPath)]);
    packageJson = JSON.parse(await readFile(packagePath, "utf8")) as typeof packageJson;
    manifest = JSON.parse(await readFile(manifestPath, "utf8")) as typeof manifest;
  } catch (error) {
    throw new NativePiPolicyRefusal(`native Pi policy artifact unavailable: ${error instanceof Error ? error.message : String(error)}`);
  }
  validateNativePiPolicyArtifactBytes(input, output, packageJson, manifest);
}

export async function loadNativePiPolicyInteractiveMode(): Promise<PatchedInteractiveModeConstructor> {
  const artifactPath = resolve(dirname(fileURLToPath(import.meta.url)), "..", "native-pi-policy", "patched-interactive-mode.js");
  let input: Buffer;
  let output: Buffer;
  let packageJson: { name?: string; version?: string };
  let manifest: NativePiPolicyManifest;
  try {
    const artifactDir = dirname(artifactPath);
    const adapterDir = resolve(artifactDir, "..");
    const inputPath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "modes", "interactive", "interactive-mode.js");
    const packagePath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "package.json");
    const manifestPath = join(artifactDir, "manifest.json");
    [input, output] = await Promise.all([readFile(inputPath), readFile(artifactPath)]);
    packageJson = JSON.parse(await readFile(packagePath, "utf8")) as typeof packageJson;
    manifest = JSON.parse(await readFile(manifestPath, "utf8")) as NativePiPolicyManifest;
  } catch (error) {
    throw new NativePiPolicyRefusal(`native Pi policy artifact unavailable: ${error instanceof Error ? error.message : String(error)}`);
  }
  validateNativePiPolicyArtifactBytes(input, output, packageJson, manifest);
  // Import the same validated immutable byte snapshot; do not re-open the pathname
  // between hash verification and module evaluation.
  const moduleUrl = `data:text/javascript;base64,${output.toString("base64")}`;
  const loaded = await import(moduleUrl);
  if (typeof loaded.InteractiveMode !== "function") {
    throw new NativePiPolicyRefusal("native Pi policy refused: patched module has no InteractiveMode constructor");
  }
  return loaded.InteractiveMode as PatchedInteractiveModeConstructor;
}
