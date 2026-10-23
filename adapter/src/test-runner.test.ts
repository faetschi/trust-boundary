import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

test("test runner pins the spec reporter when stdout is piped for evidence", () => {
  const root = mkdtempSync(join(tmpdir(), "tbound-test-reporter-"));
  try {
    mkdirSync(join(root, "src"));
    writeFileSync(join(root, "src", "reporter.test.ts"), 'import { test } from "node:test";\ntest("sample", () => {});\n');
    const env: NodeJS.ProcessEnv = { ...process.env, NO_COLOR: "1" };
    delete env.NODE_TEST_CONTEXT;
    const result = spawnSync(process.execPath, [fileURLToPath(new URL("../scripts/test.mjs", import.meta.url))], {
      cwd: root,
      encoding: "utf8",
      env,
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /^ℹ tests 1$/m);
    assert.match(result.stdout, /^ℹ pass 1$/m);
    assert.doesNotMatch(result.stdout, /^TAP version|^# tests/m);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
