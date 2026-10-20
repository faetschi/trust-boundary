#!/usr/bin/env bash
# Offline regression for manual release asset selection and checksum coverage.
# Synthetic files are never executed; --dry-run never invokes gh or the network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [ -z "${TMPDIR:-}" ] || [ ! -d "$TMPDIR" ] || [ "$(stat -c '%a' "$TMPDIR" 2>/dev/null || true)" != 700 ]; then
  echo "verify-publish-release-assets: set TMPDIR to an existing private mode-0700 directory" >&2
  exit 2
fi
BASE="$(mktemp -d "$TMPDIR/tbound-publish-assets.XXXXXX")"
trap 'rm -rf "$BASE"' EXIT
VERSION=1.2.3
METADATA="tbound-cell-entrypoint-$VERSION-linux-amd64.metadata.json"
CHECKSUM="tbound-cell-entrypoint-$VERSION-linux-amd64.sha256"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
fixture() {
  local dir="$1"
  mkdir -m 700 "$dir"
  shift
  local name
  for name in "$@"; do
    printf 'synthetic publisher fixture\n' > "$dir/$name"
    printf '%s  %s\n' "$(sha256_of "$dir/$name")" "$name" >> "$dir/SHA256SUMS"
  done
}
dry_run() {
  TBOUND_DIST="$DIST" bash "$ROOT/scripts/publish-release.sh" "$VERSION" --dry-run
}
die() { echo "verify-publish-release-assets: FAIL: $*" >&2; exit 1; }
pass() { printf '  PASS  %s\n' "$*"; }

DIST="$BASE/complete"
assets=("tbound-$VERSION-linux-amd64.tar.gz" "tbound-$VERSION-windows-amd64.tar.gz" "$METADATA" "$CHECKSUM")
fixture "$DIST" "${assets[@]}"
dry_run > "$BASE/complete.log" 2>&1 || die "complete asset set was rejected"
for asset in "${assets[@]}" SHA256SUMS; do
  grep -Fq "$DIST/$asset" "$BASE/complete.log" || die "dry-run upload arguments omit $asset"
done
pass "dry-run uploads every tarball, both TCB sidecars, and SHA256SUMS"

for asset in "$METADATA" "$CHECKSUM"; do
  mv "$DIST/$asset" "$BASE/missing-asset"
  if dry_run > "$BASE/missing.log" 2>&1; then die "missing listed asset was accepted: $asset"; fi
  grep -Fq 'SHA256SUMS does not verify' "$BASE/missing.log" || die "missing asset failed for an unexpected reason"
  if grep -Fq 'gh release create' "$BASE/missing.log"; then die "missing asset reached publication"; fi
  mv "$BASE/missing-asset" "$DIST/$asset"
done
pass "either missing checksum-listed TCB sidecar fails before publication"

DIST="$BASE/unlisted"
fixture "$DIST" "tbound-$VERSION-linux-amd64.tar.gz"
printf 'unlisted fixture\n' > "$DIST/$METADATA"
if dry_run > "$BASE/unlisted.log" 2>&1; then die "sidecar lacking checksum coverage was accepted"; fi
grep -Fq "SHA256SUMS is missing an entry for $METADATA" "$BASE/unlisted.log" || die "unlisted sidecar failed for an unexpected reason"
pass "a sidecar without checksum coverage fails before publication"

DIST="$BASE/nonlinux"
fixture "$DIST" "tbound-$VERSION-windows-amd64.tar.gz"
dry_run > "$BASE/nonlinux.log" 2>&1 || die "non-Linux release was rejected"
if grep -Fq 'tbound-cell-entrypoint-' "$BASE/nonlinux.log"; then die "non-Linux release uploads nonexistent sidecars"; fi
pass "non-Linux releases need no TCB sidecars"

echo "verify-publish-release-assets: 4 passed, 0 failed"
