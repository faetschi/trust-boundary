/**
 * Rebuild the private Pi InteractiveMode policy artifact from the exact pinned
 * installed 0.87.1 dist file. This is intentionally a narrow textual patcher:
 * it refuses source/version/context drift rather than silently producing a
 * superficially similar copy.
 */
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const artifactDir = dirname(fileURLToPath(import.meta.url));
const adapterDir = resolve(artifactDir, "..");
const inputPath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "modes", "interactive", "interactive-mode.js");
const packagePath = join(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "package.json");
const outputPath = join(artifactDir, "patched-interactive-mode.js");
const manifestPath = join(artifactDir, "manifest.json");
const pinnedVersion = "0.87.1";
const pinnedInputSha256 = "7dc366e8609d7d1e81fb85952e466aea9933b13e74842c2a9b9df678d267d12e";

const contextChecks = [
  "export class InteractiveMode {",
  "        this.defaultEditor.onSubmit = async (text) => {",
  "        this.defaultEditor.onEscape = () => {\n            if (this.session.isStreaming)",
  "// Accept text while startup completes, but only enable interrupt, exit, and submission feedback.",
  'this.defaultEditor.onAction("app.model.cycleForward", () => this.cycleModel("forward"));',
  "this.defaultEditor.onExtensionShortcut = (data) => {",
  "async run() {",
];

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function replaceOnce(source, needle, replacement, label) {
  const count = source.split(needle).length - 1;
  if (count !== 1) {
    throw new Error(`native Pi policy patch refused: ${label} context count ${count}, expected 1`);
  }
  return source.replace(needle, replacement);
}

function rewriteInternalImports(source) {
  const resolveImport = (specifier) => specifier.startsWith(".")
    ? pathToFileURL(resolve(adapterDir, "node_modules", "@earendil-works", "pi-coding-agent", "dist", "modes", "interactive", specifier)).href
    : import.meta.resolve(specifier);
  return source
    .replace(/^(\s*import\b[^\n]*?\bfrom\s+)"([^\"]+)"/gm, (_match, prefix, specifier) => `${prefix}"${resolveImport(specifier)}"`)
    .replace(/^(\s*export\b[^\n]*?\bfrom\s+)"([^\"]+)"/gm, (_match, prefix, specifier) => `${prefix}"${resolveImport(specifier)}"`)
    .replace(/^(\s*import\s+)"([^\"]+)"/gm, (_match, prefix, specifier) => `${prefix}"${resolveImport(specifier)}"`);
}

function patchPolicyHelpers(source) {
  const marker = 'const DEAD_TERMINAL_ERROR_CODES = new Set(["EIO", "EPIPE", "ENOTCONN"]);';
  const insertion = `${marker}
function classifyNativeSubmit(text, streaming) {
    if (text.startsWith("!!")) {
        return { type: "submit", semantic: "user_bash", command: text.slice(2).trim(), excludeFromContext: true };
    }
    if (text.startsWith("!")) {
        return { type: "submit", semantic: "user_bash", command: text.slice(1).trim(), excludeFromContext: false };
    }
    if (text.startsWith("/")) {
        const space = text.indexOf(" ");
        return { type: "submit", semantic: "builtin_command", command: space === -1 ? text.slice(1) : text.slice(1, space) };
    }
    return { type: "submit", semantic: streaming ? "steer" : "prompt" };
}
function nativePolicyActionName(action) {
    if (action.type === "submit")
        return \`\${action.semantic}:\${action.command ?? ""}\`;
    if (action.type === "shortcut")
        return \`shortcut:\${action.action}\`;
    if (action.type === "extension_shortcut")
        return \`extension_shortcut:\${action.shortcut}\`;
    return action.type;
}
function nativeFixtureActionAdmitted(action) {
    if (action.type === "submit")
        return action.semantic === "prompt" || action.semantic === "steer";
    if (action.type === "shortcut")
        return ["app.interrupt", "app.interrupt_bash", "editor.clear_bash", "app.tools.expand", "app.thinking.toggle", "app.message.followUp"].includes(action.action);
    return false;
}`;
  return replaceOnce(source, marker, insertion, "policy helper marker");
}

