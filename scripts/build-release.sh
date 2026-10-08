#!/usr/bin/env bash
# Build tbound release artifacts into a dist directory.
#
# Produces, for each target:
#   tbound-<version>-<os>-<arch>.tar.gz   containing bin/tbound, bin/tbound-doctor, runtime/, share/completions/
# plus a SHA256SUMS file covering every tarball. Artifacts are staged in a private
# work directory and only published to <outdir> after every target builds, so a
# failed target never leaves partial tarballs or a stale SHA256SUMS.
#
# Usage:
#   scripts/build-release.sh [version] [outdir] [goos/goarch ...]
#
# Environment:
#   GO   Go toolchain to use (default: go)
#
# Note: linux/arm64 is intentionally excluded from the default set. The tbound
# runtime currently uses a linuxSysRenameat2 helper implemented only for amd64,
# so TARGETS = linux/arm64 fails to build and must be fixed before it is advertised.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"; [ "$#" -gt 0 ] && shift
OUT="${1:-$ROOT/dist}"; [ "$#" -gt 0 ] && shift
TARGETS=("$@")
if [ "${#TARGETS[@]}" -eq 0 ]; then
  TARGETS=("linux/amd64" "darwin/amd64" "darwin/arm64" "windows/amd64")
fi
GO="${GO:-go}"

case "$VERSION" in
  *[!A-Za-z0-9._-]*|"") echo "build-release: invalid version '$VERSION'" >&2; exit 1 ;;
esac

if ! command -v "$GO" >/dev/null 2>&1; then
  echo "build-release: Go toolchain '$GO' not found on PATH" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-release.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
STAGE_OUT="$WORK/out"
mkdir -p "$STAGE_OUT"

# Runtime bundle shared by every platform (source + pinned dependency manifest).
RUNTIME="$WORK/runtime"
mkdir -p "$RUNTIME"
cp "$ROOT/adapter/package.json" "$RUNTIME/package.json"
[ -f "$ROOT/adapter/package-lock.json" ] && cp "$ROOT/adapter/package-lock.json" "$RUNTIME/package-lock.json"
[ -f "$ROOT/adapter/tsconfig.json" ] && cp "$ROOT/adapter/tsconfig.json" "$RUNTIME/tsconfig.json"
mkdir -p "$RUNTIME/src"
cp "$ROOT"/adapter/src/*.ts "$RUNTIME/src/"

for target in "${TARGETS[@]}"; do
  os="${target%%/*}"
  arch="${target##*/}"
  bin="tbound"; exe="tbound-doctor"
  [ "$os" = "windows" ] && bin="tbound.exe" && exe="tbound-doctor.exe"
  stage="$WORK/stage-$os-$arch"
  rm -rf "$stage"; mkdir -p "$stage/bin"
  echo "build-release: building $os/$arch"
  ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -o "$stage/bin/$bin" ./cmd/tbound )
  ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -o "$stage/bin/$exe" ./cmd/tbound-doctor )
  cp -R "$RUNTIME" "$stage/runtime"
  if [ -d "$ROOT/install/completions" ]; then
    mkdir -p "$stage/share/completions"
    cp "$ROOT"/install/completions/* "$stage/share/completions/"
  fi
  printf '%s\n' "$VERSION" > "$stage/VERSION"
  tar -C "$stage" -czf "$STAGE_OUT/tbound-$VERSION-$os-$arch.tar.gz" .
  echo "build-release: staged tbound-$VERSION-$os-$arch.tar.gz"
done

# All targets built. Checksum in staging, then update the destination.
( cd "$STAGE_OUT" && { sha256sum tbound-"$VERSION"-*.tar.gz > SHA256SUMS 2>/dev/null || shasum -a 256 tbound-"$VERSION"-*.tar.gz > SHA256SUMS; } )
mkdir -p "$OUT"
rm -f "$OUT"/tbound-"$VERSION"-*.tar.gz "$OUT/SHA256SUMS"
mv "$STAGE_OUT"/tbound-"$VERSION"-*.tar.gz "$STAGE_OUT/SHA256SUMS" "$OUT/"
echo "build-release: wrote $OUT/SHA256SUMS"
cat "$OUT/SHA256SUMS"
