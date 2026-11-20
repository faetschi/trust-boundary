#!/usr/bin/env bash
# Build tbound release artifacts into a dist directory.
#
# TBound releases target linux/amd64 only. WSL 2 and Linux VMs run the same
# build, so no macOS or Windows binaries are made. It produces:
#   tbound-<version>-linux-amd64.tar.gz
#     bin/tbound, bin/tbound-doctor, bin/tbound-profile
#     libexec/tbound/tbound-cell-entrypoint, libexec/tbound/cell-entrypoint.json
#     runtime/, share/completions/, LICENSE, MANIFEST.sha256 (except self), VERSION
# plus a SHA256SUMS file and a separate cell-entrypoint metadata + checksum
# artifact. Artifacts are staged in a private work directory and only published
# to <outdir> after the build succeeds, so a failed build never leaves partial
# tarballs or a stale SHA256SUMS.
#
# tbound-cell-entrypoint is the unsigned Linux/amd64 binary intended for a
# frozen in-image D09 TCB entrypoint. Its Landlock syscall numbers and the
# runtime's linuxSysRenameat2 helper are amd64-specific, so linux/arm64 is
# intentionally not built or advertised. This script never signs it, generates
# keys, or invokes it; signing/profile admission remain operator-controlled.
#
# Usage:
#   scripts/build-release.sh [version] [outdir] [linux/amd64]
#
# Environment:
#   GO   Go toolchain to use (default: go)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-dev}"; [ "$#" -gt 0 ] && shift
OUT="${1:-$ROOT/dist}"; [ "$#" -gt 0 ] && shift
TARGETS=("$@")
if [ "${#TARGETS[@]}" -eq 0 ]; then
  TARGETS=("linux/amd64")
fi
GO="${GO:-go}"

case "$VERSION" in
  *[!A-Za-z0-9._-]*|"") echo "build-release: invalid version '$VERSION'" >&2; exit 1 ;;
esac

for target in "${TARGETS[@]}"; do
  case "$target" in
    linux/amd64) ;;
    linux/arm64) echo "build-release: linux/arm64 is not supported/advertised (amd64-only runtime and TCB syscalls)" >&2; exit 1 ;;
    *) echo "build-release: unsupported target '$target' (releases are linux/amd64 only; use WSL 2 or a Linux VM elsewhere)" >&2; exit 1 ;;
  esac
done

if ! command -v "$GO" >/dev/null 2>&1; then
  echo "build-release: Go toolchain '$GO' not found on PATH" >&2
  exit 1
fi
[ -f "$ROOT/LICENSE" ] || { echo "build-release: missing canonical LICENSE file" >&2; exit 1; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}';
  else echo "build-release: need sha256sum or shasum to hash artifacts" >&2; exit 1; fi
}

