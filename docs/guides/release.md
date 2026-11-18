# Releasing tbound

This is the maintainer runbook for producing and publishing tbound artifacts. A
release artifact does not qualify a governed run. On Linux, `tbound serve --pi`
is a client for the local systemd socket-activated service; service provisioning
and the live signed-profile/source path are separate from release packaging.
G1 has real-host engineering evidence at F12; each new deployment needs fresh
qualification. See [OVERVIEW](../../OVERVIEW.md) for the run boundary.

## Local build

```sh
scripts/build-release.sh 0.1.0 dist \
  linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
```

The packaging layout test is diagnostic by default: when an owner-source Linux
compile error is present, it reports the Linux checks as skipped so the other
platform/package checks remain useful. That mode is **not release acceptance**.
For a release decision, require Linux explicitly:

```sh
TBOUND_REQUIRE_LINUX_RELEASE=1 \
  bash tests/packaging/verify-release-layout.sh 0.1.0 --require-linux
```

In strict mode any Linux build failure, missing Linux artifact, or skipped
required check is a failure. CI runs this strict mode after its cross-platform
build and offline install check. Its regression test injects a fake Linux build
failure to ensure diagnostic mode may skip while strict mode must fail:

```sh
bash tests/packaging/verify-release-strict-mode.sh
```

Artifacts are staged and checksummed in a private work dir and the destination is
updated only after every target builds, so a failed target never leaves partial
tarballs or a stale `SHA256SUMS`. The final destination update is not a
transactional swap: if the process is interrupted while updating `dist/`, rerun the
build.

Each tarball contains:

| Path | Every target | linux/amd64 only |
|---|---|---|
| `bin/tbound` | yes | |
| `bin/tbound-doctor` | yes | |
| `bin/tbound-profile` | yes (Linux-only stub elsewhere) | |
| `LICENSE` | yes | |
| `libexec/tbound/tbound-cell-entrypoint` | | yes |
| `libexec/tbound/cell-entrypoint.json` | | yes |
| `runtime/`, `share/completions/`, `VERSION` | yes | |
| `MANIFEST.sha256` | yes | |

`MANIFEST.sha256` is a per-file integrity manifest: it lists the sha256 of every
shipped regular file except the manifest itself, using paths relative to the
extraction root, so `sha256sum -c MANIFEST.sha256` works after extraction. The
outer `SHA256SUMS` digest covers the complete tarball (including this manifest).
Neither file is a signature or publisher-authentication mechanism; a separately
authorized signature/trust workflow remains required for governed artifacts.

`bin/tbound-profile` generates/signs/verifies the Linux host profile; it is built
for every target because the non-Linux build is a clear stub. The
`tbound-cell-entrypoint` is an **unsigned binary** intended for a signed, frozen
in-image D09 TCB entrypoint. It is **linux/amd64 only** — its Landlock syscall
numbers and the runtime's `linuxSysRenameat2` helper are amd64-specific — and is
**not** signed by the build script (no automatic keys) and **not** shipped
through npm. The build also writes
a separate metadata + checksum release artifact:

```text
dist/tbound-cell-entrypoint-<ver>-linux-amd64.metadata.json   # provenance/license/sha256/signing note
dist/tbound-cell-entrypoint-<ver>-linux-amd64.sha256
```

The metadata records the entrypoint binary digest, source commit/dirty state,
source-tree and D09-template-source digests, Go toolchain/build flags, the
canonical `LICENSE` source, its inclusion status and SHA-256 digest, and an
explicit `registered_d09_profile_digest: null`; the build cannot invent a
measured/signed profile hash. `SHA256SUMS` covers each tarball
and both separate metadata/checksum artifacts. The `.sha256` file names the
binary as stored inside the tarball and can be checked from the extraction root
with `sha256sum -c <path-to-release-checksum-file>`. Every release tarball
includes the full MIT license text at its root, covered by `MANIFEST.sha256`.

Attach these alongside the tarballs when publishing; the release workflow
uploads the tarballs, `SHA256SUMS`, and both exact Linux/amd64 sidecar filename
globs as workflow artifacts and GitHub release assets.

> `linux/arm64` is intentionally not built or advertised: the runtime uses a
> `linuxSysRenameat2` helper implemented only for amd64, and the entrypoint is
> amd64-only. Add arm64 support before advertising it.

## npm packages

```sh
scripts/make-npm-packages.sh 0.1.0 dist
# -> dist/npm/cli (root package) + dist/npm/cli-linux-x64 (platform package)
```

npm packages are generated for Linux amd64 only; non-Linux release tarballs are
skipped. The Linux package ships `bin/tbound`, `bin/tbound-doctor`, and
`bin/tbound-profile`. The root launcher forwards `tbound doctor` and `tbound
profile` to the dedicated binaries; the TCB entrypoint is never packaged for
npm. The root and platform package trees include the
canonical `LICENSE` file and list it in their npm `files` allowlists.

Publishing is manual/opt-in:

```sh
for d in dist/npm/cli-*/; do (cd "$d" && npm publish --access public); done
(cd dist/npm/cli && npm publish --access public)
```

Only do this after confirming namespace ownership and approving publication; no
`@tbound` packages are published by the build or currently claimed available.

## Publish a GitHub release

```sh
scripts/publish-release.sh 0.1.0 --dry-run   # preview
scripts/publish-release.sh 0.1.0             # create the release via gh
```

## CI

`.github/workflows/release.yml` builds the artifact matrix, generates the npm
packages, runs the offline install verification, uploads the tarballs,
`SHA256SUMS`, and the exact entrypoint metadata/checksum sidecar globs, then
attaches these release assets on a `v*` tag. npm publishing is left manual on
purpose.

## Package managers

- Debian: `scripts/build-deb.sh <numeric-version> dist` (version must start with a digit).
- RPM: `scripts/build-rpm.sh <version> dist` (x86_64 only today).
- Homebrew: fill in `packaging/homebrew/tbound.rb` after a release.
- Container: `scripts/build-container.sh <tag>` (podman preferred).

These package layouts carry `tbound`, `tbound-doctor`, `tbound-profile`, the
runtime bundle, and (linux/amd64) the unsigned TCB entrypoint binary under a
non-PATH libexec directory. Package installation alone does not establish
signature, root-owned immutability, or governed trust.
