#!/usr/bin/env bash
# Offline regression for the manual release publisher: every checksum-covered
# asset, including the linux/amd64 TCB sidecars, is uploaded, and a linux release
# without both sidecars, or a sidecar without checksum coverage, is refused.
# Synthetic files only; --dry-run never invokes gh or the network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-publish-release-assets: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "$TMPDIR/tbound-publish-assets.XXXXXX")"
trap 'rm -rf "$BASE"' EXIT
VERSION=1.2.3
TARBALL_LINUX="tbound-$VERSION-linux-amd64.tar.gz"
METADATA="tbound-cell-entrypoint-$VERSION-linux-amd64.metadata.json"
CHECKSUM="tbound-cell-entrypoint-$VERSION-linux-amd64.sha256"

die() { echo "verify-publish-release-assets: FAIL: $*" >&2; exit 1; }
pass() { printf '  PASS  %s\n' "$*"; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
# fixture DIR NAME...: synthetic files, each listed in DIR/SHA256SUMS.
fixture() {
  local dir="$1" name
  shift
  mkdir -m 700 "$dir"
  for name in "$@"; do
    printf 'synthetic publisher fixture\n' > "$dir/$name"
    printf '%s  %s\n' "$(sha256_of "$dir/$name")" "$name" >> "$dir/SHA256SUMS"
  done
}
dry_run() {
  TBOUND_DIST="$DIST" bash "$ROOT/scripts/publish-release.sh" "$VERSION" --dry-run
}

DIST="$BASE/complete"
fixture "$DIST" "$TARBALL_LINUX" "$METADATA" "$CHECKSUM"
dry_run > "$BASE/complete.log" 2>&1 || die "complete asset set was rejected"
for asset in "$TARBALL_LINUX" "$METADATA" "$CHECKSUM" SHA256SUMS; do
  grep -Fq "$DIST/$asset" "$BASE/complete.log" || die "dry-run upload arguments omit $asset"
done
pass "dry-run uploads the linux tarball, both TCB sidecars, and SHA256SUMS"

for asset in "$METADATA" "$CHECKSUM"; do
  mv "$DIST/$asset" "$BASE/moved"
  if dry_run > "$BASE/missing.log" 2>&1; then die "missing checksum-listed asset was accepted: $asset"; fi
  grep -Fq "linux release is missing $asset" "$BASE/missing.log" || die "missing $asset failed for an unexpected reason"
  mv "$BASE/moved" "$DIST/$asset"
done
pass "a missing checksum-listed TCB sidecar fails before publication"

DIST="$BASE/unlisted"
fixture "$DIST" "$TARBALL_LINUX" "$CHECKSUM"
printf 'unlisted fixture\n' > "$DIST/$METADATA"
if dry_run > "$BASE/unlisted.log" 2>&1; then die "sidecar without checksum coverage was accepted"; fi
grep -Fq "SHA256SUMS is missing an entry for $METADATA" "$BASE/unlisted.log" || die "unlisted sidecar failed for an unexpected reason"
pass "a sidecar without checksum coverage is refused before publication"

DIST="$BASE/linux-without-sidecars"
fixture "$DIST" "$TARBALL_LINUX"
if dry_run > "$BASE/linux-without-sidecars.log" 2>&1; then die "linux release without its TCB sidecars was accepted"; fi
grep -Fq "linux release is missing $METADATA" "$BASE/linux-without-sidecars.log" || die "linux release without sidecars failed for an unexpected reason"
pass "a linux release without its TCB sidecars is refused before publication"

DIST="$BASE/non-linux-only"
fixture "$DIST" "tbound-$VERSION-windows-amd64.tar.gz"
if dry_run > "$BASE/non-linux-only.log" 2>&1; then die "a release without the linux/amd64 tarball was accepted"; fi
grep -Fq "no linux/amd64 tarball for version $VERSION" "$BASE/non-linux-only.log" || die "non-linux release failed for an unexpected reason"
pass "a release without the linux/amd64 tarball is refused before publication"

DIST="$BASE/stray-non-linux"
fixture "$DIST" "$TARBALL_LINUX" "$METADATA" "$CHECKSUM" "tbound-$VERSION-darwin-arm64.tar.gz"
dry_run > "$BASE/stray.log" 2>&1 || die "linux release with a stray non-linux tarball was rejected"
if grep -Fq "darwin-arm64" "$BASE/stray.log"; then die "a stray non-linux tarball would be uploaded"; fi
pass "a stray non-linux tarball in dist is not uploaded"

echo "verify-publish-release-assets: 6 passed, 0 failed"