entrypoint_source_digest() {
  local manifest="$1"
  (
    cd "$ROOT"
    local -a hash_command
    if command -v sha256sum >/dev/null 2>&1; then
      hash_command=(sha256sum)
    elif command -v shasum >/dev/null 2>&1; then
      hash_command=(shasum -a 256)
    else
      echo "build-release: need sha256sum or shasum for entrypoint provenance" >&2
      exit 1
    fi
    find supervisor/cmd/tbound-cell-entrypoint supervisor/internal/piruntime \
      -type f -name '*.go' ! -name '*_test.go' -print \
      | LC_ALL=C sort \
      | while IFS= read -r source; do "${hash_command[@]}" "$source"; done
    "${hash_command[@]}" supervisor/go.mod supervisor/go.sum
  ) > "$manifest"
  sha256_of "$manifest"
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-release.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
STAGE_OUT="$WORK/out"
mkdir -p "$STAGE_OUT"

# Runtime bundle shared by every platform (source + pinned dependency manifest).
RUNTIME="$WORK/runtime"
mkdir -p "$RUNTIME"
cp "$ROOT/adapter/pi-runtime-package.json" "$RUNTIME/package.json"
mkdir -p "$RUNTIME/src"
for runtime_source in tbound-cli-extension.ts broker-provider.ts proxy-tools.ts ipc-transport.ts result-hints.ts; do
  cp "$ROOT/adapter/src/$runtime_source" "$RUNTIME/src/$runtime_source"
done

ENTRY_SHA=""
ENTRY_TARGET=""
ENTRY_SOURCE_SHA=""
ENTRY_TEMPLATE_SOURCE_SHA=""
ENTRY_COMMIT="unknown"
ENTRY_GO_VERSION="unknown"
ENTRY_DIRTY=null
for target in "${TARGETS[@]}"; do
  os="${target%%/*}"
  arch="${target##*/}"
  bin="tbound"; exe="tbound-doctor"; prof="tbound-profile"
  stage="$WORK/stage-$os-$arch"
  rm -rf "$stage"; mkdir -p "$stage/bin"
  echo "build-release: building $os/$arch"
  ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-X main.version=$VERSION" -o "$stage/bin/$bin" ./cmd/tbound )
  ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -o "$stage/bin/$exe" ./cmd/tbound-doctor )
  ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath -o "$stage/bin/$prof" ./cmd/tbound-profile )
  if [ "$os" = "linux" ] && [ "$arch" = "amd64" ]; then
    mkdir -p "$stage/libexec/tbound"
    ( cd "$ROOT/supervisor" && GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 "$GO" build -trimpath \
        -o "$stage/libexec/tbound/tbound-cell-entrypoint" ./cmd/tbound-cell-entrypoint )
    ENTRY_SHA="$(sha256_of "$stage/libexec/tbound/tbound-cell-entrypoint")"
    ENTRY_SOURCE_SHA="$(entrypoint_source_digest "$WORK/entrypoint-source.sha256")"
    ENTRY_TEMPLATE_SOURCE_SHA="$(sha256_of "$ROOT/supervisor/internal/piruntime/d09_template_linux.go")"
    ENTRY_GO_VERSION="$("$GO" version | tr '\n' ' ')"
    if git -C "$ROOT" rev-parse --verify HEAD >/dev/null 2>&1; then
      ENTRY_COMMIT="$(git -C "$ROOT" rev-parse --verify HEAD)"
      if [ -n "$(git -C "$ROOT" status --porcelain -- supervisor/cmd/tbound-cell-entrypoint supervisor/internal/piruntime supervisor/go.mod supervisor/go.sum)" ]; then
        ENTRY_DIRTY=true
      else
        ENTRY_DIRTY=false
      fi
    fi
    ENTRY_TARGET="$os-$arch"
    cat > "$stage/libexec/tbound/cell-entrypoint.json" <<JSON
{
  "schema_version": "tbound-cell-entrypoint-metadata/v1",
  "name": "tbound-cell-entrypoint",
  "version": "$VERSION",
  "os": "linux",
  "arch": "amd64",
  "tarball": "tbound-$VERSION-linux-amd64.tar.gz",
  "path": "libexec/tbound/tbound-cell-entrypoint",
  "sha256": "sha256:$ENTRY_SHA",
  "source_commit": "$ENTRY_COMMIT",
  "source_dirty": $ENTRY_DIRTY,
  "source_tree_sha256": "sha256:$ENTRY_SOURCE_SHA",
  "entrypoint_source": "supervisor/cmd/tbound-cell-entrypoint/main_linux.go",
  "entrypoint_source_sha256": "sha256:$(sha256_of "$ROOT/supervisor/cmd/tbound-cell-entrypoint/main_linux.go")",
  "d09_template_source": "supervisor/internal/piruntime/d09_template_linux.go",
  "d09_template_source_sha256": "sha256:$ENTRY_TEMPLATE_SOURCE_SHA",
  "registered_d09_profile_digest": null,
  "profile_note": "no measured or signed D09 profile is generated by this build",
  "go_toolchain": "$ENTRY_GO_VERSION",
  "build_flags": ["CGO_ENABLED=0", "-trimpath"],
  "license": "MIT",
  "license_source": "LICENSE",
  "license_text_included": true,
  "license_sha256": "sha256:$(sha256_of "$ROOT/LICENSE")",
  "source": "supervisor/cmd/tbound-cell-entrypoint",
  "role": "unsigned binary intended for a governed D09 in-image TCB entrypoint",
  "signed": false,
  "signing_note": "not signed by this build; incorporate into the reviewed image and sign/verify through the approved offline operator workflow; no automatic keys",
  "install_note": "package copies are unsigned distribution artifacts; only the separately admitted root-owned read-only image/profile is eligible for governed use"
}
JSON
  fi
  cp -R "$RUNTIME" "$stage/runtime"
  cp "$ROOT/LICENSE" "$stage/LICENSE"
  if [ -d "$ROOT/install/completions" ]; then
    mkdir -p "$stage/share/completions"
    cp "$ROOT"/install/completions/* "$stage/share/completions/"
  fi
  printf '%s\n' "$VERSION" > "$stage/VERSION"
  if find "$stage" ! -type d ! -type f -print -quit | grep -q .; then
    echo "build-release: refusing non-regular artifact object under $stage" >&2
    exit 1
  fi
  # Verified tarball manifest: every shipped file with its sha256, relative to the
  # extraction root, so `sha256sum -c MANIFEST.sha256` works after extraction.
  ( cd "$stage" && find . -type f ! -name MANIFEST.sha256 | LC_ALL=C sort | while IFS= read -r rel; do
      printf '%s  %s\n' "$(sha256_of "$rel")" "$rel"
    done ) > "$stage/MANIFEST.sha256"
  tar -C "$stage" -czf "$STAGE_OUT/tbound-$VERSION-$os-$arch.tar.gz" .
  echo "build-release: staged tbound-$VERSION-$os-$arch.tar.gz"
done

# All targets built. Stage checksum inputs, then update the destination.
# Separate metadata + checksum release artifacts for the linux/amd64 TCB
# entrypoint. The metadata and checksum files themselves are included in
# SHA256SUMS; the checksum names the binary within the extracted tarball.
ENTRY_META_BASE=""
if [ -n "$ENTRY_SHA" ]; then
  ENTRY_META_BASE="tbound-cell-entrypoint-$VERSION-$ENTRY_TARGET"
  cp "$WORK/stage-$ENTRY_TARGET/libexec/tbound/cell-entrypoint.json" "$STAGE_OUT/$ENTRY_META_BASE.metadata.json"
  printf '%s  %s\n' "$ENTRY_SHA" "libexec/tbound/tbound-cell-entrypoint" > "$STAGE_OUT/$ENTRY_META_BASE.sha256"
fi

CHECKSUM_INPUTS=("$STAGE_OUT"/tbound-"$VERSION"-*.tar.gz)
if [ -n "$ENTRY_META_BASE" ]; then
  CHECKSUM_INPUTS+=("$STAGE_OUT/$ENTRY_META_BASE.metadata.json" "$STAGE_OUT/$ENTRY_META_BASE.sha256")
fi
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$STAGE_OUT" && sha256sum "${CHECKSUM_INPUTS[@]##*/}" > SHA256SUMS )
else
  ( cd "$STAGE_OUT" && shasum -a 256 "${CHECKSUM_INPUTS[@]##*/}" > SHA256SUMS )
fi

mkdir -p "$OUT"
rm -f "$OUT"/tbound-"$VERSION"-*.tar.gz "$OUT/SHA256SUMS" \
      "$OUT"/tbound-cell-entrypoint-"$VERSION"-linux-amd64.metadata.json \
      "$OUT"/tbound-cell-entrypoint-"$VERSION"-linux-amd64.sha256
mv "$STAGE_OUT"/tbound-"$VERSION"-*.tar.gz "$STAGE_OUT/SHA256SUMS" "$OUT/"
if [ -n "$ENTRY_META_BASE" ]; then
  mv "$STAGE_OUT/$ENTRY_META_BASE.metadata.json" "$STAGE_OUT/$ENTRY_META_BASE.sha256" "$OUT/"
  echo "build-release: wrote $OUT/$ENTRY_META_BASE.metadata.json and .sha256"
fi
echo "build-release: wrote $OUT/SHA256SUMS"
cat "$OUT/SHA256SUMS"