function patchPolicyMethods(source) {
  const marker = "    get settingsManager() {\n        return this.session.settingsManager;\n    }\n";
  const insertion = `${marker}    async authorizeNativeAction(action) {
        if (!nativeFixtureActionAdmitted(action)) {
            const policyObserver = this.options.actionPolicy;
            if (typeof policyObserver === "function") {
                try {
                    await policyObserver(action);
                }
                catch {
                    // Static denials remain authoritative; observer failures are ignored.
                }
            }
            this.showWarning(\`fixture action permanently refused: \${nativePolicyActionName(action)}\`);
            return false;
        }
        const policy = this.options.actionPolicy;
        if (typeof policy !== "function") {
            this.showWarning(\`native action refused without a policy: \${nativePolicyActionName(action)}\`);
            return false;
        }
        let decision;
        try {
            decision = await policy(action);
        }
        catch (error) {
            decision = { allow: false, reason: \`policy callback failed for \${nativePolicyActionName(action)}: \${error instanceof Error ? error.message : String(error)}\` };
        }
        if (decision !== null && typeof decision === "object" && decision.allow === true)
            return true;
        this.showWarning(decision?.reason ?? \`native action refused: \${nativePolicyActionName(action)}\`);
        return false;
    }
    async runNativeAction(action, effect) {
        if (!(await this.authorizeNativeAction(action)))
            return;
        return await effect();
    }
`;
  return replaceOnce(source, marker, insertion, "policy method marker");
}

function patchInitHandlers(source) {
  const marker = `        // Accept text while startup completes, but only enable interrupt, exit, and submission feedback.
        this.defaultEditor.onAction("app.clear", () => this.handleCtrlC());
        this.defaultEditor.onCtrlD = () => this.handleCtrlD();
        this.defaultEditor.onSubmit = (text) => this.handleStartupSubmit(text);`;
  const replacement = `        // Guard even the temporary handlers before the TUI starts accepting terminal bytes.
        this.defaultEditor.onAction("app.clear", () => {
            void this.runNativeAction({ type: "shortcut", action: "app.clear" }, () => this.handleCtrlC());
        });
        this.defaultEditor.onCtrlD = () => {
            void this.runNativeAction({ type: "shortcut", action: "app.exit" }, () => this.handleCtrlD());
        };
        this.defaultEditor.onSubmit = (text) => {
            void this.runNativeAction({ type: "submit", semantic: "startup", text }, () => this.handleStartupSubmit(text));
        };`;
  return replaceOnce(source, marker, replacement, "early init handler authorization marker");
}

function patchSubmit(source) {
  const marker = "            if (!text)\n                return;\n            // Handle commands";
  const replacement = "            if (!text)\n                return;\n            if (!(await this.authorizeNativeAction({ ...classifyNativeSubmit(text, this.session.isStreaming), text }))) {\n                this.editor.setText(\"\");\n                return;\n            }\n            // Handle commands";
  return replaceOnce(source, marker, replacement, "submit authorization marker");
}

function patchStartup(source) {
  const marker = "        await this.init();\n        if (!process.env.PI_OFFLINE) {";
  const replacement = `        await this.init();
        if (!(await this.authorizeNativeAction({ type: "startup", operations: ["model_catalog", "version_check", "package_update", "tmux_keyboard_check", "install_telemetry"] })))
            return;
        if (!process.env.PI_OFFLINE) {`;
  return replaceOnce(source, marker, replacement, "startup authorization marker");
}

