#!/usr/bin/env bash
# Build TBound release artefacts: a static binary, a versioned tarball with
# SHA256SUMS, and (when nfpm is available) .deb/.rpm packages.
#
# No network access is performed. Signing the packages is an operator step.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/.." && pwd)"
version="${VERSION:-$(git -C "$repo" describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)}"
out="${OUTDIR:-$repo/dist}"
stage="$out/tbound-$version"

if ! command -v go >/dev/null 2>&1; then
  echo "go toolchain not found on PATH" >&2
  exit 1
fi
if git -C "$repo" diff --quiet --ignore-submodules 2>/dev/null; then :; else
  echo "note: working tree is dirty; version='$version'" >&2
fi

rm -rf "$stage"
mkdir -p "$stage/bin" "$stage/systemd"

echo "building static tbound $version"
( cd "$repo/supervisor" && CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH:-amd64}" \
    go build -trimpath -ldflags "-s -w" -o "$stage/bin/tbound" ./cmd/tbound )

install -m 0755 "$here/systemd/tbound.service" "$stage/systemd/tbound.service"
install -m 0644 "$here/systemd/tbound-user.service" "$stage/systemd/tbound-user.service"
install -m 0755 "$here/install.sh" "$stage/install.sh"
install -m 0755 "$here/uninstall.sh" "$stage/uninstall.sh"
install -m 0644 "$repo/docs/install.md" "$stage/INSTALL.md"
if [ -f "$repo/LICENSE" ]; then install -m 0644 "$repo/LICENSE" "$stage/LICENSE"; fi

tarball="$out/tbound-$version-linux-${GOARCH:-amd64}.tar.gz"
tar -C "$out" -czf "$tarball" "$(basename "$stage")"
( cd "$out" && sha256sum "$(basename "$tarball")" > SHA256SUMS )
echo "wrote $tarball"

if command -v nfpm >/dev/null 2>&1; then
  echo "building deb/rpm with nfpm"
  VERSION="$version" nfpm pkg --config "$here/nfpm.yaml" --packager deb --target "$out/"
  VERSION="$version" nfpm pkg --config "$here/nfpm.yaml" --packager rpm --target "$out/"
  ( cd "$out" && sha256sum ./*.deb ./*.rpm >> SHA256SUMS 2>/dev/null || true )
else
  echo "nfpm not found: skipping .deb/.rpm (install nfpm to build them)" >&2
fi

echo "artefacts in $out"
