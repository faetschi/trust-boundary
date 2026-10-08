#!/usr/bin/env node
// Thin launcher for the tbound Go binary installed via a platform-specific
// optional dependency (the esbuild/rollup pattern). It performs no work beyond
// locating and exec'ing the correct binary, so `npm i -g @tbound/cli` gives a
// `tbound` command right next to a globally installed Pi.
"use strict";

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const PLATFORM_PACKAGES = {
  "linux-x64": "@tbound/cli-linux-x64",
  "linux-arm64": "@tbound/cli-linux-arm64",
  "darwin-x64": "@tbound/cli-darwin-x64",
  "darwin-arm64": "@tbound/cli-darwin-arm64",
  "win32-x64": "@tbound/cli-win32-x64",
};

function binaryName() {
  return process.platform === "win32" ? "tbound.exe" : "tbound";
}

function resolveBinary() {
  const key = `${process.platform}-${process.arch}`;
  const pkg = PLATFORM_PACKAGES[key];
  if (!pkg) {
    throw new Error(`tbound does not ship a binary for ${key}`);
  }
  let pkgDir;
  try {
    pkgDir = path.dirname(require.resolve(`${pkg}/package.json`));
  } catch {
    throw new Error(
      `the platform package ${pkg} is not installed. ` +
        `Reinstall without --no-optional, or build from source (see docs/install.md).`
    );
  }
  return path.join(pkgDir, "bin", binaryName());
}

function main() {
  let binary;
  try {
    binary = resolveBinary();
  } catch (err) {
    console.error(`tbound: ${err.message}`);
    process.exit(1);
  }
  const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
  if (result.error) {
    console.error(`tbound: failed to launch ${binary}: ${result.error.message}`);
    process.exit(1);
  }
  process.exit(result.status ?? 1);
}

main();