function patchExtensionShortcut(source) {
  const marker = `                    // Run handler async, don't block input
                    Promise.resolve(shortcut.handler(createContext())).catch((err) => {`;
  const replacement = `                    // Run policy before extension code; the policy may deny the shortcut.
                    Promise.resolve(this.runNativeAction({ type: "extension_shortcut", shortcut: shortcutStr, extension: shortcut.extensionPath }, () => shortcut.handler(createContext()))).catch((err) => {`;
  return replaceOnce(source, marker, replacement, "extension shortcut authorization marker");
}

function patchKeyHandlers(source) {
  let patched = replaceOnce(source, `    onRightClickPaste = () => {
        void this.handleRightClickPaste();
    };`, `    onRightClickPaste = () => {
        void this.runNativeAction({ type: "shortcut", action: "app.clipboard.rightClickPaste" }, () => this.handleRightClickPaste());
    };`, "right-click paste authorization marker");
  patched = replaceOnce(patched, `                this.defaultEditor.onEscape = () => {
                    this.session.abortCompaction();
                };`, `                this.defaultEditor.onEscape = () => {
                    void this.runNativeAction({ type: "shortcut", action: "app.abort_compaction" }, () => this.session.abortCompaction());
                };`, "compaction escape authorization marker");
  patched = replaceOnce(patched, `                this.defaultEditor.onEscape = () => {
                    this.session.abortRetry();
                };`, `                this.defaultEditor.onEscape = () => {
                    void this.runNativeAction({ type: "shortcut", action: "app.abort_retry" }, () => this.session.abortRetry());
                };`, "retry escape authorization marker");
  patched = replaceOnce(patched, `                    this.defaultEditor.onEscape = () => {
                        this.session.abortBranchSummary();
                    };`, `                    this.defaultEditor.onEscape = () => {
                        void this.runNativeAction({ type: "shortcut", action: "app.abort_branch_summary" }, () => this.session.abortBranchSummary());
                    };`, "branch-summary escape authorization marker");
  const oldBlock = `        this.defaultEditor.onEscape = () => {
            if (this.session.isStreaming) {
                this.restoreQueuedMessagesToEditor({ abort: true });
            }
            else if (this.session.isBashRunning) {
                this.session.abortBash();
            }
            else if (this.isBashMode) {
                this.editor.setText("");
                this.isBashMode = false;
                this.updateEditorBorderColor();
            }
            else if (!this.editor.getText().trim()) {
                // Double-escape with empty editor triggers /tree, /fork, or nothing based on setting
                const action = this.settingsManager.getDoubleEscapeAction();
                if (action !== "none") {
                    const now = Date.now();
                    if (now - this.lastEscapeTime < 500) {
                        if (action === "tree") {
                            this.showTreeSelector();
                        }
                        else {
                            this.showUserMessageSelector();
                        }
                        this.lastEscapeTime = 0;
                    }
                    else {
                        this.lastEscapeTime = now;
                    }
                }
            }
        };
        // Register app action handlers
        this.defaultEditor.onAction("app.clear", () => this.handleCtrlC());
        this.defaultEditor.onCtrlD = () => this.handleCtrlD();
        this.defaultEditor.onAction("app.suspend", () => this.handleCtrlZ());
        this.defaultEditor.onAction("app.thinking.cycle", () => this.cycleThinkingLevel());
        this.defaultEditor.onAction("app.model.cycleForward", () => this.cycleModel("forward"));
        this.defaultEditor.onAction("app.model.cycleBackward", () => this.cycleModel("backward"));
        // Global debug handler on TUI (works regardless of focus)
        this.ui.onDebug = () => this.handleDebugCommand();
        this.defaultEditor.onAction("app.model.select", () => this.showModelSelector());
        this.defaultEditor.onAction("app.tools.expand", () => this.toggleToolOutputExpansion());
        this.defaultEditor.onAction("app.thinking.toggle", () => this.toggleThinkingBlockVisibility());
        this.defaultEditor.onAction("app.editor.external", () => void this.handleOpenExternalEditor());
        this.defaultEditor.onAction("app.message.copy", () => void this.handleCopyCommand({ flashConfirmation: true, preferSelection: true }));
        this.defaultEditor.onAction("app.message.followUp", () => this.handleFollowUp());
        this.defaultEditor.onAction("app.message.dequeue", () => this.handleDequeue());
        this.defaultEditor.onAction("app.session.new", () => this.handleClearCommand());
        this.defaultEditor.onAction("app.session.tree", () => this.showTreeSelector());
        this.defaultEditor.onAction("app.session.fork", () => this.showUserMessageSelector());
        this.defaultEditor.onAction("app.session.resume", () => this.showSessionSelector());
        this.defaultEditor.onChange = (text) => {`;
  const newBlock = `        this.defaultEditor.onEscape = () => {
            const body = () => {
                if (this.session.isStreaming) {
                    this.restoreQueuedMessagesToEditor({ abort: true });
                }
                else if (this.session.isBashRunning) {
                    this.session.abortBash();
                }
                else if (this.isBashMode) {
                    this.editor.setText("");
                    this.isBashMode = false;
                    this.updateEditorBorderColor();
                }
                else if (!this.editor.getText().trim()) {
                    const doubleEscapeAction = this.settingsManager.getDoubleEscapeAction();
                    if (doubleEscapeAction !== "none") {
                        const now = Date.now();
                        if (now - this.lastEscapeTime < 500) {
                            const action = doubleEscapeAction === "tree" ? "app.session.tree" : "app.session.fork";
                            void this.runNativeAction({ type: "shortcut", action }, () => {
                                if (doubleEscapeAction === "tree")
                                    this.showTreeSelector();
                                else
                                    this.showUserMessageSelector();
                            });
                            this.lastEscapeTime = 0;
                        }
                        else {
                            this.lastEscapeTime = now;
                        }
                    }
                }
            };
            const action = this.session.isStreaming ? "app.interrupt" : this.session.isBashRunning ? "app.interrupt_bash" : this.isBashMode ? "editor.clear_bash" : "app.interrupt";
            void this.runNativeAction({ type: "shortcut", action }, body);
        };
        // Register app action handlers through the structured policy boundary.
        const authorizeShortcut = (action, effect) => {
            void this.runNativeAction({ type: "shortcut", action }, effect);
        };
        this.defaultEditor.onAction("app.clear", () => authorizeShortcut("app.clear", () => this.handleCtrlC()));
        this.defaultEditor.onCtrlD = () => authorizeShortcut("app.exit", () => this.handleCtrlD());
        this.defaultEditor.onAction("app.suspend", () => authorizeShortcut("app.suspend", () => this.handleCtrlZ()));
        this.defaultEditor.onAction("app.thinking.cycle", () => authorizeShortcut("app.thinking.cycle", () => this.cycleThinkingLevel()));
        this.defaultEditor.onAction("app.model.cycleForward", () => authorizeShortcut("app.model.cycleForward", () => this.cycleModel("forward")));
        this.defaultEditor.onAction("app.model.cycleBackward", () => authorizeShortcut("app.model.cycleBackward", () => this.cycleModel("backward")));
        // Global debug handler on TUI (works regardless of focus)
        this.ui.onDebug = () => authorizeShortcut("ui.debug", () => this.handleDebugCommand());
        this.defaultEditor.onAction("app.model.select", () => authorizeShortcut("app.model.select", () => this.showModelSelector()));
        this.defaultEditor.onAction("app.tools.expand", () => authorizeShortcut("app.tools.expand", () => this.toggleToolOutputExpansion()));
        this.defaultEditor.onAction("app.thinking.toggle", () => authorizeShortcut("app.thinking.toggle", () => this.toggleThinkingBlockVisibility()));
        this.defaultEditor.onAction("app.editor.external", () => authorizeShortcut("app.editor.external", () => this.handleOpenExternalEditor()));
        this.defaultEditor.onAction("app.message.copy", () => authorizeShortcut("app.message.copy", () => this.handleCopyCommand({ flashConfirmation: true, preferSelection: true })));
        this.defaultEditor.onAction("app.message.followUp", () => authorizeShortcut("app.message.followUp", () => this.handleFollowUp()));
        this.defaultEditor.onAction("app.message.dequeue", () => authorizeShortcut("app.message.dequeue", () => this.handleDequeue()));
        this.defaultEditor.onAction("app.session.new", () => authorizeShortcut("app.session.new", () => this.handleClearCommand()));
        this.defaultEditor.onAction("app.session.tree", () => authorizeShortcut("app.session.tree", () => this.showTreeSelector()));
        this.defaultEditor.onAction("app.session.fork", () => authorizeShortcut("app.session.fork", () => this.showUserMessageSelector()));
        this.defaultEditor.onAction("app.session.resume", () => authorizeShortcut("app.session.resume", () => this.showSessionSelector()));
        this.defaultEditor.onChange = (text) => {`;
  const withPastePolicy = replaceOnce(patched, oldBlock, newBlock, "key handler block");
  const pasteMarker = `        this.defaultEditor.onPasteImage = () => {
            void this.handleClipboardPaste();
        };`;
  const pasteReplacement = `        this.defaultEditor.onPasteImage = () => {
            authorizeShortcut("app.clipboard.pasteImage", () => this.handleClipboardPaste());
        };`;
  return replaceOnce(withPastePolicy, pasteMarker, pasteReplacement, "clipboard paste authorization marker");
}

