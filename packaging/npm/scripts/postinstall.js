#!/usr/bin/env node
// Postinstall is intentionally minimal: it only reports what was installed and
// points at tbound-doctor. It never downloads binaries itself (they arrive via
// the platform optional dependencies) and never runs sudo.
"use strict";

const key = `${process.platform}-${process.arch}`;
console.log(`tbound: installed launcher for ${key} (node ${process.version}).`);
console.log("tbound: run `tbound doctor` (or `tbound-doctor` after a tarball install) to check that Node and Pi are ready.");
console.log(
  "tbound: governed `tbound serve --pi` additionally needs a signed host profile and " +
    "the Podman/crun containment stack on Linux."
);
if (key !== "linux-x64") {
  console.log("tbound: this package ships a Linux amd64 binary only; use a Linux VM or WSL on this platform.");
}
