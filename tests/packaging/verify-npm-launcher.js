#!/usr/bin/env node
"use strict";

// Exercise the actual checked-in launcher without npm install/network by running
// it in a VM with local fake optional-dependency metadata and a recording
// spawnSync. The launcher ships the Linux amd64 binary only.
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const launcherPath = path.resolve(__dirname, "../../packaging/npm/bin/tbound.js");
const source = fs.readFileSync(launcherPath, "utf8");

let passed = 0;
let failed = 0;
function ok(label) {
  console.log(`  PASS  ${label}`);
  passed += 1;
}
function bad(label, detail) {
  console.error(`  FAIL  ${label}${detail ? `: ${detail}` : ""}`);
  failed += 1;
}

function runCase({ platform, arch, args, installed = true, spawnError = null }) {
  const calls = [];
  const errors = [];
  let exitCode = null;
  const fakeProcess = {
    platform,
    arch,
    argv: ["node", launcherPath, ...args],
    exit(code) {
      exitCode = code;
      throw new Error("TEST_PROCESS_EXIT");
    },
  };
  const fakeRequire = (id) => {
    if (id === "node:child_process") {
      return {
        spawnSync(binary, forwarded, options) {
          calls.push({ binary, forwarded, options });
          return { status: 23, error: spawnError };
        },
      };
    }
    if (id === "node:path") return path.posix;
    throw new Error(`unexpected require ${id}`);
  };
  fakeRequire.resolve = (id) => {
    const match = /^@tbound\/cli-([^/]+)\/package\.json$/.exec(id);
    if (!match || !installed) throw new Error("optional package not installed");
    return `/fake/node_modules/@tbound/cli-${match[1]}/package.json`;
  };
  const sandbox = {
    require: fakeRequire,
    process: fakeProcess,
    console: { error: (text) => errors.push(String(text)) },
  };
  try {
    vm.runInNewContext(source, sandbox, { filename: launcherPath });
  } catch (error) {
    if (error.message !== "TEST_PROCESS_EXIT") throw error;
  }
  return { calls, errors, exitCode };
}

function assertDispatch(label, opts, expectedBinary, expectedArgs) {
  const result = runCase(opts);
  const call = result.calls[0];
  if (result.calls.length !== 1 || !call || call.binary !== expectedBinary ||
      JSON.stringify(call.forwarded) !== JSON.stringify(expectedArgs) || result.exitCode !== 23 ||
      call.options.stdio !== "inherit") {
    bad(label, JSON.stringify({ calls: result.calls, exitCode: result.exitCode, errors: result.errors }));
    return;
  }
  ok(label);
}

function assertRefused(label, opts, expectedMessage) {
  const result = runCase(opts);
  if (result.exitCode === 1 && result.calls.length === 0 &&
      result.errors.some((message) => message.includes(expectedMessage))) {
    ok(label);
    return;
  }
  bad(label, JSON.stringify(result));
}

console.log("verify-npm-launcher: actual wrapper in a local VM harness");
assertDispatch(
  "Linux doctor chooses the local doctor binary and strips the dispatch token",
  { platform: "linux", arch: "x64", args: ["doctor", "--json"] },
  "/fake/node_modules/@tbound/cli-linux-x64/bin/tbound-doctor",
  ["--json"],
);
assertDispatch(
  "Linux profile chooses the local profile binary and strips the dispatch token",
  { platform: "linux", arch: "x64", args: ["profile", "verify", "--profile", "p.json"] },
  "/fake/node_modules/@tbound/cli-linux-x64/bin/tbound-profile",
  ["verify", "--profile", "p.json"],
);
assertDispatch(
  "Linux runtime invocation stays on tbound and keeps all arguments",
  { platform: "linux", arch: "x64", args: ["serve", "--pi"] },
  "/fake/node_modules/@tbound/cli-linux-x64/bin/tbound",
  ["serve", "--pi"],
);

assertRefused(
  "unsupported Linux arm64 fails clearly without spawning",
  { platform: "linux", arch: "arm64", args: ["doctor"] },
  "does not ship a binary for linux-arm64",
);
assertRefused(
  "darwin is refused without spawning",
  { platform: "darwin", arch: "arm64", args: ["profile"] },
  "does not ship a binary for darwin-arm64",
);
assertRefused(
  "win32 is refused without spawning",
  { platform: "win32", arch: "x64", args: ["doctor"] },
  "does not ship a binary for win32-x64",
);
assertRefused(
  "missing Linux platform package fails clearly",
  { platform: "linux", arch: "x64", args: ["profile"], installed: false },
  "platform package @tbound/cli-linux-x64 is not installed",
);

const spawnFailure = runCase({
  platform: "linux", arch: "x64", args: ["profile"], spawnError: new Error("ENOENT"),
});
if (spawnFailure.exitCode === 1 && spawnFailure.errors.some((message) => message.includes("failed to launch"))) {
  ok("failed local binary launch reports failure");
} else {
  bad("failed local binary launch reports failure", JSON.stringify(spawnFailure));
}

console.log(`verify-npm-launcher: ${passed} passed, ${failed} failed`);
process.exitCode = failed === 0 ? 0 : 1;
