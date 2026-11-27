// V-shaped client for the f35 probe: the pinned pi-ai 0.87.1 builds every request body, as it does inside V.
// The model entry is what vrun/args.go writes to models.json (id only); the other fields are the
// pi-coding-agent 0.87.1 defaults for such an entry (provider-composer.js:87-94).
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { complete } from "/home/tbound/tbound-work/x30/adapter/node_modules/@earendil-works/pi-ai/dist/compat.js";

const [baseUrl, out] = process.argv.slice(2);
const model = {
  id: "deepseek/deepseek-v4.1-flash", name: "deepseek/deepseek-v4.1-flash", api: "openai-completions",
  provider: "research", baseUrl, reasoning: false, input: ["text"],
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128000, maxTokens: 16384,
};
const tools = [
  { name: "read", description: "Read a file and return its text.",
    parameters: { type: "object", properties: { path: { type: "string", description: "File path" } }, required: ["path"] } },
  { name: "bash", description: "Run a shell command.",
    parameters: { type: "object", properties: { command: { type: "string", description: "Command" } }, required: ["command"] } },
];
const context = {
  systemPrompt: "You are a coding agent working in a small repository. Use the tools to inspect files.",
  messages: [{ role: "user", content: "First read README.md with the read tool. Then run wc -w README.md with the bash tool. Then reason about whether the README has a spelling mistake and tell me in one sentence.", timestamp: Date.now() }],
  tools,
};
const files = { "README.md": "# probe\n\nThis tool will recieve input from the user.\n" };
const options = { apiKey: process.env.TBOUND_V_KEY };
for (let turn = 1; turn <= 4; turn++) {
  const reply = await complete(model, context, options);
  writeFileSync(join(out, `v-turn-${turn}.json`), JSON.stringify({
    stopReason: reply.stopReason, errorMessage: reply.errorMessage, usage: reply.usage,
    content: reply.content.map((b) => ({ type: b.type, chars: (b.text ?? b.thinking ?? "").length,
      signature: b.thinkingSignature ? (b.thinkingSignature.startsWith("[") ? "reasoning_details-json" : b.thinkingSignature) : undefined,
      name: b.name, arguments: b.arguments })),
  }, null, 1), { mode: 0o600 });
  console.log(`v turn ${turn}: stop=${reply.stopReason} err=${reply.errorMessage ?? ""}`);
  if (reply.stopReason !== "toolUse") break;
  context.messages.push(reply);
  for (const call of reply.content.filter((b) => b.type === "toolCall")) {
    const text = call.name === "read" ? (files[call.arguments.path] ?? "no such file") : "exit 0";
    context.messages.push({ role: "toolResult", toolCallId: call.id, toolName: call.name,
      content: [{ type: "text", text }], isError: false, timestamp: Date.now() });
  }
}
