# Packaging and release

TBound ships as a single static Go binary. Release artefacts are produced by
`packaging/build-packages.sh`:

- `dist/tbound-<version>-linux-amd64.tar.gz` — binary + systemd units +
  install/uninstall scripts + docs.
- `dist/*.deb` and `dist/*.rpm` — built with `nfpm` when available.
- `dist/SHA256SUMS` — checksums for all artefacts.

## Install paths

| Path | Mode | Purpose |
|---|---|---|
| `/usr/local/bin/tbound` | 0755 | supervisor CLI |
| `/etc/tbound/` | 0750 | host profile + broker config (operator-owned) |
| `/opt/tbound/` | 0750 | managed, pinned runtime bundle (Node/Pi/deps) |
| `~/.config/systemd/user/tbound.service` | 0644 | rootless user unit (`Delegate=yes`) |

## Trust and signature boundary

- Packages are **not** signed by this tooling; an operator/release process signs
  them. Do not distribute unsigned packages for governed use.
- The runtime bundle is verified per-artefact by SHA-256 from an operator-supplied
  manifest; placeholder hashes are rejected.
- The cell image is verified by digest + cosign public key (see cell-image.md).
- Credentials never live in the repo, the package, or Pi.

## Current limitation

Packaging makes TBound installable and verifiable, but governed
`tbound serve --pi` still refuses until the signed host profile and signed cell
image are provisioned. The development fixtures remain explicitly
non-claim-bearing. See `docs/tbound-readiness-status-2026-10-08.md`.
