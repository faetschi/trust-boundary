#!/usr/bin/env node
// Postinstall is intentionally minimal: it only reports what was installed and
// points at tbound-doctor. It never downloads binaries itself (they arrive via
// the platform optional dependencies) and never runs sudo.
"use strict";

const os = require("node:os");

const key = `${process.platform}-${process.arch}`;
console.log(`tbound: installed launcher for ${key} (node ${process.version}).`);
console.log("tbound: run `tbound doctor` (or `tbound-doctor` after a tarball install) to check that Node and Pi are ready.");
console.log(
  "tbound: governed `tbound serve --pi` additionally needs a signed host profile and " +
    "the Podman/crun containment stack on Linux."
);
if (os.platform() === "win32") {
  console.log("tbound: governed runs are Linux-only; use the declared Linux evaluation host.");
}
