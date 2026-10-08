# Installing TBound on Linux

This guide covers the user-facing install path for TBound on a Linux host. It is
honest about scope: today you can provision TBound, verify the host, and run the
development fixtures; a fully governed `tbound serve --pi` session additionally
requires an operator-provisioned signed host profile and signed cell image.

## What `tbound install` does

`tbound install` is fail-closed and does the following, in order:

1. Runs the same host preflight as `tbound doctor` and stops if any check fails.
2. Verifies the admitted host profile (`/etc/tbound/pi-host-profile.json` +
   `.ed25519` signature + `.pub`), root-owned and not group/world writable.
3. Installs the pinned managed runtime bundle (Node + Pi + locked deps) into
   `/opt/tbound` from a manifest you supply, verifying each artefact's SHA-256.
   It never trusts a global `npm`/Pi install.
4. Writes an owner-only (`0600`) broker configuration template. It never stores a
   credential value; the provider key stays out-of-band.
5. Prints the systemd **user** unit that delegates a cgroup v2 subtree
   (`Delegate=yes`) for rootless isolation.

## Commands

```sh
tbound doctor                 # preflight; exit non-zero if the host is not ready
tbound doctor --json          # machine-readable report
tbound install --dry-run      # show the plan without changing the host
tbound install --manifest ./runtime-bundle.json
tbound uninstall --dry-run    # show exactly what would be removed
tbound uninstall
```

## Host requirements (preflight)

- Ubuntu 24.04 (declared profile), recent kernel.
- Landlock ABI >= 3, seccomp filter, `pidfd_open`.
- Unprivileged user namespaces enabled.
- cgroup v2 unified mount with a **delegated, writable** subtree and `cgroup.kill`.
- Rootless Podman, `crun`, and `cosign` present.
- A subordinate UID/GID range (subuid/subgid) for the trial user.

Any missing capability fails closed. No fallback to Docker, WSL, or a different
runtime is performed.

## Operator-gated steps (not automated)

These require explicit operator authorization and cannot be satisfied by a
boolean or a test fixture:

1. Provision `/etc/tbound/pi-host-profile.json` (+ signature + public key),
   root-owned.
2. Build, digest-pin, and cosign-sign the cell image offline; publish the public
   key. See [cell-image.md](cell-image.md).
3. Grant the fixed, non-threaded cgroup v2 delegation used by the user unit.
4. Provide the provider credential out-of-band (never in this repo or Pi).

## Running the runtime bundle manifest

`--manifest` points at a `tbound-runtime-bundle/v1` JSON file. Every component
must have a real 64-hex `sha256` and an `https://` URL; placeholder values are
rejected so a template cannot be installed as if it were provisioned. Add
`"extract": true` for `.tar.gz` artifacts (e.g. the pinned Node toolchain) so
TBound verifies the archive and unpacks it safely into the target; archives with
absolute paths, `..`, or symlink/hardlink entries are refused. Example:

```json
{
  "schema_version": "tbound-runtime-bundle/v1",
  "components": [
    {"name":"node","version":"24.21.0","url":"https://nodejs.org/dist/v24.21.0/node-v24.21.0-linux-x64.tar.gz","sha256":"<64 hex>","target":"toolchains/node","extract":true}
  ]
}
```

Pi and its pinned dependencies (`@earendil-works/*@0.87.1`, `typebox@1.3.27`) are
materialized from the adapter lockfile against the installed Node; that step
reuses the repository's pinned `npm ci --ignore-scripts` workflow and is not
performed by this installer.

## What still refuses

`tbound serve --pi` remains refused until the admitted host profile and signed
cell image exist and the production launcher is enabled. The development
fixtures (`serve --pi --native-fixture`) remain available and are explicitly
non-claim-bearing. See `docs/tbound-readiness-status-2026-10-08.md`.