async function main() {
  const [sourceBytes, packageBytes] = await Promise.all([readFile(inputPath), readFile(packagePath)]);
  const sourceSha256 = sha256(sourceBytes);
  if (sourceSha256 !== pinnedInputSha256)
    throw new Error(`native Pi policy patch refused: input SHA mismatch ${sourceSha256}`);
  const packageJson = JSON.parse(packageBytes.toString("utf8"));
  if (packageJson.name !== "@earendil-works/pi-coding-agent" || packageJson.version !== pinnedVersion)
    throw new Error(`native Pi policy patch refused: package identity/version mismatch ${packageJson.name}@${packageJson.version}`);
  let source = sourceBytes.toString("utf8");
  for (const context of contextChecks) {
    const count = source.split(context).length - 1;
    if (count !== 1)
      throw new Error(`native Pi policy patch refused: context count ${count} for ${JSON.stringify(context)}`);
  }
  source = patchPolicyHelpers(source);
  source = patchPolicyMethods(source);
  source = patchInitHandlers(source);
  source = patchSubmit(source);
  source = patchStartup(source);
  source = patchExtensionShortcut(source);
  source = patchKeyHandlers(source);
  source = rewriteInternalImports(source);
  const outputBytes = Buffer.from(source, "utf8");
  const outputSha256 = sha256(outputBytes);
  await writeFile(outputPath, outputBytes);
  const manifest = {
    artifact: "experimental-private-pi-interactive-mode-policy",
    package: "@earendil-works/pi-coding-agent",
    version: pinnedVersion,
    input: {
      relativePath: relative(adapterDir, inputPath).replaceAll("\\", "/"),
      sha256: sourceSha256,
      bytes: sourceBytes.length,
      contextChecks,
    },
    output: {
      relativePath: relative(adapterDir, outputPath).replaceAll("\\", "/"),
      sha256: outputSha256,
      bytes: outputBytes.length,
    },
    patch: {
      source: "apply-patch.mjs",
      policyBoundary: "InteractiveMode submit, startup, app/extension shortcut, and escape dispatch",
      dependencies: "installed Pi 0.87.1 internal modules; no dependency copy or package edit",
      moduleResolution: "static imports resolve to absolute installed-package file URLs for exact-byte data-URL loading; output is checkout-specific",
    },
  };
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(JSON.stringify({ input: sourceSha256, inputBytes: sourceBytes.length, output: outputSha256, outputBytes: outputBytes.length }));
}

await main();
