import { spawnSync } from "node:child_process";
import { readdirSync } from "node:fs";

// The embedded SDK worker and optional generated native patch are retired.
// Keep all remaining adapter tests discoverable, including the official CLI
// RPC integration, so new portable coverage cannot be silently omitted.
const files = readdirSync("src").filter((name) => name.endsWith(".test.ts")).sort();
const suites = files.map((name) => `src/${name}`);

if (suites.length === 0) {
  throw new Error("No adapter test suites were discovered in src/");
}

const result = spawnSync(
  process.execPath,
  ["--experimental-strip-types", "--test", ...suites],
  { stdio: "inherit", shell: false },
);

if (result.error) throw result.error;
if (result.signal) process.exit(1);
process.exit(result.status ?? 1);
