#!/usr/bin/env node
"use strict";

// Exercise the actual checked-in launcher without npm install/network by running
// it in a VM with local fake optional-dependency metadata and a recording
// spawnSync. The test explicitly models the win32 path module, so .exe selection
// is tested on any host OS.
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const launcherPath = path.resolve(__dirname, "../../packaging/npm/bin/tbound.js");
const source = fs.readFileSync(launcherPath, "utf8");
const hostPath = require("node:path");

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
  const platformPath = platform === "win32" ? hostPath.win32 : hostPath.posix;
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
    if (id === "node:path") return platformPath;
    throw new Error(`unexpected require ${id}`);
  };
  fakeRequire.resolve = (id) => {
    const match = /^@tbound\/cli-([^/]+)\/package\.json$/.exec(id);
    if (!match || !installed) throw new Error("optional package not installed");
    const name = match[1];
    return platform === "win32"
      ? `C:\\fake\\node_modules\\@tbound\\cli-${name}\\package.json`
      : `/fake/node_modules/@tbound/cli-${name}/package.json`;
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
  return { calls, errors, exitCode, platformPath };
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
assertDispatch(
  "Windows profile chooses the .exe binary using Windows path semantics",
  { platform: "win32", arch: "x64", args: ["profile", "--help"] },
  "C:\\fake\\node_modules\\@tbound\\cli-win32-x64\\bin\\tbound-profile.exe",
  ["--help"],
);
assertDispatch(
  "Windows doctor chooses the .exe binary using Windows path semantics",
  { platform: "win32", arch: "x64", args: ["doctor", "--json"] },
  "C:\\fake\\node_modules\\@tbound\\cli-win32-x64\\bin\\tbound-doctor.exe",
  ["--json"],
);

const unsupported = runCase({ platform: "linux", arch: "arm64", args: ["doctor"] });
if (unsupported.exitCode === 1 && unsupported.calls.length === 0 &&
    unsupported.errors.some((message) => message.includes("does not ship a binary for linux-arm64"))) {
  ok("unsupported Linux arm64 fails clearly without spawning");
} else {
  bad("unsupported Linux arm64 fails clearly without spawning", JSON.stringify(unsupported));
}

const absent = runCase({ platform: "darwin", arch: "x64", args: ["profile"], installed: false });
if (absent.exitCode === 1 && absent.calls.length === 0 &&
    absent.errors.some((message) => message.includes("platform package @tbound/cli-darwin-x64 is not installed"))) {
  ok("missing platform optional dependency fails clearly");
} else {
  bad("missing platform optional dependency fails clearly", JSON.stringify(absent));
}

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
