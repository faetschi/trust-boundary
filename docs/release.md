# Releasing tbound

This is the maintainer runbook for producing and publishing tbound artifacts. It
does **not** enable governed `serve --pi`; that still requires the signed profile
and containment described in [`governed-setup.md`](governed-setup.md).

## Local build

```sh
scripts/build-release.sh 0.1.0 dist \
  linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
```

Artifacts are staged and checksummed in a private work dir and the destination is
updated only after every target builds, so a failed target never leaves partial
tarballs or a stale `SHA256SUMS`. The final destination update is not a
transactional swap: if the process is interrupted while updating `dist/`, rerun the
build. Each tarball contains `bin/tbound`, `bin/tbound-doctor`, `runtime/`,
`share/completions/`, and `VERSION`.

> `linux/arm64` is intentionally not built yet: the runtime uses a
> `linuxSysRenameat2` helper implemented only for amd64. Add arm64 support before
> advertising it.

## npm packages

```sh
scripts/make-npm-packages.sh 0.1.0 dist
# -> dist/npm/cli (root package) + dist/npm/cli-<os>-<arch> (platform packages)
```

Publishing is manual/opt-in:

```sh
for d in dist/npm/*/; do (cd "$d" && npm publish --access public); done
```

## Publish a GitHub release

```sh
scripts/publish-release.sh 0.1.0 --dry-run   # preview
scripts/publish-release.sh 0.1.0             # create the release via gh
```

## CI

`.github/workflows/release.yml` builds the artifact matrix, generates the npm
packages, runs the offline install verification, uploads artifacts, and attaches
the tarballs + `SHA256SUMS` to a GitHub release on a `v*` tag. npm publishing is
left manual on purpose.

## Package managers

- Debian: `scripts/build-deb.sh <numeric-version> dist` (version must start with a digit).
- RPM: `scripts/build-rpm.sh <version> dist` (x86_64 only today).
- Homebrew: fill in `packaging/homebrew/tbound.rb` after a release.
- Container: `scripts/build-container.sh <tag>` (podman preferred).

## Honesty boundary

Every artifact is the **dev/install surface**. Governed `tbound serve --pi`
remains refused. No packaging artifact establishes containment, G1, or H1.
